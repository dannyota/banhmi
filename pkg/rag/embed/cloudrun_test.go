package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCloudRunEmbedder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/embed" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req cloudRunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		vecs := make([][]float32, len(req.Texts))
		for i := range vecs {
			vecs[i] = make([]float32, 3)
			vecs[i][0] = float32(i)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cloudRunResponse{
			Model:   "bge-m3",
			Dims:    3,
			Vectors: vecs,
		})
	}))
	defer srv.Close()

	e := newCloudRunWithClient(srv.URL, "bge-m3", 3, srv.Client())
	if e.Model() != "bge-m3" {
		t.Fatalf("Model() = %q, want bge-m3", e.Model())
	}
	if e.Dims() != 3 {
		t.Fatalf("Dims() = %d, want 3", e.Dims())
	}

	vecs, err := e.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("got %d vectors, want 2", len(vecs))
	}
	if vecs[0][0] != 0 || vecs[1][0] != 1 {
		t.Fatalf("unexpected vectors: %v", vecs)
	}
}

func TestCloudRunEmbedderEmpty(t *testing.T) {
	e := newCloudRunWithClient("http://unused", "bge-m3", 3, &http.Client{})
	vecs, err := e.Embed(context.Background(), nil)
	if err != nil {
		t.Fatalf("Embed(nil): %v", err)
	}
	if vecs != nil {
		t.Fatalf("got %v, want nil", vecs)
	}
}

func TestCloudRunEmbedderBatching(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req cloudRunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(req.Texts) > maxBatchSize {
			t.Fatalf("batch too large: %d", len(req.Texts))
		}
		vecs := make([][]float32, len(req.Texts))
		for i := range vecs {
			vecs[i] = []float32{1.0}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cloudRunResponse{Model: "m", Dims: 1, Vectors: vecs})
	}))
	defer srv.Close()

	e := newCloudRunWithClient(srv.URL, "m", 1, srv.Client())
	texts := make([]string, 300)
	for i := range texts {
		texts[i] = "t"
	}
	vecs, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 300 {
		t.Fatalf("got %d vectors, want 300", len(vecs))
	}
	if calls != 2 {
		t.Fatalf("expected 2 HTTP calls (256+44), got %d", calls)
	}
}
