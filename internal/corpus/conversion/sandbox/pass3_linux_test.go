//go:build linux

package sandbox

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func fakeParentCgroup(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	for name, value := range map[string]string{"cpu.stat": "usage_usec 250000\nuser_usec 100000\n", "memory.peak": "123456", "cgroup.events": "populated 0\nfrozen 0\n", "cgroup.procs": "", "cgroup.threads": ""} {
		if err := os.WriteFile(filepath.Join(parent, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return parent
}

func zip64Fixture(t *testing.T) []byte {
	t.Helper()
	data := zipFixture(t, []byte("synthetic"))
	end := len(data) - 22
	central := binary.LittleEndian.Uint32(data[end+16 : end+20])
	size := binary.LittleEndian.Uint32(data[end+12 : end+16])
	record := make([]byte, 56+20)
	copy(record, "PK\x06\x06")
	binary.LittleEndian.PutUint64(record[4:12], 44)
	binary.LittleEndian.PutUint64(record[24:32], 1)
	binary.LittleEndian.PutUint64(record[32:40], 1)
	binary.LittleEndian.PutUint64(record[40:48], uint64(size))
	binary.LittleEndian.PutUint64(record[48:56], uint64(central))
	copy(record[56:], "PK\x06\x07")
	binary.LittleEndian.PutUint64(record[64:72], uint64(end))
	binary.LittleEndian.PutUint32(record[72:76], 1)
	result := append(append(append([]byte(nil), data[:end]...), record...), data[end:]...)
	eocd := result[len(result)-22:]
	binary.LittleEndian.PutUint16(eocd[10:12], 0xffff)
	binary.LittleEndian.PutUint32(eocd[12:16], 0xffffffff)
	binary.LittleEndian.PutUint32(eocd[16:20], 0xffffffff)
	return result
}

func TestPidfdUnavailableFailsUnconfirmed(t *testing.T) {
	p, err := bindProcess(os.Getpid(), procIdentity{}, -1, unix.ENOSYS)
	if p != nil || err == nil || !errors.Is(err, unix.ENOSYS) || !strings.Contains(err.Error(), "UNCONFIRMED") {
		t.Fatal("unavailable pidfd did not fail closed")
	}
	report := &terminationReport{Mechanism: "subreaper-proc"}
	recordPinFailure(report, err)
	if report.Mechanism != "subreaper-proc+pidfd-UNAVAILABLE-ENOSYS" {
		t.Fatal("receipt omitted unavailable pidfd mechanism")
	}
}

func TestParentCgroupSampling(t *testing.T) {
	parent := fakeParentCgroup(t)
	cpu, memory, err := cgroupUsage(parent)
	if err != nil || cpu != 250*time.Millisecond || memory != 123456 {
		t.Fatal("parent hierarchical counters not parsed")
	}
	if err := os.WriteFile(filepath.Join(parent, "cpu.stat"), []byte("usage_usec -1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cgroupUsage(parent); err == nil {
		t.Fatal("negative parent CPU counter accepted")
	}
}

func TestContainerRequiresDelegation(t *testing.T) {
	dir := t.TempDir()
	runtime, profile := filepath.Join(dir, "runtime"), filepath.Join(dir, "profile")
	for _, path := range []string{runtime, profile} {
		if err := os.WriteFile(path, nil, 0700); err != nil {
			t.Fatal(err)
		}
	}
	_, err := NewContainerLauncher(ContainerConfig{Runtime: runtime, SeccompProfile: profile, Image: "synthetic@sha256:" + strings.Repeat("a", 64)})
	if err == nil || !strings.Contains(err.Error(), "delegated cgroup v2 subtree") {
		t.Fatal("missing operator prerequisite not reported")
	}
}

func TestContainerRemovalRetriesAndReportsLeak(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		t.Run(map[bool]string{false: "retry", true: "leak"}[permanent], func(t *testing.T) {
			dir := t.TempDir()
			parent := fakeParentCgroup(t)
			clock := &fakeLauncherClock{now: time.Unix(1, 0)}
			calls := []string{}
			removals := 0
			executor := fakeRuntimeExecutor{run: func(_ context.Context, args []string) (string, error) {
				calls = append(calls, args[1])
				switch args[1] {
				case "kill":
					return "", nil
				case "wait":
					return "0", nil
				case "rm":
					// The final counters must be sealed before runtime removal,
					// while the supervisor still owns its parent.
					if _, err := os.Stat(parent); err != nil {
						t.Fatal("parent removed before runtime removal")
					}
					removals++
					if permanent || removals == 1 {
						return "", errors.New("synthetic removal failure")
					}
					clearFakeCgroupFiles(t, parent)
					return "", nil
				}
				return "", errors.New("unexpected runtime command")
			}}
			l := &ContainerLauncher{config: ContainerConfig{Runtime: "/synthetic", Image: "synthetic"}, executor: executor, clock: clock}
			limits := OfficeLimits()
			limits.TeardownTime = time.Second
			job, err := l.prepareWithParent(context.Background(), Recipe{}, ChildRequest{InputDir: dir, Limits: limits}, dir, limits.CPUTime, parent)
			if err != nil {
				t.Fatal(err)
			}
			if cpu, memory, err := job.usage(); err != nil || cpu != 250*time.Millisecond || memory != 123456 {
				t.Fatal("accounting depends on container startup")
			}
			err = job.terminate()
			if removals < 2 || calls[0] != "kill" || calls[1] != "wait" {
				t.Fatal("bounded removal retry sequencing incorrect")
			}
			if !permanent && removals != 2 {
				t.Fatal("successful removal retried again")
			}
			if cpu, memory, _ := job.usage(); cpu != 250*time.Millisecond || memory != 123456 {
				t.Fatal("final counters lost after removal")
			}
			if permanent && (err == nil || !strings.Contains(job.report.Mechanism, "container-removal-UNCONFIRMED") || !validCleanupReference(job.report.Retention.CleanupReference)) {
				t.Fatal("leaked container not failed closed in receipt")
			}
			if !permanent && (err == nil || !strings.Contains(job.report.Mechanism, "container-removal-retried")) {
				t.Fatal("removal retry not recorded or attempt did not fail closed")
			}
			if !permanent {
				if _, err := os.Stat(parent); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("parent retained after successful removal")
				}
			}
			if permanent && (clock.now.Sub(time.Unix(1, 0)) != time.Second || len(job.report.Retention.Reasons) != 2) {
				t.Fatal("permanent failure exceeded bound or omitted retained parent")
			}
		})
	}
}

func TestContainerReadinessUsesChildPIDAndCancellation(t *testing.T) {
	dir := t.TempDir()
	runtime, marker := filepath.Join(dir, "runtime"), filepath.Join(dir, "started")
	script := "#!/bin/sh\nshift\ncase \"$1 $2\" in\n'inspect --format={{.State.Pid}}') if [ -f '" + marker + "' ]; then echo 123; else echo 0; fi ;;\n*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(runtime, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := &ContainerLauncher{config: ContainerConfig{Runtime: runtime}}
	limits := OfficeLimits()
	job, err := l.prepareWithParent(ctx, Recipe{}, ChildRequest{InputDir: dir, Limits: limits}, dir, limits.CPUTime, fakeParentCgroup(t))
	if err != nil {
		t.Fatal(err)
	}
	if job.ready(ctx) {
		t.Fatal("creating container reported ready")
	}
	if _, _, err := job.usage(); err != nil {
		t.Fatal("creating container accounting failed")
	}
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if !job.ready(ctx) {
		t.Fatal("started child PID not reported ready")
	}
	cancel()
	if job.ready(ctx) {
		t.Fatal("cancelled readiness did not stop")
	}
}
