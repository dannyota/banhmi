// Package embed defines the Embedder interface and provides an OpenAI-compatible
// implementation that works with the fixed self-hosted BGE-M3 OpenVINO endpoint
// used by Index.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Embedder turns a batch of texts into embedding vectors.
// Implementations are selected by config; no vendor is hardcoded.
//
// Model returns the model identifier written to gold.chunk_embedding.model.
// Dims returns the vector dimension written to gold.chunk_embedding.dims.
// Embed sends texts to the embedding endpoint and returns one vector per input.
// The returned slice is always len(texts) on success.
type Embedder interface {
	Model() string
	Dims() int
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// defaultTimeout is the per-request HTTP timeout. CPU BGE-M3 can take more than
// a minute for full 32-text batches, especially while other requests are queued.
// Prefer slow completion over silently skipping embeddings.
const defaultTimeout = 5 * time.Minute

// openAIEmbedder POSTs to an OpenAI-compatible /embeddings endpoint.
type openAIEmbedder struct {
	endpoint string // e.g. "http://localhost:8080" — no trailing slash
	model    string
	dims     int
	apiKey   string // optional Bearer token; empty = no Authorization header
	client   *http.Client
}

// New returns an Embedder backed by an OpenAI-compatible /embeddings endpoint.
// endpoint must be non-empty (e.g. "http://localhost:8080").
// apiKey is optional; pass "" when the endpoint needs no auth.
func New(endpoint, model string, dims int, apiKey string) Embedder {
	return &openAIEmbedder{
		endpoint: strings.TrimRight(endpoint, "/"),
		model:    model,
		dims:     dims,
		apiKey:   apiKey,
		client:   &http.Client{Timeout: defaultTimeout},
	}
}

// newWithClient is used in tests to inject a custom HTTP client (e.g. httptest).
func newWithClient(endpoint, model string, dims int, apiKey string, c *http.Client) Embedder {
	return &openAIEmbedder{
		endpoint: strings.TrimRight(endpoint, "/"),
		model:    model,
		dims:     dims,
		apiKey:   apiKey,
		client:   c,
	}
}

func (e *openAIEmbedder) Model() string { return e.model }
func (e *openAIEmbedder) Dims() int     { return e.dims }

// embedRequest is the JSON body sent to /embeddings.
type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embedResponse is the JSON body returned from /embeddings.
type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Embed sends texts to the endpoint in one request and returns the vectors.
// The returned slice is parallel to texts: result[i] is the embedding of texts[i].
func (e *openAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(embedRequest{Model: e.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("embed: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: POST %s/embeddings: %w", e.endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("embed: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Try to surface the API error message.
		var apiErr embedResponse
		if jerr := json.Unmarshal(raw, &apiErr); jerr == nil && apiErr.Error != nil {
			return nil, fmt.Errorf("embed: endpoint returned %d: %s", resp.StatusCode, apiErr.Error.Message)
		}
		return nil, fmt.Errorf("embed: endpoint returned %d: %s", resp.StatusCode, raw)
	}

	var result embedResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("embed: parse response: %w", err)
	}
	if result.Error != nil {
		return nil, fmt.Errorf("embed: API error: %s", result.Error.Message)
	}

	// The OpenAI spec guarantees data[i].index == i, but we sort by index to
	// be safe — some self-hosted servers re-order responses.
	out := make([][]float32, len(texts))
	for _, d := range result.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("embed: response index %d out of range [0,%d)", d.Index, len(texts))
		}
		out[d.Index] = d.Embedding
	}
	for i, v := range out {
		if v == nil {
			return nil, fmt.Errorf("embed: no embedding returned for index %d", i)
		}
	}
	return out, nil
}
