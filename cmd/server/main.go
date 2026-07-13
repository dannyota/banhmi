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

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/base/dns"
	blog "danny.vn/banhmi/pkg/base/log"
)

var version = "dev"

func main() {
	cfgPath := flag.String("config", "config/config.yaml", "path to config file")
	addr := flag.String("addr", "", "listen address (overrides config server.addr)")
	flag.Parse()

	dns.InstallFallback()

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

	// SIGINT locally, SIGTERM on ECS / container runtimes — both shut down
	// gracefully. The received signal is logged so an orchestrator-initiated stop
	// (SIGTERM from ECS or a host shutdown) is distinguishable from a crash when
	// reading container logs after an outage.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Info("shutdown signal received", "signal", sig.String())
		signal.Stop(sigCh) // a second signal falls through to the default hard exit
		cancel()
	}()

	// One process serves every jurisdiction, sharing ONE query embedder. Each site
	// keeps its own corpus, MCP brief, and landing page; requests are routed by the
	// CloudFront-injected jurisdiction header (see sites.go).
	sites, closeSites, err := buildSites(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer closeSites()

	codes := make([]string, len(sites))
	for i, s := range sites {
		codes[i] = s.code
	}
	log.Info("serving jurisdictions", "codes", codes, "shared_embedder", true)

	return serve(ctx, addr, sites, cfg, log)
}

// serve mounts the per-jurisdiction handlers behind a router plus a shared health
// check, and runs the HTTP server until the context is cancelled (SIGINT), then
// shuts down gracefully — mirroring cmd/pipeline's ctx/signal pattern.
func serve(ctx context.Context, addr string, sites []*site, cfg *config.Config, log *slog.Logger) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// Everything else (the MCP surface + the landing page and its SEO/GEO side
	// files) is per jurisdiction: the router picks the corpus, then that site's own
	// mux picks the route.
	mux.Handle("/", recoverPanic(router(sites, log), log))

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
