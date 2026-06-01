package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"danny.vn/banhmi/pkg/rag/retrieve"
)

// fakeSearcher is a stub Searcher returning fixed hits and recording the last call.
type fakeSearcher struct {
	hits        []retrieve.Hit
	relatedHits []retrieve.RelatedHit
	gaps        []retrieve.Gap
	abstain     bool
	err         error
	lastQuery   string
	lastOpts    retrieve.SearchOpts
}

func (f *fakeSearcher) Search(_ context.Context, q string, opts retrieve.SearchOpts) ([]retrieve.Hit, error) {
	f.lastQuery = q
	f.lastOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.hits, nil
}

func (f *fakeSearcher) SearchEvidence(ctx context.Context, q string, opts retrieve.SearchOpts) (retrieve.Evidence, error) {
	hits, err := f.Search(ctx, q, opts)
	if err != nil {
		return retrieve.Evidence{}, err
	}
	return retrieve.Evidence{
		Hits:        hits,
		RelatedHits: f.relatedHits,
		Gaps:        f.gaps,
		Abstain:     f.abstain,
		Scope: retrieve.ScopeEvidence{
			Checked:      true,
			InDomain:     true,
			MatchedTerms: []string{"alpha scope"},
		},
	}, nil
}

type fakeCorpus struct {
	status  corpusStatusOutput
	quality qualityGapsOutput
	doc     documentOutput
	err     error
}

func (f fakeCorpus) CorpusStatus(context.Context) (corpusStatusOutput, error) {
	if f.err != nil {
		return corpusStatusOutput{}, f.err
	}
	return f.status, nil
}

func (f fakeCorpus) QualityGaps(context.Context, qualityGapsInput) (qualityGapsOutput, error) {
	if f.err != nil {
		return qualityGapsOutput{}, f.err
	}
	return f.quality, nil
}

func (f fakeCorpus) Document(context.Context, documentInput) (documentOutput, error) {
	if f.err != nil {
		return documentOutput{}, f.err
	}
	return f.doc, nil
}

func sampleHit() retrieve.Hit {
	return retrieve.Hit{
		ChunkID:       42,
		DocumentID:    10,
		DocNumber:     "11/2026/TT-NHNN",
		Title:         "Quy định về an toàn hệ thống thông tin",
		Citation:      "Điều 7, Khoản 2",
		ContextPrefix: "Văn bản: 11/2026/TT-NHNN",
		Content:       "Tổ chức tín dụng phải xây dựng hệ thống quản lý an toàn thông tin.",
		Score:         1.5,
		VectorRank:    1,
		BM25Rank:      2,
		Validity: retrieve.ValidityEvidence{
			StatusCode:    "CHL",
			StatusClass:   "in_force",
			EffectiveFrom: "2026-01-01",
			Source:        "vbpl",
		},
		Text: retrieve.TextEvidence{
			HasBindingText: true,
			Authorities:    []string{"gazette_borndigital"},
			Sources:        []string{"docx"},
			ExtractEngines: []string{"markitdown"},
			MaxConfidence:  0.99,
		},
		Relations: []retrieve.Relation{{
			RelationID:           7,
			Direction:            "incoming",
			RelationType:         "amends_supplements",
			DocNumber:            "12/2027/TT-NHNN",
			Title:                "Văn bản sửa đổi",
			Resolved:             true,
			TargetIndexed:        true,
			TargetHasBindingText: true,
			TargetValidity: retrieve.ValidityEvidence{
				StatusClass:   "in_force",
				EffectiveFrom: "2027-01-01",
			},
			TargetText: retrieve.TextEvidence{
				HasBindingText: true,
				Authorities:    []string{"gazette_borndigital"},
			},
			Evidence: retrieve.RelationEvidence{
				EvidenceID:      99,
				EvidenceKind:    "structured_relation",
				Operator:        "amends",
				SourceAuthority: "vbpl",
				Confidence:      1,
				Promoted:        true,
				Citation:        "Điều 1",
				Snippet:         "Sửa đổi yêu cầu an toàn thông tin.",
			},
		}},
	}
}

func sampleRelatedHit() retrieve.RelatedHit {
	return retrieve.RelatedHit{
		BaseChunkID:    42,
		BaseDocumentID: 10,
		BaseDocNumber:  "11/2026/TT-NHNN",
		RelationID:     7,
		Direction:      "incoming",
		RelationType:   "amends_supplements",
		Source:         "vbpl",
		ChunkID:        55,
		DocumentID:     12,
		DocNumber:      "12/2027/TT-NHNN",
		Title:          "Văn bản sửa đổi",
		Citation:       "Điều 2",
		ContextPrefix:  "Văn bản: 12/2027/TT-NHNN",
		Content:        "Sửa đổi yêu cầu an toàn thông tin.",
		Validity: retrieve.ValidityEvidence{
			StatusClass:   "in_force",
			EffectiveFrom: "2027-01-01",
		},
		Text: retrieve.TextEvidence{
			HasBindingText: true,
			Authorities:    []string{"gazette_borndigital"},
			ExtractEngines: []string{"markitdown"},
		},
		BM25Rank:  1,
		BM25Score: 12.5,
	}
}

// connect builds the MCP server over the given fakes, wires an in-memory client to
// it, and returns the connected client session. The in-memory transports exercise
// the real JSON-RPC dispatch (tools/list, tools/call) end to end.
func connect(t *testing.T, r Searcher, opts ...Option) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	srv := New(r, nil, opts...)
	clientT, serverT := mcp.NewInMemoryTransports()
	if _, err := srv.mcp.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// decodeStructured re-decodes a tool result's structured content (delivered as a
// generic map over the wire) into a typed value for assertions.
func decodeStructured(t *testing.T, res *mcp.CallToolResult, v any) {
	t.Helper()
	if res.StructuredContent == nil {
		t.Fatal("result has no structured content")
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("unmarshal structured content into %T: %v", v, err)
	}
}

// --- tools/list -----------------------------------------------------------------

func TestListTools(t *testing.T) {
	cs := connect(t, &fakeSearcher{hits: []retrieve.Hit{sampleHit()}})

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	names := make(map[string]*mcp.Tool, len(res.Tools))
	for _, tool := range res.Tools {
		names[tool.Name] = tool
	}
	if len(names) != 5 {
		t.Fatalf("len(tools) = %d, want 5 (got %v)", len(res.Tools), keys(names))
	}

	search, ok := names["search"]
	if !ok {
		t.Fatal("search tool not listed")
	}
	if search.InputSchema == nil {
		t.Error("search tool has no input schema")
	}
	for _, name := range []string{"guide", "corpus_status", "quality_gaps", "document"} {
		tool, ok := names[name]
		if !ok {
			t.Fatalf("%s tool not listed", name)
		}
		if tool.InputSchema == nil {
			t.Errorf("%s tool has no input schema", name)
		}
	}
}

func keys(m map[string]*mcp.Tool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// --- tools/call: agent support --------------------------------------------------

func TestCallGuide(t *testing.T) {
	cs := connect(t, &fakeSearcher{})

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "guide",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool guide: %v", err)
	}
	if res.IsError {
		t.Fatalf("guide returned IsError: %v", textOf(res))
	}

	var out guideOutput
	decodeStructured(t, res, &out)
	if !strings.Contains(out.Purpose, "database evidence") {
		t.Errorf("guide purpose = %q, want database evidence boundary", out.Purpose)
	}
	if len(out.Tools) < 3 || len(out.RecommendedFlow) == 0 || len(out.EvidenceContract) == 0 {
		t.Errorf("guide output too thin: %+v", out)
	}
}

func TestCallCorpusStatus(t *testing.T) {
	fc := fakeCorpus{status: corpusStatusOutput{
		SearchReady: true,
		EmbedModel:  "test-embed",
		Docs:        corpusDocStats{Total: 10, Indexed: 8, NonBindingOnly: 1},
		Chunks:      corpusChunkStats{Total: 100, ConfiguredEmbeddings: 100},
		Relations:   corpusRelationStats{ConfirmedEdges: 4, ResolvedTargets: 3},
		Notes:       []string{"one known gap"},
	}}
	cs := connect(t, &fakeSearcher{}, WithCorpus(fc))

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "corpus_status",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool corpus_status: %v", err)
	}
	if res.IsError {
		t.Fatalf("corpus_status returned IsError: %v", textOf(res))
	}

	var out corpusStatusOutput
	decodeStructured(t, res, &out)
	if !out.SearchReady || out.Docs.Total != 10 || out.Chunks.ConfiguredEmbeddings != 100 {
		t.Errorf("corpus status = %+v, want fake DB-backed status", out)
	}
	if len(out.Notes) != 1 {
		t.Errorf("notes = %+v, want gap note", out.Notes)
	}
}

func TestCallQualityGaps(t *testing.T) {
	fc := fakeCorpus{quality: qualityGapsOutput{
		Limit:      20,
		Categories: []string{qualityCategoryFetch},
		Summary:    corpusGapStats{UnresolvedRelationTargets: 3},
		FetchIncomplete: []qualityFetchGap{{
			FetchDocID: 1,
			Source:     "vbpl",
			ExternalID: "123",
			State:      "error",
			SoKyHieu:   "11/2026/TT-NHNN",
			LastError:  "source returned an error",
		}},
	}}
	cs := connect(t, &fakeSearcher{}, WithCorpus(fc))

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "quality_gaps",
		Arguments: map[string]any{
			"category": "fetch",
			"limit":    5,
		},
	})
	if err != nil {
		t.Fatalf("CallTool quality_gaps: %v", err)
	}
	if res.IsError {
		t.Fatalf("quality_gaps returned IsError: %v", textOf(res))
	}

	var out qualityGapsOutput
	decodeStructured(t, res, &out)
	if out.Limit != 20 || len(out.FetchIncomplete) != 1 {
		t.Fatalf("quality gaps = %+v, want fake fetch gap", out)
	}
	if out.FetchIncomplete[0].SoKyHieu != "11/2026/TT-NHNN" {
		t.Errorf("fetch gap doc = %+v, want so_ky_hieu", out.FetchIncomplete[0])
	}
}

func TestCallDocument(t *testing.T) {
	fc := fakeCorpus{doc: documentOutput{
		Found: true,
		Document: documentMeta{
			DocumentID:  10,
			SoKyHieu:    "11/2026/TT-NHNN",
			Title:       "Quy định về an toàn hệ thống thông tin",
			StatusClass: "in_force",
			Validity: validityEvidence{
				StatusClass:   "in_force",
				EffectiveFrom: "2026-01-01",
			},
		},
		ValidityPeriods: []documentValidityPeriod{{
			ValidityID:    1,
			StatusClass:   "in_force",
			EffectiveFrom: "2026-01-01",
			Source:        "vbpl",
		}},
		TextSummary: textProvenance{
			HasBindingText: true,
			Authorities:    []string{"gazette_borndigital"},
			ExtractEngines: []string{"markitdown"},
		},
		TextProvenance: []documentTextEvidence{{
			TextID:        1,
			Authority:     "gazette_borndigital",
			HasText:       true,
			IsBinding:     true,
			ExtractEngine: "markitdown",
		}},
		Chunks: []documentChunk{{
			ChunkID:        42,
			Location:       "Điều 7, Khoản 2",
			Content:        "Tổ chức tín dụng phải xây dựng hệ thống quản lý an toàn thông tin.",
			Ordinal:        7,
			Validity:       validityEvidence{StatusClass: "in_force", EffectiveFrom: "2026-01-01"},
			TextProvenance: textProvenance{HasBindingText: true, Authorities: []string{"gazette_borndigital"}},
		}},
		Relations: []searchRelation{{
			RelationID:   7,
			Direction:    "incoming",
			RelationType: "amends_supplements",
			SoKyHieu:     "12/2027/TT-NHNN",
			Resolved:     true,
		}},
		Limit: 20,
	}}
	cs := connect(t, &fakeSearcher{}, WithCorpus(fc))

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "document",
		Arguments: map[string]any{
			"so_ky_hieu": "11/2026/TT-NHNN",
			"citation":   "Điều 7",
		},
	})
	if err != nil {
		t.Fatalf("CallTool document: %v", err)
	}
	if res.IsError {
		t.Fatalf("document returned IsError: %v", textOf(res))
	}

	var out documentOutput
	decodeStructured(t, res, &out)
	if !out.Found || out.Document.SoKyHieu != "11/2026/TT-NHNN" {
		t.Fatalf("document output = %+v, want found document", out)
	}
	if len(out.Chunks) != 1 || out.Chunks[0].Location != "Điều 7, Khoản 2" {
		t.Errorf("chunks = %+v, want requested citation evidence", out.Chunks)
	}
	if len(out.Relations) != 1 || out.Relations[0].RelationType != "amends_supplements" {
		t.Errorf("relations = %+v, want relation context", out.Relations)
	}
	if out.Document.Validity.StatusClass != "in_force" || len(out.ValidityPeriods) != 1 {
		t.Errorf("document validity = %+v periods=%+v, want current validity evidence", out.Document.Validity, out.ValidityPeriods)
	}
	if !out.TextSummary.HasBindingText || len(out.TextProvenance) != 1 {
		t.Errorf("text provenance = summary %+v rows %+v, want binding extraction evidence", out.TextSummary, out.TextProvenance)
	}
}

// --- tools/call: search ---------------------------------------------------------

func TestCallSearch(t *testing.T) {
	fs := &fakeSearcher{
		hits:        []retrieve.Hit{sampleHit()},
		relatedHits: []retrieve.RelatedHit{sampleRelatedHit()},
	}
	cs := connect(t, fs)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query": "an toàn thông tin",
			"top_k": 5,
		},
	})
	if err != nil {
		t.Fatalf("CallTool search: %v", err)
	}
	if res.IsError {
		t.Fatalf("search returned IsError: %v", textOf(res))
	}

	var out searchOutput
	decodeStructured(t, res, &out)
	if len(out.Hits) != 1 {
		t.Fatalf("len(hits) = %d, want 1", len(out.Hits))
	}
	h := out.Hits[0]
	if h.SoKyHieu != "11/2026/TT-NHNN" {
		t.Errorf("hit so_ky_hieu = %q", h.SoKyHieu)
	}
	if h.Location != "Điều 7, Khoản 2" {
		t.Errorf("hit location = %q", h.Location)
	}
	if !strings.Contains(h.Snippet, "an toàn thông tin") {
		t.Errorf("hit snippet = %q", h.Snippet)
	}
	if h.ChunkID != 42 || h.DocumentID != 10 {
		t.Errorf("hit ids = chunk %d doc %d, want 42/10", h.ChunkID, h.DocumentID)
	}
	if h.ContextPrefix == "" || h.VectorRank != 1 || h.BM25Rank != 2 {
		t.Errorf("hit context/ranks = prefix %q vector %d bm25 %d", h.ContextPrefix, h.VectorRank, h.BM25Rank)
	}
	if h.Validity.StatusClass != "in_force" || !h.Text.HasBindingText {
		t.Errorf("hit validity/text = %+v / %+v, want current binding evidence", h.Validity, h.Text)
	}
	if len(h.Relations) != 1 || h.Relations[0].RelationType != "amends_supplements" {
		t.Errorf("hit relations = %+v, want confirmed relation context", h.Relations)
	}
	if h.Relations[0].TargetValidity == nil || h.Relations[0].TargetValidity.StatusClass != "in_force" {
		t.Errorf("relation target validity = %+v, want in_force", h.Relations[0].TargetValidity)
	}
	// Search relations are slim: the heavy target text-provenance and evidence snippet
	// are dropped to keep the pack compact (the agent opens the target via document).
	if h.Relations[0].TargetText != nil || h.Relations[0].Evidence != nil {
		t.Errorf("search relation should omit target_text/evidence, got %+v / %+v",
			h.Relations[0].TargetText, h.Relations[0].Evidence)
	}
	if !out.Scope.Checked || !out.Scope.InDomain || len(out.Scope.MatchedTerms) != 1 {
		t.Errorf("scope = %+v, want configured scope evidence", out.Scope)
	}
	if len(out.RelatedHits) != 1 {
		t.Fatalf("len(related_hits) = %d, want 1", len(out.RelatedHits))
	}
	relHit := out.RelatedHits[0]
	if relHit.BaseChunkID != 42 || relHit.RelationID != 7 || relHit.SoKyHieu != "12/2027/TT-NHNN" {
		t.Errorf("related hit = %+v, want relation provenance and related doc", relHit)
	}
	if relHit.BM25Rank != 1 || relHit.BM25Score == 0 {
		t.Errorf("related hit bm25 diagnostics = rank %d score %.2f", relHit.BM25Rank, relHit.BM25Score)
	}
	if relHit.StatusClass != "in_force" || relHit.EffectiveDate != "2027-01-01" {
		t.Errorf("related hit validity = %q/%q, want status/effective date", relHit.StatusClass, relHit.EffectiveDate)
	}
	if relHit.Validity.StatusClass != "in_force" || !relHit.Text.HasBindingText {
		t.Errorf("related hit evidence = %+v / %+v, want structured validity/text", relHit.Validity, relHit.Text)
	}

	// The request fields were forwarded to the core.
	if fs.lastQuery != "an toàn thông tin" {
		t.Errorf("forwarded query = %q", fs.lastQuery)
	}
	if fs.lastOpts.TopK != 5 {
		t.Errorf("forwarded TopK = %d, want 5", fs.lastOpts.TopK)
	}
	if fs.lastOpts.RelatedK != defaultMCPRelatedK {
		t.Errorf("forwarded RelatedK = %d, want MCP default", fs.lastOpts.RelatedK)
	}
}

func TestCallSearch_DisableRelated(t *testing.T) {
	fs := &fakeSearcher{hits: []retrieve.Hit{sampleHit()}}
	cs := connect(t, fs)

	includeRelated := false
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query":           "an toàn thông tin",
			"include_related": includeRelated,
		},
	})
	if err != nil {
		t.Fatalf("CallTool search: %v", err)
	}
	if res.IsError {
		t.Fatalf("search returned IsError: %v", textOf(res))
	}
	if fs.lastOpts.RelatedK != 0 {
		t.Errorf("forwarded RelatedK = %d, want 0 when disabled", fs.lastOpts.RelatedK)
	}
}

func TestCallSearch_Empty(t *testing.T) {
	cs := connect(t, &fakeSearcher{hits: nil})

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "không có gì"},
	})
	if err != nil {
		t.Fatalf("CallTool search: %v", err)
	}
	if res.IsError {
		t.Fatalf("search returned IsError: %v", textOf(res))
	}

	var out searchOutput
	decodeStructured(t, res, &out)
	if out.Hits == nil {
		t.Error("hits = nil, want non-nil empty slice (so JSON is [])")
	}
	if len(out.Hits) != 0 {
		t.Errorf("len(hits) = %d, want 0", len(out.Hits))
	}
}

func TestCallSearch_GapsAreContext(t *testing.T) {
	fs := &fakeSearcher{
		hits: []retrieve.Hit{sampleHit()},
		gaps: []retrieve.Gap{{
			Kind:      retrieve.GapKnownBindingTextGap,
			DocNumber: "01/2026/QD-ABC",
		}},
	}
	cs := connect(t, fs)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "alpha scope"},
	})
	if err != nil {
		t.Fatalf("CallTool search: %v", err)
	}
	if res.IsError {
		t.Fatalf("search returned IsError: %v", textOf(res))
	}

	var out searchOutput
	decodeStructured(t, res, &out)
	if out.Abstain {
		t.Error("Abstain = true, want false; binding gaps are model context")
	}
	if len(out.Gaps) != 1 || out.Gaps[0].Kind != string(retrieve.GapKnownBindingTextGap) {
		t.Fatalf("gaps = %+v, want binding text gap", out.Gaps)
	}
}

// --- tools/call: unknown tool ---------------------------------------------------

func TestCallUnknownTool(t *testing.T) {
	cs := connect(t, &fakeSearcher{})

	// An unknown tool is a protocol-level error (the tool does not exist), so
	// CallTool returns a non-nil error rather than an IsError result.
	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "nope",
		Arguments: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected an error for an unknown tool")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error = %v, want it to name the unknown tool", err)
	}
}

// textOf returns the concatenated text of a result's content for assertions.
func textOf(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// Compile-time checks that the concrete cores satisfy the consumer interfaces this
// surface depends on.
var (
	_ Searcher = (retrieve.Retriever)(nil)
)
