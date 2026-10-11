// Package serve is the lean composition root for read-only evidence serving.
package serve

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"danny.vn/banhmi/internal/evidence/api"
	evidenceservice "danny.vn/banhmi/internal/evidence/service"
	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/base/db"
	"danny.vn/banhmi/pkg/base/jurisdiction"
	"danny.vn/banhmi/pkg/base/sourceurl"
	"danny.vn/banhmi/pkg/mcp"
	"danny.vn/banhmi/pkg/rag/embed"
	"danny.vn/banhmi/pkg/rag/embed/onnxembed"
	"danny.vn/banhmi/pkg/rag/embed/ovembed"
	"danny.vn/banhmi/pkg/rag/retrieve"
	"danny.vn/banhmi/pkg/scope"
	dbconfig "danny.vn/banhmi/pkg/store/config"
)

type App struct {
	Pool       *pgxpool.Pool
	Retriever  retrieve.Retriever
	Operations api.Operations
	MCP        *mcp.Server
}

func New(ctx context.Context, cfg *config.Config, log *slog.Logger, version string, emb embed.Embedder, resolver api.ReleaseResolver, behindProxy bool) (*App, error) {
	if cfg == nil {
		return nil, fmt.Errorf("serving config is required")
	}
	if emb == nil {
		return nil, fmt.Errorf("query embedder is required")
	}
	if _, ok := jurisdiction.Lookup(cfg.Jurisdiction); !ok {
		return nil, fmt.Errorf("unknown jurisdiction %q", cfg.Jurisdiction)
	}
	pool, err := db.NewPool(ctx, cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("database pool: %w", err)
	}
	r, err := newRetriever(ctx, pool, cfg, emb, log)
	if err != nil {
		pool.Close()
		return nil, err
	}
	corpus := mcp.NewCorpusReader(pool, map[string]func(string) string{"vbpl": sourceurl.VBPLFilesListingURL})
	baseOperations, err := evidenceservice.NewOperations(r, corpus, mcp.GuideFor(cfg.Jurisdiction), version, log)
	if err != nil {
		pool.Close()
		return nil, err
	}
	if resolver == nil {
		resolver = evidenceservice.NewLegacyResolver(cfg.Jurisdiction, baseOperations)
	}
	operations, err := evidenceservice.New(cfg.Jurisdiction, baseOperations, cfg.Retrieve, resolver)
	if err != nil {
		pool.Close()
		return nil, err
	}
	metadata := mcp.StartupMetadataFor(cfg.Jurisdiction)
	loadedMetadata, metadataErr := evidenceservice.LoadStartupMetadata(ctx, pool, metadata)
	metadata = loadedMetadata
	if metadataErr != nil {
		log.Warn("evidence startup metadata incomplete", "err", metadataErr)
	}
	opts := []mcp.Option{
		mcp.WithJurisdiction(cfg.Jurisdiction),
		mcp.WithVersion(version),
		mcp.WithStartupMetadata(metadata),
	}
	if behindProxy {
		opts = append(opts, mcp.WithBehindProxy())
	}
	transport := mcp.NewOperations(operations, log, opts...)
	return &App{Pool: pool, Retriever: r, Operations: operations, MCP: transport}, nil
}

func (a *App) Close() {
	if a != nil && a.Pool != nil {
		a.Pool.Close()
	}
}

// NewQueryEmbedder builds one query embedder shared by all served countries.
func NewQueryEmbedder(cfg *config.Config) (embed.Embedder, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BANHMI_EMBED_QUERY"))) {
	case "openvino", "ov":
		return ovembed.New(ovembed.Config{ModelDir: envOr("BANHMI_OV_MODEL_DIR", "/models/bge-m3"), TokenizerPath: envOr("BANHMI_OV_TOKENIZER", "/models/bge-m3/tokenizer.json"), Dims: config.EmbedDims, Model: config.EmbedModel, Device: envOr("BANHMI_OV_DEVICE", "AUTO")})
	case "onnx":
		concurrency, maxQueued, err := queryONNXAdmissionConfig()
		if err != nil {
			return nil, err
		}
		return onnxembed.New(onnxembed.Config{ModelPath: envOr("BANHMI_ONNX_MODEL", "/models/qwen3-embedding/model_fp16.onnx"), TokenizerPath: envOr("BANHMI_ONNX_TOKENIZER", "/models/qwen3-embedding/tokenizer.json"), LibPath: os.Getenv("BANHMI_ONNX_LIB"), Dims: config.EmbedDims, Model: config.EmbedModel, CUDA: os.Getenv("BANHMI_ONNX_CUDA") == "1", Concurrency: concurrency, MaxQueued: maxQueued})
	}
	endpoint := strings.TrimSpace(os.Getenv("BANHMI_EMBED_QUERY_ENDPOINT"))
	if endpoint == "" {
		endpoint = cfg.EmbedEndpoint()
	}
	return embed.New(endpoint, config.EmbedModel, config.EmbedDims, os.Getenv("BANHMI_EMBED_TOKEN")), nil
}

func queryONNXAdmissionConfig() (int, int, error) {
	defaultConcurrency := runtime.NumCPU()
	if defaultConcurrency < 2 {
		defaultConcurrency = 2
	}
	concurrency, err := positiveEnvInt("BANHMI_EMBED_CONCURRENCY", defaultConcurrency)
	if err != nil {
		return 0, 0, err
	}
	maxQueued, err := positiveEnvInt("BANHMI_EMBED_MAX_QUEUED", concurrency)
	if err != nil {
		return 0, 0, err
	}
	return concurrency, maxQueued, nil
}

func positiveEnvInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return value, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func newRetriever(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, emb embed.Embedder, log *slog.Logger) (retrieve.Retriever, error) {
	queries := dbconfig.New(pool)
	gate, err := loadRetrieveGate(ctx, queries, cfg.Jurisdiction)
	if err != nil {
		return nil, err
	}
	disable, err := validityFilterUnusable(ctx, pool)
	if err != nil {
		return nil, err
	}
	gate.DisableValidityFilter = disable
	if disable {
		log.Warn("retrieve: validity pre-filter disabled; corpus has no current validity rows")
	}
	diacriticRows, err := queries.ListDiacriticRestore(ctx, cfg.Jurisdiction)
	if err != nil {
		return nil, fmt.Errorf("load diacritic_restore: %w", err)
	}
	diacritics := make(map[string]string, len(diacriticRows))
	for _, row := range diacriticRows {
		diacritics[row.FoldedToken] = row.RestoredToken
	}
	abbreviationRows, err := queries.ListAbbreviationExpand(ctx, cfg.Jurisdiction)
	if err != nil {
		return nil, fmt.Errorf("load abbreviation_expand: %w", err)
	}
	abbreviations := make(map[string]string, len(abbreviationRows))
	for _, row := range abbreviationRows {
		abbreviations[strings.ToLower(row.Abbreviation)] = row.Expansion
	}
	families, err := loadConsolidationFamilies(ctx, pool)
	if err != nil {
		return nil, err
	}
	return retrieve.New(pool, emb, cfg.Retrieve, log,
		retrieve.WithGateConfig(gate),
		retrieve.WithJurisdiction(jurisdiction.For(cfg.Jurisdiction)),
		retrieve.WithDiacriticDict(diacritics),
		retrieve.WithAbbreviationDict(abbreviations),
		retrieve.WithConsolidationFamilies(retrieve.BuildFamilyMap(families)),
	), nil
}

func loadRetrieveGate(ctx context.Context, queries *dbconfig.Queries, country string) (retrieve.GateConfig, error) {
	rows, err := queries.ListScopeTerms(ctx, country)
	if err != nil {
		return retrieve.GateConfig{}, fmt.Errorf("load retrieval scope terms: %w", err)
	}
	terms := make([]scope.Term, 0, len(rows))
	for _, row := range rows {
		terms = append(terms, scope.Term{Text: row.Term, Class: row.TermClass})
	}
	settings, err := queries.ListSettings(ctx)
	if err != nil {
		return retrieve.GateConfig{}, fmt.Errorf("load retrieval settings: %w", err)
	}
	return retrieve.GateConfig{ScopeTerms: terms, MinScore: floatSetting(settings, "retrieve.abstain.min_score")}, nil
}

func floatSetting(rows []dbconfig.ListSettingsRow, key string) float64 {
	for _, row := range rows {
		if strings.TrimSpace(row.Key) == key {
			value, err := strconv.ParseFloat(strings.TrimSpace(row.Value), 64)
			if err == nil && value >= 0 {
				return value
			}
		}
	}
	return 0
}

func loadConsolidationFamilies(ctx context.Context, pool *pgxpool.Pool) ([][2]int64, error) {
	const query = `SELECT r.from_document_id, ref.document_id FROM silver.document_relation r JOIN silver.doc_ref ref ON ref.id = r.to_ref_id AND ref.document_id IS NOT NULL WHERE r.relation_type = 'consolidates'`
	rows, err := pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("load consolidation families: %w", err)
	}
	defer rows.Close()
	var pairs [][2]int64
	for rows.Next() {
		var pair [2]int64
		if err := rows.Scan(&pair[0], &pair[1]); err != nil {
			return nil, fmt.Errorf("scan consolidation family: %w", err)
		}
		pairs = append(pairs, pair)
	}
	return pairs, rows.Err()
}

func validityFilterUnusable(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	const query = `SELECT (SELECT count(*) FROM gold.chunk) > 0 AND NOT EXISTS (SELECT 1 FROM (SELECT DISTINCT ON (document_id) status_class FROM silver.validity_period WHERE superseded_at IS NULL AND section_id IS NULL ORDER BY document_id, observed_at DESC, id DESC) cur WHERE cur.status_class IN ('in_force', 'partial'))`
	var unusable bool
	if err := pool.QueryRow(ctx, query).Scan(&unusable); err != nil {
		return false, fmt.Errorf("probe validity coverage: %w", err)
	}
	return unusable, nil
}
