package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/storage"

	"danny.vn/banhmi/pkg/rag/embed"
)

const maxTextsPerRequest = 2048
const maxBodyBytes = 10 << 20  // 10 MB
const maxTextChars = 32_768    // 32K chars per text
const embedBatchSize = 2048
const maxBatchRows  = 200_000 // cap for /embed-batch input rows

type embedReq struct {
	Texts []string `json:"texts"`
}

type embedResp struct {
	Model   string      `json:"model"`
	Dims    int         `json:"dims"`
	Vectors [][]float32 `json:"vectors"`
}

type embedBatchReq struct {
	Input  string `json:"input"`  // gs://bucket/path input JSONL
	Output string `json:"output"` // gs://bucket/path output JSONL.gz
}

type embedBatchResp struct {
	Output string `json:"output"`
	Rows   int    `json:"rows"`
}

type embedErrResp struct {
	Error string `json:"error"`
}

func serveEmbed(ctx context.Context, addr string, log *slog.Logger) error {
	embedder, err := newServeEmbedder()
	if err != nil {
		return fmt.Errorf("create embedder: %w", err)
	}

	embedToken := os.Getenv("BANHMI_EMBED_TOKEN")

	gcsClient, err := storage.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("create GCS client: %w", err)
	}
	defer func() { _ = gcsClient.Close() }()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /embed", embedHandler(embedder, log, embedToken))
	dataBucket := os.Getenv("BANHMI_GCS_DATA_BUCKET")
	mux.HandleFunc("POST /embed-batch", embedBatchHandler(embedder, gcsClient, dataBucket, log, embedToken))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
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

	go func() {
		<-ctx.Done()
		log.Info("embed server shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Error("embed server shutdown", "err", err)
		}
	}()

	log.Info("embed server listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("embed server: %w", err)
	}
	return nil
}

func embedHandler(embedder embed.Embedder, log *slog.Logger, token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("X-Embed-Token") != token {
			writeJSONError(w, http.StatusUnauthorized, "invalid or missing embed token")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		var req embedReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.Warn("embed: invalid request body", "err", err)
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if len(req.Texts) == 0 {
			writeJSONError(w, http.StatusBadRequest, "texts must be non-empty")
			return
		}
		if len(req.Texts) > maxTextsPerRequest {
			writeJSONError(w, http.StatusBadRequest,
				fmt.Sprintf("batch too large: %d texts, max %d", len(req.Texts), maxTextsPerRequest))
			return
		}
		for i, t := range req.Texts {
			if len(t) > maxTextChars {
				writeJSONError(w, http.StatusBadRequest,
					fmt.Sprintf("text %d exceeds %d char limit", i, maxTextChars))
				return
			}
		}

		start := time.Now()
		log.Info("embed: start", "texts", len(req.Texts))
		vecs, err := embedder.Embed(r.Context(), req.Texts)
		elapsed := time.Since(start)
		if err != nil {
			log.Error("embed: failed", "texts", len(req.Texts), "elapsed", elapsed, "err", err)
			writeJSONError(w, http.StatusInternalServerError, "embedding failed")
			return
		}
		log.Info("embed: done", "texts", len(req.Texts), "vectors", len(vecs), "elapsed", elapsed)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(embedResp{
			Model:   embedder.Model(),
			Dims:    embedder.Dims(),
			Vectors: vecs,
		})
	}
}

// embedBatchHandler handles POST /embed-batch: reads input JSONL from GCS,
// embeds in batches on GPU, writes gzipped output JSONL to GCS. The request
// body is {input: "gs://...", output: "gs://..."}. Both URIs must point to
// allowedBucket — the handler rejects cross-bucket access.
func embedBatchHandler(embedder embed.Embedder, gcs *storage.Client, allowedBucket string, log *slog.Logger, token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("X-Embed-Token") != token {
			writeJSONError(w, http.StatusUnauthorized, "invalid or missing embed token")
			return
		}

		var req embedBatchReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Input == "" || req.Output == "" {
			writeJSONError(w, http.StatusBadRequest, "input and output are required")
			return
		}

		inputBucket, inputPath, ok := parseGCSURI(req.Input)
		if !ok {
			writeJSONError(w, http.StatusBadRequest, "invalid input GCS URI")
			return
		}
		outputBucket, outputPath, ok := parseGCSURI(req.Output)
		if !ok {
			writeJSONError(w, http.StatusBadRequest, "invalid output GCS URI")
			return
		}
		if inputBucket != allowedBucket || outputBucket != allowedBucket {
			writeJSONError(w, http.StatusForbidden, "GCS bucket not allowed")
			return
		}

		start := time.Now()
		log.Info("embed-batch: start", "input", req.Input, "output", req.Output)

		rows, err := runBatchEmbed(r.Context(), embedder, gcs, inputBucket, inputPath, outputBucket, outputPath, log)
		elapsed := time.Since(start)
		if err != nil {
			log.Error("embed-batch: failed", "elapsed", elapsed, "err", err)
			writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("batch embed failed: %v", err))
			return
		}

		log.Info("embed-batch: done", "rows", rows, "elapsed", elapsed)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(embedBatchResp{Output: req.Output, Rows: rows})
	}
}

type batchInputRow struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}

type batchOutputRow struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

// runBatchEmbed reads input JSONL from GCS, embeds in batches, writes
// gzipped output JSONL to GCS. Returns the number of rows processed.
func runBatchEmbed(ctx context.Context, embedder embed.Embedder, gcs *storage.Client, inBucket, inPath, outBucket, outPath string, log *slog.Logger) (int, error) {
	// Read input.
	reader, err := gcs.Bucket(inBucket).Object(inPath).NewReader(ctx)
	if err != nil {
		return 0, fmt.Errorf("open input gs://%s/%s: %w", inBucket, inPath, err)
	}
	defer func() { _ = reader.Close() }()

	type indexedText struct {
		Index int
		Text  string
	}
	var inputs []indexedText
	sc := bufio.NewScanner(reader)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var row batchInputRow
		if err := json.Unmarshal(line, &row); err != nil {
			return 0, fmt.Errorf("parse input row: %w", err)
		}
		inputs = append(inputs, indexedText{Index: row.Index, Text: row.Text})
		if len(inputs) > maxBatchRows {
			return 0, fmt.Errorf("input exceeds %d row limit", maxBatchRows)
		}
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("scan input: %w", err)
	}
	_ = reader.Close()

	if len(inputs) == 0 {
		return 0, nil
	}

	log.Info("embed-batch: loaded input", "rows", len(inputs))

	// Write output as gzipped JSONL.
	outWriter := gcs.Bucket(outBucket).Object(outPath).NewWriter(ctx)
	outWriter.ContentType = "application/gzip"
	gz := gzip.NewWriter(outWriter)
	enc := json.NewEncoder(gz)

	total := 0
	for i := 0; i < len(inputs); i += embedBatchSize {
		end := i + embedBatchSize
		if end > len(inputs) {
			end = len(inputs)
		}
		batch := inputs[i:end]

		texts := make([]string, len(batch))
		for j, it := range batch {
			texts[j] = it.Text
		}

		vecs, err := embedder.Embed(ctx, texts)
		if err != nil {
			_ = gz.Close()
			_ = outWriter.Close()
			return 0, fmt.Errorf("embed batch [%d:%d]: %w", i, end, err)
		}

		for j, vec := range vecs {
			if err := enc.Encode(batchOutputRow{Index: batch[j].Index, Embedding: vec}); err != nil {
				_ = gz.Close()
				_ = outWriter.Close()
				return 0, fmt.Errorf("write output row %d: %w", batch[j].Index, err)
			}
			total++
		}
		log.Info("embed-batch: batch done", "from", i, "to", end, "total", total)
	}

	if err := gz.Close(); err != nil {
		_ = outWriter.Close()
		return 0, fmt.Errorf("close gzip: %w", err)
	}
	if err := outWriter.Close(); err != nil {
		return 0, fmt.Errorf("close GCS output: %w", err)
	}

	return total, nil
}

// parseGCSURI parses "gs://bucket/path" into bucket and path.
func parseGCSURI(uri string) (bucket, path string, ok bool) {
	if !strings.HasPrefix(uri, "gs://") {
		return "", "", false
	}
	rest := uri[5:]
	idx := strings.IndexByte(rest, '/')
	if idx < 1 {
		return "", "", false
	}
	return rest[:idx], rest[idx+1:], true
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(embedErrResp{Error: msg})
}
