package pipeline

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"danny.vn/banhmi/pkg/base/config"
)

// vbplSource is the source id whose discovery fans out across config keywords, in
// addition to its keyword-less sweep. It mirrors EnsureSchedules' vbpl handling.
const vbplSource = "vbpl"

const (
	// defaultRunAllRounds caps the backfill→fetch→extract→normalize convergence
	// loop. Each round fetches one level deeper into the VBPL amendment graph
	// (round N → depth N), so 3 resolves a typical circular→decree→law chain in a
	// single run. We cap rather than fully close the graph: backfilled targets
	// bypass scope filtering, so deeper levels pull in increasingly tangential law.
	// This loses nothing the contract promises — relation edges are still recorded
	// from VBPL detail, missing target text is surfaced as a gap, and the daily
	// schedule drains anything deeper over subsequent runs. Early-stop (a fetch pass
	// claiming 0) means a settled corpus converges in ~1-2 rounds regardless.
	defaultRunAllRounds = 3
	// defaultRunAllBackfillLimit caps relation targets enqueued per round; the loop
	// re-runs it each round so the whole graph drains over multiple rounds.
	defaultRunAllBackfillLimit = 1000
)

// RunAllParams configures one whole-pipeline run. It carries only config-derived
// knobs — never the Kaggle token, which lives on the worker (Activities) so it is
// never persisted in Temporal workflow history.
type RunAllParams struct {
	// Sources are the sources FetchAll drains each round.
	Sources []string
	// MaxArtifacts caps artifacts claimed per FetchAll pass (per source).
	MaxArtifacts int
	// MaxRounds caps the backfill→fetch→extract→normalize convergence loop.
	MaxRounds int
	// BackfillLimit caps relation targets enqueued per round.
	BackfillLimit int32
	// Stage controls ExtractAll/NormalizeAll/IndexAll pagination + Force.
	Stage StageAllParams
	// SkipOCR disables the OCR batch step (e.g. when no scans are expected).
	SkipOCR bool
	// Ocr configures the OcrAll batch (engine + Kaggle knobs from config).
	Ocr OcrAllParams
	// SkipEmbed disables the EmbedAll batch step (chunks still written; embeddings
	// backfilled later).
	SkipEmbed bool
	// Embed configures the EmbedAll Kaggle batch (Owner/Dataset/Accelerator/Dims).
	Embed EmbedAllParams
}

// RunAllResult summarizes one whole-pipeline run.
type RunAllResult struct {
	DiscoverSlices    int
	Discovered        int
	Enqueued          int
	Rounds            int
	Converged         bool
	Fetched           int // artifacts claimed across all rounds
	Extracted         int // docs extracted across all rounds
	Normalized        int // docs normalized across all rounds (incl. post-OCR)
	RelationsEnqueued int
	OcrProcessed      int
	IndexedChunks     int
	Embedded          int
}

// RunAllParamsFromConfig builds the run-all parameters from config. It carries no
// secrets: the Kaggle token authenticates the batch clients from the Activities
// struct on the worker, never through these (persisted) params.
func RunAllParamsFromConfig(cfg *config.Config) RunAllParams {
	var sources []string
	if cfg.Sources.Congbao.Enabled {
		sources = append(sources, "congbao")
	}
	if cfg.Sources.VBPL.Enabled {
		sources = append(sources, vbplSource)
	}
	if cfg.Sources.SBVHanoi.Enabled {
		sources = append(sources, "sbv_hanoi")
	}
	return RunAllParams{
		Sources: sources,
		// 0 = drain the whole fetch queue each round (like -drain). The per-fire
		// fetchMaxArtifacts cap is for the granular Fetch *schedule*; using it here
		// would cap each convergence round, so a cold build would stop after
		// MaxRounds*cap artifacts instead of fully draining. With 0, MaxRounds is a
		// relation-depth bound, not a fetch-count ceiling; concurrency still rate-limits.
		MaxArtifacts:  0,
		MaxRounds:     defaultRunAllRounds,
		BackfillLimit: defaultRunAllBackfillLimit,
		// Zero Stage knobs mean "drain all rows, default batch/concurrency, no
		// force" — the *All workflows apply their own defaults.
		Stage: StageAllParams{},
		Ocr: OcrAllParams{
			Engine:      cfg.OcrEngine(),
			Owner:       cfg.Extract.OCR.Kaggle.Owner,
			Accelerator: cfg.Extract.OCR.Kaggle.Accelerator,
			Command:     cfg.Extract.OCR.Command,
			Script:      cfg.Extract.OCR.Script,
			Languages:   cfg.Extract.OCR.Languages,
			DPI:         cfg.Extract.OCR.DPI,
			BatchSize:   cfg.Extract.OCR.BatchSize,
		},
		Embed: EmbedAllParams{
			Owner:        cfg.Embed.Kaggle.Owner,
			ModelDataset: cfg.Embed.Kaggle.ModelDataset,
			Accelerator:  cfg.Embed.Kaggle.Accelerator,
			Dims:         config.EmbedDims,
		},
	}
}

// RunAllWorkflow runs the entire ingestion pipeline end to end as one workflow:
// discover every enabled (source, keyword) slice, drain backfill→fetch→extract→
// normalize to convergence (relation backfill is inside the loop, so promoted
// targets are fetched and processed in the same run), OCR the gate-flagged scans
// and re-normalize them, index all normalized docs, then embed all/missing chunks
// on the Kaggle GPU batch. Operators run it on the single paused run-all schedule.
//
// Stages reuse the existing per-stage workflows as child workflows for per-stage
// visibility; discovery calls the Discover activity directly per slice (there can
// be many keyword slices, so child workflows per slice would be noise).
func RunAllWorkflow(ctx workflow.Context, p RunAllParams) (RunAllResult, error) {
	log := workflow.GetLogger(ctx)
	info := workflow.GetInfo(ctx)
	baseTQ := info.TaskQueueName
	parentID := info.WorkflowExecution.ID
	var res RunAllResult

	maxRounds := p.MaxRounds
	if maxRounds <= 0 {
		maxRounds = defaultRunAllRounds
	}

	// 1. Discover all slices. DiscoverSlices is a quick DB read on the local queue;
	// each Discover hits a remote feed on the external queue.
	var acts *Activities
	sliceCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:           LocalActivityTaskQueue(baseTQ),
		StartToCloseTimeout: time.Minute,
	})
	var slices []DiscoverParams
	if err := workflow.ExecuteActivity(sliceCtx, acts.DiscoverSlices, p.Sources).Get(sliceCtx, &slices); err != nil {
		return res, fmt.Errorf("run-all: discover slices: %w", err)
	}
	res.DiscoverSlices = len(slices)

	discoverCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:           ExternalActivityTaskQueue(baseTQ),
		StartToCloseTimeout: 15 * time.Minute,
		HeartbeatTimeout:    2 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})
	for _, s := range slices {
		var dr DiscoverResult
		if err := workflow.ExecuteActivity(discoverCtx, acts.Discover, s).Get(discoverCtx, &dr); err != nil {
			// One source/keyword failing must not abort the whole run; the next
			// scheduled run retries it from the same watermark.
			log.Warn("run-all: discover slice failed", "source", s.Source, "keyword", s.Keyword, "err", err)
			continue
		}
		res.Discovered += dr.Discovered
		res.Enqueued += dr.Enqueued
	}
	log.Info("run-all: discovery done", "slices", res.DiscoverSlices, "discovered", res.Discovered, "enqueued", res.Enqueued)

	// 2. Convergence loop: backfill → fetch → extract → normalize, until a fetch
	// pass claims nothing (mirrors -drain). Backfill runs first so this round's
	// fetch drains any newly-promoted relation targets.
	for round := 1; round <= maxRounds; round++ {
		res.Rounds = round

		var br BackfillRelationTargetsResult
		if err := execChild(ctx, baseTQ, childID(parentID, "backfill", round), workflowBackfillRelations,
			BackfillRelationTargetsParams{Limit: p.BackfillLimit}, &br); err != nil {
			return res, fmt.Errorf("run-all: backfill relations (round %d): %w", round, err)
		}
		res.RelationsEnqueued += br.Enqueued

		var fr FetchAllResult
		if err := execChild(ctx, baseTQ, childID(parentID, "fetch", round), workflowFetchAll,
			FetchAllParams{Sources: p.Sources, MaxArtifacts: p.MaxArtifacts}, &fr); err != nil {
			return res, fmt.Errorf("run-all: fetch-all (round %d): %w", round, err)
		}
		res.Fetched += fr.Claimed

		var er ExtractAllResult
		if err := execChild(ctx, baseTQ, childID(parentID, "extract", round), workflowExtractAll,
			p.Stage, &er); err != nil {
			return res, fmt.Errorf("run-all: extract-all (round %d): %w", round, err)
		}
		res.Extracted += er.Completed

		var nr NormalizeAllResult
		if err := execChild(ctx, baseTQ, childID(parentID, "normalize", round), workflowNormalizeAll,
			p.Stage, &nr); err != nil {
			return res, fmt.Errorf("run-all: normalize-all (round %d): %w", round, err)
		}
		res.Normalized += nr.Completed

		log.Info("run-all: drain round", "round", round, "claimed", fr.Claimed,
			"extracted", er.Completed, "normalized", nr.Completed, "relations_enqueued", br.Enqueued)
		if fr.Claimed == 0 {
			res.Converged = true
			break
		}
	}
	if !res.Converged {
		log.Warn("run-all: drain hit max rounds; pending fetch work may remain", "max_rounds", maxRounds)
	}

	// 3. OCR the gate-flagged scans (Kaggle GPU or local CPU), then re-normalize
	// the now-texted docs so their sections/validity land before indexing.
	if !p.SkipOCR {
		var or OcrAllResult
		if err := execChild(ctx, baseTQ, childID(parentID, "ocr", 0), workflowOcrAll, p.Ocr, &or); err != nil {
			return res, fmt.Errorf("run-all: ocr-all: %w", err)
		}
		res.OcrProcessed = or.Processed
		if or.Processed > 0 {
			var nr NormalizeAllResult
			if err := execChild(ctx, baseTQ, childID(parentID, "normalize-ocr", 0), workflowNormalizeAll, p.Stage, &nr); err != nil {
				return res, fmt.Errorf("run-all: normalize-all (post-OCR): %w", err)
			}
			res.Normalized += nr.Completed
		}
	}

	// 4. Index every normalized doc that still needs chunks.
	var ir IndexAllResult
	if err := execChild(ctx, baseTQ, childID(parentID, "index", 0), workflowIndexAll, p.Stage, &ir); err != nil {
		return res, fmt.Errorf("run-all: index-all: %w", err)
	}
	res.IndexedChunks = ir.ChunksWritten

	// 5. Embed all/missing chunks in one Kaggle GPU batch (query path unaffected).
	if !p.SkipEmbed {
		var emb EmbedAllResult
		if err := execChild(ctx, baseTQ, childID(parentID, "embed", 0), workflowEmbedAll, p.Embed, &emb); err != nil {
			return res, fmt.Errorf("run-all: embed-all: %w", err)
		}
		res.Embedded = emb.Embedded
	}

	log.Info("run-all complete",
		"converged", res.Converged, "rounds", res.Rounds,
		"discover_slices", res.DiscoverSlices, "discovered", res.Discovered, "enqueued", res.Enqueued,
		"fetched", res.Fetched, "normalized", res.Normalized, "relations_enqueued", res.RelationsEnqueued,
		"ocr_processed", res.OcrProcessed, "indexed_chunks", res.IndexedChunks, "embedded", res.Embedded)
	return res, nil
}

// execChild runs one stage as a child workflow on the workflow task queue and
// waits for its result. Child IDs are deterministic (parent + stage [+ round]) so
// replay is stable.
func execChild(ctx workflow.Context, taskQueue, id, name string, params, result any) error {
	cctx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID: id,
		TaskQueue:  taskQueue,
	})
	return workflow.ExecuteChildWorkflow(cctx, name, params).Get(cctx, result)
}

// childID builds a deterministic child workflow id. round 0 marks a once-per-run
// stage (ocr/index/embed); a positive round disambiguates loop iterations.
func childID(parent, stage string, round int) string {
	if round > 0 {
		return fmt.Sprintf("%s-%s-%d", parent, stage, round)
	}
	return fmt.Sprintf("%s-%s", parent, stage)
}

// DiscoverSlices returns the (source, keyword) discovery slices to run for the
// given enabled sources, mirroring EnsureSchedules: each source gets a keyword-less
// sweep, and vbpl adds one slice per configured discovery keyword. Computed at run
// time so keyword edits take effect without re-deploying the schedule. Unknown
// source ids (not wired in this worker) are skipped.
func (a *Activities) DiscoverSlices(ctx context.Context, sources []string) ([]DiscoverParams, error) {
	ids := append([]string(nil), sources...)
	sort.Strings(ids) // deterministic order for stable logs

	var slices []DiscoverParams
	for _, id := range ids {
		if _, ok := a.sources[id]; !ok {
			continue // not wired in this worker
		}
		slices = append(slices, DiscoverParams{Source: id})
		if id == vbplSource {
			keywords, err := a.configQ.ListDiscoveryKeywords(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("list %s discovery keywords: %w", id, err)
			}
			for _, kw := range keywords {
				slices = append(slices, DiscoverParams{Source: id, Keyword: kw})
			}
		}
	}
	return slices, nil
}
