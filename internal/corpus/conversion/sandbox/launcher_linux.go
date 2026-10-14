//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
	args := []string{"run", "--pull=never", "--log-driver=none", "--unsetenv-all", "--http-proxy=false", "--name", name, "--interactive", "--init",
		"--network=none", "--read-only", "--read-only-tmpfs=false", "--pid=private", "--userns=keep-id:uid=65532,gid=65532", "--user=65532:65532",
		"--cap-drop=ALL", "--security-opt=no-new-privileges", "--security-opt=seccomp=" + l.config.SeccompProfile,
		"--cpus=" + strconv.Itoa(req.Limits.VCPUs), "--memory=" + strconv.FormatInt(req.Limits.MemoryBytes, 10),
		"--memory-swap=" + strconv.FormatInt(req.Limits.MemoryBytes, 10), "--pids-limit=" + strconv.Itoa(req.Limits.TempFiles),
		"--ulimit=cpu=" + strconv.FormatInt(int64(seconds), 10) + ":" + strconv.FormatInt(int64(seconds), 10),
		"--mount=type=bind,src=" + req.InputDir + ",dst=/input,ro=true",
		"--mount=type=bind,src=" + dir + ",dst=/scratch,rw=true",
		"--mount=type=bind,src=" + filepath.Join(dir, "temp") + ",dst=/tmp,rw=true",
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
	req.Limits.CPUTime = cpu
	req.InputDir, req.OutputDir, req.ScratchDir = "/input", "/scratch/output", "/scratch/temp"
	job := &launch{cmd: cmd, request: req, report: &terminationReport{}, close: func() {}}
	var teardown sync.Mutex
	var cgroup string
	var lastCPU time.Duration
	var peakMemory int64
	var finalized bool
	var finalErr error
	// Only bounded runtime metadata is retained. Never collect inspect's full
	// object, container environment, logs, or document-provided paths.
	runtimeMetadata := func(ctx context.Context, args ...string) (string, error) {
		command := exec.CommandContext(ctx, l.config.Runtime, args...)
		command.Env = []string{}
		command.WaitDelay = 100 * time.Millisecond
		out := &boundedResponse{max: 4096, overflow: make(chan struct{}, 1)}
		command.Stdout = out
		if err := command.Run(); err != nil || out.exceeded() {
			return "", fmt.Errorf("container metadata: UNAVAILABLE")
		}
		return strings.TrimSpace(string(out.bytes())), nil
	}
	sample := func(final bool) (time.Duration, int64, error) {
		if cgroup == "" {
			inspectCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			path, pathErr := runtimeMetadata(inspectCtx, "inspect", "--format={{.State.CgroupPath}}", name)
			if pathErr == nil && filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.Contains(path, "..") && path != "/" {
				if strings.HasPrefix(path, "/sys/fs/cgroup/") {
					cgroup = path
				} else {
					cgroup = filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(path, "/"))
				}
			}
		}
		if cgroup != "" {
			cpu, memory, err := cgroupUsageSample(cgroup, final)
			if err == nil {
				lastCPU, peakMemory = max(lastCPU, cpu), max(peakMemory, memory)
				return lastCPU, peakMemory, nil
			}
		}
		return lastCPU, peakMemory, fmt.Errorf("container accounting: UNCONFIRMED")
	}
	job.usage = func() (time.Duration, int64, error) {
		teardown.Lock()
		defer teardown.Unlock()
		if finalized {
			return lastCPU, peakMemory, finalErr
		}
		return sample(false)
	}
	job.terminate = func() error {
		teardown.Lock()
		defer teardown.Unlock()
		if finalized {
			return finalErr
		}
		job.report.Mechanism = "container-pid-namespace+accounting-UNCONFIRMED"
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		defer func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}()
		// Resolve the retained runtime cgroup path before killing the container.
		_, _, _ = sample(false)
		for {
			running, err := runtimeMetadata(stopCtx, "inspect", "--format={{.State.Running}}", name)
			if err != nil {
				return fmt.Errorf("container termination: UNCONFIRMED")
			}
			if running == "false" {
				break
			}
			if running != "true" || stopCtx.Err() != nil {
				return fmt.Errorf("container termination: UNCONFIRMED")
			}
			_, _ = runtimeMetadata(stopCtx, "kill", "--signal=KILL", name)
		}
		// A stopped retained container is sampled before explicit removal. A
		// missing cgroup or peak counter is never replaced by elapsed*VCPUs.
		_, _, finalErr = sample(true)
		job.report.Mechanism = "container-pid-namespace"
		if finalErr != nil {
			job.report.Mechanism += "+accounting-UNCONFIRMED"
		}
		_, removeErr := runtimeMetadata(stopCtx, "rm", name)
		if removeErr != nil {
			return fmt.Errorf("container removal: UNCONFIRMED")
		}
		finalized = true
		return finalErr
	}
	job.close = func() {
		// Teardown retains state until final accounting, including cancellation.
		if cmd.Process != nil {
			_ = job.terminate()
		}
	}
	cmd.Cancel = job.terminate
	return job, nil
}

// DirectLauncher provides accounting and descendant cleanup, but no filesystem,
// UID, network, syscall or hard resource isolation. TMPDIR points at accounted
// scratch, but an uncontained child can still write elsewhere. Test use only.
// Its zero value refuses Run.
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
	baselineProcesses, err := descendantProcesses(os.Getpid(), nil, time.Now().Add(time.Second))
	if err != nil {
		release()
		return nil, err
	}
	baseline := map[int]uint64{}
	for pid, process := range baselineProcesses {
		baseline[pid] = process.start
	}
	cmd := exec.CommandContext(ctx, r.Executable, "--sandbox-child")
	cmd.Dir = req.ScratchDir
	cmd.Env = []string{"TMPDIR=" + req.ScratchDir}
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
	// Cancellation kills the pinned leader immediately; the run loop performs the
	// full tree sweep after Wait. It never reaps the leader concurrently with Wait.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return cmd.Process.Kill()
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

type procIdentity struct {
	parent int
	start  uint64 // /proc stat field 22, clock ticks since boot
}

func readProcIdentity(pid int) (procIdentity, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return procIdentity{}, err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return procIdentity{}, fmt.Errorf("process identity: INVALID")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return procIdentity{}, fmt.Errorf("process identity: INVALID")
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return procIdentity{}, err
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	return procIdentity{parent: parent, start: start}, err
}

func teardownDeadline(deadline time.Time) error {
	if !time.Now().Before(deadline) {
		return fmt.Errorf("descendant termination: UNCONFIRMED")
	}
	return nil
}

func descendantProcesses(root int, exclude map[int]uint64, deadline time.Time) (map[int]procIdentity, error) {
	if err := teardownDeadline(deadline); err != nil {
		return nil, err
	}
	dir, err := os.Open("/proc")
	if err != nil {
		return nil, fmt.Errorf("descendant enumeration: UNAVAILABLE")
	}
	defer dir.Close()
	processes := map[int]procIdentity{}
	for {
		if err := teardownDeadline(deadline); err != nil {
			return nil, err
		}
		entries, err := dir.ReadDir(64)
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("descendant enumeration: UNAVAILABLE")
		}
		for _, e := range entries {
			if err := teardownDeadline(deadline); err != nil {
				return nil, err
			}
			pid, err := strconv.Atoi(e.Name())
			if err != nil {
				continue
			}
			identity, err := readProcIdentity(pid)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("descendant identity: UNAVAILABLE")
			}
			processes[pid] = identity
		}
		if err == io.EOF {
			break
		}
	}
	found := map[int]procIdentity{}
	for pid, identity := range processes {
		if err := teardownDeadline(deadline); err != nil {
			return nil, err
		}
		if start, ok := exclude[pid]; ok && start == identity.start {
			continue
		}
		current, err := readProcIdentity(pid)
		if errors.Is(err, os.ErrNotExist) || (err == nil && current != identity) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("descendant identity: UNAVAILABLE")
		}
		seen := map[int]bool{pid: true}
		for parent := identity.parent; parent != 0; parent = processes[parent].parent {
			if err := teardownDeadline(deadline); err != nil {
				return nil, err
			}
			if parent == root {
				found[pid] = identity
				break
			}
			if seen[parent] {
				break
			}
			seen[parent] = true
			// A reused intermediate PID must not connect an unrelated process
			// to this tree. Verify every ancestry identity against the snapshot.
			current, err := readProcIdentity(parent)
			if errors.Is(err, os.ErrNotExist) || (err == nil && current != processes[parent]) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("descendant ancestry: UNAVAILABLE")
			}
		}
	}
	return found, nil
}

type pinnedProcess struct {
	pid   int
	start uint64
	fd    int // -1 only when pidfd_open returns ENOSYS
}

func pinProcess(pid int, identity procIdentity) (*pinnedProcess, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err == unix.ESRCH {
		return nil, nil
	}
	if err != nil && err != unix.ENOSYS {
		return nil, fmt.Errorf("process pin: UNAVAILABLE")
	}
	if err == unix.ENOSYS {
		fd = -1
	}
	current, readErr := readProcIdentity(pid)
	if readErr != nil || current.start != identity.start {
		if fd >= 0 {
			unix.Close(fd)
		}
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return nil, fmt.Errorf("process pin: UNAVAILABLE")
		}
		return nil, nil
	}
	return &pinnedProcess{pid: pid, start: identity.start, fd: fd}, nil
}

func (p *pinnedProcess) dead() (bool, error) {
	if p.fd >= 0 {
		fds := []unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}}
		_, err := unix.Poll(fds, 0)
		if err != nil {
			return false, err
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
			return false, fmt.Errorf("process confirmation: UNAVAILABLE")
		}
		return fds[0].Revents&unix.POLLIN != 0, nil
	}
	identity, err := readProcIdentity(p.pid)
	if errors.Is(err, os.ErrNotExist) || (err == nil && identity.start != p.start) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	err = syscall.Kill(p.pid, 0)
	return err == syscall.ESRCH, errUnlessGone(err)
}

func errUnlessGone(err error) error {
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

func terminateDescendants(baseline map[int]uint64, leader int, report *terminationReport) error {
	deadline := time.Now().Add(time.Second)
	known := map[int]*pinnedProcess{}
	defer func() {
		for _, p := range known {
			if p.fd >= 0 {
				unix.Close(p.fd)
			}
		}
	}()
	base := report.Mechanism
	fallback := false
	report.Mechanism = base + "+pidfd-start-time"
	for {
		processes, err := descendantProcesses(os.Getpid(), baseline, deadline)
		if err != nil {
			return err
		}
		for pid, identity := range processes {
			if err := teardownDeadline(deadline); err != nil {
				return err
			}
			if _, ok := known[pid]; !ok {
				p, err := pinProcess(pid, identity)
				if err != nil {
					return err
				}
				if p == nil {
					continue
				}
				known[pid] = p
				fallback = fallback || p.fd < 0
			}
		}
		if fallback {
			report.Mechanism = base + "+start-time-kill-fallback-ENOSYS"
		}
		for pid, p := range known {
			if err := teardownDeadline(deadline); err != nil {
				return err
			}
			dead, err := p.dead()
			if err != nil {
				return fmt.Errorf("descendant confirmation: UNCONFIRMED")
			}
			if !dead {
				if p.fd >= 0 {
					err = unix.PidfdSendSignal(p.fd, unix.SIGKILL, nil, 0)
				} else {
					// dead checked the start time immediately before numeric kill.
					err = syscall.Kill(pid, syscall.SIGKILL)
				}
				if err != nil && err != syscall.ESRCH {
					return fmt.Errorf("descendant termination: FAILED")
				}
			}
			if pid != leader {
				// A matching adopted zombie remains pinned until this reap.
				identity, err := readProcIdentity(pid)
				if err == nil && identity.start == p.start {
					var status syscall.WaitStatus
					if got, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil); err == nil && got == pid {
						report.Reaped[pid] = status
					}
				}
			}
			dead, err = p.dead()
			if err != nil {
				return fmt.Errorf("descendant confirmation: UNCONFIRMED")
			}
			if dead {
				if p.fd >= 0 {
					unix.Close(p.fd)
				}
				delete(known, pid)
			}
		}
		if len(known) == 0 && len(processes) == 0 {
			return nil
		}
		time.Sleep(time.Millisecond)
	}
}

func cgroupUsage(dir string) (time.Duration, int64, error) {
	return cgroupUsageSample(dir, true)
}

func cgroupUsageSample(dir string, final bool) (time.Duration, int64, error) {
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
		data, err = os.ReadFile(filepath.Join(dir, "memory.max_usage_in_bytes"))
	}
	if errors.Is(err, os.ErrNotExist) && !final {
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
