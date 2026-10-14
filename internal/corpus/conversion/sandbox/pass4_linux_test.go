//go:build linux

package sandbox

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every readable fixture proves that its intended payload can be consumed,
// rather than merely that NewReader accepts a central directory.
func proveZIPMember(t *testing.T, data []byte, index int) {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(z.File) <= index {
		t.Fatalf("fixture directory unreadable: %v", err)
	}
	r, err := z.File[index].Open()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(r)
	closeErr := r.Close()
	if err != nil || closeErr != nil || string(payload) != "synthetic" {
		t.Fatalf("fixture payload unreadable: %v, %v", err, closeErr)
	}
}

func TestNestedZIPSecondReadableEntry(t *testing.T) {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, name := range []string{"broken", "readable"} {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("synthetic")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data := b.Bytes()
	end := len(data) - 22
	central := int(binary.LittleEndian.Uint32(data[end+16 : end+20]))
	binary.LittleEndian.PutUint32(data[central+42:central+46], 0x7fffffff)
	proveZIPMember(t, data, 1)
	r, err := runPreflight(t, zipFixture(t, data), TypeZIP, OfficeLimits())
	assertFailure(t, r, err, UnsafeOutput, "")
}

func TestNestedZIPSignedZIP64LocalOffset(t *testing.T) {
	data := zipFixture(t, []byte("synthetic"))
	end := len(data) - 22
	central := int(binary.LittleEndian.Uint32(data[end+16 : end+20]))
	nameLen := int(binary.LittleEndian.Uint16(data[central+28 : central+30]))
	extraAt := central + 46 + nameLen
	extra := make([]byte, 12)
	binary.LittleEndian.PutUint16(extra, 1)
	binary.LittleEndian.PutUint16(extra[2:], 8)
	binary.LittleEndian.PutUint64(extra[4:], ^uint64(0))
	binary.LittleEndian.PutUint32(data[central+42:central+46], 0xffffffff)
	binary.LittleEndian.PutUint16(data[central+30:central+32], 12)
	data = append(append(append([]byte(nil), data[:extraAt]...), extra...), data[extraAt:]...)
	end += 12
	binary.LittleEndian.PutUint32(data[end+12:end+16], uint32(len(data)-22-central))
	// Declaring the directory one byte earlier supplies base=1, making signed
	// local offset -1 read at byte zero, where the local header starts.
	binary.LittleEndian.PutUint32(data[end+16:end+20], uint32(central-1))
	proveZIPMember(t, data, 0)
	r, err := runPreflight(t, zipFixture(t, data), TypeZIP, OfficeLimits())
	assertFailure(t, r, err, UnsafeOutput, "")
}

type failingReaderAt struct{ err error }

func (r failingReaderAt) ReadAt([]byte, int64) (int, error) { return 0, r.err }

func TestNestedZIPOracleErrors(t *testing.T) {
	for _, oracleErr := range []error{io.ErrUnexpectedEOF, io.EOF, zip.ErrInsecurePath, errors.New("synthetic I/O failure")} {
		if _, err := nestedArchive(failingReaderAt{oracleErr}, 100); err == nil {
			t.Fatal("oracle failure treated as opaque")
		}
	}
	data := []byte("opaque tail")
	if nested, err := nestedArchive(bytes.NewReader(data), int64(len(data))); nested || err != nil {
		t.Fatal("ErrFormat did not remain opaque")
	}
}

func TestStartupLimitValidation(t *testing.T) {
	for _, startup := range []time.Duration{0, -1, 121 * time.Second} {
		l := OfficeLimits()
		l.StartupTime = startup
		if l.Validate() == nil {
			t.Fatal("invalid startup allowance accepted")
		}
	}
	if !(&Failure{Cause: StartupTimeout}).valid() {
		t.Fatal("startup cause rejected")
	}
}

type fakeLauncherClock struct {
	now     time.Time
	onSleep func(time.Duration)
	timers  []fakeClockTimer
}

type fakeClockTimer struct {
	deadline time.Time
	cancel   context.CancelFunc
}

func (c *fakeLauncherClock) Now() time.Time { return c.now }
func (c *fakeLauncherClock) WithTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	c.timers = append(c.timers, fakeClockTimer{deadline: c.now.Add(d), cancel: cancel})
	return ctx, cancel
}
func (c *fakeLauncherClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.now = c.now.Add(d)
	if c.onSleep != nil {
		c.onSleep(d)
	}
	for _, timer := range c.timers {
		if !c.now.Before(timer.deadline) {
			timer.cancel()
		}
	}
	return ctx.Err()
}

type fakeRuntimeExecutor struct {
	run func(context.Context, []string) (string, error)
}

func (e fakeRuntimeExecutor) Run(ctx context.Context, _ string, args ...string) (string, error) {
	return e.run(ctx, args)
}

func clearFakeCgroupFiles(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			if err := os.Remove(filepath.Join(parent, entry.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestContainerConmonFailClosedAndCleanup(t *testing.T) {
	parent := fakeParentCgroup(t)
	clock := &fakeLauncherClock{now: time.Unix(1, 0)}
	calls := []string{}
	l := &ContainerLauncher{config: ContainerConfig{Runtime: "/synthetic"}, clock: clock}
	l.executor = fakeRuntimeExecutor{run: func(_ context.Context, args []string) (string, error) {
		calls = append(calls, args[1])
		switch args[1] {
		case "inspect":
			child := filepath.Join(parent, "conmon")
			if err := os.MkdirAll(child, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"cgroup.procs", "cgroup.threads", "cgroup.events"} {
				value := ""
				if name == "cgroup.events" {
					value = "populated 0\n"
				}
				if err := os.WriteFile(filepath.Join(child, name), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			return "123", nil
		case "wait":
			return "0", nil
		case "rm":
			clearFakeCgroupFiles(t, filepath.Join(parent, "conmon"))
			clearFakeCgroupFiles(t, parent)
			return "", nil
		}
		return "", errors.New("unexpected command")
	}}
	lmt := OfficeLimits()
	job, err := l.prepareWithParent(context.Background(), Recipe{}, ChildRequest{InputDir: t.TempDir(), Limits: lmt}, t.TempDir(), lmt.CPUTime, parent)
	if err != nil {
		t.Fatal(err)
	}
	if job.ready(context.Background()) || job.startupFailure == nil || job.startupFailure() == nil {
		t.Fatal("conmon charged to attempt did not fail closed")
	}
	if err := job.terminate(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(parent); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned conmon child retained")
	}
	if strings.Join(calls, ",") != "inspect,kill,wait,rm" {
		t.Fatal("runtime teardown out of order")
	}
}

func oracleRejects(t *testing.T, data []byte) bool {
	t.Helper()
	nested, err := nestedArchive(bytes.NewReader(data), int64(len(data)))
	return nested || err != nil
}

func TestContainerFinalSnapshotWaitsForTasks(t *testing.T) {
	parent := fakeParentCgroup(t)
	if err := os.WriteFile(filepath.Join(parent, "cgroup.threads"), []byte("123\n"), 0600); err != nil {
		t.Fatal(err)
	}
	clock := &fakeLauncherClock{now: time.Unix(1, 0)}
	sleeps := 0
	clock.onSleep = func(time.Duration) {
		sleeps++
		if sleeps == 1 {
			if err := os.WriteFile(filepath.Join(parent, "cgroup.threads"), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(parent, "cpu.stat"), []byte("usage_usec 500000\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	l := &ContainerLauncher{config: ContainerConfig{Runtime: "/synthetic"}, clock: clock}
	l.executor = fakeRuntimeExecutor{run: func(_ context.Context, args []string) (string, error) {
		switch args[1] {
		case "kill":
			return "", nil
		case "wait":
			return "0", nil
		case "rm":
			if sleeps != 2 {
				t.Fatal("runtime removed before two consecutive empty task reads")
			}
			clearFakeCgroupFiles(t, parent)
			return "", nil
		}
		return "", errors.New("unexpected command")
	}}
	lmt := OfficeLimits()
	job, err := l.prepareWithParent(context.Background(), Recipe{}, ChildRequest{InputDir: t.TempDir(), Limits: lmt}, t.TempDir(), lmt.CPUTime, parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := job.terminate(); err != nil {
		t.Fatal(err)
	}
	cpu, memory, err := job.usage()
	if err != nil || cpu != 500*time.Millisecond || memory != 123456 {
		t.Fatal("final snapshot preceded task completion")
	}
}

func TestContainerCompletionUnconfirmedAndParentRetained(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(map[bool]string{false: "parent-removal", true: "tasks-busy"}[busy], func(t *testing.T) {
			parent := fakeParentCgroup(t)
			if busy {
				if err := os.WriteFile(filepath.Join(parent, "cgroup.procs"), []byte("123\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			clock := &fakeLauncherClock{now: time.Unix(1, 0)}
			removed := false
			l := &ContainerLauncher{config: ContainerConfig{Runtime: "/synthetic"}, clock: clock}
			l.executor = fakeRuntimeExecutor{run: func(_ context.Context, args []string) (string, error) {
				switch args[1] {
				case "wait":
					return "0", nil
				case "kill":
					return "", nil
				case "rm":
					removed = true
					return "", nil
				}
				return "", errors.New("unexpected command")
			}}
			lmt := OfficeLimits()
			job, err := l.prepareWithParent(context.Background(), Recipe{}, ChildRequest{InputDir: t.TempDir(), Limits: lmt}, t.TempDir(), lmt.CPUTime, parent)
			if err != nil {
				t.Fatal(err)
			}
			if job.terminate() == nil {
				t.Fatal("unconfirmed cleanup accepted")
			}
			if removed == busy {
				t.Fatal("runtime removal did not respect completion gate")
			}
			if !validCleanupReference(job.report.Retention.CleanupReference) {
				t.Fatal("opaque cleanup reference missing")
			}
			if clock.now.Sub(time.Unix(1, 0)) != time.Second {
				t.Fatal("cleanup exceeded shared bound")
			}
			r := Receipt{termination: []*terminationReport{job.report}}
			methods := strings.Join(r.TerminationMechanisms(), ",")
			if !strings.Contains(methods, "PARENT_RETAINED") {
				t.Fatal("parent retention missing")
			}
			if busy && (!strings.Contains(methods, "ACCOUNTING_UNCONFIRMED") || !strings.Contains(methods, "CONTAINER_RETAINED")) {
				t.Fatal("unconfirmed task accounting omitted")
			}
		})
	}
}

type startupProbeLauncher struct{}

func (startupProbeLauncher) prepare(ctx context.Context, _ Recipe, req ChildRequest, _ string, _ time.Duration) (*launch, error) {
	cmd := exec.CommandContext(ctx, "/bin/sleep", "10")
	job := &launch{cmd: cmd, request: req, report: &terminationReport{}, close: func() {}, ready: func(context.Context) bool { return false }}
	job.usage = func() (time.Duration, int64, error) { return 0, 0, nil }
	job.terminate = func() error {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil
	}
	return job, nil
}

func TestStartupTimeoutCauseAndContextBound(t *testing.T) {
	l := OfficeLimits()
	l.WallTime, l.StartupTime = time.Second, 30*time.Millisecond
	s := &Supervisor{limits: l, launcher: startupProbeLauncher{}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err := s.runChild(ctx, ChildRequest{Limits: l}, t.TempDir(), l.CPUTime)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Cause != StartupTimeout {
		t.Fatal("startup did not return distinct cause")
	}
	ctx, cancelEarly := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancelEarly()
	l.StartupTime = time.Second
	s.limits = l
	start := time.Now()
	_, _, err = s.runChild(ctx, ChildRequest{Limits: l}, t.TempDir(), l.CPUTime)
	if err == nil || time.Since(start) > time.Second {
		t.Fatal("startup exceeded remaining context deadline")
	}
}

func TestRuntimeExecutorCancellationBoundsWait(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime")
	// exec makes sleep the only process. No runtime, root or orphan is needed.
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 10\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := (execRuntimeExecutor{}).Run(ctx, path); err == nil {
		t.Fatal("cancelled runtime succeeded")
	}
	if time.Since(start) > time.Second {
		t.Fatal("runtime wait exceeded cancellation bound")
	}
}

func TestReceiptReportsRetentionBeforeProcessStart(t *testing.T) {
	r := Receipt{termination: []*terminationReport{{Retention: RetentionState{Reasons: []RetentionReason{ParentRetained}}}}}
	methods := r.TerminationMechanisms()
	if len(methods) != 1 || methods[0] != "PARENT_RETAINED" {
		t.Fatal("retention omitted when no process started")
	}
}
