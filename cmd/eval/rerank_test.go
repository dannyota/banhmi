package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"danny.vn/banhmi/pkg/rag/retrieve"
)

func TestRerankClientOrdersHitsByScore(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/rerank" {
			t.Fatalf("path = %q, want /v3/rerank", r.URL.Path)
		}
		var req rerankRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "reranker" {
			t.Fatalf("model = %q, want reranker", req.Model)
		}
		if len(req.Documents) != 2 || !strings.Contains(req.Documents[0], "first") {
			t.Fatalf("documents = %#v", req.Documents)
		}
		_, _ = w.Write([]byte(`{"results":[{"index":0,"relevance_score":0.1},{"index":1,"relevance_score":0.9}]}`))
	}))
	defer srv.Close()

	client := newRerankClient(srv.URL+"/v3", "reranker", false, "")
	hits := []retrieve.Hit{
		{ChunkID: 1, Content: "first"},
		{ChunkID: 2, Content: "second"},
	}
	got, _, err := client.Rerank(context.Background(), "query", hits)
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].ChunkID != 2 || got[0].Score != 0.9 {
		t.Fatalf("top hit = %+v, want chunk 2 score 0.9", got[0])
	}
}

func TestRerankClientAppliesQwenTemplate(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rerankRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !strings.Contains(req.Query, "<Instruct>: legal") || !strings.Contains(req.Query, "<Query>: hello") {
			t.Fatalf("query template not applied: %q", req.Query)
		}
		if len(req.Documents) != 1 || !strings.Contains(req.Documents[0], "<Document>: doc") {
			t.Fatalf("document template not applied: %#v", req.Documents)
		}
		_, _ = w.Write([]byte(`{"results":[{"index":0,"relevance_score":0.8}]}`))
	}))
	defer srv.Close()

	client := newRerankClient(srv.URL+"/v3/rerank", "qwen", true, "legal")
	if _, _, err := client.Rerank(context.Background(), "hello", []retrieve.Hit{{ChunkID: 1, Content: "doc"}}); err != nil {
		t.Fatalf("Rerank: %v", err)
	}
}
