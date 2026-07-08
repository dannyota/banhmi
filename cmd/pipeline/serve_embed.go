package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"danny.vn/banhmi/pkg/rag/embed"
)

const maxTextsPerRequest = 256
const maxBodyBytes = 10 << 20 // 10 MB
const maxTextChars = 32_768   // 32K chars per text

type embedReq struct {
	Texts []string `json:"texts"`
}

type embedResp struct {
	Model   string      `json:"model"`
	Dims    int         `json:"dims"`
	Vectors [][]float32 `json:"vectors"`
}

type embedErrResp struct {
	Error string `json:"error"`
}

func serveEmbed(ctx context.Context, addr string, log *slog.Logger) error {
	embedder, err := newServeEmbedder()
	if err != nil {
		return fmt.Errorf("create embedder: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /embed", embedHandler(embedder, log))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
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

func embedHandler(embedder embed.Embedder, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

		vecs, err := embedder.Embed(r.Context(), req.Texts)
		if err != nil {
			log.Error("embed", "texts", len(req.Texts), "err", err)
			writeJSONError(w, http.StatusInternalServerError, "embedding failed")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(embedResp{
			Model:   embedder.Model(),
			Dims:    embedder.Dims(),
			Vectors: vecs,
		})
	}
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(embedErrResp{Error: msg})
}
