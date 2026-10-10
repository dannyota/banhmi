package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"danny.vn/banhmi/pkg/rag/retrieve"
)

func TestSearchOperationMatchesMCPPayload(t *testing.T) {
	hit := sampleHit()
	hit.IssuedDate = "2026-02-01"
	hit.Validity.EffectiveFrom = "2026-01-01"
	hit.Text.HasNonBindingText = true
	hit.Text.NeedsReview = true

	searcher := &fakeSearcher{
		hits:        []retrieve.Hit{hit},
		relatedHits: []retrieve.RelatedHit{sampleRelatedHit()},
		gaps: []retrieve.Gap{{
			Kind:      retrieve.GapKnownBindingTextGap,
			DocNumber: "01/2026/QD-ABC",
		}},
	}
	server, client := connectOperationServer(t, searcher)
	in := SearchInput{Query: "  an toàn thông tin  "}

	direct, err := server.Search(context.Background(), in)
	if err != nil {
		t.Fatalf("direct Search: %v", err)
	}
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": in.Query},
	})
	if err != nil {
		t.Fatalf("MCP search: %v", err)
	}
	if result.IsError {
		t.Fatalf("MCP search returned IsError: %s", textOf(result))
	}
	var dispatched SearchOutput
	decodeStructured(t, result, &dispatched)

	assertStructuredMatches(t, result, direct)
	if !reflect.DeepEqual(dispatched, direct) {
		t.Fatalf("MCP payload differs from direct operation:\nMCP:    %#v\ndirect: %#v", dispatched, direct)
	}
	if searcher.lastQuery != "an toàn thông tin" {
		t.Errorf("trimmed query = %q, want %q", searcher.lastQuery, "an toàn thông tin")
	}
	if searcher.lastOpts.TopK != 0 || searcher.lastOpts.RelatedK != defaultMCPRelatedK || searcher.lastOpts.InForceOnly != nil {
		t.Errorf("default search options = %+v, want retriever top-k/default validity and related_k=%d", searcher.lastOpts, defaultMCPRelatedK)
	}
	if len(direct.Hits) != 1 || direct.Hits[0].Validity.Warning == "" {
		t.Fatalf("validity warning missing from direct payload: %+v", direct.Hits)
	}
	if !direct.Hits[0].Text.HasNonBindingText || !direct.Hits[0].Text.NeedsReview {
		t.Errorf("non-binding review state missing: %+v", direct.Hits[0].Text)
	}
	if len(direct.Hits[0].Relations) != 1 || len(direct.RelatedHits) != 1 {
		t.Errorf("relation evidence missing: hit relations=%+v related hits=%+v", direct.Hits[0].Relations, direct.RelatedHits)
	}
	if len(direct.Gaps) != 1 || direct.Gaps[0].Kind != string(retrieve.GapKnownBindingTextGap) {
		t.Errorf("gap evidence = %+v, want known binding-text gap", direct.Gaps)
	}
}

func TestSearchOperationMatchesMCPError(t *testing.T) {
	server, client := connectOperationServer(t, &fakeSearcher{})

	_, directErr := server.Search(context.Background(), SearchInput{Query: " \t "})
	result, callErr := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": " \t "},
	})
	assertOperationErrorMatchesMCP(t, directErr, result, callErr)
}

func TestDocumentOperationMatchesMCPNotFoundPayload(t *testing.T) {
	notFound := DocumentOutput{
		Found:  false,
		Chunks: []documentChunk{},
		Gaps: []gap{{
			Kind:         string(retrieve.GapNoEvidence),
			Message:      "document not found by id or document number",
			BlocksAnswer: true,
		}},
		Limit: 20,
	}
	server, client := connectOperationServer(t, &fakeSearcher{}, WithCorpus(fakeCorpus{doc: notFound}))
	in := DocumentInput{DocNumber: "missing/2026"}

	direct, err := server.Document(context.Background(), in)
	if err != nil {
		t.Fatalf("direct Document: %v", err)
	}
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "document",
		Arguments: map[string]any{"doc_number": in.DocNumber},
	})
	if err != nil {
		t.Fatalf("MCP document: %v", err)
	}
	if result.IsError {
		t.Fatalf("MCP document returned IsError: %s", textOf(result))
	}
	var dispatched DocumentOutput
	decodeStructured(t, result, &dispatched)

	assertStructuredMatches(t, result, direct)
	if !reflect.DeepEqual(dispatched, direct) {
		t.Fatalf("MCP payload differs from direct operation:\nMCP:    %#v\ndirect: %#v", dispatched, direct)
	}
	if direct.Found || len(direct.Gaps) != 1 || !direct.Gaps[0].BlocksAnswer {
		t.Errorf("not-found evidence = %+v, want a blocking no-evidence gap", direct)
	}
}

func TestDocumentOperationMatchesMCPErrors(t *testing.T) {
	t.Run("missing selector", func(t *testing.T) {
		server, client := connectOperationServer(t, &fakeSearcher{}, WithCorpus(fakeCorpus{}))
		_, directErr := server.Document(context.Background(), DocumentInput{})
		result, callErr := client.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "document",
			Arguments: map[string]any{},
		})
		assertOperationErrorMatchesMCP(t, directErr, result, callErr)
	})

	t.Run("corpus failure", func(t *testing.T) {
		server, client := connectOperationServer(t, &fakeSearcher{}, WithCorpus(fakeCorpus{err: errors.New("storage unavailable")}))
		in := DocumentInput{DocumentID: 42}
		_, directErr := server.Document(context.Background(), in)
		result, callErr := client.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "document",
			Arguments: map[string]any{"document_id": in.DocumentID},
		})
		assertOperationErrorMatchesMCP(t, directErr, result, callErr)
	})
}

func connectOperationServer(t *testing.T, searcher Searcher, opts ...Option) (*Server, *mcp.ClientSession) {
	t.Helper()
	ctx := context.Background()
	server := New(searcher, nil, opts...)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := server.mcp.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "operation-test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return server, session
}

func assertOperationErrorMatchesMCP(t *testing.T, directErr error, result *mcp.CallToolResult, callErr error) {
	t.Helper()
	if directErr == nil {
		t.Fatal("direct operation error = nil")
	}
	if callErr != nil {
		t.Fatalf("MCP dispatch returned protocol error: %v", callErr)
	}
	if result == nil || !result.IsError {
		t.Fatalf("MCP result = %+v, want IsError", result)
	}
	if got := textOf(result); !strings.Contains(got, directErr.Error()) {
		t.Errorf("MCP error text = %q, want direct error %q", got, directErr)
	}
}

func assertStructuredMatches(t *testing.T, result *mcp.CallToolResult, direct any) {
	t.Helper()
	raw, err := json.Marshal(direct)
	if err != nil {
		t.Fatalf("marshal direct operation payload: %v", err)
	}
	var want any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("decode direct operation payload: %v", err)
	}
	if !reflect.DeepEqual(result.StructuredContent, want) {
		t.Errorf("MCP structured JSON differs from direct operation JSON:\nMCP:    %#v\ndirect: %#v", result.StructuredContent, want)
	}
}
