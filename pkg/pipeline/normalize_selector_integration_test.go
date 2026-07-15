package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
		// Normalize always records the writing source; a NULL source ranks 0 and
		// would let any source reopen the document (reopen-on-better-source).
		if _, err := pool.Exec(ctx,
			`INSERT INTO silver.validity_period (document_id, status_code, status_class, source, observed_at)
			 VALUES ($1, '', 'unknown', $2, now())`, docID, testSource); err != nil {
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

// normSelDB bundles the seeding helpers the multi-source selector tests share.
// Rows are keyed 'test-normsel-%' so one cleanup covers every test; fetch_doc
// rows use real source names (sbv_hanoi, vbpl) because the selector's priority
// gate ranks by source name, and are deleted by external_id pattern.
type normSelDB struct {
	t    *testing.T
	ctx  context.Context
	pool *pgxpool.Pool
}

func newNormSelDB(t *testing.T) (normSelDB, context.Context) {
	t.Helper()
	pool := normalizeValidationPool(t) // skips if BANHMI_DATABASE_PASSWORD unset / DB unreachable
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// Deleting the document cascades to alias/section/text/validity.
		_, _ = pool.Exec(c, `DELETE FROM silver.document WHERE doc_key LIKE 'test-normsel-%'`)
		_, _ = pool.Exec(c, `DELETE FROM ingest.fetch_doc WHERE external_id LIKE 'test-normsel-%'`)
	})
	return normSelDB{t: t, ctx: ctx, pool: pool}, ctx
}

func (h normSelDB) insertDocument(key string) int64 {
	h.t.Helper()
	var docID int64
	if err := h.pool.QueryRow(h.ctx,
		`INSERT INTO silver.document (doc_key, doc_number, created_at, updated_at)
		 VALUES ($1, $1, now(), now()) RETURNING id`, key).Scan(&docID); err != nil {
		h.t.Fatalf("insert document %s: %v", key, err)
	}
	return docID
}

func (h normSelDB) insertFetchDoc(source, externalID, state string) int64 {
	h.t.Helper()
	var fetchID int64
	if err := h.pool.QueryRow(h.ctx,
		`INSERT INTO ingest.fetch_doc (source, external_id, state, in_scope, discovered_at, updated_at)
		 VALUES ($1, $2, $3, true, now(), now()) RETURNING id`, source, externalID, state).Scan(&fetchID); err != nil {
		h.t.Fatalf("insert fetch_doc %s/%s: %v", source, externalID, err)
	}
	return fetchID
}

func (h normSelDB) setFetchState(fetchID int64, state string) {
	h.t.Helper()
	if _, err := h.pool.Exec(h.ctx,
		`UPDATE ingest.fetch_doc SET state = $2, updated_at = now() WHERE id = $1`, fetchID, state); err != nil {
		h.t.Fatalf("set fetch_doc %d state %s: %v", fetchID, state, err)
	}
}

func (h normSelDB) linkAlias(source, externalID string, docID int64) {
	h.t.Helper()
	if _, err := h.pool.Exec(h.ctx,
		`INSERT INTO silver.document_alias (source, external_id, document_id) VALUES ($1, $2, $3)`,
		source, externalID, docID); err != nil {
		h.t.Fatalf("insert alias %s/%s: %v", source, externalID, err)
	}
}

// insertValidity writes a doc-level validity row; open=false pre-supersedes it,
// simulating a row a later normalize replaced.
func (h normSelDB) insertValidity(docID int64, source, statusCode, statusClass string, open bool) {
	h.t.Helper()
	supersededAt := "NULL"
	if !open {
		supersededAt = "now()"
	}
	if _, err := h.pool.Exec(h.ctx,
		`INSERT INTO silver.validity_period (document_id, status_code, status_class, source, observed_at, superseded_at)
		 VALUES ($1, $2, $3, $4, now(), `+supersededAt+`)`,
		docID, statusCode, statusClass, source); err != nil {
		h.t.Fatalf("insert validity doc=%d source=%s: %v", docID, source, err)
	}
}

func (h normSelDB) insertText(docID int64) {
	h.t.Helper()
	if _, err := h.pool.Exec(h.ctx,
		`INSERT INTO silver.document_text (document_id, authority, markdown, created_at, updated_at)
		 VALUES ($1, 'transcription_html', 'Điều 1. Nội dung.', now(), now())`, docID); err != nil {
		h.t.Fatalf("insert text %d: %v", docID, err)
	}
}

func (h normSelDB) insertSection(docID int64) {
	h.t.Helper()
	if _, err := h.pool.Exec(h.ctx,
		`INSERT INTO silver.document_section (document_id, kind, ordinal, citation_path)
		 VALUES ($1, 'dieu', 1, 'Điều 1')`, docID); err != nil {
		h.t.Fatalf("insert section %d: %v", docID, err)
	}
}

func (h normSelDB) selectIDs(afterID int64, limit int32, force bool) []int64 {
	h.t.Helper()
	a := &Activities{dbpool: h.pool}
	ids, err := a.ListFetchDocIDsNeedingNormalizeAfter(h.ctx, ListStageFetchDocIDsAfterParams{
		AfterID: afterID, Limit: limit, Force: force,
	})
	if err != nil {
		h.t.Fatalf("selector (after=%d force=%v): %v", afterID, force, err)
	}
	return ids
}

func (h normSelDB) selectSet(afterID int64, force bool) map[int64]bool {
	h.t.Helper()
	set := make(map[int64]bool)
	for _, id := range h.selectIDs(afterID, 1_000_000, force) {
		set[id] = true
	}
	return set
}

// TestNormalizeSelectorPrefersAuthoritativeSource is the shadowing regression
// test: one document discovered by two sources — a statusless low-priority
// source (sbv_hanoi) holding the LOWER fetch_doc id, and vbpl (priority 10,
// carrying status HHL1P) holding the HIGHER id. The old selector picked the
// lowest fetch_doc id per document, so sbv_hanoi normalized the doc and sealed
// it with unknown validity while vbpl's bronze row was never consumed. The
// selector must now hand the document to vbpl.
//
// The test drives the selector exactly like the paginated cmd/pipeline driver
// (LIMIT 1, afterID advanced to the last returned id) and simulates the
// normalize outcome of the returned fetch_doc by inserting the validity/section
// rows normalize would write — the selector's decisions are the unit under test.
// The cursor starts just below the seeded rows so rows already in a shared dev
// DB stay out of the drive.
func TestNormalizeSelectorPrefersAuthoritativeSource(t *testing.T) {
	h, _ := newNormSelDB(t)

	const key = "test-normsel-prio-doc"
	docID := h.insertDocument(key)
	lowID := h.insertFetchDoc("sbv_hanoi", key, "complete")
	vbplID := h.insertFetchDoc("vbpl", key, "complete")
	if lowID >= vbplID {
		t.Fatalf("seed broken: low-priority fetch_doc id %d must be lower than vbpl's %d", lowID, vbplID)
	}
	h.linkAlias("sbv_hanoi", key, docID)
	h.linkAlias("vbpl", key, docID)

	normalizedVbpl := false
	afterID := lowID - 1
	for range 10 { // bounded page loop; the doc must resolve well within this
		ids := h.selectIDs(afterID, 1, false)
		if len(ids) == 0 {
			break
		}
		for _, id := range ids {
			switch id {
			case lowID:
				t.Fatalf("selector returned low-priority fetch_doc %d; vbpl fetch_doc %d must win the pick", lowID, vbplID)
			case vbplID:
				// Simulate vbpl's normalize outcome: real status + sections.
				h.insertValidity(docID, "vbpl", "HHL1P", "partial", true)
				h.insertText(docID)
				h.insertSection(docID)
				normalizedVbpl = true
			default:
				// Unrelated row in a shared dev DB — advance past it untouched.
			}
		}
		afterID = ids[len(ids)-1]
	}
	if !normalizedVbpl {
		t.Fatal("paginated drive never returned the vbpl fetch_doc")
	}

	// End state: the open doc-level validity row is vbpl's...
	var src string
	if err := h.pool.QueryRow(h.ctx,
		`SELECT COALESCE(source, '') FROM silver.validity_period
		 WHERE document_id = $1 AND section_id IS NULL AND superseded_at IS NULL`, docID).Scan(&src); err != nil {
		t.Fatalf("read open validity row: %v", err)
	}
	if src != "vbpl" {
		t.Errorf("open validity row source = %q, want vbpl", src)
	}
	// ...and the document is sealed: a fresh pass returns neither fetch_doc.
	if got := h.selectSet(lowID-1, false); got[lowID] || got[vbplID] {
		t.Errorf("document re-selected after vbpl normalized (low=%v vbpl=%v) — gate did not seal", got[lowID], got[vbplID])
	}
}

// TestNormalizeSelectorReopensForBetterSource covers reopen-on-better-source:
// a document already normalized and sealed by the low-priority source (open
// validity row source=sbv_hanoi, sections exist) must become eligible again —
// without Force — the moment its vbpl fetch_doc completes, and only via the
// vbpl fetch_doc (sbv_hanoi does not strictly outrank itself).
func TestNormalizeSelectorReopensForBetterSource(t *testing.T) {
	h, _ := newNormSelDB(t)

	const key = "test-normsel-reopen-doc"
	docID := h.insertDocument(key)
	lowID := h.insertFetchDoc("sbv_hanoi", key, "complete")
	h.linkAlias("sbv_hanoi", key, docID)
	// Sealed by the low-priority source: statusless validity + text + sections.
	h.insertValidity(docID, "sbv_hanoi", "", "unknown", true)
	h.insertText(docID)
	h.insertSection(docID)
	// vbpl discovered the document, but its fetch has not completed yet.
	vbplID := h.insertFetchDoc("vbpl", key, "fetching")
	h.linkAlias("vbpl", key, docID)

	if pre := h.selectSet(lowID-1, false); pre[lowID] || pre[vbplID] {
		t.Fatalf("sealed document selected before the vbpl fetch completed (low=%v vbpl=%v)", pre[lowID], pre[vbplID])
	}

	h.setFetchState(vbplID, "complete")
	got := h.selectSet(lowID-1, false)
	if !got[vbplID] {
		t.Error("vbpl fetch_doc not selected after completing — reopen-on-better-source regressed")
	}
	if got[lowID] {
		t.Error("low-priority fetch_doc selected — only a strictly better source may reopen a sealed document")
	}
}

// TestNormalizeSelectorSealsAfterAuthoritativeSource asserts the terminal
// state: after vbpl has normalized the document (its validity row is the open
// one; the low-priority row is superseded), no fetch_doc of the document is
// selected without Force — the reopen gate does not loop on the authoritative
// source's own row. Force still selects the document, and picks the
// authoritative fetch_doc.
func TestNormalizeSelectorSealsAfterAuthoritativeSource(t *testing.T) {
	h, _ := newNormSelDB(t)

	const key = "test-normsel-seal-doc"
	docID := h.insertDocument(key)
	lowID := h.insertFetchDoc("sbv_hanoi", key, "complete")
	vbplID := h.insertFetchDoc("vbpl", key, "complete")
	h.linkAlias("sbv_hanoi", key, docID)
	h.linkAlias("vbpl", key, docID)
	h.insertValidity(docID, "sbv_hanoi", "", "unknown", false) // superseded
	h.insertValidity(docID, "vbpl", "HHL1P", "partial", true)  // current
	h.insertText(docID)
	h.insertSection(docID)

	if got := h.selectSet(lowID-1, false); got[lowID] || got[vbplID] {
		t.Errorf("normalized document selected without force (low=%v vbpl=%v) — gate did not seal", got[lowID], got[vbplID])
	}
	forced := h.selectSet(lowID-1, true)
	if !forced[vbplID] {
		t.Error("force=true did not select the document")
	}
	if forced[lowID] {
		t.Error("force=true returned the low-priority fetch_doc — the per-document pick must prefer vbpl")
	}
}
