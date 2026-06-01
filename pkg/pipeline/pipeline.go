// Package pipeline contains banhmi's Temporal workflows and activities that drive
// the ingestion ledger: Discover (find new documents), Fetch (download), Extract
// (document text), Normalize (sections/validity), Index (chunks/embeddings), and
// Watchdog (reconcile). The Postgres ingest ledger is the durable queue and the
// handoff bus between stages; Temporal provides orchestration, retries,
// scheduling, and per-entity visibility. See docs/design/PIPELINE.md for the
// full design.
//
// The five main stages are separate workflows. No stage auto-starts the next;
// operators or schedules advance the pipeline explicitly. Watchdog is deferred
// (see PLAN.md).
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/base/temporalx"
)

// Registered workflow type names; constants keep registration and the schedule
// actions in sync.
const (
	workflowDiscover          = "Discover"
	workflowFetch             = "Fetch"
	workflowFetchAll          = "FetchAll"
	workflowExtract           = "Extract"
	workflowExtractAll        = "ExtractAll"
	workflowNormalize         = "Normalize"
	workflowNormalizeAll      = "NormalizeAll"
	workflowIndex             = "Index"
	workflowIndexAll          = "IndexAll"
	workflowBackfillRelations = "BackfillRelations"
	workflowEmbedAll          = "EmbedAll"
	workflowOcrAll            = "OcrAll"
	workflowRunAll            = "RunAll"
)

const (
	externalActivityQueueSuffix = "-external"
	localActivityQueueSuffix    = "-local"
)

// Schedule cadences. Jitter spreads outbound request bursts across sources that
// share a spec; Fetch runs more often than Discover and drains a bounded batch.
const (
	discoverInterval      = time.Hour
	discoverJitter        = 5 * time.Minute
	supportDiscoverOffset = 15 * time.Minute

	fetchInterval     = 5 * time.Minute
	fetchJitter       = time.Minute
	fetchMaxArtifacts = 200

	// run-all runs the whole pipeline end to end (discover→fetch→extract→
	// normalize→backfill→ocr→index→embed). One daily tick is enough for a corpus
	// this size; operators un-pause this single schedule instead of the granular
	// per-source ones.
	runAllInterval = 24 * time.Hour
	runAllJitter   = 10 * time.Minute
)

// runAllScheduleID is the single whole-pipeline schedule operators un-pause.
const runAllScheduleID = "pipeline:run-all"

// ExternalActivityTaskQueue returns the queue for activities that call remote
// source APIs or download public files. Keep this queue separately capped.
func ExternalActivityTaskQueue(base string) string {
	return base + externalActivityQueueSuffix
}

// LocalActivityTaskQueue returns the queue for local CPU/disk stages.
func LocalActivityTaskQueue(base string) string {
	return base + localActivityQueueSuffix
}

// RegisterWorkflows wires the implemented workflows onto a worker.
func RegisterWorkflows(w worker.Worker) {
	w.RegisterWorkflowWithOptions(DiscoverWorkflow, workflow.RegisterOptions{Name: workflowDiscover})
	w.RegisterWorkflowWithOptions(FetchWorkflow, workflow.RegisterOptions{Name: workflowFetch})
	w.RegisterWorkflowWithOptions(FetchAllWorkflow, workflow.RegisterOptions{Name: workflowFetchAll})
	w.RegisterWorkflowWithOptions(ExtractWorkflow, workflow.RegisterOptions{Name: workflowExtract})
	w.RegisterWorkflowWithOptions(ExtractAllWorkflow, workflow.RegisterOptions{Name: workflowExtractAll})
	w.RegisterWorkflowWithOptions(NormalizeWorkflow, workflow.RegisterOptions{Name: workflowNormalize})
	w.RegisterWorkflowWithOptions(NormalizeAllWorkflow, workflow.RegisterOptions{Name: workflowNormalizeAll})
	w.RegisterWorkflowWithOptions(IndexWorkflow, workflow.RegisterOptions{Name: workflowIndex})
	w.RegisterWorkflowWithOptions(IndexAllWorkflow, workflow.RegisterOptions{Name: workflowIndexAll})
	w.RegisterWorkflowWithOptions(BackfillRelationsWorkflow, workflow.RegisterOptions{Name: workflowBackfillRelations})
	w.RegisterWorkflowWithOptions(EmbedAllWorkflow, workflow.RegisterOptions{Name: workflowEmbedAll})
	w.RegisterWorkflowWithOptions(OcrAllWorkflow, workflow.RegisterOptions{Name: workflowOcrAll})
	w.RegisterWorkflowWithOptions(RunAllWorkflow, workflow.RegisterOptions{Name: workflowRunAll})
}

// RegisterActivities registers every exported method of a as a named activity.
func RegisterActivities(w worker.Worker, a *Activities) {
	w.RegisterActivity(a)
}

// Register wires workflows and activities onto one worker. Tests use this
// helper; production workers use split activity queues.
func Register(w worker.Worker, a *Activities) {
	RegisterWorkflows(w)
	RegisterActivities(w, a)
}

// DiscoveryKeywordLister loads the per-source discovery keywords from the config
// schema. *dbconfig.Queries satisfies it; the interface lives here so pipeline
// stays decoupled from the generated store.
type DiscoveryKeywordLister interface {
	ListDiscoveryKeywords(ctx context.Context, source string) ([]string, error)
}

// EnsureSchedules creates the (paused) schedules for the enabled sources. They
// are created paused so a fresh deployment never crawls government sites before an
// operator opts in — the operator un-pauses per discovery slice. Existing schedules
// are left untouched so operator edits and pause state survive restarts.
func EnsureSchedules(ctx context.Context, c client.Client, cfg *config.Config, kw DiscoveryKeywordLister, log *slog.Logger) error {
	// congbao discovers its whole newest-first RSS feed (keyword "").
	if cfg.Sources.Congbao.Enabled {
		if err := ensureDiscoverSchedule(ctx, c, cfg.Temporal.TaskQueue, "congbao", "", log); err != nil {
			return err
		}
		if err := ensureFetchSchedule(ctx, c, cfg.Temporal.TaskQueue, "congbao", log); err != nil {
			return err
		}
	}
	// vbpl runs two discovery modes off one source: the keyword-less State Bank
	// sweep (keyword "") that pkg/scope filters on title + docAbs, plus one schedule
	// per config.discovery_keyword — a title search across the cross-cutting central
	// issuers where the keyword is the filter.
	if cfg.Sources.VBPL.Enabled {
		if err := ensureDiscoverSchedule(ctx, c, cfg.Temporal.TaskQueue, "vbpl", "", log); err != nil {
			return err
		}
		keywords, err := kw.ListDiscoveryKeywords(ctx, "vbpl")
		if err != nil {
			return fmt.Errorf("load vbpl discovery keywords: %w", err)
		}
		for _, keyword := range keywords {
			if err := ensureDiscoverSchedule(ctx, c, cfg.Temporal.TaskQueue, "vbpl", keyword, log); err != nil {
				return err
			}
		}
		log.Info("ensured vbpl discover schedules", "sweep", 1, "keywords", len(keywords))
		if err := ensureFetchSchedule(ctx, c, cfg.Temporal.TaskQueue, "vbpl", log); err != nil {
			return err
		}
	}
	// SBV Hanoi Region 1 is a support source for SBV-hosted legal files that VBPL
	// misses. The portal is small, so one broad list sweep with local scope
	// filtering is gentler than fan-out keyword searches.
	if cfg.Sources.SBVHanoi.Enabled {
		if err := ensureDiscoverScheduleWithOffset(ctx, c, cfg.Temporal.TaskQueue, "sbv_hanoi", "", supportDiscoverOffset, log); err != nil {
			return err
		}
		log.Info("ensured sbv_hanoi discover schedules", "sweep", 1)
		if err := ensureFetchSchedule(ctx, c, cfg.Temporal.TaskQueue, "sbv_hanoi", log); err != nil {
			return err
		}
	}
	// The single whole-pipeline schedule. Operators un-pause this one to run the
	// entire chain on a cadence; it supersedes the granular per-source schedules
	// above (which stay as a manual/advanced fallback).
	if err := ensureRunAllSchedule(ctx, c, cfg, log); err != nil {
		return err
	}
	return nil
}

// ensureRunAllSchedule creates the single paused whole-pipeline schedule. Its
// action carries the config-derived RunAllParams (no secrets — the Kaggle token
// lives on the worker, never in workflow params). Overlap=Skip prevents a slow run
// from racing the next daily tick.
func ensureRunAllSchedule(ctx context.Context, c client.Client, cfg *config.Config, log *slog.Logger) error {
	created, err := temporalx.EnsureSchedule(ctx, c, client.ScheduleOptions{
		ID: runAllScheduleID,
		Spec: client.ScheduleSpec{
			Intervals: []client.ScheduleIntervalSpec{{Every: runAllInterval}},
			Jitter:    runAllJitter,
		},
		Action: &client.ScheduleWorkflowAction{
			ID:        runAllScheduleID,
			Workflow:  workflowRunAll,
			Args:      []any{RunAllParamsFromConfig(cfg)},
			TaskQueue: cfg.Temporal.TaskQueue,
		},
		Overlap:        enumspb.SCHEDULE_OVERLAP_POLICY_SKIP,
		CatchupWindow:  time.Minute,
		PauseOnFailure: true,
		Paused:         true,
		Note:           "created paused; un-pause to run the whole pipeline (discover→fetch→extract→normalize→backfill→ocr→index→embed) on a daily schedule",
	})
	if err != nil {
		return fmt.Errorf("ensure run-all schedule %s: %w", runAllScheduleID, err)
	}
	if created {
		log.Info("created run-all schedule (paused)", "id", runAllScheduleID, "every", runAllInterval.String())
	} else {
		log.Info("run-all schedule already exists", "id", runAllScheduleID)
	}
	return nil
}

// ensureDiscoverSchedule creates one paused Discover schedule for a (source,
// keyword) slice. Overlap=Skip prevents a slow run from racing the next tick on
// the same cursor; the small catchup window keeps a recovered worker from
// stampeding a backlog of missed ticks.
func ensureDiscoverSchedule(ctx context.Context, c client.Client, taskQueue, source, keyword string, log *slog.Logger) error {
	return ensureDiscoverScheduleWithOffset(ctx, c, taskQueue, source, keyword, 0, log)
}

func ensureDiscoverScheduleWithOffset(ctx context.Context, c client.Client, taskQueue, source, keyword string, offset time.Duration, log *slog.Logger) error {
	id := discoverScheduleID(source, keyword)
	created, err := temporalx.EnsureSchedule(ctx, c, client.ScheduleOptions{
		ID: id,
		Spec: client.ScheduleSpec{
			Intervals: []client.ScheduleIntervalSpec{{Every: discoverInterval, Offset: offset}},
			Jitter:    discoverJitter,
		},
		Action: &client.ScheduleWorkflowAction{
			ID:        id,
			Workflow:  workflowDiscover,
			Args:      []any{DiscoverParams{Source: source, Keyword: keyword}},
			TaskQueue: taskQueue,
		},
		Overlap:        enumspb.SCHEDULE_OVERLAP_POLICY_SKIP,
		CatchupWindow:  time.Minute,
		PauseOnFailure: true,
		Paused:         true,
		Note:           "created paused; un-pause to begin discovery",
	})
	if err != nil {
		return fmt.Errorf("ensure discover schedule %s: %w", id, err)
	}
	if created {
		log.Info("created discover schedule (paused)", "id", id, "every", discoverInterval.String(), "offset", offset.String())
	} else {
		log.Info("discover schedule already exists", "id", id)
	}
	return nil
}

// discoverScheduleID is the per-(source, keyword) Discover schedule id. The
// whole-feed slice (keyword "") uses the short form.
func discoverScheduleID(source, keyword string) string {
	if keyword == "" {
		return "discover:" + source
	}
	return "discover:" + source + ":" + keyword
}

// ensureFetchSchedule creates one paused Fetch drainer schedule for a source. It
// fires frequently and drains a bounded batch each run; overlap=Skip keeps two
// drains off the same queue at once.
func ensureFetchSchedule(ctx context.Context, c client.Client, taskQueue, source string, log *slog.Logger) error {
	id := fetchScheduleID(source)
	created, err := temporalx.EnsureSchedule(ctx, c, client.ScheduleOptions{
		ID: id,
		Spec: client.ScheduleSpec{
			Intervals: []client.ScheduleIntervalSpec{{Every: fetchInterval}},
			Jitter:    fetchJitter,
		},
		Action: &client.ScheduleWorkflowAction{
			ID:        id,
			Workflow:  workflowFetch,
			Args:      []any{FetchParams{Source: source, MaxArtifacts: fetchMaxArtifacts}},
			TaskQueue: taskQueue,
		},
		Overlap:        enumspb.SCHEDULE_OVERLAP_POLICY_SKIP,
		CatchupWindow:  time.Minute,
		PauseOnFailure: true,
		Paused:         true,
		Note:           "created paused; un-pause to begin fetching",
	})
	if err != nil {
		return fmt.Errorf("ensure fetch schedule %s: %w", id, err)
	}
	if created {
		log.Info("created fetch schedule (paused)", "id", id, "every", fetchInterval.String())
	} else {
		log.Info("fetch schedule already exists", "id", id)
	}
	return nil
}

// fetchScheduleID is the per-source Fetch schedule id.
func fetchScheduleID(source string) string { return "fetch:" + source }
