// Command eval runs banhmi's retrieval-quality harness over a golden Q&A set. It
// builds the dig container (pkg/app), Invokes the retriever, runs each golden case,
// and prints per-case + aggregate metrics — recall@k, MRR@k, current-law precision,
// and abstention correctness (see pkg/eval, PLAN.md, CLAUDE.md "accuracy first"). It
// exits non-zero when an aggregate metric falls below a configured floor so `make
// eval` can gate CI before defaults are locked.
//
// banhmi is evidence-only: there is no answer model to score. -retrieval-mode compares
// bm25/vector/hybrid first-stage ranking (vector is the production default). When the
// corpus is empty, eval prints a clear note and exits 0 (skip, not fail), so `make
// eval` is safe to run against an empty stack.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"danny.vn/banhmi/pkg/app"
	"danny.vn/banhmi/pkg/base/config"
	blog "danny.vn/banhmi/pkg/base/log"
	"danny.vn/banhmi/pkg/eval"
	"danny.vn/banhmi/pkg/rag/retrieve"
)

// errSkip signals an intentional, clean skip (no live stack / no data). main maps
// it to exit 0 with a note, distinct from a real failure or a threshold breach.
var errSkip = errors.New("eval skipped")

// errThreshold signals that the run completed but an aggregate metric fell below a
// configured floor. main maps it to a non-zero exit so CI gates on it.
var errThreshold = errors.New("eval below threshold")

type opts struct {
	cfgPath            string
	golden             string
	topK               int
	retrievalOnly      bool
	retrievalMode      string
	rerankEndpoint     string
	rerankModel        string
	rerankCandidates   int
	rerankQwenTemplate bool
	rerankInstruction  string
	review             bool
	reviewHits         int
	reviewPreviewChars int
	abstainMinScore    float64

	minRecall   float64
	minMRR      float64
	minCitation float64
	minInForce  float64
	minAbstain  float64
}

func main() {
	var o opts
	flag.StringVar(&o.cfgPath, "config", "config/config.yaml", "path to config file")
	flag.StringVar(&o.golden, "golden", "deploy/eval/golden.json", "path to the golden Q&A set")
	flag.IntVar(&o.topK, "top-k", 0, "override retriever top-k (0 = config default)")
	flag.BoolVar(&o.retrievalOnly, "retrieval-only", true, "retrieval-only scoring (always on; answer mode removed)")
	flag.StringVar(&o.retrievalMode, "retrieval-mode", "hybrid", "retrieval-only mode: bm25, vector, or hybrid")
	flag.StringVar(&o.rerankEndpoint, "rerank-endpoint", "", "optional rerank base URL or /rerank URL for retrieval-only eval")
	flag.StringVar(&o.rerankModel, "rerank-model", "", "rerank model name sent to the rerank endpoint")
	flag.IntVar(&o.rerankCandidates, "rerank-candidates", 50, "first-stage candidates to retrieve before reranking")
	flag.BoolVar(&o.rerankQwenTemplate, "rerank-qwen3-template", false, "wrap query/documents with the Qwen3 reranker prompt template")
	flag.StringVar(&o.rerankInstruction, "rerank-instruction", "Given a Vietnamese legal search query, retrieve relevant legal passages that answer the query.", "instruction used by the Qwen3 reranker template")
	flag.BoolVar(&o.review, "review", false, "print DB-only RAG evidence review; requires -retrieval-only")
	flag.IntVar(&o.reviewHits, "review-hits", 3, "number of top hits to print per case when -review is set")
	flag.IntVar(&o.reviewPreviewChars, "review-preview-chars", 240, "max content preview characters per hit when -review is set")
	flag.Float64Var(&o.abstainMinScore, "abstain-min-score", 0, "retrieval-only abstention floor on top hit score; 0 disables")
	flag.Float64Var(&o.minRecall, "min-recall", 0, "fail if aggregate recall@k is below this (0 = no gate)")
	flag.Float64Var(&o.minMRR, "min-mrr", 0, "fail if aggregate mrr@k is below this (0 = no gate)")
	flag.Float64Var(&o.minCitation, "min-citation", 0, "fail if aggregate citation-correctness is below this (0 = no gate)")
	flag.Float64Var(&o.minInForce, "min-inforce", 0, "fail if aggregate current-law precision is below this (0 = no gate)")
	flag.Float64Var(&o.minAbstain, "min-abstain", 0, "fail if abstention accuracy is below this (0 = no gate)")
	flag.Parse()

	log := blog.New(os.Getenv("BANHMI_LOG_LEVEL"))
	switch err := run(o, log); {
	case err == nil:
		// passed (or no thresholds set)
	case errors.Is(err, errSkip):
		log.Warn("eval skipped (no live stack or no data); not a failure", "reason", err)
	case errors.Is(err, errThreshold):
		log.Error("eval gate failed", "err", err)
		os.Exit(1)
	default:
		log.Error("eval", "err", err)
		os.Exit(1)
	}
}

func run(o opts, log *slog.Logger) error {
	mode, err := retrieve.ParseSearchMode(o.retrievalMode)
	if err != nil {
		return err
	}
	if !o.retrievalOnly {
		return fmt.Errorf("answer mode has been removed; eval is retrieval-only (do not pass -retrieval-only=false)")
	}
	if o.reviewHits <= 0 {
		return fmt.Errorf("-review-hits must be positive")
	}
	if o.reviewPreviewChars <= 0 {
		return fmt.Errorf("-review-preview-chars must be positive")
	}
	if o.abstainMinScore < 0 {
		return fmt.Errorf("-abstain-min-score must be non-negative")
	}
	o.retrievalMode = string(mode)
	if o.rerankEndpoint != "" {
		if strings.TrimSpace(o.rerankModel) == "" {
			return fmt.Errorf("-rerank-model is required with -rerank-endpoint")
		}
		if o.rerankCandidates <= 0 {
			return fmt.Errorf("-rerank-candidates must be positive")
		}
	}

	cfg, err := config.Load(o.cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Load and validate the golden set before standing up any infrastructure, so a
	// bad set fails fast without touching the database.
	cases, err := eval.LoadGolden(o.golden)
	if err != nil {
		return err
	}
	log.Info("loaded golden set", "path", o.golden, "cases", len(cases))

	ctx := context.Background()
	application, err := app.New(ctx, cfg, log, app.WithoutTemporal())
	if err != nil {
		return err
	}
	defer application.Close()

	return application.Container.Invoke(func(r retrieve.Retriever, pool *pgxpool.Pool) error {
		return evaluate(ctx, cfg, o, cases, r, pool, log)
	})
}

type reviewRun struct {
	Case   eval.Case
	Hits   []retrieve.Hit
	Gaps   []retrieve.Gap
	Result eval.CaseResult
}

// evaluate checks the corpus is non-empty, retrieves for every case, scores each
// against the golden expectations, and prints the report + gates on the thresholds.
// The current-law predicate reads the pool.
func evaluate(
	ctx context.Context,
	cfg *config.Config,
	o opts,
	cases []eval.Case,
	r retrieve.Retriever,
	pool *pgxpool.Pool,
	log *slog.Logger,
) error {
	// No ingested chunks → no retrieval is possible; skip cleanly so `make eval`
	// is safe on an empty stack.
	var chunks int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM gold.chunk`).Scan(&chunks); err != nil {
		return fmt.Errorf("count gold.chunk: %w", err)
	}
	if chunks == 0 {
		return fmt.Errorf("%w: gold.chunk is empty (ingest + index documents first)", errSkip)
	}
	log.Info("corpus ready", "chunks", chunks)
	if err := reportGoldenCorpusCoverage(ctx, pool, cases, log); err != nil {
		return err
	}
	if cfg.EmbedEndpoint() != "" {
		if err := reportEmbeddingCoverage(ctx, pool, cfg, chunks, log); err != nil {
			return err
		}
	}

	inForce := inForceChecker(ctx, pool, log)
	var reranker *rerankClient
	if o.rerankEndpoint != "" {
		reranker = newRerankClient(o.rerankEndpoint, o.rerankModel, o.rerankQwenTemplate, o.rerankInstruction)
		log.Info("rerank enabled",
			"model", o.rerankModel,
			"endpoint", reranker.endpoint,
			"candidates", o.rerankCandidates,
			"qwen3_template", o.rerankQwenTemplate)
	}
	finalTopK := effectiveTopK(cfg, o.topK)

	results := make([]eval.CaseResult, 0, len(cases))
	reviewRuns := make([]reviewRun, 0, len(cases))
	for _, c := range cases {
		searchTopK := o.topK
		if reranker != nil {
			searchTopK = o.rerankCandidates
		}
		searchOpts := retrieve.SearchOpts{
			TopK: searchTopK,
			Mode: retrieve.SearchMode(o.retrievalMode),
		}
		if reranker != nil {
			searchOpts.VectorK = o.rerankCandidates
			searchOpts.BM25K = o.rerankCandidates
		}
		ev, err := r.SearchEvidence(ctx, c.Question, searchOpts)
		if err != nil {
			return fmt.Errorf("retrieve case %q: %w", c.ID, err)
		}
		hits := ev.Hits
		if reranker != nil {
			ranked, dur, err := reranker.Rerank(ctx, c.Question, hits)
			if err != nil {
				return fmt.Errorf("rerank case %q: %w", c.ID, err)
			}
			hits = truncateHits(ranked, finalTopK)
			log.Info("reranked case",
				"case", c.ID,
				"candidates", len(ranked),
				"returned", len(hits),
				"duration_ms", dur.Milliseconds())
		}
		abstained := ev.Abstain || retrievalShouldAbstain(hits, o.abstainMinScore)
		result := eval.Score(c, hits, abstained, inForce)
		results = append(results, result)
		if o.review {
			reviewRuns = append(reviewRuns, reviewRun{
				Case:   c,
				Hits:   append([]retrieve.Hit(nil), hits...),
				Gaps:   append([]retrieve.Gap(nil), ev.Gaps...),
				Result: result,
			})
		}
	}

	agg := eval.Summarize(results)
	eval.WriteReport(os.Stdout, results, agg)
	if o.review {
		if err := writeDBReview(os.Stdout, ctx, pool, cfg, o, reviewRuns); err != nil {
			return err
		}
	}

	thresholds := eval.Thresholds{
		MinRecall:   o.minRecall,
		MinMRR:      o.minMRR,
		MinCitation: o.minCitation,
		MinInForce:  o.minInForce,
		MinAbstain:  o.minAbstain,
	}
	if fails := thresholds.Check(agg); len(fails) > 0 {
		for _, f := range fails {
			log.Error("threshold not met", "metric", f.Metric,
				"got", fmt.Sprintf("%.3f", f.Got), "want", fmt.Sprintf("%.3f", f.Want))
		}
		return fmt.Errorf("%w: %d metric(s) below floor", errThreshold, len(fails))
	}
	return nil
}

type dbReviewStats struct {
	SilverDocs                  int64
	IndexedDocs                 int64
	Chunks                      int64
	ConfiguredEmbeddings        int64
	AllEmbeddings               int64
	UnindexedDocs               int64
	NonBindingOnlyUnindexedDocs int64
	BindingNoSectionsDocs       int64
	ValidityNoSectionsDocs      int64
	IndexedDocsWithoutBinding   int64
	NonBindingOnlyIndexedDocs   int64
	BlankCitationChunks         int64
	BlankContextChunks          int64
	TinyTokenChunks             int64
	HugeTokenChunks             int64
	ShortChunks80Chars          int64
	ShortChunks200Chars         int64
	LongCitationChunks          int64
	LongCitationDocs            int64
	MojibakeChunks              int64
	MojibakeDocs                int64
	ExpiredChunks               int64
	NotYetChunks                int64
	OtherNonCurrentChunks       int64
	RelationFetchDocs           int64
	RelationCompleteDocs        int64
	RelationIndexedDocs         int64
	RelationNonBindingOnlyDocs  int64
	RelationNotCompleteDocs     int64
	PromotedRelationEvidence    int64
	WeakRelationEvidence        int64
	ConfirmedRelationEdges      int64
	RelationTargetsResolved     int64
	RelationTargetsStub         int64
	RelationTargetsIndexed      int64
}

const (
	tinyChunkTokenLimit = 20
	hugeChunkTokenLimit = 1024
	shortChunkHardLimit = 80
	shortChunkSoftLimit = 200
	longCitationLimit   = 120
)

func writeDBReview(w io.Writer, ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, o opts, runs []reviewRun) error {
	stats, err := loadDBReviewStats(ctx, pool, config.EmbedModel, config.EmbedDims)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "DB-only RAG review")
	_, _ = fmt.Fprintln(w, "------------------")
	_, _ = fmt.Fprintf(w, "retrieval mode: %s, top-k: %d, abstain-min-score: %.5f\n",
		o.retrievalMode, effectiveTopK(cfg, o.topK), o.abstainMinScore)
	_, _ = fmt.Fprintf(w, "corpus: %d silver docs, %d indexed docs, %d chunks\n",
		stats.SilverDocs, stats.IndexedDocs, stats.Chunks)
	_, _ = fmt.Fprintf(w, "embeddings: %d/%d configured-model chunks embedded (%s/%d); %d total embeddings\n",
		stats.ConfiguredEmbeddings, stats.Chunks, config.EmbedModel, config.EmbedDims, stats.AllEmbeddings)
	_, _ = fmt.Fprintf(w, "unindexed docs: %d total, %d non-binding-only OCR, %d binding-without-sections, %d validity-without-sections\n",
		stats.UnindexedDocs, stats.NonBindingOnlyUnindexedDocs, stats.BindingNoSectionsDocs, stats.ValidityNoSectionsDocs)
	_, _ = fmt.Fprintf(w, "binding safety: %d indexed docs without binding text, %d indexed docs with only non-binding text\n",
		stats.IndexedDocsWithoutBinding, stats.NonBindingOnlyIndexedDocs)
	_, _ = fmt.Fprintf(w, "chunk audit: %d blank citations, %d blank context prefixes, %d tiny-token (<%d tokens), %d huge-token (>%d tokens)\n",
		stats.BlankCitationChunks, stats.BlankContextChunks, stats.TinyTokenChunks, tinyChunkTokenLimit, stats.HugeTokenChunks, hugeChunkTokenLimit)
	_, _ = fmt.Fprintf(w, "chunk shape: %d chunks <%d chars, %d chunks <%d chars, %d overlong citations (>%d chars) across %d docs, %d mojibake-like chunks across %d docs\n",
		stats.ShortChunks80Chars, shortChunkHardLimit,
		stats.ShortChunks200Chars, shortChunkSoftLimit,
		stats.LongCitationChunks, longCitationLimit, stats.LongCitationDocs,
		stats.MojibakeChunks, stats.MojibakeDocs)
	_, _ = fmt.Fprintf(w, "historical chunks kept in gold: %d expired, %d not-yet-effective, %d other non-current; retriever current-law filter must exclude these\n",
		stats.ExpiredChunks, stats.NotYetChunks, stats.OtherNonCurrentChunks)
	_, _ = fmt.Fprintf(w, "relation wave: %d fetch docs, %d complete, %d indexed, %d non-binding-only complete, %d not complete\n",
		stats.RelationFetchDocs, stats.RelationCompleteDocs, stats.RelationIndexedDocs,
		stats.RelationNonBindingOnlyDocs, stats.RelationNotCompleteDocs)
	_, _ = fmt.Fprintf(w, "relation graph: %d confirmed edges, %d promoted evidence rows, %d weak evidence rows, %d resolved target refs, %d stub target refs, %d indexed target refs\n",
		stats.ConfirmedRelationEdges, stats.PromotedRelationEvidence, stats.WeakRelationEvidence,
		stats.RelationTargetsResolved, stats.RelationTargetsStub, stats.RelationTargetsIndexed)

	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Top retrieved evidence")
	for _, run := range runs {
		_, _ = fmt.Fprintf(w, "\n[%s] %s\n", run.Case.ID, reviewExpectation(run.Case))
		for _, gap := range run.Gaps {
			block := "warning"
			if gap.BlocksAnswer {
				block = "blocking"
			}
			_, _ = fmt.Fprintf(w, "  gap: %s %s", block, gap.Kind)
			if gap.DocNumber != "" {
				_, _ = fmt.Fprintf(w, " | %s", gap.DocNumber)
			}
			if gap.Title != "" {
				_, _ = fmt.Fprintf(w, " | %s", gap.Title)
			}
			if gap.Message != "" {
				_, _ = fmt.Fprintf(w, " | %s", gap.Message)
			}
			_, _ = fmt.Fprintln(w)
		}
		if run.Case.ExpectAbstain && len(run.Hits) > 0 {
			_, _ = fmt.Fprintln(w, "  risk: expected-abstain case still returned legal evidence")
		}
		limit := o.reviewHits
		if len(run.Hits) < limit {
			limit = len(run.Hits)
		}
		if limit == 0 {
			_, _ = fmt.Fprintln(w, "  no hits")
			continue
		}
		for i, h := range run.Hits[:limit] {
			match := ""
			if hitMatchesAnyExpected(run.Case, h) {
				match = " expected"
			}
			_, _ = fmt.Fprintf(w, "  %d.%s %s | %s | score %.5f | vector #%s | bm25 #%s\n",
				i+1, match, emptyDash(h.DocNumber), emptyDash(h.Citation), h.Score,
				rankString(h.VectorRank), rankString(h.BM25Rank))
			_, _ = fmt.Fprintf(w, "     %s\n", previewText(h.Content, o.reviewPreviewChars))
			for _, rel := range h.Relations {
				_, _ = fmt.Fprintf(w, "     relation: %s %s -> %s\n",
					rel.Direction, rel.RelationType, reviewRelationDoc(rel))
			}
		}
	}
	return nil
}

func loadDBReviewStats(ctx context.Context, pool *pgxpool.Pool, model string, dims int) (dbReviewStats, error) {
	var s dbReviewStats
	const generalQ = `
WITH docs AS (
  SELECT d.id,
         EXISTS (SELECT 1 FROM gold.chunk c WHERE c.document_id=d.id) AS indexed,
         EXISTS (
           SELECT 1 FROM silver.document_text dt
           WHERE dt.document_id=d.id AND dt.is_binding AND NULLIF(btrim(dt.markdown), '') IS NOT NULL
         ) AS has_binding_text,
         EXISTS (
           SELECT 1 FROM silver.document_text dt
           WHERE dt.document_id=d.id AND NOT dt.is_binding AND NULLIF(btrim(dt.markdown), '') IS NOT NULL
         ) AS has_nonbinding_text,
         EXISTS (SELECT 1 FROM silver.document_section sec WHERE sec.document_id=d.id) AS has_sections,
         EXISTS (
           SELECT 1 FROM silver.validity_period vp
           WHERE vp.document_id=d.id AND vp.section_id IS NULL AND vp.superseded_at IS NULL
         ) AS has_current_validity
  FROM silver.document d
),
current_validity AS (
  SELECT DISTINCT ON (document_id) document_id, status_class
  FROM silver.validity_period
  WHERE superseded_at IS NULL
  ORDER BY document_id, observed_at DESC, id DESC
)
SELECT
  (SELECT count(*) FROM docs) AS silver_docs,
  (SELECT count(*) FROM docs WHERE indexed) AS indexed_docs,
  (SELECT count(*) FROM gold.chunk) AS chunks,
  (SELECT count(*) FROM gold.chunk_embedding WHERE model=$1 AND dims=$2) AS configured_embeddings,
  (SELECT count(*) FROM gold.chunk_embedding) AS all_embeddings,
  (SELECT count(*) FROM docs WHERE NOT indexed) AS unindexed_docs,
  (SELECT count(*) FROM docs WHERE NOT indexed AND has_nonbinding_text AND NOT has_binding_text) AS nonbinding_only_unindexed,
  (SELECT count(*) FROM docs WHERE NOT indexed AND has_binding_text AND NOT has_sections) AS binding_no_sections,
  (SELECT count(*) FROM docs WHERE NOT indexed AND has_current_validity AND NOT has_sections) AS validity_no_sections,
  (SELECT count(*) FROM docs WHERE indexed AND NOT has_binding_text) AS indexed_without_binding,
  (SELECT count(*) FROM docs WHERE indexed AND has_nonbinding_text AND NOT has_binding_text) AS nonbinding_only_indexed,
  (SELECT count(*) FROM gold.chunk WHERE NULLIF(btrim(citation), '') IS NULL) AS blank_citation_chunks,
  (SELECT count(*) FROM gold.chunk WHERE NULLIF(btrim(COALESCE(context_prefix, '')), '') IS NULL) AS blank_context_chunks,
  (SELECT count(*) FROM gold.chunk WHERE token_count IS NOT NULL AND token_count < $3) AS tiny_chunks,
  (SELECT count(*) FROM gold.chunk WHERE token_count IS NOT NULL AND token_count > $4) AS huge_chunks,
  (SELECT count(*) FROM gold.chunk WHERE length(btrim(content)) < $5) AS short_chunks_80,
  (SELECT count(*) FROM gold.chunk WHERE length(btrim(content)) < $6) AS short_chunks_200,
  (SELECT count(*) FROM gold.chunk WHERE length(citation) > $7) AS long_citation_chunks,
  (SELECT count(DISTINCT document_id) FROM gold.chunk WHERE length(citation) > $7) AS long_citation_docs,
  (SELECT count(*) FROM gold.chunk WHERE content ~ 'Ã[¡-ÿ]' OR content LIKE '%�%' OR content LIKE '%â€%' OR citation ~ 'Ã[¡-ÿ]' OR citation LIKE '%�%' OR citation LIKE '%â€%') AS mojibake_chunks,
  (SELECT count(DISTINCT document_id) FROM gold.chunk WHERE content ~ 'Ã[¡-ÿ]' OR content LIKE '%�%' OR content LIKE '%â€%' OR citation ~ 'Ã[¡-ÿ]' OR citation LIKE '%�%' OR citation LIKE '%â€%') AS mojibake_docs,
  (SELECT count(*) FROM gold.chunk c JOIN current_validity cv ON cv.document_id=c.document_id WHERE cv.status_class='expired') AS expired_chunks,
  (SELECT count(*) FROM gold.chunk c JOIN current_validity cv ON cv.document_id=c.document_id WHERE cv.status_class='not_yet') AS not_yet_chunks,
  (SELECT count(*) FROM gold.chunk c JOIN current_validity cv ON cv.document_id=c.document_id WHERE cv.status_class NOT IN ('in_force', 'partial', 'expired', 'not_yet')) AS other_non_current_chunks`
	if err := pool.QueryRow(ctx, generalQ,
		model, dims,
		tinyChunkTokenLimit, hugeChunkTokenLimit,
		shortChunkHardLimit, shortChunkSoftLimit, longCitationLimit,
	).Scan(
		&s.SilverDocs,
		&s.IndexedDocs,
		&s.Chunks,
		&s.ConfiguredEmbeddings,
		&s.AllEmbeddings,
		&s.UnindexedDocs,
		&s.NonBindingOnlyUnindexedDocs,
		&s.BindingNoSectionsDocs,
		&s.ValidityNoSectionsDocs,
		&s.IndexedDocsWithoutBinding,
		&s.NonBindingOnlyIndexedDocs,
		&s.BlankCitationChunks,
		&s.BlankContextChunks,
		&s.TinyTokenChunks,
		&s.HugeTokenChunks,
		&s.ShortChunks80Chars,
		&s.ShortChunks200Chars,
		&s.LongCitationChunks,
		&s.LongCitationDocs,
		&s.MojibakeChunks,
		&s.MojibakeDocs,
		&s.ExpiredChunks,
		&s.NotYetChunks,
		&s.OtherNonCurrentChunks,
	); err != nil {
		return dbReviewStats{}, fmt.Errorf("load DB review stats: %w", err)
	}

	const relationQ = `
WITH rel AS (
  SELECT fd.id AS fetch_doc_id,
         COALESCE(NULLIF(upper(regexp_replace(btrim(sd.doc_number), '[[:space:]]+', ' ', 'g')), ''), sd.source || ':' || sd.external_id) AS doc_key
  FROM ingest.fetch_doc fd
  JOIN bronze.source_document sd ON sd.source=fd.source AND sd.external_id=fd.external_id
  WHERE fd.provenance='relation' AND fd.source='vbpl'
),
docs AS (
  SELECT rel.fetch_doc_id, d.id AS document_id
  FROM rel JOIN silver.document d ON d.doc_key=rel.doc_key
),
indexed AS (
  SELECT DISTINCT docs.fetch_doc_id
  FROM docs JOIN gold.chunk c ON c.document_id=docs.document_id
),
text_state AS (
  SELECT docs.fetch_doc_id,
         bool_or(dt.is_binding AND NULLIF(btrim(dt.markdown), '') IS NOT NULL) AS has_binding,
         bool_or((NOT dt.is_binding) AND NULLIF(btrim(dt.markdown), '') IS NOT NULL) AS has_nonbinding
  FROM docs
  LEFT JOIN silver.document_text dt ON dt.document_id=docs.document_id
  GROUP BY docs.fetch_doc_id
),
relation_targets AS (
  SELECT DISTINCT dr.to_ref_id, ref.document_id
  FROM silver.document_relation dr
  JOIN silver.doc_ref ref ON ref.id=dr.to_ref_id
)
SELECT
  count(*) AS relation_fetch_docs,
  count(*) FILTER (WHERE fd.state='complete') AS complete_relation_docs,
  count(indexed.fetch_doc_id) FILTER (WHERE fd.state='complete') AS complete_indexed,
  count(*) FILTER (WHERE fd.state='complete' AND indexed.fetch_doc_id IS NULL AND text_state.has_nonbinding AND NOT text_state.has_binding) AS complete_nonbinding_only_unindexed,
  count(*) FILTER (WHERE fd.state <> 'complete') AS not_complete,
  (SELECT count(*) FROM silver.relation_evidence WHERE promoted) AS promoted_relation_evidence,
  (SELECT count(*) FROM silver.relation_evidence WHERE evidence_kind='weak_relation') AS weak_relation_evidence,
  (SELECT count(*) FROM silver.document_relation) AS confirmed_relation_edges,
  (SELECT count(*) FROM relation_targets WHERE document_id IS NOT NULL) AS relation_targets_resolved,
  (SELECT count(*) FROM relation_targets WHERE document_id IS NULL) AS relation_targets_stub,
  (SELECT count(*) FROM relation_targets rt WHERE EXISTS (SELECT 1 FROM gold.chunk c WHERE c.document_id=rt.document_id)) AS relation_targets_indexed
FROM ingest.fetch_doc fd
JOIN rel ON rel.fetch_doc_id=fd.id
LEFT JOIN indexed ON indexed.fetch_doc_id=fd.id
LEFT JOIN text_state ON text_state.fetch_doc_id=fd.id`
	if err := pool.QueryRow(ctx, relationQ).Scan(
		&s.RelationFetchDocs,
		&s.RelationCompleteDocs,
		&s.RelationIndexedDocs,
		&s.RelationNonBindingOnlyDocs,
		&s.RelationNotCompleteDocs,
		&s.PromotedRelationEvidence,
		&s.WeakRelationEvidence,
		&s.ConfirmedRelationEdges,
		&s.RelationTargetsResolved,
		&s.RelationTargetsStub,
		&s.RelationTargetsIndexed,
	); err != nil {
		return dbReviewStats{}, fmt.Errorf("load relation review stats: %w", err)
	}
	return s, nil
}

func reviewExpectation(c eval.Case) string {
	if c.ExpectAbstain {
		return "expected abstain"
	}
	parts := make([]string, 0, len(c.ExpectedCitations))
	for _, ec := range c.ExpectedCitations {
		part := strings.TrimSpace(ec.SoKyHieu)
		if ec.Dieu != "" {
			part += " Điều " + ec.Dieu
		}
		if ec.Khoan != "" {
			part += " Khoản " + ec.Khoan
		}
		if ec.Diem != "" {
			part += " điểm " + ec.Diem
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "no expected citation"
	}
	return "expected " + strings.Join(parts, "; ")
}

func hitMatchesAnyExpected(c eval.Case, h retrieve.Hit) bool {
	for _, ec := range c.ExpectedCitations {
		if !strings.EqualFold(strings.TrimSpace(ec.SoKyHieu), strings.TrimSpace(h.DocNumber)) {
			continue
		}
		if ec.Dieu != "" && !citationHasReviewNumber(h.Citation, "điều", ec.Dieu) {
			continue
		}
		if ec.Khoan != "" && !citationHasReviewNumber(h.Citation, "khoản", ec.Khoan) {
			continue
		}
		if ec.Diem != "" && !citationHasReviewNumber(h.Citation, "điểm", ec.Diem) {
			continue
		}
		return true
	}
	return false
}

func reviewRelationDoc(rel retrieve.Relation) string {
	parts := []string{}
	if rel.DocNumber != "" {
		parts = append(parts, rel.DocNumber)
	}
	if rel.Title != "" {
		parts = append(parts, rel.Title)
	}
	if len(parts) == 0 {
		return "unresolved target"
	}
	return strings.Join(parts, " | ")
}

func citationHasReviewNumber(citation, keyword, want string) bool {
	fields := strings.FieldsFunc(citation, func(r rune) bool {
		return r == ',' || r == ' '
	})
	for i := 0; i < len(fields)-1; i++ {
		if strings.EqualFold(fields[i], keyword) && strings.EqualFold(fields[i+1], want) {
			return true
		}
	}
	return false
}

func previewText(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	if maxRunes <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= maxRunes {
		return s
	}
	if maxRunes <= 1 {
		return string(rs[:maxRunes])
	}
	return string(rs[:maxRunes-1]) + "…"
}

func emptyDash(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "-"
	}
	return s
}

func rankString(rank int) string {
	if rank <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", rank)
}

func retrievalShouldAbstain(hits []retrieve.Hit, minScore float64) bool {
	if len(hits) == 0 {
		return true
	}
	if minScore <= 0 {
		return false
	}
	return hits[0].Score < minScore
}

// reportGoldenCorpusCoverage distinguishes retrieval failures from corpus gaps:
// if a golden case expects a document that is absent from silver or unchunked in
// gold, retrieval cannot possibly satisfy that case. The eval still runs and
// counts recall normally, but the warning points the fix at Discover/Fetch/Index.
func reportGoldenCorpusCoverage(ctx context.Context, pool *pgxpool.Pool, cases []eval.Case, log *slog.Logger) error {
	expected := make(map[string][]string)
	display := make(map[string]string)
	for _, c := range cases {
		for _, ec := range c.ExpectedCitations {
			number := strings.TrimSpace(ec.SoKyHieu)
			if number == "" {
				continue
			}
			key := strings.ToLower(number)
			display[key] = number
			if !contains(expected[key], c.ID) {
				expected[key] = append(expected[key], c.ID)
			}
		}
	}
	if len(expected) == 0 {
		return nil
	}

	keys := make([]string, 0, len(expected))
	for key := range expected {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	const q = `
SELECT d.id, COALESCE(d.doc_number, ''), count(c.id)
FROM silver.document d
LEFT JOIN gold.chunk c ON c.document_id = d.id
WHERE lower(COALESCE(d.doc_number, '')) = $1
GROUP BY d.id, d.doc_number
ORDER BY d.id
LIMIT 1`
	var present, indexed int
	for _, key := range keys {
		var docID int64
		var docNumber string
		var chunks int64
		err := pool.QueryRow(ctx, q, key).Scan(&docID, &docNumber, &chunks)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			log.Warn("golden expected document missing from corpus",
				"so_ky_hieu", display[key], "cases", expected[key])
			continue
		case err != nil:
			return fmt.Errorf("check golden corpus coverage for %q: %w", display[key], err)
		}
		present++
		if chunks == 0 {
			log.Warn("golden expected document has no indexed chunks",
				"so_ky_hieu", docNumber, "document_id", docID, "cases", expected[key])
			continue
		}
		indexed++
	}
	log.Info("golden corpus coverage",
		"expected_docs", len(keys), "present", present, "indexed", indexed)
	return nil
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// reportEmbeddingCoverage surfaces whether a hybrid/vector eval is fair. If most
// corpus chunks do not have the configured model embedding yet, the vector arm can
// only rank a biased subset even though BM25 still sees the whole corpus.
func reportEmbeddingCoverage(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, chunks int64, log *slog.Logger) error {
	const q = `
SELECT count(*)
FROM gold.chunk_embedding
WHERE model = $1 AND dims = $2`
	var embedded int64
	dims := config.EmbedDims
	model := config.EmbedModel
	if err := pool.QueryRow(ctx, q, model, dims).Scan(&embedded); err != nil {
		return fmt.Errorf("count chunk embeddings: %w", err)
	}
	coverage := float64(embedded) / float64(chunks)
	attrs := []any{
		"model", model,
		"dims", dims,
		"embedded", embedded,
		"chunks", chunks,
		"coverage", fmt.Sprintf("%.1f%%", coverage*100),
	}
	if embedded < chunks {
		log.Warn("embedding coverage incomplete; hybrid/vector eval is biased toward embedded chunks", attrs...)
		return nil
	}
	log.Info("embedding coverage complete", attrs...)
	return nil
}

// inForceChecker returns a predicate reporting whether a retrieved hit's document
// is current legal material, using the same rule as the retriever's pre-filter:
// the newest non-superseded validity row is in_force or partial. It memoizes per
// document id so repeated hits for one document hit the DB once. A query error is
// logged and treated as not-current (conservative: surfaces, never hides, a leak).
func inForceChecker(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) eval.InForceFn {
	cache := make(map[int64]bool)
	const q = `
SELECT cur.status_class IN ('in_force', 'partial')
FROM (
    SELECT DISTINCT ON (document_id) status_class
    FROM silver.validity_period
    WHERE document_id = $1 AND superseded_at IS NULL
    ORDER BY document_id, observed_at DESC, id DESC
) cur`
	return func(h retrieve.Hit) bool {
		if v, ok := cache[h.DocumentID]; ok {
			return v
		}
		var ok bool
		err := pool.QueryRow(ctx, q, h.DocumentID).Scan(&ok)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// No validity row at all → cannot assert current-law status → treat as a leak.
			ok = false
		case err != nil:
			log.Warn("current-law check failed; counting hit as not-current", "document_id", h.DocumentID, "err", err)
			ok = false
		}
		cache[h.DocumentID] = ok
		return ok
	}
}
