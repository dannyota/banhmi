package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/rag/embed"
)

// fakeEmbedder implements embed.Embedder for tests.
type fakeEmbedder struct {
	model string
	dims  int
}

func (f *fakeEmbedder) Model() string { return f.model }
func (f *fakeEmbedder) Dims() int     { return f.dims }
func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		vec := make([]float32, f.dims)
		for j := range vec {
			vec[j] = float32(i + 1)
		}
		out[i] = vec
	}
	return out, nil
}

func newTestHandler(token string) http.Handler {
	log := testLogger()
	emb := &fakeEmbedder{model: config.EmbedModel, dims: config.EmbedDims}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /embeddings", embeddingsHandler(emb, log, token))
	return mux
}

func testLogger() *slog.Logger {
	return slog.Default()
}

// TestHandler_roundTrip proves the existing pkg/rag/embed.New client
// can successfully call our handler and parse the response.
func TestHandler_roundTrip(t *testing.T) {
	const token = "test-secret"
	srv := httptest.NewServer(newTestHandler(token))
	defer srv.Close()

	client := embed.New(srv.URL, config.EmbedModel, config.EmbedDims, token)
	vecs, err := client.Embed(context.Background(), []string{"hello world", "second text"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("len(vecs) = %d, want 2", len(vecs))
	}
	if len(vecs[0]) != config.EmbedDims {
		t.Errorf("vecs[0] dims = %d, want %d", len(vecs[0]), config.EmbedDims)
	}
}

// TestHandler_roundTrip_noAuth tests the no-auth path (empty token env).
func TestHandler_roundTrip_noAuth(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(""))
	defer srv.Close()

	client := embed.New(srv.URL, config.EmbedModel, config.EmbedDims, "")
	vecs, err := client.Embed(context.Background(), []string{"test"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 1 {
		t.Fatalf("len(vecs) = %d, want 1", len(vecs))
	}
}

// TestHandler_authFailure tests auth rejection.
func TestHandler_authFailure(t *testing.T) {
	srv := httptest.NewServer(newTestHandler("correct-token"))
	defer srv.Close()

	client := embed.New(srv.URL, config.EmbedModel, config.EmbedDims, "wrong-token")
	_, err := client.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error for wrong token, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %q, want to contain 401", err.Error())
	}
}

// TestHandler_batchTooLarge tests the max texts limit.
func TestHandler_batchTooLarge(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(""))
	defer srv.Close()

	// Build a request with too many texts.
	texts := make([]string, maxTextsPerRequest+1)
	for i := range texts {
		texts[i] = "x"
	}
	body, _ := json.Marshal(map[string]any{"model": "m", "input": texts})
	resp, err := http.Post(srv.URL+"/embeddings", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "batch too large") {
		t.Errorf("body = %s, want 'batch too large'", raw)
	}
}

// TestHandler_textTooLong tests per-text char limit enforcement.
func TestHandler_textTooLong(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(""))
	defer srv.Close()

	longText := strings.Repeat("a", maxTextChars+1)
	body, _ := json.Marshal(map[string]any{"model": "m", "input": []string{longText}})
	resp, err := http.Post(srv.URL+"/embeddings", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "char limit") {
		t.Errorf("body = %s, want 'char limit'", raw)
	}
}

// TestHandler_stringInput tests single-string input (OpenAI compat).
func TestHandler_stringInput(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(""))
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{"model": "m", "input": "single string"})
	resp, err := http.Post(srv.URL+"/embeddings", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, raw)
	}
	var result embeddingsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Data) != 1 {
		t.Errorf("data len = %d, want 1", len(result.Data))
	}
	if result.Object != "list" {
		t.Errorf("object = %q, want 'list'", result.Object)
	}
	if result.Model != config.EmbedModel {
		t.Errorf("model = %q, want %q", result.Model, config.EmbedModel)
	}
}

// TestHandler_emptyInput tests empty input rejection.
func TestHandler_emptyInput(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(""))
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{"model": "m", "input": []string{}})
	resp, err := http.Post(srv.URL+"/embeddings", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestHandler_errorResponse tests the error response format.
func TestHandler_errorResponse(t *testing.T) {
	srv := httptest.NewServer(newTestHandler("secret"))
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{"model": "m", "input": []string{"x"}})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/embeddings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var errResp errorResponse
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if errResp.Error.Message == "" {
		t.Error("expected non-empty error message")
	}
}

// Ensure fakeEmbedder satisfies the Embedder interface.
var _ embed.Embedder = (*fakeEmbedder)(nil)

// TestParseInput covers both string and array forms.
func TestParseInput(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int
		wantErr bool
	}{
		{"array", `["a","b","c"]`, 3, false},
		{"single_string", `"hello"`, 1, false},
		{"empty_array", `[]`, 0, false},
		{"null", `null`, 0, true},
		{"number", `42`, 0, true},
		{"object", `{"foo":"bar"}`, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseInput(json.RawMessage(tt.raw))
			if (err != nil) != tt.wantErr {
				t.Errorf("parseInput(%s) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
				return
			}
			if !tt.wantErr && len(got) != tt.want {
				t.Errorf("parseInput(%s) len = %d, want %d", tt.raw, len(got), tt.want)
			}
		})
	}
}
