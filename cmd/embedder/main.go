// Command embedder serves OpenAI-compatible batch POST /embeddings and
// singleton POST /query/embeddings endpoints backed by an in-process ONNX
// Runtime embedder (Qwen3-Embedding-0.6B FP16). Built with -tags onnx;
// without the tag the process exits with an instructive error.
//
// Env:
//
//	BANHMI_EMBED_ADDR        — listen address (default ":8089")
//	BANHMI_EMBED_TOKEN       — expected Bearer token; empty = no auth (local dev)
//	BANHMI_EMBED_CONCURRENCY — max concurrent inference runs (default: NumCPU, min 2)
//	BANHMI_EMBED_MAX_QUEUED  - max requests waiting for inference (default: concurrency)
//	BANHMI_ONNX_MODEL     — path to ONNX model file
//	BANHMI_ONNX_TOKENIZER — path to tokenizer.json
//	BANHMI_ONNX_LIB       — path to libonnxruntime.so (optional)
//	BANHMI_ONNX_CUDA      — "1" to enable CUDA
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"danny.vn/banhmi/pkg/base/config"
	blog "danny.vn/banhmi/pkg/base/log"
	"danny.vn/banhmi/pkg/rag/embed"
	"danny.vn/banhmi/pkg/rag/embed/onnxembed"
)

const maxTextsPerRequest = 2048
const maxBodyBytes = 10 << 20 // 10 MB
const maxInputBytes = 32 << 10
const maxQueryBodyBytes = 256 << 10 // Protective limit; tune from hosted request measurements.

func main() {
	log := blog.New(os.Getenv("BANHMI_LOG_LEVEL"))
	if err := run(log); err != nil {
		log.Error("embedder", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	admissionCfg, err := loadAdmissionConfig()
	if err != nil {
		return err
	}
	embedder, err := newEmbedder(admissionCfg.MaxConcurrent, admissionCfg.MaxQueued, admissionCfg.MaxBatchTexts)
	if err != nil {
		return fmt.Errorf("create embedder: %w", err)
	}
	admission, err := onnxembed.NewAdmission(admissionCfg)
	if err != nil {
		return fmt.Errorf("configure embedding admission: %w", err)
	}

	addr := os.Getenv("BANHMI_EMBED_ADDR")
	if addr == "" {
		addr = ":8089"
	}
	token := os.Getenv("BANHMI_EMBED_TOKEN")

	// Warm-up: embed one short probe text and flip the ready flag.
	var ready atomic.Bool
	log.Info("embedder warm-up starting")
	vecs, err := embedder.Embed(context.Background(), []string{"warm-up probe"})
	if err != nil {
		return fmt.Errorf("warm-up inference failed: %w", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != config.EmbedDims {
		return fmt.Errorf("warm-up returned unexpected dims: got %d vectors, first len %d, want 1x%d",
			len(vecs), len(vecs[0]), config.EmbedDims)
	}
	ready.Store(true)
	log.Info("embedder warm-up complete", "dims", len(vecs[0]))

	mux := http.NewServeMux()
	mux.HandleFunc("POST /embeddings", embeddingsHandler(embedder, admission, log, token))
	mux.HandleFunc("POST /query/embeddings", queryEmbeddingsHandler(embedder, admission, log, token))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Minute,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		log.Info("embedder shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Error("embedder shutdown", "err", err)
		}
	}()

	log.Info("embedder listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("embedder server: %w", err)
	}
	return nil
}

// --- OpenAI-compatible types ---

// embeddingsRequest accepts OpenAI-compatible input: "input" may be a JSON array
// of strings or a single string.
type embeddingsRequest struct {
	Model string          `json:"model"`
	Input json.RawMessage `json:"input"`
}

type embeddingData struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

type embeddingsResponse struct {
	Object string          `json:"object"`
	Model  string          `json:"model"`
	Data   []embeddingData `json:"data"`
}

type errorDetail struct {
	Message string `json:"message"`
}

type errorResponse struct {
	Error errorDetail `json:"error"`
}

// parseInput handles both string and []string forms of the "input" field.
func parseInput(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, errors.New("input is required")
	}
	// Try array first (most common).
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr, nil
	}
	// Try single string.
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, nil
	}
	return nil, errors.New("input must be a string or array of strings")
}

func defaultAdmissionConfig() onnxembed.AdmissionConfig {
	concurrency := runtime.NumCPU()
	if concurrency < 2 {
		concurrency = 2
	}
	return onnxembed.AdmissionConfig{
		MaxBatchTexts: maxTextsPerRequest,
		MaxInputBytes: maxInputBytes,
		// This protective queue default is unmeasured. Hosted load tests must
		// set a capacity value for the target host.
		MaxQueued:     concurrency,
		MaxConcurrent: concurrency,
	}
}

func loadAdmissionConfig() (onnxembed.AdmissionConfig, error) {
	cfg := defaultAdmissionConfig()
	var err error
	if cfg.MaxConcurrent, err = positiveEnvInt("BANHMI_EMBED_CONCURRENCY", cfg.MaxConcurrent); err != nil {
		return onnxembed.AdmissionConfig{}, err
	}
	if cfg.MaxQueued, err = positiveEnvInt("BANHMI_EMBED_MAX_QUEUED", cfg.MaxConcurrent); err != nil {
		return onnxembed.AdmissionConfig{}, err
	}
	return cfg, nil
}

func positiveEnvInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return value, nil
}

type inferenceAdmission interface {
	Acquire(context.Context) (func(), error)
	ValidateBatch(int) error
	ValidateInput(string) error
}

func embeddingsHandler(embedder embed.Embedder, admission inferenceAdmission, log *slog.Logger, token string) http.HandlerFunc {
	return serveEmbeddings(embedder, admission, log, token, maxBodyBytes, false)
}

func queryEmbeddingsHandler(embedder embed.Embedder, admission inferenceAdmission, log *slog.Logger, token string) http.HandlerFunc {
	return serveEmbeddings(embedder, admission, log, token, maxQueryBodyBytes, true)
}

func serveEmbeddings(embedder embed.Embedder, admission inferenceAdmission, log *slog.Logger, token string, bodyLimit int64, singleton bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Auth check.
		if token != "" {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer "+token {
				writeError(w, http.StatusUnauthorized, "invalid or missing authorization token")
				return
			}
		}

		release, err := admission.Acquire(r.Context())
		if err != nil {
			if errors.Is(err, onnxembed.ErrOverloaded) {
				w.Header().Set("Retry-After", "1")
				writeError(w, http.StatusServiceUnavailable, "embedding service is busy; retry later")
			}
			return
		}
		defer release()

		r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
		var req embeddingsRequest
		if err = json.NewDecoder(r.Body).Decode(&req); err != nil {
			if r.Context().Err() != nil {
				return
			}
			log.Warn("embeddings: invalid request body", "err", err)
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		texts, err := parseInput(req.Input)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(texts) == 0 {
			writeError(w, http.StatusBadRequest, "input must be non-empty")
			return
		}
		if singleton && len(texts) > 1 {
			writeError(w, http.StatusBadRequest, "input must contain exactly one text")
			return
		}
		if err := admission.ValidateBatch(len(texts)); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		for i, text := range texts {
			if err := admission.ValidateInput(text); err != nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("text %d: %v", i, err))
				return
			}
		}

		start := time.Now()
		log.Info("embeddings: start", "texts", len(texts))
		vecs, err := embedder.Embed(r.Context(), texts)
		release()
		elapsed := time.Since(start)
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			log.Error("embeddings: failed", "texts", len(texts), "elapsed", elapsed, "err", err)
			writeError(w, http.StatusInternalServerError, "embedding failed")
			return
		}
		log.Info("embeddings: done", "texts", len(texts), "vectors", len(vecs), "elapsed", elapsed)

		data := make([]embeddingData, len(vecs))
		for i, v := range vecs {
			data[i] = embeddingData{
				Object:    "embedding",
				Index:     i,
				Embedding: v,
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(embeddingsResponse{
			Object: "list",
			Model:  config.EmbedModel,
			Data:   data,
		})
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: errorDetail{Message: msg}})
}
