//go:build linux

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The intermediate child exits after starting a writer. Its stdout pipe gives
// the subreaper a readiness handshake without keeping exec.Cmd.Wait blocked.
func teardownProbe() int {
	if err := CloseInheritedDescriptors(); err != nil {
		return 1
	}
	if len(os.Args) == 2 {
		exe, err := os.Executable()
		if err != nil {
			return 1
		}
		cmd := exec.Command(exe, "--sandbox-teardown-probe", "writer")
		cmd.Env = []string{}
		cmd.Stdout = os.Stdout
		if err := cmd.Start(); err != nil {
			return 1
		}
		return 0
	}
	if len(os.Args) != 3 || os.Args[2] != "writer" {
		return 1
	}
	if _, err := fmt.Fprintln(os.Stdout, os.Getpid()); err != nil {
		return 1
	}
	for {
		_ = syscall.Pause()
	}
}

func TestDescendantTerminationInterruptedPoll(t *testing.T) {
	for _, tc := range []struct {
		name        string
		interruptAt int
		permanent   bool
		allowance   time.Duration
	}{
		{name: "before-kill", interruptAt: 1, allowance: 5 * time.Second},
		{name: "after-kill", interruptAt: 2, allowance: 5 * time.Second},
		{name: "until-deadline", interruptAt: 2, permanent: true, allowance: time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Use the same exclusion boundary as DirectLauncher. No unrelated
			// sandbox child may start while this test owns its descendant tree.
			directSlot <- struct{}{}
			defer func() { <-directSlot }()
			if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
				t.Fatal(err)
			}
			processes, err := descendantProcesses(os.Getpid(), nil, time.Now().Add(5*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			baseline := map[int]uint64{}
			for pid, identity := range processes {
				baseline[pid] = identity.start
			}
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "--sandbox-teardown-probe")
			cmd.Env = []string{}
			cmd.Stdout = writer
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				// Cover setup failures as well as failed regression assertions.
				// Wait owns the intermediate leader; cleanup owns its descendants.
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				report := &terminationReport{Mechanism: "subreaper-proc", Reaped: map[int]syscall.WaitStatus{}}
				if err := terminateDescendants(baseline, cmd.Process.Pid, report, time.Now().Add(5*time.Second)); err != nil {
					t.Errorf("clean up probe descendants: %v", err)
				}
			}()
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			// Reaping the intermediate parent establishes adoption before the
			// scanner or poll seam runs. The writer still holds stdout open.
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			type readiness struct {
				pid int
				err error
			}
			ready := make(chan readiness, 1)
			go func() {
				var pid int
				_, err := fmt.Fscan(reader, &pid)
				ready <- readiness{pid: pid, err: err}
			}()
			var pid int
			select {
			case result := <-ready:
				if result.err != nil || result.pid <= 0 {
					t.Fatalf("descendant readiness: pid=%d err=%v", result.pid, result.err)
				}
				pid = result.pid
			case <-time.After(5 * time.Second):
				t.Fatal("descendant readiness did not finish")
			}
			identity, err := readProcIdentity(pid)
			if err != nil || identity.parent != os.Getpid() {
				t.Fatalf("writer was not adopted: identity=%v err=%v", identity, err)
			}
			report := &terminationReport{Mechanism: "subreaper-proc", Reaped: map[int]syscall.WaitStatus{}}
			polls := 0
			poll := func(fds []unix.PollFd, timeout int) (int, error) {
				polls++
				if polls == tc.interruptAt || (tc.permanent && polls > tc.interruptAt) {
					// Check 1 precedes SIGKILL. Check 2 follows SIGKILL and
					// the first nonblocking reap, while exit may be completing.
					return -1, unix.EINTR
				}
				return unix.Poll(fds, timeout)
			}
			err = terminateDescendantsWithPoll(baseline, cmd.Process.Pid, report, time.Now().Add(tc.allowance), poll)
			if tc.permanent {
				if err == nil || !strings.Contains(err.Error(), "UNCONFIRMED") {
					t.Fatalf("permanent interruption did not fail closed at the deadline: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("interrupted descendant confirmation aborted teardown: %v", err)
			}
			status, ok := report.Reaped[pid]
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatalf("adopted writer was not reaped after SIGKILL: recorded=%v status=%v", ok, status)
			}
			if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
				t.Fatalf("adopted writer still exists: %v", err)
			}
		})
	}
}

func TestPidfdConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		revents  int16
		err      error
		wantDead bool
		wantErr  bool
	}{
		{name: "interrupted", revents: unix.POLLIN, err: unix.EINTR},
		{name: "pending"},
		{name: "exited", revents: unix.POLLIN, wantDead: true},
		{name: "reaped", revents: unix.POLLIN | unix.POLLHUP, wantDead: true},
		{name: "read-error", revents: unix.POLLIN, err: syscall.EIO, wantErr: true},
		{name: "invalid-fd", revents: unix.POLLIN | unix.POLLNVAL, wantErr: true},
		{name: "poll-error", revents: unix.POLLIN | unix.POLLERR, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &pinnedProcess{fd: 42}
			dead, err := p.dead(func(fds []unix.PollFd, timeout int) (int, error) {
				if len(fds) != 1 || fds[0].Fd != 42 || fds[0].Events != unix.POLLIN || timeout != 0 {
					t.Fatal("confirmation did not poll the pinned identity without blocking")
				}
				fds[0].Revents = tc.revents
				if tc.err != nil {
					return -1, tc.err
				}
				if tc.revents == 0 {
					return 0, nil
				}
				return 1, nil
			})
			if dead != tc.wantDead || (err != nil) != tc.wantErr {
				t.Fatalf("unconfirmed process reported gone or wrong error: dead=%v err=%v", dead, err)
			}
			if tc.wantErr && tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("confirmation lost the poll error: %v", err)
			}
		})
	}
}
