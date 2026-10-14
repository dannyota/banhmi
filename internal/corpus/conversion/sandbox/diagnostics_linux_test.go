//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestHandlerFailureDiagnostic(t *testing.T) {
	r, err := newTestSupervisor(t, OfficeLimits(), nil).Run(context.Background(), testAttempt(t, "exit"))
	assertFailure(t, r, err, ExitFailure, "")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Reason != "HANDLER_FAILED" {
		t.Fatal("handler failure lost its fixed diagnostic reason")
	}
	if strings.Contains(err.Error(), "synthetic handler failure") {
		t.Fatal("handler diagnostics exposed the internal error")
	}
}

func TestFailureDiagnosticPrivacy(t *testing.T) {
	failure := &Failure{Cause: ExitFailure, Reason: "private document payload"}
	if failure.valid() {
		t.Fatal("accepted an arbitrary child diagnostic")
	}
	if strings.Contains(publicError("child protocol", failure).Error(), failure.Reason) {
		t.Fatal("public error exposed an unrecognized diagnostic")
	}
	if strings.Contains((&Failure{Cause: ExitFailure}).Error(), "()") {
		t.Fatal("failure without a bound has an empty public reason")
	}
}

// This launcher records diagnostics only for this package's synthetic children.
// Public receipts and errors must never contain stderr or internal read errors.
type diagnosticDirectLauncher struct {
	*DirectLauncher
	mu      sync.Mutex
	details []string
}

func (l *diagnosticDirectLauncher) prepare(ctx context.Context, recipe Recipe, req ChildRequest, dir string, cpu time.Duration) (*launch, error) {
	job, err := l.DirectLauncher.prepare(ctx, recipe, req, dir, cpu)
	if err != nil {
		l.record(fmt.Sprintf("phase=%s prepare_error=%v", req.Phase, err))
		return nil, err
	}
	terminate := job.terminate
	var teardownErr error
	job.terminate = func() error {
		err := terminate()
		if err != nil {
			teardownErr = err
		}
		return err
	}
	closeJob := job.close
	job.close = func() {
		var stderr []byte
		if stream, ok := job.cmd.Stderr.(*boundedResponse); ok {
			stderr = stream.bytes()
		}
		l.record(fmt.Sprintf("phase=%s child_status=%v stderr=%q teardown_error=%v", req.Phase, job.cmd.ProcessState, stderr, teardownErr))
		closeJob()
	}
	return job, nil
}

func (l *diagnosticDirectLauncher) record(detail string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.details = append(l.details, detail)
}

func (l *diagnosticDirectLauncher) diagnostics() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.details, "\n")
}

func (l *diagnosticDirectLauncher) logOnFailure(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("synthetic child diagnostics:\n%s", l.diagnostics())
		}
	})
}

func TestChildStderrDiagnostic(t *testing.T) {
	s := newTestSupervisor(t, OfficeLimits(), nil)
	l := &diagnosticDirectLauncher{DirectLauncher: NewDirectLauncher(UncontainedForTests{})}
	s.launcher = l
	r, err := s.Run(context.Background(), testAttempt(t, "stderr-exit"))
	assertFailure(t, r, err, ExitFailure, "")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Reason != "CHILD_EXIT_FAILED" {
		t.Fatalf("child exit lost its fixed reason: %v", err)
	}
	if detail := l.diagnostics(); !strings.Contains(detail, "exit status 7") || !strings.Contains(detail, "synthetic child stderr") {
		t.Fatalf("child status or stderr missing: %s", detail)
	}
	if strings.Contains(err.Error(), "synthetic child stderr") {
		t.Fatal("public error exposed child stderr")
	}
}

func TestStderrDiagnosticPrefixBound(t *testing.T) {
	w := &boundedResponse{max: 16 << 10, overflow: make(chan struct{}, 1), prefixOnly: true}
	if _, err := w.Write(bytes.Repeat([]byte{'x'}, 8<<10)); err != nil {
		t.Fatal(err)
	}
	if len(w.bytes()) != 4096 || w.count() != 8<<10 || w.exceeded() {
		t.Fatal("stderr prefix changed full-stream accounting")
	}
	if _, err := w.Write(bytes.Repeat([]byte{'x'}, 16<<10)); err != nil {
		t.Fatal(err)
	}
	if len(w.bytes()) != 4096 || w.count() != 24<<10 || !w.exceeded() {
		t.Fatal("stderr prefix hid output overflow")
	}
	select {
	case <-w.overflow:
	default:
		t.Fatal("stderr overflow did not notify the supervisor")
	}
}

type infrastructureFailureLauncher struct {
	*DirectLauncher
	accounting bool
}

func (l *infrastructureFailureLauncher) prepare(ctx context.Context, recipe Recipe, req ChildRequest, dir string, cpu time.Duration) (*launch, error) {
	job, err := l.DirectLauncher.prepare(ctx, recipe, req, dir, cpu)
	if err != nil {
		return nil, err
	}
	failure := &os.PathError{Op: "read", Path: "/synthetic/private", Err: syscall.EIO}
	if l.accounting {
		job.usage = func() (time.Duration, int64, error) { return 0, 0, failure }
	} else {
		terminate := job.terminate
		job.terminate = func() error {
			if err := terminate(); err != nil {
				return err
			}
			return failure
		}
	}
	return job, nil
}

func TestInfrastructureFailureDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		mode       string
		accounting bool
		reason     string
	}{
		{"tree", true, "CHILD_ACCOUNTING_FAILED"},
		{"success", false, "CHILD_TERMINATION_FAILED"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			s := newTestSupervisor(t, OfficeLimits(), nil)
			s.launcher = &infrastructureFailureLauncher{DirectLauncher: NewDirectLauncher(UncontainedForTests{}), accounting: tc.accounting}
			r, err := s.Run(context.Background(), testAttempt(t, tc.mode))
			assertFailure(t, r, err, ExitFailure, "")
			var failure *Failure
			if !errors.As(err, &failure) || failure.Reason != tc.reason {
				t.Fatalf("infrastructure failure lost its fixed reason: %v", err)
			}
			if strings.Contains(err.Error(), "/synthetic/private") {
				t.Fatal("public error exposed an internal read error")
			}
		})
	}
}
