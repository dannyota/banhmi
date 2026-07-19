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

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/base/db"
	"danny.vn/banhmi/pkg/base/jurisdiction"
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

	jur := jurisdiction.For(cfg.Jurisdiction)
	norm := lexical.NormalizerFor(jur.TextNormalizer)
	written, err := lexical.IndexCorpusWith(ctx, pool, batch, log, norm)
	if err != nil {
		return err
	}
	log.Info("lexindex: done", "doc_vectors", written)
	return nil
}
