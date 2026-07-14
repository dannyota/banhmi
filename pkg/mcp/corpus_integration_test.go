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

// seedChainDoc inserts a bare silver.document (+ document-level validity) for
// chain tests and registers cleanup. Deletes cascade validity/relations.
func seedChainDoc(t *testing.T, pool *pgxpool.Pool, docKey, docNumber string) int64 {
	t.Helper()
	ctx := context.Background()
	var docID int64
	err := pool.QueryRow(ctx, `
		INSERT INTO silver.document (doc_key, doc_number, title, created_at, updated_at)
		VALUES ($1, $2, $2, now(), now())
		RETURNING id`, docKey, docNumber).Scan(&docID)
	if err != nil {
		t.Fatalf("seed chain doc %q: %v", docKey, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM silver.document WHERE id = $1`, docID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO silver.validity_period (document_id, status_code, status_class, eff_from, observed_at)
		VALUES ($1, 'TEST', 'in_force', now(), now())`, docID); err != nil {
		t.Fatalf("seed chain validity %q: %v", docKey, err)
	}
	return docID
}

// seedChainEdge inserts fromDocID —relationType→ toDocID (via a doc_ref) and
// registers cleanup.
func seedChainEdge(t *testing.T, pool *pgxpool.Pool, fromDocID, toDocID int64, refKey, relationType string) {
	t.Helper()
	ctx := context.Background()
	var refID int64
	err := pool.QueryRow(ctx, `
		INSERT INTO silver.doc_ref (ref_key, document_id, label, created_at, updated_at)
		VALUES ($1, $2, $1, now(), now())
		RETURNING id`, refKey, toDocID).Scan(&refID)
	if err != nil {
		t.Fatalf("seed doc_ref %q: %v", refKey, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM silver.doc_ref WHERE id = $1`, refID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO silver.document_relation (from_document_id, to_ref_id, relation_type, source)
		VALUES ($1, $2, $3, 'test')`, fromDocID, refID, relationType); err != nil {
		t.Fatalf("seed relation %q: %v", refKey, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM silver.document_relation WHERE from_document_id = $1 AND to_ref_id = $2`, fromDocID, refID)
	})
}

func TestAmendmentChainIntegration(t *testing.T) {
	pool := testCorpusPool(t)
	ctx := context.Background()
	c := dbCorpus{pool: pool}

	// A ← B (amends_supplements), B ← C (replaces): reading A must surface the
	// 2-hop lineage and the amendment_chain gap.
	docA := seedChainDoc(t, pool, "chain-test-a", "01/2001/TT-TEST")
	docB := seedChainDoc(t, pool, "chain-test-b", "02/2002/TT-TEST")
	docC := seedChainDoc(t, pool, "chain-test-c", "03/2003/TT-TEST")
	seedChainEdge(t, pool, docB, docA, "chain-test-ref-a", "amends_supplements")
	seedChainEdge(t, pool, docC, docB, "chain-test-ref-b", "replaces")

	out, err := c.Document(ctx, documentInput{DocumentID: docA})
	if err != nil {
		t.Fatalf("Document A: %v", err)
	}
	if len(out.AmendmentChain) != 2 {
		t.Fatalf("amendment_chain = %+v, want B(depth 1) + C(depth 2)", out.AmendmentChain)
	}
	if out.AmendmentChain[0].Depth != 1 || out.AmendmentChain[0].DocumentID != docB ||
		out.AmendmentChain[0].RelationType != "amends_supplements" {
		t.Errorf("chain[0] = %+v, want doc B at depth 1 via amends_supplements", out.AmendmentChain[0])
	}
	if out.AmendmentChain[1].Depth != 2 || out.AmendmentChain[1].DocumentID != docC ||
		out.AmendmentChain[1].RelationType != "replaces" {
		t.Errorf("chain[1] = %+v, want doc C at depth 2 via replaces", out.AmendmentChain[1])
	}
	if out.AmendmentChain[0].StatusLabel == "" {
		t.Errorf("chain[0].status_label empty, want validity badge")
	}
	if !documentHasGap(out.Gaps, "amendment_chain") {
		t.Errorf("gaps = %+v, want amendment_chain gap", out.Gaps)
	}

	// Reading B: only C amends it (depth 1) — the chain earns no tokens, so it
	// is omitted and no chain gap is emitted.
	outB, err := c.Document(ctx, documentInput{DocumentID: docB})
	if err != nil {
		t.Fatalf("Document B: %v", err)
	}
	if outB.AmendmentChain != nil {
		t.Errorf("B amendment_chain = %+v, want omitted at depth-1-only lineage", outB.AmendmentChain)
	}
	if documentHasGap(outB.Gaps, "amendment_chain") {
		t.Errorf("B gaps = %+v, want no amendment_chain gap", outB.Gaps)
	}

	// The relation on A pointing at amender B must warn that B is itself amended.
	if len(out.Relations) == 0 {
		t.Fatalf("A relations empty, want incoming edge from B")
	}
	var found bool
	for _, rel := range out.Relations {
		if rel.DocumentID == docB {
			found = true
			if len(rel.TargetAmendedBy) != 1 || rel.TargetAmendedBy[0] != "03/2003/TT-TEST" {
				t.Errorf("relation target_amended_by = %v, want [03/2003/TT-TEST]", rel.TargetAmendedBy)
			}
		}
	}
	if !found {
		t.Errorf("relations = %+v, want an edge whose target is doc B", out.Relations)
	}
}

func TestAmendmentChainCycleTerminates(t *testing.T) {
	pool := testCorpusPool(t)
	ctx := context.Background()
	c := dbCorpus{pool: pool}

	// X ← Y and Y ← X (a source data error): the walk must terminate, and the
	// base document must not appear in its own lineage.
	docX := seedChainDoc(t, pool, "chain-cycle-x", "04/2004/TT-TEST")
	docY := seedChainDoc(t, pool, "chain-cycle-y", "05/2005/TT-TEST")
	seedChainEdge(t, pool, docY, docX, "chain-cycle-ref-x", "amends_supplements")
	seedChainEdge(t, pool, docX, docY, "chain-cycle-ref-y", "amends_supplements")

	chain, err := c.amendmentChain(ctx, docX)
	if err != nil {
		t.Fatalf("amendmentChain: %v", err)
	}
	if len(chain) != 1 || chain[0].DocumentID != docY || chain[0].Depth != 1 {
		t.Fatalf("cycle chain = %+v, want only Y at depth 1 (base excluded, walk terminated)", chain)
	}
}
