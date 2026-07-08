package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"google.golang.org/api/idtoken"
)

const maxRetries = 3

// batchTimeout is the per-batch context timeout. Cloud Run cold start can take
// ~90s for GPU model loading, plus ~10-30s for a 256-text batch.
const batchTimeout = 5 * time.Minute

// cloudRunEmbedder calls a remote banhmi-embedder HTTP service (Cloud Run L4)
// to embed texts. It replaces the Kaggle batch path for local dev.
type cloudRunEmbedder struct {
	endpoint   string
	model      string
	dims       int
	client     *http.Client
	embedToken string // optional BANHMI_EMBED_TOKEN for app-level auth
}

// NewCloudRun returns an Embedder that calls the banhmi-embedder Cloud Run L4
// HTTP service. endpoint is the base URL (e.g. "https://banhmi-embedder-xxx.run.app").
// Auth via GOOGLE_APPLICATION_CREDENTIALS (service account key) or GCP metadata server.
func NewCloudRun(ctx context.Context, endpoint, model string, dims int) (Embedder, error) {
	audience := strings.TrimRight(endpoint, "/")
	client, err := idtoken.NewClient(ctx, audience)
	if err != nil {
		return nil, fmt.Errorf("cloudrun embed: ID token client (set GOOGLE_APPLICATION_CREDENTIALS): %w", err)
	}
	return &cloudRunEmbedder{
		endpoint:   audience,
		model:      model,
		dims:       dims,
		client:     client,
		embedToken: os.Getenv("BANHMI_EMBED_TOKEN"),
	}, nil
}

// newCloudRunWithClient is for testing — bypasses GCP auth.
func newCloudRunWithClient(endpoint, model string, dims int, client *http.Client) Embedder {
	return &cloudRunEmbedder{
		endpoint:   strings.TrimRight(endpoint, "/"),
		model:      model,
		dims:       dims,
		client:     client,
		embedToken: os.Getenv("BANHMI_EMBED_TOKEN"),
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

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt*attempt) * 5 * time.Second
			slog.Warn("cloudrun embed: retrying", "attempt", attempt, "delay", delay, "err", lastErr)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		batchCtx, cancel := context.WithTimeout(ctx, batchTimeout)
		var result [][]float32
		result, lastErr = e.doPost(batchCtx, body, len(texts))
		cancel()
		if lastErr == nil {
			return result, nil
		}
		if !isTransient(lastErr) {
			return nil, lastErr
		}
	}
	return nil, lastErr
}

func (e *cloudRunEmbedder) doPost(ctx context.Context, body []byte, wantN int) ([][]float32, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint+"/embed", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("cloudrun embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.embedToken != "" {
		req.Header.Set("X-Embed-Token", e.embedToken)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloudrun embed: POST %s/embed: %w", e.endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cloudrun embed: read response: %w", err)
	}

	if resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusGatewayTimeout {
		return nil, fmt.Errorf("cloudrun embed: %d: %s", resp.StatusCode, raw)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &permanentError{fmt.Errorf("cloudrun embed: %d: %s", resp.StatusCode, raw)}
	}

	var result cloudRunResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, &permanentError{fmt.Errorf("cloudrun embed: parse response: %w", err)}
	}
	if result.Error != "" {
		return nil, &permanentError{fmt.Errorf("cloudrun embed: %s", result.Error)}
	}
	if len(result.Vectors) != wantN {
		return nil, &permanentError{fmt.Errorf("cloudrun embed: got %d vectors, want %d", len(result.Vectors), wantN)}
	}
	return result.Vectors, nil
}

type permanentError struct{ error }

func isTransient(err error) bool {
	if err == nil {
		return false
	}
	var pe *permanentError
	if errors.As(err, &pe) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "connection timed out") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "server misbehaving") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "502") ||
		strings.Contains(msg, "503") ||
		strings.Contains(msg, "504")
}
