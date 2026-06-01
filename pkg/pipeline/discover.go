package pipeline

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// DiscoverParams selects the slice to discover: a source and an optional keyword.
// An empty keyword means the source's whole newest-first feed (e.g. congbao RSS);
// keyword-filtered sources (vbpl) run one Discover per keyword.
type DiscoverParams struct {
	Source  string
	Keyword string
}

// DiscoverResult summarizes one Discover run.
type DiscoverResult struct {
	Discovered int    // documents the feed returned after the watermark
	Enqueued   int    // in-scope documents written to the ledger this run
	Skipped    int    // documents skipped as out of scope, excluded, or duplicate
	Watermark  string // new watermark persisted to discover_cursor (RFC3339)
}

// DiscoverWorkflow is the thin orchestration around the Discover activity: it
// sets the activity's timeout and retry policy and returns the result. All I/O
// (read the feed, write the ledger) lives in the activity so the workflow stays
// deterministic. A per-source Temporal Schedule triggers it (see EnsureSchedules).
func DiscoverWorkflow(ctx workflow.Context, p DiscoverParams) (DiscoverResult, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:           ExternalActivityTaskQueue(workflow.GetInfo(ctx).TaskQueueName),
		StartToCloseTimeout: 10 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    5 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    5,
		},
	})

	var a *Activities // typed nil: only used to name the activity for the SDK
	var res DiscoverResult
	if err := workflow.ExecuteActivity(ctx, a.Discover, p).Get(ctx, &res); err != nil {
		return DiscoverResult{}, err
	}

	workflow.GetLogger(ctx).Info("discover complete",
		"source", p.Source, "keyword", p.Keyword,
		"discovered", res.Discovered, "in_scope", res.Enqueued, "skipped", res.Skipped, "watermark", res.Watermark)
	return res, nil
}
