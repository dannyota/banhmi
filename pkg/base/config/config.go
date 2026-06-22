// Package config loads banhmi configuration from YAML. Secrets are supplied by
// the environment so they never live in the file. A missing file is not an
// error: built-in defaults are returned so a fresh clone runs without setup.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// BGE-M3 is the fixed self-hosted embedder. It is served by the podman
	// OpenVINO Model Server service and can only be toggled on/off by config.
	EmbedModel = "Fede90/bge-m3-int8-ov"
	EmbedDims  = 1024

	hostEmbedEndpoint      = "http://127.0.0.1:10007/v3"
	containerEmbedEndpoint = "http://embedder:8000/v3"
)

// Config is the top-level banhmi configuration.
type Config struct {
	Name         string         `yaml:"name"`
	Jurisdiction string         `yaml:"jurisdiction"` // legal jurisdiction served (default "vn"); selects sources/scope/config
	Database     DatabaseConfig `yaml:"database"`
	Redis        RedisConfig    `yaml:"redis"`
	Temporal     TemporalConfig `yaml:"temporal"`
	Sources      SourcesConfig  `yaml:"sources"`
	Crawl        CrawlConfig    `yaml:"crawl"`
	Storage      StorageConfig  `yaml:"storage"`
	Extract      ExtractConfig  `yaml:"extract"`
	Embed        EmbedConfig    `yaml:"embed"`
	Retrieve     RetrieveConfig `yaml:"retrieve"`
	Server       ServerConfig   `yaml:"server"`

	// KaggleToken is the Kaggle API token (KGAT). Like the DB password it is a
	// secret: loaded from KAGGLE_API_TOKEN in applyEnv, never from the YAML file.
	// It drives the "auto" bulk-engine choice (EmbedEngine/OcrEngine) and
	// authenticates the bulk embed/OCR Kaggle clients. It is never placed in
	// Temporal workflow params (which are persisted in history).
	KaggleToken string `yaml:"-"`
}

// DatabaseConfig holds PostgreSQL connection settings. Password comes from the
// environment (BANHMI_DATABASE_PASSWORD), never the YAML file.
type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	DBName   string `yaml:"dbname"`
	SSLMode  string `yaml:"sslmode"`
	Password string `yaml:"-"`
}

// RedisConfig holds the Redis address.
type RedisConfig struct {
	Addr string `yaml:"addr"`
}

// TemporalConfig holds Temporal client settings.
type TemporalConfig struct {
	HostPort  string `yaml:"hostport"`
	Namespace string `yaml:"namespace"`
	TaskQueue string `yaml:"task_queue"`
}

// SourceConfig configures a single source crawler. Per-source crawl vocabulary
// (issuer/agency ids, categories) lives in the config schema, not here.
type SourceConfig struct {
	Enabled bool `yaml:"enabled"`
}

// SourcesConfig groups the per-source settings.
type SourcesConfig struct {
	Congbao  SourceConfig `yaml:"congbao"`
	VBPL     SourceConfig `yaml:"vbpl"`
	Vanban   SourceConfig `yaml:"vanban"`
	SBVHanoi SourceConfig `yaml:"sbv_hanoi"`
	Phapluat SourceConfig `yaml:"phapluat"`
}

// CrawlConfig holds shared crawler etiquette settings.
type CrawlConfig struct {
	UserAgent   string `yaml:"user_agent"`
	OffPeakOnly bool   `yaml:"off_peak_only"`
}

// StorageConfig locates the raw-file store. Downloaded PDFs/DOCX/DOC are written here
// (a volume path) and referenced from bronze by content hash, not stored in
// Postgres.
type StorageConfig struct {
	Dir string `yaml:"dir"`
}

// ExtractConfig controls deterministic extraction.
type ExtractConfig struct {
	OCR        OCRConfig        `yaml:"ocr"`
	Markitdown MarkitdownConfig `yaml:"markitdown"`
}

// OCRConfig controls scanned-PDF OCR. OCR uses EasyOCR (Apache-2.0) and runs as a
// batch (OcrAll), never inline. Engine "auto" (default) uses the Kaggle GPU when
// KAGGLE_API_TOKEN is set, else the local CPU EasyOCR tool; "local"/"kaggle" force
// one. Auth is the KAGGLE_API_TOKEN environment variable, never the YAML file.
type OCRConfig struct {
	Engine    string          `yaml:"engine"`     // "auto" | "local" | "kaggle"
	Command   string          `yaml:"command"`    // python3 runner for the local EasyOCR tool
	Script    string          `yaml:"script"`     // helper script path; empty = compiled default
	Languages string          `yaml:"languages"`  // EasyOCR language list, e.g. "vi"
	DPI       int             `yaml:"dpi"`        // PDF render DPI (default 300)
	BatchSize int             `yaml:"batch_size"` // EasyOCR recognition batch size (default 32)
	Kaggle    OCRKaggleConfig `yaml:"kaggle"`

	// Legacy OCRmyPDF/Tesseract knobs for the previous, now-unwired OCRClient,
	// kept so it can be re-enabled or removed cleanly. Unused by the EasyOCR path.
	Tesseract  string `yaml:"tesseract"`
	PDFToImage string `yaml:"pdftoppm"`
	Language   string `yaml:"language"`
}

// OCRKaggleConfig configures the Kaggle batch OCR engine (pkg/rag/ocr/kagglebatch).
type OCRKaggleConfig struct {
	// Owner is the Kaggle username owning the input dataset and OCR kernel
	// (auto-derived from KAGGLE_API_TOKEN when empty).
	Owner string `yaml:"owner"`
	// Accelerator is the Kaggle machine shape, e.g. "NvidiaTeslaT4".
	Accelerator string `yaml:"accelerator"`
	// MinBatch falls back to local CPU OCR when fewer than this many scans need
	// OCR (a Kaggle round-trip is not worth it for a handful).
	MinBatch int `yaml:"min_batch"`
}

// MarkitdownConfig locates the local MarkItDown runner. MarkItDown is required:
// the app container installs it next to the Go binaries.
type MarkitdownConfig struct {
	Command string `yaml:"command"` // e.g. "python3"; empty = python3
	Script  string `yaml:"script"`  // helper script; empty = compiled defaults
}

// EmbedConfig selects how chunk embeddings are produced for indexing/backfill.
// Query-time embedding always uses the local endpoint (see EmbedEndpoint); Engine
// only chooses the BULK embedding engine, never the synchronous query path.
//
// Engine: "auto" (default) uses Kaggle when KAGGLE_API_TOKEN is set, else local;
// "local" forces the OpenVINO endpoint; "kaggle" forces the Kaggle batch engine.
type EmbedConfig struct {
	Engine string            `yaml:"engine"`
	Kaggle EmbedKaggleConfig `yaml:"kaggle"`
}

// EmbedKaggleConfig configures the Kaggle batch embedding engine
// (pkg/rag/embed/kaggle). Auth is the KAGGLE_API_TOKEN environment variable,
// never the YAML file.
type EmbedKaggleConfig struct {
	// Owner is the Kaggle username owning the input dataset and embed kernel.
	Owner string `yaml:"owner"`
	// ModelDataset optionally mounts BGE-M3 from a Kaggle dataset ("owner/slug")
	// so the kernel runs offline; empty pulls BAAI/bge-m3 from HuggingFace.
	ModelDataset string `yaml:"model_dataset"`
	// Accelerator is the Kaggle machine shape, e.g. "NvidiaTeslaT4".
	Accelerator string `yaml:"accelerator"`
	// MinBatch falls back to the local embedder when fewer than this many chunks
	// need embedding (a Kaggle round-trip is not worth it for small batches).
	MinBatch int `yaml:"min_batch"`
}

// RetrieveConfig configures the retrieval pipeline (pkg/rag/retrieve). TopK is the
// number of fused hits returned; VectorK / BM25K cap each arm's candidate list
// before RRF fusion; RRFK is the reciprocal-rank-fusion constant (score =
// Σ 1/(RRFK + rank)). Lexical selects the lexical engine ("pg_search" BM25).
type RetrieveConfig struct {
	Lexical  string `yaml:"lexical"`
	Reranker string `yaml:"reranker"` // NOT yet consumed — ViRanker rerank is planned/unwired

	InForceOnly bool `yaml:"in_force_only"`
	TopK        int  `yaml:"top_k"`
	VectorK     int  `yaml:"vector_k"`
	BM25K       int  `yaml:"bm25_k"`
	RRFK        int  `yaml:"rrf_k"`
	// LexicalWeight scales the lexical (BM25 sparse) arm in RRF fusion relative to
	// the dense vector arm (1.0). Below 1.0 keeps a noisy lexical arm from
	// outvoting dense relevance; 0 falls back to 1.0. Default 0.5.
	LexicalWeight float64 `yaml:"lexical_weight"`
	// LexicalBoostWeight is the lexical weight used for queries the router sends to
	// lexical (diacritic-less text or an explicit số ký hiệu) — where the dense
	// vector is weak and BM25 should lead. 0 disables routing (always LexicalWeight).
	LexicalBoostWeight float64 `yaml:"lexical_boost_weight"`

	// RollupLevel collapses sibling chunks to their parent provision so one Khoản's
	// Điểm/Đoạn do not crowd the top-k: "khoan" (default), "dieu", or "none".
	RollupLevel string `yaml:"rollup_level"`
}

// ServerConfig configures the HTTP query surface (cmd/server): the evidence-only
// MCP-over-HTTP endpoint (/mcp) for remote user-owned agents. Addr is the listen
// address (host:port; empty host binds all interfaces).
type ServerConfig struct {
	Addr string `yaml:"addr"` // e.g. ":8088"
}

// Default returns the built-in configuration used when no config file exists.
func Default() *Config {
	return &Config{
		Name:         "banhmi",
		Jurisdiction: "vn",
		Database:     DatabaseConfig{Host: "localhost", Port: 5432, User: "banhmi", DBName: "banhmi", SSLMode: "disable"},
		Redis:        RedisConfig{Addr: "localhost:6379"},
		Storage:      StorageConfig{Dir: "data/files"},
		Extract: ExtractConfig{
			OCR: OCRConfig{
				Engine:    "auto",
				Command:   "python3",
				Languages: "vi",
				DPI:       300,
				BatchSize: 32,
				Kaggle:    OCRKaggleConfig{Accelerator: "NvidiaTeslaT4", MinBatch: 4},
				Tesseract: "tesseract", PDFToImage: "ocrmypdf", Language: "vie+eng",
			},
			Markitdown: MarkitdownConfig{Command: "python3"},
		},
		Embed: EmbedConfig{
			Engine: "auto",
			Kaggle: EmbedKaggleConfig{Accelerator: "NvidiaTeslaT4", MinBatch: 500},
		},
		Temporal: TemporalConfig{HostPort: "localhost:7233", Namespace: "default", TaskQueue: "banhmi"},
		Retrieve: RetrieveConfig{
			Lexical: "sparsevec", Reranker: "none", InForceOnly: true,
			TopK: 8, VectorK: 50, BM25K: 50, RRFK: 60, RollupLevel: "khoan",
			LexicalWeight: 0.5, LexicalBoostWeight: 1.0,
		},
		Server: ServerConfig{Addr: ":8088"},
	}
}

// Load reads configuration from path, falling back to Default when the file is
// absent. Secrets are always read from the environment.
func Load(path string) (*Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	case os.IsNotExist(err):
		// keep defaults
	default:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	c.applyEnv()
	return c, nil
}

// applyEnv lets the environment override file/default config so a single image
// works across deployments — local YAML, or Cloud Run + Neon via env + secrets.
// Non-secret connection params (host/port/user/dbname/sslmode) and the embedder
// endpoint are env-overridable; the password stays env-only.
func (c *Config) applyEnv() {
	if v := os.Getenv("BANHMI_DATABASE_HOST"); v != "" {
		c.Database.Host = v
	}
	if v := os.Getenv("BANHMI_DATABASE_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			c.Database.Port = p
		}
	}
	if v := os.Getenv("BANHMI_DATABASE_USER"); v != "" {
		c.Database.User = v
	}
	if v := os.Getenv("BANHMI_DATABASE_NAME"); v != "" {
		c.Database.DBName = v
	}
	if v := os.Getenv("BANHMI_DATABASE_SSLMODE"); v != "" {
		c.Database.SSLMode = v
	}
	if v := os.Getenv("BANHMI_DATABASE_PASSWORD"); v != "" {
		c.Database.Password = v
	}
	if v := os.Getenv("KAGGLE_API_TOKEN"); v != "" {
		c.KaggleToken = v
	}
	if v := os.Getenv("BANHMI_JURISDICTION"); v != "" {
		c.Jurisdiction = v
	}
	if c.Jurisdiction == "" {
		c.Jurisdiction = "vn"
	}
}

// EmbedEndpoint returns the BGE-M3 query endpoint. The embedder is required
// (vector-only retrieval): host-run binaries use the published podman port,
// in-container binaries use the compose service name. BANHMI_EMBED_ENDPOINT
// overrides both — e.g. a Cloud Run sidecar at http://127.0.0.1:8000/v3.
func (c *Config) EmbedEndpoint() string {
	if v := os.Getenv("BANHMI_EMBED_ENDPOINT"); v != "" {
		return v
	}
	if c.inContainerNetwork() {
		return containerEmbedEndpoint
	}
	return hostEmbedEndpoint
}

func (c *Config) inContainerNetwork() bool {
	host := strings.ToLower(strings.TrimSpace(c.Database.Host))
	return host != "" && host != "localhost" && host != "127.0.0.1" && host != "::1"
}

// EmbedEngine resolves the bulk-embedding engine: "kaggle" or "local". The
// configured "auto" (or empty) resolves to "kaggle" when KAGGLE_API_TOKEN is
// set, otherwise "local". Query-time embedding is unaffected.
func (c *Config) EmbedEngine() string {
	switch strings.ToLower(strings.TrimSpace(c.Embed.Engine)) {
	case "local":
		return "local"
	case "kaggle":
		return "kaggle"
	default: // "auto" or empty
		if c.KaggleToken != "" {
			return "kaggle"
		}
		return "local"
	}
}

// OcrEngine resolves the OCR batch engine: "kaggle" or "local". Configured "auto"
// (or empty) resolves to "kaggle" when KAGGLE_API_TOKEN is set, otherwise "local".
// OCR always runs as a batch (OcrAll), never inline.
func (c *Config) OcrEngine() string {
	switch strings.ToLower(strings.TrimSpace(c.Extract.OCR.Engine)) {
	case "local":
		return "local"
	case "kaggle":
		return "kaggle"
	default: // "auto" or empty
		if c.KaggleToken != "" {
			return "kaggle"
		}
		return "local"
	}
}

// OCRLanguages returns the EasyOCR language list, following the one-main-language-
// per-country policy: Malaysia's corpus is English, so it OCRs in "en"; every other
// jurisdiction uses the configured value (default "vi" for Vietnam). OCR text is
// never the binding legal text, so the language only needs to match the corpus.
func (c *Config) OCRLanguages() string {
	if strings.EqualFold(strings.TrimSpace(c.Jurisdiction), "my") {
		return "en"
	}
	return c.Extract.OCR.Languages
}

// DSN returns a libpq connection string, including the password only if set.
func (d DatabaseConfig) DSN() string {
	parts := []string{
		"host=" + dsnQuote(d.Host),
		"port=" + strconv.Itoa(d.Port),
		"user=" + dsnQuote(d.User),
		"dbname=" + dsnQuote(d.DBName),
		"sslmode=" + dsnQuote(d.SSLMode),
	}
	if d.Password != "" {
		parts = append(parts, "password="+dsnQuote(d.Password))
	}
	return strings.Join(parts, " ")
}

// dsnQuote escapes a libpq keyword/value DSN value. A value that is empty or
// contains a space, single quote, or backslash is wrapped in single quotes with
// ' and \ backslash-escaped — so a Neon password with special characters can't
// corrupt the connection string (it feeds both the pgx pool and cmd/migrate).
func dsnQuote(v string) string {
	if v == "" {
		return "''"
	}
	if !strings.ContainsAny(v, ` '\`) {
		return v
	}
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}

// Redacted returns a DSN safe for logs (no password).
func (d DatabaseConfig) Redacted() string {
	return fmt.Sprintf("host=%s port=%d user=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.DBName, d.SSLMode)
}
