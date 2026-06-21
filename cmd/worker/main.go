// Command worker runs banhmi's Temporal pipeline: it registers the workflows and
// activities (Discover, Fetch, Extract, Normalize, Index today; Watchdog next) and ensures their
// schedules, created paused. Dependencies are wired by the dig container in
// pkg/app; this command only builds it and runs the worker. The dev flags
// -discover/-fetch/-extract/-normalize/-index run one workflow synchronously and exit.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"danny.vn/banhmi/pkg/app"
	"danny.vn/banhmi/pkg/base/config"
	blog "danny.vn/banhmi/pkg/base/log"
	"danny.vn/banhmi/pkg/pipeline"
	dbconfig "danny.vn/banhmi/pkg/store/config"
)

var fetchAllSources = []string{"congbao", "vbpl", "vanban", "sbv_hanoi"}

type runOpts struct {
	cfgPath           string
	discover          string // run Discover once for this source, then exit (dev)
	keyword           string // -discover query keyword (vbpl); empty for congbao RSS
	fetch             string // run Fetch once for this source, then exit (dev)
	max               int    // max concurrent external Discover/Fetch activity executions
	limit             int    // -fetch artifact cap or -extract-all doc cap (0 drains all)
	extract           int64  // run Extract once for this fetch_doc id, then exit (dev)
	extractAll        bool   // run Extract for fetch_doc rows that need text, then exit (dev)
	normalize         int64  // run Normalize once for this fetch_doc id, then exit (dev)
	normalizeAll      bool   // run Normalize for fetch_doc rows that need sections/validity, then exit (dev)
	index             int64  // run Index once for this fetch_doc id, then exit (dev)
	indexAll          bool   // run Index for normalized fetch_doc rows that need chunks, then exit (dev)
	embedAll          bool   // run EmbedAll (Kaggle batch embed of all/missing chunks), then exit (dev)
	ocrAll            bool   // run OcrAll (batch OCR of gate-flagged scans, local or Kaggle), then exit (dev)
	backfillRelations bool   // enqueue promoted official relation targets, then exit (dev)
	drain             bool   // run the INPUT pipeline to convergence, then exit (dev)
	runAll            bool   // run the whole pipeline (RunAll) once to convergence, then exit (dev)
	force             bool   // force supported all-stage reruns
}

func main() {
	var o runOpts
	flag.StringVar(&o.cfgPath, "config", "config/config.yaml", "path to config file")
	flag.StringVar(&o.discover, "discover", "", "run Discover once for this source, then exit (dev)")
	flag.StringVar(&o.keyword, "keyword", "", "query keyword for -discover (vbpl; congbao ignores it)")
	flag.StringVar(&o.fetch, "fetch", "", "run Fetch once for this source, then exit (dev)")
	flag.IntVar(&o.max, "max", 5, "max concurrent external Discover/Fetch activity executions")
	flag.IntVar(&o.limit, "limit", 0, "max artifacts for -fetch or docs for -extract-all/-normalize-all/-index-all; 0 drains all")
	flag.Int64Var(&o.extract, "extract", 0, "run Extract once for this fetch_doc id, then exit (dev)")
	flag.BoolVar(&o.extractAll, "extract-all", false, "run Extract for fetch_doc rows that need text, then exit (dev)")
	flag.Int64Var(&o.normalize, "normalize", 0, "run Normalize once for this fetch_doc id, then exit (dev)")
	flag.BoolVar(&o.normalizeAll, "normalize-all", false, "run Normalize for fetch_doc rows that need sections/validity, then exit (dev)")
	flag.Int64Var(&o.index, "index", 0, "run Index once for this fetch_doc id, then exit (dev)")
	flag.BoolVar(&o.indexAll, "index-all", false, "run Index for normalized fetch_doc rows that need chunks, then exit (dev)")
	flag.BoolVar(&o.embedAll, "embed-all", false, "run EmbedAll (Kaggle batch embed of all/missing chunks), then exit (dev)")
	flag.BoolVar(&o.ocrAll, "ocr-all", false, "run OcrAll (batch OCR of gate-flagged scans, local CPU or Kaggle GPU), then exit (dev)")
	flag.BoolVar(&o.backfillRelations, "backfill-relations", false, "enqueue promoted official relation targets, then exit (dev)")
	flag.BoolVar(&o.drain, "drain", false, "run the INPUT pipeline to convergence (backfill→fetch→extract→normalize, repeat until drained), then exit (dev)")
	flag.BoolVar(&o.runAll, "run-all", false, "run the whole pipeline once (discover→fetch→extract→normalize→backfill→ocr→index→embed) to convergence, then exit (dev)")
	flag.BoolVar(&o.force, "force", false, "force supported all-stage reruns; reruns every eligible -normalize-all/-index-all document")
	flag.Parse()

	log := blog.New(os.Getenv("BANHMI_LOG_LEVEL"))
	if err := run(o, log); err != nil {
		log.Error("worker", "err", err)
		os.Exit(1)
	}
}

func run(o runOpts, log *slog.Logger) error {
	cfg, err := config.Load(o.cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application, err := app.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer application.Close()

	return application.Container.Invoke(func(tc client.Client, acts *pipeline.Activities, cfgQ *dbconfig.Queries) error {
		return serve(ctx, tc, acts, cfgQ, cfg, o, log)
	})
}

// serve registers the workflows/activities on a worker and either runs a one-shot
// dev trigger or ensures the schedules and blocks until shutdown.
func serve(ctx context.Context, tc client.Client, acts *pipeline.Activities, cfgQ *dbconfig.Queries, cfg *config.Config, o runOpts, log *slog.Logger) error {
	fetchActivityLimit := o.max
	if fetchActivityLimit <= 0 {
		fetchActivityLimit = 5
	}
	workers, err := startPipelineWorkers(tc, acts, cfg.Temporal.TaskQueue, fetchActivityLimit)
	if err != nil {
		return err
	}
	defer stopWorkers(workers)
	log.Info("banhmi workers running",
		"workflow_task_queue", cfg.Temporal.TaskQueue,
		"external_activity_task_queue", pipeline.ExternalActivityTaskQueue(cfg.Temporal.TaskQueue),
		"external_max_concurrent_activities", fetchActivityLimit,
		"local_activity_task_queue", pipeline.LocalActivityTaskQueue(cfg.Temporal.TaskQueue),
		"local_max_concurrent_activities", localActivityLimit(),
		"temporal", cfg.Temporal.HostPort)

	switch {
	case o.discover != "":
		if o.discover == "all" {
			return triggerDiscoverAll(ctx, tc, cfg.Temporal.TaskQueue, cfgQ, log)
		}
		return triggerDiscover(ctx, tc, cfg.Temporal.TaskQueue, o.discover, o.keyword, log)
	case o.fetch != "":
		if o.fetch == "all" {
			return triggerFetchAll(ctx, tc, cfg.Temporal.TaskQueue, fetchAllSources, fetchActivityLimit, o.limit, log)
		}
		return triggerFetch(ctx, tc, cfg.Temporal.TaskQueue, o.fetch, fetchActivityLimit, o.limit, log)
	case o.extract > 0:
		return triggerExtract(ctx, tc, cfg.Temporal.TaskQueue, o.extract, log)
	case o.extractAll:
		return triggerExtractAll(ctx, tc, cfg.Temporal.TaskQueue, o.limit, log)
	case o.normalize > 0:
		return triggerNormalize(ctx, tc, cfg.Temporal.TaskQueue, o.normalize, log)
	case o.normalizeAll:
		return triggerNormalizeAll(ctx, tc, cfg.Temporal.TaskQueue, o.limit, o.force, log)
	case o.index > 0:
		return triggerIndex(ctx, tc, cfg.Temporal.TaskQueue, o.index, log)
	case o.indexAll:
		return triggerIndexAll(ctx, tc, cfg.Temporal.TaskQueue, o.limit, o.force, log)
	case o.embedAll:
		return triggerEmbedAll(ctx, tc, cfg, o.limit, o.force, log)
	case o.ocrAll:
		return triggerOcrAll(ctx, tc, cfg, o.limit, o.force, log)
	case o.backfillRelations:
		return triggerBackfillRelations(ctx, tc, cfg.Temporal.TaskQueue, o.limit, log)
	case o.drain:
		return triggerDrain(ctx, tc, cfg.Temporal.TaskQueue, fetchAllSources, fetchActivityLimit, o.limit, log)
	case o.runAll:
		return triggerRunAll(ctx, tc, cfg, o.force, log)
	}

	if err := pipeline.EnsureSchedules(ctx, tc, cfg, cfgQ, log); err != nil {
		return fmt.Errorf("ensure schedules: %w", err)
	}
	<-ctx.Done()
	log.Info("worker shutting down")
	return nil
}

type pipelineWorker struct {
	name   string
	worker worker.Worker
}

func startPipelineWorkers(tc client.Client, acts *pipeline.Activities, baseTaskQueue string, fetchActivityLimit int) ([]pipelineWorker, error) {
	workflowWorker := worker.New(tc, baseTaskQueue, worker.Options{})
	pipeline.RegisterWorkflows(workflowWorker)

	externalWorker := worker.New(tc, pipeline.ExternalActivityTaskQueue(baseTaskQueue), worker.Options{
		MaxConcurrentActivityExecutionSize: fetchActivityLimit,
	})
	pipeline.RegisterActivities(externalWorker, acts)

	localWorker := worker.New(tc, pipeline.LocalActivityTaskQueue(baseTaskQueue), worker.Options{
		MaxConcurrentActivityExecutionSize: localActivityLimit(),
	})
	pipeline.RegisterActivities(localWorker, acts)

	workers := []pipelineWorker{
		{name: "workflow", worker: workflowWorker},
		{name: "external_activity", worker: externalWorker},
		{name: "local_activity", worker: localWorker},
	}
	started := make([]pipelineWorker, 0, len(workers))
	for _, w := range workers {
		if err := w.worker.Start(); err != nil {
			stopWorkers(started)
			return nil, fmt.Errorf("start %s worker: %w", w.name, err)
		}
		started = append(started, w)
	}
	return started, nil
}

func stopWorkers(workers []pipelineWorker) {
	for i := len(workers) - 1; i >= 0; i-- {
		workers[i].worker.Stop()
	}
}

func localActivityLimit() int {
	n := runtime.NumCPU() - 2
	if n < 1 {
		return 1
	}
	return n
}

// triggerDiscover runs one Discover synchronously against the running worker.
func triggerDiscover(ctx context.Context, tc client.Client, taskQueue, source, keyword string, log *slog.Logger) error {
	run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        fmt.Sprintf("discover-%s-manual-%d", source, time.Now().UnixNano()),
		TaskQueue: taskQueue,
	}, "Discover", pipeline.DiscoverParams{Source: source, Keyword: keyword})
	if err != nil {
		return fmt.Errorf("start discover: %w", err)
	}
	var res pipeline.DiscoverResult
	if err := run.Get(ctx, &res); err != nil {
		return fmt.Errorf("discover run: %w", err)
	}
	log.Info("discover finished", "source", source, "keyword", keyword,
		"discovered", res.Discovered, "in_scope", res.Enqueued, "skipped", res.Skipped, "watermark", res.Watermark)
	return nil
}

func triggerDiscoverAll(ctx context.Context, tc client.Client, taskQueue string, cfgQ *dbconfig.Queries, log *slog.Logger) error {
	type target struct {
		source  string
		keyword string
	}
	targets := []target{
		{source: "congbao"},
		{source: "vbpl"},
	}
	keywords, err := cfgQ.ListDiscoveryKeywords(ctx, "vbpl")
	if err != nil {
		return fmt.Errorf("list vbpl discovery keywords: %w", err)
	}
	for _, keyword := range keywords {
		targets = append(targets, target{source: "vbpl", keyword: keyword})
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(targets))
	for _, target := range targets {
		target := target
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := triggerDiscover(ctx, tc, taskQueue, target.source, target.keyword, log); err != nil {
				errCh <- fmt.Errorf("%s/%q: %w", target.source, target.keyword, err)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	if err := triggerDiscover(ctx, tc, taskQueue, "vanban", "", log); err != nil {
		return fmt.Errorf("vanban after vbpl: %w", err)
	}
	if err := triggerDiscover(ctx, tc, taskQueue, "sbv_hanoi", "", log); err != nil {
		return fmt.Errorf("sbv_hanoi after vbpl: %w", err)
	}
	log.Info("discover-all finished", "workflows", len(targets)+2, "vbpl_keywords", len(keywords))
	return nil
}

// triggerFetch runs one Fetch synchronously against the running worker.
// maxActivities is logged for the external activity queue cap; limit optionally
// caps total artifacts.
func triggerFetch(ctx context.Context, tc client.Client, taskQueue, source string, maxActivities, limit int, log *slog.Logger) error {
	run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                                       "fetch:" + source,
		TaskQueue:                                taskQueue,
		WorkflowIDConflictPolicy:                 enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
		WorkflowIDReusePolicy:                    enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}, "Fetch", pipeline.FetchParams{Source: source, MaxArtifacts: limit})
	if err != nil {
		return fmt.Errorf("start fetch: %w", err)
	}
	var res pipeline.FetchResult
	if err := run.Get(ctx, &res); err != nil {
		return fmt.Errorf("fetch run: %w", err)
	}
	log.Info("fetch finished", "source", source,
		"claimed", res.Claimed, "bodies", res.Bodies, "files", res.Files, "completed", res.DocsCompleted,
		"max_concurrent_activities", maxActivities, "artifact_limit", limit)
	return nil
}

// runFetchAll executes one FetchAll pass and returns its result. triggerFetchAll
// wraps it for the CLI; triggerDrain uses res.Claimed to detect convergence.
func runFetchAll(ctx context.Context, tc client.Client, taskQueue string, sources []string, limit int) (pipeline.FetchAllResult, error) {
	run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                                       "fetch:all",
		TaskQueue:                                taskQueue,
		WorkflowIDConflictPolicy:                 enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
		WorkflowIDReusePolicy:                    enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}, "FetchAll", pipeline.FetchAllParams{Sources: sources, MaxArtifacts: limit})
	if err != nil {
		return pipeline.FetchAllResult{}, fmt.Errorf("start fetch-all: %w", err)
	}
	var res pipeline.FetchAllResult
	if err := run.Get(ctx, &res); err != nil {
		return pipeline.FetchAllResult{}, fmt.Errorf("fetch-all run: %w", err)
	}
	return res, nil
}

// triggerFetchAll starts one FetchAll workflow for the supported sources. The
// external activity queue cap is the only backpressure shared across sources.
func triggerFetchAll(ctx context.Context, tc client.Client, taskQueue string, sources []string, maxActivities, limit int, log *slog.Logger) error {
	res, err := runFetchAll(ctx, tc, taskQueue, sources, limit)
	if err != nil {
		return err
	}
	log.Info("fetch-all finished",
		"sources", res.Sources, "failed_sources", res.FailedSources,
		"claimed", res.Claimed, "bodies", res.Bodies, "trees", res.Trees,
		"files", res.Files, "completed", res.DocsCompleted,
		"max_concurrent_activities", maxActivities, "artifact_limit", limit)
	return nil
}

// triggerDrain runs the INPUT pipeline to convergence: enqueue relation targets,
// fetch all pending artifacts, then extract + normalize, repeating until a fetch
// pass claims nothing. Backfill and the congbao source-fallback enqueue new fetch
// work mid-pipeline (e.g. the gazette copy of a VBPL placeholder), which a single
// pass strands; the loop drains it without relying on the (paused) fetch schedule.
// Index/embed/OCR stay terminal — run them once after the drain converges.
func triggerDrain(ctx context.Context, tc client.Client, taskQueue string, sources []string, maxActivities, limit int, log *slog.Logger) error {
	const maxRounds = 6
	for round := 1; round <= maxRounds; round++ {
		// Enqueue relation targets first so this round's fetch drains them too.
		if err := triggerBackfillRelations(ctx, tc, taskQueue, limit, log); err != nil {
			return err
		}
		res, err := runFetchAll(ctx, tc, taskQueue, sources, limit)
		if err != nil {
			return err
		}
		log.Info("drain: fetch pass",
			"round", round, "claimed", res.Claimed, "bodies", res.Bodies,
			"files", res.Files, "completed", res.DocsCompleted)
		if err := triggerExtractAll(ctx, tc, taskQueue, limit, log); err != nil {
			return err
		}
		if err := triggerNormalizeAll(ctx, tc, taskQueue, limit, false, log); err != nil {
			return err
		}
		if res.Claimed == 0 {
			log.Info("drain converged: fetch pass claimed nothing", "rounds", round)
			return nil
		}
	}
	log.Warn("drain stopped at max rounds; pending fetch work may remain — re-run -drain",
		"max_rounds", maxRounds)
	return nil
}

// triggerRunAll starts one RunAll workflow: the whole pipeline end to end —
// discover every enabled (source, keyword) slice, drain backfill→fetch→extract→
// normalize to convergence, OCR the flagged scans + re-normalize, index, then
// Kaggle-embed. It is the same workflow operators run on the pipeline:run-all
// schedule; this dev flag triggers it once and waits. -force reruns the normalize/
// index stages over all eligible docs.
func triggerRunAll(ctx context.Context, tc client.Client, cfg *config.Config, force bool, log *slog.Logger) error {
	params := pipeline.RunAllParamsFromConfig(cfg)
	params.Stage.Force = force
	run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        fmt.Sprintf("run-all-manual-%d", time.Now().Unix()),
		TaskQueue: cfg.Temporal.TaskQueue,
	}, "RunAll", params)
	if err != nil {
		return fmt.Errorf("start run-all: %w", err)
	}
	var res pipeline.RunAllResult
	if err := run.Get(ctx, &res); err != nil {
		return fmt.Errorf("run-all run: %w", err)
	}
	log.Info("run-all finished",
		"converged", res.Converged, "rounds", res.Rounds,
		"discover_slices", res.DiscoverSlices, "discovered", res.Discovered, "enqueued", res.Enqueued,
		"fetched", res.Fetched, "extracted", res.Extracted, "normalized", res.Normalized,
		"relations_enqueued", res.RelationsEnqueued, "ocr_processed", res.OcrProcessed,
		"indexed_chunks", res.IndexedChunks, "embedded", res.Embedded, "force", force)
	return nil
}

// triggerExtract runs only the Extract stage for one fetched document. This is
// the safe backfill/validation path before Normalize and Index are trusted.
func triggerExtract(ctx context.Context, tc client.Client, taskQueue string, fetchDocID int64, log *slog.Logger) error {
	run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        fmt.Sprintf("extract-%d-manual-%d", fetchDocID, time.Now().Unix()),
		TaskQueue: taskQueue,
	}, "Extract", pipeline.StageParams{FetchDocID: fetchDocID})
	if err != nil {
		return fmt.Errorf("start extract: %w", err)
	}
	var res pipeline.ExtractResult
	if err := run.Get(ctx, &res); err != nil {
		return fmt.Errorf("extract run: %w", err)
	}
	log.Info("extract finished", "fetch_doc", fetchDocID,
		"document", res.DocumentID, "engine", res.Engine, "confidence", res.Confidence,
		"needs_review", res.NeedsReview, "source_unavailable", res.SourceUnavailable)
	return nil
}

// triggerExtractAll starts one ExtractAll workflow. The workflow controls
// pagination and caps concurrent extraction to (CPU-2) docs by default.
func triggerExtractAll(ctx context.Context, tc client.Client, taskQueue string, limit int, log *slog.Logger) error {
	batchSize := int32(localActivityLimit())
	if batchSize <= 0 {
		batchSize = 1
	}
	run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        fmt.Sprintf("extract-all-manual-%d", time.Now().Unix()),
		TaskQueue: taskQueue,
	}, "ExtractAll", pipeline.ExtractAllParams{
		Limit:         int32(limit),
		BatchSize:     batchSize,
		MaxConcurrent: batchSize,
	})
	if err != nil {
		return fmt.Errorf("start extract-all: %w", err)
	}
	var res pipeline.ExtractAllResult
	if err := run.Get(ctx, &res); err != nil {
		return fmt.Errorf("extract-all run: %w", err)
	}
	log.Info("extract-all finished",
		"total", res.Total, "completed", res.Completed, "failed", res.Failed,
		"needs_review", res.NeedsReview, "source_unavailable", res.SourceUnavailable)
	return nil
}

// triggerNormalize runs only the Normalize stage for one fetched document. It
// writes section/validity rows, but deliberately does not run Index.
func triggerNormalize(ctx context.Context, tc client.Client, taskQueue string, fetchDocID int64, log *slog.Logger) error {
	run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        fmt.Sprintf("normalize-%d-manual-%d", fetchDocID, time.Now().Unix()),
		TaskQueue: taskQueue,
	}, "Normalize", pipeline.StageParams{FetchDocID: fetchDocID})
	if err != nil {
		return fmt.Errorf("start normalize: %w", err)
	}
	var res pipeline.NormalizeResult
	if err := run.Get(ctx, &res); err != nil {
		return fmt.Errorf("normalize run: %w", err)
	}
	log.Info("normalize finished", "fetch_doc", fetchDocID,
		"document", res.DocumentID, "text_authority", res.TextAuthority, "text_source", res.TextSource,
		"sections_parsed", res.SectionsParsed, "sections_written", res.SectionsWritten,
		"articles", res.ArticleCount, "clauses", res.ClauseCount, "points", res.PointCount,
		"relation_evidence", res.RelationEvidenceWritten, "relations", res.RelationsWritten,
		"relation_targets_enqueued", res.RelationTargetsEnqueued,
		"validity_status", res.ValidityStatusClass, "skip_reason", res.SkipReason,
		"warnings", res.Warnings)
	return nil
}

// triggerEmbedAll starts one EmbedAll workflow: a whole-corpus (or missing-only)
// Kaggle GPU embedding pass. Auth is KAGGLE_API_TOKEN from the env; the Kaggle
// owner is auto-derived from that token, so no username need be configured.
func triggerEmbedAll(ctx context.Context, tc client.Client, cfg *config.Config, limit int, force bool, log *slog.Logger) error {
	run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        fmt.Sprintf("embed-all-manual-%d", time.Now().Unix()),
		TaskQueue: cfg.Temporal.TaskQueue,
	}, "EmbedAll", pipeline.EmbedAllParams{
		Owner:        cfg.Embed.Kaggle.Owner,
		ModelDataset: cfg.Embed.Kaggle.ModelDataset,
		Accelerator:  cfg.Embed.Kaggle.Accelerator,
		Dims:         config.EmbedDims,
		Force:        force,
		Limit:        limit,
	})
	if err != nil {
		return fmt.Errorf("start embed-all: %w", err)
	}
	var res pipeline.EmbedAllResult
	if err := run.Get(ctx, &res); err != nil {
		return fmt.Errorf("embed-all run: %w", err)
	}
	log.Info("embed-all finished", "embedded", res.Embedded, "force", force)
	return nil
}

// triggerOcrAll starts one OcrAll workflow: a whole-corpus OCR pass over the
// scans the gate flagged. Engine is config.OcrEngine() (Kaggle when
// KAGGLE_API_TOKEN is set, else local CPU); the Kaggle owner is auto-derived.
func triggerOcrAll(ctx context.Context, tc client.Client, cfg *config.Config, limit int, force bool, log *slog.Logger) error {
	engine := cfg.OcrEngine()
	run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        fmt.Sprintf("ocr-all-manual-%d", time.Now().Unix()),
		TaskQueue: cfg.Temporal.TaskQueue,
	}, "OcrAll", pipeline.OcrAllParams{
		Engine:      engine,
		Owner:       cfg.Extract.OCR.Kaggle.Owner,
		Accelerator: cfg.Extract.OCR.Kaggle.Accelerator,
		Command:     cfg.Extract.OCR.Command,
		Script:      cfg.Extract.OCR.Script,
		Languages:   cfg.OCRLanguages(),
		DPI:         cfg.Extract.OCR.DPI,
		BatchSize:   cfg.Extract.OCR.BatchSize,
		Force:       force,
		Limit:       limit,
	})
	if err != nil {
		return fmt.Errorf("start ocr-all: %w", err)
	}
	var res pipeline.OcrAllResult
	if err := run.Get(ctx, &res); err != nil {
		return fmt.Errorf("ocr-all run: %w", err)
	}
	log.Info("ocr-all finished", "processed", res.Processed, "failed", res.Failed, "engine", engine)
	return nil
}

// triggerNormalizeAll starts one NormalizeAll workflow. By default it selects
// only docs that still need Normalize; force reruns all eligible docs.
func triggerNormalizeAll(ctx context.Context, tc client.Client, taskQueue string, limit int, force bool, log *slog.Logger) error {
	batchSize := int32(localActivityLimit())
	if batchSize <= 0 {
		batchSize = 1
	}
	run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        fmt.Sprintf("normalize-all-manual-%d", time.Now().Unix()),
		TaskQueue: taskQueue,
	}, "NormalizeAll", pipeline.StageAllParams{
		Limit:         int32(limit),
		BatchSize:     batchSize,
		MaxConcurrent: batchSize,
		Force:         force,
	})
	if err != nil {
		return fmt.Errorf("start normalize-all: %w", err)
	}
	var res pipeline.NormalizeAllResult
	if err := run.Get(ctx, &res); err != nil {
		return fmt.Errorf("normalize-all run: %w", err)
	}
	log.Info("normalize-all finished",
		"total", res.Total, "completed", res.Completed, "failed", res.Failed,
		"sections_written", res.SectionsWritten,
		"relation_targets_enqueued", res.RelationTargetsEnqueued,
		"skipped", res.Skipped, "force", force)
	return nil
}

// triggerIndex runs only the Index stage for one normalized document. It writes
// chunks and optional embeddings, but deliberately does not run Extract/Normalize.
func triggerIndex(ctx context.Context, tc client.Client, taskQueue string, fetchDocID int64, log *slog.Logger) error {
	run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        fmt.Sprintf("index-%d-manual-%d", fetchDocID, time.Now().Unix()),
		TaskQueue: taskQueue,
	}, "Index", pipeline.StageParams{FetchDocID: fetchDocID})
	if err != nil {
		return fmt.Errorf("start index: %w", err)
	}
	var res pipeline.IndexResult
	if err := run.Get(ctx, &res); err != nil {
		return fmt.Errorf("index run: %w", err)
	}
	log.Info("index finished", "fetch_doc", fetchDocID,
		"document", res.DocumentID, "chunks", res.ChunksWritten)
	return nil
}

// triggerIndexAll starts one IndexAll workflow. By default the workflow selects
// only docs that still need chunks; force reruns every eligible normalized doc.
func triggerIndexAll(ctx context.Context, tc client.Client, taskQueue string, limit int, force bool, log *slog.Logger) error {
	batchSize := int32(localActivityLimit())
	if batchSize <= 0 {
		batchSize = 1
	}
	run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        fmt.Sprintf("index-all-manual-%d", time.Now().Unix()),
		TaskQueue: taskQueue,
	}, "IndexAll", pipeline.StageAllParams{
		Limit:         int32(limit),
		BatchSize:     batchSize,
		MaxConcurrent: batchSize,
		Force:         force,
	})
	if err != nil {
		return fmt.Errorf("start index-all: %w", err)
	}
	var res pipeline.IndexAllResult
	if err := run.Get(ctx, &res); err != nil {
		return fmt.Errorf("index-all run: %w", err)
	}
	log.Info("index-all finished",
		"total", res.Total, "completed", res.Completed, "failed", res.Failed,
		"chunks_written", res.ChunksWritten, "force", force)
	return nil
}

func triggerBackfillRelations(ctx context.Context, tc client.Client, taskQueue string, limit int, log *slog.Logger) error {
	run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        fmt.Sprintf("backfill-relations-manual-%d", time.Now().Unix()),
		TaskQueue: taskQueue,
	}, "BackfillRelations", pipeline.BackfillRelationTargetsParams{Limit: int32(limit)})
	if err != nil {
		return fmt.Errorf("start relation backfill: %w", err)
	}
	var res pipeline.BackfillRelationTargetsResult
	if err := run.Get(ctx, &res); err != nil {
		return fmt.Errorf("relation backfill run: %w", err)
	}
	log.Info("relation backfill finished",
		"candidates", res.Candidates, "enqueued", res.Enqueued, "skipped", res.Skipped, "limit", limit)
	return nil
}
