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
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestMain dispatches synthetic protocol and escaped-descendant helpers. Each
// exec closes inherited descriptors before reading requests or running handlers.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "--sandbox-limit-probe" {
		if err := CloseInheritedDescriptors(); err != nil {
			os.Exit(1)
		}
		if inheritedCPUProbe() {
			os.Exit(0)
		}
		os.Exit(1)
	}
	if len(os.Args) == 2 && os.Args[1] == "--sandbox-descriptor-probe" {
		if err := CloseInheritedDescriptors(); err != nil {
			os.Exit(1)
		}
		fd, err := unix.Open("/proc/self/fd", unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			os.Exit(1)
		}
		var buf [8192]byte
		var names []string
		for {
			n, err := unix.ReadDirent(fd, buf[:])
			if err != nil {
				os.Exit(1)
			}
			if n == 0 {
				break
			}
			_, _, entries := unix.ParseDirent(buf[:n], -1, nil)
			for _, entry := range entries {
				if entry != strconv.Itoa(fd) {
					names = append(names, entry)
				}
			}
		}
		_ = unix.Close(fd)
		if _, err := os.Stdout.Write([]byte(strings.Join(names, ","))); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) >= 2 && os.Args[1] == "--sandbox-grandchild" {
		// Keep stderr inherited deliberately for the bounded-Wait regression.
		if len(os.Args) != 4 {
			os.Exit(1)
		}
		exe, err := os.Executable()
		if err != nil {
			os.Exit(1)
		}
		switch os.Args[2] {
		case "session":
			if _, err := syscall.Setsid(); err != nil {
				os.Exit(1)
			}
			cmd := exec.Command(exe, "--sandbox-grandchild", "fork", os.Args[3])
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		case "fork":
			cmd := exec.Command(exe, "--sandbox-grandchild", "writer", os.Args[3])
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		case "writer":
			if err := os.WriteFile(os.Args[3], []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
				os.Exit(1)
			}
			for {
				_ = syscall.Pause()
			}
		}
		os.Exit(1)
	}
	if len(os.Args) == 2 && os.Args[1] == "--sandbox-child" {
		if err := ChildMain(os.Stdin, os.Stdout, testHandler); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func testHandler(req ChildRequest) error {
	// Mode is synthetic fixture content, not a caller-supplied command.
	data, err := os.ReadFile(filepath.Join(req.InputDir, "mode"))
	if errors.Is(err, os.ErrNotExist) {
		// Preflight fixtures replace mode with a file of the declared input type.
		if info, fixtureErr := os.Stat(filepath.Join(req.InputDir, "fixture")); fixtureErr == nil && info.Mode().IsRegular() {
			data, err = []byte("success"), nil
		}
	}
	if err != nil {
		return err
	}
	mode := string(data)
	switch mode {
	case "tree", "held-stderr":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		grandchild := exec.Command(exe, "--sandbox-grandchild", "session", filepath.Join(req.ScratchDir, "ready"))
		grandchild.Stderr = os.Stderr
		grandchild.Env = []string{}
		if err := grandchild.Start(); err != nil {
			return err
		}
		if err := grandchild.Wait(); err != nil {
			return err
		}
		if mode == "tree" {
			for {
				_ = syscall.Pause()
			}
		}
		// Leader exits successfully while the double-fork writer holds stderr.
		return nil
	case "cpu":
		for {
			_, _ = sha256.New().Write([]byte("synthetic CPU workload"))
		}
	case "scratch":
		return os.WriteFile(filepath.Join(req.ScratchDir, "large"), bytes.Repeat([]byte{'x'}, 64<<10), 0600)
	case "files":
		for i := 0; i < 40; i++ {
			if err := os.WriteFile(filepath.Join(req.ScratchDir, strconv.Itoa(i)), nil, 0600); err != nil {
				return err
			}
		}
	case "stdout":
		_, err := os.Stdout.Write(bytes.Repeat([]byte{'x'}, 64<<10))
		return err
	case "exit":
		return fmt.Errorf("synthetic handler failure")
	}
	content := []byte("synthetic output")
	if mode == "output" {
		content = bytes.Repeat([]byte{'x'}, 64<<10)
	}
	if mode == "descriptors" {
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			return err
		}
		var names []string
		for _, entry := range entries {
			fd, err := strconv.Atoi(entry.Name())
			if err != nil {
				return err
			}
			// The inventory directory fd has been closed by ReadDir. Go may
			// have opened CLOEXEC poll descriptors after entry sanitization.
			flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
			if errno == 0 && flags&syscall.FD_CLOEXEC == 0 {
				names = append(names, entry.Name())
			}
		}
		content = []byte(strings.Join(names, ","))
	}
	if mode == "environment" {
		content = []byte(strings.Join(os.Environ(), "\n"))
	}
	if err := os.WriteFile(filepath.Join(req.OutputDir, "result"), content, 0600); err != nil {
		return err
	}
	o := Output{Path: "result", Type: TypeText, Size: int64(len(content))}
	switch mode {
	case "traversal":
		o.Path = "../temp/secret"
	case "absolute":
		o.Path = "/etc/passwd"
	case "unnormalized":
		o.Path = "a/../result"
	case "size":
		o.Size++
	case "type":
		o.Type = "executable"
	case "hardlink":
		if err := os.Remove(filepath.Join(req.OutputDir, "result")); err != nil {
			return err
		}
		if err := os.Link(filepath.Join(req.InputDir, "mode"), filepath.Join(req.OutputDir, "result")); err != nil {
			return err
		}
		o.Size = int64(len(mode))
	case "symlink":
		if err := os.Symlink("result", filepath.Join(req.OutputDir, "link")); err != nil {
			return err
		}
		o.Path = "link"
	case "parent-symlink":
		if err := os.Symlink(req.OutputDir, filepath.Join(req.OutputDir, "link")); err != nil {
			return err
		}
		o.Path = "link/result"
	case "undeclared":
		if err := os.WriteFile(filepath.Join(req.OutputDir, "extra"), nil, 0600); err != nil {
			return err
		}
	case "validation-order":
		content = []byte("<a><b><c/></b></a>")
		if err := os.WriteFile(filepath.Join(req.OutputDir, "result"), content, 0600); err != nil {
			return err
		}
		o.Type, o.Size = TypeXML, int64(len(content))
	case "deep-output":
		content, err = os.ReadFile(filepath.Join(req.InputDir, "deep.xml"))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(req.OutputDir, "result"), content, 0600); err != nil {
			return err
		}
		o.Type, o.Size = TypeXML, int64(len(content))
	}
	outputs := []Output{o}
	if mode == "validation-order" {
		outputs = append(outputs, Output{Path: "../escape", Type: TypeText})
	}
	encoded, err := json.Marshal(Manifest{Outputs: outputs})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(req.OutputDir, "manifest.json"), encoded, 0600)
}

func newTestSupervisor(t *testing.T, limits Limits, env []Variable) *Supervisor {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(limits, Recipe{Executable: exe, Environment: env}, NewDirectLauncher(UncontainedForTests{}))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func testAttempt(t *testing.T, mode string) Attempt {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte(mode), 0400); err != nil {
		t.Fatal(err)
	}
	return Attempt{Key: "conversion-" + strings.Repeat("a", 32), InputDir: dir, InputType: TypeText}
}

func assertFailure(t *testing.T, r Receipt, err error, cause Cause, bound Bound) {
	t.Helper()
	defer r.Close()
	var f *Failure
	if !errors.As(err, &f) || f.Cause != cause || f.Bound != bound {
		t.Fatalf("failure = %v, want %s/%s", err, cause, bound)
	}
	if r.Complete || r.Finding == nil || len(r.Outputs) != 0 || r.Envelope.Manifest != "" {
		t.Fatal("failed attempt exposed complete output")
	}
	if r.root != "" {
		t.Fatal("failed attempt retained scratch")
	}
}

func TestPositiveLimits(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []int64{0, -1} {
		for i := 0; i < reflect.TypeOf(Limits{}).NumField(); i++ {
			l := OfficeLimits()
			reflect.ValueOf(&l).Elem().Field(i).SetInt(bad)
			if _, err := New(l, Recipe{Executable: exe}, NewDirectLauncher(UncontainedForTests{})); err == nil {
				t.Fatalf("field %d accepted %d", i, bad)
			}
		}
	}
	if err := PDFLimits().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestResourceOverruns(t *testing.T) {
	for _, tc := range []struct {
		mode   string
		bound  Bound
		change func(*Limits)
	}{
		{"cpu", BoundCPU, func(l *Limits) { l.CPUTime = 200 * time.Millisecond }},
		{"scratch", BoundScratch, func(l *Limits) { l.ScratchBytes = 32 << 10 }},
		{"output", BoundOutput, func(l *Limits) { l.OutputBytes = 32 << 10 }},
		{"stdout", BoundOutput, func(l *Limits) { l.OutputBytes = 32 << 10 }},
		{"files", BoundFiles, func(l *Limits) { l.TempFiles = 20 }},
		{"success", BoundMemory, func(l *Limits) { l.MemoryBytes = 1 }},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			l := OfficeLimits()
			l.WallTime = 15 * time.Second
			l.StartupTime = min(l.StartupTime, l.WallTime)
			l.TeardownTime = min(l.TeardownTime, l.WallTime)
			tc.change(&l)
			s := newTestSupervisor(t, l, nil)
			r, err := s.Run(context.Background(), testAttempt(t, tc.mode))
			assertFailure(t, r, err, ResourceLimit, tc.bound)
		})
	}
}

func TestRejectManifestBeforeOpeningOutputs(t *testing.T) {
	for _, mode := range []string{"traversal", "absolute", "unnormalized", "symlink", "parent-symlink", "size", "type", "undeclared"} {
		t.Run(mode, func(t *testing.T) {
			s := newTestSupervisor(t, OfficeLimits(), nil)
			r, err := s.Run(context.Background(), testAttempt(t, mode))
			assertFailure(t, r, err, UnsafeOutput, "")
		})
	}
}

func TestValidateAllDeclarationsBeforePreflight(t *testing.T) {
	l := OfficeLimits()
	l.XMLDepth = 1
	r, err := newTestSupervisor(t, l, nil).Run(context.Background(), testAttempt(t, "validation-order"))
	// Reading the first valid declaration would trip XMLDepth. The unsafe
	// second declaration must win before any declared output is parsed.
	assertFailure(t, r, err, UnsafeOutput, "")
}

func TestExitFailure(t *testing.T) {
	r, err := newTestSupervisor(t, OfficeLimits(), nil).Run(context.Background(), testAttempt(t, "exit"))
	assertFailure(t, r, err, ExitFailure, "")
}

func TestSuccessHashesAndCompleteEnvelope(t *testing.T) {
	s := newTestSupervisor(t, OfficeLimits(), nil)
	r, err := s.Run(context.Background(), testAttempt(t, "success"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !r.Complete || r.Finding != nil || len(r.Outputs) != 1 {
		t.Fatalf("incomplete receipt: %+v", r)
	}
	if err := r.Envelope.Validate(); err != nil {
		t.Fatal(err)
	}
	f, err := r.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	if string(r.Outputs[0].SHA256) != hex.EncodeToString(hash[:]) || r.Outputs[0].ByteSize != int64(len(data)) {
		t.Fatal("output was not hashed")
	}
	encoded, err := json.Marshal(r.Outputs)
	if err != nil {
		t.Fatal(err)
	}
	hash = sha256.Sum256(encoded)
	if string(r.Envelope.Manifest) != hex.EncodeToString(hash[:]) {
		t.Fatal("envelope does not bind output digests")
	}
	root := r.root
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("private scratch survived Close")
	}
}

func TestOnlyDeclaredEnvironment(t *testing.T) {
	t.Setenv("SANDBOX_PARENT_MARKER", "must not reach child")
	s := newTestSupervisor(t, OfficeLimits(), []Variable{{Name: "LANG", Value: "C"}})
	r, err := s.Run(context.Background(), testAttempt(t, "environment"))
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
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "TMPDIR="+filepath.Join(r.root, "temp")+"\nLANG=C" {
		t.Fatal("child environment differs from declared variables")
	}
}

func TestRejectConnectionEnvironment(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []Variable{{Name: "DATABASE_URL", Value: "synthetic"}, {Name: "TZ", Value: "/tmp/socket"}, {Name: "LANG", Value: "https://example.invalid"}} {
		if _, err := New(OfficeLimits(), Recipe{Executable: exe, Environment: []Variable{v}}, NewDirectLauncher(UncontainedForTests{})); err == nil {
			t.Fatal("accepted connection-capable environment")
		}
	}
}

func TestProcessGroupTermination(t *testing.T) {
	for _, cancelAttempt := range []bool{false, true} {
		t.Run(strconv.FormatBool(cancelAttempt), func(t *testing.T) {
			base := t.TempDir()
			t.Setenv("TMPDIR", base)
			l := OfficeLimits()
			l.WallTime = 5 * time.Second
			l.StartupTime = min(l.StartupTime, l.WallTime)
			l.TeardownTime = min(l.TeardownTime, l.WallTime)
			s := newTestSupervisor(t, l, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				receipt Receipt
				err     error
			}
			done := make(chan result, 1)
			a := testAttempt(t, "tree")
			go func() { r, err := s.Run(ctx, a); done <- result{r, err} }()
			pid := waitReady(t, base, done)
			if cancelAttempt {
				cancel()
			}
			var resultValue result
			select {
			case resultValue = <-done:
			case <-time.After(15 * time.Second):
				t.Fatal("group termination did not finish")
			}
			cause := Timeout
			if cancelAttempt {
				cause = Cancelled
			}
			assertFailure(t, resultValue.receipt, resultValue.err, cause, "")
			// The supervisor reaps this adopted grandchild and records SIGKILL,
			// then prove its PID no longer exists. A zombie is not a test pass.
			var status syscall.WaitStatus
			got := 0
			var err error
			for _, report := range resultValue.receipt.termination {
				if report != nil {
					if reaped, ok := report.Reaped[pid]; ok {
						got, status = pid, reaped
					}
				}
			}
			if got == 0 {
				err = fmt.Errorf("missing descendant reap record")
			}
			if err != nil || got != pid || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatalf("grandchild termination: pid=%d status=%v err=%v", got, status, err)
			}
			methods := resultValue.receipt.TerminationMechanisms()
			if len(methods) != 1 || !validIdentityMechanism(methods[0]) {
				t.Fatalf("descendant termination mechanism = %v", methods)
			}
			if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
				t.Fatalf("grandchild still exists: %v", err)
			}
		})
	}
}

// waitReady uses a child-written handshake and a deadline, never an assumed
// sleep duration. It proves the grandchild started before triggering cancellation.
func waitReady[T any](t *testing.T, base string, done <-chan T) int {
	t.Helper()
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		files, err := filepath.Glob(filepath.Join(base, "corpus-attempt-*", "temp", "ready"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 1 {
			data, err := os.ReadFile(files[0])
			if err == nil {
				pid, err := strconv.Atoi(string(data))
				if err == nil && pid > 0 {
					return pid
				}
			}
		}
		select {
		case <-done:
			t.Fatal("attempt exited before child handshake")
		case <-timer.C:
			t.Fatal("grandchild handshake deadline")
		case <-ticker.C:
		}
	}
}

func TestCancelledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := newTestSupervisor(t, OfficeLimits(), nil).Run(ctx, testAttempt(t, "success"))
	assertFailure(t, r, err, Cancelled, "")
}
