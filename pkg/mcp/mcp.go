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
	"reflect"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
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
	version      string
	brief        brief
	behindProxy  bool
	filesListing map[string]func(externalID string) string
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

// WithVersion stamps the build version (e.g. "0.1.0-20260704") into the server
// so corpus_status can report what code+corpus is running.
func WithVersion(v string) Option {
	return func(s *Server) {
		s.version = v
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

// WithBehindProxy disables the SDK's localhost DNS-rebinding protection so the
// MCP handler works behind reverse proxies (Cloud Run, nginx, etc.)
// where the local listener address is loopback but the Host header is the
// proxy's public hostname.
func WithBehindProxy() Option {
	return func(s *Server) { s.behindProxy = true }
}

// WithCorpus injects a corpus reader for tests or alternate deployments.
func WithCorpus(c CorpusReader) Option {
	return func(s *Server) {
		s.corpus = c
	}
}

// WithFilesListingURL registers a source's stable files-listing URL builder: a
// permanent per-document endpoint on the official source whose GET returns
// fresh, short-lived direct download links. It surfaces as files_url on that
// source's entry in document sources. Keyed by source code, so registering is
// unconditional — only corpora holding that source's documents ever emit it.
func WithFilesListingURL(source string, build func(externalID string) string) Option {
	return func(s *Server) {
		if s.filesListing == nil {
			s.filesListing = make(map[string]func(string) string)
		}
		s.filesListing[source] = build
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
	// The listing builders ride on the corpus reader (option order-independent:
	// both are plain fields until this point).
	if dc, ok := s.corpus.(dbCorpus); ok {
		dc.filesListing = s.filesListing
		s.corpus = dc
	}

	srv := mcp.NewServer(
		&mcp.Implementation{
			Name:    s.brief.name,
			Title:   s.brief.title,
			Version: s.effectiveVersion(),
		},
		&mcp.ServerOptions{Logger: log, Instructions: buildInstructions(s.brief, s.corpus, log)},
	)
	s.mcp = srv

	mcp.AddTool(srv, &mcp.Tool{
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld, DestructiveHint: &notDestructive, Title: "Guide: how to use " + s.brief.name},
		Name:        "guide",
		Description: s.brief.guideDesc,
	}, s.handleGuide)

	mcp.AddTool(srv, &mcp.Tool{
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld, DestructiveHint: &notDestructive, Title: "Corpus status & coverage"},
		Name:        "corpus_status",
		Description: s.brief.statusDesc,
	}, s.handleCorpusStatus)

	mcp.AddTool(srv, &mcp.Tool{
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld, DestructiveHint: &notDestructive, Title: "Corpus quality gaps"},
		Name:        "quality_gaps",
		Description: s.brief.gapsDesc,
	}, s.handleQualityGaps)

	mcp.AddTool(srv, &mcp.Tool{
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld, DestructiveHint: &notDestructive, Title: "Open a legal document"},
		Name:        "document",
		Description: s.brief.documentDesc,
		InputSchema: inputSchemaFor[documentInput](),
	}, s.handleDocument)

	mcp.AddTool(srv, &mcp.Tool{
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld, DestructiveHint: &notDestructive, Title: "Search regulation evidence"},
		Name:        "search",
		Description: s.brief.searchDesc,
		InputSchema: annotateIssuerHint(inputSchemaFor[searchInput](), s.corpus, log),
	}, s.handleSearch)

	return s
}

// issuerLister is the optional corpus capability behind the issuer-filter hint.
type issuerLister interface {
	Issuers(ctx context.Context) ([]string, error)
}

// annotateIssuerHint appends the corpus's real issuer values to the search
// schema's issuer description — or, for corpora without issuer metadata, a
// warning to omit the filter. Agents guess issuer strings; showing the actual
// vocabulary (read once at startup) prevents filtered-to-zero searches.
func annotateIssuerHint(schema any, corpus CorpusReader, log *slog.Logger) any {
	s, ok := schema.(*jsonschema.Schema)
	if !ok || s == nil {
		return schema
	}
	lister, ok := corpus.(issuerLister)
	if !ok {
		return schema
	}
	prop, ok := s.Properties["issuer"]
	if !ok {
		return schema
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	issuers, err := lister.Issuers(ctx)
	if err != nil {
		log.Warn("mcp: issuer hint unavailable", "err", err)
		return schema
	}
	if len(issuers) == 0 {
		prop.Description += ". NOTE: this corpus has no issuer metadata — omit this filter; any issuer value returns zero hits"
		return schema
	}
	prop.Description += ". Issuer values in this corpus include: " + strings.Join(issuers, " | ")
	return schema
}

// inputSchemaFor infers the JSON Schema for T exactly as mcp.AddTool would,
// then collapses each optional field's ["null", X] type union to the bare X.
// Optionality is already conveyed by absence from required; strict tool
// scanners (e.g. ChatGPT's plugin review) read the union form as untyped.
func inputSchemaFor[T any]() any {
	schema, err := jsonschema.ForType(reflect.TypeFor[T](), &jsonschema.ForOptions{})
	if err != nil {
		return nil // AddTool falls back to its own inference
	}
	for _, prop := range schema.Properties {
		if len(prop.Types) != 2 {
			continue
		}
		switch {
		case prop.Types[0] == "null":
			prop.Type = prop.Types[1]
		case prop.Types[1] == "null":
			prop.Type = prop.Types[0]
		default:
			continue
		}
		prop.Types = nil
	}
	return schema
}

// defaultVersion is the fallback when WithVersion is not called.
const defaultVersion = "0.2.0"

func (s *Server) effectiveVersion() string {
	if s.version != "" {
		return s.version
	}
	return defaultVersion
}

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

// notDestructive is the DestructiveHint for every tool. All tools are read-only,
// so the hint is implied — but directory reviews (e.g. ChatGPT's) require all
// three annotations set explicitly on every tool, and unset ≠ false.
var notDestructive = false

// Run serves the MCP server over the given transport until ctx is cancelled. cmd/mcp
// passes an *mcp.StdioTransport so the server speaks JSON-RPC over stdin/stdout.
func (s *Server) Run(ctx context.Context, t mcp.Transport) error {
	return s.mcp.Run(ctx, t)
}

// HTTPHandler serves this MCP server over the Streamable HTTP transport for remote
// user-owned agents (Claude.ai, ChatGPT, Gemini, Grok). cmd/server mounts it; the
// same underlying server is reused for every request.
//
// Stateless: every tool (guide, corpus_status, quality_gaps, search, document) is a
// pure read-only query, and the server pushes nothing — no notifications,
// subscriptions, sampling, elicitation, or progress. So there is no session state
// worth keeping, and keeping it would cost us: session state lives in one process's
// memory, so a second task (rolling deploy, extra instance) could only serve a
// session-bearing request with sticky routing CloudFront does not do, and every
// deploy would invalidate live sessions. Stateless makes any topology safe.
// Per the MCP spec a stateless server answers the GET/SSE stream with 405 — we
// stream nothing, so nothing is lost. Revisit only if a server-initiated feature
// (resource subscriptions, progress on long searches) is ever added.
func (s *Server) HTTPHandler() http.Handler {
	opts := &mcp.StreamableHTTPOptions{
		Stateless:                  true,
		DisableLocalhostProtection: s.behindProxy,
	}
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp }, opts)
}

// --- search --------------------------------------------------------------------

// searchInput is the search tool's argument schema. TopK overrides the retriever
// default (0 = default).
type searchInput struct {
	Query          string `json:"query" jsonschema:"the legal question or keywords — ALWAYS in the corpus's binding legal language (the tool description states which); queries in other languages return degraded rankings"`
	TopK           int    `json:"top_k,omitempty" jsonschema:"max ranked hits to return (0 = default)"`
	Detail         string `json:"detail,omitempty" jsonschema:"response size — compact: discovery pass (metadata + snippet + cite + validity badge; no provision text, relations, or related_hits — cheapest); standard (default): adds relations, related_hits, and full validity, but NOT the inline full provision text (open the document tool with provision.citation to read the whole article/section); full: everything including the full enclosing provision text inline on every hit (largest — prefer the two-pass compact/standard → document pattern)"`
	InForceOnly    *bool  `json:"in_force_only,omitempty" jsonschema:"default (omit): current law leads, with a small badged pass of non-current law after it; true: current law only (in force + partial); false: no validity filter, pure relevance (historical/admin)"`
	IncludeRelated *bool  `json:"include_related,omitempty" jsonschema:"also return chunks from confirmed related documents (default true; ignored when detail=compact — compact never returns related_hits)"`
	RelatedK       int    `json:"related_k,omitempty" jsonschema:"max related chunks (0 = MCP default)"`

	// Optional pre-filters (narrow which documents are eligible before ranking).
	AsOf       string   `json:"as_of,omitempty" jsonschema:"point-in-time (YYYY-MM-DD): return law in force ON that date (its effective window contains the date) instead of current-as-of-now; uses recorded effective dates, so documents without one are excluded"`
	IssuedFrom string   `json:"issued_from,omitempty" jsonschema:"only documents issued on or after this date (YYYY-MM-DD)"`
	IssuedTo   string   `json:"issued_to,omitempty" jsonschema:"only documents issued on or before this date (YYYY-MM-DD)"`
	Issuer     []string `json:"issuer,omitempty" jsonschema:"filter by issuing body — case-insensitive substring match on a hit's issuer value (e.g. Ngân hàng Nhà nước matches Ngân hàng Nhà nước Việt Nam)"`
	DocType    []string `json:"doc_type,omitempty" jsonschema:"filter by document type — case-insensitive exact match on a hit's doc_type value (copy the vocabulary from search hits' doc_type)"`
}

// searchHit is one retrieved chunk shaped for the search tool: citation + snippet +
// document number, with provenance ids and the fused score.
type searchHit struct {
	DocNumber      string           `json:"doc_number" jsonschema:"document number / identifier exactly as printed by the official source"`
	Title          string           `json:"title,omitempty" jsonschema:"document summary / short title"`
	IssuedDate     string           `json:"issued_date,omitempty" jsonschema:"date the document was issued, YYYY-MM-DD"`
	Source         string           `json:"source,omitempty" jsonschema:"official source site code — corpus_status lists this corpus's sources"`
	SourceURL      string           `json:"source_url,omitempty" jsonschema:"official source landing page for this document; a citable page, never a file download"`
	Cite           string           `json:"cite,omitempty" jsonschema:"ready-to-paste citation: provision + document number + validity + source link"`
	Location       string           `json:"location" jsonschema:"position within the document, in the corpus's native citation vocabulary (article/section/clause)"`
	ParentCitation string           `json:"parent_citation,omitempty" jsonschema:"enclosing provision (the whole article/section this chunk belongs to) — pass it to the document tool to read the full provision"`
	ContextPrefix  string           `json:"context_prefix,omitempty" jsonschema:"deterministic contextual header used at index time"`
	Snippet        string           `json:"snippet" jsonschema:"the precise matched provision text (a sub-provision of a long article/section) — see provision for the whole enclosing article/section"`
	Provision      *provision       `json:"provision,omitempty" jsonschema:"the full enclosing article/section, verbatim — snippet is the precise match that ranked, provision.text is the whole article/section so the matched clause is never read out of context"`
	DocumentID     int64            `json:"document_id"`
	ChunkID        int64            `json:"chunk_id"`
	Score          float64          `json:"score" jsonschema:"RRF fusion score (higher is better)"`
	VectorRank     int              `json:"vector_rank,omitempty" jsonschema:"rank in the dense vector arm, 0 if absent"`
	BM25Rank       int              `json:"bm25_rank,omitempty" jsonschema:"rank in the BM25 lexical arm, 0 if absent"`
	Similarity     float64          `json:"similarity,omitempty" jsonschema:"dense vector cosine similarity in [0,1]; 0 if the hit came from the BM25 arm only"`
	BM25Score      float64          `json:"bm25_score,omitempty" jsonschema:"BM25 lexical score (sparse inner product); 0 if the hit came from the vector arm only"`
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
	Citation  string `json:"citation" jsonschema:"the enclosing article/section in the corpus's citation vocabulary — pass it as the document tool's citation filter to read the whole provision"`
	Text      string `json:"text,omitempty" jsonschema:"verbatim full text of the enclosing article/section (all its sub-provisions). Inlined only when detail=full; at the default detail=standard the text stays out of the response — open the document tool (filter by citation) to read it. Empty with truncated=true means it is too large to inline even at detail=full."`
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
	Warning       string `json:"warning,omitempty" jsonschema:"data-quality flag when the source's own metadata is self-contradictory. Two kinds: (1) validity dates are inconsistent (effective_from precedes issued_date — a source data-entry error); (2) the source still badges this document current law while a confirmed relation records another document as replacing/repealing it. banhmi surfaces the contradiction and NEVER overrides the badge or the date — open the named document(s) / the enacting clause (Điều khoản thi hành) and decide which text is operative."`
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
	DocNumber            string            `json:"doc_number,omitempty"`
	Title                string            `json:"title,omitempty"`
	Resolved             bool              `json:"resolved"`
	TargetIndexed        bool              `json:"target_indexed"`
	TargetHasBindingText bool              `json:"target_has_binding_text"`
	TargetNeedsReview    bool              `json:"target_needs_review"`
	TargetValidity       *validityEvidence `json:"target_validity,omitempty"`
	TargetText           *textProvenance   `json:"target_text_provenance,omitempty"`
	Evidence             *relationEvidence `json:"evidence,omitempty"`
	RelationTypeRaw      *int32            `json:"relation_type_raw,omitempty"`
	TargetAmendedBy      []string          `json:"target_amended_by,omitempty" jsonschema:"doc numbers of documents that further amend/replace this relation's target — a currency warning: the target itself has been amended, so open it (or the newest amender) with the document tool before relying on its text; its amendment_chain shows the full lineage"`
}

// relationEvidence is the stored evidence row behind a confirmed graph edge.
// relationEvidence records WHY banhmi believes a relation exists, so the agent can
// weigh it. The two evidence kinds carry different strength, and `snippet` means
// something different in each — read evidence_kind before trusting the edge.
type relationEvidence struct {
	EvidenceID   int64  `json:"evidence_id,omitempty"`
	EvidenceKind string `json:"evidence_kind,omitempty" jsonschema:"how this edge was established. 'structured_relation' = asserted by the official source's own reference metadata, NOT corroborated against the document's text; the source can be wrong, so treat it as a lead and verify. 'weak_relation' = derived from the amending document's own wording, and citation/snippet point at that wording."`
	Operator     string `json:"operator,omitempty" jsonschema:"the legal operation, e.g. sửa đổi, bổ sung / bãi bỏ / thay thế, or the source's relation code for structured evidence"`
	TargetText   string `json:"target_text,omitempty" jsonschema:"the target document as identified by the evidence — usually its số ký hiệu"`
	// TargetCitation is the provision inside the TARGET that the evidence names.
	TargetCitation string `json:"target_citation,omitempty" jsonschema:"the provision of the target document the evidence points at, when the evidence names one"`
	Citation       string `json:"citation,omitempty" jsonschema:"where the evidence sits INSIDE the amending document (e.g. 'Điều 50, Khoản 2') for weak_relation. For structured_relation this is a source sentinel such as 'vbpl:references', meaning the edge came from source metadata and no clause in the document was located."`
	Snippet        string `json:"snippet,omitempty" jsonschema:"for weak_relation, the verbatim sentence from the amending document that establishes the edge — read it, because a document number appearing in an amendment sentence is often a RECITAL of the target's own amendment history ('… của Luật X đã được sửa đổi theo Luật Y …' names X as the target and Y only as history), not a second target. For structured_relation this is the target document's TITLE, not a quotation of any clause."`
	// SourceAuthority and Confidence describe the source's own claim strength, never truth.
	SourceAuthority string  `json:"source_authority,omitempty" jsonschema:"provenance tier of the assertion, e.g. official_structured (the source's metadata) or official_text (the document body)"`
	Confidence      float64 `json:"confidence,omitempty" jsonschema:"how strongly the SOURCE asserts this edge, not how likely it is to be correct: official metadata is stored at 1 even when the document's text does not support it"`
	Promoted        bool    `json:"promoted" jsonschema:"true when this evidence was promoted to a confirmed relation in the graph"`
}

// relatedHit is a matching chunk reached through a confirmed relation from a
// primary hit. It is not a rank boost and should be treated as adjacent context.
type relatedHit struct {
	BaseChunkID    int64            `json:"base_chunk_id" jsonschema:"the primary hit chunk that led to this relation"`
	BaseDocumentID int64            `json:"base_document_id" jsonschema:"the primary hit document that led to this relation"`
	BaseDocNumber  string           `json:"base_doc_number,omitempty" jsonschema:"document number of the primary document"`
	RelationID     int64            `json:"relation_id"`
	Direction      string           `json:"direction"`
	RelationType   string           `json:"relation_type"`
	Source         string           `json:"source,omitempty" jsonschema:"provenance of the relation edge"`
	ToCitation     string           `json:"to_citation,omitempty"`
	DocNumber      string           `json:"doc_number" jsonschema:"document number of the related document"`
	Title          string           `json:"title,omitempty" jsonschema:"summary (trích yếu) of the related document"`
	SourceURL      string           `json:"source_url,omitempty" jsonschema:"official source landing page for the related document (view on VBPL/Cong Bao/SBV Hanoi); a citable page, never a file download"`
	Cite           string           `json:"cite,omitempty" jsonschema:"ready-to-paste citation for the related provision: provision + document number + validity + source link"`
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
	DocNumber    string `json:"doc_number,omitempty"`
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

// searchDetail selects how much of the evidence pack the search tool returns —
// progressive disclosure so an agent pays only for the phase it is in: compact for
// discovery, standard (the default) for reading, full for the complete legacy pack
// with inline provision text.
type searchDetail string

const (
	detailCompact  searchDetail = "compact"
	detailStandard searchDetail = "standard"
	detailFull     searchDetail = "full"
)

// parseSearchDetail maps the input string to a detail level. Empty means the
// standard default; ok=false reports an unknown value (also mapped to standard,
// never an error — a typo must not fail a legal query).
func parseSearchDetail(s string) (level searchDetail, ok bool) {
	switch searchDetail(strings.ToLower(strings.TrimSpace(s))) {
	case "", detailStandard:
		return detailStandard, true
	case detailCompact:
		return detailCompact, true
	case detailFull:
		return detailFull, true
	}
	return detailStandard, false
}

// handleSearch is the search tool handler: parse → Search → shape the MCP result.
// No retrieval logic lives here. Search uses the retriever's in-force default; the
// detail level only shapes the response — it never changes ranking.
func (s *Server) handleSearch(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, searchOutput, error) {
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return nil, searchOutput{}, fmt.Errorf("query is required")
	}

	detail, known := parseSearchDetail(in.Detail)
	if !known {
		s.log.Warn("mcp: unknown search detail level, using standard", "detail", in.Detail)
	}
	relatedK := searchRelatedK(in)
	if detail == detailCompact {
		relatedK = 0 // compact never returns related_hits; skip the retrieval work too
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
		return nil, searchOutput{}, fmt.Errorf("search: %w", err)
	}

	out := searchOutput{
		Hits:    toSearchHits(ev.Hits, detail),
		Gaps:    toGaps(ev.Gaps),
		Abstain: ev.Abstain,
		Scope:   toScopeEvidence(ev.Scope),
	}
	if detail != detailCompact {
		out.RelatedHits = toRelatedHits(ev.RelatedHits, detail)
	}
	return nil, out, nil
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

// toSearchHits maps retrieved evidence to the search tool shape at the given
// detail level. Returns a non-nil empty slice so the JSON field is [] not null
// when nothing matched.
//
// Detail shaping (see searchDetail): full keeps everything including the inline
// provision text and per-arm scoring diagnostics; standard (default) keeps the
// provision pointer (citation only — the document tool reads the text) and trims
// provenance to its flags + quality gloss; compact additionally drops relations,
// context_prefix, and the provision pointer, and trims validity to its badge.
// Data-quality signals (needs_review, validity.warning) survive every level —
// a smaller response must never hide weak data.
func toSearchHits(hits []retrieve.Hit, detail searchDetail) []searchHit {
	out := make([]searchHit, 0, len(hits))
	for _, h := range hits {
		v := toValidity(h.Validity)
		v.Warning = joinWarnings(
			validityWarning(h.IssuedDate, v.EffectiveFrom),
			supersededWarning(h.Validity.SupersededBy),
		)
		sh := searchHit{
			DocNumber:      h.DocNumber,
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
			Validity:       v,
			Text:           toTextProvenance(h.Text),
		}
		if h.ArticleCitation != "" {
			// The truncated flag only means something when text inlining was
			// attempted, i.e. at detail=full; standard carries the citation alone.
			sh.Provision = &provision{Citation: h.ArticleCitation}
		}
		switch detail {
		case detailFull:
			sh.VectorRank = h.VectorRank
			sh.BM25Rank = h.BM25Rank
			sh.Similarity = h.Similarity
			sh.BM25Score = h.BM25Score
			sh.Relations = toSearchRelations(h.Relations, false)
			if sh.Provision != nil {
				sh.Provision.Text = h.Article
				sh.Provision.Truncated = h.ArticleTruncated
			}
		case detailStandard:
			sh.Text = trimTextProvenance(sh.Text)
			sh.Relations = toSearchRelations(h.Relations, false)
		case detailCompact:
			sh.Text = trimTextProvenance(sh.Text)
			sh.Validity = compactValidity(sh.Validity)
			sh.ContextPrefix = ""
			sh.Provision = nil
		}
		out = append(out, sh)
	}
	return out
}

// trimTextProvenance keeps the binding/review flags and the plain-English quality
// gloss, dropping the diagnostic arrays (authorities, sources, extract engines)
// and the confidence number; detail=full restores them.
func trimTextProvenance(tp textProvenance) textProvenance {
	return textProvenance{
		HasBindingText:    tp.HasBindingText,
		HasNonBindingText: tp.HasNonBindingText,
		NeedsReview:       tp.NeedsReview,
		Quality:           tp.Quality,
	}
}

// compactValidity keeps only the validity badge (label + class) and any
// data-quality warning — the discovery pass needs the badge, and a warning is a
// red flag a smaller response shape must never hide.
func compactValidity(v validityEvidence) validityEvidence {
	return validityEvidence{
		StatusClass: v.StatusClass,
		StatusLabel: v.StatusLabel,
		Warning:     v.Warning,
	}
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
			DocNumber:            rel.DocNumber,
			Title:                rel.Title,
			Resolved:             rel.Resolved,
			TargetIndexed:        rel.TargetIndexed,
			TargetHasBindingText: rel.TargetHasBindingText,
			TargetNeedsReview:    rel.TargetNeedsReview,
			RelationTypeRaw:      rel.RelationTypeRaw,
			TargetAmendedBy:      rel.TargetAmendedBy,
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

// toRelatedHits shapes related-document previews. Only detail=full keeps the
// verbose provenance arrays; the standard level trims them (compact never returns
// related hits at all).
func toRelatedHits(hits []retrieve.RelatedHit, detail searchDetail) []relatedHit {
	out := make([]relatedHit, 0, len(hits))
	for _, h := range hits {
		text := toTextProvenance(h.Text)
		if detail != detailFull {
			text = trimTextProvenance(text)
		}
		out = append(out, relatedHit{
			BaseChunkID:    h.BaseChunkID,
			BaseDocumentID: h.BaseDocumentID,
			BaseDocNumber:  h.BaseDocNumber,
			RelationID:     h.RelationID,
			Direction:      h.Direction,
			RelationType:   h.RelationType,
			Source:         h.Source,
			ToCitation:     h.ToCitation,
			DocNumber:      h.DocNumber,
			Title:          h.Title,
			SourceURL:      h.SourceURL,
			Cite:           citeString(h.DocNumber, h.Citation, statusLabel(h.Validity.StatusClass), h.SourceURL),
			StatusClass:    h.Validity.StatusClass,
			EffectiveDate:  h.Validity.EffectiveFrom,
			Validity:       toValidity(h.Validity),
			Text:           text,
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
// supersededWarning reports the source's own contradiction: it still badges the
// document current law while a confirmed relation of a superseding type says
// another document displaced it. banhmi does NOT rewrite the badge — deriving
// repeal from a relation would be banhmi asserting a legal conclusion, and a
// `replaces` can be partial in practice. The agent gets both facts and decides.
func supersededWarning(supersededBy []string) string {
	if len(supersededBy) == 0 {
		return ""
	}
	return fmt.Sprintf("the source still badges this document current, but a confirmed relation records %s as replacing/repealing it — the source's own metadata is contradictory. banhmi does not override the badge: open the named document(s) and verify which text is operative before relying on this one.", strings.Join(supersededBy, ", "))
}

// joinWarnings concatenates the data-quality warnings that apply to one document
// so a second signal never silently replaces the first.
func joinWarnings(ws ...string) string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		if w != "" {
			out = append(out, w)
		}
	}
	return strings.Join(out, " ALSO: ")
}

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
			DocNumber:    g.DocNumber,
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
