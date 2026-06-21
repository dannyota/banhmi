// Package app is banhmi's composition root. It builds a go.uber.org/dig container
// that provides the process-wide singletons (config, logger, database pool,
// Temporal client) and the constructors for the stores, sources, and pipeline
// activity set. Each command builds the container with New and Invokes what it
// needs, so dependency wiring lives here — not in the commands and not in
// workflow/activity logic. (This is a deliberate divergence from hotpot's manual
// wiring; see CLAUDE.md.)
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"
	"go.uber.org/dig"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/base/db"
	"danny.vn/banhmi/pkg/base/temporalx"
	"danny.vn/banhmi/pkg/extract"
	"danny.vn/banhmi/pkg/ingest"
	"danny.vn/banhmi/pkg/ingest/agclom"
	"danny.vn/banhmi/pkg/ingest/congbao"
	"danny.vn/banhmi/pkg/ingest/sbvhanoi"
	"danny.vn/banhmi/pkg/ingest/sc"
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

type options struct{ skipTemporal bool }

// WithoutTemporal skips dialing Temporal. Use it for the query-only commands
// (server, mcp, eval) that need only the retriever + DB pool and may run where no
// Temporal exists (e.g. the Cloud Run MCP server). The Temporal client constructor
// is still registered but, being unused by those commands, is never built.
func WithoutTemporal() Option { return func(o *options) { o.skipTemporal = true } }

// New builds the container for cfg. It eagerly constructs the singletons that need
// the startup context or cleanup (the database pool and, unless WithoutTemporal is
// set, the Temporal client) and registers everything else as constructors that dig
// resolves on demand. Call Close to release resources.
func New(ctx context.Context, cfg *config.Config, log *slog.Logger, opts ...Option) (*App, error) {
	var o options
	for _, fn := range opts {
		fn(&o)
	}

	a := &App{Container: dig.New()}

	pool, err := db.NewPool(ctx, cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("database pool: %w", err)
	}
	a.closers = append(a.closers, pool.Close)

	var tc client.Client
	if !o.skipTemporal {
		tc, err = temporalx.Dial(cfg.Temporal, log)
		if err != nil {
			a.Close()
			return nil, err
		}
		a.closers = append(a.closers, tc.Close)
	}

	if err := a.provide(ctx, cfg, log, pool, tc); err != nil {
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
func (a *App) provide(ctx context.Context, cfg *config.Config, log *slog.Logger, pool *pgxpool.Pool, tc client.Client) error {
	c := a.Container
	return errors.Join(
		// The startup context, so constructors that read config at build time
		// (buildSources → SbvAgencyIDs) can reach the database.
		c.Provide(func() context.Context { return ctx }),
		c.Provide(func() *config.Config { return cfg }),
		c.Provide(func() *slog.Logger { return log }),
		c.Provide(func() *pgxpool.Pool { return pool }),
		c.Provide(func() client.Client { return tc }),
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

// buildSources selects the source crawlers for the deployment's jurisdiction
// (config.Jurisdiction, default "vn"). Each jurisdiction is a disjoint source set
// off the one shared codebase; Malaysia's set (agclom/bnm/sc) is wired in the MY
// build steps. The default and any absent value resolve to "vn", so existing VN
// deployments are unchanged.
func buildSources(ctx context.Context, log *slog.Logger, cfgQ *dbconfig.Queries, cfg *config.Config) (map[string]ingest.Source, error) {
	switch cfg.Jurisdiction {
	case "vn", "":
		return buildVNSources(ctx, log, cfgQ)
	case "my":
		return buildMYSources(log)
	default:
		return nil, fmt.Errorf("unknown jurisdiction %q", cfg.Jurisdiction)
	}
}

// buildMYSources assembles Malaysia's source crawlers. agclom (the AGC Laws of
// Malaysia database) is the law-DB backbone; bnm and sc are added in later steps.
// MY scope is title-based (config scope terms), so no per-source agency ids are
// loaded here.
func buildMYSources(log *slog.Logger) (map[string]ingest.Source, error) {
	return map[string]ingest.Source{
		agclom.SourceID: agclom.New(nil, log),
		sc.SourceID:     sc.New(nil, log),
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
	return map[string]ingest.Source{
		congbao.SourceID:  congbao.New(nil, log),
		vbpl.SourceID:     vbpl.New(nil, log, sbv, nonSbv, vbplRelTypes),
		vanban.SourceID:   vanban.New(nil, log),
		sbvhanoi.SourceID: sbvhanoi.New(nil, log),
	}, nil
}

// newActivities adapts the dig-injected dependencies to pipeline.NewActivities,
// taking the raw-file storage directory and optional embedder from config so dig
// need not resolve bare strings. OCR is not wired inline here — it runs as a
// separate batch (OcrAll); see cmd/worker -ocr-all.
func newActivities(
	pool *pgxpool.Pool,
	ledger *dbingest.Queries,
	bronze *dbbronze.Queries,
	silver *dbsilver.Queries,
	gold *dbgold.Queries,
	configQ *dbconfig.Queries,
	sources map[string]ingest.Source,
	cfg *config.Config,
) (*pipeline.Activities, error) {
	markitdown := extract.NewMarkItDownClient(
		cfg.Extract.Markitdown.Command,
		cfg.Extract.Markitdown.Script,
	)

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
	return pipeline.NewActivities(pool, ledger, bronze, silver, gold, configQ, sources, cfg.Storage.Dir, markitdown, indexEmbedder, cfg.KaggleToken, cfg.Jurisdiction), nil
}

// buildEmbedder selects the query-time embedder. Default is the OVMS HTTP endpoint
// (vector-only retrieval). Setting BANHMI_EMBED_QUERY=onnx uses the in-process ONNX
// BGE-M3 embedder (Cloud Run: no OVMS/sidecar) — only available when built with
// `-tags onnx`, else New returns an error.
func buildEmbedder(cfg *config.Config) (embed.Embedder, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BANHMI_EMBED_QUERY"))) {
	case "openvino", "ov":
		return ovembed.New(ovembed.Config{
			ModelDir:      envOr("BANHMI_OV_MODEL_DIR", "/models/bge-m3"),
			TokenizerPath: envOr("BANHMI_OV_TOKENIZER", "/models/bge-m3/tokenizer.json"),
			Dims:          config.EmbedDims,
			Model:         config.EmbedModel,
		})
	case "onnx":
		return onnxembed.New(onnxembed.Config{
			ModelPath:     envOr("BANHMI_ONNX_MODEL", "/models/bge-m3/model_quantized.onnx"),
			TokenizerPath: envOr("BANHMI_ONNX_TOKENIZER", "/models/bge-m3/tokenizer.json"),
			LibPath:       os.Getenv("BANHMI_ONNX_LIB"),
			Dims:          config.EmbedDims,
			Model:         config.EmbedModel,
		})
	}
	return embed.New(cfg.EmbedEndpoint(), config.EmbedModel, config.EmbedDims, ""), nil
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
	log *slog.Logger,
) (retrieve.Retriever, error) {
	gate, err := loadRetrieveGate(ctx, cfgQ, cfg.Jurisdiction)
	if err != nil {
		return nil, err
	}
	emb, err := buildEmbedder(cfg)
	if err != nil {
		return nil, fmt.Errorf("build query embedder: %w", err)
	}
	return retrieve.New(pool, emb, cfg.Retrieve, log, retrieve.WithGateConfig(gate)), nil
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
