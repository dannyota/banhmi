// Command server exposes banhmi's knowledge base to remote user-owned agents over
// MCP (Streamable HTTP), wired by the dig container in pkg/app. It is the same
// evidence-only MCP surface as cmd/mcp (stdio), served over HTTP so hosted agents
// (Claude.ai, ChatGPT, Gemini, Grok) can connect. banhmi serves evidence; the
// connecting model decides the answer. This is the surface deployed to GCP Cloud Run.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"danny.vn/banhmi/pkg/app"
	"danny.vn/banhmi/pkg/base/config"
	blog "danny.vn/banhmi/pkg/base/log"
	"danny.vn/banhmi/pkg/mcp"
	"danny.vn/banhmi/pkg/rag/retrieve"
)

func main() {
	cfgPath := flag.String("config", "config/config.yaml", "path to config file")
	addr := flag.String("addr", "", "listen address (overrides config server.addr)")
	flag.Parse()

	log := blog.New(os.Getenv("BANHMI_LOG_LEVEL"))
	if err := run(*cfgPath, *addr, log); err != nil {
		log.Error("server", "err", err)
		os.Exit(1)
	}
}

func run(cfgPath, addrOverride string, log *slog.Logger) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	// Listen-address precedence: -addr flag > $PORT (Cloud Run / PaaS) > config > default.
	addr := cfg.Server.Addr
	if port := os.Getenv("PORT"); port != "" {
		addr = ":" + port
	}
	if addrOverride != "" {
		addr = addrOverride
	}
	if addr == "" {
		addr = ":8088"
	}

	// SIGINT locally, SIGTERM on Cloud Run / container runtimes — both shut down gracefully.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application, err := app.New(ctx, cfg, log, app.WithoutTemporal())
	if err != nil {
		return err
	}
	defer application.Close()

	return application.Container.Invoke(func(r retrieve.Retriever, pool *pgxpool.Pool) error {
		return serve(ctx, addr, mcp.New(r, log, mcp.WithPool(pool), mcp.WithJurisdiction(cfg.Jurisdiction)), cfg, log)
	})
}

// serve mounts the MCP-over-HTTP handler and a health check on one mux and runs the
// HTTP server until the context is cancelled (SIGINT), then shuts down gracefully —
// mirroring cmd/worker's ctx/signal pattern.
func serve(ctx context.Context, addr string, srv *mcp.Server, cfg *config.Config, log *slog.Logger) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// The evidence-only MCP surface over Streamable HTTP, for remote user-owned agents.
	// Wrapped in cross-origin protection (MCP Origin-validation: reject cross-site
	// browser requests, allow server-to-server agents).
	mux.Handle("/mcp", crossOriginProtected(srv.HTTPHandler(), log))

	// The MCP server is the only public-facing component: gate it with API-key auth +
	// per-IP rate limiting + a body cap (see middleware.go).
	handler, stopEvictor := secure(mux, log)
	defer stopEvictor()

	// Timeouts close slow-loris vectors. WriteTimeout is intentionally left unset so
	// MCP Streamable-HTTP (SSE) responses are not cut mid-stream; IdleTimeout reaps
	// idle keep-alives instead.
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		<-ctx.Done()
		log.Info("server shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Error("server shutdown", "err", err)
		}
	}()

	log.Info("banhmi MCP server listening", "app", cfg.Name, "addr", addr, "endpoint", "/mcp")
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("server stopped: %w", err)
	}
	return nil
}
