package lexical

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IndexCorpus trains BM25 on the full chunk corpus and writes sparse vectors
// into gold.chunk.content_sparse. It is the library equivalent of cmd/lexindex.
func IndexCorpus(ctx context.Context, pool *pgxpool.Pool, batchSize int, log *slog.Logger) (int, error) {
	if batchSize <= 0 {
		batchSize = 2000
	}

	if _, err := pool.Exec(ctx, fmt.Sprintf(
		`ALTER TABLE gold.chunk ADD COLUMN IF NOT EXISTS content_sparse sparsevec(%d)`, Dim)); err != nil {
		return 0, fmt.Errorf("add content_sparse column: %w", err)
	}

	rows, err := pool.Query(ctx, `SELECT id, content, COALESCE(context_prefix,'') FROM gold.chunk ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("load chunks: %w", err)
	}
	type chunk struct {
		id   int64
		text string
	}
	var chunks []chunk
	var texts []string
	for rows.Next() {
		var c chunk
		var content, prefix string
		if err := rows.Scan(&c.id, &content, &prefix); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan chunk: %w", err)
		}
		c.text = content + " " + prefix
		chunks = append(chunks, c)
		texts = append(texts, c.text)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate chunks: %w", err)
	}
	if len(chunks) == 0 {
		return 0, nil
	}
	log.Info("lexindex: loaded corpus", "chunks", len(chunks))

	enc := Train(texts)
	log.Info("lexindex: trained BM25 encoder")

	written := 0
	for start := 0; start < len(chunks); start += batchSize {
		end := start + batchSize
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
				return written, fmt.Errorf("update batch at %d: %w", start, err)
			}
		}
		if err := br.Close(); err != nil {
			return written, fmt.Errorf("close batch at %d: %w", start, err)
		}
		written += end - start
		if written%10000 == 0 || written == len(chunks) {
			log.Info("lexindex: progress", "written", written, "total", len(chunks))
		}
	}
	return written, nil
}
