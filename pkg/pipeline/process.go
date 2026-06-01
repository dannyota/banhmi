package pipeline

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// StageParams identifies the document stage target by its ledger id; stage
// activities resolve the source/external_id, bronze files, and silver rows from it.
type StageParams struct {
	FetchDocID int64
}

// ListStageFetchDocIDsAfterParams pages fetch_doc IDs that still need one local
// processing stage.
type ListStageFetchDocIDsAfterParams struct {
	AfterID int64
	Limit   int32
	Force   bool
}

// StageAllParams controls paginated and bounded bulk stage execution.
type StageAllParams struct {
	// AfterID is the exclusive lower bound for DB pagination. Use 0 for the first run.
	AfterID int64
	// Limit caps total docs across all fetched pages. 0 means no limit.
	Limit int32
	// BatchSize is the per-page DB page size. 0 means default.
	BatchSize int32
	// MaxConcurrent is the max in-flight stage activities for this workflow run.
	// 0 means default.
	MaxConcurrent int32
	// Force reruns the stage over eligible rows even when target outputs already
	// exist. Used by NormalizeAll and IndexAll for deterministic repair/recompute
	// passes after stage logic changes.
	Force bool
}

// ExtractAllParams is kept as an alias for existing callers.
type ExtractAllParams = StageAllParams

// ExtractResult summarizes an Extract run.
type ExtractResult struct {
	DocumentID        int64 // silver.document id
	Engine            string
	Confidence        float64
	NeedsReview       bool
	SourceUnavailable bool
}

// ExtractAllResult summarizes a batch Extract run.
type ExtractAllResult struct {
	Total             int
	Completed         int
	Failed            int
	NeedsReview       int
	SourceUnavailable int
}

// NormalizeAllResult summarizes a batch Normalize run.
type NormalizeAllResult struct {
	Total                   int
	Completed               int
	Failed                  int
	SectionsWritten         int
	RelationTargetsEnqueued int
	Skipped                 int
}

// IndexAllResult summarizes a batch Index run.
type IndexAllResult struct {
	Total         int
	Completed     int
	Failed        int
	ChunksWritten int
}

// ExtractWorkflow runs only the Extract stage. Fetch never starts it
// automatically; operators or a future orchestrator workflow must advance this
// stage explicitly.
func ExtractWorkflow(ctx workflow.Context, p StageParams) (ExtractResult, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:           LocalActivityTaskQueue(workflow.GetInfo(ctx).TaskQueueName),
		StartToCloseTimeout: 15 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
	})

	var a *Activities
	var res ExtractResult
	if err := workflow.ExecuteActivity(ctx, a.Extract, p).Get(ctx, &res); err != nil {
		return ExtractResult{}, err
	}
	workflow.GetLogger(ctx).Info("extract workflow complete",
		"fetch_doc", p.FetchDocID, "document", res.DocumentID,
		"engine", res.Engine, "confidence", res.Confidence,
		"needs_review", res.NeedsReview, "source_unavailable", res.SourceUnavailable)
	return res, nil
}

// ExtractAllWorkflow fetches docs that still need Extract in DB pages and fans
// out Extract activities, with a bounded in-flight Extract count.
func ExtractAllWorkflow(ctx workflow.Context, p ExtractAllParams) (ExtractAllResult, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:           LocalActivityTaskQueue(workflow.GetInfo(ctx).TaskQueueName),
		StartToCloseTimeout: 15 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
	})

	var a *Activities
	var res ExtractAllResult
	var firstErr error
	selector := workflow.NewSelector(ctx)
	running := 0
	maxConcurrent := int(p.MaxConcurrent)
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	batchSize := int(p.BatchSize)
	if batchSize <= 0 {
		batchSize = 200
	}
	limit := int(p.Limit)
	afterID := p.AfterID

	for {
		var ids []int64
		if err := workflow.ExecuteActivity(ctx, a.ListFetchDocIDsNeedingExtractAfter, ListStageFetchDocIDsAfterParams{
			AfterID: afterID,
			Limit:   int32(batchSize),
		}).Get(ctx, &ids); err != nil {
			return ExtractAllResult{}, fmt.Errorf("list fetch docs needing extract: %w", err)
		}
		if len(ids) == 0 {
			break
		}

		for _, fetchDocID := range ids {
			if limit > 0 && res.Total >= limit {
				break
			}

			if running >= maxConcurrent {
				selector.Select(ctx)
			}

			fetchDocID := fetchDocID
			res.Total++
			future := workflow.ExecuteActivity(ctx, a.Extract, StageParams{FetchDocID: fetchDocID})
			running++
			selector.AddFuture(future, func(f workflow.Future) {
				defer func() { running-- }()

				var one ExtractResult
				if err := f.Get(ctx, &one); err != nil {
					res.Failed++
					if firstErr == nil {
						firstErr = fmt.Errorf("extract fetch_doc %d: %w", fetchDocID, err)
					}
					return
				}

				res.Completed++
				if one.NeedsReview {
					res.NeedsReview++
				}
				if one.SourceUnavailable {
					res.SourceUnavailable++
				}
			})
			afterID = fetchDocID
		}

		for running > 0 {
			selector.Select(ctx)
		}

		if limit > 0 && res.Total >= limit {
			break
		}
		if len(ids) < batchSize {
			break
		}
	}

	workflow.GetLogger(ctx).Info("extract-all workflow complete",
		"total", res.Total, "completed", res.Completed, "failed", res.Failed,
		"needs_review", res.NeedsReview, "source_unavailable", res.SourceUnavailable)
	return res, firstErr
}

// NormalizeWorkflow runs only the Normalize stage. It intentionally skips
// Extract and Index so stage boundaries stay explicit.
func NormalizeWorkflow(ctx workflow.Context, p StageParams) (NormalizeResult, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:           LocalActivityTaskQueue(workflow.GetInfo(ctx).TaskQueueName),
		StartToCloseTimeout: 10 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
	})

	var a *Activities
	var res NormalizeResult
	if err := workflow.ExecuteActivity(ctx, a.Normalize, p).Get(ctx, &res); err != nil {
		return NormalizeResult{}, err
	}
	workflow.GetLogger(ctx).Info("normalize workflow complete",
		"fetch_doc", p.FetchDocID, "document", res.DocumentID,
		"text_authority", res.TextAuthority, "text_source", res.TextSource,
		"sections_parsed", res.SectionsParsed, "sections_written", res.SectionsWritten,
		"articles", res.ArticleCount, "clauses", res.ClauseCount, "points", res.PointCount,
		"relation_targets_enqueued", res.RelationTargetsEnqueued,
		"validity_status", res.ValidityStatusClass,
		"skip_reason", res.SkipReason, "warnings", res.Warnings)
	return res, nil
}

// NormalizeAllWorkflow fetches docs that still need Normalize in DB pages and
// fans out Normalize activities on the local queue.
func NormalizeAllWorkflow(ctx workflow.Context, p StageAllParams) (NormalizeAllResult, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:           LocalActivityTaskQueue(workflow.GetInfo(ctx).TaskQueueName),
		StartToCloseTimeout: 10 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
	})

	var a *Activities
	var res NormalizeAllResult
	var firstErr error
	selector := workflow.NewSelector(ctx)
	running := 0
	maxConcurrent := int(p.MaxConcurrent)
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	batchSize := int(p.BatchSize)
	if batchSize <= 0 {
		batchSize = 200
	}
	limit := int(p.Limit)
	afterID := p.AfterID

	for {
		var ids []int64
		if err := workflow.ExecuteActivity(ctx, a.ListFetchDocIDsNeedingNormalizeAfter, ListStageFetchDocIDsAfterParams{
			AfterID: afterID,
			Limit:   int32(batchSize),
			Force:   p.Force,
		}).Get(ctx, &ids); err != nil {
			return NormalizeAllResult{}, fmt.Errorf("list fetch docs needing normalize: %w", err)
		}
		if len(ids) == 0 {
			break
		}

		for _, fetchDocID := range ids {
			if limit > 0 && res.Total >= limit {
				break
			}
			if running >= maxConcurrent {
				selector.Select(ctx)
			}

			fetchDocID := fetchDocID
			res.Total++
			future := workflow.ExecuteActivity(ctx, a.Normalize, StageParams{FetchDocID: fetchDocID})
			running++
			selector.AddFuture(future, func(f workflow.Future) {
				defer func() { running-- }()

				var one NormalizeResult
				if err := f.Get(ctx, &one); err != nil {
					res.Failed++
					if firstErr == nil {
						firstErr = fmt.Errorf("normalize fetch_doc %d: %w", fetchDocID, err)
					}
					return
				}
				res.Completed++
				res.SectionsWritten += one.SectionsWritten
				res.RelationTargetsEnqueued += one.RelationTargetsEnqueued
				if one.SkipReason != "" {
					res.Skipped++
				}
			})
			afterID = fetchDocID
		}

		for running > 0 {
			selector.Select(ctx)
		}
		if limit > 0 && res.Total >= limit {
			break
		}
		if len(ids) < batchSize {
			break
		}
	}

	workflow.GetLogger(ctx).Info("normalize-all workflow complete",
		"total", res.Total, "completed", res.Completed, "failed", res.Failed,
		"sections_written", res.SectionsWritten,
		"relation_targets_enqueued", res.RelationTargetsEnqueued,
		"skipped", res.Skipped)
	return res, firstErr
}

// IndexWorkflow runs only the Index stage. It assumes Normalize has already
// written silver.document_section rows and only writes gold chunks plus optional
// embeddings.
func IndexWorkflow(ctx workflow.Context, p StageParams) (IndexResult, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:           LocalActivityTaskQueue(workflow.GetInfo(ctx).TaskQueueName),
		StartToCloseTimeout: 10 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
	})

	var a *Activities
	var res IndexResult
	if err := workflow.ExecuteActivity(ctx, a.Index, p).Get(ctx, &res); err != nil {
		return IndexResult{}, err
	}
	workflow.GetLogger(ctx).Info("index workflow complete",
		"fetch_doc", p.FetchDocID, "document", res.DocumentID,
		"chunks", res.ChunksWritten)
	return res, nil
}

// IndexAllWorkflow fetches docs that still need Index in DB pages and fans out
// Index activities on the local queue.
func IndexAllWorkflow(ctx workflow.Context, p StageAllParams) (IndexAllResult, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:           LocalActivityTaskQueue(workflow.GetInfo(ctx).TaskQueueName),
		StartToCloseTimeout: 10 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
	})

	var a *Activities
	var res IndexAllResult
	var firstErr error
	selector := workflow.NewSelector(ctx)
	running := 0
	maxConcurrent := int(p.MaxConcurrent)
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	batchSize := int(p.BatchSize)
	if batchSize <= 0 {
		batchSize = 200
	}
	limit := int(p.Limit)
	afterID := p.AfterID

	for {
		var ids []int64
		if err := workflow.ExecuteActivity(ctx, a.ListFetchDocIDsNeedingIndexAfter, ListStageFetchDocIDsAfterParams{
			AfterID: afterID,
			Limit:   int32(batchSize),
			Force:   p.Force,
		}).Get(ctx, &ids); err != nil {
			return IndexAllResult{}, fmt.Errorf("list fetch docs needing index: %w", err)
		}
		if len(ids) == 0 {
			break
		}

		for _, fetchDocID := range ids {
			if limit > 0 && res.Total >= limit {
				break
			}
			if running >= maxConcurrent {
				selector.Select(ctx)
			}

			fetchDocID := fetchDocID
			res.Total++
			future := workflow.ExecuteActivity(ctx, a.Index, StageParams{FetchDocID: fetchDocID})
			running++
			selector.AddFuture(future, func(f workflow.Future) {
				defer func() { running-- }()

				var one IndexResult
				if err := f.Get(ctx, &one); err != nil {
					res.Failed++
					if firstErr == nil {
						firstErr = fmt.Errorf("index fetch_doc %d: %w", fetchDocID, err)
					}
					return
				}
				res.Completed++
				res.ChunksWritten += one.ChunksWritten
			})
			afterID = fetchDocID
		}

		for running > 0 {
			selector.Select(ctx)
		}
		if limit > 0 && res.Total >= limit {
			break
		}
		if len(ids) < batchSize {
			break
		}
	}

	workflow.GetLogger(ctx).Info("index-all workflow complete",
		"total", res.Total, "completed", res.Completed, "failed", res.Failed,
		"chunks_written", res.ChunksWritten)
	return res, firstErr
}
