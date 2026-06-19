package pipeline

import (
	"context"
	"testing"
	"time"
)

// TestNormalizeSelectorReSelectsOCRDocs is an opt-in DB integration test
// (BANHMI_DATABASE_PASSWORD must be set; it skips cleanly without a local DB) for
// the normalize candidate selector, ListFetchDocIDsNeedingNormalizeAfter.
//
// It guards the fix for the OCR→normalize handoff: a scan normalized as textless
// during the pre-OCR drain still gets a document-level validity_period (status
// unknown), so the original "has no validity_period" check treated it as done and
// never re-normalized it once OcrAll wrote the OCR text. The selector must also
// re-select a doc that has non-empty document_text but no document_section, and it
// must stop selecting it once sections exist (no re-select loop).
func TestNormalizeSelectorReSelectsOCRDocs(t *testing.T) {
	pool := normalizeValidationPool(t) // skips if BANHMI_DATABASE_PASSWORD unset / DB unreachable
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const testSource = "test_normalize_selector"

	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// Deleting the document cascades to alias/section/text/validity (all
		// ON DELETE CASCADE); fetch_doc is independent.
		_, _ = pool.Exec(c, `DELETE FROM silver.document WHERE doc_key LIKE 'test-normsel-%'`)
		_, _ = pool.Exec(c, `DELETE FROM ingest.fetch_doc WHERE source = $1`, testSource)
	})

	// insertDoc creates a document + a complete, in-scope fetch_doc + the alias that
	// links them, and returns the fetch_doc id (what the selector yields).
	insertDoc := func(key string) (fetchID, docID int64) {
		t.Helper()
		if err := pool.QueryRow(ctx,
			`INSERT INTO silver.document (doc_key, doc_number, created_at, updated_at)
			 VALUES ($1, $1, now(), now()) RETURNING id`, key).Scan(&docID); err != nil {
			t.Fatalf("insert document %s: %v", key, err)
		}
		if err := pool.QueryRow(ctx,
			`INSERT INTO ingest.fetch_doc (source, external_id, state, in_scope, discovered_at, updated_at)
			 VALUES ($1, $2, 'complete', true, now(), now()) RETURNING id`, testSource, key).Scan(&fetchID); err != nil {
			t.Fatalf("insert fetch_doc %s: %v", key, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO silver.document_alias (source, external_id, document_id) VALUES ($1, $2, $3)`,
			testSource, key, docID); err != nil {
			t.Fatalf("insert alias %s: %v", key, err)
		}
		return fetchID, docID
	}
	addDocValidity := func(docID int64) { // doc-level row a textless normalize leaves behind
		if _, err := pool.Exec(ctx,
			`INSERT INTO silver.validity_period (document_id, status_code, status_class, observed_at)
			 VALUES ($1, '', 'unknown', now())`, docID); err != nil {
			t.Fatalf("insert validity %d: %v", docID, err)
		}
	}
	addText := func(docID int64) {
		if _, err := pool.Exec(ctx,
			`INSERT INTO silver.document_text (document_id, authority, markdown, created_at, updated_at)
			 VALUES ($1, 'ocr_extractive', 'Điều 1. Nội dung OCR.', now(), now())`, docID); err != nil {
			t.Fatalf("insert text %d: %v", docID, err)
		}
	}
	addSection := func(docID int64) {
		if _, err := pool.Exec(ctx,
			`INSERT INTO silver.document_section (document_id, kind, ordinal, citation_path)
			 VALUES ($1, 'dieu', 1, 'Điều 1')`, docID); err != nil {
			t.Fatalf("insert section %d: %v", docID, err)
		}
	}

	// A: the OCR case — doc-level validity_period + text + NO section → must be selected.
	fetchA, docA := insertDoc("test-normsel-A")
	addDocValidity(docA)
	addText(docA)
	// B: fully normalized — validity_period + text + sections → must NOT be selected.
	fetchB, docB := insertDoc("test-normsel-B")
	addDocValidity(docB)
	addText(docB)
	addSection(docB)
	// C: never normalized — no validity_period → must be selected.
	fetchC, _ := insertDoc("test-normsel-C")

	a := &Activities{dbpool: pool}
	sel := func(force bool) map[int64]bool {
		t.Helper()
		ids, err := a.ListFetchDocIDsNeedingNormalizeAfter(ctx, ListStageFetchDocIDsAfterParams{
			AfterID: 0, Limit: 1_000_000, Force: force,
		})
		if err != nil {
			t.Fatalf("selector (force=%v): %v", force, err)
		}
		set := make(map[int64]bool, len(ids))
		for _, id := range ids {
			set[id] = true
		}
		return set
	}

	got := sel(false)
	if !got[fetchA] {
		t.Error("doc A (OCR text, no sections) was not selected — the OCR→normalize re-select fix regressed")
	}
	if got[fetchB] {
		t.Error("doc B (already has sections) was selected — should be skipped (would re-select loop)")
	}
	if !got[fetchC] {
		t.Error("doc C (never normalized) was not selected")
	}

	// Force re-selects everything in scope, including the sectioned doc B.
	if forced := sel(true); !forced[fetchB] {
		t.Error("force=true did not select doc B")
	}
}
