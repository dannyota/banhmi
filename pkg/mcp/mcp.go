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
	mcp      *mcp.Server
	searcher Searcher
	corpus   CorpusReader
	log      *slog.Logger
}

// Option configures optional MCP capabilities.
type Option func(*Server)

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

	srv := mcp.NewServer(
		&mcp.Implementation{
			Name:    "banhmi",
			Title:   "banhmi — Vietnamese banking & technology regulation (evidence-only)",
			Version: version,
		},
		&mcp.ServerOptions{Logger: log, Instructions: buildInstructions(s.corpus, log)},
	)
	s.mcp = srv

	mcp.AddTool(srv, &mcp.Tool{
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld, Title: "Guide: how to use banhmi"},
		Name:        "guide",
		Description: "Read first. Explains what banhmi covers and how to use its evidence tools (search → document) to answer a Vietnamese banking/technology regulation question with exact citations — no local files or extra prompts needed.",
	}, s.handleGuide)

	mcp.AddTool(srv, &mcp.Tool{
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld, Title: "Corpus status & coverage"},
		Name:        "corpus_status",
		Description: "Live corpus coverage: document/chunk/embedding counts, relation coverage, and known data gaps. Call this to gauge how complete the evidence is before relying on it.",
	}, s.handleCorpusStatus)

	mcp.AddTool(srv, &mcp.Tool{
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld, Title: "Corpus quality gaps"},
		Name:        "quality_gaps",
		Description: "Exact database rows behind corpus-quality gaps (incomplete fetches, non-binding-only text, unresolved relations, etc.) so an agent can see what is missing. Evidence about completeness, not legal content.",
	}, s.handleQualityGaps)

	mcp.AddTool(srv, &mcp.Tool{
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld, Title: "Open a legal document"},
		Name:        "document",
		Description: "Open one legal document by id or số ký hiệu: full provision text (reassembled Điều/Khoản), validity periods, confirmed relations, verbatim incoming amendments, the official source link(s), and data gaps. Use it to read complete provisions when search returns fragments. Returns content + source links only — never file downloads.",
	}, s.handleDocument)

	mcp.AddTool(srv, &mcp.Tool{
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld, Title: "Search regulation evidence"},
		Name:        "search",
		Description: "Search Vietnamese banking & financial-technology regulation and return exact, citable evidence — ranked provisions (Điều/Khoản/Điểm) with their số ký hiệu, validity status, confirmed amendment/repeal relations, the official source link, and explicit gaps. No LLM synthesis: you get the source evidence and decide the answer. " +
			"Use this whenever the question touches Vietnamese banking/finance law or regulation, especially digital/technology topics: IT & system safety, cybersecurity & information security, data & personal-data protection, cloud & outsourcing, electronic transactions & e-signatures, digital banking & payment channels, and technology operations. You may query in English or Vietnamese — the index is multilingual.",
	}, s.handleSearch)

	return s
}

// version is the advertised server version. Kept simple (pre-release); bump as the
// surface stabilizes.
const version = "0.1.0"

// serverInstructionsBase is the server-level brief MCP hosts inject into the
// connecting model's context. banhmi has no agent of its own — this is a brief to the
// user's model: what banhmi is, why it can be trusted, when to reach for it, how to
// use it, and the evidence-only contract. buildInstructions appends live coverage.
const serverInstructionsBase = `banhmi is an evidence-only knowledge base for Vietnamese banking & financial-technology regulation. Reach for it whenever a question touches Vietnamese banking/finance law — especially digital & technology topics: IT and system safety, cybersecurity and information security, personal-data protection, cloud and IT outsourcing, electronic transactions and e-signatures, digital banking and payment channels, and technology operations. Ask in English or Vietnamese.

Why you can trust the results: the text is extracted verbatim from Vietnam's OFFICIAL government legal sources — VBPL (vbpl.vn, the Ministry of Justice national legal database), Công Báo (congbao.chinhphu.vn, the official government gazette), and the State Bank of Vietnam portal — never generated or paraphrased. Every hit and document includes source_url, the official source page, so you and the user can verify the exact wording against the authoritative origin. banhmi is evidence-only: it returns exact citations (Điều/Khoản/Điểm), validity status, confirmed amendment/repeal relations, provenance, and explicit gaps — it does NOT synthesize an answer and never hides weak data behind confident prose.

Flow: call search to get ranked provisions, each with its số ký hiệu, a plain-English validity badge, the official source link, and a ready-to-paste cite. Call document for a full provision, all official source links, the verbatim amending clauses, and a chronological timeline. Call corpus_status for live coverage, quality_gaps for what is missing, and guide for the full playbook.

When you answer (you, not banhmi): cite the exact provision and số ký hiệu, link the source_url so the user can verify, respect validity (never present repealed, superseded, or not-yet-effective text as current law), and surface gaps (gaps[], abstain, needs_review) instead of guessing.

Example questions: "IT system safety requirements for banks in Vietnam", "Quy định về bảo vệ dữ liệu cá nhân trong ngân hàng số", "which circular governs electronic KYC (eKYC) for banks".`

// coverageReader is the optional capability used to stamp live corpus coverage into
// the instructions. dbCorpus implements it; fake/test corpora need not.
type coverageReader interface {
	Coverage(ctx context.Context) (coverageCounts, error)
}

// buildInstructions returns the server brief, appending live coverage (documents /
// provisions / sources) when the corpus can report it, so a connecting model sees the
// real scale of the evidence. Read once at startup with a short timeout; any error
// falls back to the count-free base brief.
func buildInstructions(corpus CorpusReader, log *slog.Logger) string {
	cov, ok := corpus.(coverageReader)
	if !ok {
		return serverInstructionsBase
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cc, err := cov.Coverage(ctx)
	if err != nil {
		log.Warn("mcp: coverage counts for instructions", "err", err)
		return serverInstructionsBase
	}
	if cc.Docs == 0 {
		return serverInstructionsBase
	}
	return serverInstructionsBase + fmt.Sprintf(
		"\n\nCoverage right now: banhmi has extracted and indexed %d official documents (%d provisions) across %d government sources — call corpus_status for the live, detailed breakdown.",
		cc.Docs, cc.Chunks, cc.Sources)
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
	Issuer     []string `json:"issuer,omitempty" jsonschema:"filter by issuing body — case-insensitive exact match on a hit's issuer value (e.g. Ngân hàng Nhà nước Việt Nam)"`
	DocType    []string `json:"doc_type,omitempty" jsonschema:"filter by document type — case-insensitive exact match on a hit's doc_type (e.g. Thông tư, Nghị định, Quyết định)"`
}

// searchHit is one retrieved chunk shaped for the search tool: citation + snippet +
// số ký hiệu, with provenance ids and the fused score.
type searchHit struct {
	SoKyHieu       string           `json:"so_ky_hieu" jsonschema:"document number (số ký hiệu), e.g. 11/2026/TT-NHNN"`
	Title          string           `json:"title,omitempty" jsonschema:"document summary (trích yếu)"`
	IssuedDate     string           `json:"issued_date,omitempty" jsonschema:"date the document was issued (ngày ban hành), YYYY-MM-DD"`
	Source         string           `json:"source,omitempty" jsonschema:"official source site: vbpl | congbao | sbv_hanoi"`
	SourceURL      string           `json:"source_url,omitempty" jsonschema:"official source landing page for this document (view on VBPL/Cong Bao/SBV Hanoi); a citable page, never a file download"`
	Cite           string           `json:"cite,omitempty" jsonschema:"ready-to-paste citation: provision + số ký hiệu + validity + source link"`
	Location       string           `json:"location" jsonschema:"position within the document, e.g. Điều 7, Khoản 2, Điểm a"`
	ParentCitation string           `json:"parent_citation,omitempty" jsonschema:"parent provision (Điều/Khoản) — pass this to the document tool to read the whole provision"`
	ContextPrefix  string           `json:"context_prefix,omitempty" jsonschema:"deterministic contextual header used at index time"`
	Snippet        string           `json:"snippet" jsonschema:"the provision text excerpt"`
	DocumentID     int64            `json:"document_id"`
	ChunkID        int64            `json:"chunk_id"`
	Score          float64          `json:"score" jsonschema:"RRF fusion score (higher is better)"`
	VectorRank     int              `json:"vector_rank,omitempty" jsonschema:"rank in the vector arm, 0 if absent"`
	BM25Rank       int              `json:"bm25_rank,omitempty" jsonschema:"rank in the BM25 arm, 0 if absent"`
	Validity       validityEvidence `json:"validity" jsonschema:"current validity status of the chunk/document"`
	Text           textProvenance   `json:"text_provenance" jsonschema:"text source and binding/review state"`
	Relations      []searchRelation `json:"relations,omitempty" jsonschema:"confirmed one-hop relations around the document"`
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
	BM25Rank       int              `json:"bm25_rank,omitempty"`
	BM25Score      float64          `json:"bm25_score,omitempty"`
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
		out = append(out, searchHit{
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
		})
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
			BM25Rank:       h.BM25Rank,
			BM25Score:      h.BM25Score,
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
