package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	corpusrun "danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/pkg/pipeline"
)

func TestRunStageAllReturnsCountedItemFailure(t *testing.T) {
	wantErr := errors.New("extract failed")
	err := runStageAll(context.Background(), "extract", 0, 0, false,
		func(context.Context, int64, int32) ([]int64, error) {
			return []int64{1, 2, 3}, nil
		},
		func(_ context.Context, id int64) error {
			if id == 2 {
				return wantErr
			}
			return nil
		},
		slog.New(slog.DiscardHandler),
	)
	var batchErr *corpusrun.BatchError
	if !errors.As(err, &batchErr) {
		t.Fatalf("error = %v, want *run.BatchError", err)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want wrapped process error", err)
	}
	if batchErr.Result != (corpusrun.BatchResult{
		Attempted: 3, Succeeded: 2, Failed: 1, NextCursor: 3, Complete: true,
	}) {
		t.Fatalf("result = %+v", batchErr.Result)
	}
}

func TestValidateRunOptsRejectsForcedRunAllBeforeStartup(t *testing.T) {
	err := validateRunOpts(runOpts{runAll: true, force: true})
	if err == nil {
		t.Fatal("validateRunOpts accepted -run-all -force")
	}
	if err := validateRunOpts(runOpts{embedAll: true, force: true, afterID: 20}); err != nil {
		t.Fatalf("validateRunOpts rejected resumable forced stage: %v", err)
	}
}

func TestRunStageAllReturnsResumeCursorForIncompleteShard(t *testing.T) {
	err := runStageAll(context.Background(), "index", 40, 0, false,
		func(_ context.Context, afterID int64, limit int32) ([]int64, error) {
			ids := make([]int64, limit)
			for i := range ids {
				ids[i] = afterID + int64(i) + 1
			}
			return ids, nil
		},
		func(context.Context, int64) error { return nil },
		slog.New(slog.DiscardHandler),
	)
	var batchErr *corpusrun.BatchError
	if !errors.As(err, &batchErr) {
		t.Fatalf("error = %v, want *run.BatchError", err)
	}
	if batchErr.Result.NextCursor != 240 || batchErr.Result.Complete {
		t.Fatalf("result = %+v, want incomplete cursor 240", batchErr.Result)
	}
}

func TestRunFetchBatchReturnsCountedFailure(t *testing.T) {
	wantErr := errors.New("download failed")
	artifacts := []pipeline.ClaimedArtifact{{ID: 1}, {ID: 2}, {ID: 3}}
	result, err := runFetchBatch(context.Background(), artifacts, 2,
		func(_ context.Context, artifact pipeline.ClaimedArtifact) error {
			if artifact.ID == 2 {
				return wantErr
			}
			return nil
		})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want fetch failure", err)
	}
	if result.Attempted != 3 || result.Succeeded != 2 || result.Failed != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestRunFetchBatchCountsUnprocessedCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	artifacts := []pipeline.ClaimedArtifact{{ID: 1}, {ID: 2}, {ID: 3}}
	result, err := runFetchBatch(ctx, artifacts, 1,
		func(_ context.Context, _ pipeline.ClaimedArtifact) error {
			cancel()
			return nil
		})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
	if result.Attempted != 3 || result.Succeeded != 1 || result.Failed != 2 {
		t.Fatalf("result = %+v", result)
	}
}

func TestCountStageAllReturnsPartialCount(t *testing.T) {
	wantErr := errors.New("normalize failed")
	completed, err := countStageAll(context.Background(), "normalize",
		func(context.Context, int64, int32) ([]int64, error) {
			return []int64{10, 11}, nil
		},
		func(_ context.Context, id int64) error {
			if id == 11 {
				return wantErr
			}
			return nil
		},
		slog.New(slog.DiscardHandler),
	)
	if completed != 1 {
		t.Fatalf("completed = %d, want 1", completed)
	}
	var batchErr *corpusrun.BatchError
	if !errors.As(err, &batchErr) || batchErr.Result.Failed != 1 {
		t.Fatalf("error = %v, want one failed item", err)
	}
}
