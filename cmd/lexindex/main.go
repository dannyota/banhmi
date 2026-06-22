// Command lexindex builds the BM25 sparse lexical index over gold.chunk: it trains
// a BM25 encoder on the corpus (IDF + average length), then writes each chunk's
// document sparse vector into gold.chunk.content_sparse for the pgvector lexical
// retrieval arm (pkg/rag/retrieve). Query-time encoding needs no persisted state
// (hashing trick — see pkg/rag/lexical), so only the document vectors are stored.
//
// Dev tool: run after indexing/embedding to (re)build the lexical index, e.g.
//
//	go run ./cmd/lexindex            # add column if needed, train, populate
//
// pg_search/ParadeDB is unavailable on managed RDS; this is the RDS-portable
// lexical engine (pgvector sparsevec).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/base/db"
	blog "danny.vn/banhmi/pkg/base/log"
	"danny.vn/banhmi/pkg/rag/lexical"
)

func main() {
	cfgPath := flag.String("config", "config/config.yaml", "path to config file")
	batch := flag.Int("batch", 2000, "rows per update batch")
	flag.Parse()

	log := blog.New(os.Getenv("BANHMI_LOG_LEVEL"))
	if err := run(*cfgPath, *batch, log); err != nil {
		log.Error("lexindex", "err", err)
		os.Exit(1)
	}
}

type chunk struct {
	id   int64
	text string
}

func run(cfgPath string, batch int, log *slog.Logger) error {
	ctx := context.Background()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	pool, err := db.NewPool(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("db pool: %w", err)
	}
	defer pool.Close()

	// Ensure the sparsevec column exists (sparsevec ships with pgvector).
	if _, err := pool.Exec(ctx, fmt.Sprintf(
		`ALTER TABLE gold.chunk ADD COLUMN IF NOT EXISTS content_sparse sparsevec(%d)`, lexical.Dim)); err != nil {
		return fmt.Errorf("add content_sparse column: %w", err)
	}

	// Load the corpus (chunk text = content + contextual prefix, matching the
	// document vector the retriever queries against).
	rows, err := pool.Query(ctx, `SELECT id, content, COALESCE(context_prefix,'') FROM gold.chunk ORDER BY id`)
	if err != nil {
		return fmt.Errorf("load chunks: %w", err)
	}
	var chunks []chunk
	var texts []string
	for rows.Next() {
		var c chunk
		var content, prefix string
		if err := rows.Scan(&c.id, &content, &prefix); err != nil {
			rows.Close()
			return fmt.Errorf("scan chunk: %w", err)
		}
		c.text = content + " " + prefix
		chunks = append(chunks, c)
		texts = append(texts, c.text)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate chunks: %w", err)
	}
	log.Info("lexindex: loaded corpus", "chunks", len(chunks))

	enc := lexical.Train(texts)
	log.Info("lexindex: trained BM25 encoder")

	// Write document vectors in batches.
	written := 0
	for start := 0; start < len(chunks); start += batch {
		end := start + batch
		if end > len(chunks) {
			end = len(chunks)
		}
		b := &pgx.Batch{}
		for _, c := range chunks[start:end] {
			b.Queue(`UPDATE gold.chunk SET content_sparse = $1::sparsevec WHERE id = $2`,
				enc.DocVector(c.text), c.id)
		}
		br := pool.SendBatch(ctx, b)
		for range chunks[start:end] {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return fmt.Errorf("update batch at %d: %w", start, err)
			}
		}
		if err := br.Close(); err != nil {
			return fmt.Errorf("close batch at %d: %w", start, err)
		}
		written += end - start
		log.Info("lexindex: progress", "written", written, "total", len(chunks))
	}

	var nonNull int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM gold.chunk WHERE content_sparse IS NOT NULL`).Scan(&nonNull); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	log.Info("lexindex: done", "doc_vectors", nonNull)
	return nil
}
