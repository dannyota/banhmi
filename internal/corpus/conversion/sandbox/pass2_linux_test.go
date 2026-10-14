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
		if err == unix.EPERM || err == unix.EACCES {
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
		proveZIPMember(t, fixture, 0)
		if !oracleRejects(t, fixture) {
			t.Fatal("readable nested ZIP missed")
		}
	}
}

func TestMalformedZIP64LocatorRejected(t *testing.T) {
	data := zip64Fixture(t)
	locator := len(data) - 22 - 20
	binary.LittleEndian.PutUint32(data[locator+4:locator+8], 2)
	if _, err := zip.NewReader(bytes.NewReader(data), int64(len(data))); !errors.Is(err, zip.ErrFormat) {
		t.Fatalf("fixture should be opaque to archive/zip: %v", err)
	}
	if oracleRejects(t, data) {
		t.Fatal("opaque malformed locator classified as archive")
	}
	r, err := runPreflight(t, zipFixture(t, data), TypeZIP, OfficeLimits())
	// Opaque to the oracle, but its leading local ZIP header still violates
	// the existing package-member signature rule.
	assertFailure(t, r, err, UnsafeOutput, "")
}

func TestZIP64LocatorTailOracleVerdicts(t *testing.T) {
	data := make([]byte, 20+22)
	copy(data, "PK\x06\x07")
	binary.LittleEndian.PutUint32(data[16:20], 1)
	copy(data[20:], "PK\x05\x06")
	binary.LittleEndian.PutUint16(data[30:32], 0xffff)
	// Preserve the original fixture. Its locator requests a 56-byte ZIP64
	// record from a 42-byte member, so the parser reports truncation, not
	// ErrFormat. Any error other than ErrFormat is unsafe.
	if _, err := zip.NewReader(bytes.NewReader(data), int64(len(data))); !errors.Is(err, io.EOF) {
		t.Fatalf("locator fixture should be truncated: %v", err)
	}
	if !oracleRejects(t, data) {
		t.Fatal("truncated locator treated as opaque")
	}
	r, err := runPreflight(t, zipFixture(t, data), TypeZIP, OfficeLimits())
	assertFailure(t, r, err, UnsafeOutput, "")
	// The same bare locator without an EOCD is opaque and stays accepted.
	opaque := data[:20]
	if _, err := zip.NewReader(bytes.NewReader(opaque), int64(len(opaque))); !errors.Is(err, zip.ErrFormat) {
		t.Fatalf("bare locator should be opaque: %v", err)
	}
	if oracleRejects(t, opaque) {
		t.Fatal("opaque locator classified as archive")
	}
	r, err = runPreflight(t, zipFixture(t, opaque), TypeZIP, OfficeLimits())
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
}

func TestNestedZIPNegativeBaseOffset(t *testing.T) {
	data := append([]byte{'x'}, zipFixture(t, []byte("synthetic"))...)
	end := len(data) - 22
	central := int(binary.LittleEndian.Uint32(data[end+16:end+20])) + 1
	binary.LittleEndian.PutUint32(data[end+16:end+20], uint32(central+1024))
	binary.LittleEndian.PutUint32(data[central+42:central+46], 1025)
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(reader.File) != 1 {
		t.Fatalf("fixture not readable by archive/zip: %v", err)
	}
	file, err := reader.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(file)
	file.Close()
	if err != nil || string(payload) != "synthetic" {
		t.Fatalf("fixture file not readable: %v", err)
	}
	if !oracleRejects(t, data) {
		t.Fatal("negative base offset nested ZIP missed")
	}
	r, err := runPreflight(t, zipFixture(t, data), TypeZIP, OfficeLimits())
	assertFailure(t, r, err, UnsafeOutput, "")
}

func TestChildInheritedCPUHardLimit(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--sandbox-limit-probe")
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
		if method == base+"+pidfd-start-time" {
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
	parent := fakeParentCgroup(t)
	// This metadata-only fake requires no container runtime or mount privilege.
	// Paths are test-created names, never document input or environment values.
	script := "#!/bin/sh\nshift\nprintf '%s\\n' \"$1 $2\" >> '" + trace + "'\n" +
		"case \"$1 $2\" in\n" +
		"wait*) printf '0\\n' ;;\n" +
		"'kill --signal=KILL') exit 0 ;;\n" +
		"'inspect --format={{.State.Running}}') printf 'false\\n' ;;\n" +
		"'inspect --format={{.State.Pid}}') printf '0\\n' ;;\n" +
		"'inspect --format={{.State.CgroupPath}}') printf '\\n' ;;\n" +
		"rm*) rm -f '" + filepath.Join(parent, "memory.peak") + "' '" + filepath.Join(parent, "cgroup.events") + "' '" + filepath.Join(parent, "cgroup.procs") + "' '" + filepath.Join(parent, "cgroup.threads") + "'; exit 0 ;;\n*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(runtime, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	l := &ContainerLauncher{config: ContainerConfig{Runtime: runtime, Image: "synthetic@sha256:" + strings.Repeat("a", 64), SeccompProfile: profile}}
	// Remove the CPU counter to exercise failed polling and final accounting.
	if err := os.Remove(filepath.Join(parent, "cpu.stat")); err != nil {
		t.Fatal(err)
	}
	limits := OfficeLimits()
	job, err := l.prepareWithParent(context.Background(), Recipe{}, ChildRequest{InputDir: dir, Limits: limits}, dir, limits.CPUTime, parent)
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
	stopped := strings.Index(calls, "wait ")
	removed := strings.Index(calls, "rm ")
	if stopped < 0 || removed < stopped || strings.Contains(calls, "State.CgroupPath") {
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
	if err == nil {
		t.Fatal("cgroup v1 maximum substituted for required v2 peak")
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.peak"), []byte("99"), 0600); err != nil {
		t.Fatal(err)
	}
	cpu, memory, err = cgroupUsage(dir)
	if err != nil || cpu != time.Microsecond || memory != 99 {
		t.Fatal("final maximum memory counter not read")
	}
}
