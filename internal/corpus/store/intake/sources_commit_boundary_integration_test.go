package intake

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

func TestSourcesAdapterThirdExpiryRequiresOlderClaim(t *testing.T) {
	for _, marker := range []string{"same", "newer-fence", "newer-epoch"} {
		for _, cancelled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cancelled=%t", marker, cancelled), func(t *testing.T) {
				f := newSourcesAdapterFixture(t)
				f.requests(t, 1)
				claim, err := f.sources.ClaimRequest(f.db.ctx, f.doc)
				if err != nil {
					t.Fatal(err)
				}
				for ordinal := 1; ordinal < 3; ordinal++ {
					f.reclaim(t)
					if _, err := f.sources.RecoverRequest(f.db.ctx, sourceWork(f.doc, "ordering-recover", fmt.Sprint(ordinal)), claim.RequestKey); err != nil {
						t.Fatal(err)
					}
					claim, err = f.sources.ClaimRequest(f.db.ctx, f.doc)
					if err != nil {
						t.Fatal(err)
					}
				}
				query := ""
				switch marker {
				case "newer-fence":
					query = `UPDATE ingest.artifact_request SET claimed_fence = claimed_fence + 1`
				case "newer-epoch":
					query = `UPDATE ingest.artifact_request SET claimed_epoch = claimed_epoch + 1`
				}
				if query != "" {
					if _, err := f.app.Exec(f.db.ctx, query); err != nil {
						t.Fatal(err)
					}
				}
				if cancelled {
					if _, err := f.app.Exec(f.db.ctx, `UPDATE ingest.execution_lease SET cancelled_at = clock_timestamp() WHERE resource_kind = 'document_version'`); err != nil {
						t.Fatal(err)
					}
				}
				q := intakedb.New(f.app)
				before, err := q.ReadArtifactRequest(f.db.ctx, claim.RequestKey)
				if err != nil {
					t.Fatal(err)
				}
				results := f.count(t, `SELECT count(*) FROM ingest.execution_result`)
				receipt, err := f.sources.ExpireRequest(f.db.ctx, sourceWork(f.doc, "ordering-expire", marker), claim.RequestKey, "infrastructure_retry_exhausted")
				if !errors.Is(err, run.ErrStaleGrant) || !reflect.DeepEqual(receipt, run.IntakeReceipt{}) {
					t.Fatalf("non-older third claim closed: receipt=%+v err=%v", receipt, err)
				}
				after, err := q.ReadArtifactRequest(f.db.ctx, claim.RequestKey)
				if err != nil || !reflect.DeepEqual(before, after) || before.State != "leased" || before.InfrastructureAttempts != 3 || before.SourceAttempts != 0 || f.count(t, `SELECT count(*) FROM ingest.execution_result`) != results || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 1 {
					t.Fatalf("rejected expiry changed request, receipts, counters or slot: %v", err)
				}
			})
		}
	}
}

func TestSourcesAdapterResumptionReturnsCommittedResultAtBoundary(t *testing.T) {
	for _, denied := range []bool{false, true} {
		for _, replay := range []bool{false, true} {
			for _, boundary := range []string{"rotate", "cancel"} {
				t.Run(fmt.Sprintf("denied=%t/replay=%t/%s", denied, replay, boundary), func(t *testing.T) {
					f := newSourcesAdapterFixture(t)
					key := fmt.Sprintf("page-%032x", 3300)
					f.requestedPage(t, key)
					limit := 1
					if denied {
						limit = 3
					}
					for ordinal := 1; ordinal <= limit; ordinal++ {
						if _, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "boundary-claim", fmt.Sprint(ordinal)), key); err != nil {
							t.Fatal(err)
						}
						if _, err := f.sources.CloseWindow(f.db.ctx, sourceWork(f.window, "boundary-close", fmt.Sprint(ordinal)), SourceWindowClosure{Reason: "worker_lost"}); err != nil {
							t.Fatal(err)
						}
						f.reclaimWindow(t, 1)
						if ordinal < limit {
							if _, err := f.sources.ResumeWindow(f.db.ctx, sourceWork(f.window, "boundary-between", fmt.Sprint(ordinal))); err != nil {
								t.Fatal(err)
							}
						}
					}
					work := sourceWork(f.window, "boundary-resume", key)
					var prior run.IntakeReceipt
					var wantErr error
					if denied {
						wantErr = run.ErrStaleGrant
					}
					if replay {
						var err error
						prior, err = f.sources.ResumeWindow(f.db.ctx, work)
						if !errors.Is(err, wantErr) {
							t.Fatalf("prepare replay: %v", err)
						}
					}
					ctx, cancel := context.WithCancel(f.db.ctx)
					defer cancel()
					called := false
					f.sources.afterResumeCommit = func() {
						called = true
						if boundary == "cancel" {
							cancel()
						} else if _, err := f.ledger.RotateAdmission(f.db.ctx, 1, "writer-b", true); err != nil {
							t.Fatal(err)
						}
					}
					before := f.count(t, `SELECT count(*) FROM ingest.execution_result`)
					receipt, err := f.sources.ResumeWindow(ctx, work)
					if !called || !errors.Is(err, wantErr) || errors.Is(err, context.Canceled) || receipt.ResultID == "" || replay && !reflect.DeepEqual(prior, receipt) {
						t.Fatalf("lost committed resumption: receipt=%+v err=%v hook=%t", receipt, err, called)
					}
					storedReceipt, readErr := ledgerTransaction(f.db.ctx, f.ledger, func(tx pgx.Tx, q intakedb.Querier) (run.IntakeReceipt, error) {
						stored, err := q.FindExecutionResult(f.db.ctx, run.IntakeIdempotencyKey(work))
						if err != nil {
							return run.IntakeReceipt{}, err
						}
						return receiptFromResult(f.db.ctx, tx, stored)
					})
					if readErr != nil || !reflect.DeepEqual(receipt, storedReceipt) {
						t.Fatalf("caller receipt differs from committed receipt: %v", readErr)
					}
					reason, state := "", "frozen"
					if denied {
						reason, state = "infrastructure_retry_exhausted", "failed"
					}
					if f.count(t, `SELECT count(*) FROM ingest.execution_result WHERE result_key = $1 AND COALESCE(payload->>'ExhaustionReason', '') = $2`, receipt.ResultID, reason) != 1 || f.count(t, `SELECT count(*) FROM ingest.source_window WHERE state = $1`, state) != 1 || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 {
						t.Fatal("returned classification differs from committed result or accounting")
					}
					added := int64(1)
					if replay {
						added = 0
					}
					if f.count(t, `SELECT count(*) FROM ingest.execution_result`) != before+added {
						t.Fatal("resumption inserted wrong number of receipts")
					}
				})
			}
		}
	}
}
