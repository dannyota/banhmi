package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/rag/embed"
	"danny.vn/banhmi/pkg/rag/embed/onnxembed"
)

// fakeEmbedder implements embed.Embedder for tests.
type fakeEmbedder struct {
	model   string
	dims    int
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
	err     error
}

func (f *fakeEmbedder) Model() string { return f.model }
func (f *fakeEmbedder) Dims() int     { return f.dims }
func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
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
	emb := &fakeEmbedder{model: config.EmbedModel, dims: config.EmbedDims}
	return newTestHandlerWithEmbedder(token, emb, defaultAdmissionConfig())
}

func newTestHandlerWithEmbedder(token string, emb embed.Embedder, cfg onnxembed.AdmissionConfig) http.Handler {
	admission, err := onnxembed.NewAdmission(cfg)
	if err != nil {
		panic(err)
	}
	return newTestHandlerWithAdmission(token, emb, admission)
}

func newTestHandlerWithAdmission(token string, emb embed.Embedder, admission inferenceAdmission) http.Handler {
	log := testLogger()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /embeddings", embeddingsHandler(emb, admission, log, token))
	mux.HandleFunc("POST /query/embeddings", queryEmbeddingsHandler(emb, admission, log, token))
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

func TestQueryHandler_rejectsMultipleInputs(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(""))
	defer srv.Close()

	texts := []string{"first", "second"}
	body, _ := json.Marshal(map[string]any{"model": "m", "input": texts})
	resp, err := http.Post(srv.URL+"/query/embeddings", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "exactly one text") {
		t.Errorf("body = %s, want 'exactly one text'", raw)
	}
}

func TestHandler_rejectsBatchOverLegacyLimit(t *testing.T) {
	texts := make([]string, maxTextsPerRequest+1)
	for i := range texts {
		texts[i] = "x"
	}
	body, _ := json.Marshal(map[string]any{"model": "m", "input": texts})
	req := httptest.NewRequest(http.MethodPost, "/embeddings", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	newTestHandler("").ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "batch too large") {
		t.Fatalf("body = %s, want batch limit error", rec.Body.String())
	}
}

func TestQueryHandler_rejectsInputOverByteLimit(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(""))
	defer srv.Close()

	longText := strings.Repeat("a", maxInputBytes+1)
	body, _ := json.Marshal(map[string]any{"model": "m", "input": []string{longText}})
	resp, err := http.Post(srv.URL+"/query/embeddings", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "byte limit") {
		t.Errorf("body = %s, want 'byte limit'", raw)
	}
}

func TestQueryHandler_acceptsInputAtExactByteLimit(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"model": "m", "input": strings.Repeat("a", maxInputBytes)})
	req := httptest.NewRequest(http.MethodPost, "/query/embeddings", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	newTestHandler("").ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestQueryHandler_acceptsStringInput(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(""))
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{"model": "m", "input": "single string"})
	resp, err := http.Post(srv.URL+"/query/embeddings", "application/json", bytes.NewReader(body))
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

func TestQueryHandler_acceptsSingletonArray(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(""))
	defer srv.Close()
	client := embed.New(srv.URL+"/query", config.EmbedModel, config.EmbedDims, "")
	vecs, err := client.Embed(context.Background(), []string{"single string"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 1 {
		t.Fatalf("len(vecs) = %d, want 1", len(vecs))
	}
}

func TestLoadAdmissionConfigRejectsInvalidLimits(t *testing.T) {
	t.Setenv("BANHMI_EMBED_CONCURRENCY", "0")
	if _, err := loadAdmissionConfig(); err == nil {
		t.Fatal("loadAdmissionConfig concurrency error = nil")
	}
	t.Setenv("BANHMI_EMBED_CONCURRENCY", "1")
	t.Setenv("BANHMI_EMBED_MAX_QUEUED", "invalid")
	if _, err := loadAdmissionConfig(); err == nil {
		t.Fatal("loadAdmissionConfig queue error = nil")
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

func TestHandler_authenticatesBeforeAdmission(t *testing.T) {
	emb := &fakeEmbedder{model: config.EmbedModel, dims: config.EmbedDims}
	admission := fakeAdmission{
		acquire: func(context.Context) (func(), error) {
			t.Fatal("admission checked before authentication")
			return nil, nil
		},
	}
	h := newTestHandlerWithAdmission("secret", emb, admission)

	req := httptest.NewRequest(http.MethodPost, "/query/embeddings", strings.NewReader(`{"input":"unauthorized"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestHandler_returnsRetryableServiceUnavailableWhenQueueIsFull(t *testing.T) {
	emb := &fakeEmbedder{model: config.EmbedModel, dims: config.EmbedDims}
	admission := fakeAdmission{
		acquire: func(context.Context) (func(), error) {
			return nil, onnxembed.ErrOverloaded
		},
	}
	h := newTestHandlerWithAdmission("", emb, admission)

	req := httptest.NewRequest(http.MethodPost, "/query/embeddings", failOnRead{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want 1", got)
	}
}

type failOnRead struct{}

func (failOnRead) Read([]byte) (int, error) {
	panic("request body read before admission")
}

type fakeAdmission struct {
	acquire func(context.Context) (func(), error)
}

func (f fakeAdmission) Acquire(ctx context.Context) (func(), error) {
	return f.acquire(ctx)
}

func (fakeAdmission) ValidateInput(string) error { return nil }

func (fakeAdmission) ValidateBatch(int) error { return nil }

func TestHandler_cancelledQueuedRequestDoesNotRunInference(t *testing.T) {
	emb := &fakeEmbedder{model: config.EmbedModel, dims: config.EmbedDims, started: make(chan struct{}, 1), release: make(chan struct{})}
	h := newTestHandlerWithEmbedder("", emb, onnxembed.AdmissionConfig{MaxBatchTexts: maxTextsPerRequest, MaxInputBytes: maxInputBytes, MaxQueued: 1, MaxConcurrent: 1})
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/embeddings", strings.NewReader(`{"input":"first"}`)))
	}()
	<-emb.started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/embeddings", strings.NewReader(`{"input":"second"}`)).WithContext(ctx)
	h.ServeHTTP(httptest.NewRecorder(), req)

	emb.mu.Lock()
	calls := emb.calls
	emb.mu.Unlock()
	if calls != 1 {
		t.Fatalf("inference calls = %d, want 1", calls)
	}
	close(emb.release)
	<-firstDone
}

func TestHandler_releasesAdmissionAfterInferenceError(t *testing.T) {
	emb := &fakeEmbedder{model: config.EmbedModel, dims: config.EmbedDims, err: errors.New("inference failed")}
	h := newTestHandlerWithEmbedder("", emb, onnxembed.AdmissionConfig{MaxBatchTexts: maxTextsPerRequest, MaxInputBytes: maxInputBytes, MaxQueued: 1, MaxConcurrent: 1})
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/embeddings", strings.NewReader(`{"input":"query"}`)))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("request %d status = %d, want %d", i, rec.Code, http.StatusInternalServerError)
		}
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
