//go:build linux

package sandbox

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestFallbackMountIdentity(t *testing.T) {
	root, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.WriteFile(filepath.Join(root.Name(), "file"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, unavailable := range []bool{false, true} {
		calls := 0
		reader := func(int) (uint64, error) {
			calls++
			if calls == 1 {
				return 1, nil
			}
			if unavailable {
				return 0, errors.New("unavailable")
			}
			return 2, nil
		}
		fd, err := openComponentsWithMountID(int(root.Fd()), "file", reader)
		if err == nil {
			unix.Close(fd)
			t.Fatal("fallback accepted foreign or unavailable mount identity")
		}
	}
}

func TestOutputBindMountRejected(t *testing.T) {
	rootDir, source := t.TempDir(), t.TempDir()
	target := filepath.Join(rootDir, "mounted")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "file"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
		if err == unix.EPERM || err == unix.EACCES || err == unix.ENOSYS {
			t.Skip("bind mount requires mount privileges unavailable on this runner")
		}
		t.Fatal(err)
	}
	defer unix.Unmount(target, 0)
	root, err := os.Open(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, fallback := range []bool{false, true} {
		if fallback {
			fd, err := openComponents(int(root.Fd()), "mounted/file")
			if err == nil {
				unix.Close(fd)
				t.Fatal("component fallback accepted bind mount")
			}
		} else {
			f, err := openBeneath(root, "mounted/file")
			if err == nil {
				f.Close()
				t.Fatal("openat2 accepted bind mount")
			}
		}
	}
}

func TestNestedZIPCompleteWindowAndAbsoluteOffset(t *testing.T) {
	data := zipFixture(t, []byte("synthetic"))
	end := len(data) - 22
	padded := append(append([]byte{'x'}, data...), make([]byte, 66000-22)...)
	gap := append([]byte(nil), data[:end]...)
	gap = append(gap, make([]byte, 123)...)
	gap = append(gap, data[end:]...)
	for _, fixture := range [][]byte{padded, gap} {
		if _, err := zip.NewReader(bytes.NewReader(fixture), int64(len(fixture))); err != nil {
			t.Fatalf("fixture not readable by archive/zip: %v", err)
		}
		if !nestedZIP(bytes.NewReader(fixture), int64(len(fixture))) {
			t.Fatal("readable nested ZIP missed")
		}
	}
}

func TestMalformedZIP64LocatorRejected(t *testing.T) {
	data := make([]byte, 20+22)
	copy(data, "PK\x06\x07")
	binary.LittleEndian.PutUint32(data[16:20], 1)
	copy(data[20:], "PK\x05\x06")
	binary.LittleEndian.PutUint16(data[30:32], 0xffff)
	if !nestedZIP(bytes.NewReader(data), int64(len(data))) {
		t.Fatal("recognized malformed ZIP64 treated as ordinary bytes")
	}
}

func TestChildInheritedCPUHardLimit(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "--sandbox-limit-probe")
	cmd.Env = []string{}
	if err := cmd.Run(); err != nil {
		t.Fatalf("child raised inherited hard CPU limit: %v", err)
	}
}

func inheritedCPUProbe() bool {
	var before syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_CPU, &before) != nil {
		return false
	}
	ceiling := min(before.Max, uint64(2))
	if syscall.Setrlimit(syscall.RLIMIT_CPU, &syscall.Rlimit{Cur: ceiling, Max: ceiling}) != nil {
		return false
	}
	l := OfficeLimits()
	l.CPUTime = 10 * time.Second
	if childLimits(l) != nil {
		return false
	}
	var after syscall.Rlimit
	return syscall.Getrlimit(syscall.RLIMIT_CPU, &after) == nil && after.Cur == ceiling && after.Max == ceiling
}

func validIdentityMechanism(method string) bool {
	for _, base := range []string{"subreaper-proc", "cgroup-kill+subreaper-proc", "subreaper-proc-after-cgroup-kill-failure"} {
		if method == base+"+pidfd-start-time" || method == base+"+start-time-kill-fallback-ENOSYS" {
			return true
		}
	}
	return false
}

func TestIdentityEnumerationDeadline(t *testing.T) {
	_, err := descendantProcesses(os.Getpid(), nil, time.Now().Add(-time.Second))
	if err == nil || !strings.Contains(err.Error(), "UNCONFIRMED") {
		t.Fatal("expired enumeration deadline ignored")
	}
}

func TestContainerMissingFinalAccountingFailsClosed(t *testing.T) {
	dir := t.TempDir()
	runtime := filepath.Join(dir, "runtime")
	profile := filepath.Join(dir, "profile")
	trace := filepath.Join(dir, "trace")
	// This metadata-only fake requires no container runtime or mount privilege.
	// Paths are test-created names, never document input or environment values.
	script := "#!/bin/sh\nprintf '%s\\n' \"$1 $2\" >> '" + trace + "'\n" +
		"case \"$1 $2\" in\n" +
		"'inspect --format={{.State.Running}}') printf 'false\\n' ;;\n" +
		"'inspect --format={{.State.Pid}}') printf '0\\n' ;;\n" +
		"'inspect --format={{.State.CgroupPath}}') printf '\\n' ;;\n" +
		"rm*) exit 0 ;;\n*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(runtime, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	l, err := NewContainerLauncher(ContainerConfig{Runtime: runtime, Image: "synthetic@sha256:" + strings.Repeat("a", 64), SeccompProfile: profile})
	if err != nil {
		t.Fatal(err)
	}
	limits := OfficeLimits()
	job, err := l.prepare(context.Background(), Recipe{}, ChildRequest{InputDir: dir, Limits: limits}, dir, limits.CPUTime)
	if err != nil {
		t.Fatal(err)
	}
	defer job.close()
	if _, _, err := job.usage(); err == nil || !strings.Contains(err.Error(), "UNCONFIRMED") {
		t.Fatal("missing sample replaced by elapsed CPU estimate")
	}
	if err := job.terminate(); err == nil || !strings.Contains(err.Error(), "UNCONFIRMED") {
		t.Fatal("missing final counters accepted")
	}
	if job.report.Mechanism != "container-pid-namespace+accounting-UNCONFIRMED" {
		t.Fatal("receipt omitted unconfirmed accounting")
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(data)
	stopped := strings.Index(calls, "inspect --format={{.State.Running}}")
	removed := strings.Index(calls, "rm ")
	if stopped < 0 || removed < stopped || !strings.Contains(calls[stopped:removed], "inspect --format={{.State.CgroupPath}}") {
		t.Fatal("container removed before final accounting attempt")
	}
	if err := job.terminate(); err == nil {
		t.Fatal("repeated teardown lost final accounting failure")
	}
	if _, _, err := job.usage(); err == nil {
		t.Fatal("cached accounting lost final failure")
	}
}

func TestFinalCgroupRequiresPeakMemory(t *testing.T) {
	dir := t.TempDir()
	for name, value := range map[string]string{"cpu.stat": "usage_usec 1\n", "memory.current": "0"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := cgroupUsage(dir); err == nil {
		t.Fatal("current memory substituted for final peak")
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.max_usage_in_bytes"), []byte("99"), 0600); err != nil {
		t.Fatal(err)
	}
	cpu, memory, err := cgroupUsage(dir)
	if err != nil || cpu != time.Microsecond || memory != 99 {
		t.Fatal("final maximum memory counter not read")
	}
}
