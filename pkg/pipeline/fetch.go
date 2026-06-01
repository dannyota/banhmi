package pipeline

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// claimBatch is how many artifacts Fetch claims per round inside one run. It is
// a database batch size, not a concurrency limit; worker activity limits control
// execution concurrency.
const claimBatch = 10

// FetchParams selects what to drain. MaxArtifacts optionally caps how many
// artifacts one run processes; 0 drains until the source queue is empty.
type FetchParams struct {
	Source       string
	MaxArtifacts int
}

// FetchAllParams selects sources to drain in one workflow run.
type FetchAllParams struct {
	Sources      []string
	MaxArtifacts int
}

// FetchResult summarizes a Fetch run.
type FetchResult struct {
	Claimed       int // artifacts claimed and processed
	Bodies        int // body artifacts planned (detail fetched, files enqueued)
	Trees         int // provision-tree artifacts fetched or skipped
	Files         int // file artifacts processed (download attempted)
	DocsCompleted int // documents that reached state=complete this run
}

// FetchSourceResult summarizes one source inside FetchAll.
type FetchSourceResult struct {
	Source string
	Result FetchResult
}

// FetchAllResult summarizes a multi-source FetchAll run.
type FetchAllResult struct {
	Sources       int
	FailedSources int
	Claimed       int
	Bodies        int
	Trees         int
	Files         int
	DocsCompleted int
	SourceResults []FetchSourceResult
}

type fetchSourceOutcome struct {
	Source string
	Result FetchResult
	Err    error
}

// ClaimParams asks the claim activity for up to Limit due artifacts for Source.
type ClaimParams struct {
	Source string
	Limit  int
}

// ClaimedArtifact is the workflow-facing view of a leased fetch_artifact row.
type ClaimedArtifact struct {
	ID         int64
	FetchDocID int64
	Kind       string
	RefKey     string
	FileKind   string
	FileName   string
	URL        string
}

// FetchWorkflow is the scheduled batch drainer. Each run claims due artifacts
// from the ledger (FOR UPDATE SKIP LOCKED + lease) and dispatches one activity
// per artifact by kind. PlanBody fetches a document's detail and enqueues every
// selected source file; FetchTree records source structure; FetchFile downloads
// files into bronze. Fetch execution concurrency is controlled only by the
// Temporal worker activity limit. The workflow loops until the claimable set is
// empty or the optional MaxArtifacts cap is reached, then exits (no
// ContinueAsNew).
// Completeness and the dead-letter path live in the ledger. Fetch does not
// start Extract; the operator or schedule must run the next stage explicitly.
func FetchWorkflow(ctx workflow.Context, p FetchParams) (FetchResult, error) {
	ctx = withFetchActivityOptions(ctx)

	log := workflow.GetLogger(ctx)
	var a *Activities // typed nil: only used to name the activities for the SDK
	res, err := drainFetchSource(ctx, a, p.Source, p.MaxArtifacts)
	if err != nil {
		return res, err
	}

	log.Info("fetch complete", "source", p.Source,
		"claimed", res.Claimed, "bodies", res.Bodies, "trees", res.Trees, "files", res.Files, "completed", res.DocsCompleted)
	return res, nil
}

// FetchAllWorkflow runs one workflow for all requested sources. It starts one
// source drainer coroutine per source; all remote/API/download activities still
// run through the external activity queue, whose worker cap is the only fetch
// backpressure.
func FetchAllWorkflow(ctx workflow.Context, p FetchAllParams) (FetchAllResult, error) {
	ctx = withFetchActivityOptions(ctx)

	var a *Activities
	ch := workflow.NewBufferedChannel(ctx, len(p.Sources))
	for _, source := range p.Sources {
		source := source
		workflow.Go(ctx, func(ctx workflow.Context) {
			res, err := drainFetchSource(ctx, a, source, p.MaxArtifacts)
			ch.Send(ctx, fetchSourceOutcome{Source: source, Result: res, Err: err})
		})
	}

	var res FetchAllResult
	res.Sources = len(p.Sources)
	var firstErr error
	for range p.Sources {
		var out fetchSourceOutcome
		ch.Receive(ctx, &out)
		res.SourceResults = append(res.SourceResults, FetchSourceResult{Source: out.Source, Result: out.Result})
		res.Claimed += out.Result.Claimed
		res.Bodies += out.Result.Bodies
		res.Trees += out.Result.Trees
		res.Files += out.Result.Files
		res.DocsCompleted += out.Result.DocsCompleted
		if out.Err != nil {
			res.FailedSources++
			if firstErr == nil {
				firstErr = fmt.Errorf("fetch source %s: %w", out.Source, out.Err)
			}
		}
	}

	workflow.GetLogger(ctx).Info("fetch-all complete",
		"sources", res.Sources, "failed_sources", res.FailedSources,
		"claimed", res.Claimed, "bodies", res.Bodies, "trees", res.Trees,
		"files", res.Files, "completed", res.DocsCompleted)
	return res, firstErr
}

func withFetchActivityOptions(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:           ExternalActivityTaskQueue(workflow.GetInfo(ctx).TaskQueueName),
		StartToCloseTimeout: 15 * time.Minute,
		HeartbeatTimeout:    time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    5 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    3,
		},
	})
}

func drainFetchSource(ctx workflow.Context, a *Activities, source string, maxArtifacts int) (FetchResult, error) {
	var res FetchResult
	completedDocs := map[int64]bool{}
	for maxArtifacts <= 0 || res.Claimed < maxArtifacts {
		limit := claimBatch
		if maxArtifacts > 0 {
			limit = min(limit, maxArtifacts-res.Claimed)
		}
		var batch []ClaimedArtifact
		if err := workflow.ExecuteActivity(ctx, a.ClaimArtifacts, ClaimParams{Source: source, Limit: limit}).Get(ctx, &batch); err != nil {
			return res, err
		}
		if len(batch) == 0 {
			break // nothing due — drained
		}
		if err := processFetchBatch(ctx, a, batch, &res, completedDocs); err != nil {
			return res, err
		}
	}
	return res, nil
}

func processFetchBatch(
	ctx workflow.Context,
	a *Activities,
	batch []ClaimedArtifact,
	res *FetchResult,
	completedDocs map[int64]bool,
) error {
	log := workflow.GetLogger(ctx)

	selector := workflow.NewSelector(ctx)
	running := 0
	var firstErr error

	schedule := func(art ClaimedArtifact) {
		var state string
		res.Claimed++
		var future workflow.Future
		switch art.Kind {
		case "body":
			res.Bodies++
			future = workflow.ExecuteActivity(ctx, a.PlanBody, art)
		case "tree":
			res.Trees++
			future = workflow.ExecuteActivity(ctx, a.FetchTree, art)
		case "file":
			res.Files++
			future = workflow.ExecuteActivity(ctx, a.FetchFile, art)
		default:
			// ClaimArtifacts currently selects only body/tree/file. If a future query
			// broadens that set before the workflow supports the new kind, let the
			// lease expire so the capable drainer can reclaim it.
			log.Warn("fetch: skipping unhandled artifact kind", "kind", art.Kind, "id", art.ID)
			return
		}

		running++
		selector.AddFuture(future, func(f workflow.Future) {
			defer func() { running-- }()
			if err := f.Get(ctx, &state); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			if err := noteDocComplete(ctx, res, completedDocs, art.FetchDocID, state); err != nil && firstErr == nil {
				firstErr = err
			}
		})
	}

	for _, art := range batch {
		schedule(art)
	}
	for running > 0 {
		selector.Select(ctx)
	}
	return firstErr
}

func noteDocComplete(ctx workflow.Context, res *FetchResult, seen map[int64]bool, docID int64, state string) error {
	if state != "complete" || seen[docID] {
		return nil
	}
	seen[docID] = true
	res.DocsCompleted++
	workflow.GetLogger(ctx).Info("fetch: document complete; extraction not auto-started", "fetch_doc", docID)
	return nil
}
