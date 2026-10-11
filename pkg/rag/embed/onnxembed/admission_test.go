package onnxembed

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewAdmissionRejectsNonPositiveLimits(t *testing.T) {
	tests := []AdmissionConfig{
		{MaxBatchTexts: 0, MaxInputBytes: 1, MaxQueued: 1, MaxConcurrent: 1},
		{MaxBatchTexts: 1, MaxInputBytes: 0, MaxQueued: 1, MaxConcurrent: 1},
		{MaxBatchTexts: 1, MaxInputBytes: 1, MaxQueued: 0, MaxConcurrent: 1},
		{MaxBatchTexts: 1, MaxInputBytes: 1, MaxQueued: 1, MaxConcurrent: 0},
	}
	for _, cfg := range tests {
		if _, err := NewAdmission(cfg); err == nil {
			t.Errorf("NewAdmission(%+v) error = nil, want validation error", cfg)
		}
	}
}

func TestAdmissionAcceptsExactInputByteLimit(t *testing.T) {
	a := mustAdmission(t, AdmissionConfig{MaxBatchTexts: 1, MaxInputBytes: 4, MaxQueued: 1, MaxConcurrent: 1})
	if err := a.ValidateInput("1234"); err != nil {
		t.Fatalf("ValidateInput at boundary: %v", err)
	}
	if err := a.ValidateInput("12345"); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("ValidateInput over boundary error = %v, want ErrInputTooLarge", err)
	}
}

func TestAdmissionAcceptsExactBatchLimit(t *testing.T) {
	a := mustAdmission(t, AdmissionConfig{MaxBatchTexts: 2, MaxInputBytes: 4, MaxQueued: 1, MaxConcurrent: 1})
	if err := a.ValidateBatch(2); err != nil {
		t.Fatalf("ValidateBatch at boundary: %v", err)
	}
	if err := a.ValidateBatch(3); !errors.Is(err, ErrBatchTooLarge) {
		t.Fatalf("ValidateBatch over boundary error = %v, want ErrBatchTooLarge", err)
	}
}

func TestAdmissionRejectsFullQueueAndReleasesCancelledWaiter(t *testing.T) {
	a := mustAdmission(t, AdmissionConfig{MaxBatchTexts: 1, MaxInputBytes: 4, MaxQueued: 1, MaxConcurrent: 1})
	releaseActive, err := a.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire active: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	waitErr := make(chan error, 1)
	go func() {
		_, err := a.Acquire(ctx)
		waitErr <- err
	}()
	waitForQueued(t, a, 1)

	if _, err := a.Acquire(context.Background()); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("Acquire with full queue error = %v, want ErrOverloaded", err)
	}
	cancel()
	if err := <-waitErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter error = %v, want context.Canceled", err)
	}
	waitForQueued(t, a, 0)

	releaseActive()
	releaseActive()
	release, err := a.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire after releases: %v", err)
	}
	release()
}

func mustAdmission(t *testing.T, cfg AdmissionConfig) *Admission {
	t.Helper()
	a, err := NewAdmission(cfg)
	if err != nil {
		t.Fatalf("NewAdmission: %v", err)
	}
	return a
}

func waitForQueued(t *testing.T, a *Admission, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for len(a.queued) != want {
		if time.Now().After(deadline) {
			t.Fatalf("queued = %d, want %d", len(a.queued), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAdmissionCountsUTF8Bytes(t *testing.T) {
	a := mustAdmission(t, AdmissionConfig{MaxBatchTexts: 1, MaxInputBytes: 3, MaxQueued: 1, MaxConcurrent: 1})
	if err := a.ValidateInput("ệ"); err != nil {
		t.Fatalf("ValidateInput three-byte rune: %v", err)
	}
	if err := a.ValidateInput(strings.Repeat("ệ", 2)); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("ValidateInput six-byte text error = %v, want ErrInputTooLarge", err)
	}
}
