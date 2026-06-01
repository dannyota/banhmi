package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// makeServer returns an httptest.Server that echoes synthetic embeddings.
// It validates the request shape and fills data[i].embedding with a vector of
// length dims where every element equals float32(i+1).
func makeServer(t *testing.T, wantModel string, dims int, wantAPIKey string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/embeddings" {
			t.Errorf("path = %q, want /embeddings", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		if wantAPIKey != "" {
			got := r.Header.Get("Authorization")
			if got != "Bearer "+wantAPIKey {
				t.Errorf("Authorization = %q, want %q", got, "Bearer "+wantAPIKey)
			}
		}

		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Model != wantModel {
			t.Errorf("model = %q, want %q", req.Model, wantModel)
		}

		type dataItem struct {
			Object    string    `json:"object"`
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}
		type resp struct {
			Object string     `json:"object"`
			Data   []dataItem `json:"data"`
		}

		r2 := resp{Object: "list"}
		for i := range req.Input {
			vec := make([]float32, dims)
			for j := range vec {
				vec[j] = float32(i + 1)
			}
			r2.Data = append(r2.Data, dataItem{
				Object:    "embedding",
				Embedding: vec,
				Index:     i,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(r2); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
}

func TestEmbed_requestShape(t *testing.T) {
	const model = "BAAI/bge-m3"
	const dims = 1024
	const apiKey = "test-key"

	srv := makeServer(t, model, dims, apiKey)
	defer srv.Close()

	e := newWithClient(srv.URL, model, dims, apiKey, srv.Client())
	ctx := context.Background()

	vecs, err := e.Embed(ctx, []string{"hello world"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 1 {
		t.Fatalf("len(vecs) = %d, want 1", len(vecs))
	}
	if len(vecs[0]) != dims {
		t.Errorf("len(vecs[0]) = %d, want %d", len(vecs[0]), dims)
	}
}

func TestEmbed_responseParsing(t *testing.T) {
	const model = "BAAI/bge-m3"
	const dims = 4

	srv := makeServer(t, model, dims, "")
	defer srv.Close()

	e := newWithClient(srv.URL, model, dims, "", srv.Client())
	ctx := context.Background()

	vecs, err := e.Embed(ctx, []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("len(vecs) = %d, want 2", len(vecs))
	}
	// makeServer fills vecs[i] with float32(i+1).
	for i, vec := range vecs {
		if len(vec) != dims {
			t.Errorf("vecs[%d] len = %d, want %d", i, len(vec), dims)
		}
		for _, v := range vec {
			if v != float32(i+1) {
				t.Errorf("vecs[%d] element = %v, want %v", i, v, float32(i+1))
				break
			}
		}
	}
}

func TestEmbed_multiBatch(t *testing.T) {
	const model = "BAAI/bge-m3"
	const dims = 8

	srv := makeServer(t, model, dims, "")
	defer srv.Close()

	e := newWithClient(srv.URL, model, dims, "", srv.Client())
	ctx := context.Background()

	inputs := []string{"first", "second", "third", "fourth", "fifth"}
	vecs, err := e.Embed(ctx, inputs)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != len(inputs) {
		t.Fatalf("len(vecs) = %d, want %d", len(vecs), len(inputs))
	}
	for i, vec := range vecs {
		if len(vec) != dims {
			t.Errorf("vecs[%d] len = %d, want %d", i, len(vec), dims)
		}
	}
}

func TestEmbed_empty(t *testing.T) {
	// Empty input must return nil without making an HTTP call.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected HTTP call for empty input")
	}))
	defer srv.Close()

	e := newWithClient(srv.URL, "model", 1024, "", srv.Client())
	vecs, err := e.Embed(context.Background(), nil)
	if err != nil {
		t.Fatalf("Embed(nil): %v", err)
	}
	if vecs != nil {
		t.Errorf("Embed(nil) = %v, want nil", vecs)
	}
}

func TestEmbed_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"service unavailable"}}`))
	}))
	defer srv.Close()

	e := newWithClient(srv.URL, "model", 1024, "", srv.Client())
	_, err := e.Embed(context.Background(), []string{"text"})
	if err == nil {
		t.Fatal("expected error for 503 response, got nil")
	}
}

func TestEmbedder_modelAndDims(t *testing.T) {
	e := New("http://localhost:8080", "BAAI/bge-m3", 1024, "")
	if e.Model() != "BAAI/bge-m3" {
		t.Errorf("Model() = %q, want %q", e.Model(), "BAAI/bge-m3")
	}
	if e.Dims() != 1024 {
		t.Errorf("Dims() = %d, want 1024", e.Dims())
	}
}
