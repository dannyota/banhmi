//go:build onnx

package onnxembed

import (
	"context"
	"errors"
	"testing"
)

func TestONNXAdmissionConfigDefaultsFiniteQueue(t *testing.T) {
	cfg, err := onnxAdmissionConfig(Config{})
	if err != nil {
		t.Fatalf("onnxAdmissionConfig: %v", err)
	}
	if cfg.MaxConcurrent < 2 {
		t.Fatalf("MaxConcurrent = %d, want at least 2", cfg.MaxConcurrent)
	}
	if cfg.MaxQueued != cfg.MaxConcurrent {
		t.Fatalf("MaxQueued = %d, want resolved concurrency %d", cfg.MaxQueued, cfg.MaxConcurrent)
	}
	if cfg.MaxBatchTexts != 2048 {
		t.Fatalf("MaxBatchTexts = %d, want 2048", cfg.MaxBatchTexts)
	}
}

func TestONNXAdmissionConfigRejectsNegativeQueue(t *testing.T) {
	if _, err := onnxAdmissionConfig(Config{MaxQueued: -1}); err == nil {
		t.Fatal("onnxAdmissionConfig negative MaxQueued error = nil")
	}
}

func TestEmbedRejectsFullQueueBeforeTokenization(t *testing.T) {
	tokenized := make(chan struct{}, 1)
	e := &onnxEmbedder{
		admission: mustAdmission(t, AdmissionConfig{MaxBatchTexts: 1, MaxInputBytes: 32, MaxQueued: 1, MaxConcurrent: 1}),
		tokenize: func(string) ([]uint32, error) {
			tokenized <- struct{}{}
			return []uint32{1}, nil
		},
	}
	release, err := e.admission.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire active slot: %v", err)
	}
	defer release()

	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	queuedDone := make(chan error, 1)
	go func() {
		_, err := e.Embed(queuedCtx, []string{"queued"})
		queuedDone <- err
	}()
	waitForQueued(t, e.admission, 1)

	if _, err := e.Embed(context.Background(), []string{"rejected"}); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("Embed with full queue error = %v, want ErrOverloaded", err)
	}
	if len(tokenized) != 0 {
		t.Fatal("tokenizer called for queued or overloaded input")
	}

	cancelQueued()
	err = <-queuedDone
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Embed error = %v, want context.Canceled", err)
	}
	if len(tokenized) != 0 {
		t.Fatal("tokenizer called before cancelled queued Embed returned")
	}
}

func TestEmbedReleasesInferenceSlotAfterTokenizationError(t *testing.T) {
	wantErr := errors.New("tokenize failed")
	e := &onnxEmbedder{
		admission: mustAdmission(t, AdmissionConfig{MaxBatchTexts: 1, MaxInputBytes: 32, MaxQueued: 1, MaxConcurrent: 1}),
		tokenize: func(string) ([]uint32, error) {
			return nil, wantErr
		},
	}

	if _, err := e.Embed(context.Background(), []string{"query"}); !errors.Is(err, wantErr) {
		t.Fatalf("first Embed error = %v, want tokenize error", err)
	}
	if _, err := e.Embed(context.Background(), []string{"query"}); !errors.Is(err, wantErr) {
		t.Fatalf("second Embed error = %v, want tokenize error", err)
	}
}

func TestEmbedValidatesBoundsBeforeTokenization(t *testing.T) {
	called := false
	e := &onnxEmbedder{
		admission: mustAdmission(t, AdmissionConfig{MaxBatchTexts: 1, MaxInputBytes: 4, MaxQueued: 1, MaxConcurrent: 1}),
		tokenize: func(string) ([]uint32, error) {
			called = true
			return []uint32{1}, nil
		},
	}

	if _, err := e.Embed(context.Background(), []string{"12345"}); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("Embed error = %v, want ErrInputTooLarge", err)
	}
	if called {
		t.Fatal("tokenizer called for oversized input")
	}
}

func TestEmbedRejectsBatchOverLimitBeforeTokenization(t *testing.T) {
	called := false
	e := &onnxEmbedder{
		admission: mustAdmission(t, AdmissionConfig{MaxBatchTexts: 1, MaxInputBytes: 4, MaxQueued: 1, MaxConcurrent: 1}),
		tokenize: func(string) ([]uint32, error) {
			called = true
			return []uint32{1}, nil
		},
	}
	if _, err := e.Embed(context.Background(), []string{"one", "two"}); err == nil {
		t.Fatal("Embed oversized batch error = nil")
	}
	if called {
		t.Fatal("tokenizer called for oversized batch")
	}
}

func TestEmbedReleasesInferenceSlotAfterInferenceError(t *testing.T) {
	wantErr := errors.New("inference failed")
	e := &onnxEmbedder{
		admission: mustAdmission(t, AdmissionConfig{MaxBatchTexts: 1, MaxInputBytes: 32, MaxQueued: 1, MaxConcurrent: 1}),
		tokenize: func(string) ([]uint32, error) {
			return []uint32{1}, nil
		},
		infer: func(context.Context, int64, int64, []int64, []int64, []int64) ([][]float32, error) {
			return nil, wantErr
		},
	}
	for i := 0; i < 2; i++ {
		if _, err := e.Embed(context.Background(), []string{"query"}); !errors.Is(err, wantErr) {
			t.Fatalf("Embed %d error = %v, want inference error", i, err)
		}
	}
}
