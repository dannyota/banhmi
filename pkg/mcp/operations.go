package mcp

import (
	"context"
	"fmt"
	"strings"

	"danny.vn/banhmi/pkg/rag/retrieve"
)

// SearchInput is the shared search operation input. Its JSON fields and defaults
// are also the MCP search tool contract.
type SearchInput struct {
	Query          string `json:"query" jsonschema:"the legal question or keywords — ALWAYS in the corpus's binding legal language (the tool description states which); queries in other languages return degraded rankings"`
	TopK           int    `json:"top_k,omitempty" jsonschema:"max ranked hits to return (0 = default)"`
	Detail         string `json:"detail,omitempty" jsonschema:"response size — compact: discovery pass (metadata + snippet + cite + validity badge; no provision text, relations, or related_hits — cheapest); standard (default): adds relations, related_hits, and full validity, but NOT the inline full provision text (open the document tool with provision.citation to read the whole article/section); full: everything including the full enclosing provision text inline on every hit (largest — prefer the two-pass compact/standard → document pattern)"`
	InForceOnly    *bool  `json:"in_force_only,omitempty" jsonschema:"default (omit): current law leads, with a small badged pass of non-current law after it; true: current law only (in force + partial); false: no validity filter, pure relevance (historical/admin)"`
	IncludeRelated *bool  `json:"include_related,omitempty" jsonschema:"also return chunks from confirmed related documents (default true; ignored when detail=compact — compact never returns related_hits)"`
	RelatedK       int    `json:"related_k,omitempty" jsonschema:"max related chunks (0 = MCP default)"`

	AsOf       string   `json:"as_of,omitempty" jsonschema:"point-in-time (YYYY-MM-DD): return law in force ON that date (its effective window contains the date) instead of current-as-of-now; uses recorded effective dates, so documents without one are excluded"`
	IssuedFrom string   `json:"issued_from,omitempty" jsonschema:"only documents issued on or after this date (YYYY-MM-DD)"`
	IssuedTo   string   `json:"issued_to,omitempty" jsonschema:"only documents issued on or before this date (YYYY-MM-DD)"`
	Issuer     []string `json:"issuer,omitempty" jsonschema:"filter by issuing body — case-insensitive substring match on a hit's issuer value (e.g. Ngân hàng Nhà nước matches Ngân hàng Nhà nước Việt Nam)"`
	DocType    []string `json:"doc_type,omitempty" jsonschema:"filter by document type — case-insensitive exact match on a hit's doc_type value (copy the vocabulary from search hits' doc_type)"`
}

// SearchOutput is the shared search operation result. It contains ranked
// evidence without model synthesis.
type SearchOutput struct {
	Hits        []searchHit   `json:"hits" jsonschema:"các đoạn trích phù hợp nhất, đã xếp hạng"`
	RelatedHits []relatedHit  `json:"related_hits,omitempty" jsonschema:"đoạn trích liên quan qua quan hệ xác nhận; không phải rank boost"`
	Gaps        []gap         `json:"gaps,omitempty" jsonschema:"khoảng trống dữ liệu hoặc lý do cần abstain"`
	Abstain     bool          `json:"abstain" jsonschema:"true khi có gap chặn (xem gaps[] để biết lý do); hits vẫn luôn được trả về để bạn tự đánh giá — abstain không có nghĩa là hits sai"`
	Scope       scopeEvidence `json:"scope" jsonschema:"tín hiệu phạm vi từ config.scope_term và tham chiếu văn bản đã biết"`
}

// DocumentInput is the shared document operation input. Its JSON fields and
// defaults are also the MCP document tool contract.
type DocumentInput struct {
	DocumentID int64    `json:"document_id,omitempty" jsonschema:"silver.document id; use this when search returned document_id"`
	DocNumber  string   `json:"doc_number,omitempty" jsonschema:"document number / identifier exactly as printed by the official source (use the doc_number returned by search)"`
	Citation   string   `json:"citation,omitempty" jsonschema:"filter chunks by provision position, in the corpus's citation vocabulary — copy it from a search hit's location or parent_citation"`
	Include    []string `json:"include,omitempty" jsonschema:"response sections to return — any of: chunks (provision text), relations (confirmed graph edges), amendments (verbatim incoming amendment clauses), timeline (chronological history), provenance (per-artifact extraction rows). Omitted = chunks + relations + amendments + timeline. Reading one provision? Pass include=['chunks'] with a citation filter — the cheapest call. Document metadata, validity, sources, text_summary, and gaps are always returned."`
	Limit      int      `json:"limit,omitempty" jsonschema:"maximum chunks to return (0 = default)"`
	Offset     int      `json:"offset,omitempty" jsonschema:"offset to page through more chunks"`
}

// DocumentOutput is the shared document operation result.
type DocumentOutput struct {
	Found    bool         `json:"found"`
	Document documentMeta `json:"document,omitempty"`
	// AlsoMatches lists other documents sharing the requested document number.
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

// Search returns the evidence payload used by every search transport.
func (s *Server) Search(ctx context.Context, in SearchInput) (SearchOutput, error) {
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return SearchOutput{}, fmt.Errorf("query is required")
	}

	detail, known := parseSearchDetail(in.Detail)
	if !known {
		s.log.Warn("mcp: unknown search detail level, using standard", "detail", in.Detail)
	}
	relatedK := searchRelatedK(in)
	if detail == detailCompact {
		relatedK = 0
	}

	ev, err := s.searcher.SearchEvidence(ctx, query, retrieve.SearchOpts{
		TopK:        in.TopK,
		InForceOnly: in.InForceOnly,
		RelatedK:    relatedK,
		AsOf:        in.AsOf,
		IssuedFrom:  in.IssuedFrom,
		IssuedTo:    in.IssuedTo,
		Issuer:      in.Issuer,
		DocType:     in.DocType,
	})
	if err != nil {
		s.log.Error("mcp: search", "err", err)
		return SearchOutput{}, fmt.Errorf("search: %w", err)
	}

	out := SearchOutput{
		Hits:    toSearchHits(ev.Hits, detail),
		Gaps:    toGaps(ev.Gaps),
		Abstain: ev.Abstain,
		Scope:   toScopeEvidence(ev.Scope),
	}
	if detail != detailCompact {
		out.RelatedHits = toRelatedHits(ev.RelatedHits, detail)
	}
	return out, nil
}

// Document returns the evidence payload used by every document transport.
func (s *Server) Document(ctx context.Context, in DocumentInput) (DocumentOutput, error) {
	if s.corpus == nil {
		return DocumentOutput{}, fmt.Errorf("corpus database is not configured")
	}
	if in.DocumentID == 0 && strings.TrimSpace(in.DocNumber) == "" {
		return DocumentOutput{}, fmt.Errorf("document_id or doc_number is required")
	}
	for _, name := range in.Include {
		if normalized := strings.ToLower(strings.TrimSpace(name)); normalized != "" && !documentSections[normalized] {
			s.log.Warn("mcp: unknown document include section, ignoring", "section", name)
		}
	}
	out, err := s.corpus.Document(ctx, in)
	if err != nil {
		s.log.Error("mcp: document", "err", err)
		return DocumentOutput{}, fmt.Errorf("document: %w", err)
	}
	return out, nil
}
