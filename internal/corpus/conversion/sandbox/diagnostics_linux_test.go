//go:build linux

package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
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
