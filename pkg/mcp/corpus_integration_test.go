package mcp

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/rag/retrieve"
)

func testCorpusPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("BANHMI_DATABASE_PASSWORD") == "" {
		t.Skip("BANHMI_DATABASE_PASSWORD not set; skipping MCP corpus DB integration test")
	}
	cfg := config.Default()
	cfg.Database.Host = "localhost"
	cfg.Database.Port = 10001
	cfg.Database.Password = os.Getenv("BANHMI_DATABASE_PASSWORD")

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.Database.DSN())
	if err != nil {
		t.Skipf("cannot create pool (DB unavailable?): %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("cannot ping DB, skipping integration test: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestDBCorpusStatusIntegration(t *testing.T) {
	pool := testCorpusPool(t)

	out, err := (dbCorpus{pool: pool}).CorpusStatus(context.Background())
	if err != nil {
		t.Fatalf("CorpusStatus: %v", err)
	}
	if out.Docs.Total == 0 {
		t.Fatal("Docs.Total = 0, want local corpus rows")
	}
	if out.Chunks.Total > 0 && !out.SearchReady {
		t.Fatalf("SearchReady = false with %d chunks", out.Chunks.Total)
	}

	gaps, err := (dbCorpus{pool: pool}).QualityGaps(context.Background(), qualityGapsInput{Category: qualityCategoryFetch, Limit: 5})
	if err != nil {
		t.Fatalf("QualityGaps: %v", err)
	}
	if gaps.Limit != 5 || len(gaps.Categories) != 1 || gaps.Categories[0] != qualityCategoryFetch {
		t.Fatalf("quality gap shape = %+v, want fetch category with requested limit", gaps)
	}
}

func TestDBCorpusDocumentIntegration(t *testing.T) {
	pool := testCorpusPool(t)
	ctx := context.Background()

	var docID int64
	err := pool.QueryRow(ctx, `
SELECT d.id
FROM silver.document d
WHERE EXISTS (SELECT 1 FROM gold.chunk c WHERE c.document_id=d.id)
ORDER BY d.id
LIMIT 1`).Scan(&docID)
	if err != nil {
		if err == pgx.ErrNoRows {
			t.Skip("no indexed documents in local corpus")
		}
		t.Fatalf("select indexed document: %v", err)
	}

	out, err := (dbCorpus{pool: pool}).Document(ctx, documentInput{DocumentID: docID, Limit: 2})
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	if !out.Found || out.Document.DocumentID != docID {
		t.Fatalf("document output = %+v, want selected doc %d", out, docID)
	}
	if len(out.Chunks) == 0 {
		t.Fatalf("document %d returned no chunks", docID)
	}

	miss, err := (dbCorpus{pool: pool}).Document(ctx, documentInput{
		DocumentID: docID,
		Citation:   "definitely-not-a-real-citation",
		Limit:      2,
	})
	if err != nil {
		t.Fatalf("Document citation miss: %v", err)
	}
	if !miss.Found || len(miss.Chunks) != 0 {
		t.Fatalf("citation miss = %+v, want found document with no chunks", miss)
	}
	if !documentHasGap(miss.Gaps, string(retrieve.GapNoEvidence)) {
		t.Fatalf("citation miss gaps = %+v, want no_evidence gap", miss.Gaps)
	}
}

func documentHasGap(gaps []gap, kind string) bool {
	for _, gap := range gaps {
		if gap.Kind == kind {
			return true
		}
	}
	return false
}
