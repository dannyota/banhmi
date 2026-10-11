package onnxembed

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	ErrOverloaded    = errors.New("embedding inference is overloaded")
	ErrBatchTooLarge = errors.New("batch too large")
	ErrInputTooLarge = errors.New("embedding input is too large")
)

type AdmissionConfig struct {
	MaxBatchTexts int
	MaxInputBytes int
	MaxQueued     int
	MaxConcurrent int
}

// Admission bounds active inference and callers waiting to start it. Admission
// is a protective work limit, not a hard bound on ONNX Runtime native memory.
type Admission struct {
	maxBatchTexts int
	maxInputBytes int
	queued        chan struct{}
	concurrent    chan struct{}
}

func NewAdmission(cfg AdmissionConfig) (*Admission, error) {
	if cfg.MaxBatchTexts <= 0 {
		return nil, fmt.Errorf("max batch texts must be positive: %d", cfg.MaxBatchTexts)
	}
	if cfg.MaxInputBytes <= 0 {
		return nil, fmt.Errorf("max input bytes must be positive: %d", cfg.MaxInputBytes)
	}
	if cfg.MaxQueued <= 0 {
		return nil, fmt.Errorf("max queued must be positive: %d", cfg.MaxQueued)
	}
	if cfg.MaxConcurrent <= 0 {
		return nil, fmt.Errorf("max concurrent must be positive: %d", cfg.MaxConcurrent)
	}
	return &Admission{
		maxBatchTexts: cfg.MaxBatchTexts,
		maxInputBytes: cfg.MaxInputBytes,
		queued:        make(chan struct{}, cfg.MaxQueued),
		concurrent:    make(chan struct{}, cfg.MaxConcurrent),
	}, nil
}

func (a *Admission) ValidateBatch(texts int) error {
	if texts > a.maxBatchTexts {
		return fmt.Errorf("%w: %d texts exceeds %d text limit", ErrBatchTooLarge, texts, a.maxBatchTexts)
	}
	return nil
}

func (a *Admission) ValidateInput(text string) error {
	if len(text) > a.maxInputBytes {
		return fmt.Errorf("%w: %d bytes exceeds %d byte limit", ErrInputTooLarge, len(text), a.maxInputBytes)
	}
	return nil
}

// Acquire reserves bounded waiting capacity, then waits for an active slot.
// The returned release function is idempotent so every successful acquisition
// can use one deferred release path.
func (a *Admission) Acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case a.concurrent <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-a.concurrent
			return nil, err
		}
		return a.releaseFunc(), nil
	default:
	}

	select {
	case a.queued <- struct{}{}:
	default:
		return nil, ErrOverloaded
	}

	select {
	case a.concurrent <- struct{}{}:
		<-a.queued
		if err := ctx.Err(); err != nil {
			<-a.concurrent
			return nil, err
		}
		return a.releaseFunc(), nil
	case <-ctx.Done():
		<-a.queued
		return nil, ctx.Err()
	}
}

func (a *Admission) releaseFunc() func() {
	var once sync.Once
	return func() {
		once.Do(func() { <-a.concurrent })
	}
}
