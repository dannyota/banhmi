package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"danny.vn/banhmi/pkg/app"
	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/base/jurisdiction"
	"danny.vn/banhmi/pkg/mcp"
	"danny.vn/banhmi/pkg/rag/embed"
	"danny.vn/banhmi/pkg/rag/retrieve"
)

// jurisdictionHeader is set per CloudFront distribution (one per domain) and is
// the authoritative signal for which corpus a request wants. It is preferred over
// Host because CloudFront only forwards the viewer's Host when explicitly
// configured, while a custom origin header is unconditional — the same mechanism
// already carries X-Origin-Verify.
const jurisdictionHeader = "X-Banhmi-Jurisdiction"

// site is one jurisdiction served by this process: its own corpus, MCP surface
// (brief, guide, tool descriptions in that country's legal language) and landing
// page. Every site shares the ONE query embedder held by the process.
type site struct {
	code    string
	domain  string
	handler http.Handler
}

// buildSites constructs one site per jurisdiction in BANHMI_JURISDICTIONS
// (comma-separated; defaults to the single configured jurisdiction, which keeps
// local dev and cmd/mcp unchanged).
//
// All sites share ONE embedder. The Qwen3 ONNX weights are ~2 GB resident and ORT
// does not share them across processes, so the previous container-per-jurisdiction
// layout cost 2 GB each and forced an 8 GB box for three countries. One process
// with one model makes a new country cost a pool and a router entry.
func buildSites(ctx context.Context, base *config.Config, log *slog.Logger) ([]*site, func(), error) {
	codes := jurisdictionCodes(base.Jurisdiction)

	// Build the embedder once, from the base config, and hand it to every site.
	emb, err := app.NewQueryEmbedder(base)
	if err != nil {
		return nil, nil, fmt.Errorf("build shared query embedder: %w", err)
	}

	// Parity guard: probe the embedder before accepting traffic. The remote
	// embedder service may still be loading the model (~40 s cold start), so
	// retry with backoff for up to ~3 minutes.
	if err := probeEmbedder(ctx, emb, log); err != nil {
		return nil, nil, fmt.Errorf("embedder parity probe: %w", err)
	}

	var (
		sites  []*site
		closes []func()
	)
	closeAll := func() {
		for i := len(closes) - 1; i >= 0; i-- {
			closes[i]()
		}
	}

	for _, code := range codes {
		s, closeSite, err := buildSite(ctx, base, code, emb, log)
		if err != nil {
			// One unreachable corpus must not take down the others: a broken rendang
			// cannot be allowed to black out live VN and MY. Serve what is healthy and
			// let /healthz and the logs report the gap.
			log.Error("site unavailable — serving without it", "jurisdiction", code, "err", err)
			continue
		}
		closes = append(closes, closeSite)
		sites = append(sites, s)
	}
	if len(sites) == 0 {
		closeAll()
		return nil, nil, fmt.Errorf("no jurisdiction could be served (wanted %v)", codes)
	}
	return sites, closeAll, nil
}

// buildSite wires one jurisdiction: its own DB pool, retriever, MCP server, and
// landing page, on its own mux.
func buildSite(ctx context.Context, base *config.Config, code string, emb embed.Embedder, log *slog.Logger) (*site, func(), error) {
	cfg := siteConfig(base, code)
	jlog := log.With("jurisdiction", code)

	application, err := app.New(ctx, cfg, jlog, app.WithEmbedder(emb))
	if err != nil {
		return nil, nil, err
	}

	var handler http.Handler
	err = application.Container.Invoke(func(r retrieve.Retriever, pool *pgxpool.Pool) error {
		opts := append(app.MCPFileLinkOptions(), mcp.WithPool(pool), mcp.WithJurisdiction(code), mcp.WithVersion(version))
		if envBool("BANHMI_TRUST_PROXY", false) {
			opts = append(opts, mcp.WithBehindProxy())
		}
		mux := http.NewServeMux()
		mux.Handle("/mcp", crossOriginProtected(mcp.New(r, jlog, opts...).HTTPHandler(), jlog))
		if err := mountLanding(mux, code, version, jlog); err != nil {
			return err
		}
		handler = mux
		return nil
	})
	if err != nil {
		application.Close()
		return nil, nil, err
	}
	return &site{code: code, domain: landingFor(code, version).Domain, handler: handler}, application.Close, nil
}

// siteConfig clones base for one jurisdiction: same server/retrieve/embed
// settings, but that country's own database. Never share a database between
// jurisdictions — one corpus per country is the product's core invariant.
func siteConfig(base *config.Config, code string) *config.Config {
	cfg := *base
	cfg.Jurisdiction = code
	cfg.Database = base.Database
	cfg.Database.DBName = jurisdiction.For(code).DBName
	// An explicit per-jurisdiction DB override wins (used by local dev, where the
	// three corpora may live in differently named databases).
	if v := os.Getenv("BANHMI_DATABASE_NAME_" + strings.ToUpper(code)); v != "" {
		cfg.Database.DBName = v
	}

	// Retrieval tuning that differs per corpus. One process serves every country,
	// so a single env var cannot express three values — the descriptor carries the
	// per-jurisdiction default (VN: exact scan, no ANN). A per-jurisdiction env
	// var still overrides, for operator tuning without a redeploy.
	cfg.Retrieve = base.Retrieve
	if m := jurisdiction.For(code).HNSWCandidateMultiplier; m != 0 {
		cfg.Retrieve.HNSWCandidateMultiplier = m
	}
	if v := os.Getenv("BANHMI_HNSW_CANDIDATE_MULTIPLIER_" + strings.ToUpper(code)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Retrieve.HNSWCandidateMultiplier = n
		}
	}
	return &cfg
}

// jurisdictionCodes reads BANHMI_JURISDICTIONS ("vn,my,id"), falling back to the
// single configured jurisdiction so single-country deployments and local dev keep
// working untouched.
func jurisdictionCodes(fallback string) []string {
	raw := strings.TrimSpace(os.Getenv("BANHMI_JURISDICTIONS"))
	if raw == "" {
		return []string{fallback}
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.Split(raw, ",") {
		code := strings.ToLower(strings.TrimSpace(p))
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	if len(out) == 0 {
		return []string{fallback}
	}
	return out
}

// router dispatches each request to the jurisdiction it asked for: the
// CloudFront-injected header first, then the Host (so a direct hit on
// laksa.danny.vn still works), then the default site. With one site configured
// this is a pass-through, so single-jurisdiction deployments are unaffected.
func router(sites []*site, log *slog.Logger) http.Handler {
	byCode := make(map[string]*site, len(sites))
	byDomain := make(map[string]*site, len(sites))
	for _, s := range sites {
		byCode[s.code] = s
		if s.domain != "" {
			byDomain[strings.ToLower(s.domain)] = s
		}
	}
	def := sites[0]

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code := strings.ToLower(strings.TrimSpace(r.Header.Get(jurisdictionHeader))); code != "" {
			if s, ok := byCode[code]; ok {
				s.handler.ServeHTTP(w, r)
				return
			}
			// An explicit but unknown/unserved jurisdiction is an error, not a
			// silent fallback to another country's corpus — answering a rendang
			// request from the VN corpus would be a wrong-law citation.
			log.Warn("unknown jurisdiction requested", "header", code, "host", r.Host)
			http.Error(w, "unknown jurisdiction: "+code, http.StatusNotFound)
			return
		}
		host := strings.ToLower(r.Host)
		if i := strings.IndexByte(host, ':'); i >= 0 {
			host = host[:i]
		}
		if s, ok := byDomain[host]; ok {
			s.handler.ServeHTTP(w, r)
			return
		}
		def.handler.ServeHTTP(w, r)
	})
}

// probeEmbedder verifies the embedder is operational and returns the expected
// dimensions before accepting traffic. Retries with backoff for up to ~3 minutes
// to allow a remote embedder service time to cold-start.
func probeEmbedder(ctx context.Context, emb embed.Embedder, log *slog.Logger) error {
	const (
		maxDuration = 3 * time.Minute
		initBackoff = 2 * time.Second
		maxBackoff  = 15 * time.Second
	)
	deadline := time.Now().Add(maxDuration)
	backoff := initBackoff

	for attempt := 1; ; attempt++ {
		vecs, err := emb.Embed(ctx, []string{"parity probe"})
		if err == nil {
			if len(vecs) != 1 {
				return fmt.Errorf("probe returned %d vectors, want 1", len(vecs))
			}
			if len(vecs[0]) != config.EmbedDims {
				return fmt.Errorf("probe returned dims=%d, want %d", len(vecs[0]), config.EmbedDims)
			}
			log.Info("embedder parity probe passed", "dims", len(vecs[0]), "attempts", attempt)
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("embedder not ready after %v (%d attempts): %w", maxDuration, attempt, err)
		}
		log.Warn("embedder probe failed, retrying", "attempt", attempt, "backoff", backoff, "err", err)

		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled during embedder probe: %w", ctx.Err())
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}
