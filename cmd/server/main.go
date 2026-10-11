// Command server serves the authenticated Vietnam website and MCP resource.
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

	appserve "danny.vn/banhmi/internal/app/serve"
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
	hostedConfig, err := appserve.LoadHostedConfig()
	if err != nil {
		return fmt.Errorf("load hosted config: %w", err)
	}
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

	queryEmbedder, err := appserve.NewVietnamQueryEmbedder(cfg)
	if err != nil {
		return fmt.Errorf("build Vietnam query embedder: %w", err)
	}
	if err := probeEmbedder(ctx, queryEmbedder, log); err != nil {
		return fmt.Errorf("embedder parity probe: %w", err)
	}
	hosted, err := appserve.NewHosted(ctx, cfg, hostedConfig, log, version, queryEmbedder)
	if err != nil {
		return err
	}
	defer hosted.Close()

	handler, stopLimiters, err := hostedHandler(hosted, hostedConfig.Identity.PublicOrigin, log)
	if err != nil {
		return err
	}
	defer stopLimiters()
	return serve(ctx, addr, handler, cfg, log)
}

// serve runs the HTTP server until the process context is cancelled.
func serve(ctx context.Context, addr string, handler http.Handler, cfg *config.Config, log *slog.Logger) error {
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

	log.Info("banhmi authenticated server listening", "app", cfg.Name, "addr", addr, "jurisdiction", "vn")
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("server stopped: %w", err)
	}
	return nil
}
