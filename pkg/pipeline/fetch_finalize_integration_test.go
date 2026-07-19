package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	dbingest "danny.vn/banhmi/pkg/store/ingest"
)

// TestFinalizeDocTerminalStates is an opt-in DB integration test
// (BANHMI_DATABASE_PASSWORD must be set; it skips cleanly without a local DB)
// for finalizeDoc / MarkDocCompleteIfDone terminal-state semantics.
//
// It guards the dead-letter completion fix: a document whose artifacts are all
// resolved but some dead-lettered (attempts exhausted, e.g. a permanently-404
// PDF) must reach the terminal 'partial' state — visible to extract/normalize
// with whatever artifacts it has — instead of sitting in 'fetching' forever.
// A transiently-failed artifact (state='error', attempts < max) must keep the
// doc retryable (NOT terminal), even when a sibling artifact is already dead.
func TestFinalizeDocTerminalStates(t *testing.T) {
	pool := normalizeValidationPool(t) // skips if BANHMI_DATABASE_PASSWORD unset / DB unreachable
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const testSource = "test_finalize"
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// fetch_artifact rows cascade from fetch_doc.
		_, _ = pool.Exec(c, `DELETE FROM ingest.fetch_doc WHERE source = $1`, testSource)
	})

	h := finalizeDB{t: t, ctx: ctx, pool: pool}
	a := &Activities{ledger: dbingest.New(pool)}
	now := time.Now().UTC()

	finalize := func(docID int64) string {
		t.Helper()
		state, err := a.finalizeDoc(ctx, docID, now)
		if err != nil {
			t.Fatalf("finalizeDoc(%d): %v", docID, err)
		}
		return state
	}

	// Case 1: every artifact done → complete (unchanged behavior).
	allDone := h.insertFetchDoc(testSource, "all-done", 2)
	h.insertArtifact(allDone, "body", "main", "done", 1, 5)
	h.insertArtifact(allDone, "file", "0.pdf", "done", 1, 5)
	if got := finalize(allDone); got != "complete" {
		t.Errorf("all-done doc state = %q, want complete", got)
	}

	// Case 2: done + dead-lettered (attempts exhausted) → partial, a terminal
	// state — the doc proceeds with the artifacts it HAS (e.g. body-only).
	withDead := h.insertFetchDoc(testSource, "done-plus-dead", 2)
	h.insertArtifact(withDead, "body", "main", "done", 1, 5)
	h.insertArtifact(withDead, "file", "0.pdf", "dead", 5, 5)
	if got := finalize(withDead); got != "partial" {
		t.Errorf("done+dead doc state = %q, want partial", got)
	}

	// Case 3: done + retryable error (attempts < max) → NOT terminal; the doc
	// stays 'fetching' so a later run retries the artifact.
	retryable := h.insertFetchDoc(testSource, "done-plus-retryable", 2)
	h.insertArtifact(retryable, "body", "main", "done", 1, 5)
	h.insertArtifact(retryable, "file", "0.pdf", "error", 2, 5)
	if got := finalize(retryable); got != "fetching" {
		t.Errorf("done+retryable doc state = %q, want fetching (retry path preserved)", got)
	}

	// Case 4: dead + retryable error → NOT terminal. The dead artifact alone
	// must not flip the doc to partial while a sibling is still retryable.
	mixed := h.insertFetchDoc(testSource, "dead-plus-retryable", 3)
	h.insertArtifact(mixed, "body", "main", "done", 1, 5)
	h.insertArtifact(mixed, "file", "0.pdf", "dead", 5, 5)
	h.insertArtifact(mixed, "file", "1.pdf", "error", 2, 5)
	if got := finalize(mixed); got != "fetching" {
		t.Errorf("dead+retryable doc state = %q, want fetching (dead must not preempt retries)", got)
	}

	// Case 5: not yet plan-ready → completeness is not judged; stays fetching.
	unplanned := h.insertFetchDocUnplanned(testSource, "not-plan-ready")
	h.insertArtifact(unplanned, "body", "main", "done", 1, 5)
	if got := finalize(unplanned); got != "fetching" {
		t.Errorf("unplanned doc state = %q, want fetching", got)
	}

	// Downstream visibility: the partial doc flows to extract alongside
	// complete docs; fetching docs stay invisible.
	visible := map[int64]bool{}
	ids, err := dbingest.New(pool).ListCompleteFetchDocIDsAfter(ctx, dbingest.ListCompleteFetchDocIDsAfterParams{
		AfterID:  0,
		RowLimit: 1_000_000,
	})
	if err != nil {
		t.Fatalf("list complete fetch docs: %v", err)
	}
	for _, id := range ids {
		visible[id] = true
	}
	if !visible[allDone] {
		t.Error("complete doc not visible to the extract selector")
	}
	if !visible[withDead] {
		t.Error("partial doc not visible to the extract selector — dead-letter completion regressed")
	}
	if visible[retryable] || visible[mixed] || visible[unplanned] {
		t.Error("non-terminal doc leaked into the extract selector")
	}

	// The completeness counters must reflect the child rows.
	var done, failed int
	if err := pool.QueryRow(ctx,
		`SELECT artifacts_done, artifacts_failed FROM ingest.fetch_doc WHERE id = $1`, withDead,
	).Scan(&done, &failed); err != nil {
		t.Fatalf("read counters: %v", err)
	}
	if done != 1 || failed != 1 {
		t.Errorf("partial doc counters = done %d / failed %d, want 1 / 1", done, failed)
	}
}

// finalizeDB bundles the fetch-ledger seeding helpers.
type finalizeDB struct {
	t    *testing.T
	ctx  context.Context
	pool *pgxpool.Pool
}

// insertFetchDoc creates a plan-ready fetch_doc in state 'fetching' with the
// given artifacts_expected count.
func (h finalizeDB) insertFetchDoc(source, externalID string, expected int) int64 {
	h.t.Helper()
	var id int64
	if err := h.pool.QueryRow(h.ctx,
		`INSERT INTO ingest.fetch_doc (source, external_id, state, plan_ready, in_scope, artifacts_expected, discovered_at, updated_at)
		 VALUES ($1, $2, 'fetching', true, true, $3, now(), now()) RETURNING id`,
		source, externalID, expected).Scan(&id); err != nil {
		h.t.Fatalf("insert fetch_doc %s/%s: %v", source, externalID, err)
	}
	return id
}

// insertFetchDocUnplanned creates a fetch_doc whose artifacts are not yet
// enumerated (plan_ready=false), so completeness cannot be judged.
func (h finalizeDB) insertFetchDocUnplanned(source, externalID string) int64 {
	h.t.Helper()
	var id int64
	if err := h.pool.QueryRow(h.ctx,
		`INSERT INTO ingest.fetch_doc (source, external_id, state, plan_ready, in_scope, discovered_at, updated_at)
		 VALUES ($1, $2, 'fetching', false, true, now(), now()) RETURNING id`,
		source, externalID).Scan(&id); err != nil {
		h.t.Fatalf("insert unplanned fetch_doc %s/%s: %v", source, externalID, err)
	}
	return id
}

func (h finalizeDB) insertArtifact(docID int64, kind, refKey, state string, attempts, maxAttempts int) {
	h.t.Helper()
	if _, err := h.pool.Exec(h.ctx,
		`INSERT INTO ingest.fetch_artifact (fetch_doc_id, kind, ref_key, state, attempts, max_attempts, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, now(), now())`,
		docID, kind, refKey, state, attempts, maxAttempts); err != nil {
		h.t.Fatalf("insert artifact %d/%s/%s: %v", docID, kind, refKey, err)
	}
}
