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
	for name, value := range map[string]string{"cpu.stat": "usage_usec 250000\nuser_usec 100000\n", "memory.peak": "123456", "cgroup.events": "populated 0\nfrozen 0\n"} {
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
	central := binary.LittleEndian.Uint32(data[end+16:end+20])
	size := binary.LittleEndian.Uint32(data[end+12:end+16])
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
			runtime, trace := filepath.Join(dir, "runtime"), filepath.Join(dir, "trace")
			parent := fakeParentCgroup(t)
			script := "#!/bin/sh\nshift\ncase \"$1 $2\" in\n'inspect --format={{.State.Running}}') echo false ;;\nrm*)\n"
			if permanent {
				script += "echo rm >> '" + trace + "'\nexit 1\n"
			} else {
				script += "if [ ! -f '" + trace + "' ]; then echo rm > '" + trace + "'; exit 1; fi\necho rm >> '" + trace + "'\n"
			}
			script += "rm -f '" + filepath.Join(parent, "cpu.stat") + "' '" + filepath.Join(parent, "memory.peak") + "' '" + filepath.Join(parent, "cgroup.events") + "'\n;;\n*) exit 1 ;;\nesac\n"
			if err := os.WriteFile(runtime, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			l := &ContainerLauncher{config: ContainerConfig{Runtime: runtime, Image: "synthetic"}}
			limits := OfficeLimits()
			job, err := l.prepareWithParent(context.Background(), Recipe{}, ChildRequest{InputDir: dir, Limits: limits}, dir, limits.CPUTime, parent)
			if err != nil {
				t.Fatal(err)
			}
			// These counters exist before any container metadata or process.
			if cpu, memory, err := job.usage(); err != nil || cpu != 250*time.Millisecond || memory != 123456 {
				t.Fatal("accounting depends on container startup")
			}
			err = job.terminate()
			data, readErr := os.ReadFile(trace)
			if readErr != nil || strings.Count(string(data), "rm") < 2 {
				t.Fatal("removal not retried")
			}
			if cpu, memory, _ := job.usage(); cpu != 250*time.Millisecond || memory != 123456 {
				t.Fatal("final counters lost after removal")
			}
			if permanent && (err == nil || !strings.Contains(job.report.Mechanism, "container-removal-UNCONFIRMED")) {
				t.Fatal("leaked container not failed closed in receipt")
			}
			if !permanent && (err == nil || !strings.Contains(job.report.Mechanism, "container-removal-retried")) {
				t.Fatal("removal retry not recorded or attempt did not fail closed")
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
	if job.ready() {
		t.Fatal("creating container reported ready")
	}
	if _, _, err := job.usage(); err != nil {
		t.Fatal("creating container accounting failed")
	}
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if !job.ready() {
		t.Fatal("started child PID not reported ready")
	}
	cancel()
	if job.ready() {
		t.Fatal("cancelled readiness did not stop")
	}
}
