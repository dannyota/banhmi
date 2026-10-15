package app

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
	"danny.vn/banhmi/pkg/base/db"
	"danny.vn/banhmi/pkg/base/jurisdiction"
	"danny.vn/banhmi/pkg/identity"
	"danny.vn/banhmi/pkg/mcp"
	"danny.vn/banhmi/pkg/oauthserver"
	"danny.vn/banhmi/pkg/rag/embed"
	"danny.vn/banhmi/pkg/rag/retrieve"
	"danny.vn/banhmi/pkg/web"
)

const oauthClientsFileEnv = "BANHMI_OAUTH_CLIENTS_FILE"

// HostedConfig is the complete local configuration for the authenticated HTTP
// service. LoadHostedConfig validates it before database or provider access.
type HostedConfig struct {
	Identity identity.Config
	OAuth    oauthserver.Config
	Clients  *oauthserver.ClientRegistry
}

// LoadHostedConfig loads authentication, OAuth, and preregistered client policy.
func LoadHostedConfig() (HostedConfig, error) {
	identityConfig, err := identity.LoadConfig()
	if err != nil {
		return HostedConfig{}, fmt.Errorf("load identity config: %w", err)
	}
	oauthConfig, err := oauthserver.LoadConfig(identityConfig.PublicOrigin)
	if err != nil {
		return HostedConfig{}, fmt.Errorf("load OAuth config: %w", err)
	}
	clientsFile := strings.TrimSpace(os.Getenv(oauthClientsFileEnv))
	if clientsFile == "" {
		return HostedConfig{}, fmt.Errorf("%s is required", oauthClientsFileEnv)
	}
	data, err := os.ReadFile(clientsFile)
	if err != nil {
		return HostedConfig{}, fmt.Errorf("read %s: %w", oauthClientsFileEnv, err)
	}
	clients, err := oauthserver.ParseClientRegistry(data, oauthConfig.Resource)
	if err != nil {
		return HostedConfig{}, fmt.Errorf("parse %s: %w", oauthClientsFileEnv, err)
	}
	return HostedConfig{Identity: identityConfig, OAuth: oauthConfig, Clients: clients}, nil
}

// Hosted owns the single-origin authenticated service and its database pools.
type Hosted struct {
	Identity *identity.Manager
	OAuth    *oauthserver.Server
	Web      *web.Server
	Evidence *mcp.Server

	corpusApp *App
	authPool  *pgxpool.Pool
}

// NewHosted composes one Vietnam evidence source with shared website and MCP
// adapters. It never runs migrations or contacts Google during construction.
func NewHosted(ctx context.Context, base *config.Config, hosted HostedConfig, log *slog.Logger, version string, queryEmbedder embed.Embedder) (*Hosted, error) {
	if err := db.ValidateConnectionURI("BANHMI_AUTH_DATABASE_URL", hosted.Identity.AuthDatabaseURL); err != nil {
		return nil, err
	}
	if base == nil {
		return nil, fmt.Errorf("base config is required")
	}
	if hosted.Clients == nil {
		return nil, fmt.Errorf("OAuth client registry is required")
	}
	if queryEmbedder == nil {
		return nil, fmt.Errorf("query embedder is required")
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

	corpusConfig := vietnamConfig(base)
	corpusApp, err := New(ctx, corpusConfig, log, WithEmbedder(queryEmbedder))
	if err != nil {
		return nil, fmt.Errorf("build Vietnam evidence app: %w", err)
	}
	closeCorpus := true
	defer func() {
		if closeCorpus {
			corpusApp.Close()
		}
	}()

	var evidence *mcp.Server
	err = corpusApp.Container.Invoke(func(r retrieve.Retriever, pool *pgxpool.Pool) error {
		opts := append(MCPFileLinkOptions(), mcp.WithPool(pool), mcp.WithJurisdiction("vn"), mcp.WithVersion(version))
		trustProxy, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv("BANHMI_TRUST_PROXY")))
		if trustProxy {
			opts = append(opts, mcp.WithBehindProxy())
		}
		evidence = mcp.New(r, log, opts...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("build Vietnam evidence server: %w", err)
	}
	website, err := web.New(evidence, manager, hosted.Identity.PublicOrigin)
	if err != nil {
		return nil, fmt.Errorf("build website: %w", err)
	}

	closeAuth = false
	closeCorpus = false
	return &Hosted{
		Identity:  manager,
		OAuth:     oauth,
		Web:       website,
		Evidence:  evidence,
		corpusApp: corpusApp,
		authPool:  authPool,
	}, nil
}

// NewVietnamQueryEmbedder builds the embedder selected by the Vietnam serving config.
func NewVietnamQueryEmbedder(base *config.Config) (embed.Embedder, error) {
	if base == nil {
		return nil, fmt.Errorf("base config is required")
	}
	return NewQueryEmbedder(vietnamConfig(base))
}

// Register installs website, identity, and OAuth routes. The caller mounts the
// protected MCP handler after applying transport-specific origin protection.
func (h *Hosted) Register(mux *http.ServeMux) {
	h.Identity.Register(mux)
	h.OAuth.Register(mux)
	h.Web.Register(mux)
}

// MCPHandler returns the bearer-protected Streamable HTTP evidence endpoint.
func (h *Hosted) MCPHandler() http.Handler {
	return h.OAuth.Protect(h.Evidence.HTTPHandler())
}

// Close releases the corpus and authentication pools.
func (h *Hosted) Close() {
	if h.corpusApp != nil {
		h.corpusApp.Close()
	}
	if h.authPool != nil {
		h.authPool.Close()
	}
}

func vietnamConfig(base *config.Config) *config.Config {
	cfg := *base
	cfg.Jurisdiction = "vn"
	cfg.Database = base.Database
	cfg.Database.DBName = jurisdiction.For("vn").DBName
	if name := strings.TrimSpace(os.Getenv("BANHMI_DATABASE_NAME_VN")); name != "" {
		cfg.Database.DBName = name
	}
	if multiplier := jurisdiction.For("vn").HNSWCandidateMultiplier; multiplier != 0 {
		cfg.Retrieve.HNSWCandidateMultiplier = multiplier
	}
	return &cfg
}
