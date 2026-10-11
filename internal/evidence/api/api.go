// Package api defines the transport-neutral evidence contract.
package api

import "context"

// Operations is the complete read-only evidence surface.
type Operations interface {
	Guide(context.Context, GuideInput) (GuideOutput, error)
	CorpusStatus(context.Context, CorpusStatusInput) (CorpusStatusOutput, error)
	QualityGaps(context.Context, QualityGapsInput) (QualityGapsOutput, error)
	Search(context.Context, SearchInput) (SearchOutput, error)
	Document(context.Context, DocumentInput) (DocumentOutput, error)
}

// StartupMetadata carries jurisdiction-specific presentation data without
// coupling evidence operations to corpus workers or transports.
type StartupMetadata struct {
	Country                string
	Coverage               string
	IndexedLanguage        string
	BindingLanguage        string
	IssuerVocabulary       []string
	DocumentTypeVocabulary []string
}

// ReleasePin holds one release selection for the lifetime of a request. Close
// must be safe to call more than once. Callers propagate Close failures.
type ReleasePin interface {
	ReleaseID() string
	Operations() Operations
	Close() error
}

// ReleaseResolver pins the requested release for one country. An empty
// requested release selects the active release.
type ReleaseResolver interface {
	Pin(context.Context, string, string) (ReleasePin, error)
}

type GuideInput struct{}

type GuideTool struct {
	Name string `json:"name"`
	Use  string `json:"use"`
}

type GuideOutput struct {
	Purpose          string      `json:"purpose"`
	RecommendedFlow  []string    `json:"recommended_flow"`
	Tools            []GuideTool `json:"tools"`
	EvidenceContract []string    `json:"evidence_contract"`
}

type CorpusStatusInput struct{}

type CorpusDocStats struct {
	Total              int64 `json:"total"`
	Indexed            int64 `json:"indexed"`
	Unindexed          int64 `json:"unindexed"`
	NonBindingOnly     int64 `json:"non_binding_only_unindexed"`
	IndexedNoBinding   int64 `json:"indexed_without_binding_text"`
	NeedsReviewTextDoc int64 `json:"needs_review_text_docs"`
	RelationContext    int64 `json:"relation_context_unindexed"`
}

type CorpusChunkStats struct {
	Total                int64 `json:"total"`
	ConfiguredEmbeddings int64 `json:"configured_embeddings"`
	AllEmbeddings        int64 `json:"all_embeddings"`
}

type CorpusRelationStats struct {
	ConfirmedEdges  int64 `json:"confirmed_edges"`
	ResolvedTargets int64 `json:"resolved_targets"`
	StubTargets     int64 `json:"stub_targets"`
	IndexedTargets  int64 `json:"indexed_targets"`
	TargetTextGaps  int64 `json:"target_text_gaps"`
}

type CorpusFetchStats struct {
	Total          int64              `json:"total"`
	Complete       int64              `json:"complete"`
	Partial        int64              `json:"partial"`
	Error          int64              `json:"error"`
	NotComplete    int64              `json:"not_complete"`
	ArtifactsError int64              `json:"artifacts_error"`
	ArtifactsDead  int64              `json:"artifacts_dead"`
	BySource       []SourceFetchStats `json:"by_source,omitempty"`
}

type SourceFetchStats struct {
	Source      string `json:"source"`
	Total       int64  `json:"total"`
	Complete    int64  `json:"complete"`
	Partial     int64  `json:"partial"`
	Error       int64  `json:"error"`
	NotComplete int64  `json:"not_complete"`
}

type CorpusGapStats struct {
	DocsWithoutCurrentValidity int64 `json:"docs_without_current_validity"`
	SectionValidityRows        int64 `json:"section_validity_rows"`
	TextNeedsReviewDocs        int64 `json:"text_needs_review_docs"`
	NonBindingOnlyUnindexed    int64 `json:"non_binding_only_unindexed_docs"`
	UnresolvedRelationTargets  int64 `json:"unresolved_relation_targets"`
	RelationTargetTextGaps     int64 `json:"relation_target_text_gaps"`
	MojibakeChunks             int64 `json:"mojibake_chunks"`
}

type CorpusStatusOutput struct {
	Version     string              `json:"version,omitempty"`
	SearchReady bool                `json:"search_ready"`
	EmbedModel  string              `json:"embed_model"`
	Docs        CorpusDocStats      `json:"docs"`
	Chunks      CorpusChunkStats    `json:"chunks"`
	Relations   CorpusRelationStats `json:"relations"`
	Fetch       CorpusFetchStats    `json:"fetch"`
	Gaps        CorpusGapStats      `json:"gaps"`
	Notes       []string            `json:"notes,omitempty"`
}

type QualityGapsInput struct {
	Category string `json:"category,omitempty" jsonschema:"optional category: all, fetch, non_binding, mojibake, partial_validity, unresolved_relations, relation_target_text"`
	Limit    int    `json:"limit,omitempty" jsonschema:"maximum rows per category (0 = default, max 100)"`
}

type QualityGapsOutput struct {
	Limit                  int                         `json:"limit"`
	Categories             []string                    `json:"categories"`
	Summary                CorpusGapStats              `json:"summary"`
	FetchIncomplete        []QualityFetchGap           `json:"fetch_incomplete,omitempty"`
	NonBindingOnly         []QualityTextGap            `json:"non_binding_only,omitempty"`
	MojibakeChunks         []QualityChunkGap           `json:"mojibake_chunks,omitempty"`
	PartialValidity        []QualityValidityGap        `json:"partial_validity,omitempty"`
	UnresolvedRelations    []QualityUnresolvedRelation `json:"unresolved_relations,omitempty"`
	RelationTargetTextGaps []QualityRelationTextGap    `json:"relation_target_text_gaps,omitempty"`
	Notes                  []string                    `json:"notes,omitempty"`
}

type QualityFetchGap struct {
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

type QualityTextGap struct {
	DocumentID     int64  `json:"document_id"`
	DocNumber      string `json:"doc_number,omitempty"`
	Title          string `json:"title,omitempty"`
	NeedsReview    bool   `json:"needs_review"`
	Authorities    string `json:"authorities,omitempty"`
	ExtractEngines string `json:"extract_engines,omitempty"`
}

type QualityChunkGap struct {
	ChunkID    int64  `json:"chunk_id"`
	DocumentID int64  `json:"document_id"`
	DocNumber  string `json:"doc_number,omitempty"`
	Location   string `json:"location,omitempty"`
	Sample     string `json:"sample,omitempty"`
}

type QualityValidityGap struct {
	DocumentID  int64  `json:"document_id"`
	DocNumber   string `json:"doc_number,omitempty"`
	Title       string `json:"title,omitempty"`
	StatusClass string `json:"status_class"`
	Chunks      int64  `json:"chunks"`
}

type QualityUnresolvedRelation struct {
	RefKey   string `json:"ref_key"`
	Label    string `json:"label,omitempty"`
	Edges    int64  `json:"edges"`
	FromDocs string `json:"from_docs,omitempty"`
}

type QualityRelationTextGap struct {
	DocumentID int64  `json:"document_id"`
	DocNumber  string `json:"doc_number,omitempty"`
	Title      string `json:"title,omitempty"`
	Edges      int64  `json:"edges"`
	HasAnyText bool   `json:"has_any_text"`
}

type SearchInput struct {
	Query          string   `json:"query" jsonschema:"the legal question or keywords — ALWAYS in the corpus's binding legal language (the tool description states which); queries in other languages return degraded rankings"`
	TopK           int      `json:"top_k,omitempty" jsonschema:"max ranked hits to return (0 = default)"`
	Detail         string   `json:"detail,omitempty" jsonschema:"response size — compact: discovery pass (metadata + snippet + cite + validity badge; no provision text, relations, or related_hits — cheapest); standard (default): adds relations, related_hits, and full validity, but NOT the inline full provision text (open the document tool with provision.citation to read the whole article/section); full: everything including the full enclosing provision text inline on every hit (largest — prefer the two-pass compact/standard → document pattern)"`
	InForceOnly    *bool    `json:"in_force_only,omitempty" jsonschema:"default (omit): current law leads, with a small badged pass of non-current law after it; true: current law only (in force + partial); false: no validity filter, pure relevance (historical/admin)"`
	IncludeRelated *bool    `json:"include_related,omitempty" jsonschema:"also return chunks from confirmed related documents (default true; ignored when detail=compact — compact never returns related_hits)"`
	RelatedK       int      `json:"related_k,omitempty" jsonschema:"max related chunks (0 = MCP default)"`
	AsOf           string   `json:"as_of,omitempty" jsonschema:"point-in-time (YYYY-MM-DD): return law in force ON that date (its effective window contains the date) instead of current-as-of-now; uses recorded effective dates, so documents without one are excluded"`
	IssuedFrom     string   `json:"issued_from,omitempty" jsonschema:"only documents issued on or after this date (YYYY-MM-DD)"`
	IssuedTo       string   `json:"issued_to,omitempty" jsonschema:"only documents issued on or before this date (YYYY-MM-DD)"`
	Issuer         []string `json:"issuer,omitempty" jsonschema:"filter by issuing body — case-insensitive substring match on a hit's issuer value (e.g. Ngân hàng Nhà nước matches Ngân hàng Nhà nước Việt Nam)"`
	DocType        []string `json:"doc_type,omitempty" jsonschema:"filter by document type — case-insensitive exact match on a hit's doc_type value (copy the vocabulary from search hits' doc_type)"`
	ReleaseID      string   `json:"release_id,omitempty" jsonschema:"optional immutable corpus release; unavailable releases fail explicitly"`
}

type SearchOutput struct {
	Hits        []SearchHit   `json:"hits" jsonschema:"các đoạn trích phù hợp nhất, đã xếp hạng"`
	RelatedHits []RelatedHit  `json:"related_hits,omitempty" jsonschema:"đoạn trích liên quan qua quan hệ xác nhận; không phải rank boost"`
	Gaps        []Gap         `json:"gaps,omitempty" jsonschema:"khoảng trống dữ liệu hoặc lý do cần abstain"`
	Abstain     bool          `json:"abstain" jsonschema:"true khi có gap chặn (xem gaps[] để biết lý do); hits vẫn luôn được trả về để bạn tự đánh giá — abstain không có nghĩa là hits sai"`
	Scope       ScopeEvidence `json:"scope" jsonschema:"tín hiệu phạm vi từ config.scope_term và tham chiếu văn bản đã biết"`
	ReleaseID   string        `json:"release_id,omitempty"`
}

type DocumentInput struct {
	DocumentID int64    `json:"document_id,omitempty" jsonschema:"silver.document id; use this when search returned document_id"`
	DocNumber  string   `json:"doc_number,omitempty" jsonschema:"document number / identifier exactly as printed by the official source (use the doc_number returned by search)"`
	Citation   string   `json:"citation,omitempty" jsonschema:"filter chunks by provision position, in the corpus's citation vocabulary — copy it from a search hit's location or parent_citation"`
	Include    []string `json:"include,omitempty" jsonschema:"response sections to return — any of: chunks (provision text), relations (confirmed graph edges), amendments (verbatim incoming amendment clauses), timeline (chronological history), provenance (per-artifact extraction rows). Omitted = chunks + relations + amendments + timeline. Reading one provision? Pass include=['chunks'] with a citation filter — the cheapest call. Document metadata, validity, sources, text_summary, and gaps are always returned."`
	Limit      int      `json:"limit,omitempty" jsonschema:"maximum chunks to return (0 = default)"`
	Offset     int      `json:"offset,omitempty" jsonschema:"offset to page through more chunks"`
	ReleaseID  string   `json:"release_id,omitempty" jsonschema:"optional immutable corpus release; unavailable releases fail explicitly"`
}

type DocumentOutput struct {
	Found              bool                     `json:"found"`
	Document           DocumentMeta             `json:"document,omitempty"`
	AlsoMatches        []DocAlternative         `json:"also_matches,omitempty" jsonschema:"other distinct documents carrying the same document number; re-query by document_id to open one"`
	Sources            []DocSource              `json:"sources,omitempty" jsonschema:"all official sources where this document is published (view-on-source links); never file downloads"`
	Files              []DocumentFile           `json:"files,omitempty" jsonschema:"downloadable official files observed for this document, one entry per unique content hash across sources"`
	ValidityPeriods    []DocumentValidityPeriod `json:"validity_periods,omitempty"`
	TextProvenance     []DocumentTextEvidence   `json:"text_provenance,omitempty"`
	TextSummary        TextProvenance           `json:"text_summary"`
	Chunks             []DocumentChunk          `json:"chunks"`
	Relations          []SearchRelation         `json:"relations,omitempty"`
	IncomingAmendments []AmendmentClause        `json:"incoming_amendments,omitempty"`
	AmendmentChain     []ChainNode              `json:"amendment_chain,omitempty" jsonschema:"transitive amendment lineage — who amends this document (depth 1), who amends those amenders (depth 2+). Present only when an amender is itself amended; the deepest/last node is the newest treatment: read it before relying on any amender's text"`
	Timeline           []TimelineEvent          `json:"timeline,omitempty" jsonschema:"chronological history: issued → effective → amended/replaced → expired, from validity + confirmed relations"`
	Gaps               []Gap                    `json:"gaps,omitempty"`
	Limit              int                      `json:"limit"`
	Offset             int                      `json:"offset"`
	NextOffset         int                      `json:"next_offset,omitempty"`
	ReleaseID          string                   `json:"release_id,omitempty"`
}

type SearchHit struct {
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
	Provision      *Provision       `json:"provision,omitempty" jsonschema:"the full enclosing article/section, verbatim — snippet is the precise match that ranked, provision.text is the whole article/section so the matched clause is never read out of context"`
	DocumentID     int64            `json:"document_id"`
	ChunkID        int64            `json:"chunk_id"`
	Score          float64          `json:"score" jsonschema:"RRF fusion score (higher is better)"`
	VectorRank     int              `json:"vector_rank,omitempty" jsonschema:"rank in the dense vector arm, 0 if absent"`
	BM25Rank       int              `json:"bm25_rank,omitempty" jsonschema:"rank in the BM25 lexical arm, 0 if absent"`
	Similarity     float64          `json:"similarity,omitempty" jsonschema:"dense vector cosine similarity in [0,1]; 0 if the hit came from the BM25 arm only"`
	BM25Score      float64          `json:"bm25_score,omitempty" jsonschema:"BM25 lexical score (sparse inner product); 0 if the hit came from the vector arm only"`
	Validity       ValidityEvidence `json:"validity" jsonschema:"current validity status of the chunk/document"`
	Text           TextProvenance   `json:"text_provenance" jsonschema:"text source and binding/review state"`
	Relations      []SearchRelation `json:"relations,omitempty" jsonschema:"confirmed one-hop relations around the document; listed on the first hit of each document only (sibling hits share them)"`
}

type Provision struct {
	Citation  string `json:"citation" jsonschema:"the enclosing article/section in the corpus's citation vocabulary — pass it as the document tool's citation filter to read the whole provision"`
	Text      string `json:"text,omitempty" jsonschema:"verbatim full text of the enclosing article/section (all its sub-provisions). Inlined only when detail=full; at the default detail=standard the text stays out of the response — open the document tool (filter by citation) to read it. Empty with truncated=true means it is too large to inline even at detail=full."`
	Truncated bool   `json:"truncated,omitempty" jsonschema:"true when the enclosing article/section is too large to inline; text is omitted — open the document tool (filter by this citation) for the full provision"`
}

type ValidityEvidence struct {
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

type TextProvenance struct {
	HasBindingText    bool     `json:"has_binding_text"`
	HasNonBindingText bool     `json:"has_nonbinding_text"`
	NeedsReview       bool     `json:"needs_review"`
	Quality           string   `json:"quality,omitempty" jsonschema:"plain-English evidence quality, e.g. 'born-digital, binding' or 'OCR text, needs review'"`
	Authorities       []string `json:"authorities,omitempty"`
	Sources           []string `json:"sources,omitempty"`
	ExtractEngines    []string `json:"extract_engines,omitempty"`
	MaxConfidence     float64  `json:"max_confidence,omitempty"`
}

type SearchRelation struct {
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
	TargetValidity       *ValidityEvidence `json:"target_validity,omitempty"`
	TargetText           *TextProvenance   `json:"target_text_provenance,omitempty"`
	Evidence             *RelationEvidence `json:"evidence,omitempty"`
	RelationTypeRaw      *int32            `json:"relation_type_raw,omitempty"`
	TargetAmendedBy      []string          `json:"target_amended_by,omitempty" jsonschema:"doc numbers of documents that further amend/replace this relation's target — a currency warning: the target itself has been amended, so open it (or the newest amender) with the document tool before relying on its text; its amendment_chain shows the full lineage"`
}

type RelationEvidence struct {
	EvidenceID      int64   `json:"evidence_id,omitempty"`
	EvidenceKind    string  `json:"evidence_kind,omitempty" jsonschema:"how this edge was established. 'structured_relation' = asserted by the official source's own reference metadata, NOT corroborated against the document's text; the source can be wrong, so treat it as a lead and verify. 'weak_relation' = derived from the amending document's own wording, and citation/snippet point at that wording."`
	Operator        string  `json:"operator,omitempty" jsonschema:"the legal operation, e.g. sửa đổi, bổ sung / bãi bỏ / thay thế, or the source's relation code for structured evidence"`
	TargetText      string  `json:"target_text,omitempty" jsonschema:"the target document as identified by the evidence — usually its số ký hiệu"`
	TargetCitation  string  `json:"target_citation,omitempty" jsonschema:"the provision of the target document the evidence points at, when the evidence names one"`
	Citation        string  `json:"citation,omitempty" jsonschema:"where the evidence sits INSIDE the amending document (e.g. 'Điều 50, Khoản 2') for weak_relation. For structured_relation this is a source sentinel such as 'vbpl:references', meaning the edge came from source metadata and no clause in the document was located."`
	Snippet         string  `json:"snippet,omitempty" jsonschema:"for weak_relation, the verbatim sentence from the amending document that establishes the edge — read it, because a document number appearing in an amendment sentence is often a RECITAL of the target's own amendment history ('… của Luật X đã được sửa đổi theo Luật Y …' names X as the target and Y only as history), not a second target. For structured_relation this is the target document's TITLE, not a quotation of any clause."`
	SourceAuthority string  `json:"source_authority,omitempty" jsonschema:"provenance tier of the assertion, e.g. official_structured (the source's metadata) or official_text (the document body)"`
	Confidence      float64 `json:"confidence,omitempty" jsonschema:"how strongly the SOURCE asserts this edge, not how likely it is to be correct: official metadata is stored at 1 even when the document's text does not support it"`
	Promoted        bool    `json:"promoted" jsonschema:"true when this evidence was promoted to a confirmed relation in the graph"`
}

type RelatedHit struct {
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
	Validity       ValidityEvidence `json:"validity" jsonschema:"current validity of the related document"`
	Text           TextProvenance   `json:"text_provenance" jsonschema:"text source and binding/review state of the related document"`
	Location       string           `json:"location" jsonschema:"position within the related document"`
	ContextPrefix  string           `json:"context_prefix,omitempty"`
	Snippet        string           `json:"snippet" jsonschema:"preview of the related provision; open the document tool for full text"`
	DocumentID     int64            `json:"document_id"`
	ChunkID        int64            `json:"chunk_id"`
	Rank           int              `json:"rank,omitempty" jsonschema:"1-based rank of this chunk within its relation (vector order)"`
}

type Gap struct {
	Kind         string `json:"kind"`
	Message      string `json:"message,omitempty"`
	BlocksAnswer bool   `json:"blocks_answer"`
	DocumentID   int64  `json:"document_id,omitempty"`
	DocNumber    string `json:"doc_number,omitempty"`
	Title        string `json:"title,omitempty"`
	RelationID   int64  `json:"relation_id,omitempty"`
	RelationType string `json:"relation_type,omitempty"`
}

type ScopeEvidence struct {
	Checked        bool     `json:"checked"`
	InDomain       bool     `json:"in_domain"`
	MatchedTerms   []string `json:"matched_terms,omitempty"`
	KnownReference bool     `json:"known_reference"`
}

type DocumentMeta struct {
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
	Validity    ValidityEvidence `json:"validity"`
}

type DocAlternative struct {
	DocumentID  int64  `json:"document_id"`
	DocNumber   string `json:"doc_number,omitempty"`
	DocType     string `json:"doc_type,omitempty"`
	Title       string `json:"title,omitempty"`
	StatusClass string `json:"status_class,omitempty"`
	Indexed     bool   `json:"indexed"`
}

type DocSource struct {
	Source   string `json:"source"`
	URL      string `json:"url"`
	FilesURL string `json:"files_url,omitempty" jsonschema:"stable official endpoint listing this document's files; each GET returns fresh short-lived direct download links (vbpl: ~24h presigned, reachable from Vietnamese IPs only)"`
}

type DocumentFile struct {
	Label      string          `json:"label,omitempty" jsonschema:"source-provided file name"`
	Kind       string          `json:"kind,omitempty" jsonschema:"artifact role: main, appendix, attachment, version_snapshot, or original_scan"`
	Format     string          `json:"format,omitempty"`
	ByteSize   int64           `json:"byte_size,omitempty"`
	SHA256     string          `json:"sha256,omitempty" jsonschema:"content hash of the archived bytes; matches text_provenance source_file_sha256, so a download is verifiable against the extracted text"`
	OriginURLs []DocFileOrigin `json:"origin_urls,omitempty" jsonschema:"direct official download links serving these exact bytes; absent when no durable official link exists (reachability may vary by source)"`
}

type DocFileOrigin struct {
	Source string `json:"source"`
	URL    string `json:"url"`
}

type DocumentChunk struct {
	ChunkID        int64            `json:"chunk_id"`
	SectionID      int64            `json:"section_id,omitempty"`
	Location       string           `json:"location"`
	ContextPrefix  string           `json:"context_prefix,omitempty"`
	Content        string           `json:"content"`
	Ordinal        int32            `json:"ordinal"`
	Validity       ValidityEvidence `json:"validity"`
	TextProvenance TextProvenance   `json:"text_provenance"`
}

type DocumentValidityPeriod struct {
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

type DocumentTextEvidence struct {
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

type AmendmentClause struct {
	AmendingDoc           string `json:"amending_doc"`
	AmendingEffectiveFrom string `json:"amending_effective_from,omitempty"`
	RelationType          string `json:"relation_type"`
	Position              string `json:"position"`
	Text                  string `json:"text"`
}

type ChainNode struct {
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

type TimelineEvent struct {
	Date         string `json:"date,omitempty"`
	Event        string `json:"event"`
	RelationType string `json:"relation_type,omitempty"`
	Doc          string `json:"doc,omitempty"`
	Note         string `json:"note,omitempty"`
}
