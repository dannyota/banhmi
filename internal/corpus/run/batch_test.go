package run

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func testBatchBudget(items, concurrency int) Budget {
	return Budget{
		MaxItems: items, MaxInputBytes: 1 << 20, MaxPages: 100,
		MaxElapsed: time.Minute, MaxConcurrent: concurrency,
	}
}

func TestKeysetPagesAndLimit(t *testing.T) {
	var cursors []int64
	list := func(_ context.Context, afterID int64, limit int) ([]BatchItem, error) {
		cursors = append(cursors, afterID)
		all := []int64{2, 4, 7, 9, 11}
		var page []BatchItem
		for _, id := range all {
			if id > afterID && len(page) < limit {
				page = append(page, BatchItem{ID: id, InputBytes: 1, Pages: 1})
			}
		}
		return page, nil
	}

	var mu sync.Mutex
	var processed []int64
	first, err := RunKeysetBatch(context.Background(), BatchConfig{
		Stage: "index", Budget: testBatchBudget(2, 2),
	}, list, func(_ context.Context, id int64) error {
		mu.Lock()
		processed = append(processed, id)
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("RunKeysetBatch: %v", err)
	}
	if first != (BatchResult{Attempted: 2, Succeeded: 2, NextCursor: 4}) {
		t.Fatalf("first result = %+v", first)
	}
	second, err := RunKeysetBatch(context.Background(), BatchConfig{
		Stage: "index", AfterID: first.NextCursor, Budget: testBatchBudget(2, 2),
	}, list, func(_ context.Context, id int64) error {
		mu.Lock()
		processed = append(processed, id)
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("second RunKeysetBatch: %v", err)
	}
	if second != (BatchResult{Attempted: 2, Succeeded: 2, NextCursor: 9}) {
		t.Fatalf("second result = %+v", second)
	}
	if !reflect.DeepEqual(cursors, []int64{0, 4}) {
		t.Fatalf("cursors = %v, want [0 4]", cursors)
	}
	if len(processed) != 4 {
		t.Fatalf("processed = %v, want four IDs", processed)
	}
}

func TestKeysetContinuesBatchThenReturnsCountedError(t *testing.T) {
	listCalls := 0
	list := func(_ context.Context, _ int64, _ int) ([]BatchItem, error) {
		listCalls++
		return []BatchItem{
			{ID: 1, InputBytes: 1, Pages: 1},
			{ID: 2, InputBytes: 1, Pages: 1},
			{ID: 3, InputBytes: 1, Pages: 1},
		}, nil
	}
	var mu sync.Mutex
	var processed []int64
	wantErr := errors.New("bad document")
	result, err := RunKeysetBatch(context.Background(), BatchConfig{
		Stage: "normalize", Budget: testBatchBudget(3, 2),
	}, list, func(_ context.Context, id int64) error {
		mu.Lock()
		processed = append(processed, id)
		mu.Unlock()
		if id == 2 {
			return wantErr
		}
		return nil
	})
	if result != (BatchResult{Attempted: 3, Succeeded: 2, Failed: 1, NextCursor: 3, Complete: true}) {
		t.Fatalf("result = %+v", result)
	}
	var batchErr *BatchError
	if !errors.As(err, &batchErr) {
		t.Fatalf("error = %v, want *BatchError", err)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want wrapped item error", err)
	}
	if batchErr.Result != result {
		t.Fatalf("error result = %+v, want %+v", batchErr.Result, result)
	}
	if len(processed) != 3 {
		t.Fatalf("processed = %v, want the full listed batch", processed)
	}
	if listCalls != 1 {
		t.Fatalf("list calls = %d, want stop after failed batch", listCalls)
	}
}

func TestKeysetCancellationStopsScheduling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := RunKeysetBatch(ctx, BatchConfig{
		Stage: "extract", Budget: testBatchBudget(4, 1),
	}, func(context.Context, int64, int) ([]BatchItem, error) {
		return []BatchItem{
			{ID: 1, InputBytes: 1, Pages: 1},
			{ID: 2, InputBytes: 1, Pages: 1},
			{ID: 3, InputBytes: 1, Pages: 1},
			{ID: 4, InputBytes: 1, Pages: 1},
		}, nil
	}, func(_ context.Context, id int64) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if result.Attempted != 1 || result.Succeeded != 1 {
		t.Fatalf("result = %+v, want only the in-flight item", result)
	}
}

func TestKeysetReturnsCountedListError(t *testing.T) {
	wantErr := errors.New("list unavailable")
	calls := 0
	result, err := RunKeysetBatch(context.Background(), BatchConfig{
		Stage: "index", Budget: testBatchBudget(2, 1),
	}, func(_ context.Context, _ int64, _ int) ([]BatchItem, error) {
		calls++
		return nil, wantErr
	}, func(context.Context, int64) error { return nil })
	if result != (BatchResult{}) {
		t.Fatalf("result = %+v", result)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want list error", err)
	}
	if calls != 1 {
		t.Fatalf("list calls = %d, want 1", calls)
	}
}

func TestKeysetRejectsNonAdvancingCursor(t *testing.T) {
	calls := 0
	_, err := RunKeysetBatch(context.Background(), BatchConfig{
		Stage: "index", Budget: testBatchBudget(1, 1),
	}, func(_ context.Context, _ int64, _ int) ([]BatchItem, error) {
		calls++
		return []BatchItem{{ID: 0, InputBytes: 1, Pages: 1}}, nil
	}, func(context.Context, int64) error { return nil })
	var batchErr *BatchError
	if !errors.As(err, &batchErr) {
		t.Fatalf("error = %v, want *BatchError", err)
	}
	if calls != 1 {
		t.Fatalf("list calls = %d, want 1", calls)
	}
}

func TestKeysetStopsAtDeclaredByteAndPageBudgets(t *testing.T) {
	items := []BatchItem{
		{ID: 1, InputBytes: 4, Pages: 2},
		{ID: 2, InputBytes: 5, Pages: 1},
		{ID: 3, InputBytes: 1, Pages: 1},
	}
	var processed []int64
	result, err := RunKeysetBatch(context.Background(), BatchConfig{
		Stage: "extract",
		Budget: Budget{
			MaxItems: 3, MaxInputBytes: 8, MaxPages: 3,
			MaxElapsed: time.Minute, MaxConcurrent: 1,
		},
	}, func(context.Context, int64, int) ([]BatchItem, error) {
		return items, nil
	}, func(_ context.Context, id int64) error {
		processed = append(processed, id)
		return nil
	})
	if err != nil {
		t.Fatalf("RunKeysetBatch: %v", err)
	}
	if result != (BatchResult{Attempted: 1, Succeeded: 1, NextCursor: 1}) {
		t.Fatalf("result = %+v", result)
	}
	if !reflect.DeepEqual(processed, []int64{1}) {
		t.Fatalf("processed = %v, want [1]", processed)
	}
}

func TestKeysetRejectsOneOversizeDeclaredInput(t *testing.T) {
	result, err := RunKeysetBatch(context.Background(), BatchConfig{
		Stage: "extract",
		Budget: Budget{
			MaxItems: 1, MaxInputBytes: 8, MaxPages: 1,
			MaxElapsed: time.Minute, MaxConcurrent: 1,
		},
	}, func(context.Context, int64, int) ([]BatchItem, error) {
		return []BatchItem{{ID: 9, InputBytes: 9, Pages: 1}}, nil
	}, func(context.Context, int64) error {
		t.Fatal("process called for rejected input")
		return nil
	})
	var batchErr *BatchError
	if !errors.As(err, &batchErr) {
		t.Fatalf("error = %v, want *BatchError", err)
	}
	if result != (BatchResult{Rejected: 1, NextCursor: 9, Complete: true}) {
		t.Fatalf("result = %+v", result)
	}
}
