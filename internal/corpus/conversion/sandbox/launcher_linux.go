//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Launcher is a closed launch boundary. A caller must choose ContainerLauncher;
// DirectLauncher can be enabled only by this package's test-only constructor.
type Launcher interface {
	prepare(context.Context, Recipe, ChildRequest, string, time.Duration) (*launch, error)
}

// ContainerConfig pins a Podman-compatible OCI command, image and seccomp file.
// Runtime and SeccompProfile are absolute operator paths, never document fields.
// Image must identify a pinned digest. No caller argument template is accepted.
type ContainerConfig struct {
	Runtime        string
	Image          string
	SeccompProfile string
}

type ContainerLauncher struct{ config ContainerConfig }

func NewContainerLauncher(c ContainerConfig) (*ContainerLauncher, error) {
	if !filepath.IsAbs(c.Runtime) || !filepath.IsAbs(c.SeccompProfile) || !strings.Contains(c.Image, "@sha256:") || strings.HasPrefix(c.Image, "-") {
		return nil, fmt.Errorf("container configuration: INVALID")
	}
	for _, p := range []string{c.Runtime, c.SeccompProfile} {
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() || (p == c.Runtime && info.Mode()&0111 == 0) {
			return nil, fmt.Errorf("container configuration: UNAVAILABLE")
		}
	}
	return &ContainerLauncher{config: c}, nil
}

type terminationReport struct {
	Mechanism string
	Reaped    map[int]syscall.WaitStatus
}

type launch struct {
	cmd       *exec.Cmd
	request   ChildRequest
	terminate func() error
	started   func() error
	usage     func() (time.Duration, int64, error)
	close     func()
	report    *terminationReport
}

func (l *ContainerLauncher) prepare(ctx context.Context, r Recipe, req ChildRequest, dir string, cpu time.Duration) (*launch, error) {
	if l == nil {
		return nil, &Failure{Cause: ExitFailure}
	}
	// Recheck the binary on every launch, including fresh validation.
	if _, err := NewContainerLauncher(l.config); err != nil {
		return nil, err
	}
	name := filepath.Base(dir) + "-" + string(req.Phase)
	seconds := cpu / time.Second
	if cpu%time.Second != 0 {
		seconds++
	}
	args := []string{"run", "--rm", "--pull=never", "--log-driver=none", "--unsetenv-all", "--http-proxy=false", "--name", name, "--interactive", "--init",
		"--network=none", "--read-only", "--read-only-tmpfs=false", "--pid=private", "--userns=keep-id:uid=65532,gid=65532", "--user=65532:65532",
		"--cap-drop=ALL", "--security-opt=no-new-privileges", "--security-opt=seccomp=" + l.config.SeccompProfile,
		"--cpus=" + strconv.Itoa(req.Limits.VCPUs), "--memory=" + strconv.FormatInt(req.Limits.MemoryBytes, 10),
		"--memory-swap=" + strconv.FormatInt(req.Limits.MemoryBytes, 10), "--pids-limit=" + strconv.Itoa(req.Limits.TempFiles),
		"--ulimit=cpu=" + strconv.FormatInt(int64(seconds), 10) + ":" + strconv.FormatInt(int64(seconds), 10),
		"--mount=type=bind,src=" + req.InputDir + ",dst=/input,ro=true",
		"--mount=type=bind,src=" + dir + ",dst=/scratch,rw=true",
		"--tmpfs=/tmp:rw,noexec,nosuid,nodev,size=" + strconv.FormatInt(req.Limits.ScratchBytes, 10),
		"--workdir=/scratch/temp", "--entrypoint=/corpus-converter-child"}
	// Mount syntax cannot represent commas. Reject rather than reinterpret paths.
	if strings.ContainsAny(req.InputDir+dir, ",\n\r") {
		return nil, &Failure{Cause: UnsafeOutput}
	}
	for _, v := range r.Environment {
		args = append(args, "--env="+v.Name+"="+v.Value)
	}
	args = append(args, l.config.Image, "--sandbox-child")
	cmd := exec.CommandContext(ctx, l.config.Runtime, args...)
	cmd.Env = []string{}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	req.InputDir, req.OutputDir, req.ScratchDir = "/input", "/scratch/output", "/scratch/temp"
	job := &launch{cmd: cmd, request: req, report: &terminationReport{}, close: func() {}}
	started := time.Now()
	lastSample := started
	var cgroup string
	var lastCPU time.Duration
	var peakMemory int64
	job.usage = func() (time.Duration, int64, error) {
		if cgroup == "" {
			inspectCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			defer cancel()
			inspect := exec.CommandContext(inspectCtx, l.config.Runtime, "inspect", "--format={{.State.Pid}}", name)
			inspect.Env = []string{}
			inspect.WaitDelay = 100 * time.Millisecond
			out := &boundedResponse{max: 64, overflow: make(chan struct{}, 1)}
			inspect.Stdout = out
			if err := inspect.Run(); err == nil && !out.exceeded() {
				pid, err := strconv.Atoi(strings.TrimSpace(string(out.bytes())))
				if err == nil && pid > 0 {
					cgroup = processCgroup(pid)
				}
			}
		}
		if cgroup != "" {
			sampled := time.Now()
			cpu, memory, err := cgroupUsage(cgroup)
			if err == nil {
				lastCPU, peakMemory = max(lastCPU, cpu), max(peakMemory, memory)
				lastSample = sampled
				return lastCPU, peakMemory, nil
			}
			if !errors.Is(err, os.ErrNotExist) {
				return 0, 0, fmt.Errorf("container accounting: UNAVAILABLE")
			}
		}
		// Creation and removal can race sampling. Charge the maximum possible
		// CPU since the last successful sample rather than undercounting an
		// exited container. The cgroup quota still enforces the VCPU ceiling.
		upper := cpuUpperBound(time.Since(lastSample), req.Limits.VCPUs)
		if lastCPU > time.Duration(1<<63-1)-upper {
			return time.Duration(1<<63 - 1), peakMemory, nil
		}
		return lastCPU + upper, peakMemory, nil
	}
	var teardown sync.Mutex
	job.terminate = func() error {
		teardown.Lock()
		defer teardown.Unlock()
		// Recheck after Wait even if Cancel already ran. A runtime may finish
		// creating the container while the first cancellation check runs.
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		defer func() {
			if cmd.Process != nil {
				_ = killGroup(cmd.Process.Pid)
			}
		}()
		for {
			inspect := exec.CommandContext(stopCtx, l.config.Runtime, "container", "exists", name)
			inspect.Env = []string{}
			inspect.WaitDelay = 100 * time.Millisecond
			err := inspect.Run()
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 1 {
				job.report.Mechanism = "container-pid-namespace"
				return nil
			}
			if err != nil {
				return fmt.Errorf("container termination: UNCONFIRMED")
			}
			stop := exec.CommandContext(stopCtx, l.config.Runtime, "kill", "--signal=KILL", name)
			stop.Env = []string{}
			stop.WaitDelay = 100 * time.Millisecond
			// An exit racing kill is resolved by the next exists check.
			_ = stop.Run()
			if stopCtx.Err() != nil {
				return fmt.Errorf("container termination: UNCONFIRMED")
			}
		}
	}
	cmd.Cancel = job.terminate
	return job, nil
}

// DirectLauncher provides accounting and descendant cleanup, but no filesystem,
// UID, network, syscall or hard resource isolation. Its zero value refuses Run.
// Only the _test.go constructor can set the unexported admission option.
type DirectLauncher struct{ uncontainedForTests bool }

var directSlot = make(chan struct{}, 1)

func (l *DirectLauncher) prepare(ctx context.Context, r Recipe, req ChildRequest, dir string, cpu time.Duration) (*launch, error) {
	if l == nil || !l.uncontainedForTests {
		return nil, &Failure{Cause: ExitFailure}
	}
	select {
	case directSlot <- struct{}{}:
	case <-ctx.Done():
		return nil, contextFailure(ctx.Err())
	}
	release := func() { <-directSlot }
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		release()
		return nil, fmt.Errorf("subreaper setup: UNAVAILABLE")
	}
	baseline, err := descendantPIDs(os.Getpid(), nil)
	if err != nil {
		release()
		return nil, err
	}
	cmd := exec.CommandContext(ctx, r.Executable, "--sandbox-child")
	cmd.Dir = req.ScratchDir
	cmd.Env = []string{}
	for _, v := range r.Environment {
		cmd.Env = append(cmd.Env, v.Name+"="+v.Value)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	report := &terminationReport{Reaped: map[int]syscall.WaitStatus{}}
	cgroup := attemptCgroup(req.Limits)
	job := &launch{cmd: cmd, request: req, report: report}
	job.close = func() {
		if cgroup != "" {
			_ = os.Remove(cgroup)
		}
		release()
	}
	job.started = func() error {
		if cgroup != "" {
			if err := os.WriteFile(filepath.Join(cgroup, "cgroup.procs"), []byte(strconv.Itoa(cmd.Process.Pid)), 0600); err != nil {
				return fmt.Errorf("cgroup attachment: FAILED")
			}
		}
		return nil
	}
	// Cancellation kills the group immediately; the run loop performs the
	// full tree sweep after Wait. It never reaps the leader concurrently with Wait.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return killGroup(cmd.Process.Pid)
	}
	job.terminate = func() error {
		report.Mechanism = "subreaper-proc"
		if cgroup != "" {
			if err := os.WriteFile(filepath.Join(cgroup, "cgroup.kill"), []byte("1"), 0600); err == nil {
				report.Mechanism = "cgroup-kill+subreaper-proc"
			} else {
				report.Mechanism = "subreaper-proc-after-cgroup-kill-failure"
			}
		}
		if cmd.Process != nil {
			_ = killGroup(cmd.Process.Pid)
		}
		return terminateDescendants(baseline, cmd.Process.Pid, report)
	}
	return job, nil
}

// The caller's current cgroup must be delegated. A read-only or undelegated
// hierarchy returns the proc/subreaper fallback without requiring root.
func attemptCgroup(l Limits) string {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "0::/") {
			continue
		}
		path := strings.TrimPrefix(line, "0::")
		if filepath.Clean(path) != path || strings.Contains(path, "..") {
			return ""
		}
		parent := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(line, "0::/"))
		if _, err := os.Stat(filepath.Join(parent, "cgroup.kill")); err != nil {
			return ""
		}
		dir, err := os.MkdirTemp(parent, "corpus-test-")
		if err != nil {
			return ""
		}
		values := map[string]string{"memory.max": strconv.FormatInt(l.MemoryBytes, 10), "pids.max": strconv.Itoa(l.TempFiles), "cpu.max": strconv.Itoa(l.VCPUs*100000) + " 100000"}
		for file, value := range values {
			if err := os.WriteFile(filepath.Join(dir, file), []byte(value), 0600); err != nil {
				_ = os.Remove(dir)
				return ""
			}
		}
		return dir
	}
	return ""
}

func descendantPIDs(root int, exclude map[int]bool) (map[int]bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("descendant enumeration: UNAVAILABLE")
	}
	parents := map[int]int{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("descendant accounting: UNAVAILABLE")
		}
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 {
			return nil, fmt.Errorf("descendant accounting: INVALID")
		}
		fields := strings.Fields(string(data[end+1:]))
		if len(fields) < 2 {
			return nil, fmt.Errorf("descendant accounting: INVALID")
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("descendant accounting: INVALID")
		}
		parents[pid] = ppid
	}
	found := map[int]bool{}
	for changed := true; changed; {
		changed = false
		for pid, parent := range parents {
			if !found[pid] && !exclude[pid] && (parent == root || found[parent]) {
				found[pid] = true
				changed = true
			}
		}
	}
	return found, nil
}

func terminateDescendants(baseline map[int]bool, leader int, report *terminationReport) error {
	deadline := time.Now().Add(time.Second)
	known := map[int]bool{}
	for {
		pids, err := descendantPIDs(os.Getpid(), baseline)
		if err != nil {
			return err
		}
		for pid := range pids {
			known[pid] = true
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
				return fmt.Errorf("descendant termination: FAILED")
			}
		}
		alive := false
		for pid := range known {
			if pid != leader {
				var status syscall.WaitStatus
				if got, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil); err == nil && got == pid {
					report.Reaped[pid] = status
				}
			}
			if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
				alive = true
			}
		}
		if !alive && len(pids) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("descendant termination: UNCONFIRMED")
		}
		time.Sleep(time.Millisecond)
	}
}

// processCgroup resolves trusted host PID metadata from runtime inspection.
func processCgroup(pid int) string {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "0::/") {
			continue
		}
		path := strings.TrimPrefix(line, "0::")
		if filepath.Clean(path) != path || strings.Contains(path, "..") {
			return ""
		}
		return filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(path, "/"))
	}
	return ""
}

func cgroupUsage(dir string) (time.Duration, int64, error) {
	data, err := os.ReadFile(filepath.Join(dir, "cpu.stat"))
	if err != nil {
		return 0, 0, err
	}
	var cpu time.Duration
	found := false
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		fields := strings.Fields(string(line))
		if len(fields) == 2 && fields[0] == "usage_usec" {
			n, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil || n < 0 || n > int64((1<<63-1)/time.Microsecond) {
				return 0, 0, fmt.Errorf("cgroup CPU counter: INVALID")
			}
			cpu, found = time.Duration(n)*time.Microsecond, true
		}
	}
	if !found {
		return 0, 0, fmt.Errorf("cgroup CPU counter: MISSING")
	}
	data, err = os.ReadFile(filepath.Join(dir, "memory.peak"))
	if errors.Is(err, os.ErrNotExist) {
		data, err = os.ReadFile(filepath.Join(dir, "memory.current"))
	}
	if err != nil {
		return 0, 0, err
	}
	memory, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || memory < 0 {
		return 0, 0, fmt.Errorf("cgroup memory counter: INVALID")
	}
	return cpu, memory, nil
}

func cpuUpperBound(elapsed time.Duration, vcpus int) time.Duration {
	if elapsed > time.Duration((1<<63-1)/int64(vcpus)) {
		return time.Duration(1<<63 - 1)
	}
	return elapsed * time.Duration(vcpus)
}
