package serve

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/base/jurisdiction"
	"danny.vn/banhmi/pkg/identity"
	"danny.vn/banhmi/pkg/mcp"
	"danny.vn/banhmi/pkg/oauthserver"
	"danny.vn/banhmi/pkg/rag/embed"
	"danny.vn/banhmi/pkg/web"
)

const oauthClientsFileEnv = "BANHMI_OAUTH_CLIENTS_FILE"

type HostedConfig struct {
	Identity identity.Config
	OAuth    oauthserver.Config
	Clients  *oauthserver.ClientRegistry
}

func LoadHostedConfig() (HostedConfig, error) {
	identityConfig, err := identity.LoadConfig()
	if err != nil {
		return HostedConfig{}, fmt.Errorf("load identity config: %w", err)
	}
	oauthConfig, err := oauthserver.LoadConfig(identityConfig.PublicOrigin)
	if err != nil {
		return HostedConfig{}, fmt.Errorf("load OAuth config: %w", err)
	}
	path := strings.TrimSpace(os.Getenv(oauthClientsFileEnv))
	if path == "" {
		return HostedConfig{}, fmt.Errorf("%s is required", oauthClientsFileEnv)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return HostedConfig{}, fmt.Errorf("read %s: %w", oauthClientsFileEnv, err)
	}
	clients, err := oauthserver.ParseClientRegistry(data, oauthConfig.Resource)
	if err != nil {
		return HostedConfig{}, fmt.Errorf("parse %s: %w", oauthClientsFileEnv, err)
	}
	return HostedConfig{Identity: identityConfig, OAuth: oauthConfig, Clients: clients}, nil
}

type Hosted struct {
	Identity *identity.Manager
	OAuth    *oauthserver.Server
	Web      *web.Server
	Evidence *mcp.Server

	corpus   *App
	authPool *pgxpool.Pool
}

func NewHosted(ctx context.Context, base *config.Config, hosted HostedConfig, log *slog.Logger, version string, queryEmbedder embed.Embedder) (*Hosted, error) {
	if base == nil || hosted.Clients == nil || queryEmbedder == nil {
		return nil, fmt.Errorf("hosted serving configuration is incomplete")
	}
	authPool, err := pgxpool.New(ctx, hosted.Identity.AuthDatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("open authentication pool: %w", err)
	}
	closeAuth := true
	defer func() {
		if closeAuth {
			authPool.Close()
		}
	}()
	manager, err := identity.NewManager(identity.NewPostgresStore(authPool), hosted.Identity)
	if err != nil {
		return nil, fmt.Errorf("build identity manager: %w", err)
	}
	oauth, err := oauthserver.NewServer(authPool, hosted.Clients, manager, hosted.OAuth)
	if err != nil {
		return nil, fmt.Errorf("build OAuth server: %w", err)
	}
	cfg := CountryConfig(base, "vn")
	corpus, err := New(ctx, cfg, log, version, queryEmbedder, nil, envBool("BANHMI_TRUST_PROXY"))
	if err != nil {
		return nil, fmt.Errorf("build Vietnam evidence service: %w", err)
	}
	website, err := web.New(corpus.Operations, manager, hosted.Identity.PublicOrigin)
	if err != nil {
		corpus.Close()
		return nil, fmt.Errorf("build website: %w", err)
	}
	closeAuth = false
	return &Hosted{Identity: manager, OAuth: oauth, Web: website, Evidence: corpus.MCP, corpus: corpus, authPool: authPool}, nil
}

func (h *Hosted) Register(mux *http.ServeMux) {
	h.Identity.Register(mux)
	h.OAuth.Register(mux)
	h.Web.Register(mux)
}

func (h *Hosted) MCPHandler() http.Handler { return h.OAuth.Protect(h.Evidence.HTTPHandler()) }

func (h *Hosted) Close() {
	if h == nil {
		return
	}
	if h.corpus != nil {
		h.corpus.Close()
	}
	if h.authPool != nil {
		h.authPool.Close()
	}
}

func NewVietnamQueryEmbedder(base *config.Config) (embed.Embedder, error) {
	if base == nil {
		return nil, fmt.Errorf("base config is required")
	}
	return NewQueryEmbedder(CountryConfig(base, "vn"))
}

func CountryConfig(base *config.Config, code string) *config.Config {
	cfg := *base
	cfg.Jurisdiction = code
	cfg.Database = base.Database
	cfg.Database.DBName = jurisdiction.For(code).DBName
	if name := strings.TrimSpace(os.Getenv("BANHMI_DATABASE_NAME_" + strings.ToUpper(code))); name != "" {
		cfg.Database.DBName = name
	}
	cfg.Retrieve = base.Retrieve
	if multiplier := jurisdiction.For(code).HNSWCandidateMultiplier; multiplier != 0 {
		cfg.Retrieve.HNSWCandidateMultiplier = multiplier
	}
	return &cfg
}

func envBool(key string) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(key)))
	return err == nil && value
}
