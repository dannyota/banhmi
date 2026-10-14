//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
)

// Variable is a declared inert locale setting. Arbitrary variable names and
// values, including URLs, socket paths and credentials, are not admitted.
type Variable struct {
	Name  string
	Value string
}

// Recipe selects a trusted child-protocol executable, never a shell command.
// It has no caller-supplied arguments, descriptors, clients, or secret settings.
type Recipe struct {
	Executable  string
	Environment []Variable
}

// Supervisor runs one attempt including a fresh bounded validation child.
type Supervisor struct {
	limits   Limits
	recipe   Recipe
	launcher Launcher
}

// New validates all ceilings and copies the recipe's closed environment.
func New(l Limits, r Recipe, launcher Launcher) (*Supervisor, error) {
	if err := l.Validate(); err != nil {
		return nil, fmt.Errorf("construct supervisor: %w", err)
	}
	if launcher == nil {
		return nil, fmt.Errorf("construct supervisor: LAUNCHER_REQUIRED")
	}
	if !filepath.IsAbs(r.Executable) {
		return nil, fmt.Errorf("child executable must be absolute")
	}
	info, err := os.Stat(r.Executable)
	if err != nil {
		return nil, fmt.Errorf("child executable: UNAVAILABLE")
	}
	if !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return nil, fmt.Errorf("child executable must be executable and regular")
	}
	seen := map[string]bool{}
	for _, v := range r.Environment {
		if seen[v.Name] {
			return nil, fmt.Errorf("duplicate declared environment variable")
		}
		seen[v.Name] = true
		switch v.Name {
		case "LANG", "LC_ALL":
			if v.Value != "C" && v.Value != "C.UTF-8" && v.Value != "vi_VN.UTF-8" {
				return nil, fmt.Errorf("unsupported locale")
			}
		case "TZ":
			if v.Value != "UTC" && v.Value != "Asia/Ho_Chi_Minh" {
				return nil, fmt.Errorf("unsupported timezone")
			}
		default:
			return nil, fmt.Errorf("environment variable outside recipe vocabulary")
		}
	}
	r.Environment = append([]Variable(nil), r.Environment...)
	return &Supervisor{limits: l, recipe: r, launcher: launcher}, nil
}

// Attempt names a screened ledger key and a read-only mounted input directory.
// InputDir is local worker configuration, not a transport envelope or URL.
type Attempt struct {
	Key       string
	InputDir  string
	InputType OutputType
}

// RetentionReason is a closed vocabulary. CleanupReference is random hex and
// names corpus-attempt-<reference> in the configured runtime and delegation.
// No document name, process ID or host path enters this local evidence.
type RetentionReason string

const (
	ContainerRetained     RetentionReason = "CONTAINER_RETAINED"
	ParentRetained        RetentionReason = "PARENT_RETAINED"
	AccountingUnconfirmed RetentionReason = "ACCOUNTING_UNCONFIRMED"
)

type RetentionState struct {
	CleanupReference string
	Reasons          []RetentionReason
}

// Receipt is local staging evidence, not quality acceptance or persistence.
// Envelope.Manifest hashes the canonical Outputs array, including every output
// digest. Close removes private staging after the adapter has retained outputs.
type Receipt struct {
	Envelope    model.ConversionEnvelope
	Outputs     []model.AssetRef
	Finding     *Failure
	Complete    bool
	Retentions  []RetentionState
	root        string
	outputs     []model.AssetRef
	retained    []*os.File
	termination []*terminationReport
}

// Close removes this attempt's private directory. It is safe to call repeatedly.
func (r *Receipt) Close() error {
	for _, f := range r.retained {
		_ = f.Close()
	}
	r.retained = nil
	if r.root == "" {
		return nil
	}
	if err := os.RemoveAll(r.root); err != nil {
		return fmt.Errorf("remove attempt scratch: FAILED")
	}
	r.root = ""
	return nil
}

// Open gives the persistence adapter a validated output, never an arbitrary path.
func (r *Receipt) Open(index int) (*os.File, error) {
	if !r.Complete || r.root == "" || index < 0 || index >= len(r.outputs) {
		return nil, fmt.Errorf("output unavailable")
	}
	if index >= len(r.retained) {
		return nil, fmt.Errorf("output unavailable")
	}
	// procfs reopens the pinned inode with an independent read offset. No
	// document-provided pathname is resolved after the receipt is sealed.
	f, err := os.Open("/proc/self/fd/" + strconv.FormatUint(uint64(r.retained[index].Fd()), 10))
	if err != nil {
		return nil, fmt.Errorf("open retained output: UNAVAILABLE")
	}
	return f, nil
}

// ManifestBytes returns the exact local bytes named by Envelope.Manifest.
// A persistence adapter stores these alongside the hashed outputs before apply.
func (r *Receipt) ManifestBytes() ([]byte, error) {
	if !r.Complete || r.root == "" {
		return nil, fmt.Errorf("manifest unavailable")
	}
	data, err := json.Marshal(r.outputs)
	if err != nil {
		return nil, fmt.Errorf("encode output manifest: %w", err)
	}
	return data, nil
}

type phase string

const (
	executePhase  phase = "execute"
	validatePhase phase = "validate"
)

// ChildRequest is a local stdin-only protocol. No connection configuration or
// raw document bytes are passed in arguments, environment or control-plane logs.
type ChildRequest struct {
	Phase      phase
	InputDir   string
	OutputDir  string
	ScratchDir string
	InputType  OutputType
	Limits     Limits
}

type childResponse struct {
	Outputs     []model.AssetRef
	Failure     *Failure
	Termination *terminationReport `json:"-"`
}

// Run terminates the complete process tree on any cause and waits for the leader.
// A successful leader cannot leave background writers alive during validation.
func (s *Supervisor) Run(ctx context.Context, a Attempt) (receipt Receipt, resultErr error) {
	defer func() {
		for _, report := range receipt.termination {
			if report != nil && len(report.Retention.Reasons) > 0 {
				state := report.Retention
				state.Reasons = append([]RetentionReason(nil), state.Reasons...)
				receipt.Retentions = append(receipt.Retentions, state)
			}
		}
	}()
	defer func() { resultErr = publicError("run attempt", resultErr) }()
	if err := model.ValidateEnvelopeKey(a.Key, "conversion"); err != nil {
		return receipt, fmt.Errorf("attempt key: %w", err)
	}
	if !filepath.IsAbs(a.InputDir) || !a.InputType.valid() {
		return receipt, fmt.Errorf("attempt requires local input root and closed type")
	}
	if err := ctx.Err(); err != nil {
		return failReceipt(receipt, &Failure{Cause: Cancelled})
	}
	deadline, cancel := context.WithTimeout(ctx, s.limits.WallTime)
	defer cancel()
	if err := checkTree(deadline, a.InputDir, s.limits, true); err != nil {
		return failReceipt(receipt, err)
	}
	dir, err := os.MkdirTemp("", "corpus-attempt-")
	if err != nil {
		return receipt, fmt.Errorf("create private scratch: FAILED")
	}
	receipt.root = dir
	for _, name := range []string{"output", "temp"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			_ = receipt.Close()
			return receipt, fmt.Errorf("create attempt directory: FAILED")
		}
	}
	req := ChildRequest{Phase: executePhase, InputDir: a.InputDir, InputType: a.InputType,
		OutputDir: filepath.Join(dir, "output"), ScratchDir: filepath.Join(dir, "temp"), Limits: s.limits}
	var cpu time.Duration
	response, used, err := s.runChild(deadline, req, dir, s.limits.CPUTime)
	cpu += used
	receipt.termination = append(receipt.termination, response.Termination)
	if err != nil {
		return failReceipt(receipt, err)
	}
	if response.Failure != nil {
		return failReceipt(receipt, response.Failure)
	}
	req.Phase = validatePhase
	response, _, err = s.runChild(deadline, req, dir, s.limits.CPUTime-cpu)
	receipt.termination = append(receipt.termination, response.Termination)
	if err != nil {
		return failReceipt(receipt, err)
	}
	if response.Failure != nil {
		return failReceipt(receipt, response.Failure)
	}
	if err := deadline.Err(); err != nil {
		return failReceipt(receipt, contextFailure(err))
	}
	if len(response.Outputs) == 0 || len(response.Outputs) > s.limits.TempFiles {
		return failReceipt(receipt, &Failure{Cause: UnsafeOutput})
	}
	var total int64
	for _, ref := range response.Outputs {
		if err := ref.Validate(); err != nil {
			return failReceipt(receipt, &Failure{Cause: UnsafeOutput})
		}
		if ref.ByteSize > s.limits.OutputBytes-total {
			return failReceipt(receipt, exceeded(BoundOutput))
		}
		total += ref.ByteSize
	}
	// Pin the exact validated inodes through receipt sealing and persistence.
	// Rehash through the pinned descriptors; replacement cannot change Open.
	root, err := os.Open(req.OutputDir)
	if err != nil {
		return failReceipt(receipt, &Failure{Cause: UnsafeOutput})
	}
	defer root.Close()
	for _, ref := range response.Outputs {
		info, err := os.Lstat(filepath.Join(req.OutputDir, ref.Path))
		if err != nil || info.Size() != ref.ByteSize {
			return failReceipt(receipt, &Failure{Cause: UnsafeOutput})
		}
		f, err := openChecked(root, ref.Path, info)
		if err != nil {
			return failReceipt(receipt, err)
		}
		receipt.retained = append(receipt.retained, f)
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(f, ref.ByteSize+1))
		if err != nil || n != ref.ByteSize || model.Digest(hex.EncodeToString(h.Sum(nil))) != ref.SHA256 {
			return failReceipt(receipt, &Failure{Cause: UnsafeOutput})
		}
	}
	if err := deadline.Err(); err != nil {
		return failReceipt(receipt, contextFailure(err))
	}
	encoded, err := json.Marshal(response.Outputs)
	if err != nil {
		return failReceipt(receipt, fmt.Errorf("serialize screened outputs: %w", err))
	}
	hash := sha256.Sum256(encoded)
	receipt.Envelope = model.ConversionEnvelope{Key: a.Key, Manifest: model.Digest(hex.EncodeToString(hash[:]))}
	receipt.Outputs = response.Outputs
	receipt.outputs = append([]model.AssetRef(nil), response.Outputs...)
	receipt.Complete = true
	return receipt, nil
}

func failReceipt(r Receipt, err error) (Receipt, error) {
	var failure *Failure
	if !errors.As(err, &failure) || !failure.valid() {
		failure = &Failure{Cause: ExitFailure}
	}
	r.Finding = failure
	_ = r.Close()
	return r, fmt.Errorf("attempt failed: %w", failure)
}

// publicError discards internal diagnostics, including wrapped PathErrors.
func publicError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var failure *Failure
	if errors.As(err, &failure) && failure.valid() {
		return fmt.Errorf("%s: %w", operation, failure)
	}
	return fmt.Errorf("%s: FAILED", operation)
}

// TerminationMechanisms reports teardown methods without process IDs or paths.
func (r *Receipt) TerminationMechanisms() []string {
	var methods []string
	for _, report := range r.termination {
		if report == nil {
			continue
		}
		if report.Mechanism != "" {
			methods = append(methods, report.Mechanism)
		}
		for _, reason := range report.Retention.Reasons {
			methods = append(methods, string(reason))
		}
	}
	return methods
}

func contextFailure(err error) *Failure {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Failure{Cause: Timeout}
	}
	return &Failure{Cause: Cancelled}
}

func (s *Supervisor) runChild(ctx context.Context, req ChildRequest, dir string, cpuBudget time.Duration) (response childResponse, used time.Duration, resultErr error) {
	if cpuBudget <= 0 {
		return response, 0, exceeded(BoundCPU)
	}
	req.Limits.CPUTime = cpuBudget
	job, err := s.launcher.prepare(ctx, s.recipe, req, dir, cpuBudget)
	if err != nil {
		return response, 0, err
	}
	defer job.close()
	defer func() { response.Termination = job.report }()
	protocol, err := json.Marshal(job.request)
	if err != nil {
		return response, 0, fmt.Errorf("encode child protocol: INVALID")
	}
	cmd := job.cmd
	cmd.Stdin = bytes.NewReader(protocol)
	cmd.WaitDelay = 100 * time.Millisecond
	// Neither child output nor diagnostics enter logs. The response is capped
	// independently, even when a recipe has a large structured-output bound.
	overflow := make(chan struct{}, 1)
	stdout := &boundedResponse{max: min(s.limits.OutputBytes, int64(1<<20)), overflow: overflow}
	stderr := &boundedResponse{max: s.limits.OutputBytes, overflow: overflow, discard: true}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := ctx.Err(); err != nil {
		return response, 0, contextFailure(err)
	}
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return response, 0, contextFailure(ctx.Err())
		}
		return response, 0, fmt.Errorf("start sandbox child: FAILED")
	}
	pid := cmd.Process.Pid
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	// Container accounting exists before launch. Readiness is independent:
	// either a protocol byte or a positive child PID confirms startup.
	var readiness <-chan struct{}
	startupCtx := ctx
	startupCancel := func() {}
	if job.ready != nil {
		startup := req.Limits.StartupTime
		if deadline, ok := ctx.Deadline(); ok {
			startup = min(startup, max(time.Until(deadline), 0))
		}
		startupCtx, startupCancel = context.WithTimeout(ctx, startup)
		defer startupCancel()
		readiness = startupCtx.Done()
	}
	var terminal error
	if job.started != nil {
		terminal = job.started()
	}
	var cpu time.Duration
	var waitErr error
	finished := false
	for !finished {
		select {
		case waitErr = <-waited:
			finished = true
			if errors.Is(waitErr, exec.ErrWaitDelay) && terminal == nil {
				terminal = &Failure{Cause: Timeout}
			}
		case <-ctx.Done():
			terminal = contextFailure(ctx.Err())
		case <-readiness:
			terminal = &Failure{Cause: StartupTimeout}
			if ctx.Err() != nil {
				terminal = contextFailure(ctx.Err())
			}
			readiness = nil
		case <-overflow:
			terminal = exceeded(BoundOutput)
		case <-ticker.C:
			if readiness != nil && (stdout.count() > 0 || job.ready(startupCtx)) {
				if startupCtx.Err() != nil {
					terminal = &Failure{Cause: StartupTimeout}
					if ctx.Err() != nil {
						terminal = contextFailure(ctx.Err())
					}
				}
				startupCancel()
				readiness = nil
			}
			usage := job.usage
			if usage == nil {
				usage = func() (time.Duration, int64, error) { return groupUsage(pid) }
			}
			used, memory, err := usage()
			if err != nil {
				terminal = fmt.Errorf("account child group: %w", err)
			} else {
				cpu = max(cpu, used)
				if cpu > cpuBudget {
					terminal = exceeded(BoundCPU)
				} else if memory > s.limits.MemoryBytes {
					terminal = exceeded(BoundMemory)
				}
			}
			if terminal == nil {
				terminal = s.checkOutputUsage(ctx, dir, stdout, stderr)
			}
		}
		if job.startupFailure != nil && terminal == nil {
			terminal = job.startupFailure()
		}
		if terminal != nil && !finished {
			if job.usage != nil {
				if err := job.terminate(); err != nil {
					terminal = err
				}
			} else if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				terminal = fmt.Errorf("terminate group: %w", err)
			}
			waitErr = <-waited
			finished = true
		}
	}
	// Confirm teardown before the final accounting read. Container teardown
	// captures its final counters before removal and caches them for usage.
	if err := job.terminate(); err != nil {
		return response, cpu, err
	}
	if job.usage != nil {
		used, memory, err := job.usage()
		if err != nil && terminal == nil {
			terminal = err
		}
		cpu = max(cpu, used)
		if memory > s.limits.MemoryBytes && terminal == nil {
			terminal = exceeded(BoundMemory)
		}
	}
	if cmd.ProcessState != nil {
		cpu = max(cpu, cmd.ProcessState.UserTime()+cmd.ProcessState.SystemTime())
		if usage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok && usage.Maxrss > s.limits.MemoryBytes/1024 && terminal == nil {
			terminal = exceeded(BoundMemory)
		}
	}
	if ctx.Err() != nil {
		return response, cpu, contextFailure(ctx.Err())
	}
	if terminal != nil {
		return response, cpu, terminal
	}
	if err := ctx.Err(); err != nil {
		return response, cpu, contextFailure(err)
	}
	if cpu > cpuBudget {
		return response, cpu, exceeded(BoundCPU)
	}
	if stdout.exceeded() || stderr.exceeded() {
		return response, cpu, exceeded(BoundOutput)
	}
	if err := s.checkOutputUsage(ctx, dir, stdout, stderr); err != nil {
		return response, cpu, err
	}
	if waitErr != nil {
		return response, cpu, &Failure{Cause: ExitFailure}
	}
	d := json.NewDecoder(bytes.NewReader(stdout.bytes()))
	d.DisallowUnknownFields()
	if err := d.Decode(&response); err != nil {
		return response, cpu, &Failure{Cause: ExitFailure}
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return response, cpu, &Failure{Cause: ExitFailure}
	}
	if response.Failure != nil && !response.Failure.valid() {
		return childResponse{}, cpu, &Failure{Cause: ExitFailure}
	}
	return response, cpu, nil
}

func (s *Supervisor) checkOutputUsage(ctx context.Context, dir string, stdout, stderr *boundedResponse) error {
	l := s.limits
	for _, stream := range []*boundedResponse{stdout, stderr} {
		n := stream.count()
		if n > l.OutputBytes {
			return exceeded(BoundOutput)
		}
		l.OutputBytes -= n
	}
	return checkTree(ctx, dir, l, false)
}

type boundedResponse struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	n        int64
	max      int64
	overflow chan struct{}
	discard  bool
}

func (w *boundedResponse) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.n += int64(len(p))
	if w.n > w.max {
		select {
		case w.overflow <- struct{}{}:
		default:
		}
		return len(p), nil
	}
	if !w.discard {
		_, _ = w.buf.Write(p)
	}
	return len(p), nil
}
func (w *boundedResponse) exceeded() bool { w.mu.Lock(); defer w.mu.Unlock(); return w.n > w.max }
func (w *boundedResponse) count() int64   { w.mu.Lock(); defer w.mu.Unlock(); return w.n }
func (w *boundedResponse) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf.Bytes()...)
}

func checkTree(ctx context.Context, directory string, l Limits, input bool) error {
	rootInfo, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("stat attempt root: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return &Failure{Cause: UnsafeOutput}
	}
	var bytes, output int64
	var files int
	return filepath.WalkDir(directory, func(name string, entry fs.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return contextFailure(err)
		}
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("account scratch: %w", err)
		}
		if name == directory {
			return nil
		}
		files++
		if files > l.TempFiles {
			return exceeded(BoundFiles)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return &Failure{Cause: UnsafeOutput}
		}
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("account file: %w", err)
		}
		bound := l.ScratchBytes
		if input {
			bound = l.InputBytes
		}
		if !singleRegular(info) {
			return &Failure{Cause: UnsafeOutput}
		}
		if info.Size() > bound-bytes {
			if input {
				return exceeded(BoundInput)
			}
			return exceeded(BoundScratch)
		}
		bytes += info.Size()
		if !input {
			rel, err := filepath.Rel(directory, name)
			if err != nil {
				return fmt.Errorf("account output path: %w", err)
			}
			if strings.HasPrefix(rel, "output/") {
				if info.Size() > l.OutputBytes-output {
					return exceeded(BoundOutput)
				}
				output += info.Size()
			}
		}
		return nil
	})
}

// Linux procfs exports CPU counters in USER_HZ (100 ticks/second), not the
// kernel scheduler's CONFIG_HZ. Reads contain process metadata only.
func groupUsage(group int) (time.Duration, int64, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, 0, err
	}
	var ticks uint64
	var rss int64
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			continue
		}
		if err != nil {
			return 0, 0, err
		}
		end := bytes.LastIndexByte(data, ')')
		if end < 0 {
			return 0, 0, fmt.Errorf("invalid process accounting record")
		}
		fields := strings.Fields(string(data[end+1:]))
		if len(fields) < 22 {
			return 0, 0, fmt.Errorf("short process accounting record")
		}
		pgid, err := strconv.Atoi(fields[2])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid process group")
		}
		if pgid != group {
			continue
		}
		for _, index := range []int{11, 12, 13, 14} {
			n, err := strconv.ParseUint(fields[index], 10, 64)
			if err != nil {
				return 0, 0, fmt.Errorf("invalid CPU counter")
			}
			ticks += n
		}
		pages, err := strconv.ParseInt(fields[21], 10, 64)
		if err != nil || pages < 0 {
			return 0, 0, fmt.Errorf("invalid resident counter")
		}
		rss += pages * int64(os.Getpagesize())
	}
	return time.Duration(ticks) * 10 * time.Millisecond, rss, nil
}
