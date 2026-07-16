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

	"danny.vn/banhmi/pkg/base/jurisdiction"
)

const (
	// Qwen3-Embedding-0.6B is the single embedding model — in-process ONNX Runtime
	// (BANHMI_EMBED_QUERY=onnx, `-tags onnx`) for query-time; Cloud Run L4 or Kaggle
	// for bulk indexing. Same model everywhere (index/query parity).
	EmbedModel = "qwen3-embedding-0.6b"
	EmbedDims  = 1024

	// Legacy HTTP-endpoint fallbacks, used only when BANHMI_EMBED_QUERY is
	// unset: a self-hoster's own OpenAI-compatible embeddings service
	// (BANHMI_EMBED_ENDPOINT overrides both).
	hostEmbedEndpoint      = "http://127.0.0.1:10007/v3"
	containerEmbedEndpoint = "http://embedder:8000/v3"
)

// Config is the top-level banhmi configuration.
type Config struct {
	Name         string         `yaml:"name"`
	Jurisdiction string         `yaml:"jurisdiction"` // legal jurisdiction served (default "vn"); selects sources/scope/config
	Database     DatabaseConfig `yaml:"database"`
	Redis        RedisConfig    `yaml:"redis"`
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
	// authenticates the bulk embed/OCR Kaggle clients.
	KaggleToken string `yaml:"-"`

	// OJKProxyURL is the Cloud Run fetch-proxy endpoint for OJK requests.
	// When set, the OJK source routes all HTTP requests through this proxy
	// (bypasses OJK geo-blocking). Loaded from BANHMI_OJK_PROXY_URL.
	OJKProxyURL string `yaml:"-"`
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
// Postgres. S3DataBucket is the optional S3 bucket for persisting fetched files
// so ephemeral-disk environments (EC2) can recover them between stages.
type StorageConfig struct {
	Dir          string `yaml:"dir"`
	S3DataBucket string `yaml:"s3_data_bucket"` // S3 bucket name; empty disables
}

// ExtractConfig controls deterministic extraction.
type ExtractConfig struct {
	OCR OCRConfig `yaml:"ocr"`
}

// OCRConfig controls scanned-PDF OCR. Engine "auto" (default) uses the Kaggle GPU
// when KAGGLE_API_TOKEN is set, else the local CPU EasyOCR tool; "local"/"kaggle"
// force one; "documentai" uses GCP Document AI Enterprise OCR (synchronous
// ProcessDocument with client-side parallelism, S3 text cache).
// Auth: Kaggle via KAGGLE_API_TOKEN; Document AI via ADC (gcloud auth).
type OCRConfig struct {
	Engine    string          `yaml:"engine"`     // "auto" | "local" | "kaggle" | "documentai"
	Command   string          `yaml:"command"`    // python3 runner for the local EasyOCR tool
	Script    string          `yaml:"script"`     // helper script path; empty = compiled default
	Languages string          `yaml:"languages"`  // EasyOCR language list, e.g. "vi"
	DPI       int             `yaml:"dpi"`        // PDF render DPI (default 300)
	BatchSize int             `yaml:"batch_size"` // EasyOCR recognition batch size (default 32)
	Kaggle    OCRKaggleConfig `yaml:"kaggle"`
	// DocumentAI configures the GCP Document AI Enterprise OCR engine.
	DocumentAI OCRDocumentAIConfig `yaml:"documentai"`

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

// OCRDocumentAIConfig configures the Google sync OCR engine
// (pkg/extract/docai — Vision images:annotate, DOCUMENT_TEXT_DETECTION,
// model builtin/latest). Auth is ADC (Application Default Credentials),
// never the YAML file. No processor resource is needed: Vision is a global
// API, unlike the Document AI processor this engine previously wrapped.
type OCRDocumentAIConfig struct {
	// Concurrency is the maximum number of parallel annotate calls.
	// Default 8.
	Concurrency int `yaml:"concurrency"`
	// RequestsPerMinute caps Vision API calls. Default 600 (the
	// document-text quota is 1,800/min).
	RequestsPerMinute int `yaml:"requests_per_minute"`
}

// EmbedConfig selects how chunk embeddings are produced for indexing/backfill.
// Query-time embedding always uses the local endpoint (see EmbedEndpoint); Engine
// only chooses the BULK embedding engine, never the synchronous query path.
//
// Engine: "auto" (default) uses Kaggle when KAGGLE_API_TOKEN is set, else local;
// "local" forces the OpenVINO endpoint; "kaggle" forces the Kaggle batch engine;
// "sagemaker" forces the AWS SageMaker Processing Job batch engine.
type EmbedConfig struct {
	Engine    string               `yaml:"engine"`
	Kaggle    EmbedKaggleConfig    `yaml:"kaggle"`
	SageMaker EmbedSageMakerConfig `yaml:"sagemaker"`
}

// EmbedKaggleConfig configures the Kaggle batch embedding engine
// (pkg/rag/embed/kaggle). Auth is the KAGGLE_API_TOKEN environment variable,
// never the YAML file.
type EmbedKaggleConfig struct {
	// Owner is the Kaggle username owning the input dataset and embed kernel.
	Owner string `yaml:"owner"`
	// ModelDataset mounts the Qwen3-Embedding-0.6B ONNX FP16 model from a Kaggle
	// dataset ("owner/slug") so the kernel runs offline. Required — the kernel
	// uses onnxruntime, not PyTorch/HuggingFace.
	ModelDataset string `yaml:"model_dataset"`
	// Accelerator is the Kaggle machine shape, e.g. "NvidiaTeslaT4".
	Accelerator string `yaml:"accelerator"`
	// MinBatch falls back to the local embedder when fewer than this many chunks
	// need embedding (a Kaggle round-trip is not worth it for small batches).
	MinBatch int `yaml:"min_batch"`
}

// EmbedSageMakerConfig configures the AWS SageMaker batch embedding engine
// (pkg/rag/embed/sagebatch). Auth is the standard AWS SDK credential chain
// (AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY env vars or IAM role), never the YAML
// file.
type EmbedSageMakerConfig struct {
	// Bucket is the S3 bucket for input/output data.
	Bucket string `yaml:"bucket"`
	// RoleARN is the SageMaker execution role ARN.
	RoleARN string `yaml:"role_arn"`
	// Region is the AWS region (e.g. "ap-southeast-1"). Empty inherits from
	// AWS_DEFAULT_REGION.
	Region string `yaml:"region"`
	// InstanceType is the SageMaker instance type (e.g. "ml.g4dn.xlarge").
	InstanceType string `yaml:"instance_type"`
	// ContainerImage overrides the default PyTorch DLC image. Empty uses the
	// built-in default.
	ContainerImage string `yaml:"container_image"`
}

// RetrieveConfig configures the retrieval pipeline (pkg/rag/retrieve). TopK is the
// number of fused hits returned; VectorK / BM25K cap each arm's candidate list
// before RRF fusion; RRFK is the reciprocal-rank-fusion constant (score =
// Σ 1/(RRFK + rank)). The lexical arm is always pgvector sparsevec BM25 — there is
// no engine selector.
type RetrieveConfig struct {
	Reranker string `yaml:"reranker"` // NOT yet consumed — ViRanker rerank is planned/unwired

	InForceOnly bool `yaml:"in_force_only"`
	TopK        int  `yaml:"top_k"`
	VectorK     int  `yaml:"vector_k"`
	BM25K       int  `yaml:"bm25_k"`
	RRFK        int  `yaml:"rrf_k"`
	// DocCap limits how many primary-pass hits one document may occupy in the
	// top-k (0 = no cap). Large laws and near-duplicate statutes can otherwise
	// crowd every slot; capped-out hits are demoted, and their slots go to the
	// next-ranked documents. Hits are backfilled if fewer than top-k survive.
	DocCap int `yaml:"doc_cap"`
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

	// SectionAggregate enables promotion-only section aggregation AFTER the
	// natural top-k is produced: multi-fragment groups (>=2 members under the
	// same DocumentID + article-level citation) whose best member is NOT in the
	// natural top-k are APPENDED (up to 2) if their aggregate score (sum of
	// top-3 members' fused RRF scores) exceeds the weakest top-k entry. The
	// natural top-k is never modified — no entry is removed, reordered, or
	// demoted. This rescues long, multi-subsection articles whose relevance
	// signal is diluted across chunks without risking recall regressions.
	SectionAggregate bool `yaml:"section_aggregate"`

	// HNSWCandidateMultiplier sizes the ANN candidate pool for the vector arm:
	// the HNSW scan fetches multiplier×VectorK nearest chunks (floor 400) before
	// the current-law/document filters apply outside the scan. Bigger = fewer
	// exact-scan fallbacks on filter-heavy queries, slightly slower scans.
	// 0 = default 24. -1 DISABLES the ANN path entirely (exact scan only) — for
	// corpora whose cached exact scan beats filtered-ANN recall (VN: one golden
	// case sits >1200 deep unfiltered but top-50 in-force; see PLAN 2026-07-12).
	// Env override: BANHMI_HNSW_CANDIDATE_MULTIPLIER.
	HNSWCandidateMultiplier int `yaml:"hnsw_candidate_multiplier"`
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
		},
		Embed: EmbedConfig{
			Engine: "auto",
			Kaggle: EmbedKaggleConfig{
				ModelDataset: "danhsoftware/qwen3-embedding-06b-onnx-fp16",
				Accelerator:  "NvidiaTeslaT4",
				MinBatch:     500,
			},
		},
		Retrieve: RetrieveConfig{
			Reranker: "none", InForceOnly: true,
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
	if v := os.Getenv("BANHMI_EMBED_ENGINE"); v != "" {
		c.Embed.Engine = v
	}
	if v := os.Getenv("BANHMI_EMBED_KAGGLE_MODEL_DATASET"); v != "" {
		c.Embed.Kaggle.ModelDataset = v
	}
	if v := os.Getenv("BANHMI_OCR_ENGINE"); v != "" {
		c.Extract.OCR.Engine = v
	}
	if v := os.Getenv("BANHMI_S3_DATA_BUCKET"); v != "" {
		c.Storage.S3DataBucket = v
	}
	if v := os.Getenv("BANHMI_HNSW_CANDIDATE_MULTIPLIER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Retrieve.HNSWCandidateMultiplier = n
		}
	}
	if v := os.Getenv("BANHMI_OJK_PROXY_URL"); v != "" {
		c.OJKProxyURL = v
	}
	if v := os.Getenv("BANHMI_STORAGE_DIR"); v != "" {
		c.Storage.Dir = v
	}
	if v := os.Getenv("BANHMI_JURISDICTION"); v != "" {
		c.Jurisdiction = v
	}
	if c.Jurisdiction == "" {
		c.Jurisdiction = "vn"
	}
	// One database per country: when nothing overrides the compiled default
	// database name, it follows the jurisdiction descriptor so a non-VN worker
	// can never write into the VN database by omission. Explicit env always wins.
	if os.Getenv("BANHMI_DATABASE_NAME") == "" && c.Database.DBName == Default().Database.DBName {
		c.Database.DBName = jurisdiction.For(c.Jurisdiction).DBName
	}
	// Per-jurisdiction file cache: data/vn, data/my, data/id — so parallel
	// pipeline runs don't share a single directory.
	if os.Getenv("BANHMI_STORAGE_DIR") == "" && c.Storage.Dir == Default().Storage.Dir {
		c.Storage.Dir = "data/" + c.Jurisdiction
	}
}

// EmbedEndpoint returns the HTTP embeddings endpoint used when the in-process
// embedder is not selected (BANHMI_EMBED_QUERY unset) — a self-hoster's own
// embedder service. BANHMI_EMBED_ENDPOINT overrides the built-in defaults.
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

// EmbedEngine resolves the bulk-embedding engine: "kaggle", "sagemaker",
// "onnx", or "local". The configured "auto" (or empty) resolves to "kaggle"
// when KAGGLE_API_TOKEN is set, otherwise "local".
func (c *Config) EmbedEngine() string {
	switch strings.ToLower(strings.TrimSpace(c.Embed.Engine)) {
	case "local":
		return "local"
	case "kaggle":
		return "kaggle"
	case "sagemaker":
		return "sagemaker"
	case "onnx":
		return "onnx"
	default: // "auto" or empty
		if c.KaggleToken != "" {
			return "kaggle"
		}
		return "local"
	}
}

// OcrEngine resolves the OCR batch engine: "kaggle", "local", or "documentai".
// Configured "auto" (or empty) resolves to "kaggle" when KAGGLE_API_TOKEN is set,
// otherwise "local". "documentai" forces GCP Document AI Enterprise OCR.
// OCR always runs as a batch (OcrAll), never inline.
func (c *Config) OcrEngine() string {
	switch strings.ToLower(strings.TrimSpace(c.Extract.OCR.Engine)) {
	case "local":
		return "local"
	case "kaggle":
		return "kaggle"
	case "documentai":
		return "documentai"
	default: // "auto" or empty
		if c.KaggleToken != "" {
			return "kaggle"
		}
		return "local"
	}
}

// OCRLanguages returns the EasyOCR language list, following the one-main-language-
// per-country policy: a jurisdiction whose descriptor names a language is locked to
// it; VN, the compiled fallback, leaves the descriptor empty and uses the configured
// value (default "vi"). OCR text is never the binding legal text, so the language
// only needs to match the corpus.
func (c *Config) OCRLanguages() string {
	if l := jurisdiction.For(c.Jurisdiction).OCRLanguages; l != "" {
		return l
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
