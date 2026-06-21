// Package mcp is banhmi's MCP query surface: a thin front door over the shared
// retrieval core (pkg/rag) so user-owned agents/models (Claude.ai, ChatGPT, Gemini,
// Grok, …) can query banhmi's evidence over the Model Context Protocol (stdio,
// JSON-RPC 2.0). It exposes evidence-only tools — guide, corpus_status, quality_gaps,
// document, and search — built on the official Go SDK
// (github.com/modelcontextprotocol/go-sdk). banhmi never answers; the connecting
// model decides the answer from the evidence.
//
// Handlers are thin: parse the typed input, call the core, shape the MCP result.
// All retrieval and citation logic stays in the core (see CLAUDE.md: "Keep
// retrieval/citation logic in the core, not in a surface"). The surface depends on
// the minimal Searcher interface defined here (at the consumer); *retrieve.Retriever
// satisfies it, and tests inject fakes so no live retriever is required.
//
// stdout is the MCP transport, so this package logs only to the slog.Logger it is
// given (banhmi's logger writes to stderr).
package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"danny.vn/banhmi/pkg/rag/retrieve"
)

// Searcher is the slice of the retrieval core the MCP surface needs for the search
// tool. *retrieve.Retriever (the retrieve.Retriever interface) satisfies it.
type Searcher interface {
	Search(ctx context.Context, query string, opts retrieve.SearchOpts) ([]retrieve.Hit, error)
	SearchEvidence(ctx context.Context, query string, opts retrieve.SearchOpts) (retrieve.Evidence, error)
}

// Server wraps the official MCP server with banhmi's evidence tools registered over
// the retrieval core. Build it with New and serve it with Run.
type Server struct {
	mcp          *mcp.Server
	searcher     Searcher
	corpus       CorpusReader
	log          *slog.Logger
	jurisdiction string
	brief        brief
}

// Option configures optional MCP capabilities.
type Option func(*Server)

// WithJurisdiction selects the served jurisdiction's brief, guide, tool
// descriptions, and product name. Defaults to VN (the compiled fallback) when
// unset or unknown. The tool mechanics are identical across jurisdictions.
func WithJurisdiction(jurisdiction string) Option {
	return func(s *Server) {
		s.jurisdiction = jurisdiction
	}
}

// WithPool enables DB-backed corpus_status and document tools for deployed
// agents. The database is local to the banhmi stack; no local files are exposed.
func WithPool(pool *pgxpool.Pool) Option {
	return func(s *Server) {
		if pool != nil {
			s.corpus = dbCorpus{pool: pool}
		}
	}
}

// WithCorpus injects a corpus reader for tests or alternate deployments.
func WithCorpus(c CorpusReader) Option {
	return func(s *Server) {
		s.corpus = c
	}
}

// New builds the evidence-only MCP surface over a Searcher. log may be nil (a discard
// logger is used); it must not write to stdout, which is the MCP transport.
func New(r Searcher, log *slog.Logger, opts ...Option) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	// Apply options first so the corpus reader is available when we build the
	// server-level instructions (which stamp in live coverage counts).
	s := &Server{searcher: r, log: log}
	for _, opt := range opts {
		opt(s)
	}
	s.brief = briefFor(s.jurisdiction)

	srv := mcp.NewServer(
		&mcp.Implementation{
			Name:    s.brief.name,
			Title:   s.brief.title,
			Version: version,
		},
		&mcp.ServerOptions{Logger: log, Instructions: buildInstructions(s.brief, s.corpus, log)},
	)
	s.mcp = srv

	mcp.AddTool(srv, &mcp.Tool{
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld, Title: "Guide: how to use " + s.brief.name},
		Name:        "guide",
		Description: s.brief.guideDesc,
	}, s.handleGuide)

	mcp.AddTool(srv, &mcp.Tool{
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld, Title: "Corpus status & coverage"},
		Name:        "corpus_status",
		Description: s.brief.statusDesc,
	}, s.handleCorpusStatus)

	mcp.AddTool(srv, &mcp.Tool{
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld, Title: "Corpus quality gaps"},
		Name:        "quality_gaps",
		Description: s.brief.gapsDesc,
	}, s.handleQualityGaps)

	mcp.AddTool(srv, &mcp.Tool{
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld, Title: "Open a legal document"},
		Name:        "document",
		Description: s.brief.documentDesc,
	}, s.handleDocument)

	mcp.AddTool(srv, &mcp.Tool{
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld, Title: "Search regulation evidence"},
		Name:        "search",
		Description: s.brief.searchDesc,
	}, s.handleSearch)

	return s
}

// version is the advertised server version. Kept simple (pre-release); bump as the
// surface stabilizes.
const version = "0.1.0"

// coverageReader is the optional capability used to stamp live corpus coverage into
// the instructions. dbCorpus implements it; fake/test corpora need not.
type coverageReader interface {
	Coverage(ctx context.Context) (coverageCounts, error)
}

// buildInstructions returns the jurisdiction's server brief, appending live coverage
// (documents / provisions / sources) when the corpus can report it, so a connecting
// model sees the real scale of the evidence. Read once at startup with a short
// timeout; any error falls back to the count-free base brief.
func buildInstructions(b brief, corpus CorpusReader, log *slog.Logger) string {
	cov, ok := corpus.(coverageReader)
	if !ok {
		return b.instructions
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cc, err := cov.Coverage(ctx)
	if err != nil {
		log.Warn("mcp: coverage counts for instructions", "err", err)
		return b.instructions
	}
	if cc.Docs == 0 {
		return b.instructions
	}
	return b.instructions + fmt.Sprintf(b.coverageFmt, cc.Docs, cc.Chunks, cc.Sources)
}

// closedWorld is the OpenWorldHint value for banhmi's tools: they query a bounded,
// known corpus, not an open-ended external world. It's a pointer because the MCP
// hint is *bool (unset ≠ false).
var closedWorld = false

// Run serves the MCP server over the given transport until ctx is cancelled. cmd/mcp
// passes an *mcp.StdioTransport so the server speaks JSON-RPC over stdin/stdout.
func (s *Server) Run(ctx context.Context, t mcp.Transport) error {
	return s.mcp.Run(ctx, t)
}

// HTTPHandler serves this MCP server over the Streamable HTTP transport for remote
// user-owned agents (Claude.ai, ChatGPT, Gemini, Grok). cmd/server mounts it; the
// same underlying server is reused for every session.
func (s *Server) HTTPHandler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp }, nil)
}

// --- search --------------------------------------------------------------------

// searchInput is the search tool's argument schema. TopK overrides the retriever
// default (0 = default).
type searchInput struct {
	Query          string `json:"query" jsonschema:"the legal question or keywords; English or Vietnamese both work (the index is multilingual)"`
	TopK           int    `json:"top_k,omitempty" jsonschema:"max ranked hits to return (0 = default)"`
	InForceOnly    *bool  `json:"in_force_only,omitempty" jsonschema:"default (omit): current law leads, with a small badged pass of non-current law after it; true: current law only (in force + partial); false: no validity filter, pure relevance (historical/admin)"`
	IncludeRelated *bool  `json:"include_related,omitempty" jsonschema:"also return chunks from confirmed related documents (default true)"`
	RelatedK       int    `json:"related_k,omitempty" jsonschema:"max related chunks (0 = MCP default)"`

	// Optional pre-filters (narrow which documents are eligible before ranking).
	AsOf       string   `json:"as_of,omitempty" jsonschema:"point-in-time (YYYY-MM-DD): return law in force ON that date (its effective window contains the date) instead of current-as-of-now; uses recorded effective dates, so documents without one are excluded"`
	IssuedFrom string   `json:"issued_from,omitempty" jsonschema:"only documents issued on or after this date (YYYY-MM-DD)"`
	IssuedTo   string   `json:"issued_to,omitempty" jsonschema:"only documents issued on or before this date (YYYY-MM-DD)"`
	Issuer     []string `json:"issuer,omitempty" jsonschema:"filter by issuing body — case-insensitive exact match on a hit's issuer value (e.g. Ngân hàng Nhà nước Việt Nam, or Bank Negara Malaysia)"`
	DocType    []string `json:"doc_type,omitempty" jsonschema:"filter by document type — case-insensitive exact match on a hit's doc_type (e.g. Thông tư / Nghị định in Vietnam, or Act / Policy Document in Malaysia)"`
}

// searchHit is one retrieved chunk shaped for the search tool: citation + snippet +
// số ký hiệu, with provenance ids and the fused score.
type searchHit struct {
	SoKyHieu       string           `json:"so_ky_hieu" jsonschema:"document number / identifier — e.g. 11/2026/TT-NHNN (Vietnam) or an Act / P.U. / regulator reference (Malaysia)"`
	Title          string           `json:"title,omitempty" jsonschema:"document summary / short title"`
	IssuedDate     string           `json:"issued_date,omitempty" jsonschema:"date the document was issued, YYYY-MM-DD"`
	Source         string           `json:"source,omitempty" jsonschema:"official source site, e.g. vbpl | congbao | sbv_hanoi (Vietnam) or agclom | bnm | sc (Malaysia)"`
	SourceURL      string           `json:"source_url,omitempty" jsonschema:"official source landing page for this document; a citable page, never a file download"`
	Cite           string           `json:"cite,omitempty" jsonschema:"ready-to-paste citation: provision + document number + validity + source link"`
	Location       string           `json:"location" jsonschema:"position within the document — e.g. Điều 7, Khoản 2 (Vietnam) or Section 5, (1) (Malaysia)"`
	ParentCitation string           `json:"parent_citation,omitempty" jsonschema:"enclosing provision (the Điều in Vietnam, the Section in Malaysia) — pass it to the document tool to read the whole provision"`
	ContextPrefix  string           `json:"context_prefix,omitempty" jsonschema:"deterministic contextual header used at index time"`
	Snippet        string           `json:"snippet" jsonschema:"the precise matched provision text (a sub-provision of a long article/section) — see provision for the whole enclosing article/section"`
	Provision      *provision       `json:"provision,omitempty" jsonschema:"the full enclosing article/section, verbatim — snippet is the precise match that ranked, provision.text is the whole article/section so the matched clause is never read out of context"`
	DocumentID     int64            `json:"document_id"`
	ChunkID        int64            `json:"chunk_id"`
	Score          float64          `json:"score" jsonschema:"RRF fusion score (higher is better)"`
	VectorRank     int              `json:"vector_rank,omitempty" jsonschema:"rank in the vector arm, 0 if absent"`
	BM25Rank       int              `json:"bm25_rank,omitempty" jsonschema:"rank in the BM25 arm, 0 if absent"`
	Validity       validityEvidence `json:"validity" jsonschema:"current validity status of the chunk/document"`
	Text           textProvenance   `json:"text_provenance" jsonschema:"text source and binding/review state"`
	Relations      []searchRelation `json:"relations,omitempty" jsonschema:"confirmed one-hop relations around the document; listed on the first hit of each document only (sibling hits share them)"`
}

// provision is the full enclosing Điều for a hit, reassembled from all of its chunks.
// Search ranks fine-grained chunks (a long Điều is split by Khoản/Điểm/Đoạn for
// retrieval precision); snippet stays the precise matched provision, while
// provision.text carries the whole article so the agent never reads a clause without
// the surrounding definitions, conditions, and exceptions of its Điều.
type provision struct {
	Citation  string `json:"citation" jsonschema:"the enclosing article/section, e.g. Điều 7 (Vietnam) or Section 5 (Malaysia)"`
	Text      string `json:"text,omitempty" jsonschema:"verbatim full text of the enclosing article/section (all its sub-provisions). Empty with truncated=true means it is too large to inline (e.g. an amendment law whose Điều 1 is the whole law) — use the snippet and open the document tool."`
	Truncated bool   `json:"truncated,omitempty" jsonschema:"true when the enclosing article/section is too large to inline; text is omitted — open the document tool (filter by this citation) for the full provision"`
}

// validityEvidence is current validity context. SectionID is present when the
// status is provision-scoped; otherwise it is document-level.
type validityEvidence struct {
	SectionID     int64  `json:"section_id,omitempty"`
	StatusCode    string `json:"status_code,omitempty"`
	StatusClass   string `json:"status_class,omitempty"`
	StatusLabel   string `json:"status_label,omitempty" jsonschema:"plain-English validity badge: In force | Partially in force | Expired/repealed | Not yet effective | Suspended"`
	EffectiveFrom string `json:"effective_from,omitempty"`
	EffectiveTo   string `json:"effective_to,omitempty"`
	Source        string `json:"source,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Warning       string `json:"warning,omitempty" jsonschema:"data-quality flag when the source's own validity dates are internally inconsistent (e.g. effective_from precedes issued_date — a source data-entry error). banhmi surfaces the contradiction and does NOT correct the date; verify the effective date against the document's enacting clause (Điều khoản thi hành)."`
}

// textProvenance summarizes the document_text rows behind a hit/document.
type textProvenance struct {
	HasBindingText    bool     `json:"has_binding_text"`
	HasNonBindingText bool     `json:"has_nonbinding_text"`
	NeedsReview       bool     `json:"needs_review"`
	Quality           string   `json:"quality,omitempty" jsonschema:"plain-English evidence quality, e.g. 'born-digital, binding' or 'OCR text, needs review'"`
	Authorities       []string `json:"authorities,omitempty"`
	Sources           []string `json:"sources,omitempty"`
	ExtractEngines    []string `json:"extract_engines,omitempty"`
	MaxConfidence     float64  `json:"max_confidence,omitempty"`
}

// searchRelation is confirmed graph evidence adjacent to a search hit.
type searchRelation struct {
	RelationID           int64             `json:"relation_id"`
	Direction            string            `json:"direction"`
	RelationType         string            `json:"relation_type"`
	Source               string            `json:"source,omitempty"`
	ToCitation           string            `json:"to_citation,omitempty"`
	DocumentID           int64             `json:"document_id,omitempty"`
	SoKyHieu             string            `json:"so_ky_hieu,omitempty"`
	Title                string            `json:"title,omitempty"`
	Resolved             bool              `json:"resolved"`
	TargetIndexed        bool              `json:"target_indexed"`
	TargetHasBindingText bool              `json:"target_has_binding_text"`
	TargetNeedsReview    bool              `json:"target_needs_review"`
	TargetValidity       *validityEvidence `json:"target_validity,omitempty"`
	TargetText           *textProvenance   `json:"target_text_provenance,omitempty"`
	Evidence             *relationEvidence `json:"evidence,omitempty"`
	RelationTypeRaw      *int32            `json:"relation_type_raw,omitempty"`
}

// relationEvidence is the stored evidence row behind a confirmed graph edge.
type relationEvidence struct {
	EvidenceID      int64   `json:"evidence_id,omitempty"`
	EvidenceKind    string  `json:"evidence_kind,omitempty"`
	Operator        string  `json:"operator,omitempty"`
	TargetText      string  `json:"target_text,omitempty"`
	TargetCitation  string  `json:"target_citation,omitempty"`
	Citation        string  `json:"citation,omitempty"`
	Snippet         string  `json:"snippet,omitempty"`
	SourceAuthority string  `json:"source_authority,omitempty"`
	Confidence      float64 `json:"confidence,omitempty"`
	Promoted        bool    `json:"promoted"`
}

// relatedHit is a matching chunk reached through a confirmed relation from a
// primary hit. It is not a rank boost and should be treated as adjacent context.
type relatedHit struct {
	BaseChunkID    int64            `json:"base_chunk_id" jsonschema:"the primary hit chunk that led to this relation"`
	BaseDocumentID int64            `json:"base_document_id" jsonschema:"the primary hit document that led to this relation"`
	BaseSoKyHieu   string           `json:"base_so_ky_hieu,omitempty" jsonschema:"số ký hiệu of the primary document"`
	RelationID     int64            `json:"relation_id"`
	Direction      string           `json:"direction"`
	RelationType   string           `json:"relation_type"`
	Source         string           `json:"source,omitempty" jsonschema:"provenance of the relation edge"`
	ToCitation     string           `json:"to_citation,omitempty"`
	SoKyHieu       string           `json:"so_ky_hieu" jsonschema:"số ký hiệu of the related document"`
	Title          string           `json:"title,omitempty" jsonschema:"summary (trích yếu) of the related document"`
	SourceURL      string           `json:"source_url,omitempty" jsonschema:"official source landing page for the related document (view on VBPL/Cong Bao/SBV Hanoi); a citable page, never a file download"`
	Cite           string           `json:"cite,omitempty" jsonschema:"ready-to-paste citation for the related provision: provision + số ký hiệu + validity + source link"`
	StatusClass    string           `json:"status_class,omitempty" jsonschema:"validity status of the related document"`
	EffectiveDate  string           `json:"effective_date,omitempty" jsonschema:"current effective date if known"`
	Validity       validityEvidence `json:"validity" jsonschema:"current validity of the related document"`
	Text           textProvenance   `json:"text_provenance" jsonschema:"text source and binding/review state of the related document"`
	Location       string           `json:"location" jsonschema:"position within the related document"`
	ContextPrefix  string           `json:"context_prefix,omitempty"`
	Snippet        string           `json:"snippet" jsonschema:"preview of the related provision; open the document tool for full text"`
	DocumentID     int64            `json:"document_id"`
	ChunkID        int64            `json:"chunk_id"`
	Rank           int              `json:"rank,omitempty" jsonschema:"1-based rank of this chunk within its relation (vector order)"`
}

// gap is a DB-backed reason the evidence is incomplete or should abstain.
type gap struct {
	Kind         string `json:"kind"`
	Message      string `json:"message,omitempty"`
	BlocksAnswer bool   `json:"blocks_answer"`
	DocumentID   int64  `json:"document_id,omitempty"`
	SoKyHieu     string `json:"so_ky_hieu,omitempty"`
	Title        string `json:"title,omitempty"`
	RelationID   int64  `json:"relation_id,omitempty"`
	RelationType string `json:"relation_type,omitempty"`
}

// scopeEvidence is the configured domain-scope signal attached to search output.
type scopeEvidence struct {
	Checked        bool     `json:"checked"`
	InDomain       bool     `json:"in_domain"`
	MatchedTerms   []string `json:"matched_terms,omitempty"`
	KnownReference bool     `json:"known_reference"`
}

// searchOutput is the search tool's structured result: the top hits with no LLM
// synthesis (useful even when no LLM is configured).
type searchOutput struct {
	Hits        []searchHit   `json:"hits" jsonschema:"các đoạn trích phù hợp nhất, đã xếp hạng"`
	RelatedHits []relatedHit  `json:"related_hits,omitempty" jsonschema:"đoạn trích liên quan qua quan hệ xác nhận; không phải rank boost"`
	Gaps        []gap         `json:"gaps,omitempty" jsonschema:"khoảng trống dữ liệu hoặc lý do cần abstain"`
	Abstain     bool          `json:"abstain" jsonschema:"true khi có gap chặn (xem gaps[] để biết lý do); hits vẫn luôn được trả về để bạn tự đánh giá — abstain không có nghĩa là hits sai"`
	Scope       scopeEvidence `json:"scope" jsonschema:"tín hiệu phạm vi từ config.scope_term và tham chiếu văn bản đã biết"`
}

// handleSearch is the search tool handler: parse → Search → shape the MCP result.
// No retrieval logic lives here. Search uses the retriever's in-force default; the
// search tool intentionally exposes only query + top_k.
func (s *Server) handleSearch(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, searchOutput, error) {
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return nil, searchOutput{}, fmt.Errorf("query is required")
	}

	ev, err := s.searcher.SearchEvidence(ctx, query, retrieve.SearchOpts{
		TopK:        in.TopK,
		InForceOnly: in.InForceOnly,
		RelatedK:    searchRelatedK(in),
		AsOf:        in.AsOf,
		IssuedFrom:  in.IssuedFrom,
		IssuedTo:    in.IssuedTo,
		Issuer:      in.Issuer,
		DocType:     in.DocType,
	})
	if err != nil {
		s.log.Error("mcp: search", "err", err)
		return nil, searchOutput{}, fmt.Errorf("search: %w", err)
	}

	return nil, searchOutput{
		Hits:        toSearchHits(ev.Hits),
		RelatedHits: toRelatedHits(ev.RelatedHits),
		Gaps:        toGaps(ev.Gaps),
		Abstain:     ev.Abstain,
		Scope:       toScopeEvidence(ev.Scope),
	}, nil
}

const defaultMCPRelatedK = 8

func searchRelatedK(in searchInput) int {
	if in.IncludeRelated != nil && !*in.IncludeRelated {
		return 0
	}
	if in.RelatedK > 0 {
		return in.RelatedK
	}
	return defaultMCPRelatedK
}

// --- shaping helpers -----------------------------------------------------------

// toSearchHits maps retrieved evidence to the search tool shape. Returns a non-nil
// empty slice so the JSON field is [] not null when nothing matched.
func toSearchHits(hits []retrieve.Hit) []searchHit {
	out := make([]searchHit, 0, len(hits))
	for _, h := range hits {
		v := toValidity(h.Validity)
		v.Warning = validityWarning(h.IssuedDate, v.EffectiveFrom)
		sh := searchHit{
			SoKyHieu:       h.DocNumber,
			Title:          h.Title,
			IssuedDate:     h.IssuedDate,
			Source:         h.Source,
			SourceURL:      h.SourceURL,
			Cite:           citeString(h.DocNumber, h.Citation, v.StatusLabel, h.SourceURL),
			Location:       h.Citation,
			ParentCitation: h.ParentCitation,
			ContextPrefix:  h.ContextPrefix,
			Snippet:        h.Content,
			DocumentID:     h.DocumentID,
			ChunkID:        h.ChunkID,
			Score:          h.Score,
			VectorRank:     h.VectorRank,
			BM25Rank:       h.BM25Rank,
			Validity:       v,
			Text:           toTextProvenance(h.Text),
			Relations:      toSearchRelations(h.Relations, false),
		}
		if h.ArticleCitation != "" {
			sh.Provision = &provision{
				Citation:  h.ArticleCitation,
				Text:      h.Article,
				Truncated: h.ArticleTruncated,
			}
		}
		out = append(out, sh)
	}
	return out
}

// relationLabel maps a relation type to its agent-facing label. Unmapped raw VBPL
// diagram codes (vbpl_type_N) carry no agreed legal meaning, so they show as a neutral
// "related"; the exact code stays in relation_type_raw. We never guess a legal effect.
func relationLabel(t string) string {
	if strings.HasPrefix(t, "vbpl_type_") {
		return "related"
	}
	return t
}

// toSearchRelations shapes confirmed relations for the MCP surface. full=false (the
// search pack) keeps a compact graph signal — type, direction, target id/title,
// validity, and usability flags — but drops the verbose target text-provenance and
// evidence snippet so the evidence pack stays small; the agent opens the target via
// the document tool for that detail. full=true (the document tool) includes them.
func toSearchRelations(relations []retrieve.Relation, full bool) []searchRelation {
	out := make([]searchRelation, 0, len(relations))
	for _, rel := range relations {
		sr := searchRelation{
			RelationID:           rel.RelationID,
			Direction:            rel.Direction,
			RelationType:         relationLabel(rel.RelationType),
			Source:               rel.Source,
			ToCitation:           rel.ToCitation,
			DocumentID:           rel.DocumentID,
			SoKyHieu:             rel.DocNumber,
			Title:                rel.Title,
			Resolved:             rel.Resolved,
			TargetIndexed:        rel.TargetIndexed,
			TargetHasBindingText: rel.TargetHasBindingText,
			TargetNeedsReview:    rel.TargetNeedsReview,
			RelationTypeRaw:      rel.RelationTypeRaw,
		}
		if v := toValidity(rel.TargetValidity); v != (validityEvidence{}) {
			sr.TargetValidity = &v
		}
		if full {
			t := toTextProvenance(rel.TargetText)
			sr.TargetText = &t
			sr.Evidence = toRelationEvidence(rel.Evidence)
		}
		out = append(out, sr)
	}
	return out
}

// relatedSnippetMax caps related-hit snippet length. Related hits are adjacent graph
// context, not primary evidence, so a preview keeps the evidence pack compact; the
// agent opens the full provision via the document tool.
const relatedSnippetMax = 320

// clampSnippet returns a rune-safe preview of s up to maxRunes (… when truncated).
func clampSnippet(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	rs := []rune(s)
	if maxRunes <= 1 || len(rs) <= maxRunes {
		return s
	}
	return string(rs[:maxRunes-1]) + "…"
}

func toRelatedHits(hits []retrieve.RelatedHit) []relatedHit {
	out := make([]relatedHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, relatedHit{
			BaseChunkID:    h.BaseChunkID,
			BaseDocumentID: h.BaseDocumentID,
			BaseSoKyHieu:   h.BaseDocNumber,
			RelationID:     h.RelationID,
			Direction:      h.Direction,
			RelationType:   h.RelationType,
			Source:         h.Source,
			ToCitation:     h.ToCitation,
			SoKyHieu:       h.DocNumber,
			Title:          h.Title,
			SourceURL:      h.SourceURL,
			Cite:           citeString(h.DocNumber, h.Citation, statusLabel(h.Validity.StatusClass), h.SourceURL),
			StatusClass:    h.Validity.StatusClass,
			EffectiveDate:  h.Validity.EffectiveFrom,
			Validity:       toValidity(h.Validity),
			Text:           toTextProvenance(h.Text),
			Location:       h.Citation,
			ContextPrefix:  h.ContextPrefix,
			Snippet:        clampSnippet(h.Content, relatedSnippetMax),
			DocumentID:     h.DocumentID,
			ChunkID:        h.ChunkID,
			Rank:           h.Rank,
		})
	}
	return out
}

func toValidity(in retrieve.ValidityEvidence) validityEvidence {
	return validityEvidence{
		SectionID:     in.SectionID,
		StatusCode:    in.StatusCode,
		StatusClass:   in.StatusClass,
		StatusLabel:   statusLabel(in.StatusClass),
		EffectiveFrom: in.EffectiveFrom,
		EffectiveTo:   in.EffectiveTo,
		Source:        in.Source,
		Reason:        in.Reason,
	}
}

// validityWarning flags an internally-inconsistent source validity record: an
// effective date that precedes the document's own issue date is impossible and
// signals a source (e.g. VBPL) data-entry error. banhmi does NOT correct the
// date — it surfaces the contradiction so the connecting agent verifies the
// effective date against the document's enacting clause. Both dates are
// YYYY-MM-DD; returns "" when either is absent, unparseable, or consistent.
func validityWarning(issuedDate, effectiveFrom string) string {
	if issuedDate == "" || effectiveFrom == "" {
		return ""
	}
	const layout = "2006-01-02"
	issued, err1 := time.Parse(layout, issuedDate)
	eff, err2 := time.Parse(layout, effectiveFrom)
	if err1 != nil || err2 != nil {
		return ""
	}
	if eff.Before(issued) {
		return fmt.Sprintf("source effective date (%s) precedes the issue date (%s); a document cannot take effect before it is issued — likely a source data error. banhmi does not auto-correct it: verify the effective date against the document's own enacting clause (Điều khoản thi hành).", effectiveFrom, issuedDate)
	}
	return ""
}

// statusLabel maps a validity status_class to a short plain-English badge so a
// foreign model can weigh currency without reading Vietnamese. The structured
// dates/codes stay in their own fields; this is only a readable gloss. Classes
// mirror config.validity_status (in_force, partial, expired, not_yet, suspended).
func statusLabel(class string) string {
	switch strings.ToLower(strings.TrimSpace(class)) {
	case "in_force":
		return "In force"
	case "partial":
		return "Partially in force"
	case "expired":
		return "Expired/repealed"
	case "not_yet":
		return "Not yet effective"
	case "suspended":
		return "Suspended"
	case "unknown":
		return "Validity unknown — verify against the official source"
	case "":
		return ""
	default:
		return class
	}
}

// citeString builds a ready-to-paste citation from evidence already in the hit:
// the Vietnamese provision + số ký hiệu (verbatim legal data) plus an English
// status gloss and the official source link. It never invents text.
func citeString(docNumber, citation, status, sourceURL string) string {
	var parts []string
	if c := strings.TrimSpace(citation); c != "" {
		parts = append(parts, c)
	}
	if d := strings.TrimSpace(docNumber); d != "" {
		parts = append(parts, d)
	}
	cite := strings.Join(parts, ", ")
	if s := strings.TrimSpace(status); s != "" {
		cite += " — " + s
	}
	if u := strings.TrimSpace(sourceURL); u != "" {
		cite += " — " + u
	}
	return cite
}

// textQuality renders a short English evidence-quality gloss from the text
// provenance flags so a model can weight the evidence at a glance.
func textQuality(t textProvenance) string {
	var kind string
	switch {
	case t.HasBindingText:
		kind = "binding text"
	case t.HasNonBindingText:
		kind = "non-binding text"
	default:
		return ""
	}
	if t.NeedsReview {
		kind += ", needs review"
	}
	return kind
}

func toTextProvenance(in retrieve.TextEvidence) textProvenance {
	tp := textProvenance{
		HasBindingText:    in.HasBindingText,
		HasNonBindingText: in.HasNonBindingText,
		NeedsReview:       in.NeedsReview,
		Authorities:       append([]string(nil), in.Authorities...),
		Sources:           append([]string(nil), in.Sources...),
		ExtractEngines:    append([]string(nil), in.ExtractEngines...),
		MaxConfidence:     in.MaxConfidence,
	}
	tp.Quality = textQuality(tp)
	return tp
}

func toRelationEvidence(in retrieve.RelationEvidence) *relationEvidence {
	if in.EvidenceID == 0 &&
		in.EvidenceKind == "" &&
		in.Operator == "" &&
		in.TargetText == "" &&
		in.TargetCitation == "" &&
		in.Citation == "" &&
		in.Snippet == "" &&
		in.SourceAuthority == "" &&
		in.Confidence == 0 &&
		!in.Promoted {
		return nil
	}
	return &relationEvidence{
		EvidenceID:      in.EvidenceID,
		EvidenceKind:    in.EvidenceKind,
		Operator:        in.Operator,
		TargetText:      in.TargetText,
		TargetCitation:  in.TargetCitation,
		Citation:        in.Citation,
		Snippet:         in.Snippet,
		SourceAuthority: in.SourceAuthority,
		Confidence:      in.Confidence,
		Promoted:        in.Promoted,
	}
}

func toGaps(gaps []retrieve.Gap) []gap {
	out := make([]gap, 0, len(gaps))
	for _, g := range gaps {
		out = append(out, gap{
			Kind:         string(g.Kind),
			Message:      g.Message,
			BlocksAnswer: g.BlocksAnswer,
			DocumentID:   g.DocumentID,
			SoKyHieu:     g.DocNumber,
			Title:        g.Title,
			RelationID:   g.RelationID,
			RelationType: g.RelationType,
		})
	}
	return out
}

func toScopeEvidence(in retrieve.ScopeEvidence) scopeEvidence {
	return scopeEvidence{
		Checked:        in.Checked,
		InDomain:       in.InDomain,
		MatchedTerms:   append([]string(nil), in.MatchedTerms...),
		KnownReference: in.KnownReference,
	}
}
