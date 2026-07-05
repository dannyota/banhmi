package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// cloudRunEmbedder calls a remote banhmi-embedder HTTP service (Cloud Run L4)
// to embed texts. It replaces the Kaggle batch path for local dev.
type cloudRunEmbedder struct {
	endpoint string
	model    string
	dims     int
	client   *http.Client
}

// NewCloudRun returns an Embedder that calls the banhmi-embedder Cloud Run L4
// HTTP service. endpoint is the base URL (e.g. "https://banhmi-embedder-xxx.run.app").
func NewCloudRun(endpoint, model string, dims int) Embedder {
	return &cloudRunEmbedder{
		endpoint: strings.TrimRight(endpoint, "/"),
		model:    model,
		dims:     dims,
		client:   &http.Client{Timeout: defaultTimeout},
	}
}

func (e *cloudRunEmbedder) Model() string { return e.model }
func (e *cloudRunEmbedder) Dims() int     { return e.dims }

type cloudRunRequest struct {
	Texts []string `json:"texts"`
}

type cloudRunResponse struct {
	Model   string      `json:"model"`
	Dims    int         `json:"dims"`
	Vectors [][]float32 `json:"vectors"`
	Error   string      `json:"error,omitempty"`
}

const maxBatchSize = 256

func (e *cloudRunEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	// Split into batches to respect the server's 256-text limit.
	if len(texts) <= maxBatchSize {
		return e.embedBatch(ctx, texts)
	}

	out := make([][]float32, 0, len(texts))
	for i := 0; i < len(texts); i += maxBatchSize {
		end := i + maxBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := e.embedBatch(ctx, texts[i:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (e *cloudRunEmbedder) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(cloudRunRequest{Texts: texts})
	if err != nil {
		return nil, fmt.Errorf("cloudrun embed: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint+"/embed", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("cloudrun embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloudrun embed: POST %s/embed: %w", e.endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cloudrun embed: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cloudrun embed: %d: %s", resp.StatusCode, raw)
	}

	var result cloudRunResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("cloudrun embed: parse response: %w", err)
	}
	if result.Error != "" {
		return nil, fmt.Errorf("cloudrun embed: %s", result.Error)
	}
	if len(result.Vectors) != len(texts) {
		return nil, fmt.Errorf("cloudrun embed: got %d vectors, want %d", len(result.Vectors), len(texts))
	}
	return result.Vectors, nil
}
