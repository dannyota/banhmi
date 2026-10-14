package intake

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
)

func TestSourcesAdapterCancelledRequestClosureReleasesOnce(t *testing.T) {
	for _, mode := range []string{"expire", "fail", "exhaust", "expire-third"} {
		t.Run(mode, func(t *testing.T) {
			f := newSourcesAdapterFixture(t)
			number := 3
			if mode == "expire-third" {
				number = 2
			}
			inventory, requests := f.requests(t, number)
			captured, err := f.sources.ClaimRequest(f.db.ctx, f.doc)
			if err != nil {
				t.Fatal(err)
			}
			capture := f.capture
			capture.CaptureKey = fmt.Sprintf("capture-%032x", 3200)
			past := requests[0].RetryStartedAt
			transport := SourceTransportReceipt{RequestKey: captured.RequestKey, AttemptKey: captured.AttemptKey, Attempt: captured.NextAttempt, Capture: &capture, Next: past, RetryAfter: past}
			if _, err := f.sources.RecordTransport(f.db.ctx, sourceWork(f.doc, "cancel-capture", mode), transport); err != nil {
				t.Fatal(err)
			}
			claim, err := f.sources.ClaimRequest(f.db.ctx, f.doc)
			if err != nil || claim.RequestKey == captured.RequestKey {
				t.Fatalf("claim second request: %v", err)
			}
			if mode == "expire-third" {
				for ordinal := 1; ordinal < 3; ordinal++ {
					f.reclaim(t)
					if _, err := f.sources.RecoverRequest(f.db.ctx, sourceWork(f.doc, "cancel-recover", fmt.Sprint(ordinal)), claim.RequestKey); err != nil {
						t.Fatal(err)
					}
					claim, err = f.sources.ClaimRequest(f.db.ctx, f.doc)
					if err != nil {
						t.Fatal(err)
					}
				}
				f.reclaim(t)
			}
			if mode == "exhaust" {
				// Seed the spent allowance through mutable accounting columns;
				// the retry deadline is a frozen input and cannot be changed.
				if _, err := f.app.Exec(f.db.ctx, `UPDATE ingest.artifact_request SET source_attempts = source_attempt_limit WHERE request_key = $1`, claim.RequestKey); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.app.Exec(f.db.ctx, `UPDATE ingest.execution_lease SET cancelled_at = clock_timestamp() WHERE resource_kind = 'document_version' AND resource_key = 'version-a'`); err != nil {
				t.Fatal(err)
			}
			var cancelledAt time.Time
			if err := f.app.QueryRow(f.db.ctx, `SELECT cancelled_at FROM ingest.execution_lease WHERE resource_kind = 'document_version' AND resource_key = 'version-a'`).Scan(&cancelledAt); err != nil {
				t.Fatal(err)
			}
			artifact := inventory.Artifacts[0]
			artifact.Capture = &capture
			acceptance := CaptureAcceptance{RequestKey: captured.RequestKey, Capture: capture, Artifact: &artifact, CaptureURL: artifact.URL}
			results := f.count(t, `SELECT count(*) FROM ingest.execution_result`)
			if _, err := f.sources.ApplyCapture(f.db.ctx, sourceWork(f.doc, "cancel-accept", mode), acceptance); !errors.Is(err, run.ErrStaleGrant) {
				t.Fatalf("cancelled lease accepted capture: %v", err)
			}
			if f.count(t, `SELECT count(*) FROM ingest.execution_result`) != results || f.count(t, `SELECT count(*) FROM bronze.artifact`) != 0 {
				t.Fatal("cancelled acceptance left a receipt or artifact")
			}
			sourceAttempts := f.count(t, `SELECT sum(source_attempts) FROM ingest.artifact_request`)
			infrastructureAttempts := f.count(t, `SELECT sum(infrastructure_attempts) FROM ingest.artifact_request`)
			work := sourceWork(f.doc, "cancel-close", mode)
			state, reason := model.ArtifactRequestExpired, "processing_closed"
			if mode == "fail" {
				state, reason = model.ArtifactRequestFailed, "policy_rejected"
			} else if mode == "exhaust" {
				state, reason = model.ArtifactRequestExhausted, "source_exhausted"
			}
			closeRequest := func() (run.IntakeReceipt, error) {
				if state == model.ArtifactRequestExpired {
					return f.sources.ExpireRequest(f.db.ctx, work, claim.RequestKey, reason)
				}
				return f.sources.CloseRequest(f.db.ctx, work, claim.RequestKey, state, reason)
			}
			first, err := closeRequest()
			if err != nil {
				t.Fatal(err)
			}
			if f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 || f.count(t, `SELECT count(*) FROM ingest.artifact_request WHERE request_key = $1 AND state = $2 AND reason = $3`, claim.RequestKey, string(state), reason) != 1 {
				t.Fatal("cancelled closure did not close request and release host")
			}
			if _, err := f.sources.ClaimRequest(f.db.ctx, f.doc); !errors.Is(err, run.ErrStaleGrant) {
				t.Fatalf("cancelled lease claimed fresh request on free host: %v", err)
			}
			// The window lease remains current. Its next discovery request can
			// use the capacity-one host after the document releases its slot.
			key := fmt.Sprintf("page-%032x", 3200)
			plan := f.plan(fmt.Sprintf("request-%032x", 101), "2", model.PurposeListing)
			plan.ParentCapture = fmt.Sprintf("capture-%032x", 1)
			if _, err := f.sources.RegisterPage(f.db.ctx, sourceWork(f.window, "cancel-next-page", key), SourcePagePlan{PageKey: key, Ordinal: 1, Plan: plan, RetryStartedAt: f.frozen.Request.Cutoff}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "cancel-other-claim", key), key); err != nil {
				t.Fatalf("unrelated request could not claim released host: %v", err)
			}
			results = f.count(t, `SELECT count(*) FROM ingest.execution_result`)
			again, err := closeRequest()
			if err != nil || !reflect.DeepEqual(first, again) {
				t.Fatalf("cancelled closure replay changed receipt: %v", err)
			}
			if f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 1 || f.count(t, `SELECT count(*) FROM ingest.execution_result`) != results || f.count(t, `SELECT sum(source_attempts) FROM ingest.artifact_request`) != sourceAttempts || f.count(t, `SELECT sum(infrastructure_attempts) FROM ingest.artifact_request`) != infrastructureAttempts {
				t.Fatal("cancelled closure or replay changed counters or released unrelated slot")
			}
			if f.count(t, `SELECT count(*) FROM ingest.execution_lease WHERE resource_kind = 'document_version' AND resource_key = 'version-a' AND cancelled_at = $1 AND expires_at > clock_timestamp()`, cancelledAt) != 1 {
				t.Fatal("closure erased cancellation or changed live lease")
			}
		})
	}
}

func TestSourcesAdapterCancelledRequestClosureStillRequiresCurrentGrant(t *testing.T) {
	for _, operation := range []string{"expire", "close"} {
		for _, stale := range []string{"admission", "owner", "fence", "lease-epoch", "expired"} {
			t.Run(operation+"/"+stale, func(t *testing.T) {
				f := newSourcesAdapterFixture(t)
				_, requests := f.requests(t, 1)
				if _, err := f.sources.ClaimRequest(f.db.ctx, f.doc); err != nil {
					t.Fatal(err)
				}
				if _, err := f.app.Exec(f.db.ctx, `UPDATE ingest.execution_lease SET cancelled_at = clock_timestamp() WHERE resource_kind = 'document_version'`); err != nil {
					t.Fatal(err)
				}
				query := ""
				switch stale {
				case "admission":
					query = `UPDATE ingest.country_admission SET enabled = false, admission_epoch = admission_epoch + 1`
				case "owner":
					query = `UPDATE ingest.execution_lease SET owner = 'other-owner' WHERE resource_kind = 'document_version'`
				case "fence":
					query = `UPDATE ingest.execution_lease SET attempt_fence = attempt_fence + 1 WHERE resource_kind = 'document_version'`
				case "lease-epoch":
					query = `UPDATE ingest.execution_lease SET admission_epoch = admission_epoch + 1 WHERE resource_kind = 'document_version'`
				case "expired":
					query = `UPDATE ingest.execution_lease SET expires_at = clock_timestamp() - interval '1 second' WHERE resource_kind = 'document_version'`
				}
				if _, err := f.app.Exec(f.db.ctx, query); err != nil {
					t.Fatal(err)
				}
				before := f.count(t, `SELECT count(*) FROM ingest.execution_result`)
				work := sourceWork(f.doc, "cancel-stale-close", stale)
				var err error
				if operation == "expire" {
					_, err = f.sources.ExpireRequest(f.db.ctx, work, requests[0].Plan.RequestKey, "processing_closed")
				} else {
					_, err = f.sources.CloseRequest(f.db.ctx, work, requests[0].Plan.RequestKey, model.ArtifactRequestFailed, "policy_rejected")
				}
				if !errors.Is(err, run.ErrStaleGrant) {
					t.Fatalf("cancelled closure waived %s: %v", stale, err)
				}
				if f.count(t, `SELECT count(*) FROM ingest.execution_result`) != before || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 1 || f.count(t, `SELECT count(*) FROM ingest.artifact_request WHERE state = 'leased' AND source_attempts = 0 AND infrastructure_attempts = 1`) != 1 {
					t.Fatal("stale cancelled closure mutated request, receipt, counters or slot")
				}
			})
		}
	}
}
