// Command mcp serves banhmi's knowledge base over MCP (stdio transport, JSON-RPC
// 2.0) so local LLM/agent clients (Onyx, Claude, …) can query banhmi as a tool.
// Dependencies are wired by the lean serving composition; this command builds it
// and serves the MCP server over stdin/stdout
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

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	appserve "danny.vn/banhmi/internal/app/serve"
	"danny.vn/banhmi/pkg/base/config"
	blog "danny.vn/banhmi/pkg/base/log"
)

var version = "dev"

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

	embedder, err := appserve.NewQueryEmbedder(cfg)
	if err != nil {
		return fmt.Errorf("build query embedder: %w", err)
	}
	application, err := appserve.New(ctx, cfg, log, version, embedder, nil, false)
	if err != nil {
		return err
	}
	defer application.Close()

	log.Info("banhmi mcp server running (stdio)")
	if err := application.MCP.Run(ctx, &mcpsdk.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("serve mcp: %w", err)
	}
	log.Info("banhmi mcp server stopped")
	return nil
}
