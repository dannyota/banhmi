//go:build linux

package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

// Handler runs byte work only in a child after admission preflight. Composition
// pins a handler in the executable; callers cannot send code or command lines.
type Handler func(ChildRequest) error

// ChildMain serves exactly one bounded stdin request. The entrypoint must call
// it explicitly, without init registration or credential/provider composition.
// A nil handler allows validation only and fails closed for conversion.
func ChildMain(in io.Reader, out io.Writer, handler Handler) (resultErr error) {
	defer func() { resultErr = publicError("child protocol", resultErr) }()
	if err := CloseInheritedDescriptors(); err != nil {
		return err
	}
	var req ChildRequest
	d := json.NewDecoder(io.LimitReader(in, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		return fmt.Errorf("decode child request: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("child request has trailing values")
	}
	if err := req.Limits.Validate(); err != nil {
		return fmt.Errorf("child limits: %w", err)
	}
	if req.Phase != executePhase && req.Phase != validatePhase {
		return fmt.Errorf("unknown child phase")
	}
	for _, p := range []string{req.InputDir, req.OutputDir, req.ScratchDir} {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("child roots must be absolute")
		}
	}
	if err := childLimits(req.Limits); err != nil {
		return fmt.Errorf("apply child ceilings: %w", err)
	}
	var response childResponse
	var err error
	if req.Phase == validatePhase {
		response.Outputs, err = validateStaged(req.OutputDir, req.Limits)
	} else {
		err = preflightInput(req)
		if err == nil {
			if handler == nil {
				err = &Failure{Cause: ExitFailure}
			} else {
				err = handler(req)
			}
		}
	}
	if err != nil {
		var f *Failure
		if !errors.As(err, &f) {
			f = &Failure{Cause: ExitFailure}
		}
		response.Failure = f
	}
	if err := json.NewEncoder(out).Encode(response); err != nil {
		return fmt.Errorf("encode child receipt: %w", err)
	}
	return nil
}

func preflightInput(req ChildRequest) error {
	if err := checkTree(context.Background(), req.InputDir, req.Limits, true); err != nil {
		return err
	}
	root, err := os.OpenRoot(req.InputDir)
	if err != nil {
		return fmt.Errorf("open input root: %w", err)
	}
	defer root.Close()
	budget := newPhaseBudget(req.Limits)
	return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk input: %w", err)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return &Failure{Cause: UnsafeOutput}
		}
		file, err := root.Open(name)
		if err != nil {
			return fmt.Errorf("open input member: %w", err)
		}
		info, err := file.Stat()
		if err == nil {
			err = preflightBudget(file, info.Size(), req.InputType, req.Limits, req.Limits.InputBytes, BoundInput, budget)
		}
		closeErr := file.Close()
		if err != nil {
			return fmt.Errorf("preflight input: %w", err)
		}
		if closeErr != nil {
			return fmt.Errorf("close input member: %w", closeErr)
		}
		return nil
	})
}

func childLimits(l Limits) error {
	// Hard per-process CPU and file ceilings supplement aggregate monitoring.
	// RSS and aggregate disk hard caps require cgroups and bounded mounts.
	seconds := uint64(l.CPUTime / time.Second)
	if l.CPUTime%time.Second != 0 {
		seconds++
	}
	for resource, limit := range map[int]uint64{syscall.RLIMIT_CPU: seconds, syscall.RLIMIT_FSIZE: uint64(l.ScratchBytes) + 1, syscall.RLIMIT_CORE: 0} {
		if err := syscall.Setrlimit(resource, &syscall.Rlimit{Cur: limit, Max: limit}); err != nil {
			return fmt.Errorf("set resource limit: %w", err)
		}
	}
	runtime.GOMAXPROCS(l.VCPUs)
	// Apply the available-CPU subset to existing runtime threads. New threads
	// inherit their creator's affinity. No assumption about CPU numbering is made.
	var mask [128]byte
	_, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_GETAFFINITY, 0, uintptr(len(mask)), uintptr(unsafe.Pointer(&mask[0])))
	if errno != 0 {
		return fmt.Errorf("read CPU affinity: %w", errno)
	}
	left := l.VCPUs
	for i := range mask {
		for bit := uint(0); bit < 8; bit++ {
			if mask[i]&(1<<bit) == 0 {
				continue
			}
			if left > 0 {
				left--
			} else {
				mask[i] &^= 1 << bit
			}
		}
	}
	threads, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return fmt.Errorf("list runtime threads: %w", err)
	}
	for _, thread := range threads {
		var tid int
		if _, err := fmt.Sscan(thread.Name(), &tid); err != nil {
			return fmt.Errorf("parse thread ID: %w", err)
		}
		_, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_SETAFFINITY, uintptr(tid), uintptr(len(mask)), uintptr(unsafe.Pointer(&mask[0])))
		if errno != 0 && errno != syscall.ESRCH {
			return fmt.Errorf("bound CPU affinity: %w", errno)
		}
	}
	return nil
}
