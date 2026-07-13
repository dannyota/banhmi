// Command pipeline runs banhmi's ingestion pipeline without Temporal. It calls
// pipeline activity methods directly: Discover, Fetch, Extract, Normalize, Index,
// EmbedAll, OcrAll, LexicalIndex, and the orchestrated RunAll. Structured slog
// output goes to stdout (captured by GCP Cloud Logging when running as a Cloud
// Run Job). Each stage flag runs one stage and exits; -run-all runs the full
// pipeline to convergence.
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
	extract           int64
	extractAll        bool
	normalize         int64
	normalizeAll      bool
	index             int64
	indexAll          bool
	embedAll          bool
	ocrAll            bool
	backfillRelations bool
	lexindex          bool
	drain             bool
	runAll            bool
	force             bool
	serveEmbed        string
}

func main() {
	var o runOpts
	flag.StringVar(&o.cfgPath, "config", "config/config.yaml", "path to config file")
	flag.StringVar(&o.discover, "discover", "", "run Discover once for this source (or 'all'), then exit")
	flag.StringVar(&o.keyword, "keyword", "", "query keyword for -discover (vbpl; congbao ignores it)")
	flag.StringVar(&o.fetch, "fetch", "", "run Fetch once for this source (or 'all'), then exit")
	flag.IntVar(&o.max, "max", 5, "max concurrent fetch activities")
	flag.IntVar(&o.limit, "limit", 0, "max docs per source for -run-all discover, or artifacts for -fetch, or docs for -extract-all/-normalize-all/-index-all; 0 = all")
	flag.Int64Var(&o.extract, "extract", 0, "run Extract once for this fetch_doc id, then exit")
	flag.BoolVar(&o.extractAll, "extract-all", false, "run Extract for all fetch_doc rows that need text, then exit")
	flag.Int64Var(&o.normalize, "normalize", 0, "run Normalize once for this fetch_doc id, then exit")
	flag.BoolVar(&o.normalizeAll, "normalize-all", false, "run Normalize for all fetch_doc rows that need it, then exit")
	flag.Int64Var(&o.index, "index", 0, "run Index once for this fetch_doc id, then exit")
	flag.BoolVar(&o.indexAll, "index-all", false, "run Index for all normalized fetch_doc rows, then exit")
	flag.BoolVar(&o.embedAll, "embed-all", false, "run EmbedAll (batch embed all/missing chunks), then exit")
	flag.BoolVar(&o.ocrAll, "ocr-all", false, "run OcrAll (batch OCR of gate-flagged scans), then exit")
	flag.BoolVar(&o.backfillRelations, "backfill-relations", false, "enqueue promoted official relation targets, then exit")
	flag.BoolVar(&o.lexindex, "lexindex", false, "rebuild BM25 sparse vectors, then exit")
	flag.BoolVar(&o.drain, "drain", false, "run the INPUT pipeline to convergence (backfill→fetch→extract→normalize), then exit")
	flag.BoolVar(&o.runAll, "run-all", false, "run the whole pipeline to convergence, then exit")
	flag.BoolVar(&o.force, "force", false, "force reruns for supported stages; with -discover, ignore the stored watermark (full rescan)")
	flag.StringVar(&o.serveEmbed, "serve-embed", "", "start HTTP embedding server on this address (e.g. :8089)")
	flag.Parse()

	dns.InstallFallback()

	log := blog.New(os.Getenv("BANHMI_LOG_LEVEL"))
	if err := run(o, log); err != nil {
		log.Error("pipeline", "err", err)
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

	if o.serveEmbed != "" {
		return serveEmbed(ctx, o.serveEmbed, log)
	}

	application, err := app.New(ctx, cfg, log, app.WithoutTemporal())
	if err != nil {
		return err
	}
	defer application.Close()

	return application.Container.Invoke(func(acts *pipeline.Activities, cfgQ *dbconfig.Queries) error {
		return dispatch(ctx, acts, cfgQ, cfg, o, log)
	})
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
		return doExtractAll(ctx, acts, o.limit, log)
	case o.normalize > 0:
		return doNormalize(ctx, acts, o.normalize, log)
	case o.normalizeAll:
		return doNormalizeAll(ctx, acts, o.limit, o.force, log)
	case o.index > 0:
		return doIndex(ctx, acts, o.index, log)
	case o.indexAll:
		return doIndexAll(ctx, acts, o.limit, o.force, log)
	case o.embedAll:
		return doEmbedAll(ctx, acts, cfg, o.limit, o.force, log)
	case o.ocrAll:
		return doOcrAll(ctx, acts, cfg, o.limit, o.force, log)
	case o.backfillRelations:
		return doBackfillRelations(ctx, acts, o.limit, log)
	case o.lexindex:
		return doLexicalIndex(ctx, acts, log)
	case o.drain:
		return doDrain(ctx, acts, sources, o.limit, log)
	case o.runAll:
		return doRunAll(ctx, acts, cfgQ, cfg, sources, o.limit, o.force, log)
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
		claimed, err := drainFetchSource(ctx, acts, src, o.limit, log)
		if err != nil {
			return fmt.Errorf("fetch %s: %w", src, err)
		}
		log.Info("fetch done", "source", src, "claimed", claimed)
	}
	return nil
}

func drainFetchSource(ctx context.Context, acts *pipeline.Activities, source string, limit int, log *slog.Logger) (int, error) {
	const fetchConcurrency = 10
	total := 0
	for {
		if limit > 0 && total >= limit {
			break
		}
		batch := 20
		if limit > 0 && limit-total < batch {
			batch = limit - total
		}
		claimed, err := acts.ClaimArtifacts(ctx, pipeline.ClaimParams{Source: source, Limit: batch})
		if err != nil {
			return total, fmt.Errorf("claim artifacts: %w", err)
		}
		if len(claimed) == 0 {
			break
		}
		var wg sync.WaitGroup
		sem := make(chan struct{}, fetchConcurrency)
		for _, art := range claimed {
			wg.Add(1)
			go func(art pipeline.ClaimedArtifact) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				switch art.Kind {
				case "body":
					if _, err := acts.PlanBody(ctx, art); err != nil {
						log.Warn("fetch body failed", "id", art.ID, "err", err)
					}
				case "tree":
					if _, err := acts.FetchTree(ctx, art); err != nil {
						log.Warn("fetch tree failed", "id", art.ID, "err", err)
					}
				case "file":
					if _, err := acts.FetchFile(ctx, art); err != nil {
						log.Warn("fetch file failed", "id", art.ID, "err", err)
					}
				default:
					log.Warn("unknown artifact kind", "id", art.ID, "kind", art.Kind)
				}
			}(art)
		}
		wg.Wait()
		total += len(claimed)
	}
	return total, nil
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

func doExtractAll(ctx context.Context, acts *pipeline.Activities, limit int, log *slog.Logger) error {
	return runStageAll(ctx, "extract", limit, false,
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

func doNormalizeAll(ctx context.Context, acts *pipeline.Activities, limit int, force bool, log *slog.Logger) error {
	return runStageAll(ctx, "normalize", limit, force,
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

func doIndexAll(ctx context.Context, acts *pipeline.Activities, limit int, force bool, log *slog.Logger) error {
	return runStageAll(ctx, "index", limit, force,
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

func doEmbedAll(ctx context.Context, acts *pipeline.Activities, cfg *config.Config, limit int, force bool, log *slog.Logger) error {
	res, err := acts.EmbedAll(ctx, pipeline.EmbedAllParams{
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
	})
	if err != nil {
		return err
	}
	log.Info("embed-all done", "embedded", res.Embedded, "force", force)
	return nil
}

func doOcrAll(ctx context.Context, acts *pipeline.Activities, cfg *config.Config, limit int, force bool, log *slog.Logger) error {
	res, err := acts.OcrAll(ctx, pipeline.OcrAllParams{
		Engine:      cfg.OcrEngine(),
		Owner:       cfg.Extract.OCR.Kaggle.Owner,
		Accelerator: cfg.Extract.OCR.Kaggle.Accelerator,
		Command:     cfg.Extract.OCR.Command,
		Script:      cfg.Extract.OCR.Script,
		Languages:   cfg.OCRLanguages(),
		DPI:         cfg.Extract.OCR.DPI,
		BatchSize:   cfg.Extract.OCR.BatchSize,
		Force:       force,
		Limit:       limit,
		Processor:   cfg.Extract.OCR.DocumentAI.Processor,
		Bucket:      cfg.Extract.OCR.DocumentAI.Bucket,
	})
	if err != nil {
		return err
	}
	log.Info("ocr-all done", "processed", res.Processed, "failed", res.Failed)
	return nil
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

func doDrain(ctx context.Context, acts *pipeline.Activities, sources []string, limit int, log *slog.Logger) error {
	const maxRounds = 6
	for round := 1; round <= maxRounds; round++ {
		if err := doBackfillRelations(ctx, acts, limit, log); err != nil {
			return err
		}
		fetched := 0
		for _, src := range sources {
			n, err := drainFetchSource(ctx, acts, src, limit, log)
			if err != nil {
				return err
			}
			fetched += n
		}
		if err := doExtractAll(ctx, acts, limit, log); err != nil {
			return err
		}
		if err := doNormalizeAll(ctx, acts, limit, false, log); err != nil {
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

func doRunAll(ctx context.Context, acts *pipeline.Activities, cfgQ *dbconfig.Queries, cfg *config.Config, sources []string, limit int, force bool, log *slog.Logger) error {
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
			n, err := drainFetchSource(ctx, acts, src, 0, log)
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
	ocrRes, err := acts.OcrAll(ctx, pipeline.OcrAllParams{
		Engine:      cfg.OcrEngine(),
		Owner:       cfg.Extract.OCR.Kaggle.Owner,
		Accelerator: cfg.Extract.OCR.Kaggle.Accelerator,
		Command:     cfg.Extract.OCR.Command,
		Script:      cfg.Extract.OCR.Script,
		Languages:   cfg.OCRLanguages(),
		DPI:         cfg.Extract.OCR.DPI,
		BatchSize:   cfg.Extract.OCR.BatchSize,
		Processor:   cfg.Extract.OCR.DocumentAI.Processor,
		Bucket:      cfg.Extract.OCR.DocumentAI.Bucket,
	})
	if err != nil {
		return fmt.Errorf("ocr-all: %w", err)
	}
	if ocrRes.Processed > 0 {
		if err := doNormalizeAll(ctx, acts, 0, false, log); err != nil {
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
	embedRes, err := acts.EmbedAll(ctx, pipeline.EmbedAllParams{
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
	})
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

func localConcurrency() int {
	n := runtime.NumCPU() - 2
	if n < 4 {
		return 4
	}
	return n
}

type listFn func(ctx context.Context, afterID int64, batch int32) ([]int64, error)
type processFn func(ctx context.Context, id int64) error

func runStageAll(ctx context.Context, stage string, limit int, force bool, list listFn, process processFn, log *slog.Logger) error {
	maxConcurrent := localConcurrency()
	batchSize := int32(200)
	var afterID int64
	total, completed, failed := 0, 0, 0

	for {
		ids, err := list(ctx, afterID, batchSize)
		if err != nil {
			return fmt.Errorf("list %s: %w", stage, err)
		}
		if len(ids) == 0 {
			break
		}

		sem := make(chan struct{}, maxConcurrent)
		var mu sync.Mutex
		var wg sync.WaitGroup

		for _, id := range ids {
			if limit > 0 && total >= limit {
				break
			}
			total++
			id := id
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				if err := process(ctx, id); err != nil {
					log.Warn(stage+" failed", "fetch_doc", id, "err", err)
					mu.Lock()
					failed++
					mu.Unlock()
					return
				}
				mu.Lock()
				completed++
				mu.Unlock()
			}()
		}
		wg.Wait()

		if limit > 0 && total >= limit {
			break
		}
		afterID = ids[len(ids)-1]
	}

	log.Info(stage+"-all done", "total", total, "completed", completed, "failed", failed, "force", force)
	return nil
}

func countStageAll(ctx context.Context, stage string, list listFn, process processFn, log *slog.Logger) (int, error) {
	maxConcurrent := localConcurrency()
	batchSize := int32(200)
	var afterID int64
	completed := 0

	log.Debug(stage+" starting", "concurrency", maxConcurrent, "batch_size", batchSize)

	for {
		ids, err := list(ctx, afterID, batchSize)
		if err != nil {
			return completed, fmt.Errorf("list %s: %w", stage, err)
		}
		if len(ids) == 0 {
			break
		}
		log.Debug(stage+" batch", "ids", len(ids), "completed_so_far", completed)

		sem := make(chan struct{}, maxConcurrent)
		var mu sync.Mutex
		var wg sync.WaitGroup

		for _, id := range ids {
			id := id
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				if err := process(ctx, id); err != nil {
					log.Warn(stage+" failed", "fetch_doc", id, "err", err)
					return
				}
				mu.Lock()
				completed++
				mu.Unlock()
			}()
		}
		wg.Wait()
		afterID = ids[len(ids)-1]
	}
	return completed, nil
}
