// Package retrieve is banhmi's retrieval core (GOLD). The default path is BGE-M3
// vector search when embeddings are enabled, and BM25 when embeddings are
// disabled. Explicit eval modes can still run vector, BM25, or hybrid RRF.
//
// Current law leads but is not the only thing returned: because Vietnamese legal
// material overlaps heavily (a base doc plus many partial amendments/replacements),
// the default surfaces current law first and then appends a small, badged pass of
// non-current (expired/not-yet) matches as evidence rather than excluding them —
// the connecting agent judges currency. SearchOpts.InForceOnly=true restores a
// strict current-only hard filter; =false disables both (pure relevance).
//
// The arms use pgvector distance operators (`<=>` dense, `<#>` sparsevec BM25) and
// validity/document filters assembled into per-query CTEs that sqlc cannot model,
// so the retriever runs raw, parameterized pgx against the pool rather than going
// through the generated store. Each arm's SQL is built once and only its parameters
// vary.
package retrieve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvector "github.com/pgvector/pgvector-go"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/rag/embed"
	"danny.vn/banhmi/pkg/rag/lexical"
)

// Defaults applied when a SearchOpts field (or the corresponding config value) is
// zero. They keep a bare Search(ctx, q, SearchOpts{}) call sensible.
const (
	defaultTopK    = 8
	defaultVectorK = 50
	defaultBM25K   = 50
	defaultRRFK    = 60

	relationLimitPerDocument   = 8
	relatedHitLimitPerRelation = 2

	// Roll-up levels collapse sibling chunks to their parent provision so one
	// Khoản's Điểm/Đoạn do not crowd the top-k. Default is Khoản.
	rollupNone  = "none"
	rollupKhoan = "khoan"
	rollupDieu  = "dieu"
	// rollupOverfetch is how many extra candidates (× topK) to hydrate before
	// roll-up, so distinct parent provisions can fill the result set.
	rollupOverfetch = 8
)

// SearchMode selects which first-stage retrieval arms run. Empty opts use the
// configured production default: vector when BGE-M3 is enabled, BM25 otherwise.
// Explicit modes exist so eval can compare BM25, vector, and fused retrieval
// against the same golden set.
type SearchMode string

const (
	ModeHybrid SearchMode = "hybrid"
	ModeBM25   SearchMode = "bm25"
	ModeVector SearchMode = "vector"
)

var errUnknownSearchMode = errors.New("unknown retrieval mode")

// ParseSearchMode normalizes a user/config value into a SearchMode.
func ParseSearchMode(s string) (SearchMode, error) {
	switch SearchMode(strings.ToLower(strings.TrimSpace(s))) {
	case "", ModeHybrid:
		return ModeHybrid, nil
	case ModeBM25:
		return ModeBM25, nil
	case ModeVector:
		return ModeVector, nil
	default:
		return "", fmt.Errorf("%w %q", errUnknownSearchMode, s)
	}
}

// Hit is one fused retrieval result with the metadata an answer needs to cite the
// source. ChunkID is the gold.chunk id the LLM must cite by; DocNumber (số ký
// hiệu) and Title come from silver.document; Citation is the human-facing
// "Điều 7, Khoản 2"; ContextPrefix is the deterministic contextual-retrieval
// header. Score is the fused RRF score (higher is better). VectorRank / BM25Rank
// are the 1-based ranks the chunk held in each arm (0 = absent from that arm),
// kept for diagnostics and downstream reranking.
type Hit struct {
	ChunkID        int64
	DocumentID     int64
	DocNumber      string // số ký hiệu, e.g. "11/2026/TT-NHNN"
	ParentCitation string // parent provision for roll-up/navigation, e.g. "Điều 7, Khoản 2"
	Title          string // trích yếu
	IssuedDate     string // ngày ban hành (YYYY-MM-DD), document-level
	Source         string // official source key: vbpl | congbao | sbv_hanoi
	SourceURL      string // official source landing page (detail_url); never a file link
	Citation       string // "Điều 7, Khoản 2"
	ContextPrefix  string
	Content        string // matched chunk body (a Khoản/Điểm/Đoạn for a long, split Điều)
	// Article is the full enclosing Điều, reassembled from all of its chunks, so a hit
	// on one Khoản/Điểm/Đoạn still carries the whole article for context. Empty when the
	// hit's citation has no resolvable Điều. ArticleTruncated is set when the text was
	// capped (the agent opens the document tool for the remainder).
	ArticleCitation  string // e.g. "Điều 7"
	Article          string
	ArticleTruncated bool
	Score            float64
	// Similarity is the vector arm's cosine similarity (1 − distance, in [0,1]);
	// 0 when the hit came from BM25 only. Unlike the rank-derived RRF Score it is
	// an absolute relevance signal, so the abstain score floor gates on it.
	Similarity float64
	// BM25Score is the lexical arm's raw BM25 score (sparse inner product); 0 when
	// the hit came from the vector arm only. Returned alongside Similarity so a
	// caller/reranker sees both signals, not just the fused rank.
	BM25Score  float64
	VectorRank int
	BM25Rank   int
	Validity   ValidityEvidence
	Text       TextEvidence
	Relations  []Relation
}

// ValidityEvidence is the current validity row attached to a document/chunk.
// SectionID is non-zero only when the row is clause/provision scoped.
type ValidityEvidence struct {
	SectionID     int64
	StatusCode    string
	StatusClass   string
	EffectiveFrom string
	EffectiveTo   string
	Source        string
	Reason        string
}

// TextEvidence summarizes the document_text rows behind retrieved text. It tells
// an agent whether the evidence is binding and whether extraction needs review.
type TextEvidence struct {
	HasBindingText    bool
	HasNonBindingText bool
	NeedsReview       bool
	Authorities       []string
	Sources           []string
	ExtractEngines    []string
	MaxConfidence     float64
}

// RelatedHit is a matching chunk from a document reached through a confirmed
// relation attached to a primary hit. It is deliberately separate from Hits so
// graph adjacency never mutates first-stage retrieval rank or precision metrics.
type RelatedHit struct {
	BaseChunkID    int64
	BaseDocumentID int64
	BaseDocNumber  string
	RelationID     int64
	Direction      string
	RelationType   string
	Source         string
	ToCitation     string

	ChunkID       int64
	DocumentID    int64
	DocNumber     string
	Title         string
	SourceURL     string // official source landing page (detail_url) for the related document
	Citation      string
	ContextPrefix string
	Content       string
	Validity      ValidityEvidence
	Text          TextEvidence
	// Rank is the chunk's 1-based position within its base relation, in cosine
	// order (the related pass is vector-ranked).
	Rank int
}

// Relation is confirmed relation evidence adjacent to a retrieved document. It
// is database evidence, not generated prose: callers can expose it directly to a
// user-supplied model so amendment/repeal/status questions do not rely on chunk
// search alone.
type Relation struct {
	RelationID           int64
	Direction            string // outgoing: hit doc acts on target; incoming: another doc acts on hit doc
	RelationType         string
	Source               string
	ToCitation           string
	DocumentID           int64
	DocNumber            string
	Title                string
	Resolved             bool
	TargetIndexed        bool
	TargetHasBindingText bool
	TargetNeedsReview    bool
	TargetValidity       ValidityEvidence
	TargetText           TextEvidence
	Evidence             RelationEvidence
	RelationTypeRaw      *int32
}

// RelationEvidence is the best stored evidence row behind a confirmed relation.
// It is empty when the relation came from a legacy path without a matching
// silver.relation_evidence row.
type RelationEvidence struct {
	EvidenceID      int64
	EvidenceKind    string
	Operator        string
	TargetText      string
	TargetCitation  string
	Citation        string
	Snippet         string
	SourceAuthority string
	Confidence      float64
	Promoted        bool
}

// SearchOpts tunes one Search call. Zero fields fall back to the Retriever's
// configured defaults, so callers can pass SearchOpts{} for the common path.
// InForceOnly keeps the historical config/API name, but the active filter means
// current legal material: fully in force plus partially in force. To disable the
// pre-filter, set a non-nil *bool pointing at false.
type SearchOpts struct {
	Mode    SearchMode // bm25, vector, hybrid; empty = vector when enabled, else BM25
	TopK    int        // fused hits returned; 0 = config TopK
	VectorK int        // vector candidates before fusion; 0 = config VectorK
	BM25K   int        // BM25 candidates before fusion; 0 = config BM25K
	RRFK    int        // RRF constant; 0 = config RRFK
	// RelatedK returns up to this many relation-expanded chunks beside the primary
	// hits. 0 disables expansion. It is intended for MCP evidence packs, not
	// first-stage ranking.
	RelatedK int

	// InForceOnly controls current-law handling. nil (default) = current law leads
	// and a small badged pass of non-current law is surfaced after it (evidence-only).
	// true = strict current-only hard filter (no non-current pass). false = no filter,
	// pure relevance (historical/admin).
	InForceOnly *bool

	// RollupLevel collapses sibling hits to their parent provision so one Khoản's
	// Điểm/Đoạn do not crowd the top-k: "khoan" (default), "dieu", or "none".
	// Empty = the configured default.
	RollupLevel string

	// Optional document pre-filters (empty/nil = no filter). They narrow which
	// documents are eligible BEFORE vector ranking — they never change embeddings, and
	// when all are empty the query is byte-for-byte the default path. AsOf/IssuedFrom/
	// IssuedTo are YYYY-MM-DD. AsOf selects point-in-time: law whose effective window
	// contains that date (instead of current-as-of-now). Issuer/DocType match the
	// document's issuer / doc_type (case-insensitive, exact).
	AsOf       string
	IssuedFrom string
	IssuedTo   string
	Issuer     []string
	DocType    []string
}

// Retriever runs the selected retrieval path with the current-law pre-filter.
// Build it with New; call Search per query.
type Retriever interface {
	Search(ctx context.Context, query string, opts SearchOpts) ([]Hit, error)
	SearchEvidence(ctx context.Context, query string, opts SearchOpts) (Evidence, error)
}

// hybridRetriever is the concrete Retriever over a pgxpool and an optional embedder.
type hybridRetriever struct {
	pool         *pgxpool.Pool
	embedder     embed.Embedder // nil → vector arm skipped, BM25-only
	cfg          config.RetrieveConfig
	gate         gateState
	jurisdiction string // "vn" (default) | "my" | … — tunes the lexical query router
	log          *slog.Logger
}

// New builds a Retriever. embedder may be nil: with no embedder, empty-mode Search
// runs BM25-only. log may be nil (a no-op logger is used). The cfg supplies the
// default TopK/VectorK/BM25K/RRFK and the InForceOnly default.
func New(pool *pgxpool.Pool, embedder embed.Embedder, cfg config.RetrieveConfig, log *slog.Logger, opts ...Option) Retriever {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	r := &hybridRetriever{
		pool:         pool,
		embedder:     embedder,
		cfg:          cfg,
		jurisdiction: "vn",
		log:          log,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// resolved holds the per-call knobs after merging SearchOpts with config defaults.
// inForceOnly filters the PRIMARY pass to current law (in_force + partial).
// surfaceNonCurrent additionally runs a second pass for non-current law and
// appends it, badged, after the current results — so repealed/overlapping law
// stays findable rather than excluded, while current law leads.
type resolved struct {
	mode              SearchMode
	topK              int
	vectorK           int
	bm25K             int
	rrfK              int
	inForceOnly       bool
	surfaceNonCurrent bool
	rollupLevel       string
	asOf              string
	issuedFrom        string
	issuedTo          string
	issuer            []string
	docType           []string
}

// hasDocFilter reports whether any optional document pre-filter is set.
func (r resolved) hasDocFilter() bool {
	return r.asOf != "" || r.issuedFrom != "" || r.issuedTo != "" || len(r.issuer) > 0 || len(r.docType) > 0
}

// lowerNonEmpty lowercases/trims each value and drops empties; returns nil for an
// all-empty input so the filter reads as unset.
func lowerNonEmpty(vals []string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// resolve merges opts over the configured defaults, applying package defaults for
// any value still zero so a Search never runs with a zero limit.
func (r *hybridRetriever) resolve(opts SearchOpts) (resolved, error) {
	pick := func(o, c, d int) int {
		switch {
		case o > 0:
			return o
		case c > 0:
			return c
		default:
			return d
		}
	}
	// Default (opts.InForceOnly nil): current law leads the primary pass AND
	// non-current law is surfaced (badged) after it — the evidence-only contract,
	// since "current law" in an overlapping corpus is the agent's call, not a hard
	// exclusion. Explicit true: strict current-only (no non-current pass). Explicit
	// false: no filter, pure relevance (historical/admin). cfg.InForceOnly no longer
	// drives the default.
	inForce := true
	surfaceNonCurrent := true
	if r.gate.disableValidityFilter {
		// No usable validity data: the in_force pre-filter and the non-current
		// pass both key off validity_period, so applying them would hide the whole
		// corpus. Fall back to pure relevance until validity is derived.
		inForce = false
		surfaceNonCurrent = false
	}
	if opts.InForceOnly != nil {
		inForce = *opts.InForceOnly
		surfaceNonCurrent = false
	}
	mode := opts.Mode
	if mode == "" {
		// Production default: hybrid (dense vector + BM25 sparse, query-routed) when
		// an embedder is present — eval beats vector-only (recall@k 85.7%→89.3%,
		// mrr 78.6%→84.6%, current-law 100%, no regression). Falls back to the
		// lexical arm alone when no embedder is configured.
		if r.embedder != nil {
			mode = ModeHybrid
		} else {
			mode = ModeBM25
		}
	} else {
		var err error
		mode, err = ParseSearchMode(string(mode))
		if err != nil {
			return resolved{}, err
		}
	}
	rollup := strings.ToLower(strings.TrimSpace(opts.RollupLevel))
	if rollup == "" {
		rollup = strings.ToLower(strings.TrimSpace(r.cfg.RollupLevel))
	}
	switch rollup {
	case rollupNone, rollupKhoan, rollupDieu:
	default:
		rollup = rollupKhoan // sensible default: collapse Điểm/Đoạn into their Khoản
	}
	asOf := strings.TrimSpace(opts.AsOf)
	issuedFrom := strings.TrimSpace(opts.IssuedFrom)
	issuedTo := strings.TrimSpace(opts.IssuedTo)
	issuer := lowerNonEmpty(opts.Issuer)
	docType := lowerNonEmpty(opts.DocType)
	// A scoped query (point-in-time or metadata filter) returns only its filtered
	// primary pass — skip the non-current "badged extra" pass so it can't surface
	// documents outside the requested scope.
	if asOf != "" || issuedFrom != "" || issuedTo != "" || len(issuer) > 0 || len(docType) > 0 {
		surfaceNonCurrent = false
	}
	return resolved{
		mode:              mode,
		topK:              pick(opts.TopK, r.cfg.TopK, defaultTopK),
		vectorK:           pick(opts.VectorK, r.cfg.VectorK, defaultVectorK),
		bm25K:             pick(opts.BM25K, r.cfg.BM25K, defaultBM25K),
		rrfK:              pick(opts.RRFK, r.cfg.RRFK, defaultRRFK),
		inForceOnly:       inForce,
		surfaceNonCurrent: surfaceNonCurrent,
		rollupLevel:       rollup,
		asOf:              asOf,
		issuedFrom:        issuedFrom,
		issuedTo:          issuedTo,
		issuer:            issuer,
		docType:           docType,
	}, nil
}

// buildDocFilterCTE builds the `WITH in_force AS (...)` document pre-filter for the
// primary pass. With no filters and inForceOnly it returns the canonical inForceCTE
// verbatim (so the default hot path is byte-for-byte unchanged); with neither filters
// nor inForceOnly it returns "" (no CTE). With filters it narrows the eligible
// documents by point-in-time validity (AsOf — effective window contains the date)
// and/or metadata (issue date, issuer, doc type). startParam is the first positional
// placeholder the CTE may use; returned args align to it in order.
func buildDocFilterCTE(res resolved, startParam int) (string, []any) {
	if !res.hasDocFilter() {
		if res.inForceOnly {
			return inForceCTE, nil
		}
		return "", nil
	}
	var conds []string
	var args []any
	p := startParam
	switch {
	case res.asOf != "":
		conds = append(conds, fmt.Sprintf("cur.eff_from IS NOT NULL AND cur.eff_from <= $%d::date AND (cur.eff_to IS NULL OR cur.eff_to > $%d::date)", p, p))
		args = append(args, res.asOf)
		p++
	case res.inForceOnly:
		conds = append(conds, "cur.status_class IN ('in_force', 'partial')")
	}
	if res.issuedFrom != "" {
		conds = append(conds, fmt.Sprintf("d.issued_at::date >= $%d::date", p))
		args = append(args, res.issuedFrom)
		p++
	}
	if res.issuedTo != "" {
		conds = append(conds, fmt.Sprintf("d.issued_at::date <= $%d::date", p))
		args = append(args, res.issuedTo)
		p++
	}
	if len(res.issuer) > 0 {
		conds = append(conds, fmt.Sprintf("lower(COALESCE(d.issuer, '')) = ANY($%d)", p))
		args = append(args, res.issuer)
		p++
	}
	if len(res.docType) > 0 {
		conds = append(conds, fmt.Sprintf("lower(COALESCE(d.doc_type, '')) = ANY($%d)", p))
		args = append(args, res.docType)
		p++
	}
	_ = p // next free placeholder; kept incremented so a new filter clause appended below stays correct
	cte := fmt.Sprintf(`
WITH in_force AS (
    SELECT cur.document_id
    FROM (
        SELECT DISTINCT ON (document_id) document_id, status_class, eff_from, eff_to
        FROM silver.validity_period
        WHERE superseded_at IS NULL
          AND section_id IS NULL
        ORDER BY document_id, observed_at DESC, id DESC
    ) cur
    JOIN silver.document d ON d.id = cur.document_id
    WHERE %s
)`, strings.Join(conds, "\n      AND "))
	return cte, args
}

// Search returns ranked hits only. Use SearchEvidence for gaps and scope signals.
func (r *hybridRetriever) Search(ctx context.Context, query string, opts SearchOpts) ([]Hit, error) {
	ev, err := r.SearchEvidence(ctx, query, opts)
	if err != nil {
		return nil, err
	}
	return ev.Hits, nil
}

// searchHits embeds the query when the vector arm is active, runs the selected
// arm(s) under the current-law pre-filter, hydrates the top hits with citation
// metadata, and returns up to topK results sorted by score.
func (r *hybridRetriever) searchHits(ctx context.Context, query string, opts SearchOpts) ([]Hit, error) {
	if query == "" {
		return nil, nil
	}
	res, err := r.resolve(opts)
	if err != nil {
		return nil, fmt.Errorf("retrieve: resolve opts: %w", err)
	}

	// Vector arm — skipped when no embedder is configured.
	var vectorList []ranked
	if res.mode != ModeBM25 {
		if r.embedder == nil {
			if res.mode == ModeVector {
				return nil, fmt.Errorf("retrieve: vector mode requested but no embedder is configured")
			}
			r.log.Debug("retrieve: no embedder, running BM25-only")
		} else {
			vecs, err := r.embedder.Embed(ctx, []string{query})
			if err != nil {
				return nil, fmt.Errorf("retrieve: embed query: %w", err)
			}
			if len(vecs) != 1 || vecs[0] == nil {
				return nil, fmt.Errorf("retrieve: embedder returned %d vectors for one query", len(vecs))
			}
			vectorList, err = r.vectorArm(ctx, pgvector.NewVector(vecs[0]), res, false)
			if err != nil {
				return nil, fmt.Errorf("retrieve: vector arm: %w", err)
			}
		}
	}

	// Lexical arm — native pgvector BM25 sparse vectors (RDS-portable; pg_search is
	// unavailable on managed RDS).
	var bm25List []ranked
	if res.mode != ModeVector {
		bm25List, err = r.sparseArm(ctx, query, res)
		if err != nil {
			return nil, fmt.Errorf("retrieve: lexical arm: %w", err)
		}
	}

	fused := fuseRRF(vectorList, bm25List, res.rrfK, r.lexicalWeightFor(query))
	if len(fused) == 0 {
		return nil, nil
	}
	// Over-fetch before parent roll-up so distinct provisions can fill the top-k —
	// sibling Điểm/Đoạn of one Khoản must not crowd out other provisions.
	pool := res.topK
	if res.rollupLevel != rollupNone {
		if over := res.topK * rollupOverfetch; over > pool {
			pool = over
		}
	}
	if len(fused) > pool {
		fused = fused[:pool]
	}

	hits, err := r.hydrate(ctx, fused)
	if err != nil {
		return nil, fmt.Errorf("retrieve: hydrate hits: %w", err)
	}
	hits = rollupByParent(hits, res.rollupLevel)
	if len(hits) > res.topK {
		hits = hits[:res.topK]
	}

	// Surface a small, separate pass of non-current law (badged) AFTER the current
	// results, so repealed/overlapping law stays findable without crowding current
	// law out of the primary ranking. Vector-only (production path); skipped when
	// strict or no embedder.
	if res.surfaceNonCurrent && r.embedder != nil && len(query) > 0 {
		nc, err := r.nonCurrentHits(ctx, query, res)
		if err != nil {
			return nil, fmt.Errorf("retrieve: non-current pass: %w", err)
		}
		hits = appendNonCurrent(hits, nc)
	}
	hits = dedupeRelationsPerDocument(hits)
	r.log.Debug("retrieve: search complete",
		"query_len", len(query),
		"mode", res.mode,
		"vector_hits", len(vectorList),
		"bm25_hits", len(bm25List),
		"fused", len(fused),
		"returned", len(hits),
		"in_force_only", res.inForceOnly,
	)
	return hits, nil
}

// inForceCTE is the hard current-law pre-filter, shared by both arms. It selects
// the document_ids whose current (system-time non-superseded, newest by
// observed_at) validity row is fully or partially in force. DISTINCT ON picks the
// newest row per document; the outer filter excludes fully
// expired/not-yet/suspended/inappropriate law. Clause-level validity is deferred,
// so partial documents are included as current but answers must still cite exact
// provisions and surface amendment context when available.
//
// The current-law class set ('in_force','partial') mirrors config.validity_status
// (is_current_law) and is kept as an inline literal here on purpose: this is the
// hottest, most load-bearing query, and a config subquery changes its plan enough
// to perturb tie-broken ordering at the rollup/top-k boundary. Driving it from
// config needs a plan-stable build (load the classes in Go, validate against the
// status_class enum, format the IN-list) — see PLAN.md Phase 5.
const inForceCTE = `
WITH in_force AS (
    SELECT document_id
    FROM (
        SELECT DISTINCT ON (document_id) document_id, status_class
        FROM silver.validity_period
        WHERE superseded_at IS NULL
          AND section_id IS NULL
        ORDER BY document_id, observed_at DESC, id DESC
    ) cur
    WHERE cur.status_class IN ('in_force', 'partial')
)`

// outOfForceCTE is the inverse of inForceCTE: documents whose current validity row
// is NOT current law (expired/not-yet/suspended/…). It powers the second,
// non-current retrieval pass that surfaces repealed/overlapping law as badged
// evidence after the current results. Mirrors config.validity_status.
const outOfForceCTE = `
WITH in_force AS (
    SELECT document_id
    FROM (
        SELECT DISTINCT ON (document_id) document_id, status_class
        FROM silver.validity_period
        WHERE superseded_at IS NULL
          AND section_id IS NULL
        ORDER BY document_id, observed_at DESC, id DESC
    ) cur
    WHERE cur.status_class NOT IN ('in_force', 'partial')
)`

// vectorArm runs pgvector cosine ANN over gold.chunk_embedding joined to
// gold.chunk, optionally pre-filtered to current-law documents, returning the top
// vectorK chunk ids in cosine order (closest first → rank 1). The embedding model
// is pinned to the configured embedder so a chunk with multiple model vectors is
// matched on the right one.
func (r *hybridRetriever) vectorArm(ctx context.Context, qv pgvector.Vector, res resolved, notInForce bool) ([]ranked, error) {
	model := r.embedder.Model()
	args := []any{qv, model, res.vectorK}

	const inForceBody = `
SELECT c.id, (e.embedding <=> $1)::float8
FROM gold.chunk_embedding e
JOIN gold.chunk c ON c.id = e.chunk_id
WHERE e.model = $2
  AND c.document_id IN (SELECT document_id FROM in_force)
ORDER BY e.embedding <=> $1, c.id
LIMIT $3`

	var sql string
	switch {
	case notInForce:
		// Second pass: non-current law only (surfaced, badged, after current). Filters
		// never reach here — a scoped query skips the non-current pass entirely.
		sql = outOfForceCTE + inForceBody
	default:
		cte, fargs := buildDocFilterCTE(res, len(args)+1)
		if cte == "" {
			sql = `
SELECT c.id, (e.embedding <=> $1)::float8
FROM gold.chunk_embedding e
JOIN gold.chunk c ON c.id = e.chunk_id
WHERE e.model = $2
ORDER BY e.embedding <=> $1, c.id
LIMIT $3`
		} else {
			args = append(args, fargs...)
			sql = cte + inForceBody
		}
	}

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	return scanRankedWithDistance(rows)
}

// lexicalWeightFor routes the per-query lexical fusion weight. Queries the dense
// vector handles poorly — diacritic-less text (no dấu) or an explicit số ký hiệu —
// get LexicalBoostWeight so BM25 leads; every other (semantic) query stays
// vector-primary at LexicalWeight. This wins the lexical edge cases without
// letting BM25 displace semantic hits on normal queries — a single global weight
// cannot do both (see PLAN.md). Boost ≤ 0 disables routing.
func (r *hybridRetriever) lexicalWeightFor(query string) float64 {
	if r.cfg.LexicalBoostWeight <= 0 {
		return r.cfg.LexicalWeight
	}
	// The no-diacritics boost is Vietnamese-specific: English (e.g. Malaysia) is
	// always diacritic-free, so applying it there would boost every query. Citation
	// routing (extractDocumentRefs) is also VN số-ký-hiệu-shaped today.
	if r.jurisdiction == "vn" && (lexical.DiacriticFree(query) || len(extractDocumentRefs(query)) > 0) {
		return r.cfg.LexicalBoostWeight
	}
	return r.cfg.LexicalWeight
}

// sparseArm runs the native pgvector BM25 lexical arm: the query is encoded to a
// BM25 sparse vector (term presence over the same hashing/unaccent scheme the
// stored gold.chunk.content_sparse document vectors use), and chunks are ranked by
// sparse inner product (pgvector `<#>` returns the negative, so ascending order =
// highest BM25). Only chunks with term overlap (inner product > 0) are returned.
// The raw BM25 score is carried on each ranked row. RDS-portable — no ParadeDB.
// unaccent in the tokenizer lets diacritic-less queries match.
func (r *hybridRetriever) sparseArm(ctx context.Context, query string, res resolved) ([]ranked, error) {
	qv := lexical.QueryVector(query)
	args := []any{qv, res.bm25K}

	const inForceBody = `
SELECT c.id, (c.content_sparse <#> $1::sparsevec) AS neg_ip
FROM gold.chunk c
WHERE c.content_sparse IS NOT NULL
  AND (c.content_sparse <#> $1::sparsevec) < 0
  AND c.document_id IN (SELECT document_id FROM in_force)
ORDER BY c.content_sparse <#> $1::sparsevec
LIMIT $2`

	cte, fargs := buildDocFilterCTE(res, len(args)+1)
	var sql string
	if cte == "" {
		sql = `
SELECT c.id, (c.content_sparse <#> $1::sparsevec) AS neg_ip
FROM gold.chunk c
WHERE c.content_sparse IS NOT NULL
  AND (c.content_sparse <#> $1::sparsevec) < 0
ORDER BY c.content_sparse <#> $1::sparsevec
LIMIT $2`
	} else {
		args = append(args, fargs...)
		sql = cte + inForceBody
	}

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	return scanRankedBM25(rows)
}

// scanRankedBM25 reads (chunk id, negative inner product) rows into a ranked list,
// converting to a positive BM25 score (−neg_ip) and assigning 1-based ranks in
// result order. It closes rows.
func scanRankedBM25(rows pgx.Rows) ([]ranked, error) {
	defer rows.Close()
	var out []ranked
	rank := 0
	for rows.Next() {
		var id int64
		var negIP float64
		if err := rows.Scan(&id, &negIP); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		rank++
		out = append(out, ranked{chunkID: id, rank: rank, bm25Score: -negIP})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// scanRanked reads a single-column (chunk id) result set into a ranked list,
// assigning 1-based ranks in result order. It closes rows.
func scanRanked(rows pgx.Rows) ([]ranked, error) {
	defer rows.Close()
	var out []ranked
	rank := 0
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		rank++
		out = append(out, ranked{chunkID: id, rank: rank})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// scanRankedWithDistance reads (chunk id, cosine distance) rows into a ranked
// list, converting distance to similarity (1 − distance) so the evidence gate
// can apply an absolute relevance floor. It closes rows.
func scanRankedWithDistance(rows pgx.Rows) ([]ranked, error) {
	defer rows.Close()
	var out []ranked
	rank := 0
	for rows.Next() {
		var id int64
		var dist float64
		if err := rows.Scan(&id, &dist); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		rank++
		out = append(out, ranked{chunkID: id, rank: rank, similarity: 1 - dist})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// nonCurrentCap bounds how many non-current (expired/not-yet/…) provisions are
// surfaced after the current-law results. Small on purpose: current law leads.
const nonCurrentCap = 3

// nonCurrentHits runs a vector-only pass restricted to non-current law and returns
// badged hits for surfacing after the current results (so repealed/overlapping law
// stays findable, not excluded). At most one hit per document — the pass says
// "this non-current document also matches", not "here are its provisions" — and
// at most min(nonCurrentCap, topK) hits, so a small top_k is not dwarfed by the
// non-current tail.
func (r *hybridRetriever) nonCurrentHits(ctx context.Context, query string, res resolved) ([]Hit, error) {
	vecs, err := r.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vecs) != 1 || vecs[0] == nil {
		return nil, fmt.Errorf("embedder returned %d vectors for one query", len(vecs))
	}
	list, err := r.vectorArm(ctx, pgvector.NewVector(vecs[0]), res, true)
	if err != nil {
		return nil, fmt.Errorf("vector arm: %w", err)
	}
	fused := fuseRRF(list, nil, res.rrfK, r.cfg.LexicalWeight)
	if len(fused) == 0 {
		return nil, nil
	}
	pool := nonCurrentCap
	if res.rollupLevel != rollupNone {
		pool = nonCurrentCap * rollupOverfetch
	}
	if len(fused) > pool {
		fused = fused[:pool]
	}
	hits, err := r.hydrate(ctx, fused)
	if err != nil {
		return nil, fmt.Errorf("hydrate: %w", err)
	}
	hits = rollupByParent(hits, res.rollupLevel)
	hits = bestHitPerDocument(hits)
	limit := nonCurrentCap
	if res.topK < limit {
		limit = res.topK
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// dedupeRelationsPerDocument keeps each document's relation evidence on its
// first (best-ranked) hit only: sibling hits from the same document would
// otherwise repeat an identical relations array on every hit and bloat the
// evidence pack.
func dedupeRelationsPerDocument(hits []Hit) []Hit {
	seen := make(map[int64]struct{}, len(hits))
	for i := range hits {
		if _, dup := seen[hits[i].DocumentID]; dup {
			hits[i].Relations = nil
			continue
		}
		seen[hits[i].DocumentID] = struct{}{}
	}
	return hits
}

// bestHitPerDocument keeps the first (best-ranked) hit of each document,
// preserving order.
func bestHitPerDocument(hits []Hit) []Hit {
	out := make([]Hit, 0, len(hits))
	seen := make(map[int64]struct{}, len(hits))
	for _, h := range hits {
		if _, dup := seen[h.DocumentID]; dup {
			continue
		}
		seen[h.DocumentID] = struct{}{}
		out = append(out, h)
	}
	return out
}

// appendNonCurrent appends non-current hits after the current ones, skipping any
// chunk already present in the current results.
func appendNonCurrent(current, nonCurrent []Hit) []Hit {
	if len(nonCurrent) == 0 {
		return current
	}
	seen := make(map[int64]bool, len(current))
	for _, h := range current {
		seen[h.ChunkID] = true
	}
	for _, h := range nonCurrent {
		if !seen[h.ChunkID] {
			current = append(current, h)
			seen[h.ChunkID] = true
		}
	}
	return current
}

// rollupByParent collapses ranked hits that share a parent provision (at the given
// level) to their best-scoring representative, so sibling Điểm/Đoạn of one Khoản do
// not crowd out other provisions. Rank order is preserved and every hit's
// ParentCitation is set (for the agent to navigate up via the document tool).
// level=="none" disables collapsing but still annotates ParentCitation.
func rollupByParent(hits []Hit, level string) []Hit {
	out := make([]Hit, 0, len(hits))
	seen := make(map[string]struct{}, len(hits))
	for _, h := range hits {
		h.ParentCitation = parentCitation(h.Citation, level)
		if level == rollupNone || level == "" {
			out = append(out, h)
			continue
		}
		key := strconv.FormatInt(h.DocumentID, 10) + "|" + h.ParentCitation
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, h)
	}
	return out
}

// parentCitation rolls a provision citation up to the given level by keeping the
// comma-separated parts up to and including that level and dropping the finer ones
// ("Điểm"/"Đoạn" for khoan; "Khoản" and below for dieu). A citation already coarser
// than the level (e.g. "Điều 19" rolled to khoan) is returned unchanged.
func parentCitation(citation, level string) string {
	citation = strings.TrimSpace(citation)
	var prefix string
	switch level {
	case rollupDieu:
		prefix = "điều "
	case rollupKhoan:
		prefix = "khoản "
	default:
		return citation
	}
	parts := strings.Split(citation, ",")
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		kept = append(kept, p)
		if strings.HasPrefix(strings.ToLower(p), prefix) {
			break // reached the requested level; drop finer parts
		}
	}
	if len(kept) == 0 {
		return citation
	}
	return strings.Join(kept, ", ")
}

// hydrate loads citation metadata for the fused hits and returns them as []Hit in
// the same (already score-sorted) order. It fetches all rows in one query keyed by
// chunk id, then re-orders to match fused so the RRF ranking is preserved.
func (r *hybridRetriever) hydrate(ctx context.Context, fused []fusedHit) ([]Hit, error) {
	ids := make([]int64, len(fused))
	for i, f := range fused {
		ids[i] = f.chunkID
	}

	const sql = `
WITH current_validity AS (
    SELECT DISTINCT ON (document_id, section_id)
        document_id,
        section_id,
        status_code,
        status_class,
        eff_from,
        eff_to,
        COALESCE(source, '') AS source,
        COALESCE(reason, '') AS reason
    FROM silver.validity_period
    WHERE superseded_at IS NULL
    ORDER BY document_id, section_id NULLS FIRST, observed_at DESC, id DESC
),
text_state AS (
    SELECT
        document_id,
        bool_or(is_binding AND NULLIF(btrim(COALESCE(markdown, '')), '') IS NOT NULL) AS has_binding_text,
        bool_or((NOT is_binding) AND NULLIF(btrim(COALESCE(markdown, '')), '') IS NOT NULL) AS has_nonbinding_text,
        bool_or(needs_review) AS needs_review,
        COALESCE(array_agg(DISTINCT authority ORDER BY authority), ARRAY[]::text[]) AS authorities,
        COALESCE(array_agg(DISTINCT source ORDER BY source) FILTER (WHERE NULLIF(source, '') IS NOT NULL), ARRAY[]::text[]) AS sources,
        COALESCE(array_agg(DISTINCT extract_engine ORDER BY extract_engine) FILTER (WHERE NULLIF(extract_engine, '') IS NOT NULL), ARRAY[]::text[]) AS extract_engines,
        COALESCE(max(extract_confidence), 0) AS max_confidence
    FROM silver.document_text
    GROUP BY document_id
)
SELECT
    c.id,
    c.document_id,
    c.citation,
    COALESCE(c.context_prefix, ''),
    c.content,
    COALESCE(d.doc_number, ''),
    COALESCE(d.title, ''),
    COALESCE(sd.source, '') AS source,
    COALESCE(sd.detail_url, '') AS source_url,
    COALESCE(secv.section_id, docv.section_id, 0) AS validity_section_id,
    COALESCE(secv.status_code, docv.status_code, '') AS status_code,
    COALESCE(secv.status_class, docv.status_class, '') AS status_class,
    COALESCE(to_char(COALESCE(secv.eff_from, docv.eff_from), 'YYYY-MM-DD'), '') AS eff_from,
    COALESCE(to_char(COALESCE(secv.eff_to, docv.eff_to), 'YYYY-MM-DD'), '') AS eff_to,
    COALESCE(secv.source, docv.source, '') AS validity_source,
    COALESCE(secv.reason, docv.reason, '') AS validity_reason,
    COALESCE(ts.has_binding_text, false) AS has_binding_text,
    COALESCE(ts.has_nonbinding_text, false) AS has_nonbinding_text,
    COALESCE(ts.needs_review, false) AS text_needs_review,
    COALESCE(ts.authorities, ARRAY[]::text[]) AS text_authorities,
    COALESCE(ts.sources, ARRAY[]::text[]) AS text_sources,
    COALESCE(ts.extract_engines, ARRAY[]::text[]) AS extract_engines,
    COALESCE(ts.max_confidence, 0) AS max_confidence,
    COALESCE(to_char(d.issued_at, 'YYYY-MM-DD'), '') AS issued_at
FROM gold.chunk c
LEFT JOIN silver.document d ON d.id = c.document_id
LEFT JOIN bronze.source_document sd ON sd.id = d.source_document_id
LEFT JOIN current_validity docv ON docv.document_id = c.document_id AND docv.section_id IS NULL
LEFT JOIN current_validity secv ON secv.document_id = c.document_id AND secv.section_id = c.section_id
LEFT JOIN text_state ts ON ts.document_id = c.document_id
WHERE c.id = ANY($1)`

	rows, err := r.pool.Query(ctx, sql, ids)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	type meta struct {
		documentID    int64
		citation      string
		contextPrefix string
		content       string
		docNumber     string
		title         string
		issuedDate    string
		source        string
		sourceURL     string
		validity      ValidityEvidence
		text          TextEvidence
	}
	byID := make(map[int64]meta, len(fused))
	for rows.Next() {
		var id int64
		var m meta
		if err := rows.Scan(
			&id,
			&m.documentID,
			&m.citation,
			&m.contextPrefix,
			&m.content,
			&m.docNumber,
			&m.title,
			&m.source,
			&m.sourceURL,
			&m.validity.SectionID,
			&m.validity.StatusCode,
			&m.validity.StatusClass,
			&m.validity.EffectiveFrom,
			&m.validity.EffectiveTo,
			&m.validity.Source,
			&m.validity.Reason,
			&m.text.HasBindingText,
			&m.text.HasNonBindingText,
			&m.text.NeedsReview,
			&m.text.Authorities,
			&m.text.Sources,
			&m.text.ExtractEngines,
			&m.text.MaxConfidence,
			&m.issuedDate,
		); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		byID[id] = m
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}

	docIDs := make([]int64, 0, len(byID))
	seenDocIDs := make(map[int64]bool, len(byID))
	for _, m := range byID {
		if m.documentID == 0 || seenDocIDs[m.documentID] {
			continue
		}
		seenDocIDs[m.documentID] = true
		docIDs = append(docIDs, m.documentID)
	}
	relations, err := r.loadRelations(ctx, docIDs)
	if err != nil {
		return nil, fmt.Errorf("load relations: %w", err)
	}

	hits := make([]Hit, 0, len(fused))
	for _, f := range fused {
		m, ok := byID[f.chunkID]
		if !ok {
			// A chunk vanished between ranking and hydration (concurrent delete);
			// skip it rather than emit an empty citation.
			r.log.Warn("retrieve: ranked chunk missing on hydrate, skipping", "chunk_id", f.chunkID)
			continue
		}
		hits = append(hits, Hit{
			ChunkID:       f.chunkID,
			DocumentID:    m.documentID,
			DocNumber:     m.docNumber,
			Title:         m.title,
			IssuedDate:    m.issuedDate,
			Source:        m.source,
			SourceURL:     m.sourceURL,
			Citation:      m.citation,
			ContextPrefix: m.contextPrefix,
			Content:       m.content,
			Score:         f.score,
			Similarity:    f.similarity,
			BM25Score:     f.bm25Score,
			VectorRank:    f.vectorRank,
			BM25Rank:      f.bm25Rank,
			Validity:      m.validity,
			Text:          m.text,
			Relations:     relations[m.documentID],
		})
	}
	return hits, nil
}

// loadRelations returns one-hop confirmed relation evidence adjacent to each hit
// document. Relations are deliberately kept separate from ranked chunks: a related
// document is graph evidence for status/context, not proof that its chunks answer
// the user's question.
func (r *hybridRetriever) loadRelations(ctx context.Context, docIDs []int64) (map[int64][]Relation, error) {
	if len(docIDs) == 0 {
		return nil, nil
	}

	const sql = `
WITH hit_doc(id) AS (
    SELECT unnest($1::bigint[])
),
current_validity AS (
    SELECT DISTINCT ON (document_id)
        document_id,
        status_code,
        status_class,
        eff_from,
        eff_to,
        COALESCE(source, '') AS source,
        COALESCE(reason, '') AS reason
    FROM silver.validity_period
    WHERE superseded_at IS NULL
      AND section_id IS NULL
    ORDER BY document_id, observed_at DESC, id DESC
),
text_state AS (
    SELECT
        document_id,
        bool_or(is_binding AND NULLIF(btrim(COALESCE(markdown, '')), '') IS NOT NULL) AS has_binding_text,
        bool_or((NOT is_binding) AND NULLIF(btrim(COALESCE(markdown, '')), '') IS NOT NULL) AS has_nonbinding_text,
        bool_or(needs_review) AS needs_review,
        COALESCE(array_agg(DISTINCT authority ORDER BY authority), ARRAY[]::text[]) AS authorities,
        COALESCE(array_agg(DISTINCT source ORDER BY source) FILTER (WHERE NULLIF(source, '') IS NOT NULL), ARRAY[]::text[]) AS sources,
        COALESCE(array_agg(DISTINCT extract_engine ORDER BY extract_engine) FILTER (WHERE NULLIF(extract_engine, '') IS NOT NULL), ARRAY[]::text[]) AS extract_engines,
        COALESCE(max(extract_confidence), 0) AS max_confidence
    FROM silver.document_text
    GROUP BY document_id
),
relation_rows AS (
    SELECT
        dr.from_document_id AS base_document_id,
        dr.id AS relation_id,
        'outgoing'::text AS direction,
        dr.relation_type,
        COALESCE(dr.source, '') AS source,
        COALESCE(dr.to_citation, '') AS to_citation,
        dr.relation_type_raw,
        COALESCE(ref.document_id, 0) AS related_document_id,
        COALESCE(td.doc_number, ref.label, ref.ref_key, '') AS related_doc_number,
        COALESCE(td.title, '') AS related_title,
        ref.document_id IS NOT NULL AS resolved,
        EXISTS (SELECT 1 FROM gold.chunk c WHERE c.document_id = ref.document_id) AS target_indexed,
        COALESCE(ts.has_binding_text, false) AS target_has_binding_text,
        COALESCE(ts.needs_review, false) AS target_needs_review,
        COALESCE(cv.status_code, '') AS target_status_code,
        COALESCE(cv.status_class, '') AS target_status_class,
        COALESCE(to_char(cv.eff_from, 'YYYY-MM-DD'), '') AS target_eff_from,
        COALESCE(to_char(cv.eff_to, 'YYYY-MM-DD'), '') AS target_eff_to,
        COALESCE(cv.source, '') AS target_validity_source,
        COALESCE(cv.reason, '') AS target_validity_reason,
        COALESCE(ts.has_nonbinding_text, false) AS target_has_nonbinding_text,
        COALESCE(ts.authorities, ARRAY[]::text[]) AS target_authorities,
        COALESCE(ts.sources, ARRAY[]::text[]) AS target_text_sources,
        COALESCE(ts.extract_engines, ARRAY[]::text[]) AS target_extract_engines,
        COALESCE(ts.max_confidence, 0) AS target_max_confidence,
        COALESCE(ev.id, 0) AS evidence_id,
        COALESCE(ev.evidence_kind, '') AS evidence_kind,
        COALESCE(ev.operator, '') AS evidence_operator,
        COALESCE(ev.target_text, '') AS evidence_target_text,
        COALESCE(ev.target_citation, '') AS evidence_target_citation,
        COALESCE(ev.citation, '') AS evidence_citation,
        COALESCE(ev.snippet, '') AS evidence_snippet,
        COALESCE(ev.source_authority, '') AS evidence_source_authority,
        COALESCE(ev.confidence, 0) AS evidence_confidence,
        COALESCE(ev.promoted, false) AS evidence_promoted
    FROM silver.document_relation dr
    JOIN hit_doc h ON h.id = dr.from_document_id
    JOIN silver.doc_ref ref ON ref.id = dr.to_ref_id
    LEFT JOIN silver.document td ON td.id = ref.document_id
    LEFT JOIN current_validity cv ON cv.document_id = ref.document_id
    LEFT JOIN text_state ts ON ts.document_id = ref.document_id
    LEFT JOIN LATERAL (
        SELECT
            re.id,
            re.evidence_kind,
            re.operator,
            re.target_text,
            re.target_citation,
            re.citation,
            re.snippet,
            re.source_authority,
            re.confidence,
            re.promoted
        FROM silver.relation_evidence re
        WHERE re.from_document_id = dr.from_document_id
          AND re.target_ref_id = dr.to_ref_id
          AND re.relation_type = dr.relation_type
        ORDER BY re.promoted DESC, re.confidence DESC, re.id
        LIMIT 1
    ) ev ON true

    UNION ALL

    SELECT
        ref.document_id AS base_document_id,
        dr.id AS relation_id,
        'incoming'::text AS direction,
        dr.relation_type,
        COALESCE(dr.source, '') AS source,
        COALESCE(dr.to_citation, '') AS to_citation,
        dr.relation_type_raw,
        dr.from_document_id AS related_document_id,
        COALESCE(fd.doc_number, '') AS related_doc_number,
        COALESCE(fd.title, '') AS related_title,
        true AS resolved,
        EXISTS (SELECT 1 FROM gold.chunk c WHERE c.document_id = dr.from_document_id) AS target_indexed,
        COALESCE(ts.has_binding_text, false) AS target_has_binding_text,
        COALESCE(ts.needs_review, false) AS target_needs_review,
        COALESCE(cv.status_code, '') AS target_status_code,
        COALESCE(cv.status_class, '') AS target_status_class,
        COALESCE(to_char(cv.eff_from, 'YYYY-MM-DD'), '') AS target_eff_from,
        COALESCE(to_char(cv.eff_to, 'YYYY-MM-DD'), '') AS target_eff_to,
        COALESCE(cv.source, '') AS target_validity_source,
        COALESCE(cv.reason, '') AS target_validity_reason,
        COALESCE(ts.has_nonbinding_text, false) AS target_has_nonbinding_text,
        COALESCE(ts.authorities, ARRAY[]::text[]) AS target_authorities,
        COALESCE(ts.sources, ARRAY[]::text[]) AS target_text_sources,
        COALESCE(ts.extract_engines, ARRAY[]::text[]) AS target_extract_engines,
        COALESCE(ts.max_confidence, 0) AS target_max_confidence,
        COALESCE(ev.id, 0) AS evidence_id,
        COALESCE(ev.evidence_kind, '') AS evidence_kind,
        COALESCE(ev.operator, '') AS evidence_operator,
        COALESCE(ev.target_text, '') AS evidence_target_text,
        COALESCE(ev.target_citation, '') AS evidence_target_citation,
        COALESCE(ev.citation, '') AS evidence_citation,
        COALESCE(ev.snippet, '') AS evidence_snippet,
        COALESCE(ev.source_authority, '') AS evidence_source_authority,
        COALESCE(ev.confidence, 0) AS evidence_confidence,
        COALESCE(ev.promoted, false) AS evidence_promoted
    FROM silver.doc_ref ref
    JOIN hit_doc h ON h.id = ref.document_id
    JOIN silver.document_relation dr ON dr.to_ref_id = ref.id
    JOIN silver.document fd ON fd.id = dr.from_document_id
    LEFT JOIN current_validity cv ON cv.document_id = dr.from_document_id
    LEFT JOIN text_state ts ON ts.document_id = dr.from_document_id
    LEFT JOIN LATERAL (
        SELECT
            re.id,
            re.evidence_kind,
            re.operator,
            re.target_text,
            re.target_citation,
            re.citation,
            re.snippet,
            re.source_authority,
            re.confidence,
            re.promoted
        FROM silver.relation_evidence re
        WHERE re.from_document_id = dr.from_document_id
          AND re.target_ref_id = dr.to_ref_id
          AND re.relation_type = dr.relation_type
        ORDER BY re.promoted DESC, re.confidence DESC, re.id
        LIMIT 1
    ) ev ON true
    WHERE ref.document_id IS NOT NULL
),
ranked AS (
    SELECT
        *,
        row_number() OVER (
            PARTITION BY base_document_id
            ORDER BY
                CASE direction WHEN 'outgoing' THEN 0 ELSE 1 END,
                relation_type,
                related_doc_number,
                relation_id
        ) AS rn
    FROM relation_rows
)
SELECT
    base_document_id,
    relation_id,
    direction,
    relation_type,
    source,
    to_citation,
    relation_type_raw,
    related_document_id,
    related_doc_number,
    related_title,
    resolved,
    target_indexed,
    target_has_binding_text,
    target_needs_review,
    target_status_code,
    target_status_class,
    target_eff_from,
    target_eff_to,
    target_validity_source,
    target_validity_reason,
    target_has_nonbinding_text,
    target_authorities,
    target_text_sources,
    target_extract_engines,
    target_max_confidence,
    evidence_id,
    evidence_kind,
    evidence_operator,
    evidence_target_text,
    evidence_target_citation,
    evidence_citation,
    evidence_snippet,
    evidence_source_authority,
    evidence_confidence,
    evidence_promoted
FROM ranked
WHERE rn <= $2
ORDER BY base_document_id, rn`

	rows, err := r.pool.Query(ctx, sql, docIDs, relationLimitPerDocument)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	out := make(map[int64][]Relation, len(docIDs))
	for rows.Next() {
		var (
			baseDocumentID int64
			rel            Relation
		)
		if err := rows.Scan(
			&baseDocumentID,
			&rel.RelationID,
			&rel.Direction,
			&rel.RelationType,
			&rel.Source,
			&rel.ToCitation,
			&rel.RelationTypeRaw,
			&rel.DocumentID,
			&rel.DocNumber,
			&rel.Title,
			&rel.Resolved,
			&rel.TargetIndexed,
			&rel.TargetHasBindingText,
			&rel.TargetNeedsReview,
			&rel.TargetValidity.StatusCode,
			&rel.TargetValidity.StatusClass,
			&rel.TargetValidity.EffectiveFrom,
			&rel.TargetValidity.EffectiveTo,
			&rel.TargetValidity.Source,
			&rel.TargetValidity.Reason,
			&rel.TargetText.HasNonBindingText,
			&rel.TargetText.Authorities,
			&rel.TargetText.Sources,
			&rel.TargetText.ExtractEngines,
			&rel.TargetText.MaxConfidence,
			&rel.Evidence.EvidenceID,
			&rel.Evidence.EvidenceKind,
			&rel.Evidence.Operator,
			&rel.Evidence.TargetText,
			&rel.Evidence.TargetCitation,
			&rel.Evidence.Citation,
			&rel.Evidence.Snippet,
			&rel.Evidence.SourceAuthority,
			&rel.Evidence.Confidence,
			&rel.Evidence.Promoted,
		); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		rel.TargetText.HasBindingText = rel.TargetHasBindingText
		rel.TargetText.NeedsReview = rel.TargetNeedsReview
		out[baseDocumentID] = append(out[baseDocumentID], rel)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}
