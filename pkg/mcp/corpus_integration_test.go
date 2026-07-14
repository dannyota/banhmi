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

	// Run EVERY category: each maps to its own SQL, and a parse-time error
	// (e.g. an ambiguous column after a schema change) only surfaces when that
	// category's query actually executes — "all" is what agents call.
	for _, cat := range []string{
		qualityCategoryAll,
		qualityCategoryNonBinding,
		qualityCategoryMojibake,
		qualityCategoryPartialValidity,
		qualityCategoryUnresolvedRelation,
		qualityCategoryRelationTargetText,
	} {
		if _, err := (dbCorpus{pool: pool}).QualityGaps(context.Background(), qualityGapsInput{Category: cat, Limit: 3}); err != nil {
			t.Errorf("QualityGaps(%s): %v", cat, err)
		}
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

	// The default include set carries every section except the per-artifact
	// provenance rows.
	if len(out.TextProvenance) != 0 {
		t.Fatalf("default include returned %d provenance rows, want none (opt-in)", len(out.TextProvenance))
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

	// include=['chunks'] returns the text and skips every other section.
	chunksOnly, err := (dbCorpus{pool: pool}).Document(ctx, documentInput{
		DocumentID: docID,
		Include:    []string{"chunks"},
		Limit:      2,
	})
	if err != nil {
		t.Fatalf("Document include=chunks: %v", err)
	}
	if !chunksOnly.Found || len(chunksOnly.Chunks) == 0 {
		t.Fatalf("include=chunks = %+v, want the document's chunks", chunksOnly)
	}
	if chunksOnly.Relations != nil || chunksOnly.IncomingAmendments != nil || chunksOnly.Timeline != nil || chunksOnly.TextProvenance != nil {
		t.Fatalf("include=chunks returned extra sections: relations=%d amendments=%d timeline=%d provenance=%d",
			len(chunksOnly.Relations), len(chunksOnly.IncomingAmendments), len(chunksOnly.Timeline), len(chunksOnly.TextProvenance))
	}
	if !chunksOnly.TextSummary.HasBindingText && !chunksOnly.TextSummary.HasNonBindingText {
		t.Fatalf("include=chunks text_summary = %+v, want the always-on provenance summary", chunksOnly.TextSummary)
	}

	// include=['provenance'] restores the per-artifact rows and skips the text.
	provOnly, err := (dbCorpus{pool: pool}).Document(ctx, documentInput{
		DocumentID: docID,
		Include:    []string{"provenance"},
		Limit:      2,
	})
	if err != nil {
		t.Fatalf("Document include=provenance: %v", err)
	}
	if len(provOnly.TextProvenance) == 0 {
		t.Fatalf("include=provenance returned no per-artifact rows for indexed doc %d", docID)
	}
	if len(provOnly.Chunks) != 0 {
		t.Fatalf("include=provenance returned %d chunks, want none", len(provOnly.Chunks))
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
