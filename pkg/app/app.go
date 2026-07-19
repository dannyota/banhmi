// Package app is banhmi's composition root. It builds a go.uber.org/dig container
// that provides the process-wide singletons (config, logger, database pool) and
// the constructors for the stores, sources, and pipeline activity set. Each
// command builds the container with New and Invokes what it needs, so dependency
// wiring lives here — not in the commands and not in pipeline logic.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/dig"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/base/db"
	"danny.vn/banhmi/pkg/base/jurisdiction"
	"danny.vn/banhmi/pkg/fetch"
	"danny.vn/banhmi/pkg/ingest"
	"danny.vn/banhmi/pkg/ingest/agclom"
	"danny.vn/banhmi/pkg/ingest/bi"
	"danny.vn/banhmi/pkg/ingest/bnm"
	"danny.vn/banhmi/pkg/ingest/bot"
	"danny.vn/banhmi/pkg/ingest/bpk"
	"danny.vn/banhmi/pkg/ingest/cdcgov"
	"danny.vn/banhmi/pkg/ingest/congbao"
	"danny.vn/banhmi/pkg/ingest/csa"
	"danny.vn/banhmi/pkg/ingest/etda"
	"danny.vn/banhmi/pkg/ingest/mas"
	nbcpkg "danny.vn/banhmi/pkg/ingest/nbc"
	"danny.vn/banhmi/pkg/ingest/ocs"
	"danny.vn/banhmi/pkg/ingest/odc"
	"danny.vn/banhmi/pkg/ingest/ojk"
	"danny.vn/banhmi/pkg/ingest/ojkweb"
	"danny.vn/banhmi/pkg/ingest/pdpc"
	"danny.vn/banhmi/pkg/ingest/sbvhanoi"
	"danny.vn/banhmi/pkg/ingest/sc"
	"danny.vn/banhmi/pkg/ingest/sec"
	"danny.vn/banhmi/pkg/ingest/serc"
	"danny.vn/banhmi/pkg/ingest/sso"
	"danny.vn/banhmi/pkg/ingest/vanban"
	"danny.vn/banhmi/pkg/ingest/vbpl"
	"danny.vn/banhmi/pkg/pipeline"
	"danny.vn/banhmi/pkg/rag/embed"
	"danny.vn/banhmi/pkg/rag/embed/onnxembed"
	"danny.vn/banhmi/pkg/rag/embed/ovembed"
	"danny.vn/banhmi/pkg/rag/retrieve"
	"danny.vn/banhmi/pkg/scope"
	dbbronze "danny.vn/banhmi/pkg/store/bronze"
	dbconfig "danny.vn/banhmi/pkg/store/config"
	dbgold "danny.vn/banhmi/pkg/store/gold"
	dbingest "danny.vn/banhmi/pkg/store/ingest"
	dbsilver "danny.vn/banhmi/pkg/store/silver"
)

// App is a built dependency container plus the resources to release on shutdown.
type App struct {
	Container *dig.Container
	closers   []func()
}

// Option configures App construction.
type Option func(*options)

type options struct {
	embedder embed.Embedder
}

// WithoutTemporal is a no-op kept for backward compatibility with callers that
// still pass it. Temporal has been removed from the codebase.
func WithoutTemporal() Option { return func(*options) {} }

// WithEmbedder supplies an already-built query embedder instead of letting the
// container build its own. cmd/server uses it to serve every jurisdiction from
// ONE process with ONE model in memory: the Qwen3 ONNX weights are ~2 GB
// resident and ORT does not share them across processes, so a container per
// jurisdiction costs 2 GB each — which is what forced a t4g.large. Sharing one
// embedder makes a new country cost a pool and a router entry, not 2 GB.
//
// The embedder must be safe for concurrent use (ORT sessions are).
func WithEmbedder(e embed.Embedder) Option {
	return func(o *options) { o.embedder = e }
}

// NewQueryEmbedder builds the query-time embedder for cfg, for callers that need
// to construct it once and share it across jurisdictions (see WithEmbedder).
func NewQueryEmbedder(cfg *config.Config) (embed.Embedder, error) { return buildEmbedder(cfg) }

// New builds the container for cfg. It eagerly constructs the database pool and
// registers everything else as constructors that dig resolves on demand. Call
// Close to release resources.
func New(ctx context.Context, cfg *config.Config, log *slog.Logger, opts ...Option) (*App, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	a := &App{Container: dig.New()}

	pool, err := db.NewPool(ctx, cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("database pool: %w", err)
	}
	a.closers = append(a.closers, pool.Close)

	if err := a.provide(ctx, cfg, log, pool, o.embedder); err != nil {
		a.Close()
		return nil, fmt.Errorf("provide dependencies: %w", err)
	}
	return a, nil
}

// Close releases the eagerly-built resources in reverse order of construction.
func (a *App) Close() {
	for i := len(a.closers) - 1; i >= 0; i-- {
		a.closers[i]()
	}
}

// provide registers the value singletons and the constructors. The store
// providers take *pgxpool.Pool (which satisfies each generated DBTX interface) so
// dig can resolve them without a bare-interface provider.
func (a *App) provide(ctx context.Context, cfg *config.Config, log *slog.Logger, pool *pgxpool.Pool, shared embed.Embedder) error {
	c := a.Container
	// The query embedder: a caller-supplied one (shared across jurisdictions by
	// cmd/server — see WithEmbedder) or one built for this container alone.
	queryEmbedder := func(cfg *config.Config) (embed.Embedder, error) {
		if shared != nil {
			return shared, nil
		}
		return buildEmbedder(cfg)
	}
	return errors.Join(
		c.Provide(queryEmbedder),
		c.Provide(func() context.Context { return ctx }),
		c.Provide(func() *config.Config { return cfg }),
		c.Provide(func() *slog.Logger { return log }),
		c.Provide(func() *pgxpool.Pool { return pool }),
		c.Provide(func(p *pgxpool.Pool) *dbingest.Queries { return dbingest.New(p) }),
		c.Provide(func(p *pgxpool.Pool) *dbbronze.Queries { return dbbronze.New(p) }),
		c.Provide(func(p *pgxpool.Pool) *dbsilver.Queries { return dbsilver.New(p) }),
		c.Provide(func(p *pgxpool.Pool) *dbgold.Queries { return dbgold.New(p) }),
		c.Provide(func(p *pgxpool.Pool) *dbconfig.Queries { return dbconfig.New(p) }),
		c.Provide(buildSources),
		c.Provide(newActivities),
		c.Provide(newRetriever),
	)
}

// sourceBuilder is a constructor that builds a jurisdiction's source crawlers.
type sourceBuilder func(context.Context, *slog.Logger, *dbconfig.Queries) (map[string]ingest.Source, error)

// sourceBuilders maps every registered jurisdiction code to its source
// constructor. TestSourceBuildersCoverRegistry guards against drift.
var sourceBuilders = map[string]sourceBuilder{
	"vn": buildVNSources,
	"my": func(_ context.Context, log *slog.Logger, _ *dbconfig.Queries) (map[string]ingest.Source, error) {
		return buildMYSources(log)
	},
	"id": buildIDSources,
	"sg": func(_ context.Context, log *slog.Logger, _ *dbconfig.Queries) (map[string]ingest.Source, error) {
		return buildSGSources(log)
	},
	"th": func(_ context.Context, log *slog.Logger, _ *dbconfig.Queries) (map[string]ingest.Source, error) {
		return buildTHSources(log)
	},
	"kh": func(_ context.Context, log *slog.Logger, _ *dbconfig.Queries) (map[string]ingest.Source, error) {
		return buildKHSources(log)
	},
}

// resolveJurisdiction validates the configured code against the registry and
// returns the descriptor. Unknown codes fail fast — the worker must never serve
// the wrong country's sources.
func resolveJurisdiction(cfg *config.Config) (jurisdiction.Descriptor, error) {
	d, ok := jurisdiction.Lookup(cfg.Jurisdiction)
	if !ok {
		return jurisdiction.Descriptor{}, fmt.Errorf("unknown jurisdiction %q", cfg.Jurisdiction)
	}
	return d, nil
}

// cfgWithJurisdiction returns a minimal config with the given jurisdiction set,
// for use in tests.
func cfgWithJurisdiction(code string) *config.Config {
	c := config.Default()
	c.Jurisdiction = code
	return c
}

// buildSources selects the source crawlers for the deployment's jurisdiction
// (config.Jurisdiction, default "vn"). Each jurisdiction is a disjoint source set
// off the one shared codebase. The default and any absent value resolve to "vn",
// so existing VN deployments are unchanged.
func buildSources(ctx context.Context, log *slog.Logger, cfgQ *dbconfig.Queries, cfg *config.Config) (map[string]ingest.Source, error) {
	d, err := resolveJurisdiction(cfg)
	if err != nil {
		return nil, err
	}
	build := sourceBuilders[d.Code]
	if build == nil {
		return nil, fmt.Errorf("no source builder for jurisdiction %q", d.Code)
	}
	return build(ctx, log, cfgQ)
}

// buildMYSources assembles Malaysia's source crawlers. agclom (the AGC Laws of
// Malaysia database) is the law-DB backbone; bnm and sc are added in later steps.
// MY scope is title-based (config scope terms), so no per-source agency ids are
// loaded here.
func buildMYSources(log *slog.Logger) (map[string]ingest.Source, error) {
	return map[string]ingest.Source{
		agclom.SourceID: agclom.New(nil, log),
		bnm.SourceID:    bnm.New(nil, log),
		sc.SourceID:     sc.New(nil, log),
	}, nil
}

// buildIDSources assembles Indonesia's source crawlers: bpk (JDIH BPK RI, the
// national legal database), bi (Bank Indonesia regulations API), ojk (JDIH OJK
// API), and ojkweb (ojk.go.id SharePoint scraper). jdih.ojk.go.id geo-drops
// non-Indonesian IPs, so the ojk source needs BANHMI_OJK_PROXY_URL (HTTP/SOCKS5
// forward proxy). www.ojk.go.id serves listings, details, and PDFs directly
// (verified 2026-07-16 from a Malaysian egress), so ojkweb is always enabled
// and only routes through the proxy when the env var is set.
func buildIDSources(_ context.Context, log *slog.Logger, _ *dbconfig.Queries) (map[string]ingest.Source, error) {
	proxyURL := os.Getenv("BANHMI_OJK_PROXY_URL")
	var biClient *fetch.Client
	if biProxy := os.Getenv("BANHMI_BI_PROXY_URL"); biProxy != "" {
		u, err := url.Parse(biProxy)
		if err != nil {
			return nil, fmt.Errorf("parse BANHMI_BI_PROXY_URL: %w", err)
		}
		biClient = fetch.New(nil, log)
		biClient.HTTP = &http.Client{
			Transport: fetch.ProxiedChromeTransport(u),
			Timeout:   90 * time.Second,
		}
		log.Info("bi source via proxy", "proxy", biProxy)
	}
	sources := map[string]ingest.Source{
		bpk.SourceID:    bpk.New(nil, log),
		bi.SourceID:     bi.New(biClient, log),
		ojkweb.SourceID: ojkweb.New(&ojkweb.Config{ProxyURL: proxyURL}, log),
	}
	if proxyURL != "" {
		sources[ojk.SourceID] = ojk.New(&ojk.Config{ProxyURL: proxyURL}, nil, log)
		log.Info("ojk (jdih) source enabled via proxy", "proxy", proxyURL)
	} else {
		log.Info("ojk (jdih) source disabled (no BANHMI_OJK_PROXY_URL); ojkweb runs direct")
	}
	return sources, nil
}

// buildSGSources assembles Singapore's source crawlers: sso (Singapore Statutes
// Online, consolidated Acts), mas (MAS Notices + Guidelines via Solr API),
// pdpc (PDPC advisory guidelines via JSON API), csa (CSA CII documents via sitemap).
func buildSGSources(log *slog.Logger) (map[string]ingest.Source, error) {
	return map[string]ingest.Source{
		sso.SourceID:  sso.New(nil, log),
		mas.SourceID:  mas.New(nil, log),
		pdpc.SourceID: pdpc.New(nil, log),
		csa.SourceID:  csa.New(nil, log),
	}, nil
}

// buildTHSources assembles Thailand's source crawlers: ocs (Office of the Council
// of State, consolidated Acts via JSON API), bot (Bank of Thailand FIPCS notifications
// via WebForms), etda (ETDA e-transaction regulations via HTML scrape), and sec
// (Securities and Exchange Commission NRS portal). SEC PDF downloads from
// publish.sec.or.th are geo-blocked (F5 BIG-IP); discovery on capital.sec.or.th
// works worldwide. Set BANHMI_SEC_PROXY_URL for PDF downloads.
func buildTHSources(log *slog.Logger) (map[string]ingest.Source, error) {
	proxyURL := os.Getenv("BANHMI_SEC_PROXY_URL")
	secSrc := sec.New(&sec.Config{ProxyURL: proxyURL}, nil, log)
	if proxyURL != "" {
		log.Info("sec PDF downloads via proxy", "proxy", proxyURL)
	} else {
		log.Info("sec PDF downloads disabled (no BANHMI_SEC_PROXY_URL); discovery still works")
	}
	return map[string]ingest.Source{
		ocs.SourceID:  ocs.New(nil, log),
		bot.SourceID:  bot.New(nil, log),
		etda.SourceID: etda.New(nil, log),
		sec.SourceID:  secSrc,
	}, nil
}

// buildKHSources assembles Cambodia's source crawlers: nbc (National Bank of
// Cambodia, requires SOCKS5 proxy for geo-blocked CloudFront), serc (Securities
// and Exchange Regulator), cdcgov (Council for Development, foundational laws),
// odc (Open Development Cambodia CKAN API).
func buildKHSources(log *slog.Logger) (map[string]ingest.Source, error) {
	proxyURL := os.Getenv("BANHMI_NBC_PROXY_URL")
	nbcSrc := nbcpkg.New(&nbcpkg.Config{ProxyURL: proxyURL}, nil, log)
	if proxyURL != "" {
		log.Info("nbc via proxy", "proxy", proxyURL)
	} else {
		log.Info("nbc proxy not set (BANHMI_NBC_PROXY_URL); NBC will fail on geo-blocked IPs")
	}
	return map[string]ingest.Source{
		cdcgov.SourceID: cdcgov.New(nil, log),
		serc.SourceID:   serc.New(nil, log),
		odc.SourceID:    odc.New(nil, log),
		nbcpkg.SourceID: nbcSrc,
	}, nil
}

// buildVNSources assembles Vietnam's source crawlers. A nil HTTP client lets each
// source apply its own (e.g. congbao's AIA-completing client). vbpl's agency ids
// come from config.issuer_code (not hardcoded): the is_sbv set drives the keyword-
// less State Bank sweep, the remaining in-scope set is the target of the keyword
// searches.
func buildVNSources(ctx context.Context, log *slog.Logger, cfgQ *dbconfig.Queries) (map[string]ingest.Source, error) {
	codes, err := cfgQ.ListIssuerCodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("load vbpl issuer codes: %w", err)
	}
	var sbv, nonSbv []string
	for _, c := range codes {
		if c.Source != vbpl.SourceID || !c.InScope {
			continue
		}
		if c.IsSbv {
			sbv = append(sbv, c.Code)
		} else {
			nonSbv = append(nonSbv, c.Code)
		}
	}
	if len(sbv) == 0 {
		log.Warn("no SBV agency ids in config for vbpl; the agency sweep will be unfiltered (run cmd/seed)")
	}
	if len(nonSbv) == 0 {
		log.Warn("no non-SBV agency ids in config for vbpl; cross-cutting keyword searches will be skipped (run cmd/seed)")
	}
	relTypes, err := cfgQ.ListRelationTypes(ctx)
	if err != nil {
		return nil, fmt.Errorf("load vbpl relation types: %w", err)
	}
	vbplRelTypes := make(map[int]string)
	for _, rt := range relTypes {
		if rt.Source != vbpl.SourceID {
			continue
		}
		if code, err := strconv.Atoi(rt.Code); err == nil {
			vbplRelTypes[code] = rt.Label
		}
	}
	// vbpl.vn geo-drops non-Vietnamese IPs. When running from outside VN, set
	// BANHMI_VBPL_PROXY_URL to an HTTP forward proxy with a VN egress (same
	// pattern as BANHMI_OJK_PROXY_URL / BANHMI_SEC_PROXY_URL).
	var vbplClient *http.Client
	if proxyURL := os.Getenv("BANHMI_VBPL_PROXY_URL"); proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("parse BANHMI_VBPL_PROXY_URL: %w", err)
		}
		vbplClient = &http.Client{
			Timeout:   60 * time.Second,
			Transport: &http.Transport{Proxy: http.ProxyURL(u)},
		}
		log.Info("vbpl source routed via proxy", "proxy", u.Redacted())
	}
	return map[string]ingest.Source{
		congbao.SourceID:  congbao.New(nil, log),
		vbpl.SourceID:     vbpl.New(vbplClient, log, sbv, nonSbv, vbplRelTypes),
		vanban.SourceID:   vanban.New(nil, log),
		sbvhanoi.SourceID: sbvhanoi.New(nil, log),
	}, nil
}

// newActivities adapts the dig-injected dependencies to pipeline.NewActivities,
// taking the raw-file storage directory and optional embedder from config so dig
// need not resolve bare strings. OCR is not wired inline here — it runs as a
// separate batch (OcrAll); see cmd/pipeline -ocr-all.
func newActivities(
	ctx context.Context,
	log *slog.Logger,
	pool *pgxpool.Pool,
	ledger *dbingest.Queries,
	bronze *dbbronze.Queries,
	silver *dbsilver.Queries,
	gold *dbgold.Queries,
	configQ *dbconfig.Queries,
	sources map[string]ingest.Source,
	cfg *config.Config,
) (*pipeline.Activities, error) {
	// Index embeds inline only for the local engine. With the Kaggle engine, bulk
	// embedding runs as a separate batch (cmd/embed-backfill) on Kaggle GPUs, so
	// Index writes chunks only — a nil embedder is skipped (best-effort), and the
	// vectors are filled by the batch pass. Query-time retrieval is unaffected: it
	// always uses the live local embedder (see newRetriever).
	indexEmbedder, err := buildEmbedder(cfg)
	if err != nil {
		return nil, fmt.Errorf("build index embedder: %w", err)
	}
	if cfg.EmbedEngine() == "kaggle" {
		indexEmbedder = nil
	}
	files, err := pipeline.BuildFileStore(ctx, cfg.Storage.S3DataBucket, cfg.Storage.Dir, log)
	if err != nil {
		return nil, fmt.Errorf("build file store: %w", err)
	}
	return pipeline.NewActivities(log, pool, ledger, bronze, silver, gold, configQ, sources, cfg.Storage.Dir, files, indexEmbedder, cfg.KaggleToken, jurisdiction.For(cfg.Jurisdiction)), nil
}

// buildEmbedder selects the query-time embedder. BANHMI_EMBED_QUERY=openvino is
// the standard path (in-process OpenVINO BGE-M3, `-tags openvino` — Cloud Run and
// local dev); "onnx" is the in-process ONNX alternative (`-tags onnx`). Unset
// falls back to a generic HTTP embeddings endpoint (BANHMI_EMBED_ENDPOINT) for
// self-hosters running their own embedder service.
func buildEmbedder(cfg *config.Config) (embed.Embedder, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BANHMI_EMBED_QUERY"))) {
	case "openvino", "ov":
		return ovembed.New(ovembed.Config{
			ModelDir:      envOr("BANHMI_OV_MODEL_DIR", "/models/bge-m3"),
			TokenizerPath: envOr("BANHMI_OV_TOKENIZER", "/models/bge-m3/tokenizer.json"),
			Dims:          config.EmbedDims,
			Model:         config.EmbedModel,
			Device:        envOr("BANHMI_OV_DEVICE", "AUTO"),
		})
	case "onnx":
		concurrency, _ := strconv.Atoi(os.Getenv("BANHMI_EMBED_CONCURRENCY"))
		return onnxembed.New(onnxembed.Config{
			ModelPath:     envOr("BANHMI_ONNX_MODEL", "/models/qwen3-embedding/model_fp16.onnx"),
			TokenizerPath: envOr("BANHMI_ONNX_TOKENIZER", "/models/qwen3-embedding/tokenizer.json"),
			LibPath:       os.Getenv("BANHMI_ONNX_LIB"),
			Dims:          config.EmbedDims,
			Model:         config.EmbedModel,
			CUDA:          os.Getenv("BANHMI_ONNX_CUDA") == "1",
			Concurrency:   concurrency,
		})
	}
	return embed.New(cfg.EmbedEndpoint(), config.EmbedModel, config.EmbedDims, os.Getenv("BANHMI_EMBED_TOKEN")), nil
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// newRetriever builds the retrieval core. The embedder is required, so the default
// query path is BGE-M3 vector search; the eval harness can still force bm25/hybrid.
func newRetriever(
	ctx context.Context,
	pool *pgxpool.Pool,
	cfgQ *dbconfig.Queries,
	cfg *config.Config,
	emb embed.Embedder,
	log *slog.Logger,
) (retrieve.Retriever, error) {
	gate, err := loadRetrieveGate(ctx, cfgQ, cfg.Jurisdiction)
	if err != nil {
		return nil, err
	}
	// Disable the current-law pre-filter when the corpus has chunks but no
	// in-force/partial validity yet (e.g. Malaysia, whose validity is still all
	// 'unknown'): filtering would hide every result. Data-driven, not hardcoded —
	// it auto-re-enables once real validity lands. VN has in-force rows, so the
	// filter stays on and its behavior is unchanged.
	disable, err := validityFilterUnusable(ctx, pool)
	if err != nil {
		return nil, err
	}
	gate.DisableValidityFilter = disable
	if disable {
		log.Warn("retrieve: validity pre-filter disabled — corpus has chunks but no in-force/partial validity; serving pure relevance until validity is derived")
	}
	// Load the diacritic-restore dictionary for the dense arm (VN only today).
	drRows, err := cfgQ.ListDiacriticRestore(ctx, cfg.Jurisdiction)
	if err != nil {
		return nil, fmt.Errorf("load diacritic_restore: %w", err)
	}
	var drDict map[string]string
	if len(drRows) > 0 {
		drDict = make(map[string]string, len(drRows))
		for _, row := range drRows {
			drDict[row.FoldedToken] = row.RestoredToken
		}
		log.Info("retrieve: loaded diacritic-restore dictionary", "entries", len(drDict), "jurisdiction", cfg.Jurisdiction)
	}
	// Load the abbreviation-expansion dictionary for the dense arm.
	abbrRows, err := cfgQ.ListAbbreviationExpand(ctx, cfg.Jurisdiction)
	if err != nil {
		return nil, fmt.Errorf("load abbreviation_expand: %w", err)
	}
	var abbrDict map[string]string
	if len(abbrRows) > 0 {
		abbrDict = make(map[string]string, len(abbrRows))
		for _, row := range abbrRows {
			abbrDict[strings.ToLower(row.Abbreviation)] = row.Expansion
		}
		log.Info("retrieve: loaded abbreviation-expand dictionary", "entries", len(abbrDict), "jurisdiction", cfg.Jurisdiction)
	}
	return retrieve.New(pool, emb, cfg.Retrieve, log,
		retrieve.WithGateConfig(gate),
		retrieve.WithJurisdiction(jurisdiction.For(cfg.Jurisdiction)),
		retrieve.WithDiacriticDict(drDict),
		retrieve.WithAbbreviationDict(abbrDict),
	), nil
}

// validityFilterUnusable reports whether the corpus has indexed chunks but not a
// single current-law (in_force/partial) validity row — the case where applying the
// current-law pre-filter would hide the entire corpus.
func validityFilterUnusable(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	const q = `
SELECT (SELECT count(*) FROM gold.chunk) > 0
   AND NOT EXISTS (
       SELECT 1 FROM (
           SELECT DISTINCT ON (document_id) status_class
           FROM silver.validity_period
           WHERE superseded_at IS NULL AND section_id IS NULL
           ORDER BY document_id, observed_at DESC, id DESC
       ) cur
       WHERE cur.status_class IN ('in_force', 'partial')
   )`
	var unusable bool
	if err := pool.QueryRow(ctx, q).Scan(&unusable); err != nil {
		return false, fmt.Errorf("probe validity coverage: %w", err)
	}
	return unusable, nil
}

func loadRetrieveGate(ctx context.Context, cfgQ *dbconfig.Queries, jurisdiction string) (retrieve.GateConfig, error) {
	scopeRows, err := cfgQ.ListScopeTerms(ctx, jurisdiction)
	if err != nil {
		return retrieve.GateConfig{}, fmt.Errorf("load retrieval scope terms: %w", err)
	}
	terms := make([]scope.Term, 0, len(scopeRows))
	for _, row := range scopeRows {
		terms = append(terms, scope.Term{Text: row.Term, Class: row.TermClass})
	}

	settings, err := cfgQ.ListSettings(ctx)
	if err != nil {
		return retrieve.GateConfig{}, fmt.Errorf("load retrieval settings: %w", err)
	}
	return retrieve.GateConfig{
		ScopeTerms: terms,
		MinScore:   floatSetting(settings, "retrieve.abstain.min_score"),
	}, nil
}

func floatSetting(rows []dbconfig.ListSettingsRow, key string) float64 {
	for _, row := range rows {
		if strings.TrimSpace(row.Key) != key {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(row.Value), 64)
		if err != nil || v < 0 {
			return 0
		}
		return v
	}
	return 0
}
