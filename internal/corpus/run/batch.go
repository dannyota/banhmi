// Package run contains bounded helpers shared by direct corpus runners.
package run

import (
	"context"
	"fmt"
	"sync"
)

// BatchResult counts work that started and its outcome.
type BatchResult struct {
	Attempted  int
	Succeeded  int
	Failed     int
	Rejected   int
	NextCursor int64
	Complete   bool
}

// Add combines another bounded batch result.
func (r *BatchResult) Add(other BatchResult) {
	r.Attempted += other.Attempted
	r.Succeeded += other.Succeeded
	r.Failed += other.Failed
	r.Rejected += other.Rejected
	r.NextCursor = other.NextCursor
	r.Complete = other.Complete
}

// BatchError reports partial counts when listing or processing fails.
type BatchError struct {
	Stage  string
	Result BatchResult
	Cause  error
}

func (e *BatchError) Error() string {
	return fmt.Sprintf("%s failed: attempted=%d succeeded=%d rejected=%d failed=%d next_cursor=%d: %v",
		e.Stage, e.Result.Attempted, e.Result.Succeeded, e.Result.Rejected,
		e.Result.Failed, e.Result.NextCursor, e.Cause)
}

// Unwrap exposes the first item, listing, or cancellation error.
func (e *BatchError) Unwrap() error { return e.Cause }

// BatchConfig bounds one keyset-paged stage.
type BatchConfig struct {
	Stage   string
	AfterID int64
	Limit   int
	Budget  Budget
}

// BatchItem is one keyset item plus positive scheduling metadata. InputBytes and
// Pages bound declared invocation metadata. Callers must not treat those values
// as measured parser input unless their listing query measured the source.
type BatchItem struct {
	ID         int64
	InputBytes int64
	Pages      int
}

// BatchListFunc returns items greater than afterID in ascending order.
type BatchListFunc func(ctx context.Context, afterID int64, limit int) ([]BatchItem, error)

// BatchProcessFunc handles one listed ID.
type BatchProcessFunc func(ctx context.Context, id int64) error

// RunKeysetBatch lists bounded pages and processes each page with a fixed worker count.
// Item failures do not stop the rest of the current page. A failed page stops
// further listing and returns a BatchError with all counts accumulated so far.
func RunKeysetBatch(ctx context.Context, cfg BatchConfig, list BatchListFunc, process BatchProcessFunc) (BatchResult, error) {
	var total BatchResult
	if cfg.Stage == "" {
		cfg.Stage = "batch"
	}
	if err := cfg.Budget.Validate(); err != nil {
		return total, &BatchError{Stage: cfg.Stage, Cause: fmt.Errorf("budget: %w", err)}
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Budget.MaxElapsed)
	defer cancel()
	itemLimit := cfg.Budget.MaxItems
	if cfg.Limit > 0 && cfg.Limit < itemLimit {
		itemLimit = cfg.Limit
	}
	maxInt := int(^uint(0) >> 1)
	if itemLimit == maxInt {
		return total, &BatchError{Stage: cfg.Stage, Cause: fmt.Errorf("max items must be less than %d", maxInt)}
	}
	items, err := list(ctx, cfg.AfterID, itemLimit+1)
	if err != nil {
		return total, &BatchError{Stage: cfg.Stage, Result: total, Cause: fmt.Errorf("list after %d: %w", cfg.AfterID, err)}
	}
	if len(items) == 0 {
		total.Complete = true
		return total, nil
	}
	total.Complete = len(items) <= itemLimit
	if len(items) > itemLimit {
		items = items[:itemLimit]
	}
	previousID := cfg.AfterID
	var declaredBytes int64
	declaredPages := 0
	ids := make([]int64, 0, len(items))
	var admissionErr error
	for _, item := range items {
		if item.ID <= previousID {
			cause := fmt.Errorf("non-advancing cursor: after=%d next=%d", previousID, item.ID)
			return total, &BatchError{Stage: cfg.Stage, Result: total, Cause: cause}
		}
		if item.InputBytes <= 0 || item.Pages <= 0 {
			total.Rejected++
			total.NextCursor = item.ID
			admissionErr = fmt.Errorf("item %d has nonpositive declared bytes or pages", item.ID)
			previousID = item.ID
			continue
		}
		if item.InputBytes > cfg.Budget.MaxInputBytes || item.Pages > cfg.Budget.MaxPages {
			total.Rejected++
			total.NextCursor = item.ID
			admissionErr = fmt.Errorf("item %d exceeds its declared byte or page budget", item.ID)
			previousID = item.ID
			continue
		}
		if item.InputBytes > cfg.Budget.MaxInputBytes-declaredBytes || item.Pages > cfg.Budget.MaxPages-declaredPages {
			total.Complete = false
			break
		}
		declaredBytes += item.InputBytes
		declaredPages += item.Pages
		ids = append(ids, item.ID)
		previousID = item.ID
	}
	if len(ids) == 0 {
		if admissionErr != nil {
			return total, &BatchError{Stage: cfg.Stage, Result: total, Cause: admissionErr}
		}
		cause := fmt.Errorf("item %d exceeds the remaining declared byte or page budget", items[0].ID)
		return total, &BatchError{Stage: cfg.Stage, Result: total, Cause: cause}
	}
	if total.NextCursor < ids[len(ids)-1] {
		total.NextCursor = ids[len(ids)-1]
	}
	page, firstErr := processPage(ctx, ids, cfg.Budget.MaxConcurrent, process)
	total.Attempted = page.Attempted
	total.Succeeded = page.Succeeded
	total.Failed = page.Failed
	if ctx.Err() != nil {
		total.NextCursor = cfg.AfterID
		total.Complete = false
	}
	if firstErr != nil {
		return total, &BatchError{Stage: cfg.Stage, Result: total, Cause: firstErr}
	}
	if admissionErr != nil {
		return total, &BatchError{Stage: cfg.Stage, Result: total, Cause: admissionErr}
	}
	if err := ctx.Err(); err != nil {
		return total, &BatchError{Stage: cfg.Stage, Result: total, Cause: err}
	}
	return total, nil
}

func processPage(ctx context.Context, ids []int64, concurrency int, process BatchProcessFunc) (BatchResult, error) {
	jobs := make(chan int64)
	var (
		result   BatchResult
		firstErr error
		mu       sync.Mutex
		wg       sync.WaitGroup
	)
	workers := min(concurrency, len(ids))
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				if ctx.Err() != nil {
					return
				}
				itemErr := process(ctx, id)
				mu.Lock()
				result.Attempted++
				if itemErr != nil {
					result.Failed++
					if firstErr == nil {
						firstErr = fmt.Errorf("item %d: %w", id, itemErr)
					}
				} else {
					result.Succeeded++
				}
				mu.Unlock()
			}
		}()
	}

sendLoop:
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		select {
		case jobs <- id:
		case <-ctx.Done():
			break sendLoop
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil && firstErr == nil {
		firstErr = err
	}
	return result, firstErr
}
