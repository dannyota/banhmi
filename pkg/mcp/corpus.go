package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/rag/retrieve"
)

// CorpusReader is the DB-backed corpus slice the MCP surface exposes to external
// agents. It lets deployed agents inspect database quality and exact documents
// without reading repo docs or local files.
type CorpusReader interface {
	CorpusStatus(ctx context.Context) (corpusStatusOutput, error)
	QualityGaps(ctx context.Context, in qualityGapsInput) (qualityGapsOutput, error)
	Document(ctx context.Context, in documentInput) (documentOutput, error)
}

type dbCorpus struct {
	pool *pgxpool.Pool
	// filesListing maps a source code to its stable files-listing URL builder
	// (WithFilesListingURL); sources without one simply omit files_url.
	filesListing map[string]func(externalID string) string
}

// coverageCounts is the headline corpus scale stamped into the server-level
// instructions so a connecting agent sees how much official material backs the
// evidence.
type coverageCounts struct {
	Docs    int64
	Chunks  int64
	Sources int64
}

// Coverage returns the headline corpus counts in one round-trip, for the server
// instructions brief. It is read once at startup; callers fall back to a count-free
// brief on error.
func (c dbCorpus) Coverage(ctx context.Context) (coverageCounts, error) {
	const q = `SELECT
  (SELECT count(*) FROM silver.document),
  (SELECT count(*) FROM gold.chunk),
  (SELECT count(DISTINCT source) FROM bronze.source_document)`
	var cc coverageCounts
	if err := c.pool.QueryRow(ctx, q).Scan(&cc.Docs, &cc.Chunks, &cc.Sources); err != nil {
		return coverageCounts{}, fmt.Errorf("coverage counts: %w", err)
	}
	return cc, nil
}

// Issuers returns the corpus's distinct issuer values, most documents first,
// for the search tool's issuer-filter hint. Read once at startup; corpora
// without issuer metadata return an empty list.
func (c dbCorpus) Issuers(ctx context.Context) ([]string, error) {
	const q = `SELECT issuer FROM silver.document WHERE COALESCE(issuer, '') <> '' GROUP BY issuer ORDER BY count(*) DESC, issuer LIMIT 10`
	rows, err := c.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("issuer values: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("issuer values: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// --- guide ---------------------------------------------------------------------

type guideInput struct{}

type guideTool struct {
	Name string `json:"name"`
	Use  string `json:"use"`
}

type guideOutput struct {
	Purpose          string      `json:"purpose"`
	RecommendedFlow  []string    `json:"recommended_flow"`
	Tools            []guideTool `json:"tools"`
	EvidenceContract []string    `json:"evidence_contract"`
}

func (s *Server) handleGuide(_ context.Context, _ *mcpsdk.CallToolRequest, _ guideInput) (*mcpsdk.CallToolResult, guideOutput, error) {
	out := s.brief.guide
	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: out.Purpose}},
	}, out, nil
}

// --- corpus_status -------------------------------------------------------------

type corpusStatusInput struct{}

type corpusDocStats struct {
	Total              int64 `json:"total"`
	Indexed            int64 `json:"indexed"`
	Unindexed          int64 `json:"unindexed"`
	NonBindingOnly     int64 `json:"non_binding_only_unindexed"`
	IndexedNoBinding   int64 `json:"indexed_without_binding_text"`
	NeedsReviewTextDoc int64 `json:"needs_review_text_docs"`
	// RelationContext documents were pulled only by relation backfill and fall
	// outside the configured scope: text and relations stay served, chunks are
	// intentionally not indexed. Disclosed separately so they never read as a
	// data problem.
	RelationContext int64 `json:"relation_context_unindexed"`
}

type corpusChunkStats struct {
	Total                int64 `json:"total"`
	ConfiguredEmbeddings int64 `json:"configured_embeddings"`
	AllEmbeddings        int64 `json:"all_embeddings"`
}

type corpusRelationStats struct {
	ConfirmedEdges  int64 `json:"confirmed_edges"`
	ResolvedTargets int64 `json:"resolved_targets"`
	StubTargets     int64 `json:"stub_targets"`
	IndexedTargets  int64 `json:"indexed_targets"`
	TargetTextGaps  int64 `json:"target_text_gaps"`
}

type corpusFetchStats struct {
	Total          int64              `json:"total"`
	Complete       int64              `json:"complete"`
	Partial        int64              `json:"partial"`
	Error          int64              `json:"error"`
	NotComplete    int64              `json:"not_complete"`
	ArtifactsError int64              `json:"artifacts_error"`
	ArtifactsDead  int64              `json:"artifacts_dead"`
	BySource       []sourceFetchStats `json:"by_source,omitempty"`
}

type sourceFetchStats struct {
	Source      string `json:"source"`
	Total       int64  `json:"total"`
	Complete    int64  `json:"complete"`
	Partial     int64  `json:"partial"`
	Error       int64  `json:"error"`
	NotComplete int64  `json:"not_complete"`
}

type corpusGapStats struct {
	DocsWithoutCurrentValidity int64 `json:"docs_without_current_validity"`
	SectionValidityRows        int64 `json:"section_validity_rows"`
	TextNeedsReviewDocs        int64 `json:"text_needs_review_docs"`
	NonBindingOnlyUnindexed    int64 `json:"non_binding_only_unindexed_docs"`
	UnresolvedRelationTargets  int64 `json:"unresolved_relation_targets"`
	RelationTargetTextGaps     int64 `json:"relation_target_text_gaps"`
	MojibakeChunks             int64 `json:"mojibake_chunks"`
}

type corpusStatusOutput struct {
	Version     string              `json:"version,omitempty"`
	SearchReady bool                `json:"search_ready"`
	EmbedModel  string              `json:"embed_model"`
	Docs        corpusDocStats      `json:"docs"`
	Chunks      corpusChunkStats    `json:"chunks"`
	Relations   corpusRelationStats `json:"relations"`
	Fetch       corpusFetchStats    `json:"fetch"`
	Gaps        corpusGapStats      `json:"gaps"`
	Notes       []string            `json:"notes,omitempty"`
}

func (s *Server) handleCorpusStatus(ctx context.Context, _ *mcpsdk.CallToolRequest, _ corpusStatusInput) (*mcpsdk.CallToolResult, corpusStatusOutput, error) {
	if s.corpus == nil {
		return nil, corpusStatusOutput{}, fmt.Errorf("corpus database is not configured")
	}
	out, err := s.corpus.CorpusStatus(ctx)
	if err != nil {
		s.log.Error("mcp: corpus_status", "err", err)
		return nil, corpusStatusOutput{}, fmt.Errorf("corpus status: %w", err)
	}
	out.Version = s.version
	return nil, out, nil
}

func (c dbCorpus) CorpusStatus(ctx context.Context) (corpusStatusOutput, error) {
	const q = `
WITH docs AS (
  SELECT d.id,
         d.index_class,
         EXISTS (SELECT 1 FROM gold.chunk ch WHERE ch.document_id=d.id) AS indexed,
         EXISTS (
           SELECT 1 FROM silver.document_text dt
           WHERE dt.document_id=d.id AND dt.is_binding AND NULLIF(btrim(COALESCE(dt.markdown, '')), '') IS NOT NULL
         ) AS has_binding_text,
         EXISTS (
           SELECT 1 FROM silver.document_text dt
           WHERE dt.document_id=d.id AND NOT dt.is_binding AND NULLIF(btrim(COALESCE(dt.markdown, '')), '') IS NOT NULL
         ) AS has_nonbinding_text,
         EXISTS (
           SELECT 1 FROM silver.document_text dt
           WHERE dt.document_id=d.id AND dt.needs_review
         ) AS needs_review_text
  FROM silver.document d
),
relation_targets AS (
  SELECT DISTINCT dr.to_ref_id, ref.document_id
  FROM silver.document_relation dr
  JOIN silver.doc_ref ref ON ref.id=dr.to_ref_id
)
SELECT
  (SELECT count(*) FROM docs) AS docs_total,
  (SELECT count(*) FROM docs WHERE indexed) AS docs_indexed,
  (SELECT count(*) FROM docs WHERE NOT indexed AND index_class <> 'relation_context') AS docs_unindexed,
  (SELECT count(*) FROM docs WHERE NOT indexed AND index_class <> 'relation_context' AND has_nonbinding_text AND NOT has_binding_text) AS nonbinding_only,
  (SELECT count(*) FROM docs WHERE indexed AND NOT has_binding_text) AS indexed_no_binding,
  (SELECT count(*) FROM docs WHERE needs_review_text) AS needs_review_text_docs,
  (SELECT count(*) FROM gold.chunk) AS chunks_total,
  (SELECT count(*) FROM gold.chunk_embedding WHERE model=$1 AND dims=$2) AS configured_embeddings,
  (SELECT count(*) FROM gold.chunk_embedding) AS all_embeddings,
  (SELECT count(*) FROM silver.document_relation) AS confirmed_edges,
  (SELECT count(*) FROM relation_targets WHERE document_id IS NOT NULL) AS resolved_targets,
  (SELECT count(*) FROM relation_targets WHERE document_id IS NULL) AS stub_targets,
  (SELECT count(*) FROM relation_targets rt WHERE EXISTS (SELECT 1 FROM gold.chunk ch WHERE ch.document_id=rt.document_id)) AS indexed_targets,
  (SELECT count(*) FROM relation_targets rt
   WHERE rt.document_id IS NOT NULL
     AND NOT EXISTS (SELECT 1 FROM gold.chunk ch WHERE ch.document_id=rt.document_id)
     AND NOT EXISTS (
       SELECT 1 FROM silver.document_text dt
       WHERE dt.document_id=rt.document_id
         AND dt.is_binding
         AND NULLIF(btrim(COALESCE(dt.markdown, '')), '') IS NOT NULL
     )) AS target_text_gaps,
  (SELECT count(*) FROM docs d
   WHERE NOT EXISTS (
     SELECT 1 FROM silver.validity_period vp
     WHERE vp.document_id=d.id AND vp.section_id IS NULL AND vp.superseded_at IS NULL
   )) AS docs_without_current_validity,
  (SELECT count(*) FROM silver.validity_period vp
   WHERE vp.section_id IS NOT NULL AND vp.superseded_at IS NULL) AS section_validity_rows,
  (SELECT count(*) FROM gold.chunk
   WHERE content ~ 'Ã[¡-ÿ]' OR content LIKE '%�%' OR content LIKE '%â€%' OR content ~ '[Ѐ-ӿ]'
      OR citation ~ 'Ã[¡-ÿ]' OR citation LIKE '%�%' OR citation LIKE '%â€%' OR citation ~ '[Ѐ-ӿ]') AS mojibake_chunks,
  (SELECT count(*) FROM docs WHERE NOT indexed AND index_class = 'relation_context') AS relation_context_unindexed`
	var out corpusStatusOutput
	out.EmbedModel = config.EmbedModel
	if err := c.pool.QueryRow(ctx, q, config.EmbedModel, config.EmbedDims).Scan(
		&out.Docs.Total,
		&out.Docs.Indexed,
		&out.Docs.Unindexed,
		&out.Docs.NonBindingOnly,
		&out.Docs.IndexedNoBinding,
		&out.Docs.NeedsReviewTextDoc,
		&out.Chunks.Total,
		&out.Chunks.ConfiguredEmbeddings,
		&out.Chunks.AllEmbeddings,
		&out.Relations.ConfirmedEdges,
		&out.Relations.ResolvedTargets,
		&out.Relations.StubTargets,
		&out.Relations.IndexedTargets,
		&out.Relations.TargetTextGaps,
		&out.Gaps.DocsWithoutCurrentValidity,
		&out.Gaps.SectionValidityRows,
		&out.Gaps.MojibakeChunks,
		&out.Docs.RelationContext,
	); err != nil {
		return corpusStatusOutput{}, fmt.Errorf("query corpus status: %w", err)
	}
	fetch, err := c.fetchStats(ctx)
	if err != nil {
		return corpusStatusOutput{}, err
	}
	out.Fetch = fetch
	out.Gaps.TextNeedsReviewDocs = out.Docs.NeedsReviewTextDoc
	out.Gaps.NonBindingOnlyUnindexed = out.Docs.NonBindingOnly
	out.Gaps.UnresolvedRelationTargets = out.Relations.StubTargets
	out.Gaps.RelationTargetTextGaps = out.Relations.TargetTextGaps
	out.SearchReady = out.Chunks.Total > 0
	out.Notes = corpusStatusNotes(out)
	return out, nil
}

// --- quality_gaps --------------------------------------------------------------

type qualityGapsInput struct {
	Category string `json:"category,omitempty" jsonschema:"optional category: all, fetch, non_binding, mojibake, partial_validity, unresolved_relations, relation_target_text"`
	Limit    int    `json:"limit,omitempty" jsonschema:"maximum rows per category (0 = default, max 100)"`
}

type qualityGapsOutput struct {
	Limit                  int                         `json:"limit"`
	Categories             []string                    `json:"categories"`
	Summary                corpusGapStats              `json:"summary"`
	FetchIncomplete        []qualityFetchGap           `json:"fetch_incomplete,omitempty"`
	NonBindingOnly         []qualityTextGap            `json:"non_binding_only,omitempty"`
	MojibakeChunks         []qualityChunkGap           `json:"mojibake_chunks,omitempty"`
	PartialValidity        []qualityValidityGap        `json:"partial_validity,omitempty"`
	UnresolvedRelations    []qualityUnresolvedRelation `json:"unresolved_relations,omitempty"`
	RelationTargetTextGaps []qualityRelationTextGap    `json:"relation_target_text_gaps,omitempty"`
	Notes                  []string                    `json:"notes,omitempty"`
}

type qualityFetchGap struct {
	FetchDocID        int64  `json:"fetch_doc_id"`
	Source            string `json:"source"`
	ExternalID        string `json:"external_id"`
	State             string `json:"state"`
	Provenance        string `json:"provenance,omitempty"`
	ArtifactsExpected int64  `json:"artifacts_expected"`
	ArtifactsDone     int64  `json:"artifacts_done"`
	ArtifactsFailed   int64  `json:"artifacts_failed"`
	DocNumber         string `json:"doc_number,omitempty"`
	Title             string `json:"title,omitempty"`
	ArtifactStates    string `json:"artifact_states,omitempty"`
	NextAttemptAt     string `json:"next_attempt_at,omitempty"`
	LastError         string `json:"last_error,omitempty"`
}

type qualityTextGap struct {
	DocumentID     int64  `json:"document_id"`
	DocNumber      string `json:"doc_number,omitempty"`
	Title          string `json:"title,omitempty"`
	NeedsReview    bool   `json:"needs_review"`
	Authorities    string `json:"authorities,omitempty"`
	ExtractEngines string `json:"extract_engines,omitempty"`
}

type qualityChunkGap struct {
	ChunkID    int64  `json:"chunk_id"`
	DocumentID int64  `json:"document_id"`
	DocNumber  string `json:"doc_number,omitempty"`
	Location   string `json:"location,omitempty"`
	Sample     string `json:"sample,omitempty"`
}

type qualityValidityGap struct {
	DocumentID  int64  `json:"document_id"`
	DocNumber   string `json:"doc_number,omitempty"`
	Title       string `json:"title,omitempty"`
	StatusClass string `json:"status_class"`
	Chunks      int64  `json:"chunks"`
}

type qualityUnresolvedRelation struct {
	RefKey   string `json:"ref_key"`
	Label    string `json:"label,omitempty"`
	Edges    int64  `json:"edges"`
	FromDocs string `json:"from_docs,omitempty"`
}

type qualityRelationTextGap struct {
	DocumentID int64  `json:"document_id"`
	DocNumber  string `json:"doc_number,omitempty"`
	Title      string `json:"title,omitempty"`
	Edges      int64  `json:"edges"`
	HasAnyText bool   `json:"has_any_text"`
}

const (
	qualityCategoryAll                = "all"
	qualityCategoryFetch              = "fetch"
	qualityCategoryNonBinding         = "non_binding"
	qualityCategoryMojibake           = "mojibake"
	qualityCategoryPartialValidity    = "partial_validity"
	qualityCategoryUnresolvedRelation = "unresolved_relations"
	qualityCategoryRelationTargetText = "relation_target_text"

	defaultQualityGapLimit = 20
	maxQualityGapLimit     = 100
)

func (s *Server) handleQualityGaps(ctx context.Context, _ *mcpsdk.CallToolRequest, in qualityGapsInput) (*mcpsdk.CallToolResult, qualityGapsOutput, error) {
	if s.corpus == nil {
		return nil, qualityGapsOutput{}, fmt.Errorf("corpus database is not configured")
	}
	out, err := s.corpus.QualityGaps(ctx, in)
	if err != nil {
		s.log.Error("mcp: quality_gaps", "err", err)
		return nil, qualityGapsOutput{}, fmt.Errorf("quality gaps: %w", err)
	}
	return nil, out, nil
}

func (c dbCorpus) QualityGaps(ctx context.Context, in qualityGapsInput) (qualityGapsOutput, error) {
	limit := normalizeLimit(in.Limit, defaultQualityGapLimit, maxQualityGapLimit)
	categories, err := qualityCategories(in.Category)
	if err != nil {
		return qualityGapsOutput{}, err
	}
	status, err := c.CorpusStatus(ctx)
	if err != nil {
		return qualityGapsOutput{}, err
	}
	out := qualityGapsOutput{
		Limit:      limit,
		Categories: categories,
		Summary:    status.Gaps,
		Notes: []string{
			"Rows are DB worklists only; do not hardcode these IDs into ingestion or retrieval logic.",
			"Fix the source/config/parser path, rerun the relevant workflow, then re-check this tool.",
		},
	}
	if qualityIncludes(categories, qualityCategoryFetch) {
		out.FetchIncomplete, err = c.qualityFetchIncomplete(ctx, limit)
		if err != nil {
			return qualityGapsOutput{}, err
		}
	}
	if qualityIncludes(categories, qualityCategoryNonBinding) {
		out.NonBindingOnly, err = c.qualityNonBindingOnly(ctx, limit)
		if err != nil {
			return qualityGapsOutput{}, err
		}
	}
	if qualityIncludes(categories, qualityCategoryMojibake) {
		out.MojibakeChunks, err = c.qualityMojibakeChunks(ctx, limit)
		if err != nil {
			return qualityGapsOutput{}, err
		}
	}
	if qualityIncludes(categories, qualityCategoryPartialValidity) {
		out.PartialValidity, err = c.qualityPartialValidity(ctx, limit)
		if err != nil {
			return qualityGapsOutput{}, err
		}
	}
	if qualityIncludes(categories, qualityCategoryUnresolvedRelation) {
		out.UnresolvedRelations, err = c.qualityUnresolvedRelations(ctx, limit)
		if err != nil {
			return qualityGapsOutput{}, err
		}
	}
	if qualityIncludes(categories, qualityCategoryRelationTargetText) {
		out.RelationTargetTextGaps, err = c.qualityRelationTargetTextGaps(ctx, limit)
		if err != nil {
			return qualityGapsOutput{}, err
		}
	}
	return out, nil
}

func qualityCategories(category string) ([]string, error) {
	category = strings.TrimSpace(strings.ToLower(category))
	if category == "" || category == qualityCategoryAll {
		return []string{
			qualityCategoryFetch,
			qualityCategoryNonBinding,
			qualityCategoryMojibake,
			qualityCategoryPartialValidity,
			qualityCategoryUnresolvedRelation,
			qualityCategoryRelationTargetText,
		}, nil
	}
	switch category {
	case qualityCategoryFetch,
		qualityCategoryNonBinding,
		qualityCategoryMojibake,
		qualityCategoryPartialValidity,
		qualityCategoryUnresolvedRelation,
		qualityCategoryRelationTargetText:
		return []string{category}, nil
	default:
		return nil, fmt.Errorf("unknown quality gap category %q", category)
	}
}

func qualityIncludes(categories []string, category string) bool {
	for _, got := range categories {
		if got == category {
			return true
		}
	}
	return false
}

func (c dbCorpus) qualityFetchIncomplete(ctx context.Context, limit int) ([]qualityFetchGap, error) {
	const q = `
WITH artifacts AS (
  SELECT
    fetch_doc_id,
    string_agg(kind || ':' || state, ', ' ORDER BY kind, ref_key) AS artifact_states,
    COALESCE(to_char(min(next_attempt_at) FILTER (WHERE state IN ('pending', 'error', 'claimed')), 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS next_attempt_at,
    COALESCE((array_agg(NULLIF(last_error, '') ORDER BY updated_at DESC) FILTER (WHERE NULLIF(last_error, '') IS NOT NULL))[1], '') AS last_error
  FROM ingest.fetch_artifact
  GROUP BY fetch_doc_id
)
SELECT
  fd.id,
  fd.source,
  fd.external_id,
  fd.state,
  COALESCE(fd.provenance, ''),
  fd.artifacts_expected,
  fd.artifacts_done,
  fd.artifacts_failed,
  COALESCE(sd.doc_number, ''),
  COALESCE(sd.title, ''),
  COALESCE(a.artifact_states, ''),
  COALESCE(a.next_attempt_at, ''),
  COALESCE(a.last_error, '')
FROM ingest.fetch_doc fd
LEFT JOIN bronze.source_document sd ON sd.source=fd.source AND sd.external_id=fd.external_id
LEFT JOIN artifacts a ON a.fetch_doc_id=fd.id
WHERE fd.state <> 'complete'
ORDER BY
  CASE fd.provenance WHEN 'relation' THEN 0 ELSE 1 END,
  fd.source,
  fd.external_id
LIMIT $1`
	rows, err := c.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("query quality fetch gaps: %w", err)
	}
	defer rows.Close()
	var out []qualityFetchGap
	for rows.Next() {
		var row qualityFetchGap
		if err := rows.Scan(
			&row.FetchDocID,
			&row.Source,
			&row.ExternalID,
			&row.State,
			&row.Provenance,
			&row.ArtifactsExpected,
			&row.ArtifactsDone,
			&row.ArtifactsFailed,
			&row.DocNumber,
			&row.Title,
			&row.ArtifactStates,
			&row.NextAttemptAt,
			&row.LastError,
		); err != nil {
			return nil, fmt.Errorf("scan quality fetch gap: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("quality fetch gap rows: %w", err)
	}
	return out, nil
}

func (c dbCorpus) qualityNonBindingOnly(ctx context.Context, limit int) ([]qualityTextGap, error) {
	const q = `
WITH text_state AS (
  SELECT
    d.id,
    COALESCE(d.doc_number, '') AS doc_number,
    COALESCE(d.title, '') AS title,
    bool_or(dt.is_binding AND NULLIF(btrim(COALESCE(dt.markdown, '')), '') IS NOT NULL) AS has_binding,
    bool_or((NOT dt.is_binding) AND NULLIF(btrim(COALESCE(dt.markdown, '')), '') IS NOT NULL) AS has_nonbinding,
    bool_or(dt.needs_review) AS needs_review,
    string_agg(DISTINCT dt.authority, ', ' ORDER BY dt.authority) AS authorities,
    string_agg(DISTINCT COALESCE(dt.extract_engine, ''), ', ' ORDER BY COALESCE(dt.extract_engine, '')) AS extract_engines
  FROM silver.document d
  JOIN silver.document_text dt ON dt.document_id=d.id
  GROUP BY d.id, d.doc_number, d.title
)
SELECT ts.id, ts.doc_number, ts.title, ts.needs_review, COALESCE(ts.authorities, ''), COALESCE(ts.extract_engines, '')
FROM text_state ts
JOIN silver.document d ON d.id=ts.id
WHERE ts.has_nonbinding
  AND NOT ts.has_binding
  AND NOT EXISTS (SELECT 1 FROM gold.chunk c WHERE c.document_id=ts.id)
  AND d.index_class <> 'relation_context'
ORDER BY ts.doc_number, ts.id
LIMIT $1`
	rows, err := c.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("query non-binding text gaps: %w", err)
	}
	defer rows.Close()
	var out []qualityTextGap
	for rows.Next() {
		var row qualityTextGap
		if err := rows.Scan(&row.DocumentID, &row.DocNumber, &row.Title, &row.NeedsReview, &row.Authorities, &row.ExtractEngines); err != nil {
			return nil, fmt.Errorf("scan non-binding text gap: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("non-binding text gap rows: %w", err)
	}
	return out, nil
}

func (c dbCorpus) qualityMojibakeChunks(ctx context.Context, limit int) ([]qualityChunkGap, error) {
	const q = `
SELECT
  c.id,
  c.document_id,
  COALESCE(d.doc_number, ''),
  c.citation,
  left(regexp_replace(c.content, '\s+', ' ', 'g'), 280) AS sample
FROM gold.chunk c
JOIN silver.document d ON d.id=c.document_id
WHERE c.content ~ 'Ã[¡-ÿ]'
   OR c.content LIKE '%�%'
   OR c.content LIKE '%â€%'
   OR c.content ~ '[Ѐ-ӿ]'
   OR c.citation ~ 'Ã[¡-ÿ]'
   OR c.citation LIKE '%�%'
   OR c.citation LIKE '%â€%'
   OR c.citation ~ '[Ѐ-ӿ]'
ORDER BY d.doc_number, c.ordinal, c.id
LIMIT $1`
	rows, err := c.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("query mojibake chunks: %w", err)
	}
	defer rows.Close()
	var out []qualityChunkGap
	for rows.Next() {
		var row qualityChunkGap
		if err := rows.Scan(&row.ChunkID, &row.DocumentID, &row.DocNumber, &row.Location, &row.Sample); err != nil {
			return nil, fmt.Errorf("scan mojibake chunk: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mojibake chunk rows: %w", err)
	}
	return out, nil
}

func (c dbCorpus) qualityPartialValidity(ctx context.Context, limit int) ([]qualityValidityGap, error) {
	const q = `
SELECT
  d.id,
  COALESCE(d.doc_number, ''),
  COALESCE(d.title, ''),
  vp.status_class,
  count(c.id) AS chunks
FROM silver.document d
JOIN silver.validity_period vp
  ON vp.document_id=d.id
 AND vp.section_id IS NULL
 AND vp.superseded_at IS NULL
LEFT JOIN gold.chunk c ON c.document_id=d.id
WHERE vp.status_class='partial'
  AND NOT EXISTS (
    SELECT 1 FROM silver.validity_period svp
    WHERE svp.document_id=d.id
      AND svp.section_id IS NOT NULL
      AND svp.superseded_at IS NULL
  )
GROUP BY d.id, d.doc_number, d.title, vp.status_class
ORDER BY chunks DESC, d.doc_number, d.id
LIMIT $1`
	rows, err := c.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("query partial validity gaps: %w", err)
	}
	defer rows.Close()
	var out []qualityValidityGap
	for rows.Next() {
		var row qualityValidityGap
		if err := rows.Scan(&row.DocumentID, &row.DocNumber, &row.Title, &row.StatusClass, &row.Chunks); err != nil {
			return nil, fmt.Errorf("scan partial validity gap: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("partial validity gap rows: %w", err)
	}
	return out, nil
}

func (c dbCorpus) qualityUnresolvedRelations(ctx context.Context, limit int) ([]qualityUnresolvedRelation, error) {
	const q = `
SELECT
  ref.ref_key,
  COALESCE(ref.label, ''),
  count(dr.id) AS edges,
  string_agg(DISTINCT COALESCE(d.doc_number, ''), ', ' ORDER BY COALESCE(d.doc_number, '')) AS from_docs
FROM silver.doc_ref ref
JOIN silver.document_relation dr ON dr.to_ref_id=ref.id
JOIN silver.document d ON d.id=dr.from_document_id
WHERE ref.document_id IS NULL
GROUP BY ref.id, ref.ref_key, ref.label
ORDER BY edges DESC, ref.ref_key
LIMIT $1`
	rows, err := c.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("query unresolved relations: %w", err)
	}
	defer rows.Close()
	var out []qualityUnresolvedRelation
	for rows.Next() {
		var row qualityUnresolvedRelation
		if err := rows.Scan(&row.RefKey, &row.Label, &row.Edges, &row.FromDocs); err != nil {
			return nil, fmt.Errorf("scan unresolved relation: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("unresolved relation rows: %w", err)
	}
	return out, nil
}

func (c dbCorpus) qualityRelationTargetTextGaps(ctx context.Context, limit int) ([]qualityRelationTextGap, error) {
	const q = `
WITH targets AS (
  SELECT ref.document_id, count(dr.id) AS edges
  FROM silver.document_relation dr
  JOIN silver.doc_ref ref ON ref.id=dr.to_ref_id
  WHERE ref.document_id IS NOT NULL
  GROUP BY ref.document_id
)
SELECT
  d.id,
  COALESCE(d.doc_number, ''),
  COALESCE(d.title, ''),
  targets.edges,
  EXISTS (
    SELECT 1 FROM silver.document_text dt
    WHERE dt.document_id=d.id
      AND NULLIF(btrim(COALESCE(dt.markdown, '')), '') IS NOT NULL
  ) AS has_text
FROM targets
JOIN silver.document d ON d.id=targets.document_id
WHERE NOT EXISTS (
    SELECT 1 FROM gold.chunk c
    WHERE c.document_id=targets.document_id
  )
  OR NOT EXISTS (
    SELECT 1 FROM silver.document_text dt
    WHERE dt.document_id=targets.document_id
      AND dt.is_binding
      AND NULLIF(btrim(COALESCE(dt.markdown, '')), '') IS NOT NULL
  )
ORDER BY targets.edges DESC, d.doc_number, d.id
LIMIT $1`
	rows, err := c.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("query relation target text gaps: %w", err)
	}
	defer rows.Close()
	var out []qualityRelationTextGap
	for rows.Next() {
		var row qualityRelationTextGap
		if err := rows.Scan(&row.DocumentID, &row.DocNumber, &row.Title, &row.Edges, &row.HasAnyText); err != nil {
			return nil, fmt.Errorf("scan relation target text gap: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("relation target text gap rows: %w", err)
	}
	return out, nil
}

func (c dbCorpus) fetchStats(ctx context.Context) (corpusFetchStats, error) {
	const summaryQ = `
SELECT
  count(*) AS total,
  count(*) FILTER (WHERE state='complete') AS complete,
  count(*) FILTER (WHERE state='partial') AS partial,
  count(*) FILTER (WHERE state='error') AS error,
  count(*) FILTER (WHERE state <> 'complete') AS not_complete,
  (SELECT count(*) FROM ingest.fetch_artifact WHERE state='error') AS artifacts_error,
  (SELECT count(*) FROM ingest.fetch_artifact WHERE state='dead') AS artifacts_dead
FROM ingest.fetch_doc`
	var out corpusFetchStats
	if err := c.pool.QueryRow(ctx, summaryQ).Scan(
		&out.Total,
		&out.Complete,
		&out.Partial,
		&out.Error,
		&out.NotComplete,
		&out.ArtifactsError,
		&out.ArtifactsDead,
	); err != nil {
		return corpusFetchStats{}, fmt.Errorf("query fetch status: %w", err)
	}

	const sourceQ = `
SELECT
  source,
  count(*) AS total,
  count(*) FILTER (WHERE state='complete') AS complete,
  count(*) FILTER (WHERE state='partial') AS partial,
  count(*) FILTER (WHERE state='error') AS error,
  count(*) FILTER (WHERE state <> 'complete') AS not_complete
FROM ingest.fetch_doc
GROUP BY source
ORDER BY source`
	rows, err := c.pool.Query(ctx, sourceQ)
	if err != nil {
		return corpusFetchStats{}, fmt.Errorf("query fetch status by source: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row sourceFetchStats
		if err := rows.Scan(&row.Source, &row.Total, &row.Complete, &row.Partial, &row.Error, &row.NotComplete); err != nil {
			return corpusFetchStats{}, fmt.Errorf("scan fetch status by source: %w", err)
		}
		out.BySource = append(out.BySource, row)
	}
	if err := rows.Err(); err != nil {
		return corpusFetchStats{}, fmt.Errorf("fetch status by source rows: %w", err)
	}
	return out, nil
}

func corpusStatusNotes(out corpusStatusOutput) []string {
	var notes []string
	if !out.SearchReady {
		notes = append(notes, "gold.chunk is empty; run ingest/extract/normalize/index before using search.")
	}
	if out.Chunks.Total > 0 && out.Chunks.ConfiguredEmbeddings < out.Chunks.Total {
		notes = append(notes, "some chunks do not have the configured embedding model; vector recall may be incomplete.")
	}
	if out.Docs.NonBindingOnly > 0 {
		notes = append(notes, "some documents have only non-binding OCR/review text and are intentionally not indexed.")
	}
	if out.Docs.RelationContext > 0 {
		notes = append(notes, "some relation-pulled documents are outside the configured scope and intentionally not indexed; their text and relations are still served by the document tool.")
	}
	if out.Docs.IndexedNoBinding > 0 {
		notes = append(notes, "some indexed documents carry only non-binding OCR/transcription text; every hit from them is badged non-binding/needs-review — verify wording against the official source link.")
	}
	if out.Gaps.DocsWithoutCurrentValidity > 0 {
		notes = append(notes, "some documents have no current document-level validity row.")
	}
	if out.Relations.StubTargets > 0 || out.Relations.TargetTextGaps > 0 {
		notes = append(notes, "some confirmed relation targets are unresolved or have no indexed binding chunks.")
	}
	if out.Fetch.NotComplete > 0 || out.Fetch.ArtifactsError > 0 || out.Fetch.ArtifactsDead > 0 {
		notes = append(notes, "some fetch rows or artifacts are not complete; inspect fetch.by_source and fetch artifact counts.")
	}
	if out.Gaps.MojibakeChunks > 0 {
		notes = append(notes, "some indexed chunks still contain mojibake-like text and need source review.")
	}
	return notes
}

// --- document ------------------------------------------------------------------

type documentInput struct {
	DocumentID int64    `json:"document_id,omitempty" jsonschema:"silver.document id; use this when search returned document_id"`
	DocNumber  string   `json:"doc_number,omitempty" jsonschema:"document number / identifier exactly as printed by the official source (use the doc_number returned by search)"`
	Citation   string   `json:"citation,omitempty" jsonschema:"filter chunks by provision position, in the corpus's citation vocabulary — copy it from a search hit's location or parent_citation"`
	Include    []string `json:"include,omitempty" jsonschema:"response sections to return — any of: chunks (provision text), relations (confirmed graph edges), amendments (verbatim incoming amendment clauses), timeline (chronological history), provenance (per-artifact extraction rows). Omitted = chunks + relations + amendments + timeline. Reading one provision? Pass include=['chunks'] with a citation filter — the cheapest call. Document metadata, validity, sources, text_summary, and gaps are always returned."`
	Limit      int      `json:"limit,omitempty" jsonschema:"maximum chunks to return (0 = default)"`
	Offset     int      `json:"offset,omitempty" jsonschema:"offset to page through more chunks"`
}

// documentSections is the set of optional response sections the document tool
// recognizes. Metadata, validity, sources, text_summary, and gaps are always
// returned and are not listed here.
var documentSections = map[string]bool{
	"chunks":     true,
	"relations":  true,
	"amendments": true,
	"timeline":   true,
	"provenance": true,
}

// documentIncludes resolves the include parameter to the set of sections to
// return. Omitted/empty means the default set — every section except provenance
// (per-artifact extraction rows with hashes: diagnostic detail most agents never
// need). Unknown names are dropped, never an error (a typo must not fail a legal
// query); the handler logs them.
func documentIncludes(include []string) map[string]bool {
	if len(include) == 0 {
		return map[string]bool{"chunks": true, "relations": true, "amendments": true, "timeline": true}
	}
	m := make(map[string]bool, len(include))
	for _, name := range include {
		if n := strings.ToLower(strings.TrimSpace(name)); documentSections[n] {
			m[n] = true
		}
	}
	return m
}

type documentMeta struct {
	DocumentID  int64            `json:"document_id"`
	DocNumber   string           `json:"doc_number,omitempty"`
	Title       string           `json:"title,omitempty"`
	DocType     string           `json:"doc_type,omitempty"`
	Issuer      string           `json:"issuer,omitempty"`
	Signer      string           `json:"signer,omitempty"`
	IssuedDate  string           `json:"issued_date,omitempty"`
	StatusClass string           `json:"status_class,omitempty"`
	Source      string           `json:"source,omitempty" jsonschema:"official source site code — corpus_status lists this corpus's sources"`
	SourceURL   string           `json:"source_url,omitempty" jsonschema:"official source landing page for this document (view on source); a citable page, never a file download"`
	Cite        string           `json:"cite,omitempty" jsonschema:"ready-to-paste citation: document number + validity + source link"`
	Validity    validityEvidence `json:"validity"`
}

// docSource is one official site where this document is published, with its
// landing-page URL (never a file download).
type docSource struct {
	Source   string `json:"source"`
	URL      string `json:"url"`
	FilesURL string `json:"files_url,omitempty" jsonschema:"stable official endpoint listing this document's files; each GET returns fresh short-lived direct download links (vbpl: ~24h presigned, reachable from Vietnamese IPs only)"`
}

// documentFile is one downloadable artifact banhmi observed for this document,
// merged across sibling sources by content hash. origin_urls carries every
// durable direct download link an official source serves for these exact bytes;
// expiring presigned links are never emitted — for those sources the matching
// sources[] entry carries a files_url listing that mints fresh ones.
type documentFile struct {
	Label      string          `json:"label,omitempty" jsonschema:"source-provided file name"`
	Kind       string          `json:"kind,omitempty" jsonschema:"artifact role: main, appendix, attachment, version_snapshot, or original_scan"`
	Format     string          `json:"format,omitempty"`
	ByteSize   int64           `json:"byte_size,omitempty"`
	SHA256     string          `json:"sha256,omitempty" jsonschema:"content hash of the archived bytes; matches text_provenance source_file_sha256, so a download is verifiable against the extracted text"`
	OriginURLs []docFileOrigin `json:"origin_urls,omitempty" jsonschema:"direct official download links serving these exact bytes; absent when no durable official link exists (reachability may vary by source)"`
}

// docFileOrigin is one durable official download link for a file.
type docFileOrigin struct {
	Source string `json:"source"`
	URL    string `json:"url"`
}

type documentChunk struct {
	ChunkID        int64            `json:"chunk_id"`
	SectionID      int64            `json:"section_id,omitempty"`
	Location       string           `json:"location"`
	ContextPrefix  string           `json:"context_prefix,omitempty"`
	Content        string           `json:"content"`
	Ordinal        int32            `json:"ordinal"`
	Validity       validityEvidence `json:"validity"`
	TextProvenance textProvenance   `json:"text_provenance"`
}

type documentValidityPeriod struct {
	ValidityID    int64  `json:"validity_id"`
	SectionID     int64  `json:"section_id,omitempty"`
	Location      string `json:"location,omitempty"`
	StatusCode    string `json:"status_code,omitempty"`
	StatusClass   string `json:"status_class,omitempty"`
	EffectiveFrom string `json:"effective_from,omitempty"`
	EffectiveTo   string `json:"effective_to,omitempty"`
	Source        string `json:"source,omitempty"`
	Reason        string `json:"reason,omitempty"`
	ObservedAt    string `json:"observed_at,omitempty"`
}

type documentTextEvidence struct {
	TextID            int64   `json:"text_id"`
	Authority         string  `json:"authority"`
	Source            string  `json:"source,omitempty"`
	RawFileID         int64   `json:"raw_file_id,omitempty"`
	HasText           bool    `json:"has_text"`
	IsBinding         bool    `json:"is_binding"`
	NeedsReview       bool    `json:"needs_review"`
	ExtractEngine     string  `json:"extract_engine,omitempty"`
	ExtractConfidence float64 `json:"extract_confidence,omitempty"`
	SourceFileSHA256  string  `json:"source_file_sha256,omitempty"`
	VerbatimSHA256    string  `json:"verbatim_sha256,omitempty"`
}

type documentOutput struct {
	Found    bool         `json:"found"`
	Document documentMeta `json:"document,omitempty"`
	// AlsoMatches lists OTHER documents sharing the requested số ký hiệu —
	// distinct documents can share a number (e.g. Luật and Nghị quyết
	// 51/2005/QH11). Re-query by document_id to open one of them.
	AlsoMatches        []docAlternative         `json:"also_matches,omitempty" jsonschema:"other distinct documents carrying the same document number; re-query by document_id to open one"`
	Sources            []docSource              `json:"sources,omitempty" jsonschema:"all official sources where this document is published (view-on-source links); never file downloads"`
	Files              []documentFile           `json:"files,omitempty" jsonschema:"downloadable official files observed for this document, one entry per unique content hash across sources"`
	ValidityPeriods    []documentValidityPeriod `json:"validity_periods,omitempty"`
	TextProvenance     []documentTextEvidence   `json:"text_provenance,omitempty"`
	TextSummary        textProvenance           `json:"text_summary"`
	Chunks             []documentChunk          `json:"chunks"`
	Relations          []searchRelation         `json:"relations,omitempty"`
	IncomingAmendments []amendmentClause        `json:"incoming_amendments,omitempty"`
	AmendmentChain     []chainNode              `json:"amendment_chain,omitempty" jsonschema:"transitive amendment lineage — who amends this document (depth 1), who amends those amenders (depth 2+). Present only when an amender is itself amended; the deepest/last node is the newest treatment: read it before relying on any amender's text"`
	Timeline           []timelineEvent          `json:"timeline,omitempty" jsonschema:"chronological history: issued → effective → amended/replaced → expired, from validity + confirmed relations"`
	Gaps               []gap                    `json:"gaps,omitempty"`
	Limit              int                      `json:"limit"`
	Offset             int                      `json:"offset"`
	NextOffset         int                      `json:"next_offset,omitempty"`
}

const (
	defaultDocumentChunkLimit = 20
	maxDocumentChunkLimit     = 50
	documentRelationLimit     = 20
)

func (s *Server) handleDocument(ctx context.Context, _ *mcpsdk.CallToolRequest, in documentInput) (*mcpsdk.CallToolResult, documentOutput, error) {
	if s.corpus == nil {
		return nil, documentOutput{}, fmt.Errorf("corpus database is not configured")
	}
	if in.DocumentID == 0 && strings.TrimSpace(in.DocNumber) == "" {
		return nil, documentOutput{}, fmt.Errorf("document_id or doc_number is required")
	}
	for _, name := range in.Include {
		if n := strings.ToLower(strings.TrimSpace(name)); n != "" && !documentSections[n] {
			s.log.Warn("mcp: unknown document include section, ignoring", "section", name)
		}
	}
	out, err := s.corpus.Document(ctx, in)
	if err != nil {
		s.log.Error("mcp: document", "err", err)
		return nil, documentOutput{}, fmt.Errorf("document: %w", err)
	}
	return nil, out, nil
}

func (c dbCorpus) Document(ctx context.Context, in documentInput) (documentOutput, error) {
	limit := normalizeLimit(in.Limit, defaultDocumentChunkLimit, maxDocumentChunkLimit)
	offset := in.Offset
	if offset < 0 {
		offset = 0
	}

	doc, found, err := c.findDocument(ctx, in.DocumentID, in.DocNumber)
	if err != nil {
		return documentOutput{}, err
	}
	out := documentOutput{
		Found:  found,
		Chunks: []documentChunk{},
		Limit:  limit,
		Offset: offset,
	}
	if !found {
		out.Gaps = []gap{{
			Kind:         string(retrieve.GapNoEvidence),
			Message:      "document not found by id or document number",
			BlocksAnswer: true,
		}}
		return out, nil
	}
	out.Document = doc

	alts, err := c.documentAlternatives(ctx, doc.DocumentID, in.DocNumber)
	if err != nil {
		return documentOutput{}, err
	}
	out.AlsoMatches = alts

	sources, err := c.documentSources(ctx, doc.DocumentID)
	if err != nil {
		return documentOutput{}, err
	}
	out.Sources = sources

	files, err := c.documentFiles(ctx, doc.DocumentID)
	if err != nil {
		return documentOutput{}, err
	}
	out.Files = files

	inc := documentIncludes(in.Include)

	// The provenance summary is always loaded — gap detection and chunk shaping
	// need it — but the per-artifact rows are emitted only on request.
	texts, textSummary, err := c.documentTextRows(ctx, doc.DocumentID)
	if err != nil {
		return documentOutput{}, err
	}
	if inc["provenance"] {
		out.TextProvenance = texts
	}
	out.TextSummary = textSummary

	validityPeriods, err := c.documentValidityPeriods(ctx, doc.DocumentID)
	if err != nil {
		return documentOutput{}, err
	}
	out.ValidityPeriods = validityPeriods

	indexed, err := c.documentIndexed(ctx, doc.DocumentID)
	if err != nil {
		return documentOutput{}, err
	}
	out.Gaps = documentGaps(doc, documentTextState{
		Indexed:       indexed,
		HasBinding:    textSummary.HasBindingText,
		HasNonBinding: textSummary.HasNonBindingText,
		NeedsReview:   textSummary.NeedsReview,
	}, hasSectionValidity(validityPeriods))

	if inc["chunks"] {
		chunks, err := c.documentChunks(ctx, doc.DocumentID, in.Citation, limit, offset, textSummary)
		if err != nil {
			return documentOutput{}, err
		}
		out.Chunks = chunks
		if len(chunks) == limit {
			out.NextOffset = offset + limit
		}
		if len(chunks) == 0 {
			out.Gaps = append(out.Gaps, gap{
				Kind:         string(retrieve.GapNoEvidence),
				Message:      documentChunkGapMessage(in.Citation, offset),
				BlocksAnswer: true,
				DocumentID:   doc.DocumentID,
				DocNumber:    doc.DocNumber,
				Title:        doc.Title,
			})
		}
	}

	if inc["relations"] {
		relations, err := c.documentRelations(ctx, doc.DocumentID)
		if err != nil {
			return documentOutput{}, err
		}
		relations, err = retrieve.AttachTargetAmenders(ctx, c.pool, doc.DocumentID, relations)
		if err != nil {
			return documentOutput{}, err
		}
		out.Relations = toSearchRelations(relations, true)
	}

	// The timeline folds in amendment events, so amendments load when either
	// section is requested; each section is emitted only per its own flag. The
	// amended-by gap is emitted whenever the clauses were loaded — a currency
	// red flag a narrower response must not hide.
	if inc["amendments"] || inc["timeline"] {
		amendments, err := c.incomingAmendments(ctx, doc.DocumentID)
		if err != nil {
			return documentOutput{}, err
		}
		if inc["amendments"] {
			out.IncomingAmendments = amendments
			chain, err := c.amendmentChain(ctx, doc.DocumentID)
			if err != nil {
				return documentOutput{}, err
			}
			// Depth-1-only lineage is already visible in relations and
			// incoming_amendments; the chain earns its tokens only when an
			// amender is itself amended.
			if chainMaxDepth(chain) >= 2 {
				out.AmendmentChain = chain
				newest := chain[len(chain)-1]
				out.Gaps = append(out.Gaps, gap{
					Kind:       "amendment_chain",
					Message:    fmt.Sprintf("a document amending this one has itself been amended; follow amendment_chain to the newest treatment (%s) before relying on any amender's text", newest.DocNumber),
					DocumentID: doc.DocumentID,
					DocNumber:  doc.DocNumber,
				})
			}
		}
		if inc["timeline"] {
			out.Timeline = buildTimeline(doc, validityPeriods, amendments)
		}
		if len(amendments) > 0 {
			out.Gaps = append(out.Gaps, gap{
				Kind:       "incoming_amendment",
				Message:    "this document is amended/replaced by other documents; read incoming_amendments (verbatim clauses + positions) to decide which provisions changed",
				DocumentID: doc.DocumentID,
				DocNumber:  doc.DocNumber,
			})
		}
	}
	return out, nil
}

// amendmentClause is one raw amendment instruction from a document that amends or
// replaces the queried document. banhmi does not interpret it: it surfaces the
// verbatim text and its position so the user's model decides what changed. The
// lead-verb vocabulary that detects these clauses is config-seeded, never hardcoded.
type amendmentClause struct {
	AmendingDoc           string `json:"amending_doc"`                      // số ký hiệu of the amending/replacing document
	AmendingEffectiveFrom string `json:"amending_effective_from,omitempty"` // when the amending document took effect
	RelationType          string `json:"relation_type"`                     // amends_supplements | replaces
	Position              string `json:"position"`                          // where in the amending document, e.g. "Điều 1, Khoản 2"
	Text                  string `json:"text"`                              // verbatim amendment instruction
}

const incomingAmendmentLimit = 100

// incomingAmendments returns the raw amendment-instruction clauses of confirmed
// amends/replaces relations that target docID. The lead-verb vocabulary comes from
// config (amendment.lead_verbs). It does not parse which provision is affected — the
// agent reads the verbatim text + position and decides (handles cross-doc references,
// additions, and phrase substitutions that a parser would get wrong).
func (c dbCorpus) incomingAmendments(ctx context.Context, docID int64) ([]amendmentClause, error) {
	const q = `
WITH verbs AS (
  SELECT lower(btrim(v)) || '%' AS p
  FROM config.setting, unnest(string_to_array(value, ',')) AS v
  WHERE key = 'amendment.lead_verbs'
)
SELECT DISTINCT a.doc_number,
       COALESCE(to_char(vp.eff_from, 'YYYY-MM-DD'), ''),
       sec.citation_path,
       btrim(sec.content),
       dr.relation_type
FROM silver.doc_ref ref
JOIN silver.document_relation dr ON dr.to_ref_id = ref.id
  AND dr.relation_type IN (SELECT label FROM config.relation_type WHERE is_amending)
JOIN silver.document a ON a.id = dr.from_document_id
JOIN silver.document_section sec ON sec.document_id = a.id
LEFT JOIN LATERAL (
  SELECT eff_from FROM silver.validity_period x
  WHERE x.document_id = a.id AND x.section_id IS NULL AND x.superseded_at IS NULL
  ORDER BY x.observed_at DESC, x.id DESC LIMIT 1
) vp ON true
WHERE ref.document_id = $1
  AND a.doc_number IS NOT NULL
  AND EXISTS (SELECT 1 FROM verbs WHERE lower(btrim(sec.content)) LIKE verbs.p)
ORDER BY a.doc_number, sec.citation_path
LIMIT $2`
	rows, err := c.pool.Query(ctx, q, docID, incomingAmendmentLimit)
	if err != nil {
		return nil, fmt.Errorf("incoming amendments: %w", err)
	}
	defer rows.Close()
	var out []amendmentClause
	for rows.Next() {
		var ac amendmentClause
		var path string
		if err := rows.Scan(&ac.AmendingDoc, &ac.AmendingEffectiveFrom, &path, &ac.Text, &ac.RelationType); err != nil {
			return nil, fmt.Errorf("scan amendment clause: %w", err)
		}
		ac.Position = pathToCitation(path)
		out = append(out, ac)
	}
	return out, rows.Err()
}

// chainNode is one document in a transitive amendment lineage. Depth 1 amends the
// base document directly; depth 2 amends a depth-1 amender, and so on. banhmi does
// not interpret what changed — the chain is metadata telling the agent which
// documents to read for the current treatment.
type chainNode struct {
	Depth         int    `json:"depth" jsonschema:"1 = amends the base document directly; 2+ = amends a shallower amender"`
	DocumentID    int64  `json:"document_id"`
	DocNumber     string `json:"doc_number,omitempty"`
	Title         string `json:"title,omitempty"`
	RelationType  string `json:"relation_type" jsonschema:"how this node treats the document one level shallower (amends_supplements, replaces, amends, revokes, ...)"`
	EffectiveFrom string `json:"effective_from,omitempty" jsonschema:"when this amender took effect, YYYY-MM-DD"`
	StatusClass   string `json:"status_class,omitempty"`
	StatusLabel   string `json:"status_label,omitempty" jsonschema:"plain-English validity badge of the amender itself"`
	Indexed       bool   `json:"indexed" jsonschema:"true when this amender's text is in the corpus (open it with the document tool)"`
}

// amendmentChainDepthCap bounds the recursive lineage walk. The deepest chain
// observed in the VN corpus is 3 hops; 4 leaves headroom without letting a data
// error walk far.
const amendmentChainDepthCap = 4

// amendmentChain walks the transitive incoming amendment lineage of a document:
// who amends/replaces it, who amends those amenders, and so on, following only
// relation types flagged is_amending in config (jurisdiction-neutral). Cycle-safe
// (path guard) and depth-capped. Ordered by depth, then the amender's effective
// date, so the deepest / newest treatment reads last.
func (c dbCorpus) amendmentChain(ctx context.Context, docID int64) ([]chainNode, error) {
	const q = `
WITH RECURSIVE amending_labels AS (
    SELECT DISTINCT label FROM config.relation_type WHERE is_amending
),
chain AS (
    SELECT dr.from_document_id AS amender_id,
           dr.relation_type,
           1 AS depth,
           ARRAY[dr.from_document_id] AS path
    FROM silver.doc_ref ref
    JOIN silver.document_relation dr ON dr.to_ref_id = ref.id
    WHERE ref.document_id = $1
      AND dr.from_document_id <> $1
      AND dr.relation_type IN (SELECT label FROM amending_labels)

    UNION ALL

    SELECT dr.from_document_id,
           dr.relation_type,
           ch.depth + 1,
           ch.path || dr.from_document_id
    FROM chain ch
    JOIN silver.doc_ref ref ON ref.document_id = ch.amender_id
    JOIN silver.document_relation dr ON dr.to_ref_id = ref.id
    WHERE dr.relation_type IN (SELECT label FROM amending_labels)
      AND ch.depth < $2
      AND dr.from_document_id <> ALL(ch.path)
      AND dr.from_document_id <> $1
),
current_validity AS (
    SELECT DISTINCT ON (document_id) document_id, status_class, eff_from
    FROM silver.validity_period
    WHERE superseded_at IS NULL AND section_id IS NULL
    ORDER BY document_id, observed_at DESC, id DESC
)
SELECT DISTINCT
       ch.depth,
       d.id,
       COALESCE(d.doc_number, ''),
       COALESCE(d.title, ''),
       ch.relation_type,
       COALESCE(to_char(cv.eff_from, 'YYYY-MM-DD'), ''),
       COALESCE(cv.status_class, ''),
       EXISTS (SELECT 1 FROM gold.chunk gc WHERE gc.document_id = d.id)
FROM chain ch
JOIN silver.document d ON d.id = ch.amender_id
LEFT JOIN current_validity cv ON cv.document_id = d.id
ORDER BY ch.depth, 6, d.id`
	rows, err := c.pool.Query(ctx, q, docID, amendmentChainDepthCap)
	if err != nil {
		return nil, fmt.Errorf("amendment chain: %w", err)
	}
	defer rows.Close()
	var out []chainNode
	for rows.Next() {
		var n chainNode
		if err := rows.Scan(&n.Depth, &n.DocumentID, &n.DocNumber, &n.Title, &n.RelationType,
			&n.EffectiveFrom, &n.StatusClass, &n.Indexed); err != nil {
			return nil, fmt.Errorf("scan chain node: %w", err)
		}
		n.StatusLabel = statusLabel(n.StatusClass)
		out = append(out, n)
	}
	return out, rows.Err()
}

// chainMaxDepth returns the deepest level present in a lineage.
func chainMaxDepth(chain []chainNode) int {
	maxDepth := 0
	for _, n := range chain {
		if n.Depth > maxDepth {
			maxDepth = n.Depth
		}
	}
	return maxDepth
}

// timelineEvent is one dated entry in a document's lifecycle, assembled from its
// validity periods and confirmed incoming amend/replace relations. banhmi does not
// interpret legal effect — it orders the evidence chronologically for the agent.
type timelineEvent struct {
	Date         string `json:"date,omitempty"`          // YYYY-MM-DD ("" = date unknown)
	Event        string `json:"event"`                   // issued | effective | amended | replaced | expired
	RelationType string `json:"relation_type,omitempty"` // for amended/replaced events
	Doc          string `json:"doc,omitempty"`           // số ký hiệu of the related document
	Note         string `json:"note,omitempty"`          // e.g. validity status label
}

// buildTimeline assembles a chronological lifecycle from data already loaded for the
// document: its issue date, document-level validity periods, and the confirmed
// incoming amend/replace clauses (one event per amending document — the verbatim
// clauses stay in incoming_amendments). Events with an unknown date sort last.
func buildTimeline(doc documentMeta, periods []documentValidityPeriod, amendments []amendmentClause) []timelineEvent {
	var ev []timelineEvent
	if doc.IssuedDate != "" {
		ev = append(ev, timelineEvent{Date: doc.IssuedDate, Event: "issued", Doc: doc.DocNumber})
	}
	for _, p := range periods {
		if p.SectionID != 0 {
			continue // document-level lifecycle only
		}
		if p.EffectiveFrom != "" {
			ev = append(ev, timelineEvent{Date: p.EffectiveFrom, Event: "effective", Note: statusLabel(p.StatusClass)})
		}
		if p.EffectiveTo != "" {
			ev = append(ev, timelineEvent{Date: p.EffectiveTo, Event: "expired", Note: statusLabel(p.StatusClass)})
		}
	}
	seen := make(map[string]bool, len(amendments))
	for _, a := range amendments {
		if a.AmendingDoc == "" || seen[a.AmendingDoc] {
			continue
		}
		seen[a.AmendingDoc] = true
		event := "amended"
		if a.RelationType == "replaces" {
			event = "replaced"
		}
		ev = append(ev, timelineEvent{
			Date:         a.AmendingEffectiveFrom,
			Event:        event,
			RelationType: a.RelationType,
			Doc:          a.AmendingDoc,
		})
	}
	sort.SliceStable(ev, func(i, j int) bool {
		di, dj := ev[i].Date, ev[j].Date
		if (di == "") != (dj == "") {
			return dj == "" // known dates before unknown
		}
		return di < dj
	})
	return ev
}

// pathToCitation renders a section citation_path (e.g. "dieu-1/khoan-2/diem-a") as a
// human/agent-facing citation ("Điều 1, Khoản 2, điểm a").
func pathToCitation(path string) string {
	parts := strings.Split(path, "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		seg := strings.SplitN(p, "-", 2)
		if len(seg) != 2 {
			out = append(out, p)
			continue
		}
		switch seg[0] {
		// VN provision kinds
		case "phan":
			out = append(out, "Phần "+seg[1])
		case "chuong":
			out = append(out, "Chương "+seg[1])
		case "muc":
			out = append(out, "Mục "+seg[1])
		case "dieu":
			out = append(out, "Điều "+seg[1])
		case "khoan":
			out = append(out, "Khoản "+seg[1])
		case "diem":
			out = append(out, "điểm "+seg[1])
		// ID provision kinds
		case "bab":
			out = append(out, "BAB "+seg[1])
		case "bagian":
			out = append(out, "Bagian "+seg[1])
		case "paragraf":
			out = append(out, "Paragraf "+seg[1])
		case "pasal":
			out = append(out, "Pasal "+seg[1])
		case "ayat":
			out = append(out, "ayat ("+seg[1]+")")
		case "huruf":
			out = append(out, "huruf "+seg[1])
		case "penjelasan":
			out = append(out, "Penjelasan "+seg[1])
		default:
			out = append(out, p)
		}
	}
	return strings.Join(out, ", ")
}

func normalizeLimit(got, def, max int) int {
	if got <= 0 {
		return def
	}
	if got > max {
		return max
	}
	return got
}

// docAlternative is another distinct document carrying the same số ký hiệu.
type docAlternative struct {
	DocumentID  int64  `json:"document_id"`
	DocNumber   string `json:"doc_number,omitempty"`
	DocType     string `json:"doc_type,omitempty"`
	Title       string `json:"title,omitempty"`
	StatusClass string `json:"status_class,omitempty"`
	Indexed     bool   `json:"indexed"`
}

// documentAlternatives lists other documents sharing the requested số ký hiệu
// so an ambiguous bare-number lookup is disclosed rather than silently resolved.
func (c dbCorpus) documentAlternatives(ctx context.Context, chosenID int64, soKyHieu string) ([]docAlternative, error) {
	soKyHieu = strings.ToLower(strings.TrimSpace(soKyHieu))
	if soKyHieu == "" {
		return nil, nil
	}
	const q = `
SELECT d.id, COALESCE(d.doc_number,''), COALESCE(d.doc_type,''), COALESCE(d.title,''),
       COALESCE((
         SELECT vp.status_class FROM silver.validity_period vp
         WHERE vp.document_id=d.id AND vp.section_id IS NULL AND vp.superseded_at IS NULL
         ORDER BY vp.observed_at DESC, vp.id DESC LIMIT 1
       ), '') AS status_class,
       EXISTS (SELECT 1 FROM gold.chunk ch WHERE ch.document_id=d.id) AS indexed
FROM silver.document d
WHERE d.id <> $1
  AND (lower(COALESCE(d.doc_number,''))=$2 OR lower(COALESCE(d.doc_number_norm,''))=$2 OR lower(d.doc_key)=$2)
ORDER BY d.id
LIMIT 5`
	rows, err := c.pool.Query(ctx, q, chosenID, soKyHieu)
	if err != nil {
		return nil, fmt.Errorf("document alternatives: %w", err)
	}
	defer rows.Close()
	var out []docAlternative
	for rows.Next() {
		var a docAlternative
		if err := rows.Scan(&a.DocumentID, &a.DocNumber, &a.DocType, &a.Title, &a.StatusClass, &a.Indexed); err != nil {
			return nil, fmt.Errorf("scan document alternative: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("document alternative rows: %w", err)
	}
	return out, nil
}

func (c dbCorpus) findDocument(ctx context.Context, id int64, soKyHieu string) (documentMeta, bool, error) {
	soKyHieu = strings.ToLower(strings.TrimSpace(soKyHieu))
	const q = `
WITH current_validity AS (
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
)
SELECT
  d.id,
  COALESCE(d.doc_number, ''),
  COALESCE(d.title, ''),
  COALESCE(d.doc_type, ''),
  COALESCE(d.issuer, ''),
  COALESCE(d.signer, ''),
  COALESCE(to_char(d.issued_at, 'YYYY-MM-DD'), ''),
  COALESCE(cv.status_class, ''),
  COALESCE(cv.status_code, ''),
  COALESCE(cv.status_class, ''),
  COALESCE(to_char(cv.eff_from, 'YYYY-MM-DD'), ''),
  COALESCE(to_char(cv.eff_to, 'YYYY-MM-DD'), ''),
  COALESCE(cv.source, ''),
  COALESCE(cv.reason, ''),
  COALESCE(sd.source, ''),
  COALESCE(sd.detail_url, '')
FROM silver.document d
LEFT JOIN current_validity cv ON cv.document_id=d.id
LEFT JOIN bronze.source_document sd ON sd.id = d.source_document_id
WHERE ($1::bigint > 0 AND d.id=$1)
   OR ($2::text <> '' AND (
      lower(COALESCE(d.doc_number, ''))=$2
      OR lower(COALESCE(d.doc_number_norm, ''))=$2
      OR lower(d.doc_key)=$2
   ))
ORDER BY
  CASE WHEN $1::bigint > 0 AND d.id=$1 THEN 0 ELSE 1 END,
  CASE WHEN $2::text <> '' AND lower(COALESCE(d.doc_number, ''))=$2 THEN 0 ELSE 1 END,
  -- Distinct documents can share a số ký hiệu (Luật vs Nghị quyết 51/2005/QH11).
  -- Prefer the searchable-corpus document; alternatives are disclosed via
  -- also_matches so the agent can re-query by document_id.
  CASE WHEN d.index_class = 'primary' THEN 0 ELSE 1 END,
  CASE WHEN EXISTS (SELECT 1 FROM gold.chunk ch WHERE ch.document_id=d.id) THEN 0 ELSE 1 END,
  d.id
LIMIT 1`
	var doc documentMeta
	err := c.pool.QueryRow(ctx, q, id, soKyHieu).Scan(
		&doc.DocumentID,
		&doc.DocNumber,
		&doc.Title,
		&doc.DocType,
		&doc.Issuer,
		&doc.Signer,
		&doc.IssuedDate,
		&doc.StatusClass,
		&doc.Validity.StatusCode,
		&doc.Validity.StatusClass,
		&doc.Validity.EffectiveFrom,
		&doc.Validity.EffectiveTo,
		&doc.Validity.Source,
		&doc.Validity.Reason,
		&doc.Source,
		&doc.SourceURL,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return documentMeta{}, false, nil
		}
		return documentMeta{}, false, fmt.Errorf("find document: %w", err)
	}
	doc.Validity.StatusLabel = statusLabel(doc.Validity.StatusClass)
	doc.Validity.Warning = validityWarning(doc.IssuedDate, doc.Validity.EffectiveFrom)
	doc.Cite = citeString(doc.DocNumber, "", doc.Validity.StatusLabel, doc.SourceURL)
	return doc, true, nil
}

// documentSources returns every official source where the document is published,
// with its landing-page URL (never a file). It unions the primary source with any
// cross-source aliases, so a doc on both VBPL and Cong Bao surfaces both links.
func (c dbCorpus) documentSources(ctx context.Context, docID int64) ([]docSource, error) {
	const q = `
SELECT DISTINCT sd.source, sd.detail_url, sd.external_id
FROM bronze.source_document sd
WHERE (
    sd.id = (SELECT source_document_id FROM silver.document WHERE id=$1)
    OR (sd.source, sd.external_id) IN (
         SELECT da.source, da.external_id FROM silver.document_alias da WHERE da.document_id=$1
       )
  )
  AND NULLIF(btrim(COALESCE(sd.detail_url, '')), '') IS NOT NULL
ORDER BY sd.source`
	rows, err := c.pool.Query(ctx, q, docID)
	if err != nil {
		return nil, fmt.Errorf("document sources: %w", err)
	}
	defer rows.Close()
	var out []docSource
	for rows.Next() {
		var s docSource
		var externalID string
		if err := rows.Scan(&s.Source, &s.URL, &externalID); err != nil {
			return nil, fmt.Errorf("scan document source: %w", err)
		}
		if build := c.filesListing[s.Source]; build != nil && externalID != "" {
			s.FilesURL = build(externalID)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("document source rows: %w", err)
	}
	return out, nil
}

// documentFileRow is one bronze.raw_file row joined to its source, before
// cross-source merging.
type documentFileRow struct {
	Source    string
	IsPrimary bool
	Label     string
	Kind      string
	Format    string
	ByteSize  int64
	SHA256    string
	URL       string
}

// documentFiles lists the document's downloadable artifacts across its primary
// source and every cross-source alias, merged by content hash.
func (c dbCorpus) documentFiles(ctx context.Context, docID int64) ([]documentFile, error) {
	const q = `
SELECT sd.source,
       sd.id = (SELECT source_document_id FROM silver.document WHERE id=$1) AS is_primary,
       rf.label, rf.file_kind, rf.file_format,
       COALESCE(rf.byte_size, 0), COALESCE(rf.sha256, ''), COALESCE(rf.url, '')
FROM bronze.source_document sd
JOIN bronze.raw_file rf ON rf.source_document_id = sd.id
WHERE sd.id = (SELECT source_document_id FROM silver.document WHERE id=$1)
   OR (sd.source, sd.external_id) IN (
        SELECT da.source, da.external_id FROM silver.document_alias da WHERE da.document_id=$1
      )
ORDER BY sd.source, rf.file_kind, rf.ordinal`
	rows, err := c.pool.Query(ctx, q, docID)
	if err != nil {
		return nil, fmt.Errorf("document files: %w", err)
	}
	defer rows.Close()
	var fileRows []documentFileRow
	for rows.Next() {
		var r documentFileRow
		if err := rows.Scan(&r.Source, &r.IsPrimary, &r.Label, &r.Kind, &r.Format, &r.ByteSize, &r.SHA256, &r.URL); err != nil {
			return nil, fmt.Errorf("scan document file: %w", err)
		}
		fileRows = append(fileRows, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("document file rows: %w", err)
	}
	return mergeDocumentFiles(fileRows), nil
}

// durableFileURL reports whether a stored file URL is a durable direct link.
// "X-Amz-" query parameters mark an expiring SigV4-presigned URL (vbpl's FPT
// Cloud links die after ~24h), which must never be served as evidence.
func durableFileURL(u string) bool {
	return u != "" && !strings.Contains(u, "X-Amz-")
}

// mergeDocumentFiles folds per-source raw-file rows into one entry per content
// hash. Metadata (label/kind/format) prefers the primary source's row — its
// naming is the richest — while origin_urls collects every durable direct link
// across sources. Rows without a hash stay separate entries rather than being
// merged blindly.
func mergeDocumentFiles(rows []documentFileRow) []documentFile {
	index := make(map[string]int)
	seenURL := make(map[string]bool)
	var out []documentFile
	for i, r := range rows {
		key := r.SHA256
		if key == "" {
			key = fmt.Sprintf("%s#%d", r.Source, i)
		}
		at, ok := index[key]
		if !ok {
			at = len(out)
			index[key] = at
			out = append(out, documentFile{
				Label: r.Label, Kind: r.Kind, Format: r.Format,
				ByteSize: r.ByteSize, SHA256: r.SHA256,
			})
		} else if r.IsPrimary {
			out[at].Label, out[at].Kind, out[at].Format = r.Label, r.Kind, r.Format
		}
		if durableFileURL(r.URL) && !seenURL[key+"|"+r.URL] {
			seenURL[key+"|"+r.URL] = true
			out[at].OriginURLs = append(out[at].OriginURLs, docFileOrigin{Source: r.Source, URL: r.URL})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := fileKindRank(out[i].Kind), fileKindRank(out[j].Kind); ri != rj {
			return ri < rj
		}
		return out[i].Label < out[j].Label
	})
	return out
}

// fileKindRank orders artifact roles main-first, matching the fetch-side
// ordering convention (main body before appendices before scans).
func fileKindRank(kind string) int {
	switch kind {
	case "main":
		return 0
	case "appendix":
		return 1
	case "attachment":
		return 2
	case "version_snapshot":
		return 3
	case "original_scan":
		return 4
	default:
		return 9
	}
}

type documentTextState struct {
	Indexed       bool
	HasBinding    bool
	HasNonBinding bool
	NeedsReview   bool
}

func (c dbCorpus) documentIndexed(ctx context.Context, docID int64) (bool, error) {
	const q = `
SELECT EXISTS (SELECT 1 FROM gold.chunk ch WHERE ch.document_id=$1)`
	var indexed bool
	if err := c.pool.QueryRow(ctx, q, docID).Scan(&indexed); err != nil {
		return false, fmt.Errorf("document indexed state: %w", err)
	}
	return indexed, nil
}

func (c dbCorpus) documentTextRows(ctx context.Context, docID int64) ([]documentTextEvidence, textProvenance, error) {
	const q = `
SELECT
  id,
  authority,
  COALESCE(source, ''),
  COALESCE(raw_file_id, 0),
  NULLIF(btrim(COALESCE(markdown, '')), '') IS NOT NULL AS has_text,
  is_binding,
  needs_review,
  COALESCE(extract_engine, ''),
  COALESCE(extract_confidence, 0),
  COALESCE(source_file_sha256, ''),
  COALESCE(verbatim_sha256, '')
FROM silver.document_text
WHERE document_id=$1
ORDER BY
  is_binding DESC,
  CASE authority
    WHEN 'human_verified' THEN 1
    WHEN 'gazette_borndigital' THEN 2
    WHEN 'transcription_html' THEN 3
    WHEN 'ocr_extractive' THEN 4
    WHEN 'ocr_generative' THEN 5
    ELSE 99
  END,
  needs_review ASC,
  id`
	rows, err := c.pool.Query(ctx, q, docID)
	if err != nil {
		return nil, textProvenance{}, fmt.Errorf("document text rows: %w", err)
	}
	defer rows.Close()

	var out []documentTextEvidence
	var summary textProvenance
	for rows.Next() {
		var row documentTextEvidence
		if err := rows.Scan(
			&row.TextID,
			&row.Authority,
			&row.Source,
			&row.RawFileID,
			&row.HasText,
			&row.IsBinding,
			&row.NeedsReview,
			&row.ExtractEngine,
			&row.ExtractConfidence,
			&row.SourceFileSHA256,
			&row.VerbatimSHA256,
		); err != nil {
			return nil, textProvenance{}, fmt.Errorf("scan document text row: %w", err)
		}
		out = append(out, row)
		summary.Authorities = appendUnique(summary.Authorities, row.Authority)
		summary.Sources = appendUnique(summary.Sources, row.Source)
		summary.ExtractEngines = appendUnique(summary.ExtractEngines, row.ExtractEngine)
		if row.HasText && row.IsBinding {
			summary.HasBindingText = true
		}
		if row.HasText && !row.IsBinding {
			summary.HasNonBindingText = true
		}
		if row.NeedsReview {
			summary.NeedsReview = true
		}
		if row.ExtractConfidence > summary.MaxConfidence {
			summary.MaxConfidence = row.ExtractConfidence
		}
	}
	if err := rows.Err(); err != nil {
		return nil, textProvenance{}, fmt.Errorf("document text row rows: %w", err)
	}
	return out, summary, nil
}

func appendUnique(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func (c dbCorpus) documentValidityPeriods(ctx context.Context, docID int64) ([]documentValidityPeriod, error) {
	const q = `
WITH current_validity AS (
  SELECT DISTINCT ON (document_id, section_id)
    id,
    document_id,
    section_id,
    status_code,
    status_class,
    eff_from,
    eff_to,
    COALESCE(source, '') AS source,
    COALESCE(reason, '') AS reason,
    observed_at
  FROM silver.validity_period
  WHERE document_id=$1
    AND superseded_at IS NULL
  ORDER BY document_id, section_id NULLS FIRST, observed_at DESC, id DESC
)
SELECT
  cv.id,
  COALESCE(cv.section_id, 0),
  COALESCE(ch.citation, s.citation_path, ''),
  COALESCE(cv.status_code, ''),
  COALESCE(cv.status_class, ''),
  COALESCE(to_char(cv.eff_from, 'YYYY-MM-DD'), ''),
  COALESCE(to_char(cv.eff_to, 'YYYY-MM-DD'), ''),
  COALESCE(cv.source, ''),
  COALESCE(cv.reason, ''),
  COALESCE(to_char(cv.observed_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '')
FROM current_validity cv
LEFT JOIN silver.document_section s ON s.id=cv.section_id
LEFT JOIN LATERAL (
  SELECT c.citation
  FROM gold.chunk c
  WHERE c.section_id=cv.section_id
  ORDER BY c.ordinal, c.id
  LIMIT 1
) ch ON true
ORDER BY cv.section_id NULLS FIRST, cv.observed_at DESC, cv.id DESC`
	rows, err := c.pool.Query(ctx, q, docID)
	if err != nil {
		return nil, fmt.Errorf("document validity periods: %w", err)
	}
	defer rows.Close()

	var out []documentValidityPeriod
	for rows.Next() {
		var row documentValidityPeriod
		if err := rows.Scan(
			&row.ValidityID,
			&row.SectionID,
			&row.Location,
			&row.StatusCode,
			&row.StatusClass,
			&row.EffectiveFrom,
			&row.EffectiveTo,
			&row.Source,
			&row.Reason,
			&row.ObservedAt,
		); err != nil {
			return nil, fmt.Errorf("scan document validity period: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("document validity period rows: %w", err)
	}
	return out, nil
}

func documentGaps(doc documentMeta, state documentTextState, hasSectionValidity bool) []gap {
	var gaps []gap
	switch doc.Validity.StatusClass {
	case "":
		gaps = append(gaps, gap{
			Kind:       string(retrieve.GapValidityUnknown),
			Message:    "document has no current validity evidence",
			DocumentID: doc.DocumentID,
			DocNumber:  doc.DocNumber,
			Title:      doc.Title,
		})
	case "partial":
		if !hasSectionValidity {
			gaps = append(gaps, gap{
				Kind:       string(retrieve.GapPartialValidityUncertain),
				Message:    "document is only partially in force and no section-level validity rows are present",
				DocumentID: doc.DocumentID,
				DocNumber:  doc.DocNumber,
				Title:      doc.Title,
			})
		}
	}
	if !state.Indexed && state.HasNonBinding && !state.HasBinding {
		gaps = append(gaps, gap{
			Kind:       string(retrieve.GapKnownBindingTextGap),
			Message:    "document text exists only as non-binding/unindexed text",
			DocumentID: doc.DocumentID,
			DocNumber:  doc.DocNumber,
			Title:      doc.Title,
		})
	}
	if state.NeedsReview {
		gaps = append(gaps, gap{
			Kind:       string(retrieve.GapTextNeedsReview),
			Message:    "one or more document text rows are marked needs_review",
			DocumentID: doc.DocumentID,
			DocNumber:  doc.DocNumber,
			Title:      doc.Title,
		})
	}
	return gaps
}

func hasSectionValidity(periods []documentValidityPeriod) bool {
	for _, period := range periods {
		if period.SectionID != 0 {
			return true
		}
	}
	return false
}

func documentChunkGapMessage(citation string, offset int) string {
	citation = strings.TrimSpace(citation)
	switch {
	case citation != "":
		return "citation filter returned no indexed chunks for this document"
	case offset > 0:
		return "chunk pagination offset returned no indexed chunks for this document"
	default:
		return "document has no indexed chunks"
	}
}

func (c dbCorpus) documentChunks(ctx context.Context, docID int64, citation string, limit, offset int, textSummary textProvenance) ([]documentChunk, error) {
	citation = strings.TrimSpace(citation)
	const q = `
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
  WHERE document_id=$1
    AND superseded_at IS NULL
  ORDER BY document_id, section_id NULLS FIRST, observed_at DESC, id DESC
)
SELECT
  c.id,
  COALESCE(c.section_id, 0),
  c.citation,
  COALESCE(c.context_prefix, ''),
  c.content,
  c.ordinal,
  COALESCE(secv.section_id, docv.section_id, 0) AS validity_section_id,
  COALESCE(secv.status_code, docv.status_code, '') AS status_code,
  COALESCE(secv.status_class, docv.status_class, '') AS status_class,
  COALESCE(to_char(COALESCE(secv.eff_from, docv.eff_from), 'YYYY-MM-DD'), '') AS eff_from,
  COALESCE(to_char(COALESCE(secv.eff_to, docv.eff_to), 'YYYY-MM-DD'), '') AS eff_to,
  COALESCE(secv.source, docv.source, '') AS validity_source,
  COALESCE(secv.reason, docv.reason, '') AS validity_reason
FROM gold.chunk c
LEFT JOIN current_validity docv ON docv.document_id = c.document_id AND docv.section_id IS NULL
LEFT JOIN current_validity secv ON secv.document_id = c.document_id AND secv.section_id = c.section_id
WHERE c.document_id=$1
  AND ($2::text = '' OR citation ILIKE '%' || $2 || '%')
ORDER BY c.ordinal, c.id
LIMIT $3 OFFSET $4`
	rows, err := c.pool.Query(ctx, q, docID, citation, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("document chunks: %w", err)
	}
	defer rows.Close()
	chunks := make([]documentChunk, 0, limit)
	for rows.Next() {
		var ch documentChunk
		if err := rows.Scan(
			&ch.ChunkID,
			&ch.SectionID,
			&ch.Location,
			&ch.ContextPrefix,
			&ch.Content,
			&ch.Ordinal,
			&ch.Validity.SectionID,
			&ch.Validity.StatusCode,
			&ch.Validity.StatusClass,
			&ch.Validity.EffectiveFrom,
			&ch.Validity.EffectiveTo,
			&ch.Validity.Source,
			&ch.Validity.Reason,
		); err != nil {
			return nil, fmt.Errorf("scan document chunk: %w", err)
		}
		ch.TextProvenance = textSummary
		chunks = append(chunks, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("document chunk rows: %w", err)
	}
	return chunks, nil
}

func (c dbCorpus) documentRelations(ctx context.Context, docID int64) ([]retrieve.Relation, error) {
	const q = `
WITH hit_doc(id) AS (
    SELECT $1::bigint
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
        EXISTS (SELECT 1 FROM gold.chunk ch WHERE ch.document_id = ref.document_id) AS target_indexed,
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
        EXISTS (SELECT 1 FROM gold.chunk ch WHERE ch.document_id = dr.from_document_id) AS target_indexed,
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
ORDER BY rn`
	rows, err := c.pool.Query(ctx, q, docID, documentRelationLimit)
	if err != nil {
		return nil, fmt.Errorf("document relations: %w", err)
	}
	defer rows.Close()

	var out []retrieve.Relation
	for rows.Next() {
		var rel retrieve.Relation
		if err := rows.Scan(
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
			return nil, fmt.Errorf("scan document relation: %w", err)
		}
		rel.TargetText.HasBindingText = rel.TargetHasBindingText
		rel.TargetText.NeedsReview = rel.TargetNeedsReview
		out = append(out, rel)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("document relation rows: %w", err)
	}
	return out, nil
}
