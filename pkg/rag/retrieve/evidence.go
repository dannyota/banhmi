package retrieve

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	pgvector "github.com/pgvector/pgvector-go"

	"danny.vn/banhmi/pkg/scope"
)

// GapKind is a machine-readable evidence/gate reason.
type GapKind string

const (
	GapOutOfDomain              GapKind = "out_of_domain"
	GapNoEvidence               GapKind = "no_evidence"
	GapLowConfidence            GapKind = "low_confidence"
	GapKnownBindingTextGap      GapKind = "known_binding_text_gap"
	GapUnresolvedRelation       GapKind = "unresolved_relation_target"
	GapRelationTargetTextGap    GapKind = "relation_target_text_gap"
	GapTextNeedsReview          GapKind = "text_needs_review"
	GapValidityUnknown          GapKind = "validity_unknown"
	GapPartialValidityUncertain GapKind = "partial_validity_uncertain"
)

// Gap is a database-backed reason the evidence is incomplete or uncertain.
// Most gaps are context for the user-owned model/agent. BlocksAnswer is reserved
// for deterministic guardrails such as out-of-domain, no-evidence, or an
// operator-configured low-confidence threshold.
type Gap struct {
	Kind         GapKind
	Message      string
	BlocksAnswer bool
	DocumentID   int64
	DocNumber    string
	Title        string
	RelationID   int64
	RelationType string
}

// ScopeEvidence describes the query-domain check. The matcher vocabulary comes
// from config.scope_term; no source policy is hardcoded here.
type ScopeEvidence struct {
	Checked        bool
	InDomain       bool
	MatchedTerms   []string
	KnownReference bool
}

// Evidence is the retrieval product boundary: ranked chunks plus explicit gaps
// and scope signals. Users can feed this to their own model/agent over MCP.
type Evidence struct {
	Hits        []Hit
	RelatedHits []RelatedHit
	Gaps        []Gap
	Scope       ScopeEvidence
	Abstain     bool
	TopScore    float64
}

// GateConfig contains the DB/config-backed knobs for evidence gating. ScopeTerms
// is normally loaded from config.scope_term. MinScore is optional and should be
// loaded from config.setting, not baked into code.
type GateConfig struct {
	ScopeTerms []scope.Term
	MinScore   float64
}

// Option configures a Retriever without changing the common constructor call.
type Option func(*hybridRetriever)

// WithGateConfig enables SearchEvidence domain and confidence gates.
func WithGateConfig(cfg GateConfig) Option {
	return func(r *hybridRetriever) {
		r.gate.minScore = cfg.MinScore
		if len(cfg.ScopeTerms) > 0 {
			r.gate.matcher = scope.Load(cfg.ScopeTerms)
		}
	}
}

type gateState struct {
	matcher  *scope.Matcher
	minScore float64
}

// SearchEvidence runs retrieval and annotates the result with DB-backed gap
// reasons. It never hides hits; callers can inspect evidence even when Abstain is
// true.
func (r *hybridRetriever) SearchEvidence(ctx context.Context, query string, opts SearchOpts) (Evidence, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Evidence{
			Abstain: true,
			Gaps: []Gap{{
				Kind:         GapNoEvidence,
				Message:      "empty query",
				BlocksAnswer: true,
			}},
		}, nil
	}

	hits, err := r.searchHits(ctx, query, opts)
	if err != nil {
		return Evidence{}, err
	}

	ev := Evidence{Hits: hits}
	if len(hits) > 0 {
		ev.TopScore = hits[0].Score
	}

	scopeEv, refs, err := r.scopeEvidence(ctx, query)
	if err != nil {
		return Evidence{}, err
	}
	ev.Scope = scopeEv

	if scopeEv.Checked && !scopeEv.InDomain && !scopeEv.KnownReference {
		ev.addGap(Gap{
			Kind:         GapOutOfDomain,
			Message:      "query does not match configured RAG scope terms or known legal references",
			BlocksAnswer: true,
		})
	}
	if len(hits) == 0 {
		ev.addGap(Gap{
			Kind:         GapNoEvidence,
			Message:      "retrieval returned no chunks",
			BlocksAnswer: true,
		})
	}
	if r.gate.minScore > 0 && len(hits) > 0 && hits[0].Score < r.gate.minScore {
		ev.addGap(Gap{
			Kind:         GapLowConfidence,
			Message:      "top retrieval score is below configured threshold",
			BlocksAnswer: true,
		})
	}

	gaps, err := r.knownBindingTextGaps(ctx, refs, scopeEv.MatchedTerms)
	if err != nil {
		return Evidence{}, err
	}
	for _, gap := range gaps {
		ev.addGap(gap)
	}
	for _, gap := range relationGaps(hits) {
		ev.addGap(gap)
	}

	if opts.RelatedK > 0 && len(hits) > 0 {
		related, err := r.relatedHits(ctx, query, hits, opts.RelatedK)
		if err != nil {
			// Related hits are adjacent graph context, not primary evidence — never fail
			// the whole search because the expansion failed.
			r.log.Warn("retrieve: related hits failed; returning primary hits only", "err", err)
		} else {
			ev.RelatedHits = related
		}
	}
	for _, gap := range evidenceStateGaps(ev.Hits, ev.RelatedHits) {
		ev.addGap(gap)
	}

	ev.Abstain = hasBlockingGap(ev.Gaps)
	return ev, nil
}

type relatedSeed struct {
	sourceRank     int
	baseChunkID    int64
	baseDocumentID int64
	baseDocNumber  string
	relationID     int64
	direction      string
	relationType   string
	source         string
	toCitation     string
	documentID     int64
}

func (r *hybridRetriever) relatedHits(ctx context.Context, query string, hits []Hit, limit int) ([]RelatedHit, error) {
	// Related-hit ranking uses vectors (the production retrieval path), so it needs the
	// embedder. Without one (BM25-only eval mode) we skip the expansion rather than run
	// a ParadeDB query that managed Postgres (e.g. RDS) cannot execute.
	if r.pool == nil || limit <= 0 || r.embedder == nil {
		return nil, nil
	}

	seeds := relatedSeeds(hits)
	if len(seeds) == 0 {
		return nil, nil
	}

	sourceRanks := make([]int32, 0, len(seeds))
	baseChunkIDs := make([]int64, 0, len(seeds))
	baseDocumentIDs := make([]int64, 0, len(seeds))
	baseDocNumbers := make([]string, 0, len(seeds))
	relationIDs := make([]int64, 0, len(seeds))
	directions := make([]string, 0, len(seeds))
	relationTypes := make([]string, 0, len(seeds))
	sources := make([]string, 0, len(seeds))
	toCitations := make([]string, 0, len(seeds))
	documentIDs := make([]int64, 0, len(seeds))
	for _, seed := range seeds {
		sourceRanks = append(sourceRanks, int32(seed.sourceRank))
		baseChunkIDs = append(baseChunkIDs, seed.baseChunkID)
		baseDocumentIDs = append(baseDocumentIDs, seed.baseDocumentID)
		baseDocNumbers = append(baseDocNumbers, seed.baseDocNumber)
		relationIDs = append(relationIDs, seed.relationID)
		directions = append(directions, seed.direction)
		relationTypes = append(relationTypes, seed.relationType)
		sources = append(sources, seed.source)
		toCitations = append(toCitations, seed.toCitation)
		documentIDs = append(documentIDs, seed.documentID)
	}

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
rel AS (
    SELECT *
    FROM unnest(
        $2::integer[],
        $3::bigint[],
        $4::bigint[],
        $5::text[],
        $6::bigint[],
        $7::text[],
        $8::text[],
        $9::text[],
        $10::text[],
        $11::bigint[]
    ) AS r(
        source_rank,
        base_chunk_id,
        base_document_id,
        base_doc_number,
        relation_id,
        direction,
        relation_type,
        source,
        to_citation,
        document_id
    )
),
ranked AS (
    SELECT
        rel.source_rank,
        rel.base_chunk_id,
        rel.base_document_id,
        rel.base_doc_number,
        rel.relation_id,
        rel.direction,
        rel.relation_type,
        rel.source,
        rel.to_citation,
        c.id AS chunk_id,
        c.document_id,
        COALESCE(d.doc_number, '') AS doc_number,
        COALESCE(d.title, '') AS title,
        COALESCE(sd.detail_url, '') AS source_url,
        COALESCE(cv.status_code, '') AS status_code,
        COALESCE(cv.status_class, '') AS status_class,
        COALESCE(to_char(cv.eff_from, 'YYYY-MM-DD'), '') AS eff_from,
        COALESCE(to_char(cv.eff_to, 'YYYY-MM-DD'), '') AS eff_to,
        COALESCE(cv.source, '') AS validity_source,
        COALESCE(cv.reason, '') AS validity_reason,
        COALESCE(ts.has_binding_text, false) AS has_binding_text,
        COALESCE(ts.has_nonbinding_text, false) AS has_nonbinding_text,
        COALESCE(ts.needs_review, false) AS text_needs_review,
        COALESCE(ts.authorities, ARRAY[]::text[]) AS text_authorities,
        COALESCE(ts.sources, ARRAY[]::text[]) AS text_sources,
        COALESCE(ts.extract_engines, ARRAY[]::text[]) AS extract_engines,
        COALESCE(ts.max_confidence, 0) AS max_confidence,
        c.citation,
        COALESCE(c.context_prefix, '') AS context_prefix,
        c.content,
        0::double precision AS bm25_score,
        (e.embedding <=> $1) AS vscore,
        row_number() OVER (
            PARTITION BY rel.relation_id
            ORDER BY e.embedding <=> $1, c.ordinal, c.id
        ) AS relation_rank
    FROM rel
    JOIN gold.chunk c ON c.document_id = rel.document_id
    JOIN gold.chunk_embedding e ON e.chunk_id = c.id AND e.model = $12
    LEFT JOIN silver.document d ON d.id = c.document_id
    LEFT JOIN bronze.source_document sd ON sd.id = d.source_document_id
    LEFT JOIN current_validity cv ON cv.document_id = c.document_id
    LEFT JOIN text_state ts ON ts.document_id = c.document_id
)
SELECT
    base_chunk_id,
    base_document_id,
    base_doc_number,
    relation_id,
    direction,
    relation_type,
    source,
    to_citation,
    chunk_id,
    document_id,
    doc_number,
    title,
    source_url,
    status_code,
    status_class,
    eff_from,
    eff_to,
    validity_source,
    validity_reason,
    has_binding_text,
    has_nonbinding_text,
    text_needs_review,
    text_authorities,
    text_sources,
    extract_engines,
    max_confidence,
    citation,
    context_prefix,
    content,
    relation_rank,
    bm25_score
FROM ranked
WHERE relation_rank <= $13
ORDER BY vscore, source_rank, relation_rank
LIMIT $14`

	vecs, err := r.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed related-hits query: %w", err)
	}
	if len(vecs) != 1 || vecs[0] == nil {
		return nil, fmt.Errorf("embedder returned %d vectors for related-hits query", len(vecs))
	}
	qv := pgvector.NewVector(vecs[0])

	rows, err := r.pool.Query(ctx, q,
		qv,
		sourceRanks,
		baseChunkIDs,
		baseDocumentIDs,
		baseDocNumbers,
		relationIDs,
		directions,
		relationTypes,
		sources,
		toCitations,
		documentIDs,
		r.embedder.Model(),
		relatedHitLimitPerRelation,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("related hits: %w", err)
	}
	defer rows.Close()

	out := make([]RelatedHit, 0, limit)
	for rows.Next() {
		var h RelatedHit
		if err := rows.Scan(
			&h.BaseChunkID,
			&h.BaseDocumentID,
			&h.BaseDocNumber,
			&h.RelationID,
			&h.Direction,
			&h.RelationType,
			&h.Source,
			&h.ToCitation,
			&h.ChunkID,
			&h.DocumentID,
			&h.DocNumber,
			&h.Title,
			&h.SourceURL,
			&h.Validity.StatusCode,
			&h.Validity.StatusClass,
			&h.Validity.EffectiveFrom,
			&h.Validity.EffectiveTo,
			&h.Validity.Source,
			&h.Validity.Reason,
			&h.Text.HasBindingText,
			&h.Text.HasNonBindingText,
			&h.Text.NeedsReview,
			&h.Text.Authorities,
			&h.Text.Sources,
			&h.Text.ExtractEngines,
			&h.Text.MaxConfidence,
			&h.Citation,
			&h.ContextPrefix,
			&h.Content,
			&h.BM25Rank,
			&h.BM25Score,
		); err != nil {
			return nil, fmt.Errorf("scan related hit: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("related hit rows: %w", err)
	}
	return out, nil
}

func relatedSeeds(hits []Hit) []relatedSeed {
	var seeds []relatedSeed
	seen := map[int64]bool{}
	sourceRank := 0
	for _, hit := range hits {
		for _, rel := range hit.Relations {
			if rel.RelationID == 0 || seen[rel.RelationID] {
				continue
			}
			if !rel.Resolved || !rel.TargetIndexed || !rel.TargetHasBindingText || rel.DocumentID == 0 {
				continue
			}
			if rel.DocumentID == hit.DocumentID {
				continue
			}
			seen[rel.RelationID] = true
			sourceRank++
			seeds = append(seeds, relatedSeed{
				sourceRank:     sourceRank,
				baseChunkID:    hit.ChunkID,
				baseDocumentID: hit.DocumentID,
				baseDocNumber:  hit.DocNumber,
				relationID:     rel.RelationID,
				direction:      rel.Direction,
				relationType:   rel.RelationType,
				source:         rel.Source,
				toCitation:     rel.ToCitation,
				documentID:     rel.DocumentID,
			})
		}
	}
	return seeds
}

func (ev *Evidence) addGap(g Gap) {
	ev.Gaps = append(ev.Gaps, g)
}

func hasBlockingGap(gaps []Gap) bool {
	for _, gap := range gaps {
		if gap.BlocksAnswer {
			return true
		}
	}
	return false
}

func (r *hybridRetriever) scopeEvidence(ctx context.Context, query string) (ScopeEvidence, []string, error) {
	refs := extractDocumentRefs(query)
	knownRef, err := r.knownDocumentReference(ctx, refs)
	if err != nil {
		return ScopeEvidence{}, nil, err
	}

	if r.gate.matcher == nil {
		return ScopeEvidence{
			Checked:        false,
			InDomain:       true,
			KnownReference: knownRef,
		}, refs, nil
	}

	match := r.gate.matcher.Match("", query, query)
	terms := uniqueStrings(match.Matched)
	return ScopeEvidence{
		Checked:        true,
		InDomain:       match.InScope || knownRef,
		MatchedTerms:   terms,
		KnownReference: knownRef,
	}, refs, nil
}

var documentRefRe = regexp.MustCompile(`(?i)\b\d+(?:/\d+)*\/[\p{L}][\p{L}\d-]*[\p{L}\d]\b`)

func extractDocumentRefs(query string) []string {
	matches := documentRefRe.FindAllString(query, -1)
	for i := range matches {
		matches[i] = strings.ToLower(strings.TrimSpace(matches[i]))
	}
	return uniqueStrings(matches)
}

func (r *hybridRetriever) knownDocumentReference(ctx context.Context, refs []string) (bool, error) {
	if len(refs) == 0 {
		return false, nil
	}
	if r.pool == nil {
		return false, nil
	}

	const q = `
SELECT EXISTS (
    SELECT 1
    FROM silver.document d
    WHERE lower(COALESCE(d.doc_number, '')) = ANY($1)
       OR lower(COALESCE(d.doc_number_norm, '')) = ANY($1)
       OR lower(d.doc_key) = ANY($1)
    UNION ALL
    SELECT 1
    FROM silver.doc_ref ref
    WHERE lower(COALESCE(ref.label, '')) = ANY($1)
       OR lower(ref.ref_key) = ANY($1)
)`
	var ok bool
	if err := r.pool.QueryRow(ctx, q, refs).Scan(&ok); err != nil {
		return false, fmt.Errorf("known document reference: %w", err)
	}
	return ok, nil
}

func (r *hybridRetriever) knownBindingTextGaps(ctx context.Context, refs, terms []string) ([]Gap, error) {
	if r.pool == nil {
		return nil, nil
	}
	terms = uniqueStrings(terms)
	if len(refs) == 0 && len(terms) == 0 {
		return nil, nil
	}
	const q = `
WITH doc_state AS (
    SELECT
        d.id,
        COALESCE(d.doc_number, '') AS doc_number,
        COALESCE(d.title, '') AS title,
        bool_or(dt.is_binding AND NULLIF(btrim(COALESCE(dt.markdown, '')), '') IS NOT NULL) AS has_binding,
        bool_or((NOT dt.is_binding) AND NULLIF(btrim(COALESCE(dt.markdown, '')), '') IS NOT NULL) AS has_nonbinding,
        bool_or(dt.needs_review) AS needs_review,
        EXISTS (SELECT 1 FROM gold.chunk c WHERE c.document_id = d.id) AS indexed,
        lower(
            COALESCE(d.doc_number, '') || E'\n' ||
            COALESCE(d.doc_number_norm, '') || E'\n' ||
            COALESCE(d.doc_key, '') || E'\n' ||
            COALESCE(d.title, '') || E'\n' ||
            string_agg(COALESCE(dt.markdown, ''), E'\n')
        ) AS haystack
    FROM silver.document d
    JOIN silver.document_text dt ON dt.document_id = d.id
    GROUP BY d.id, d.doc_number, d.doc_number_norm, d.doc_key, d.title
)
SELECT id, doc_number, title
FROM doc_state
WHERE NOT indexed
  AND has_nonbinding
  AND NOT has_binding
  AND (
      (cardinality($1::text[]) > 0 AND EXISTS (
          SELECT 1 FROM unnest($1::text[]) ref WHERE position(ref in haystack) > 0
      ))
      OR
      (cardinality($2::text[]) > 0 AND EXISTS (
          SELECT 1 FROM unnest($2::text[]) term WHERE position(term in haystack) > 0
      ))
  )
ORDER BY needs_review DESC, doc_number, id
LIMIT 5`
	rows, err := r.pool.Query(ctx, q, refs, terms)
	if err != nil {
		return nil, fmt.Errorf("known binding text gaps: %w", err)
	}
	defer rows.Close()

	var gaps []Gap
	for rows.Next() {
		var gap Gap
		gap.Kind = GapKnownBindingTextGap
		gap.Message = "matching document text exists only as non-binding/unindexed text"
		if err := rows.Scan(&gap.DocumentID, &gap.DocNumber, &gap.Title); err != nil {
			return nil, fmt.Errorf("scan binding text gap: %w", err)
		}
		gaps = append(gaps, gap)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("binding text gap rows: %w", err)
	}
	return gaps, nil
}

func relationGaps(hits []Hit) []Gap {
	seen := map[int64]bool{}
	var gaps []Gap
	for _, hit := range hits {
		for _, rel := range hit.Relations {
			if rel.RelationID != 0 && seen[rel.RelationID] {
				continue
			}
			seen[rel.RelationID] = true
			switch {
			case !rel.Resolved:
				gaps = append(gaps, Gap{
					Kind:         GapUnresolvedRelation,
					Message:      "confirmed relation target is not resolved to a corpus document",
					DocumentID:   hit.DocumentID,
					DocNumber:    hit.DocNumber,
					Title:        hit.Title,
					RelationID:   rel.RelationID,
					RelationType: rel.RelationType,
				})
			case !rel.TargetIndexed && !rel.TargetHasBindingText:
				gaps = append(gaps, Gap{
					Kind:         GapRelationTargetTextGap,
					Message:      "confirmed relation target is resolved but has no indexed binding chunks",
					DocumentID:   rel.DocumentID,
					DocNumber:    rel.DocNumber,
					Title:        rel.Title,
					RelationID:   rel.RelationID,
					RelationType: rel.RelationType,
				})
			}
		}
	}
	return gaps
}

func evidenceStateGaps(hits []Hit, related []RelatedHit) []Gap {
	type docState struct {
		documentID int64
		docNumber  string
		title      string
		validity   ValidityEvidence
		text       TextEvidence
	}
	docs := make([]docState, 0, len(hits)+len(related))
	for _, hit := range hits {
		docs = append(docs, docState{
			documentID: hit.DocumentID,
			docNumber:  hit.DocNumber,
			title:      hit.Title,
			validity:   hit.Validity,
			text:       hit.Text,
		})
	}
	for _, hit := range related {
		docs = append(docs, docState{
			documentID: hit.DocumentID,
			docNumber:  hit.DocNumber,
			title:      hit.Title,
			validity:   hit.Validity,
			text:       hit.Text,
		})
	}

	seen := map[string]bool{}
	var gaps []Gap
	add := func(kind GapKind, message string, doc docState) {
		key := fmt.Sprintf("%s/%d", kind, doc.documentID)
		if seen[key] {
			return
		}
		seen[key] = true
		gaps = append(gaps, Gap{
			Kind:       kind,
			Message:    message,
			DocumentID: doc.documentID,
			DocNumber:  doc.docNumber,
			Title:      doc.title,
		})
	}
	for _, doc := range docs {
		switch doc.validity.StatusClass {
		case "":
			add(GapValidityUnknown, "retrieved document has no current validity evidence", doc)
		case "partial":
			if doc.validity.SectionID == 0 {
				add(GapPartialValidityUncertain, "document is only partially in force and no section-level validity row is attached to this chunk", doc)
			}
		}
		if doc.text.NeedsReview {
			add(GapTextNeedsReview, "retrieved document text is marked needs_review", doc)
		}
	}
	return gaps
}

func uniqueStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(strings.ToLower(s))
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
