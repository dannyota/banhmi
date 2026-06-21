// Command mcp serves banhmi's knowledge base over MCP (stdio transport, JSON-RPC
// 2.0) so local LLM/agent clients (Onyx, Claude, …) can query banhmi as a tool.
// Dependencies are wired by the dig container in pkg/app; this command builds it,
// Invokes the Answerer + Retriever, and serves the MCP server over stdin/stdout
// until the context is cancelled. There is no HTTP — MCP here is stdio. stdout is
// the transport, so all logging goes to stderr (blog.New writes there).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/jackc/pgx/v5/pgxpool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"danny.vn/banhmi/pkg/app"
	"danny.vn/banhmi/pkg/base/config"
	blog "danny.vn/banhmi/pkg/base/log"
	"danny.vn/banhmi/pkg/mcp"
	"danny.vn/banhmi/pkg/rag/retrieve"
)

func main() {
	cfgPath := flag.String("config", "config/config.yaml", "path to config file")
	flag.Parse()

	log := blog.New(os.Getenv("BANHMI_LOG_LEVEL"))
	if err := run(*cfgPath, log); err != nil {
		log.Error("mcp", "err", err)
		os.Exit(1)
	}
}

func run(cfgPath string, log *slog.Logger) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	application, err := app.New(ctx, cfg, log, app.WithoutTemporal())
	if err != nil {
		return err
	}
	defer application.Close()

	return application.Container.Invoke(func(r retrieve.Retriever, pool *pgxpool.Pool) error {
		srv := mcp.New(r, log, mcp.WithPool(pool), mcp.WithJurisdiction(cfg.Jurisdiction))
		log.Info("banhmi mcp server running (stdio)")
		// Run blocks until ctx is cancelled (signal) or the transport closes
		// (client disconnect / EOF on stdin); both are clean shutdowns.
		if err := srv.Run(ctx, &mcpsdk.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("serve mcp: %w", err)
		}
		log.Info("banhmi mcp server stopped")
		return nil
	})
}
