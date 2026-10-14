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
	limits Limits
	recipe Recipe
}

// New validates all ceilings and copies the recipe's closed environment.
func New(l Limits, r Recipe) (*Supervisor, error) {
	if err := l.Validate(); err != nil {
		return nil, fmt.Errorf("construct supervisor: %w", err)
	}
	if !filepath.IsAbs(r.Executable) {
		return nil, fmt.Errorf("child executable must be absolute")
	}
	info, err := os.Stat(r.Executable)
	if err != nil {
		return nil, fmt.Errorf("stat child executable: %w", err)
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
	return &Supervisor{limits: l, recipe: r}, nil
}

// Attempt names a screened ledger key and a read-only mounted input directory.
// InputDir is local worker configuration, not a transport envelope or URL.
type Attempt struct {
	Key       string
	InputDir  string
	InputType OutputType
}

// Receipt is local staging evidence, not quality acceptance or persistence.
// Envelope.Manifest hashes the canonical Outputs array, including every output
// digest. Close removes private staging after the adapter has retained outputs.
type Receipt struct {
	Envelope model.ConversionEnvelope
	Outputs  []model.AssetRef
	Finding  *Failure
	Complete bool
	root     string
	outputs  []model.AssetRef
}

// Close removes this attempt's private directory. It is safe to call repeatedly.
func (r *Receipt) Close() error {
	if r.root == "" {
		return nil
	}
	if err := os.RemoveAll(r.root); err != nil {
		return fmt.Errorf("remove attempt scratch: %w", err)
	}
	r.root = ""
	return nil
}

// Open gives the persistence adapter a validated output, never an arbitrary path.
func (r *Receipt) Open(index int) (*os.File, error) {
	if !r.Complete || r.root == "" || index < 0 || index >= len(r.outputs) {
		return nil, fmt.Errorf("output unavailable")
	}
	root, err := os.OpenRoot(filepath.Join(r.root, "output"))
	if err != nil {
		return nil, fmt.Errorf("open retained output root: %w", err)
	}
	defer root.Close()
	f, err := root.Open(r.outputs[index].Path)
	if err != nil {
		return nil, fmt.Errorf("open retained output: %w", err)
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
	Outputs []model.AssetRef
	Failure *Failure
}

// Run kills the complete group on any terminal cause and waits for the leader.
// A successful leader cannot leave background writers alive during validation.
func (s *Supervisor) Run(ctx context.Context, a Attempt) (Receipt, error) {
	var receipt Receipt
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
		return receipt, fmt.Errorf("create private scratch: %w", err)
	}
	receipt.root = dir
	for _, name := range []string{"output", "temp"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			_ = receipt.Close()
			return receipt, fmt.Errorf("create attempt directory: %w", err)
		}
	}
	req := ChildRequest{Phase: executePhase, InputDir: a.InputDir, InputType: a.InputType,
		OutputDir: filepath.Join(dir, "output"), ScratchDir: filepath.Join(dir, "temp"), Limits: s.limits}
	var cpu time.Duration
	response, used, err := s.runChild(deadline, req, dir, s.limits.CPUTime)
	cpu += used
	if err != nil {
		return failReceipt(receipt, err)
	}
	if response.Failure != nil {
		return failReceipt(receipt, response.Failure)
	}
	req.Phase = validatePhase
	response, _, err = s.runChild(deadline, req, dir, s.limits.CPUTime-cpu)
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
		err = failure
	}
	r.Finding = failure
	_ = r.Close()
	return r, fmt.Errorf("attempt failed: %w", err)
}

func contextFailure(err error) *Failure {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Failure{Cause: Timeout}
	}
	return &Failure{Cause: Cancelled}
}

func (s *Supervisor) runChild(ctx context.Context, req ChildRequest, dir string, cpuBudget time.Duration) (childResponse, time.Duration, error) {
	var response childResponse
	if cpuBudget <= 0 {
		return response, 0, exceeded(BoundCPU)
	}
	protocol, err := json.Marshal(req)
	if err != nil {
		return response, 0, fmt.Errorf("encode child protocol: %w", err)
	}
	cmd := exec.Command(s.recipe.Executable, "--sandbox-child")
	cmd.Dir = req.ScratchDir
	cmd.Env = []string{}
	for _, v := range s.recipe.Environment {
		cmd.Env = append(cmd.Env, v.Name+"="+v.Value)
	}
	cmd.Stdin = bytes.NewReader(protocol)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	// Neither child output nor diagnostics enter logs. The response is capped
	// independently, even when a recipe has a large structured-output bound.
	overflow := make(chan struct{}, 1)
	stdout := &boundedResponse{max: min(s.limits.OutputBytes, int64(1<<20)), overflow: overflow}
	stderr := &boundedResponse{max: s.limits.OutputBytes, overflow: overflow, discard: true}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		return response, 0, fmt.Errorf("start sandbox child: %w", err)
	}
	pid := cmd.Process.Pid
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var terminal error
	var cpu time.Duration
	var waitErr error
	finished := false
	for !finished {
		select {
		case waitErr = <-waited:
			finished = true
		case <-ctx.Done():
			terminal = contextFailure(ctx.Err())
		case <-overflow:
			terminal = exceeded(BoundOutput)
		case <-ticker.C:
			used, memory, err := groupUsage(pid)
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
		if terminal != nil && !finished {
			if err := killGroup(pid); err != nil {
				terminal = fmt.Errorf("terminate group: %w", err)
			}
			waitErr = <-waited
			finished = true
		}
	}
	// Kill descendants even after a successful leader exit, before any file read.
	if err := killGroup(pid); err != nil {
		return response, cpu, fmt.Errorf("terminate remaining group: %w", err)
	}
	if err := waitGroupExit(pid); err != nil {
		return response, cpu, fmt.Errorf("wait for terminated group: %w", err)
	}
	if cmd.ProcessState != nil {
		cpu = max(cpu, cmd.ProcessState.UserTime()+cmd.ProcessState.SystemTime())
		if usage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok && usage.Maxrss > s.limits.MemoryBytes/1024 && terminal == nil {
			terminal = exceeded(BoundMemory)
		}
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

func killGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

// Zombies have already exited and retain no executing threads or writable file
// descriptors. Their reaping belongs to the container init, not this library.
func waitGroupExit(group int) error {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		live, err := liveGroup(group)
		if err != nil {
			return err
		}
		if !live {
			return nil
		}
		select {
		case <-timer.C:
			return fmt.Errorf("terminated group still executing")
		case <-tick.C:
		}
	}
}

func liveGroup(group int) (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			continue
		}
		if err != nil {
			return false, err
		}
		end := bytes.LastIndexByte(data, ')')
		if end < 0 {
			return false, fmt.Errorf("invalid process accounting record")
		}
		fields := strings.Fields(string(data[end+1:]))
		if len(fields) < 3 {
			return false, fmt.Errorf("short process accounting record")
		}
		pgid, err := strconv.Atoi(fields[2])
		if err != nil {
			return false, fmt.Errorf("invalid process group")
		}
		if pgid == group && fields[0] != "Z" && fields[0] != "X" {
			return true, nil
		}
	}
	return false, nil
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
