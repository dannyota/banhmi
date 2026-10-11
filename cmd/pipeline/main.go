// Command pipeline runs banhmi's ingestion pipeline without Temporal. It calls
// pipeline activity methods directly: Discover, Fetch, Extract, Normalize, Index,
// EmbedAll, OcrAll, LexicalIndex, and the orchestrated RunAll. Structured slog
// output goes to stdout (captured by GCP Cloud Logging when running as a Cloud
// Run Job). Batch stage flags execute one bounded shard and exit. An incomplete
// shard returns a resume cursor instead of reporting whole-corpus completion.
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

	corpusrun "danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/pkg/app"
	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/base/dns"
	blog "danny.vn/banhmi/pkg/base/log"
	"danny.vn/banhmi/pkg/pipeline"
	dbconfig "danny.vn/banhmi/pkg/store/config"
)

type runOpts struct {
	cfgPath           string
	discover          string
	keyword           string
	fetch             string
	max               int
	limit             int
	afterID           int64
	extract           int64
	extractAll        bool
	normalize         int64
	normalizeAll      bool
	index             int64
	indexAll          bool
	embedAll          bool
	embedParallel     int
	ocrAll            bool
	backfillRelations bool
	lexindex          bool
	resolveRefs       bool
	recomputeRels     string
	drain             bool
	runAll            bool
	force             bool
}

const (
	embedShardItems       = 25000
	embedShardInputBytes  = int64(64 << 20)
	embedShardPages       = 1
	embedShardElapsed     = 15 * time.Minute
	embedShardConcurrency = 2

	ocrShardItems       = 50
	ocrShardInputBytes  = int64(512 << 20)
	ocrShardPages       = 2000
	ocrShardElapsed     = 30 * time.Minute
	ocrShardConcurrency = 8
)

func main() {
	var o runOpts
	flag.StringVar(&o.cfgPath, "config", "config/config.yaml", "path to config file")
	flag.StringVar(&o.discover, "discover", "", "run Discover once for this source (or 'all'), then exit")
	flag.StringVar(&o.keyword, "keyword", "", "query keyword for -discover (vbpl; congbao ignores it)")
	flag.StringVar(&o.fetch, "fetch", "", "run Fetch once for this source (or 'all'), then exit")
	flag.IntVar(&o.max, "max", 5, "max concurrent fetch activities")
	flag.IntVar(&o.limit, "limit", 0, "max discover docs or lower a batch stage's built-in item cap; 0 uses the bounded stage default")
	flag.Int64Var(&o.afterID, "after-id", 0, "resume -extract-all, -normalize-all, -index-all, -embed-all, or -ocr-all after this database ID")
	flag.Int64Var(&o.extract, "extract", 0, "run Extract once for this fetch_doc id, then exit")
	flag.BoolVar(&o.extractAll, "extract-all", false, "run one bounded Extract shard, then exit")
	flag.Int64Var(&o.normalize, "normalize", 0, "run Normalize once for this fetch_doc id, then exit")
	flag.BoolVar(&o.normalizeAll, "normalize-all", false, "run one bounded Normalize shard, then exit")
	flag.Int64Var(&o.index, "index", 0, "run Index once for this fetch_doc id, then exit")
	flag.BoolVar(&o.indexAll, "index-all", false, "run one bounded Index shard, then exit")
	flag.BoolVar(&o.embedAll, "embed-all", false, "run one bounded missing-chunk embedding shard, then exit")
	flag.IntVar(&o.embedParallel, "embed-parallel", 2, "number of concurrent Kaggle kernels for -embed-all (max 2 on free tier)")
	flag.BoolVar(&o.ocrAll, "ocr-all", false, "run one bounded OCR shard of gate-flagged scans, then exit")
	flag.BoolVar(&o.backfillRelations, "backfill-relations", false, "enqueue promoted official relation targets, then exit")
	flag.BoolVar(&o.lexindex, "lexindex", false, "rebuild BM25 sparse vectors, then exit")
	flag.BoolVar(&o.resolveRefs, "resolve-refs", false, "re-resolve silver.doc_ref.document_id from ref_key, then exit (dry run unless -force)")
	flag.StringVar(&o.recomputeRels, "recompute-relations", "", "re-derive relation evidence for this source (or 'all') from bronze, then exit (dry run unless -force)")
	flag.BoolVar(&o.drain, "drain", false, "run bounded INPUT stages; rerun when an incomplete-shard error is returned")
	flag.BoolVar(&o.runAll, "run-all", false, "run bounded legacy pipeline stages; fail with a cursor when a stage is incomplete")
	flag.BoolVar(&o.force, "force", false, "force reruns for supported stages; with -discover, ignore the stored watermark (full rescan)")
	flag.Parse()

	dns.InstallFallback()

	log := blog.New(os.Getenv("BANHMI_LOG_LEVEL"))
	if err := run(o, log); err != nil {
		log.Error("pipeline", "err", err)
		os.Exit(1)
	}
}

func run(o runOpts, log *slog.Logger) error {
	if err := validateRunOpts(o); err != nil {
		return err
	}
	cfg, err := config.Load(o.cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application, err := app.New(ctx, cfg, log, app.WithoutTemporal())
	if err != nil {
		return err
	}
	defer application.Close()

	return application.Container.Invoke(func(acts *pipeline.Activities, cfgQ *dbconfig.Queries) error {
		return dispatch(ctx, acts, cfgQ, cfg, o, log)
	})
}

func validateRunOpts(o runOpts) error {
	if o.runAll && o.force {
		return fmt.Errorf("-run-all -force cannot resume bounded forced stages; run each forced stage with -after-id")
	}
	return nil
}

func dispatch(ctx context.Context, acts *pipeline.Activities, cfgQ *dbconfig.Queries, cfg *config.Config, o runOpts, log *slog.Logger) error {
	sources := pipeline.SourceIDs(acts)

	switch {
	case o.discover != "":
		return doDiscover(ctx, acts, cfgQ, sources, o, log)
	case o.fetch != "":
		return doFetch(ctx, acts, sources, o, log)
	case o.extract > 0:
		return doExtract(ctx, acts, o.extract, log)
	case o.extractAll:
		return doExtractAll(ctx, acts, o.afterID, o.limit, log)
	case o.normalize > 0:
		return doNormalize(ctx, acts, o.normalize, log)
	case o.normalizeAll:
		return doNormalizeAll(ctx, acts, o.afterID, o.limit, o.force, log)
	case o.index > 0:
		return doIndex(ctx, acts, o.index, log)
	case o.indexAll:
		return doIndexAll(ctx, acts, o.afterID, o.limit, o.force, log)
	case o.embedAll:
		return doEmbedAll(ctx, acts, cfg, o.afterID, o.limit, o.force, o.embedParallel, log)
	case o.ocrAll:
		return doOcrAll(ctx, acts, cfg, o.afterID, o.limit, o.force, log)
	case o.backfillRelations:
		return doBackfillRelations(ctx, acts, o.limit, log)
	case o.lexindex:
		return doLexicalIndex(ctx, acts, log)
	case o.resolveRefs:
		return doResolveRefs(ctx, acts, o.force, log)
	case o.recomputeRels != "":
		return doRecomputeRelations(ctx, acts, o.recomputeRels, o.force, o.limit, log)
	case o.drain:
		return doDrain(ctx, acts, sources, o.limit, o.max, log)
	case o.runAll:
		return doRunAll(ctx, acts, cfgQ, cfg, sources, o.limit, o.max, o.force, log)
	default:
		return fmt.Errorf("no stage flag specified; use -run-all, -discover, -fetch, etc.")
	}
}

// --- individual stage runners ---

func doDiscover(ctx context.Context, acts *pipeline.Activities, cfgQ *dbconfig.Queries, sources []string, o runOpts, log *slog.Logger) error {
	if o.discover == "all" {
		slices, err := acts.DiscoverSlices(ctx, sources)
		if err != nil {
			return err
		}
		for _, s := range slices {
			s.FullScan = o.force
			res, err := acts.Discover(ctx, s)
			if err != nil {
				log.Warn("discover failed", "source", s.Source, "keyword", s.Keyword, "err", err)
				continue
			}
			log.Info("discover done", "source", s.Source, "keyword", s.Keyword,
				"discovered", res.Discovered, "in_scope", res.Enqueued, "skipped", res.Skipped)
		}
		return nil
	}
	res, err := acts.Discover(ctx, pipeline.DiscoverParams{Source: o.discover, Keyword: o.keyword, FullScan: o.force})
	if err != nil {
		return err
	}
	log.Info("discover done", "source", o.discover, "keyword", o.keyword,
		"discovered", res.Discovered, "in_scope", res.Enqueued, "skipped", res.Skipped, "watermark", res.Watermark)
	return nil
}

func doFetch(ctx context.Context, acts *pipeline.Activities, sources []string, o runOpts, log *slog.Logger) error {
	fetchSources := sources
	if o.fetch != "all" {
		fetchSources = []string{o.fetch}
	}
	for _, src := range fetchSources {
		claimed, err := drainFetchSource(ctx, acts, src, o.limit, o.max, log)
		if err != nil {
			return fmt.Errorf("fetch %s: %w", src, err)
		}
		log.Info("fetch done", "source", src, "claimed", claimed)
	}
	return nil
}

func drainFetchSource(ctx context.Context, acts *pipeline.Activities, source string, limit, concurrency int, log *slog.Logger) (int, error) {
	if concurrency <= 0 {
		return 0, fmt.Errorf("fetch concurrency must be positive")
	}
	batch := 20
	if limit > 0 && limit < batch {
		batch = limit
	}
	claimed, err := acts.ClaimArtifacts(ctx, pipeline.ClaimParams{Source: source, Limit: batch})
	if err != nil {
		return 0, &corpusrun.BatchError{Stage: "fetch " + source, Cause: fmt.Errorf("claim artifacts: %w", err)}
	}
	if len(claimed) == 0 {
		return 0, nil
	}
	result, err := runFetchBatch(ctx, claimed, concurrency, func(ctx context.Context, art pipeline.ClaimedArtifact) error {
		switch art.Kind {
		case "body":
			_, err := acts.PlanBody(ctx, art)
			return err
		case "tree":
			_, err := acts.FetchTree(ctx, art)
			return err
		case "file":
			_, err := acts.FetchFile(ctx, art)
			return err
		default:
			return fmt.Errorf("unknown artifact kind %q", art.Kind)
		}
	})
	if err != nil {
		return len(claimed), &corpusrun.BatchError{Stage: "fetch " + source, Result: result, Cause: err}
	}
	if len(claimed) == batch && (limit <= 0 || limit > batch) {
		cause := fmt.Errorf("bounded fetch shard may be incomplete; rerun the command to resume claimed work")
		return len(claimed), &corpusrun.BatchError{Stage: "fetch " + source, Result: result, Cause: cause}
	}
	return len(claimed), nil
}

type fetchProcessFn func(ctx context.Context, artifact pipeline.ClaimedArtifact) error

func runFetchBatch(ctx context.Context, artifacts []pipeline.ClaimedArtifact, concurrency int, process fetchProcessFn) (corpusrun.BatchResult, error) {
	if concurrency <= 0 {
		return corpusrun.BatchResult{}, fmt.Errorf("fetch concurrency must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, stageElapsed)
	defer cancel()
	jobs := make(chan pipeline.ClaimedArtifact)
	var result corpusrun.BatchResult
	var firstErr error
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range min(concurrency, len(artifacts)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for artifact := range jobs {
				if ctx.Err() != nil {
					return
				}
				err := process(ctx, artifact)
				mu.Lock()
				if err == nil {
					result.Succeeded++
				} else if firstErr == nil {
					firstErr = fmt.Errorf("artifact %d: %w", artifact.ID, err)
				}
				mu.Unlock()
			}
		}()
	}
	for _, artifact := range artifacts {
		if ctx.Err() != nil {
			break
		}
		select {
		case jobs <- artifact:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	result.Attempted = len(artifacts)
	result.Failed = len(artifacts) - result.Succeeded
	result.Complete = result.Failed == 0
	if firstErr != nil {
		return result, firstErr
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result, nil
}

func doExtract(ctx context.Context, acts *pipeline.Activities, fetchDocID int64, log *slog.Logger) error {
	res, err := acts.Extract(ctx, pipeline.StageParams{FetchDocID: fetchDocID})
	if err != nil {
		return err
	}
	log.Info("extract done", "fetch_doc", fetchDocID,
		"document", res.DocumentID, "engine", res.Engine, "confidence", res.Confidence,
		"needs_review", res.NeedsReview)
	return nil
}

func doExtractAll(ctx context.Context, acts *pipeline.Activities, afterID int64, limit int, log *slog.Logger) error {
	return runStageAll(ctx, "extract", afterID, limit, false,
		func(ctx context.Context, afterID int64, batch int32) ([]int64, error) {
			return acts.ListFetchDocIDsNeedingExtractAfter(ctx, pipeline.ListStageFetchDocIDsAfterParams{
				AfterID: afterID, Limit: batch,
			})
		},
		func(ctx context.Context, id int64) error {
			res, err := acts.Extract(ctx, pipeline.StageParams{FetchDocID: id})
			if err != nil {
				return err
			}
			log.Info("extracted", "fetch_doc", id, "document", res.DocumentID, "engine", res.Engine)
			return nil
		},
		log,
	)
}

func doNormalize(ctx context.Context, acts *pipeline.Activities, fetchDocID int64, log *slog.Logger) error {
	res, err := acts.Normalize(ctx, pipeline.StageParams{FetchDocID: fetchDocID})
	if err != nil {
		return err
	}
	log.Info("normalize done", "fetch_doc", fetchDocID,
		"document", res.DocumentID, "sections", res.SectionsParsed)
	return nil
}

func doNormalizeAll(ctx context.Context, acts *pipeline.Activities, afterID int64, limit int, force bool, log *slog.Logger) error {
	return runStageAll(ctx, "normalize", afterID, limit, force,
		func(ctx context.Context, afterID int64, batch int32) ([]int64, error) {
			return acts.ListFetchDocIDsNeedingNormalizeAfter(ctx, pipeline.ListStageFetchDocIDsAfterParams{
				AfterID: afterID, Limit: batch, Force: force,
			})
		},
		func(ctx context.Context, id int64) error {
			_, err := acts.Normalize(ctx, pipeline.StageParams{FetchDocID: id})
			return err
		},
		log,
	)
}

func doIndex(ctx context.Context, acts *pipeline.Activities, fetchDocID int64, log *slog.Logger) error {
	res, err := acts.Index(ctx, pipeline.StageParams{FetchDocID: fetchDocID})
	if err != nil {
		return err
	}
	log.Info("index done", "fetch_doc", fetchDocID,
		"document", res.DocumentID, "chunks", res.ChunksWritten)
	return nil
}

func doIndexAll(ctx context.Context, acts *pipeline.Activities, afterID int64, limit int, force bool, log *slog.Logger) error {
	return runStageAll(ctx, "index", afterID, limit, force,
		func(ctx context.Context, afterID int64, batch int32) ([]int64, error) {
			return acts.ListFetchDocIDsNeedingIndexAfter(ctx, pipeline.ListStageFetchDocIDsAfterParams{
				AfterID: afterID, Limit: batch, Force: force,
			})
		},
		func(ctx context.Context, id int64) error {
			_, err := acts.Index(ctx, pipeline.StageParams{FetchDocID: id})
			return err
		},
		log,
	)
}

func doEmbedAll(ctx context.Context, acts *pipeline.Activities, cfg *config.Config, afterID int64, limit int, force bool, parallel int, log *slog.Logger) error {
	res, err := runEmbedAllShard(ctx, acts, cfg, afterID, limit, force, parallel)
	if err != nil {
		return err
	}
	log.Info("embed-all done", "embedded", res.Embedded, "rejected", res.Rejected, "force", force)
	return nil
}

func embedAllParams(cfg *config.Config, limit int, force bool, parallel int) pipeline.EmbedAllParams {
	return pipeline.EmbedAllParams{
		Engine:                  cfg.EmbedEngine(),
		Owner:                   cfg.Embed.Kaggle.Owner,
		ModelDataset:            cfg.Embed.Kaggle.ModelDataset,
		Accelerator:             cfg.Embed.Kaggle.Accelerator,
		SageMakerBucket:         cfg.Embed.SageMaker.Bucket,
		SageMakerRoleARN:        cfg.Embed.SageMaker.RoleARN,
		SageMakerRegion:         cfg.Embed.SageMaker.Region,
		SageMakerInstanceType:   cfg.Embed.SageMaker.InstanceType,
		SageMakerContainerImage: cfg.Embed.SageMaker.ContainerImage,
		Dims:                    config.EmbedDims,
		Force:                   force,
		Limit:                   limit,
		Parallel:                parallel,
		MaxItems:                embedShardItems,
		MaxInputBytes:           embedShardInputBytes,
		MaxPages:                embedShardPages,
		MaxElapsed:              embedShardElapsed,
		MaxConcurrent:           embedShardConcurrency,
	}
}

func runEmbedAllShard(ctx context.Context, acts *pipeline.Activities, cfg *config.Config, afterID int64, limit int, force bool, parallel int) (pipeline.EmbedAllResult, error) {
	params := embedAllParams(cfg, limit, force, parallel)
	params.AfterID = afterID
	res, err := acts.EmbedAll(ctx, params)
	if err != nil {
		return res, err
	}
	if !res.Complete {
		cause := fmt.Errorf("bounded shard incomplete; resume with -after-id=%d", res.NextCursor)
		return res, &pipeline.EmbedAllError{Result: res, Cause: cause}
	}
	return res, nil
}

func doOcrAll(ctx context.Context, acts *pipeline.Activities, cfg *config.Config, afterID int64, limit int, force bool, log *slog.Logger) error {
	res, err := runOCRAllShard(ctx, acts, cfg, afterID, limit, force)
	if err != nil {
		return err
	}
	log.Info("ocr-all done", "processed", res.Processed, "rejected", res.Rejected, "failed", res.Failed)
	return nil
}

func runOCRAllShard(ctx context.Context, acts *pipeline.Activities, cfg *config.Config, afterID int64, limit int, force bool) (pipeline.OcrAllResult, error) {
	res, err := acts.OcrAll(ctx, pipeline.OcrAllParams{
		Engine: cfg.OcrEngine(), Owner: cfg.Extract.OCR.Kaggle.Owner,
		Accelerator: cfg.Extract.OCR.Kaggle.Accelerator, Command: cfg.Extract.OCR.Command,
		Script: cfg.Extract.OCR.Script, Languages: cfg.OCRLanguages(), DPI: cfg.Extract.OCR.DPI,
		BatchSize: cfg.Extract.OCR.BatchSize, Force: force, Limit: limit, AfterID: afterID,
		MaxItems: ocrShardItems, MaxInputBytes: ocrShardInputBytes, MaxPages: ocrShardPages,
		MaxElapsed: ocrShardElapsed, MaxConcurrent: ocrShardConcurrency,
		Concurrency:       cfg.Extract.OCR.DocumentAI.Concurrency,
		RequestsPerMinute: cfg.Extract.OCR.DocumentAI.RequestsPerMinute,
		S3Bucket:          cfg.Storage.S3DataBucket,
	})
	if err != nil {
		return res, err
	}
	if !res.Complete {
		cause := fmt.Errorf("bounded shard incomplete; resume with -after-id=%d", res.NextCursor)
		return res, &pipeline.OcrAllError{Result: res, Cause: cause}
	}
	return res, nil
}

func doBackfillRelations(ctx context.Context, acts *pipeline.Activities, limit int, log *slog.Logger) error {
	res, err := acts.BackfillRelationTargets(ctx, pipeline.BackfillRelationTargetsParams{
		Limit: int32(limit), //nolint:gosec // capped by flag default
	})
	if err != nil {
		return err
	}
	log.Info("backfill-relations done",
		"candidates", res.Candidates, "enqueued", res.Enqueued, "skipped", res.Skipped)
	return nil
}

func doLexicalIndex(ctx context.Context, acts *pipeline.Activities, log *slog.Logger) error {
	res, err := acts.LexicalIndex(ctx)
	if err != nil {
		return err
	}
	log.Info("lexical-index done", "written", res.Written)
	return nil
}

// --- drain: convergence loop ---

func doDrain(ctx context.Context, acts *pipeline.Activities, sources []string, limit, fetchConcurrency int, log *slog.Logger) error {
	const maxRounds = 6
	for round := 1; round <= maxRounds; round++ {
		if err := doBackfillRelations(ctx, acts, limit, log); err != nil {
			return err
		}
		fetched := 0
		for _, src := range sources {
			n, err := drainFetchSource(ctx, acts, src, limit, fetchConcurrency, log)
			if err != nil {
				return err
			}
			fetched += n
		}
		if err := doExtractAll(ctx, acts, 0, limit, log); err != nil {
			return err
		}
		if err := doNormalizeAll(ctx, acts, 0, limit, false, log); err != nil {
			return err
		}
		log.Info("drain round", "round", round, "fetched", fetched)
		if fetched == 0 {
			log.Info("drain converged", "rounds", round)
			return nil
		}
	}
	log.Warn("drain hit max rounds", "max_rounds", maxRounds)
	return nil
}

// --- run-all: full pipeline ---

func doRunAll(ctx context.Context, acts *pipeline.Activities, cfgQ *dbconfig.Queries, cfg *config.Config, sources []string, limit, fetchConcurrency int, force bool, log *slog.Logger) error {
	if force {
		return fmt.Errorf("run-all force cannot resume bounded forced stages; run each forced stage with an explicit cursor")
	}
	const maxRounds = 3

	// 1. Discover all slices concurrently (per-source groups run in parallel;
	// the DB upserts are idempotent). When limit > 0, slices run sequentially
	// so per-source caps are respected.
	log.Info("run-all: stage 1/6 — discover", "limit", limit)
	slices, err := acts.DiscoverSlices(ctx, sources)
	if err != nil {
		return fmt.Errorf("discover slices: %w", err)
	}

	type discoverResult struct {
		discovered int
		enqueued   int
	}

	var totalDiscovered, totalEnqueued int

	if limit > 0 {
		// Sequential: per-source cap needs ordered accounting.
		sourceEnqueued := map[string]int{}
		for _, s := range slices {
			if sourceEnqueued[s.Source] >= limit {
				continue
			}
			s.Limit = limit - sourceEnqueued[s.Source]
			res, err := acts.Discover(ctx, s)
			if err != nil {
				log.Warn("discover slice failed", "source", s.Source, "keyword", s.Keyword, "err", err)
				continue
			}
			totalDiscovered += res.Discovered
			totalEnqueued += res.Enqueued
			sourceEnqueued[s.Source] += res.Enqueued
		}
	} else {
		// Parallel: no limit, fire all slices concurrently.
		results := make([]discoverResult, len(slices))
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for i, s := range slices {
			wg.Add(1)
			go func(i int, s pipeline.DiscoverParams) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				res, err := acts.Discover(ctx, s)
				if err != nil {
					log.Warn("discover slice failed", "source", s.Source, "keyword", s.Keyword, "err", err)
					return
				}
				results[i] = discoverResult{discovered: res.Discovered, enqueued: res.Enqueued}
			}(i, s)
		}
		wg.Wait()
		for _, r := range results {
			totalDiscovered += r.discovered
			totalEnqueued += r.enqueued
		}
	}
	log.Info("run-all: discovery done", "slices", len(slices),
		"discovered", totalDiscovered, "enqueued", totalEnqueued)

	// 2. Convergence loop: backfill → fetch → extract → normalize.
	log.Info("run-all: stage 2/6 — convergence loop (backfill → fetch → extract → normalize)")
	converged := false
	var rounds int
	totalFetched, totalExtracted, totalNormalized, totalRelations := 0, 0, 0, 0
	for round := 1; round <= maxRounds; round++ {
		rounds = round
		br, err := acts.BackfillRelationTargets(ctx, pipeline.BackfillRelationTargetsParams{Limit: 1000})
		if err != nil {
			return fmt.Errorf("backfill relations (round %d): %w", round, err)
		}
		totalRelations += br.Enqueued

		fetched := 0
		for _, src := range sources {
			n, err := drainFetchSource(ctx, acts, src, 0, fetchConcurrency, log)
			if err != nil {
				return fmt.Errorf("fetch %s (round %d): %w", src, round, err)
			}
			fetched += n
		}
		totalFetched += fetched

		extracted, err := countStageAll(ctx, "extract",
			func(ctx context.Context, afterID int64, batch int32) ([]int64, error) {
				return acts.ListFetchDocIDsNeedingExtractAfter(ctx, pipeline.ListStageFetchDocIDsAfterParams{
					AfterID: afterID, Limit: batch,
				})
			},
			func(ctx context.Context, id int64) error {
				_, err := acts.Extract(ctx, pipeline.StageParams{FetchDocID: id})
				return err
			},
			log,
		)
		if err != nil {
			return fmt.Errorf("extract-all (round %d): %w", round, err)
		}
		totalExtracted += extracted

		normalized, err := countStageAll(ctx, "normalize",
			func(ctx context.Context, afterID int64, batch int32) ([]int64, error) {
				return acts.ListFetchDocIDsNeedingNormalizeAfter(ctx, pipeline.ListStageFetchDocIDsAfterParams{
					AfterID: afterID, Limit: batch,
				})
			},
			func(ctx context.Context, id int64) error {
				_, err := acts.Normalize(ctx, pipeline.StageParams{FetchDocID: id})
				return err
			},
			log,
		)
		if err != nil {
			return fmt.Errorf("normalize-all (round %d): %w", round, err)
		}
		totalNormalized += normalized

		log.Info("run-all: drain round", "round", round, "fetched", fetched,
			"extracted", extracted, "normalized", normalized, "relations_enqueued", br.Enqueued)
		if fetched == 0 {
			converged = true
			break
		}
	}

	// 3. OCR gate-flagged scans, then re-normalize.
	log.Info("run-all: stage 3/6 — OCR batch")
	ocrRes, err := runOCRAllShard(ctx, acts, cfg, 0, 0, false)
	if err != nil {
		return fmt.Errorf("ocr-all: %w", err)
	}
	if ocrRes.Processed > 0 {
		if err := doNormalizeAll(ctx, acts, 0, 0, false, log); err != nil {
			return fmt.Errorf("normalize-all (post-OCR): %w", err)
		}
	}

	// 4. Index.
	log.Info("run-all: stage 4/6 — index")
	indexStage := pipeline.StageAllParams{Force: force}
	_ = indexStage
	indexed, err := countStageAll(ctx, "index",
		func(ctx context.Context, afterID int64, batch int32) ([]int64, error) {
			return acts.ListFetchDocIDsNeedingIndexAfter(ctx, pipeline.ListStageFetchDocIDsAfterParams{
				AfterID: afterID, Limit: batch, Force: force,
			})
		},
		func(ctx context.Context, id int64) error {
			_, err := acts.Index(ctx, pipeline.StageParams{FetchDocID: id})
			return err
		},
		log,
	)
	if err != nil {
		return fmt.Errorf("index-all: %w", err)
	}

	// 5. Embed.
	log.Info("run-all: stage 5/6 — embed")
	embedRes, err := runEmbedAllShard(ctx, acts, cfg, 0, 0, force, embedShardConcurrency)
	if err != nil {
		return fmt.Errorf("embed-all: %w", err)
	}

	// 6. Lexical index.
	log.Info("run-all: stage 6/6 — lexical index")
	lexRes, err := acts.LexicalIndex(ctx)
	if err != nil {
		return fmt.Errorf("lexical-index: %w", err)
	}

	log.Info("run-all complete",
		"converged", converged, "rounds", rounds,
		"discover_slices", len(slices), "discovered", totalDiscovered, "enqueued", totalEnqueued,
		"fetched", totalFetched, "extracted", totalExtracted, "normalized", totalNormalized,
		"relations_enqueued", totalRelations, "ocr_processed", ocrRes.Processed,
		"indexed_chunks", indexed, "embedded", embedRes.Embedded,
		"lexical_indexed", lexRes.Written, "force", force)
	return nil
}

// --- batch helpers ---

const (
	// ID-only legacy listers declare one 8-byte key and one logical record per
	// item. These bounds do not measure source files or decoded parser input.
	stagePageSize   = 200
	stageInputBytes = int64(64 << 20)
	stagePages      = 2000
	stageElapsed    = 15 * time.Minute
)

type listFn func(ctx context.Context, afterID int64, batch int32) ([]int64, error)
type processFn func(ctx context.Context, id int64) error

func runStageAll(ctx context.Context, stage string, afterID int64, limit int, force bool, list listFn, process processFn, log *slog.Logger) error {
	result, err := runStageBatch(ctx, stage, afterID, limit, list, process)
	if err != nil {
		log.Error(stage+"-all failed", "total", result.Attempted, "completed", result.Succeeded,
			"rejected", result.Rejected, "failed", result.Failed, "next_cursor", result.NextCursor, "force", force)
		return err
	}
	log.Info(stage+"-all done", "total", result.Attempted, "completed", result.Succeeded,
		"rejected", result.Rejected, "failed", result.Failed, "force", force)
	return nil
}

func countStageAll(ctx context.Context, stage string, list listFn, process processFn, log *slog.Logger) (int, error) {
	log.Debug(stage+" starting", "concurrency", stageWorkerConcurrency(), "batch_size", stagePageSize)
	result, err := runStageBatch(ctx, stage, 0, 0, list, process)
	return result.Succeeded, err
}

func stageWorkerConcurrency() int {
	return max(1, min(runtime.NumCPU(), 4))
}

func runStageBatch(ctx context.Context, stage string, afterID int64, limit int, list listFn, process processFn) (corpusrun.BatchResult, error) {
	result, err := corpusrun.RunKeysetBatch(ctx, corpusrun.BatchConfig{
		Stage: stage, AfterID: afterID, Limit: limit,
		Budget: corpusrun.Budget{
			MaxItems: stagePageSize, MaxInputBytes: stageInputBytes, MaxPages: stagePages,
			MaxElapsed: stageElapsed, MaxConcurrent: stageWorkerConcurrency(),
		},
	}, func(ctx context.Context, afterID int64, pageSize int) ([]corpusrun.BatchItem, error) {
		ids, err := list(ctx, afterID, int32(pageSize))
		if err != nil {
			return nil, err
		}
		items := make([]corpusrun.BatchItem, len(ids))
		for i, id := range ids {
			items[i] = corpusrun.BatchItem{ID: id, InputBytes: 8, Pages: 1}
		}
		return items, nil
	}, corpusrun.BatchProcessFunc(process))
	if err != nil {
		return result, err
	}
	if !result.Complete {
		cause := fmt.Errorf("bounded shard incomplete; resume with -after-id=%d", result.NextCursor)
		return result, &corpusrun.BatchError{Stage: stage, Result: result, Cause: cause}
	}
	return result, nil
}

// doResolveRefs recomputes silver.doc_ref.document_id from ref_key. Resolution
// normally happens once, when relation evidence is written, so rebuilding
// silver.document leaves every pointer dangling. Dry run by default: -force
// writes. Touches one column — no sections, chunks or embeddings.
func doResolveRefs(ctx context.Context, acts *pipeline.Activities, apply bool, log *slog.Logger) error {
	res, err := acts.ResolveRefs(ctx, apply)
	if err != nil {
		return fmt.Errorf("resolve refs: %w", err)
	}
	log.Info("resolve-refs done",
		"applied", apply, "scanned", res.Scanned, "resolved", res.Resolved,
		"corrected", res.Corrected, "cleared", res.Cleared,
		"ambiguous", res.Ambiguous, "unmatched", res.Unmatched, "unchanged", res.Unchanged)
	if !apply {
		log.Info("resolve-refs was a DRY RUN; re-run with -force to write")
	}
	return nil
}

// doRecomputeRelations re-derives relation evidence from bronze for one source.
// Normalize would do this too, but it rewrites sections and so forces a
// re-index; this pass writes only relation rows. Dry run unless -force.
func doRecomputeRelations(ctx context.Context, acts *pipeline.Activities, source string, apply bool, limit int, log *slog.Logger) error {
	if source == "all" {
		source = ""
	}
	res, err := acts.RecomputeRelations(ctx, source, apply, limit)
	if err != nil {
		return fmt.Errorf("recompute relations: %w", err)
	}
	log.Info("recompute-relations done",
		"applied", apply, "source", source, "scanned", res.Scanned, "processed", res.Processed,
		"skipped", res.Skipped, "evidence", res.EvidenceWritten, "relations", res.RelationsWritten,
		"failed", res.Failed)
	if !apply {
		log.Info("recompute-relations was a DRY RUN; re-run with -force to write")
	}
	return nil
}
