package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/source/api"
)

type sourcesAdapterFixture struct {
	db      sourceDatabaseFixture
	app     *pgx.Conn
	sources *Sources
	ledger  *Ledger
	window  run.IntakeWork
	doc     run.IntakeWork
	frozen  FrozenSourceWindow
	capture model.CaptureRef
}

func newSourcesAdapterFixture(t *testing.T) *sourcesAdapterFixture {
	t.Helper()
	db := newSourceDatabaseFixture(t)
	app := sourceAppConnection(t, db)
	objects := fixtureObjectStore{content: "synthetic source bytes"}
	s, err := NewSources(app, "writer-a", objects)
	if err != nil {
		t.Fatal(err)
	}
	l, err := NewLedger(app, "writer-a", objects, nil)
	if err != nil {
		t.Fatal(err)
	}
	budget := run.Budget{MaxItems: 100, MaxInputBytes: 1 << 20, MaxPages: 10, MaxElapsed: time.Hour, MaxConcurrent: 1}
	window := run.IntakeWork{Execution: run.ScopedExecution{Country: "vn", WorkflowID: "source-sync/vn/source-a/window-a", WorkflowKind: run.SourceSync, AttemptKey: "attempt-a", AdmissionEpoch: 1, Owner: "owner-a", Scope: run.ExecutionScope{Kind: run.ScopeIntake, Subject: run.IntakeSubject{Kind: run.SubjectSourceWindow, SourceWindow: "window-a"}}}, Stage: run.Discover, Substep: "open", Shard: "window-a", InputDigest: fixtureDigest, RecipeDigest: fixtureDigest, BudgetDigest: fixtureDigest, Budget: budget}
	doc := window
	doc.Execution.WorkflowID, doc.Execution.WorkflowKind = "document-ingest/vn/source-a/version-a", run.DocumentIngest
	doc.Execution.Scope.Subject = run.IntakeSubject{Kind: run.SubjectDocumentVersion, DocumentVersion: "version-a"}
	doc.Stage = run.Fetch
	for _, work := range []*run.IntakeWork{&window, &doc} {
		lease, err := l.ClaimLease(db.ctx, 1, work.Execution.Scope, "owner-a", time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		work.Execution.AttemptFence = lease.AttemptFence
		if _, err := l.RegisterExecution(db.ctx, *work, "synthetic-run", nil); err != nil {
			t.Fatal(err)
		}
	}
	hash := sha256.Sum256([]byte(objects.content))
	asset := model.AssetRef{Path: "staged/source", SHA256: model.Digest(hex.EncodeToString(hash[:])), ByteSize: int64(len(objects.content))}
	capture := model.CaptureRef{Country: "vn", SourceID: "source-a", CaptureKey: fmt.Sprintf("capture-%032x", 1), Asset: asset, MediaType: "text/html", RequestHash: fixtureDigest, ResponseHash: fixtureDigest, TransportReceipt: asset}
	now := time.Now().UTC().Truncate(time.Microsecond)
	frozen := FrozenSourceWindow{Request: api.DiscoverRequest{Country: "vn", SourceID: "source-a", WindowKey: "window-a", SliceKey: "keyword:0", Cursor: "1", Mode: api.DiscoveryIncremental, Start: now.Add(-time.Hour), Cutoff: now, PolicyHash: fixtureDigest, BudgetHash: fixtureDigest, Budget: model.ResourceBudget(budget)}, Deadline: now.Add(time.Hour), PageBudget: 10, RequestBudget: 10, AttemptLimit: 2, RetryInterval: time.Hour}
	f := &sourcesAdapterFixture{db: db, app: app, sources: s, ledger: l, window: window, doc: doc, frozen: frozen, capture: capture}
	if _, err := s.OpenWindow(db.ctx, window, frozen); err != nil {
		t.Fatal(err)
	}
	return f
}

func sourceWork(base run.IntakeWork, substep, shard string) run.IntakeWork {
	base.Substep, base.Shard = substep, shard
	return base
}

func (f *sourcesAdapterFixture) count(t *testing.T, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := f.db.conn.QueryRow(f.db.ctx, query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (f *sourcesAdapterFixture) plan(key, cursor string, purpose model.RequestPurpose) api.RequestPlan {
	return api.RequestPlan{Country: "vn", SourceID: "source-a", RequestKey: key, WindowKey: "window-a", Purpose: purpose, URL: "https://example.com/source", Method: "GET", Cursor: cursor, PolicyHash: fixtureDigest, BudgetHash: fixtureDigest, Budget: f.frozen.Request.Budget}
}

func (f *sourcesAdapterFixture) page(t *testing.T, ordinal int32, complete bool, observations bool, decorate ...func(*api.DecodedDiscoveryPage)) api.DecodedDiscoveryPage {
	t.Helper()
	key := fmt.Sprintf("page-%032x", ordinal+1)
	cursor := fmt.Sprint(ordinal + 1)
	plan := SourcePagePlan{PageKey: key, Ordinal: ordinal, Plan: f.plan(fmt.Sprintf("request-%032x", ordinal+100), cursor, model.PurposeListing), RetryStartedAt: time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)}
	if _, err := f.sources.RegisterPage(f.db.ctx, sourceWork(f.window, "plan", key), plan); err != nil {
		t.Fatal(err)
	}
	capture := f.capture
	capture.CaptureKey = fmt.Sprintf("capture-%032x", ordinal+1)
	receipt := SourceCaptureReceipt{Key: key, AttemptKey: fmt.Sprintf("request-%032x", ordinal+200), Attempt: 1, Capture: capture}
	if _, err := f.sources.RecordPageCapture(f.db.ctx, sourceWork(f.window, "capture", key), receipt); err != nil {
		t.Fatal(err)
	}
	page := api.DecodedDiscoveryPage{PageKey: key, Capture: capture, Cursor: model.CursorPosition{WindowKey: "window-a", SliceKey: "keyword:0", InitialCursor: cursor, CommittedCursor: cursor, NextCursor: cursor, Complete: complete, Ordinal: 99}, Limits: model.RuleLimits{MaxItems: 100, MaxBytes: 1 << 20}}
	if complete {
		page.Cursor.NextCursor = "checkpoint"
	}
	if observations {
		page.Observations = []api.DecodedObservation{{SourceID: "source-a", ExternalID: "native-a", VersionKey: "version-a", Capture: capture, Limits: page.Limits}}
	}
	for _, fn := range decorate {
		fn(&page)
	}
	if _, err := f.sources.RecordPageDecode(f.db.ctx, sourceWork(f.window, "decode", key), page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestSourcesAdapterCompletionWaitsForLaunchAcknowledgement(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	request := fmt.Sprintf("request-%032x", 400)
	page := f.page(t, 0, true, true, func(page *api.DecodedDiscoveryPage) {
		page.FollowUps = []model.FollowUpRequest{{RequestKey: request, WindowKey: "window-a", ParentCapture: page.Capture.CaptureKey, Purpose: model.PurposeDetail, URL: "https://example.com/detail", PolicyHash: fixtureDigest, BudgetHash: fixtureDigest, Budget: f.frozen.Request.Budget}}
	})
	acceptance := DiscoveryAcceptance{Page: page, Ordinal: 0, Launches: []SourceLaunch{{RequestKey: request, Work: f.doc, DueAt: time.Now().UTC().Truncate(time.Microsecond)}}}
	if _, err := f.sources.ApplyDiscoveryPage(f.db.ctx, sourceWork(f.window, "accept", page.PageKey), acceptance); err != nil {
		t.Fatal(err)
	}
	w, err := f.sources.ReadWindow(f.db.ctx, f.window)
	if err != nil || w.State != "progressing" || w.CommittedCursor != "1" {
		t.Fatal("unacknowledged child launch advanced checkpoint")
	}
	if f.count(t, `SELECT count(*) FROM ingest.delivery_record WHERE delivery_kind = 'launch' AND acknowledged_at IS NULL`) != 1 {
		t.Fatal("acceptance omitted durable launch intent")
	}
	if _, err := f.db.conn.Exec(f.db.ctx, `UPDATE ingest.delivery_record SET acknowledged_at = clock_timestamp() WHERE deduplication_key = $1`, request); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.ReconcileWindow(f.db.ctx, sourceWork(f.window, "reconcile", "acknowledged")); err != nil {
		t.Fatal(err)
	}
	w, err = f.sources.ReadWindow(f.db.ctx, f.window)
	if err != nil || w.State != "complete" || w.CommittedCursor != "checkpoint" {
		t.Fatalf("acknowledged window did not complete: %v", err)
	}
}

func (f *sourcesAdapterFixture) accept(t *testing.T, page api.DecodedDiscoveryPage, ordinal int32) run.IntakeReceipt {
	t.Helper()
	r, err := f.sources.ApplyDiscoveryPage(f.db.ctx, sourceWork(f.window, "accept", page.PageKey), DiscoveryAcceptance{Page: page, Ordinal: ordinal})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSourcesAdapterCompleteWindow(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	first, last := f.page(t, 0, false, false), f.page(t, 1, true, false)
	f.accept(t, last, 1)
	w, err := f.sources.ReadWindow(f.db.ctx, f.window)
	if err != nil || w.State != "progressing" || w.CommittedCursor != "1" {
		t.Fatalf("early final page advanced checkpoint: state=%s cursor=%s error=%v", w.State, w.CommittedCursor, err)
	}
	f.accept(t, first, 0)
	w, err = f.sources.ReadWindow(f.db.ctx, f.window)
	if err != nil || w.State != "complete" || w.CommittedCursor != "checkpoint" {
		t.Fatalf("fully accepted window did not complete: state=%s cursor=%s error=%v", w.State, w.CommittedCursor, err)
	}
}

func TestSourcesAdapterIncompleteAndIntrinsicFailure(t *testing.T) {
	for _, intrinsic := range []bool{false, true} {
		t.Run(fmt.Sprintf("intrinsic-%t", intrinsic), func(t *testing.T) {
			f := newSourcesAdapterFixture(t)
			first, sibling := f.page(t, 0, false, false), f.page(t, 1, false, false)
			closure := SourceWindowClosure{Reason: "window_budget"}
			if intrinsic {
				closure.PageKey, closure.Reason = first.PageKey, "decoder_rejected"
			} else {
				f.accept(t, first, 0)
				w, err := f.sources.ReadWindow(f.db.ctx, f.window)
				if err != nil || w.State != "progressing" || w.CommittedCursor != "1" {
					t.Fatal("incomplete page changed checkpoint or failed window")
				}
			}
			if _, err := f.sources.CloseWindow(f.db.ctx, sourceWork(f.window, "close", "window-a"), closure); err != nil {
				t.Fatal(err)
			}
			w, err := f.sources.ReadWindow(f.db.ctx, f.window)
			if err != nil || w.CommittedCursor != "1" {
				t.Fatal("closure changed checkpoint")
			}
			p, err := f.sources.ReadPage(f.db.ctx, f.window, sibling.PageKey)
			if err != nil {
				t.Fatal(err)
			}
			if intrinsic {
				if w.State != "failed" || p.State != "failed" || p.Reason == nil || *p.Reason != "window_closed" {
					t.Fatal("intrinsic decoder failure did not close sibling precisely")
				}
			} else if w.State != "incomplete" || p.State != "decoded" || p.Reason != nil {
				t.Fatal("budget closure fabricated page failure")
			}
		})
	}
}

func TestSourcesAdapterPageReceiptReplay(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	page := f.page(t, 0, false, false)
	capture := SourceCaptureReceipt{Key: page.PageKey, AttemptKey: fmt.Sprintf("request-%032x", 200), Attempt: 1, Capture: page.Capture}
	acceptance := DiscoveryAcceptance{Page: page, Ordinal: 0}
	calls := []struct {
		name    string
		call    func() (run.IntakeReceipt, error)
		changed func() (run.IntakeReceipt, error)
	}{
		{"capture", func() (run.IntakeReceipt, error) { return f.sources.RecordPageCapture(f.db.ctx, sourceWork(f.window, "capture", page.PageKey), capture) }, func() (run.IntakeReceipt, error) {
			changed := capture
			changed.Capture.Asset.ByteSize++
			return f.sources.RecordPageCapture(f.db.ctx, sourceWork(f.window, "capture", page.PageKey), changed)
		}},
		{"decode", func() (run.IntakeReceipt, error) { return f.sources.RecordPageDecode(f.db.ctx, sourceWork(f.window, "decode", page.PageKey), page) }, func() (run.IntakeReceipt, error) {
			changed := page
			changed.Cursor.Ordinal++
			return f.sources.RecordPageDecode(f.db.ctx, sourceWork(f.window, "decode", page.PageKey), changed)
		}},
		{"accept", func() (run.IntakeReceipt, error) { return f.sources.ApplyDiscoveryPage(f.db.ctx, sourceWork(f.window, "accept", page.PageKey), acceptance) }, func() (run.IntakeReceipt, error) {
			changed := acceptance
			changed.Ordinal++
			return f.sources.ApplyDiscoveryPage(f.db.ctx, sourceWork(f.window, "accept", page.PageKey), changed)
		}},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			first, err := tc.call()
			if err != nil {
				t.Fatal(err)
			}
			count := f.count(t, `SELECT count(*) FROM ingest.execution_result`)
			again, err := tc.call()
			if err != nil || !reflect.DeepEqual(first, again) {
				t.Fatalf("replay changed committed receipt: %v", err)
			}
			if _, err := tc.changed(); !errors.Is(err, run.ErrPayloadConflict) {
				t.Fatalf("changed receipt did not conflict: %v", err)
			}
			if f.count(t, `SELECT count(*) FROM ingest.execution_result`) != count {
				t.Fatal("replay or conflict wrote another receipt")
			}
		})
	}
}

func TestSourcesAdapterStaleGrantsWriteNothing(t *testing.T) {
	for _, stale := range []string{"epoch", "owner", "fence", "expired", "cancelled"} {
		t.Run(stale, func(t *testing.T) {
			f := newSourcesAdapterFixture(t)
			work := sourceWork(f.window, "host", "host-a")
			query := ""
			switch stale {
			case "epoch":
				if _, err := f.ledger.RotateAdmission(f.db.ctx, 1, "writer-a", true); err != nil {
					t.Fatal(err)
				}
			case "owner":
				work.Execution.Owner = "owner-b"
			case "fence":
				work.Execution.AttemptFence++
			case "expired":
				query = `UPDATE ingest.execution_lease SET expires_at = clock_timestamp() - interval '1 second' WHERE resource_kind = 'source_window'`
			case "cancelled":
				query = `UPDATE ingest.execution_lease SET cancelled_at = clock_timestamp() WHERE resource_kind = 'source_window'`
			}
			if query != "" {
				if _, err := f.db.conn.Exec(f.db.ctx, query); err != nil {
					t.Fatal(err)
				}
			}
			before := f.count(t, `SELECT count(*) FROM ingest.execution_result`)
			if _, err := f.sources.RegisterHost(f.db.ctx, work, SourceHostPacing{HostKey: "example.com", NextAllowedAt: time.Now(), MaxInFlight: 1}); !errors.Is(err, run.ErrStaleGrant) {
				t.Fatalf("stale source grant must be typed: %v", err)
			}
			if f.count(t, `SELECT count(*) FROM ingest.source_rate_limit`) != 0 || f.count(t, `SELECT count(*) FROM ingest.execution_result`) != before {
				t.Fatal("stale grant wrote pacing or receipt")
			}
		})
	}
}

func (f *sourcesAdapterFixture) requests(t *testing.T, number int) (api.DecodedDetailResult, []SourceRequestPlan) {
	t.Helper()
	page := f.page(t, 0, false, true)
	receipt := f.accept(t, page, 0)
	observations, err := f.sources.AcceptedObservations(f.db.ctx, f.window, receipt.ResultID)
	if err != nil || len(observations) != 1 {
		t.Fatal(err)
	}
	parent := observations[0].ObservationKey
	if err := model.ValidateEnvelopeKey(parent, "observation"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.RegisterHost(f.db.ctx, sourceWork(f.window, "host", "example.com"), SourceHostPacing{HostKey: "example.com", NextAllowedAt: time.Now().Add(-time.Hour), MaxInFlight: 1}); err != nil {
		t.Fatal(err)
	}
	inventory := api.DecodedDetailResult{Observation: page.Observations[0], Limits: page.Limits}
	var requests []SourceRequestPlan
	for i := 0; i < number; i++ {
		inventory.Artifacts = append(inventory.Artifacts, model.ArtifactInventoryEntry{ArtifactKey: fmt.Sprintf("artifact-%d", i), Role: "main", URL: "https://example.com/source", Ordinal: i, Required: true, Format: "html"})
		plan := f.plan(fmt.Sprintf("request-%032x", i+300), "", model.PurposeArtifact)
		plan.ParentCapture = page.Capture.CaptureKey
		requests = append(requests, SourceRequestPlan{Plan: plan, ParentObservation: parent, Role: model.ArtifactRoleMain, Required: true, HostKey: "example.com", RetryStartedAt: time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond), ArtifactKey: fmt.Sprintf("artifact-%d", i)})
	}
	if _, err := f.sources.RegisterRequests(f.db.ctx, sourceWork(f.doc, "inventory", "version-a"), inventory, requests); err != nil {
		t.Fatal(err)
	}
	return inventory, requests
}

func TestSourcesAdapterTransportPacingAndExhaustion(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	_, requests := f.requests(t, 2)
	for attempt := int32(1); attempt <= f.frozen.AttemptLimit; attempt++ {
		claim, err := f.sources.ClaimRequest(f.db.ctx, f.doc)
		if err != nil || claim.NextAttempt != attempt {
			t.Fatalf("source claim did not retain frozen attempts: %v", err)
		}
		if _, err := f.sources.ClaimRequest(f.db.ctx, f.doc); !errors.Is(err, run.ErrStaleGrant) {
			t.Fatalf("occupied host admitted another claim: %v", err)
		}
		now := requests[0].RetryStartedAt
		r := SourceTransportReceipt{RequestKey: claim.RequestKey, AttemptKey: claim.AttemptKey, Attempt: attempt, Next: now, RetryAfter: now}
		work := sourceWork(f.doc, "transport", fmt.Sprintf("attempt-%d", attempt))
		first, err := f.sources.RecordTransport(f.db.ctx, work, r)
		if err != nil {
			t.Fatal(err)
		}
		again, err := f.sources.RecordTransport(f.db.ctx, work, r)
		if err != nil || !reflect.DeepEqual(first, again) {
			t.Fatalf("transport replay changed receipt: %v", err)
		}
		changed := r
		changed.Next = changed.Next.Add(time.Minute)
		if _, err := f.sources.RecordTransport(f.db.ctx, work, changed); !errors.Is(err, run.ErrPayloadConflict) {
			t.Fatalf("changed transport did not conflict: %v", err)
		}
		if f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 {
			t.Fatal("transport replay released host slot twice")
		}
	}
	r, err := f.sources.ReadRequest(f.db.ctx, f.doc, requests[0].Plan.RequestKey)
	if err != nil || r.State != "exhausted" || r.SourceAttempts != f.frozen.AttemptLimit {
		t.Fatalf("request did not exhaust frozen allowance: %v", err)
	}
	// Retry-After raises the shared host clock as well as request eligibility.
	claim, err := f.sources.ClaimRequest(f.db.ctx, f.doc)
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	rc := SourceTransportReceipt{RequestKey: claim.RequestKey, AttemptKey: claim.AttemptKey, Attempt: claim.NextAttempt, Next: future, RetryAfter: future}
	if _, err := f.sources.RecordTransport(f.db.ctx, sourceWork(f.doc, "transport", "future"), rc); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.ClaimRequest(f.db.ctx, f.doc); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("claim ignored next_allowed_at: %v", err)
	}
}

func (f *sourcesAdapterFixture) reclaim(t *testing.T) {
	t.Helper()
	if _, err := f.db.conn.Exec(f.db.ctx, `UPDATE ingest.execution_lease SET expires_at = clock_timestamp() - interval '1 second' WHERE resource_kind = 'document_version'`); err != nil {
		t.Fatal(err)
	}
	lease, err := f.ledger.ReclaimLease(f.db.ctx, 1, f.doc.Execution.Scope, f.doc.Execution.Owner, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	f.doc.Execution.AttemptFence = lease.AttemptFence
	if _, err := f.ledger.RegisterExecution(f.db.ctx, f.doc, "recovered-run", nil); err != nil {
		t.Fatal(err)
	}
}

func TestSourcesAdapterRecoveryAndExpiry(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	_, requests := f.requests(t, 1)
	key := requests[0].Plan.RequestKey
	for infrastructure := 1; infrastructure <= 3; infrastructure++ {
		if _, err := f.sources.ClaimRequest(f.db.ctx, f.doc); err != nil {
			t.Fatal(err)
		}
		old := f.doc
		f.reclaim(t)
		if _, err := f.sources.RecoverRequest(f.db.ctx, sourceWork(old, "recover", fmt.Sprint(infrastructure)), key); !errors.Is(err, run.ErrStaleGrant) {
			t.Fatalf("stale recovery changed abandoned slot: %v", err)
		}
		if infrastructure < 3 {
			if _, err := f.sources.RecoverRequest(f.db.ctx, sourceWork(f.doc, "recover", fmt.Sprint(infrastructure)), key); err != nil {
				t.Fatal(err)
			}
		} else {
			work := sourceWork(f.doc, "expire", key)
			first, err := f.sources.ExpireRequest(f.db.ctx, work, key, "infrastructure_retry_exhausted")
			if err != nil {
				t.Fatal(err)
			}
			again, err := f.sources.ExpireRequest(f.db.ctx, work, key, "infrastructure_retry_exhausted")
			if err != nil || !reflect.DeepEqual(first, again) {
				t.Fatalf("expiry replay was not harmless: %v", err)
			}
		}
		if f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 || f.count(t, `SELECT source_attempts FROM ingest.artifact_request`) != 0 {
			t.Fatal("infrastructure recovery consumed source attempts or leaked a slot")
		}
	}
}

func TestSourcesAdapterBronzeAtomicAcceptance(t *testing.T) {
	for _, mismatch := range []string{"none", "hash", "length", "late-link"} {
		t.Run(mismatch, func(t *testing.T) {
			f := newSourcesAdapterFixture(t)
			inventory, requests := f.requests(t, 1)
			claim, err := f.sources.ClaimRequest(f.db.ctx, f.doc)
			if err != nil {
				t.Fatal(err)
			}
			capture := f.capture
			capture.CaptureKey = fmt.Sprintf("capture-%032x", 900)
			if mismatch == "hash" {
				capture.Asset.SHA256 = model.Digest(strings.Repeat("b", 64))
			} else if mismatch == "length" {
				capture.Asset.ByteSize++
			}
			now := time.Now().Add(-time.Second)
			rc := SourceTransportReceipt{RequestKey: claim.RequestKey, AttemptKey: claim.AttemptKey, Attempt: claim.NextAttempt, Capture: &capture, Next: now, RetryAfter: now}
			if _, err := f.sources.RecordTransport(f.db.ctx, sourceWork(f.doc, "transport", "artifact"), rc); err != nil {
				t.Fatal(err)
			}
			if _, err := f.sources.RecoverRequest(f.db.ctx, sourceWork(f.doc, "recover", "fetched"), claim.RequestKey); err != nil {
				t.Fatal(err)
			}
			artifact := inventory.Artifacts[0]
			artifact.Capture = &capture
			a := CaptureAcceptance{RequestKey: requests[0].Plan.RequestKey, Capture: capture, Artifact: &artifact, CaptureURL: artifact.URL}
			work := sourceWork(f.doc, "bronze", "artifact")
			before := f.count(t, `SELECT count(*) FROM ingest.execution_result`)
			if mismatch == "late-link" {
				if _, err := f.db.conn.Exec(f.db.ctx, `ALTER TABLE bronze.observation_artifact ADD CONSTRAINT synthetic_reject CHECK (false)`); err != nil {
					t.Fatal(err)
				}
			}
			first, err := f.sources.ApplyCapture(f.db.ctx, work, a)
			if mismatch != "none" {
				if err == nil {
					t.Fatal("invalid or failing Bronze acceptance committed")
				}
				if f.count(t, `SELECT count(*) FROM bronze.artifact`) != 0 || f.count(t, `SELECT count(*) FROM bronze.observation_artifact`) != 0 || f.count(t, `SELECT count(*) FROM ingest.execution_result`) != before || f.count(t, `SELECT count(*) FROM bronze.blob_retention_reference WHERE owner_kind = 'execution' AND owner_key = (SELECT execution_key FROM ingest.execution WHERE resource_kind = 'document_version')`) != 0 {
					t.Fatal("failed Bronze acceptance left partial rows or receipt")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			again, err := f.sources.ApplyCapture(f.db.ctx, work, a)
			if err != nil || !reflect.DeepEqual(first, again) {
				t.Fatalf("Bronze replay did not return committed receipt: %v", err)
			}
			if f.count(t, `SELECT count(*) FROM bronze.blob`) != 1 || f.count(t, `SELECT count(*) FROM bronze.source_record`) != 1 || f.count(t, `SELECT count(*) FROM bronze.observation_revision`) != 1 || f.count(t, `SELECT count(*) FROM bronze.artifact`) != 1 || f.count(t, `SELECT count(*) FROM bronze.observation_artifact`) != 1 || f.count(t, `SELECT count(*) FROM bronze.blob_retention_reference`) != 3 {
				t.Fatal("Bronze acceptance did not atomically retain all rows or deduplicate blob hash")
			}
			request, err := f.sources.ReadRequest(f.db.ctx, f.doc, claim.RequestKey)
			if err != nil || request.State != "verified" || request.SourceAttempts != 1 {
				t.Fatalf("Bronze receipt lost verified state or source count: %v", err)
			}
			// Use the application login, not SET ROLE or the migration login.
			tx, err := f.app.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			requireSQLState(t, tx, "42501", `DELETE FROM bronze.blob_retention_reference`)
			if err := tx.Rollback(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSourcesAdapterRejectsUnresolvedCrossSchemaOwner(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	inventory, requests := f.requests(t, 1)
	requests[0].ParentObservation = fmt.Sprintf("observation-%032x", 999)
	requests[0].Plan.RequestKey = fmt.Sprintf("request-%032x", 999)
	before := f.count(t, `SELECT count(*) FROM ingest.execution_result`)
	if _, err := f.sources.RegisterRequests(f.db.ctx, sourceWork(f.doc, "inventory", "unknown-owner"), inventory, requests); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("well-shaped nonexistent observation was admitted: %v", err)
	}
	if f.count(t, `SELECT count(*) FROM ingest.artifact_request`) != 1 || f.count(t, `SELECT count(*) FROM ingest.execution_result`) != before {
		t.Fatal("failed owner validation left request or result rows")
	}
}

func TestSourcesAdapterFreshRequestKeyCannotResetAllowance(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	inventory, requests := f.requests(t, 1)
	requests[0].Plan.RequestKey = fmt.Sprintf("request-%032x", 1300)
	if _, err := f.sources.RegisterRequests(f.db.ctx, sourceWork(f.doc, "inventory", "replacement-key"), inventory, requests); !errors.Is(err, run.ErrPayloadConflict) {
		t.Fatalf("fresh request key reset frozen inventory allowance: %v", err)
	}
	if f.count(t, `SELECT count(*) FROM ingest.artifact_request`) != 1 {
		t.Fatal("replanning created a second source allowance")
	}
}

func TestSourcesAdapterRequestClosureReleasesOnce(t *testing.T) {
	for _, state := range []model.ArtifactRequestState{model.ArtifactRequestFailed, model.ArtifactRequestExpired} {
		t.Run(string(state), func(t *testing.T) {
			f := newSourcesAdapterFixture(t)
			_, requests := f.requests(t, 1)
			if _, err := f.sources.ClaimRequest(f.db.ctx, f.doc); err != nil {
				t.Fatal(err)
			}
			work := sourceWork(f.doc, "closure", "owned-request")
			closeRequest := func() (run.IntakeReceipt, error) {
				if state == model.ArtifactRequestExpired {
					return f.sources.ExpireRequest(f.db.ctx, work, requests[0].Plan.RequestKey, "processing_closed")
				}
				return f.sources.CloseRequest(f.db.ctx, work, requests[0].Plan.RequestKey, state, "policy_rejected")
			}
			first, err := closeRequest()
			if err != nil {
				t.Fatal(err)
			}
			again, err := closeRequest()
			if err != nil || !reflect.DeepEqual(first, again) || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 || f.count(t, `SELECT source_attempts FROM ingest.artifact_request`) != 0 {
				t.Fatalf("terminal closure did not release precisely once: %v", err)
			}
		})
	}
}

func TestSourcesAdapterDetailAcceptanceIssuesObservationAndInventory(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	inventory, existing := f.requests(t, 1)
	if _, err := f.sources.ExpireRequest(f.db.ctx, sourceWork(f.doc, "expire", "unused-artifact"), existing[0].Plan.RequestKey, "processing_closed"); err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("request-%032x", 1000)
	inventory.FollowUps = []model.FollowUpRequest{{RequestKey: key, WindowKey: "window-a", ParentCapture: inventory.Observation.Capture.CaptureKey, Purpose: model.PurposeDetail, URL: "https://example.com/source", PolicyHash: fixtureDigest, BudgetHash: fixtureDigest, Budget: f.frozen.Request.Budget}}
	plan := f.plan(key, "", model.PurposeDetail)
	plan.ParentCapture, plan.ExpectedResponseKind = inventory.Observation.Capture.CaptureKey, "metadata"
	request := SourceRequestPlan{Plan: plan, ParentObservation: existing[0].ParentObservation, Required: true, HostKey: "example.com", RetryStartedAt: time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)}
	if _, err := f.sources.RegisterRequests(f.db.ctx, sourceWork(f.doc, "inventory", "detail"), inventory, []SourceRequestPlan{request}); err != nil {
		t.Fatal(err)
	}
	claim, err := f.sources.ClaimRequest(f.db.ctx, f.doc)
	if err != nil || claim.RequestKey != key {
		t.Fatalf("detail request was not claimed: %v", err)
	}
	capture := f.capture
	capture.CaptureKey = fmt.Sprintf("capture-%032x", 1000)
	now := time.Now().Add(-time.Second)
	transport := SourceTransportReceipt{RequestKey: key, AttemptKey: claim.AttemptKey, Attempt: claim.NextAttempt, Capture: &capture, Next: now, RetryAfter: now}
	if _, err := f.sources.RecordTransport(f.db.ctx, sourceWork(f.doc, "transport", "detail"), transport); err != nil {
		t.Fatal(err)
	}
	detail := inventory
	detail.Observation.Capture, detail.FollowUps = capture, nil
	artifactPlan := f.plan(fmt.Sprintf("request-%032x", 1001), "", model.PurposeArtifact)
	artifactPlan.ParentCapture = capture.CaptureKey
	nextRequest := SourceRequestPlan{Plan: artifactPlan, Role: model.ArtifactRoleMain, Required: true, HostKey: "example.com", RetryStartedAt: time.Now().Add(-time.Second).UTC().Truncate(time.Microsecond), ArtifactKey: detail.Artifacts[0].ArtifactKey}
	acceptance := CaptureAcceptance{RequestKey: key, Capture: capture, Detail: &detail, Requests: []SourceRequestPlan{nextRequest}}
	work := sourceWork(f.doc, "bronze", "detail")
	first, err := f.sources.ApplyCapture(f.db.ctx, work, acceptance)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.sources.ApplyCapture(f.db.ctx, work, acceptance)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatalf("detail acceptance replay changed receipt: %v", err)
	}
	if f.count(t, `SELECT count(*) FROM bronze.source_record`) != 1 || f.count(t, `SELECT count(*) FROM bronze.observation_revision`) != 2 || f.count(t, `SELECT count(*) FROM bronze.blob`) != 1 || f.count(t, `SELECT count(*) FROM bronze.artifact`) != 0 {
		t.Fatal("detail acceptance failed to register immutable observation or invented an artifact")
	}
	registered, err := f.sources.ReadRequest(f.db.ctx, f.doc, artifactPlan.RequestKey)
	if err != nil || registered.OwnerKey == existing[0].ParentObservation {
		t.Fatalf("new artifact request lost accepted detail parent: %v", err)
	}
	if err := model.ValidateEnvelopeKey(registered.OwnerKey, "observation"); err != nil {
		t.Fatal(err)
	}
}

func TestSourcesAdapterPageRetryExhaustion(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	keys := []string{fmt.Sprintf("page-%032x", 1), fmt.Sprintf("page-%032x", 2)}
	for i, key := range keys {
		plan := SourcePagePlan{PageKey: key, Ordinal: int32(i), Plan: f.plan(fmt.Sprintf("request-%032x", i+1100), fmt.Sprint(i+1), model.PurposeListing), RetryStartedAt: time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)}
		if _, err := f.sources.RegisterPage(f.db.ctx, sourceWork(f.window, "plan", key), plan); err != nil {
			t.Fatal(err)
		}
	}
	for attempt := int32(1); attempt <= f.frozen.AttemptLimit; attempt++ {
		now := time.Now().Add(-time.Second)
		retry := SourceRetryReceipt{PageKey: keys[0], AttemptKey: fmt.Sprintf("request-%032x", attempt+1200), Attempt: attempt, Next: now, RetryAfter: now}
		work := sourceWork(f.window, "retry", fmt.Sprint(attempt))
		first, err := f.sources.RecordPageRetry(f.db.ctx, work, retry)
		if err != nil {
			t.Fatal(err)
		}
		again, err := f.sources.RecordPageRetry(f.db.ctx, work, retry)
		if err != nil || !reflect.DeepEqual(first, again) {
			t.Fatalf("source retry replay spent another attempt: %v", err)
		}
	}
	p, err := f.sources.ReadPage(f.db.ctx, f.window, keys[0])
	if err != nil || p.State != "failed" || p.Reason == nil || *p.Reason != "source_retry_exhausted" || p.SourceAttempts != f.frozen.AttemptLimit {
		t.Fatalf("source allowance did not fail page precisely: %v", err)
	}
	sibling, err := f.sources.ReadPage(f.db.ctx, f.window, keys[1])
	if err != nil || sibling.State != "failed" || sibling.Reason == nil || *sibling.Reason != "window_closed" {
		t.Fatalf("intrinsic retry failure did not close sibling: %v", err)
	}
}
