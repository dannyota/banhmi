// Command embed-backfill fills missing gold.chunk_embedding rows without
// re-chunking documents. It is a maintenance/evaluation tool: Index writes chunks
// first and embeddings best-effort; this command later makes vector coverage fair
// enough to compare BM25, vector, and hybrid retrieval modes.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	pgvector "github.com/pgvector/pgvector-go"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/base/db"
	blog "danny.vn/banhmi/pkg/base/log"
	"danny.vn/banhmi/pkg/rag/embed"
	"danny.vn/banhmi/pkg/rag/embed/kagglebatch"
	dbgold "danny.vn/banhmi/pkg/store/gold"
)

const defaultBatchSize = 8

type opts struct {
	cfgPath       string
	batch         int
	limit         int
	progressEvery int
	dryRun        bool
	force         bool
}

type chunk struct {
	id   int64
	text string
}

func main() {
	var o opts
	flag.StringVar(&o.cfgPath, "config", "config/config.yaml", "path to config file")
	flag.IntVar(&o.batch, "batch", defaultBatchSize, "embedding request batch size")
	flag.IntVar(&o.limit, "limit", 0, "maximum chunks to backfill (0 = all missing)")
	flag.IntVar(&o.progressEvery, "progress-every", 25, "log progress after this many batches (0 = final only)")
	flag.BoolVar(&o.dryRun, "dry-run", false, "report missing coverage without writing embeddings")
	flag.BoolVar(&o.force, "force", false, "re-embed ALL chunks (overwrite), not just the missing ones")
	flag.Parse()

	log := blog.New(os.Getenv("BANHMI_LOG_LEVEL"))
	if err := run(context.Background(), o, log); err != nil {
		log.Error("embed backfill", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, o opts, log *slog.Logger) error {
	if o.batch <= 0 {
		return errors.New("batch must be positive")
	}
	if o.limit < 0 {
		return errors.New("limit must be non-negative")
	}
	if o.progressEvery < 0 {
		return errors.New("progress-every must be non-negative")
	}

	cfg, err := config.Load(o.cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	endpoint := cfg.EmbedEndpoint()
	model := config.EmbedModel
	dims := config.EmbedDims
	dims32, err := checkedInt32(dims)
	if err != nil {
		return err
	}

	pool, err := db.NewPool(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("database pool: %w", err)
	}
	defer pool.Close()

	missing, err := countMissing(ctx, pool, model, dims)
	if err != nil {
		return err
	}
	log.Info("embedding coverage",
		"model", model, "dims", dims, "missing", missing, "limit", o.limit)
	if o.dryRun {
		return nil
	}

	gold := dbgold.New(pool)

	// -force re-embeds every chunk (overwrite), regardless of current coverage.
	if o.force {
		return runForceReindex(ctx, o, cfg, pool, gold, endpoint, model, dims, dims32, log)
	}
	if missing == 0 {
		return nil
	}

	// With the Kaggle engine and a large-enough batch, embed in one GPU job
	// instead of the synchronous local OVMS loop. Small batches stay local —
	// a Kaggle cold start is not worth it for a handful of chunks.
	if cfg.EmbedEngine() == "kaggle" && int(missing) >= cfg.Embed.Kaggle.MinBatch {
		return runKaggle(ctx, o, cfg, pool, gold, model, dims, dims32, log)
	}

	embedder := embed.New(endpoint, model, dims, "")

	remaining := o.limit
	written := 0
	batches := 0
	started := time.Now()
	for {
		batchLimit := o.batch
		if remaining > 0 && remaining < batchLimit {
			batchLimit = remaining
		}

		chunks, err := loadMissing(ctx, pool, model, dims, batchLimit)
		if err != nil {
			return err
		}
		if len(chunks) == 0 {
			break
		}

		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.text
		}
		vecs, err := embedder.Embed(ctx, texts)
		if err != nil {
			return fmt.Errorf("embed batch starting chunk %d: %w", chunks[0].id, err)
		}
		if len(vecs) != len(chunks) {
			return fmt.Errorf("embedder returned %d vectors for %d chunks", len(vecs), len(chunks))
		}

		for i, c := range chunks {
			if len(vecs[i]) != dims {
				return fmt.Errorf("chunk %d vector dims = %d, want %d", c.id, len(vecs[i]), dims)
			}
			if _, err := gold.UpsertChunkEmbedding(ctx, dbgold.UpsertChunkEmbeddingParams{
				ChunkID:   c.id,
				Model:     model,
				Dims:      dims32,
				Embedding: pgvector.NewVector(vecs[i]),
			}); err != nil {
				return fmt.Errorf("upsert embedding chunk %d: %w", c.id, err)
			}
			written++
		}

		batches++
		if remaining > 0 {
			remaining -= len(chunks)
			if remaining == 0 {
				break
			}
		}
		if o.progressEvery > 0 && batches%o.progressEvery == 0 {
			nowMissing, err := countMissing(ctx, pool, model, dims)
			if err != nil {
				return err
			}
			log.Info("embedding backfill progress",
				"written", written,
				"batches", batches,
				"missing", nowMissing,
				"chunks_per_second", fmt.Sprintf("%.2f", rate(written, started)))
		}
	}

	missing, err = countMissing(ctx, pool, model, dims)
	if err != nil {
		return err
	}
	log.Info("embedding backfill complete",
		"written", written,
		"missing", missing,
		"chunks_per_second", fmt.Sprintf("%.2f", rate(written, started)))
	return nil
}

// runKaggle embeds all MISSING chunks in a single Kaggle GPU batch job.
func runKaggle(ctx context.Context, o opts, cfg *config.Config, pool dbgold.DBTX, gold *dbgold.Queries, model string, dims int, dims32 int32, log *slog.Logger) error {
	limit := o.limit
	if limit <= 0 {
		limit = 1<<31 - 1
	}
	chunks, err := loadMissing(ctx, pool, model, dims, limit)
	if err != nil {
		return err
	}
	if len(chunks) == 0 {
		return nil
	}
	return embedKaggleAndStore(ctx, cfg, gold, chunks, model, dims, dims32, log)
}

// runForceReindex re-embeds EVERY chunk (overwrite) via the selected engine.
func runForceReindex(ctx context.Context, o opts, cfg *config.Config, pool dbgold.DBTX, gold *dbgold.Queries, endpoint, model string, dims int, dims32 int32, log *slog.Logger) error {
	limit := o.limit
	if limit <= 0 {
		limit = 1<<31 - 1
	}
	chunks, err := loadAllChunks(ctx, pool, limit)
	if err != nil {
		return err
	}
	if len(chunks) == 0 {
		return nil
	}
	log.Info("force re-embedding all chunks", "count", len(chunks), "engine", cfg.EmbedEngine())
	if cfg.EmbedEngine() == "kaggle" {
		return embedKaggleAndStore(ctx, cfg, gold, chunks, model, dims, dims32, log)
	}

	embedder := embed.New(endpoint, model, dims, "")
	started := time.Now()
	written := 0
	for i := 0; i < len(chunks); i += o.batch {
		batch := chunks[i:min(i+o.batch, len(chunks))]
		texts := make([]string, len(batch))
		for j, c := range batch {
			texts[j] = c.text
		}
		vecs, err := embedder.Embed(ctx, texts)
		if err != nil {
			return fmt.Errorf("embed batch starting chunk %d: %w", batch[0].id, err)
		}
		if len(vecs) != len(batch) {
			return fmt.Errorf("embedder returned %d vectors for %d chunks", len(vecs), len(batch))
		}
		if err := upsertVectors(ctx, gold, batch, vecs, model, dims, dims32); err != nil {
			return err
		}
		written += len(batch)
	}
	log.Info("force reindex complete (local)",
		"written", written, "chunks_per_second", fmt.Sprintf("%.2f", rate(written, started)))
	return nil
}

// embedKaggleAndStore embeds the given chunks in one Kaggle GPU job and upserts
// the vectors under the canonical model tag (config.EmbedModel), so retrieval —
// which filters by that tag — finds them regardless of which engine produced them.
func embedKaggleAndStore(ctx context.Context, cfg *config.Config, gold *dbgold.Queries, chunks []chunk, model string, dims int, dims32 int32, log *slog.Logger) error {
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.text
	}
	log.Info("embedding via Kaggle batch engine",
		"owner", cfg.Embed.Kaggle.Owner, "accelerator", cfg.Embed.Kaggle.Accelerator, "chunks", len(chunks))
	be, err := kagglebatch.New(kagglebatch.Options{
		Owner:        cfg.Embed.Kaggle.Owner,
		ModelDataset: cfg.Embed.Kaggle.ModelDataset,
		Accelerator:  cfg.Embed.Kaggle.Accelerator,
		Dims:         dims,
		Token:        cfg.KaggleToken,
	}, log)
	if err != nil {
		return fmt.Errorf("kaggle embedder: %w", err)
	}

	started := time.Now()
	vecs, err := be.EmbedAll(ctx, texts)
	if err != nil {
		return fmt.Errorf("kaggle embed %d chunks: %w", len(texts), err)
	}
	if len(vecs) != len(chunks) {
		return fmt.Errorf("kaggle returned %d vectors for %d chunks", len(vecs), len(chunks))
	}
	if err := upsertVectors(ctx, gold, chunks, vecs, model, dims, dims32); err != nil {
		return err
	}
	log.Info("kaggle embedding complete",
		"written", len(chunks), "chunks_per_second", fmt.Sprintf("%.2f", rate(len(chunks), started)))
	return nil
}

// upsertVectors writes one embedding per chunk under the given model tag.
func upsertVectors(ctx context.Context, gold *dbgold.Queries, chunks []chunk, vecs [][]float32, model string, dims int, dims32 int32) error {
	for i, c := range chunks {
		if len(vecs[i]) != dims {
			return fmt.Errorf("chunk %d vector dims = %d, want %d", c.id, len(vecs[i]), dims)
		}
		if _, err := gold.UpsertChunkEmbedding(ctx, dbgold.UpsertChunkEmbeddingParams{
			ChunkID:   c.id,
			Model:     model,
			Dims:      dims32,
			Embedding: pgvector.NewVector(vecs[i]),
		}); err != nil {
			return fmt.Errorf("upsert embedding chunk %d: %w", c.id, err)
		}
	}
	return nil
}

// loadAllChunks loads every chunk's id and embedding text (context prefix +
// content), ordered by id, up to limit. Used by -force.
func loadAllChunks(ctx context.Context, q dbgold.DBTX, limit int) ([]chunk, error) {
	const sql = `
SELECT c.id, COALESCE(c.context_prefix, ''), c.content
FROM gold.chunk c
ORDER BY c.id
LIMIT $1`
	rows, err := q.Query(ctx, sql, limit)
	if err != nil {
		return nil, fmt.Errorf("list all chunks: %w", err)
	}
	defer rows.Close()

	var out []chunk
	for rows.Next() {
		var id int64
		var prefix, content string
		if err := rows.Scan(&id, &prefix, &content); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		text := strings.TrimSpace(strings.Join([]string{strings.TrimSpace(prefix), strings.TrimSpace(content)}, "\n"))
		if text == "" {
			return nil, fmt.Errorf("chunk %d has empty embedding text", id)
		}
		out = append(out, chunk{id: id, text: text})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chunks: %w", err)
	}
	return out, nil
}

func checkedInt32(v int) (int32, error) {
	if v <= 0 || v > 1<<31-1 {
		return 0, fmt.Errorf("embedding dims %d outside int32 range", v)
	}
	return int32(v), nil //nolint:gosec // bounds checked above for SQL int4.
}

func rate(written int, started time.Time) float64 {
	elapsed := time.Since(started).Seconds()
	if elapsed == 0 {
		return 0
	}
	return float64(written) / elapsed
}

func countMissing(ctx context.Context, q dbgold.DBTX, model string, dims int) (int64, error) {
	const sql = `
SELECT count(*)
FROM gold.chunk c
LEFT JOIN gold.chunk_embedding e
  ON e.chunk_id = c.id AND e.model = $1 AND e.dims = $2
WHERE e.id IS NULL`
	var missing int64
	if err := q.QueryRow(ctx, sql, model, dims).Scan(&missing); err != nil {
		return 0, fmt.Errorf("count missing embeddings: %w", err)
	}
	return missing, nil
}

func loadMissing(ctx context.Context, q dbgold.DBTX, model string, dims, limit int) ([]chunk, error) {
	const sql = `
SELECT c.id, COALESCE(c.context_prefix, ''), c.content
FROM gold.chunk c
LEFT JOIN gold.chunk_embedding e
  ON e.chunk_id = c.id AND e.model = $1 AND e.dims = $2
WHERE e.id IS NULL
ORDER BY c.id
LIMIT $3`
	rows, err := q.Query(ctx, sql, model, dims, limit)
	if err != nil {
		return nil, fmt.Errorf("list missing embeddings: %w", err)
	}
	defer rows.Close()

	var out []chunk
	for rows.Next() {
		var id int64
		var prefix, content string
		if err := rows.Scan(&id, &prefix, &content); err != nil {
			return nil, fmt.Errorf("scan missing chunk: %w", err)
		}
		text := strings.TrimSpace(strings.Join([]string{strings.TrimSpace(prefix), strings.TrimSpace(content)}, "\n"))
		if text == "" {
			return nil, fmt.Errorf("chunk %d has empty embedding text", id)
		}
		out = append(out, chunk{id: id, text: text})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate missing chunks: %w", err)
	}
	return out, nil
}
