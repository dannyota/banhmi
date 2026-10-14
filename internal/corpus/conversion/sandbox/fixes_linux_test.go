//go:build linux

package sandbox

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// UncontainedForTests is deliberately absent from production builds.
type UncontainedForTests struct{}

func NewDirectLauncher(UncontainedForTests) *DirectLauncher {
	return &DirectLauncher{uncontainedForTests: true}
}

func TestLauncherRequired(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(OfficeLimits(), Recipe{Executable: exe}, nil); err == nil {
		t.Fatal("accepted missing isolation launcher")
	}
	s, err := New(OfficeLimits(), Recipe{Executable: exe}, &DirectLauncher{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Run(context.Background(), testAttempt(t, "success"))
	assertFailure(t, r, err, ExitFailure, "")
	if _, err := NewContainerLauncher(ContainerConfig{Runtime: "/missing-runtime", Image: "synthetic@sha256:" + strings.Repeat("a", 64), SeccompProfile: "/missing-profile"}); err == nil {
		t.Fatal("accepted missing runtime")
	}
}

func TestInheritedDescriptorClosed(t *testing.T) {
	fd, err := syscall.Open(filepath.Join(t.TempDir(), "inherited"), syscall.O_CREAT|syscall.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	r, err := newTestSupervisor(t, OfficeLimits(), nil).Run(context.Background(), testAttempt(t, "descriptors"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	f, err := r.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil || string(data) != "0,1,2" {
		t.Fatalf("child descriptors = %q, err=%v", data, err)
	}
}

func TestInheritedPipeWaitBound(t *testing.T) {
	l := OfficeLimits()
	l.WallTime = time.Second
	l.StartupTime = min(l.StartupTime, l.WallTime)
	l.TeardownTime = min(l.TeardownTime, l.WallTime)
	start := time.Now()
	r, err := newTestSupervisor(t, l, nil).Run(context.Background(), testAttempt(t, "held-stderr"))
	assertFailure(t, r, err, Timeout, "")
	if time.Since(start) > l.WallTime+2*time.Second {
		t.Fatal("inherited stderr exceeded bounded wait")
	}
}

type cancelLauncher struct {
	*DirectLauncher
	cancel context.CancelFunc
	job    *launch
}

func (l *cancelLauncher) prepare(ctx context.Context, r Recipe, req ChildRequest, dir string, cpu time.Duration) (*launch, error) {
	job, err := l.DirectLauncher.prepare(ctx, r, req, dir, cpu)
	l.job = job
	l.cancel()
	return job, err
}

func TestCancelledAtLaunch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newTestSupervisor(t, OfficeLimits(), nil)
	launcher := &cancelLauncher{DirectLauncher: NewDirectLauncher(UncontainedForTests{}), cancel: cancel}
	s.launcher = launcher
	r, err := s.Run(ctx, testAttempt(t, "success"))
	assertFailure(t, r, err, Cancelled, "")
	if launcher.job == nil || launcher.job.cmd.Process != nil {
		t.Fatal("cancelled launch started a process")
	}
}

func TestPublicErrorsDiscardPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "untrusted-private-name")
	_, err := New(OfficeLimits(), Recipe{Executable: path}, NewDirectLauncher(UncontainedForTests{}))
	if err == nil || strings.Contains(err.Error(), "untrusted-private-name") {
		t.Fatal("constructor exposed path")
	}
	r := Receipt{Complete: true, root: path, outputs: nil}
	if _, err := r.Open(0); err == nil || strings.Contains(err.Error(), "untrusted-private-name") {
		t.Fatal("receipt exposed path")
	}
}

func TestOutputHardLinkRejected(t *testing.T) {
	r, err := newTestSupervisor(t, OfficeLimits(), nil).Run(context.Background(), testAttempt(t, "hardlink"))
	assertFailure(t, r, err, UnsafeOutput, "")
}

func TestOutputSwapRejected(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "output")
	if err := os.WriteFile(name, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(dir, "replacement")
	if err := os.WriteFile(replacement, []byte("replaced"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, name); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, err := openChecked(root, "output", info)
	if f != nil {
		f.Close()
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Cause != UnsafeOutput {
		t.Fatal("replacement between size check and descriptor open accepted")
	}
}

func TestOutputPreflightUsesOutputCeiling(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "sparse")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	const size = 129 << 20
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	l := OfficeLimits()
	if err := preflightBudget(f, size, TypeText, l, l.OutputBytes, BoundOutput, newPhaseBudget(l)); err != nil {
		t.Fatal(err)
	}
	if err := preflight(f, size, TypeText, l); err == nil {
		t.Fatal("original input ceiling lost")
	}
}

func zipFixture(t *testing.T, payload []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, err := w.CreateHeader(&zip.FileHeader{Name: "payload", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestPhaseZIPBudget(t *testing.T) {
	data := zipFixture(t, bytes.Repeat([]byte{'x'}, 80))
	l := OfficeLimits()
	l.ExpandedBytes = 100
	a := testAttempt(t, "success")
	if err := os.Remove(filepath.Join(a.InputDir, "mode")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(a.InputDir, name), data, 0400); err != nil {
			t.Fatal(err)
		}
	}
	a.InputType = TypeZIP
	r, err := newTestSupervisor(t, l, nil).Run(context.Background(), a)
	assertFailure(t, r, err, ResourceLimit, BoundExpanded)
	f, err := os.Open(filepath.Join(a.InputDir, "a"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := preflight(f, int64(len(data)), TypeZIP, l); err != nil {
		t.Fatalf("each archive must pass alone: %v", err)
	}
}

func TestPrefixedNestedZIP(t *testing.T) {
	inner := zipFixture(t, []byte("synthetic"))
	outer := zipFixture(t, append([]byte{'x'}, inner...))
	r, err := runPreflight(t, outer, TypeZIP, OfficeLimits())
	assertFailure(t, r, err, UnsafeOutput, "")
}

func TestReceiptPinsHashedInode(t *testing.T) {
	r, err := newTestSupervisor(t, OfficeLimits(), nil).Run(context.Background(), testAttempt(t, "success"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	replacement := filepath.Join(r.root, "replacement")
	if err := os.WriteFile(replacement, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, filepath.Join(r.root, "output", "result")); err != nil {
		t.Fatal(err)
	}
	f, err := r.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil || string(data) != "synthetic output" {
		t.Fatal("sealed receipt followed a replaced path")
	}
}

func TestContainerTemplate(t *testing.T) {
	dir := t.TempDir()
	runtime := filepath.Join(dir, "runtime")
	profile := filepath.Join(dir, "seccomp")
	for _, name := range []string{runtime, profile} {
		if err := os.WriteFile(name, nil, 0700); err != nil {
			t.Fatal(err)
		}
	}
	l := &ContainerLauncher{config: ContainerConfig{Runtime: runtime, Image: "synthetic@sha256:" + strings.Repeat("a", 64), SeccompProfile: profile}}
	parent := fakeParentCgroup(t)
	limits := OfficeLimits()
	job, err := l.prepareWithParent(context.Background(), Recipe{}, ChildRequest{Phase: executePhase, InputDir: dir, Limits: limits}, dir, 2*time.Second, parent)
	if err != nil {
		t.Fatal(err)
	}
	defer job.close()
	if job.request.Limits.CPUTime != 2*time.Second {
		t.Fatal("serialized request lost remaining phase CPU budget")
	}
	if strings.Contains(strings.Join(job.cmd.Args, " "), "--rm") {
		t.Fatal("container auto-removal bypasses final accounting")
	}
	args := strings.Join(job.cmd.Args, " ")
	for _, flag := range []string{"--cgroup-manager=cgroupfs", "--cgroup-parent=" + parent, "--network=none", "--read-only", "--read-only-tmpfs=false", "--pid=private", "--userns=keep-id:uid=65532,gid=65532", "--user=65532:65532", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--security-opt=seccomp=" + profile, "--cpus=2", "--memory=4294967296", "--pids-limit=10000", "dst=/input,ro=true", "dst=/scratch,rw=true", "src=" + filepath.Join(dir, "temp") + ",dst=/tmp,rw=true", "--pull=never", "--log-driver=none", "--unsetenv-all", "--http-proxy=false", "--init"} {
		if !strings.Contains(args, flag) {
			t.Fatalf("missing fixed launch flag %s", flag)
		}
	}
	if job.request.InputDir != "/input" || job.request.OutputDir != "/scratch/output" || job.request.ScratchDir != "/scratch/temp" {
		t.Fatal("container protocol retained host roots")
	}
}

func TestPhaseMemberBudget(t *testing.T) {
	data := zipFixture(t, []byte("synthetic"))
	l := OfficeLimits()
	l.Members = 1
	budget := newPhaseBudget(l)
	for i := 0; i < 2; i++ {
		f, err := os.CreateTemp(t.TempDir(), "archive")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(data); err != nil {
			t.Fatal(err)
		}
		err = preflightBudget(f, int64(len(data)), TypeZIP, l, l.InputBytes, BoundInput, budget)
		f.Close()
		if i == 0 && err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			var failure *Failure
			if !errors.As(err, &failure) || failure.Bound != BoundMembers {
				t.Fatal("member budget reset between files")
			}
		}
	}
}

func TestPrefixedZIP64Structure(t *testing.T) {
	data := zip64Fixture(t)
	proveZIPMember(t, data, 0)
	prefixed := append([]byte{'x'}, data...)
	// Preserve the original locator: it points before the actual ZIP64 end
	// after prefixing and the real parser reports ErrFormat.
	if _, err := zip.NewReader(bytes.NewReader(prefixed), int64(len(prefixed))); !errors.Is(err, zip.ErrFormat) {
		t.Fatalf("original prefixed locator should be opaque: %v", err)
	}
	if oracleRejects(t, prefixed) {
		t.Fatal("opaque prefixed ZIP64 classified as archive")
	}
	r, err := runPreflight(t, zipFixture(t, prefixed), TypeZIP, OfficeLimits())
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	// Fix only the locator's absolute record position. The central and local
	// offsets still omit the prefix, exercising archive/zip's base handling.
	locator := len(prefixed) - 22 - 20
	position := binary.LittleEndian.Uint64(prefixed[locator+8 : locator+16])
	binary.LittleEndian.PutUint64(prefixed[locator+8:locator+16], position+1)
	proveZIPMember(t, prefixed, 0)
	if !oracleRejects(t, prefixed) {
		t.Fatal("readable prefixed ZIP64 missed")
	}
	r, err = runPreflight(t, zipFixture(t, prefixed), TypeZIP, OfficeLimits())
	assertFailure(t, r, err, UnsafeOutput, "")
}

func TestEntryDescriptorInventory(t *testing.T) {
	fd, err := syscall.Open(filepath.Join(t.TempDir(), "inherited"), syscall.O_CREAT|syscall.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "--sandbox-descriptor-probe")
	cmd.Env = []string{}
	out, err := cmd.Output()
	if err != nil || string(out) != "0,1,2" {
		t.Fatalf("entry inventory = %q, err=%v", out, err)
	}
}

func TestPhaseNestedImageBudget(t *testing.T) {
	var pixels bytes.Buffer
	if err := png.Encode(&pixels, image.NewGray(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	data := zipFixture(t, pixels.Bytes())
	l := OfficeLimits()
	l.Pages = 1
	budget := newPhaseBudget(l)
	for i := 0; i < 2; i++ {
		f, err := os.CreateTemp(t.TempDir(), "archive")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(data); err != nil {
			t.Fatal(err)
		}
		err = preflightBudget(f, int64(len(data)), TypeZIP, l, l.InputBytes, BoundInput, budget)
		f.Close()
		if i == 0 && err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			var failure *Failure
			if !errors.As(err, &failure) || failure.Bound != BoundPages {
				t.Fatal("nested image budget reset between files")
			}
		}
	}
}

func TestPhaseOutputZIPBudget(t *testing.T) {
	dir := t.TempDir()
	data := zipFixture(t, bytes.Repeat([]byte{'x'}, 80))
	l := OfficeLimits()
	l.ExpandedBytes = 100
	var outputs []Output
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, Output{Path: name, Type: TypeZIP, Size: int64(len(data))})
	}
	manifest, err := json.Marshal(Manifest{Outputs: outputs})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = validateStaged(dir, l)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Bound != BoundExpanded {
		t.Fatal("output phase expansion budget reset between files")
	}
}

func TestComponentFallbackRefusesSymlinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, filepath.Join(dir, "parent")); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"link", "parent/file"} {
		fd, err := openComponents(int(root.Fd()), name)
		if err == nil {
			syscall.Close(fd)
			t.Fatal("openat2 fallback followed symlink")
		}
	}
	fd, err := openComponents(int(root.Fd()), "file")
	if err != nil {
		t.Fatal(err)
	}
	syscall.Close(fd)
}

func TestContainerCgroupAccounting(t *testing.T) {
	dir := t.TempDir()
	for file, value := range map[string]string{"cpu.stat": "usage_usec 250000\nuser_usec 100000\n", "memory.peak": "123456"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cpu, memory, err := cgroupUsage(dir)
	if err != nil || cpu != 250*time.Millisecond || memory != 123456 {
		t.Fatalf("aggregate container usage: cpu=%v memory=%d err=%v", cpu, memory, err)
	}
	if err := os.Remove(filepath.Join(dir, "memory.peak")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.current"), []byte("123"), 0600); err != nil {
		t.Fatal(err)
	}
	_, memory, err = cgroupUsageSample(dir, false)
	if err != nil || memory != 123 {
		t.Fatal("memory.current fallback failed")
	}
	if cpuUpperBound(time.Second, 2) != 2*time.Second || cpuUpperBound(time.Duration(1<<63-1), 2) != time.Duration(1<<63-1) {
		t.Fatal("missing-sample CPU bound lost safety")
	}
}

// Arithmetic regression only. Container accounting never uses elapsed CPU estimates.
func cpuUpperBound(elapsed time.Duration, vcpus int) time.Duration {
	if elapsed > time.Duration((1<<63-1)/int64(vcpus)) {
		return time.Duration(1<<63 - 1)
	}
	return elapsed * time.Duration(vcpus)
}
