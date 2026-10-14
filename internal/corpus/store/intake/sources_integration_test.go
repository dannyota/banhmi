package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func newSourcesAdapterFixture(t *testing.T, options ...func(*FrozenSourceWindow)) *sourcesAdapterFixture {
	t.Helper()
	db := newSourceDatabaseFixture(t)
	app := boundedSourceAppConnection(t, db)
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
	for _, option := range options {
		option(&frozen)
	}
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
	return api.RequestPlan{Country: "vn", SourceID: "source-a", RequestKey: key, WindowKey: f.frozen.Request.WindowKey, Purpose: purpose, URL: "https://example.com/source", Method: "GET", Cursor: cursor, PolicyHash: fixtureDigest, BudgetHash: fixtureDigest, Budget: f.frozen.Request.Budget}
}

func (f *sourcesAdapterFixture) page(t *testing.T, ordinal int32, complete bool, observations bool, decorate ...func(*api.DecodedDiscoveryPage)) api.DecodedDiscoveryPage {
	t.Helper()
	key := fmt.Sprintf("page-%032x", ordinal+1)
	cursor := fmt.Sprint(ordinal + 1)
	plan := SourcePagePlan{PageKey: key, Ordinal: ordinal, Plan: f.plan(fmt.Sprintf("request-%032x", ordinal+100), cursor, model.PurposeListing), RetryStartedAt: time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)}
	if ordinal > 0 {
		plan.Plan.ParentCapture = fmt.Sprintf("capture-%032x", ordinal)
	}
	if _, err := f.sources.RegisterPage(f.db.ctx, sourceWork(f.window, "plan", key), plan); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.RegisterHost(f.db.ctx, sourceWork(f.window, "page-host", "example.com"), SourceHostPacing{HostKey: "example.com", NextAllowedAt: f.frozen.Request.Start, MaxInFlight: 1}); err != nil {
		t.Fatal(err)
	}
	claim, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "page-claim", key), key)
	if err != nil {
		t.Fatal(err)
	}
	capture := f.capture
	capture.CaptureKey = fmt.Sprintf("capture-%032x", ordinal+1)
	receipt := SourceCaptureReceipt{Key: key, ClaimResult: claim.ResultID, AttemptKey: claim.AttemptKey, Attempt: 1, Capture: capture}
	if _, err := f.sources.RecordPageCapture(f.db.ctx, sourceWork(f.window, "capture", key), receipt); err != nil {
		t.Fatal(err)
	}
	page := api.DecodedDiscoveryPage{PageKey: key, Capture: capture, Cursor: model.CursorPosition{WindowKey: f.frozen.Request.WindowKey, SliceKey: "keyword:0", InitialCursor: cursor, CommittedCursor: cursor, NextCursor: cursor, Complete: complete, Ordinal: 99}, Limits: model.RuleLimits{MaxItems: 100, MaxBytes: 1 << 20}}
	if complete {
		page.Cursor.NextCursor = "checkpoint"
	}
	if !complete {
		page.FollowUps = []model.FollowUpRequest{{RequestKey: fmt.Sprintf("request-%032x", ordinal+101), WindowKey: f.frozen.Request.WindowKey, ParentCapture: capture.CaptureKey, Purpose: model.PurposeListing, URL: plan.Plan.URL, Ordinal: int(ordinal+1), PolicyHash: fixtureDigest, BudgetHash: fixtureDigest, Budget: f.frozen.Request.Budget}}
	}
	if observations {
		page.Observations = []api.DecodedObservation{{SourceID: "source-a", ExternalID: "native-"+f.doc.Execution.Scope.Subject.LeaseKey(), VersionKey: f.doc.Execution.Scope.Subject.LeaseKey(), Capture: capture, Limits: page.Limits}}
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
	control := f.control(t)
	executionKey := f.childExecutionKey(t, f.doc)
	if _, err := f.sources.AcknowledgeLaunch(f.db.ctx, sourceWork(control, "acknowledge", request), SourceLaunchAcknowledgement{WindowKey: "window-a", RequestKey: request, Child: f.doc, ExecutionKey: executionKey}); err != nil {
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

func TestSourcesAdapterSpentPageBudgetClosesIncomplete(t *testing.T) {
	f := newSourcesAdapterFixture(t, func(window *FrozenSourceWindow) { window.PageBudget = 1 })
	page := f.page(t, 0, false, false)
	f.accept(t, page, 0)
	w, err := f.sources.ReadWindow(f.db.ctx, f.window)
	if err != nil || w.State != "incomplete" || w.Reason == nil || *w.Reason != "window_budget" || w.CommittedCursor != "1" {
		t.Fatalf("spent discovery budget did not close coverage: %v", err)
	}
	p, err := f.sources.ReadPage(f.db.ctx, f.window, page.PageKey)
	if err != nil || p.State != "accepted" || p.Reason != nil {
		t.Fatalf("aggregate budget invented a page failure: %v", err)
	}
}

func TestSourcesAdapterCoverageFindingBlocksCompletion(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	page := f.page(t, 0, true, false, func(page *api.DecodedDiscoveryPage) {
		page.Findings = []model.Finding{{ReasonCode: "coverage_unknown", Severity: model.SeverityBlocking, Scope: model.GapSourceWindow, ScopeKey: "window-a"}}
	})
	f.accept(t, page, 0)
	w, err := f.sources.ReadWindow(f.db.ctx, f.window)
	if err != nil || w.State != "incomplete" || w.Reason == nil || *w.Reason != "coverage_finding" || w.CommittedCursor != "1" {
		t.Fatalf("unresolved coverage finding advanced checkpoint: %v", err)
	}
	if f.count(t, `SELECT count(*) FROM ingest.discovery_page WHERE findings->0->>'ReasonCode' = 'coverage_unknown' AND state = 'accepted'`) != 1 {
		t.Fatal("coverage closure discarded the accepted finding receipt")
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
	claim, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "page-claim", page.PageKey), page.PageKey)
	if err != nil {
		t.Fatal(err)
	}
	capture := SourceCaptureReceipt{Key: page.PageKey, ClaimResult: claim.ResultID, AttemptKey: claim.AttemptKey, Attempt: 1, Capture: page.Capture}
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
			sequenceBefore := f.count(t, `SELECT last_value FROM ingest.execution_result_id_seq`)
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
				if (mismatch == "hash" || mismatch == "length") && f.count(t, `SELECT last_value FROM ingest.execution_result_id_seq`) != sequenceBefore {
					t.Fatal("staged verification occurred after a result insertion")
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
			changed := a
			changedArtifact := artifact
			changedArtifact.Required = false
			changed.Artifact = &changedArtifact
			if _, err := f.sources.ApplyCapture(f.db.ctx, work, changed); !errors.Is(err, run.ErrPayloadConflict) {
				t.Fatalf("changed Bronze acceptance did not conflict: %v", err)
			}
			if f.count(t, `SELECT count(*) FROM bronze.blob`) != 1 || f.count(t, `SELECT count(*) FROM bronze.source_record`) != 1 || f.count(t, `SELECT count(*) FROM bronze.observation_revision`) != 1 || f.count(t, `SELECT count(*) FROM bronze.artifact`) != 1 || f.count(t, `SELECT count(*) FROM bronze.observation_artifact`) != 1 || f.count(t, `SELECT count(*) FROM bronze.blob_retention_reference`) != 3 {
				t.Fatal("Bronze acceptance did not atomically retain all rows or deduplicate blob hash")
			}
			request, err := f.sources.ReadRequest(f.db.ctx, f.doc, claim.RequestKey)
			if err != nil || request.State != "verified" || request.SourceAttempts != 1 {
				t.Fatalf("Bronze receipt lost verified state or source count: %v", err)
			}
			// Use the application login, not SET ROLE or the migration login.
			tx, err := f.app.Begin(f.db.ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, deleteErr := tx.Exec(f.db.ctx, `DELETE FROM bronze.blob_retention_reference`)
			var permissionError interface{ SQLState() string }
			if !errors.As(deleteErr, &permissionError) || permissionError.SQLState() != "42501" {
				t.Fatalf("runtime DELETE did not retain denial assertion: %v", deleteErr)
			}
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := tx.Rollback(cleanup); err != nil {
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
	if _, err := f.sources.ApplyCapture(f.db.ctx, sourceWork(f.doc, "verify", "detail"), CaptureAcceptance{RequestKey: key, Capture: capture}); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT count(*) FROM bronze.observation_revision`) != 1 {
		t.Fatal("transport verification invented decoded detail metadata")
	}
	detail := inventory
	detail.Observation.Capture, detail.FollowUps = capture, nil
	detail.Artifacts = append([]model.ArtifactInventoryEntry(nil), detail.Artifacts...)
	detail.Artifacts[0].ArtifactKey = "artifact-new-detail"
	detail.Artifacts[0].URL = "https://example.com/new-detail-artifact"
	artifactPlan := f.plan(fmt.Sprintf("request-%032x", 1001), "", model.PurposeArtifact)
	artifactPlan.ParentCapture, artifactPlan.URL = capture.CaptureKey, detail.Artifacts[0].URL
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
	seed := f.page(t, 0, false, false, func(page *api.DecodedDiscoveryPage) {
		page.FollowUps = nil
		for i := 0; i < 2; i++ {
			page.FollowUps = append(page.FollowUps, model.FollowUpRequest{RequestKey: fmt.Sprintf("request-%032x", i+1100), WindowKey: "window-a", ParentCapture: page.Capture.CaptureKey, Purpose: model.PurposeListing, URL: "https://example.com/source", Ordinal: i+1, PolicyHash: fixtureDigest, BudgetHash: fixtureDigest, Budget: f.frozen.Request.Budget})
		}
	})
	f.accept(t, seed, 0)
	keys := []string{fmt.Sprintf("page-%032x", 2), fmt.Sprintf("page-%032x", 3)}
	for i, key := range keys {
		plan := SourcePagePlan{PageKey: key, Ordinal: int32(i+1), Plan: f.plan(fmt.Sprintf("request-%032x", i+1100), fmt.Sprint(i+2), model.PurposeListing), RetryStartedAt: time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)}
		plan.Plan.ParentCapture = seed.Capture.CaptureKey
		if _, err := f.sources.RegisterPage(f.db.ctx, sourceWork(f.window, "plan", key), plan); err != nil {
			t.Fatal(err)
		}
	}
	for attempt := int32(1); attempt <= f.frozen.AttemptLimit; attempt++ {
		claim, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "retry-claim", fmt.Sprint(attempt)), keys[0])
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().Add(-time.Second)
		retry := SourceRetryReceipt{PageKey: keys[0], ClaimResult: claim.ResultID, AttemptKey: claim.AttemptKey, Attempt: attempt, Next: now, RetryAfter: now}
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

func (f *sourcesAdapterFixture) control(t *testing.T) run.IntakeWork {
	t.Helper()
	work := f.window
	work.Execution.WorkflowID = "corpus-control/vn"
	work.Execution.WorkflowKind = run.ControlReconcile
	work.Execution.Scope = run.ExecutionScope{Kind: run.ScopeControl, CountryControl: "vn"}
	lease, err := f.ledger.ClaimLease(f.db.ctx, 1, work.Execution.Scope, work.Execution.Owner, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	work.Execution.AttemptFence = lease.AttemptFence
	if _, err := f.ledger.RegisterExecution(f.db.ctx, work, "control-run", nil); err != nil {
		t.Fatal(err)
	}
	return work
}

func (f *sourcesAdapterFixture) childExecutionKey(t *testing.T, work run.IntakeWork) string {
	t.Helper()
	var key string
	if err := f.app.QueryRow(f.db.ctx, `SELECT execution_key FROM ingest.execution WHERE workflow_id = $1 AND attempt_fence = $2 AND admission_epoch = $3`, work.Execution.WorkflowID, work.Execution.AttemptFence, work.Execution.AdmissionEpoch).Scan(&key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestSourcesAdapterGenericReceiptCannotSuppressSourceMutation(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	work := sourceWork(f.window, "generic-collision", "collision-window")
	work.Execution.Scope.Subject.SourceWindow = "collision-window"
	work.Execution.WorkflowID = "source-sync/vn/source-a/collision-window"
	lease, err := f.ledger.ClaimLease(f.db.ctx, 1, work.Execution.Scope, work.Execution.Owner, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	work.Execution.AttemptFence = lease.AttemptFence
	if _, err := f.ledger.RegisterExecution(f.db.ctx, work, "collision-run", nil); err != nil {
		t.Fatal(err)
	}
	frozen := f.frozen
	frozen.Request.WindowKey = "collision-window"
	raw, err := sourceJSON(frozen)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := sourceDigest(struct {
		Operation string
		Payload   json.RawMessage
	}{"open_window", raw})
	if err != nil {
		t.Fatal(err)
	}
	request := run.IntakeApplyRequest{Work: work, IdempotencyKey: run.IntakeIdempotencyKey(work), OutputDigest: model.Digest(digest), Outcome: run.OutcomeSucceeded}
	if _, err := f.ledger.Apply(f.db.ctx, request); err != nil {
		t.Fatal(err)
	}
	before := f.count(t, `SELECT count(*) FROM ingest.execution_result`)
	if _, err := f.sources.OpenWindow(f.db.ctx, work, frozen); !errors.Is(err, run.ErrPayloadConflict) {
		t.Fatalf("generic receipt suppressed source operation: %v", err)
	}
	if f.count(t, `SELECT count(*) FROM ingest.execution_result`) != before || f.count(t, `SELECT count(*) FROM ingest.source_window WHERE window_key = 'collision-window'`) != 0 {
		t.Fatal("source conflict changed generic receipt")
	}
}

func TestSourcesAdapterDiscoveryPlanProvenance(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	plan := SourcePagePlan{PageKey: fmt.Sprintf("page-%032x", 80), Plan: f.plan(fmt.Sprintf("request-%032x", 80), "999", model.PurposeListing), RetryStartedAt: time.Now()}
	before := f.count(t, `SELECT count(*) FROM ingest.execution_result`)
	if _, err := f.sources.RegisterPage(f.db.ctx, sourceWork(f.window, "bad-first", "page"), plan); !errors.Is(err, run.ErrPayloadConflict) {
		t.Fatalf("first plan ignored frozen cursor: %v", err)
	}
	first := f.page(t, 0, false, false)
	plan.Ordinal, plan.Plan.Cursor = 1, "999"
	plan.Plan.ParentCapture = first.Capture.CaptureKey
	if _, err := f.sources.RegisterPage(f.db.ctx, sourceWork(f.window, "unrecorded-next", "page"), plan); !errors.Is(err, run.ErrPayloadConflict) {
		t.Fatalf("unrecorded continuation was admitted: %v", err)
	}
	if f.count(t, `SELECT count(*) FROM ingest.discovery_page`) != 1 || before >= f.count(t, `SELECT count(*) FROM ingest.execution_result`) {
		t.Fatal("invalid plan wrote a page or lost valid receipts")
	}
	f.accept(t, first, 0)
	w, err := f.sources.ReadWindow(f.db.ctx, f.window)
	if err != nil || w.State == "complete" || w.CommittedCursor != "1" {
		t.Fatal("unresolved continuation advanced watermark")
	}
}

func TestSourcesAdapterLaunchAssociationRejectsSubstitution(t *testing.T) {
	for _, mode := range []string{"substituted", "ambiguous", "missing"} {
		t.Run(mode, func(t *testing.T) {
			f := newSourcesAdapterFixture(t)
			page := f.page(t, 0, true, true, func(page *api.DecodedDiscoveryPage) {
				other := page.Observations[0]
				other.ExternalID, other.VersionKey, other.Ordinal = "native-b", "version-b", 1
				if mode == "ambiguous" {
					other.Ordinal = 0
				}
				page.Observations = append(page.Observations, other)
				for i := 0; i < 2; i++ {
					page.FollowUps = append(page.FollowUps, model.FollowUpRequest{RequestKey: fmt.Sprintf("request-%032x", 2000+i), WindowKey: "window-a", ParentCapture: page.Capture.CaptureKey, Purpose: model.PurposeDetail, URL: "https://example.com/detail", Ordinal: i, PolicyHash: fixtureDigest, BudgetHash: fixtureDigest, Budget: f.frozen.Request.Budget})
				}
			})
			acceptance := DiscoveryAcceptance{Page: page, Ordinal: 0}
			for _, follow := range page.FollowUps {
				acceptance.Launches = append(acceptance.Launches, SourceLaunch{RequestKey: follow.RequestKey, Work: f.doc, DueAt: f.frozen.Request.Cutoff})
			}
			if mode == "missing" {
				acceptance.Launches = acceptance.Launches[:1]
			}
			before := f.count(t, `SELECT count(*) FROM ingest.execution_result`)
			if _, err := f.sources.ApplyDiscoveryPage(f.db.ctx, sourceWork(f.window, "substituted", "page"), acceptance); !errors.Is(err, run.ErrPayloadConflict) {
				t.Fatalf("substituted or ambiguous launch was accepted: %v", err)
			}
			if f.count(t, `SELECT count(*) FROM ingest.delivery_record WHERE delivery_kind = 'launch'`) != 0 || f.count(t, `SELECT count(*) FROM bronze.observation_revision`) != 0 || f.count(t, `SELECT count(*) FROM ingest.execution_result`) != before {
				t.Fatal("invalid launch acceptance left partial evidence")
			}
		})
	}
}

func TestSourcesAdapterIncompleteAcceptancePreservesBlocker(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	first, later := f.page(t, 0, false, false), f.page(t, 1, false, false)
	f.accept(t, first, 0)
	if _, err := f.sources.CloseWindow(f.db.ctx, sourceWork(f.window, "block", "coverage"), SourceWindowClosure{Reason: "owner_blocker"}); err != nil {
		t.Fatal(err)
	}
	f.accept(t, later, 1)
	w, err := f.sources.ReadWindow(f.db.ctx, f.window)
	if err != nil || w.State != "incomplete" || w.Reason == nil || *w.Reason != "owner_blocker" || w.CommittedCursor != "1" {
		t.Fatalf("recorded evidence silently resumed window: %v", err)
	}
	if _, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "blocked-claim", "page"), later.PageKey); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("acceptance reopened fetch eligibility: %v", err)
	}
}

func TestSourcesAdapterFrozenDeadlineExhaustion(t *testing.T) {
	f := newSourcesAdapterFixture(t, func(window *FrozenSourceWindow) { window.Deadline = time.Now().Add(3*time.Second).UTC().Truncate(time.Microsecond) })
	page := f.page(t, 0, false, false)
	timer := time.NewTimer(time.Until(f.frozen.Deadline) + time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-f.db.ctx.Done():
		t.Fatal(f.db.ctx.Err())
	}
	if _, err := f.sources.ReconcileWindow(f.db.ctx, sourceWork(f.window, "deadline", "window")); err != nil {
		t.Fatal(err)
	}
	f.accept(t, page, 0)
	w, err := f.sources.ReadWindow(f.db.ctx, f.window)
	if err != nil || w.State != "incomplete" || w.Reason == nil || *w.Reason != "window_deadline" || w.CommittedCursor != "1" {
		t.Fatalf("deadline acceptance reopened window: %v", err)
	}
	if _, err := f.sources.ResumeWindow(f.db.ctx, sourceWork(f.window, "resume", "deadline")); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("spent frozen deadline resumed: %v", err)
	}
}

func TestSourcesAdapterSpentRequestBudgetAcceptance(t *testing.T) {
	f := newSourcesAdapterFixture(t, func(window *FrozenSourceWindow) { window.RequestBudget = 1 })
	_, _ = f.requests(t, 1)
	later := f.page(t, 1, false, false)
	if _, err := f.sources.ReconcileWindow(f.db.ctx, sourceWork(f.window, "budget", "window")); err != nil {
		t.Fatal(err)
	}
	f.accept(t, later, 1)
	w, err := f.sources.ReadWindow(f.db.ctx, f.window)
	if err != nil || w.State != "incomplete" || w.Reason == nil || *w.Reason != "window_budget" || w.CommittedCursor != "1" {
		t.Fatalf("spent request budget was cleared by evidence: %v", err)
	}
	if _, err := f.sources.ResumeWindow(f.db.ctx, sourceWork(f.window, "resume", "budget")); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("spent aggregate budget resumed: %v", err)
	}
}

func TestSourcesAdapterCancellationAcknowledgesStartedChild(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	request := fmt.Sprintf("request-%032x", 2100)
	page := f.page(t, 0, true, true, func(page *api.DecodedDiscoveryPage) {
		page.FollowUps = []model.FollowUpRequest{{RequestKey: request, WindowKey: "window-a", ParentCapture: page.Capture.CaptureKey, Purpose: model.PurposeDetail, URL: "https://example.com/detail", PolicyHash: fixtureDigest, BudgetHash: fixtureDigest, Budget: f.frozen.Request.Budget}}
	})
	acceptance := DiscoveryAcceptance{Page: page, Ordinal: 0, Launches: []SourceLaunch{{RequestKey: request, Work: f.doc, DueAt: f.frozen.Request.Cutoff}}}
	if _, err := f.sources.ApplyDiscoveryPage(f.db.ctx, sourceWork(f.window, "accept", page.PageKey), acceptance); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Exec(f.db.ctx, `UPDATE ingest.execution_lease SET cancelled_at = clock_timestamp() WHERE resource_kind = 'source_window' AND resource_key = 'window-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.ApplyDiscoveryPage(f.db.ctx, sourceWork(f.window, "cancelled-launch", "page"), acceptance); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("cancelled owner wrote launches: %v", err)
	}
	control := f.control(t)
	ack := SourceLaunchAcknowledgement{WindowKey: "window-a", RequestKey: request, Child: f.doc, ExecutionKey: f.childExecutionKey(t, f.doc)}
	changed := ack
	changed.ExecutionKey = fmt.Sprintf("execution-%032x", 999)
	if _, err := f.sources.AcknowledgeLaunch(f.db.ctx, sourceWork(control, "wrong-receipt", request), changed); !errors.Is(err, run.ErrPayloadConflict) {
		t.Fatalf("wrong child receipt acknowledged: %v", err)
	}
	work := sourceWork(control, "acknowledge", request)
	first, err := f.sources.AcknowledgeLaunch(f.db.ctx, work, ack)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.sources.AcknowledgeLaunch(f.db.ctx, work, ack)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatalf("cancellation acknowledgement replay changed receipt: %v", err)
	}
	if f.count(t, `SELECT count(*) FROM ingest.source_window WHERE state = 'complete' AND committed_cursor = 'checkpoint'`) != 1 || f.count(t, `SELECT count(*) FROM ingest.delivery_record WHERE delivery_kind = 'launch' AND acknowledged_at IS NOT NULL`) != 1 {
		t.Fatal("cancelled parent could not close existing launch accounting")
	}
	changed = ack
	changed.RequestKey = fmt.Sprintf("request-%032x", 2101)
	if _, err := f.sources.AcknowledgeLaunch(f.db.ctx, sourceWork(control, "missing-delivery", request), changed); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("missing launch was acknowledged: %v", err)
	}
	stale := work
	stale.Execution.AttemptFence++
	if _, err := f.sources.AcknowledgeLaunch(f.db.ctx, stale, ack); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("stale reconciler acknowledged launch: %v", err)
	}
	changed = ack
	changed.Child.InputDigest = model.Digest(strings.Repeat("b", 64))
	if _, err := f.sources.AcknowledgeLaunch(f.db.ctx, work, changed); !errors.Is(err, run.ErrPayloadConflict) {
		t.Fatalf("changed acknowledgement replay did not conflict: %v", err)
	}
}

func TestSourcesAdapterDiscoveryHostClaimAndRecovery(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	key := fmt.Sprintf("page-%032x", 1)
	plan := SourcePagePlan{PageKey: key, Plan: f.plan(fmt.Sprintf("request-%032x", 100), "1", model.PurposeListing), RetryStartedAt: f.frozen.Request.Cutoff}
	if _, err := f.sources.RegisterPage(f.db.ctx, sourceWork(f.window, "plan", key), plan); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if _, err := f.sources.RegisterHost(f.db.ctx, sourceWork(f.window, "host", "future"), SourceHostPacing{HostKey: "EXAMPLE.COM.", NextAllowedAt: future, MaxInFlight: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "host-blocked", key), key); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("discovery ignored host pacing: %v", err)
	}
	receipt := SourceCaptureReceipt{Key: key, AttemptKey: sourceAttemptKey(key, 1), Attempt: 1, Capture: f.capture}
	if _, err := f.sources.RecordPageCapture(f.db.ctx, sourceWork(f.window, "unclaimed", key), receipt); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("unclaimed discovery bypassed pacing: %v", err)
	}
	// Set the two independent scheduling clocks separately.
	if _, err := f.app.Exec(f.db.ctx, `UPDATE ingest.source_rate_limit SET next_allowed_at = clock_timestamp() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Exec(f.db.ctx, `UPDATE ingest.execution_lease SET next_eligible_at = $1 WHERE resource_kind = 'source_window'`, future); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "lease-blocked", key), key); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("discovery ignored lease eligibility watermark: %v", err)
	}
	if _, err := f.app.Exec(f.db.ctx, `UPDATE ingest.execution_lease SET next_eligible_at = clock_timestamp() - interval '1 second' WHERE resource_kind = 'source_window'`); err != nil {
		t.Fatal(err)
	}
	claimWork := sourceWork(f.window, "claim", key)
	claim, err := f.sources.ClaimPage(f.db.ctx, claimWork, key)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.sources.ClaimPage(f.db.ctx, claimWork, key)
	if err != nil || !reflect.DeepEqual(claim, again) || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 1 {
		t.Fatal("discovery claim replay acquired another slot")
	}
	if _, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "second-claim", key), key); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("claimed discovery acquired a second slot: %v", err)
	}
	old := f.window
	if _, err := f.app.Exec(f.db.ctx, `UPDATE ingest.execution_lease SET expires_at = clock_timestamp() - interval '1 second' WHERE resource_kind = 'source_window'`); err != nil {
		t.Fatal(err)
	}
	lease, err := f.ledger.ReclaimLease(f.db.ctx, 1, f.window.Execution.Scope, f.window.Execution.Owner, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	f.window.Execution.AttemptFence = lease.AttemptFence
	if _, err := f.ledger.RegisterExecution(f.db.ctx, f.window, "discovery-recovery", nil); err != nil {
		t.Fatal(err)
	}
	recovery := sourceWork(f.window, "recover", key)
	if _, err := f.sources.RecoverPage(f.db.ctx, recovery, key, claim.ResultID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.RecoverPage(f.db.ctx, recovery, key, claim.ResultID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.RecoverPage(f.db.ctx, sourceWork(old, "stale-release", key), key, claim.ResultID); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("stale discovery owner released host: %v", err)
	}
	if f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 || f.count(t, `SELECT source_attempts FROM ingest.discovery_page`) != 0 {
		t.Fatal("discovery recovery leaked slot or spent source attempt")
	}
	newClaim, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "claim-recovered", key), key)
	if err != nil {
		t.Fatal(err)
	}
	if newClaim.NextAttempt != claim.NextAttempt || newClaim.AttemptKey != claim.AttemptKey {
		t.Fatal("infrastructure recovery changed durable discovery attempt identity")
	}
	receipt.ClaimResult, receipt.AttemptKey = newClaim.ResultID, newClaim.AttemptKey
	work := sourceWork(f.window, "capture", key)
	first, err := f.sources.RecordPageCapture(f.db.ctx, work, receipt)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.sources.RecordPageCapture(f.db.ctx, work, receipt)
	if err != nil || !reflect.DeepEqual(first, replay) || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 || f.count(t, `SELECT source_attempts FROM ingest.discovery_page`) != 1 {
		t.Fatal("discovery capture replay spent attempt or released twice")
	}
}

func TestSourcesAdapterRequestAndHostClocksAreIndependent(t *testing.T) {
	for _, hostBlocked := range []bool{false, true} {
		t.Run(fmt.Sprint(hostBlocked), func(t *testing.T) {
			f := newSourcesAdapterFixture(t)
			_, requests := f.requests(t, 1)
			future := time.Now().Add(time.Hour)
			query := `UPDATE ingest.artifact_request SET next_eligible_at = $1 WHERE request_key = $2`
			args := []any{future, requests[0].Plan.RequestKey}
			if hostBlocked {
				query, args = `UPDATE ingest.source_rate_limit SET next_allowed_at = $1`, []any{future}
			}
			if _, err := f.app.Exec(f.db.ctx, query, args...); err != nil {
				t.Fatal(err)
			}
			if _, err := f.sources.ClaimRequest(f.db.ctx, f.doc); !errors.Is(err, run.ErrStaleGrant) {
				t.Fatalf("claim ignored independent scheduling clock: %v", err)
			}
			if f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 {
				t.Fatal("ineligible claim consumed a slot")
			}
		})
	}
}

func TestSourcesAdapterAcceptedSiblingSurvivesIntrinsicClosure(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	accepted, pending := f.page(t, 0, false, false), f.page(t, 1, false, false)
	f.accept(t, accepted, 0)
	if _, err := f.sources.CloseWindow(f.db.ctx, sourceWork(f.window, "intrinsic", "page"), SourceWindowClosure{PageKey: pending.PageKey, Reason: "decoder_rejected"}); err != nil {
		t.Fatal(err)
	}
	p, err := f.sources.ReadPage(f.db.ctx, f.window, accepted.PageKey)
	if err != nil || p.State != "accepted" || p.Reason != nil || p.DecodedDigest == nil || p.CaptureKey == nil {
		t.Fatal("intrinsic closure changed accepted sibling evidence")
	}
	w, err := f.sources.ReadWindow(f.db.ctx, f.window)
	if err != nil || w.State != "failed" || w.CommittedCursor != "1" {
		t.Fatal("intrinsic closure advanced incomplete watermark")
	}
}

func boundedSourceAppConnection(t *testing.T, db sourceDatabaseFixture) *pgx.Conn {
	t.Helper()
	config := db.conn.Config().Copy()
	config.User = "banhmi_corpus_writer_app"
	conn, err := pgx.ConnectConfig(db.ctx, config)
	if err != nil {
		t.Fatal("connect disposable source application failed")
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := conn.Close(cleanup); err != nil {
			t.Errorf("close source application connection: %v", err)
		}
	})
	return conn
}

func TestSourcesAdapterAccountingSurvivesNewDetailParent(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	inventory, requests := f.requests(t, 1)
	for attempt := int32(1); attempt <= f.frozen.AttemptLimit; attempt++ {
		claim, err := f.sources.ClaimRequest(f.db.ctx, f.doc)
		if err != nil {
			t.Fatal(err)
		}
		now := f.frozen.Request.Start
		if _, err := f.sources.RecordTransport(f.db.ctx, sourceWork(f.doc, "exhaust-original", fmt.Sprint(attempt)), SourceTransportReceipt{RequestKey: claim.RequestKey, AttemptKey: claim.AttemptKey, Attempt: claim.NextAttempt, Next: now, RetryAfter: now}); err != nil {
			t.Fatal(err)
		}
	}
	original, err := f.sources.ReadRequest(f.db.ctx, f.doc, requests[0].Plan.RequestKey)
	if err != nil || original.State != "exhausted" {
		t.Fatal("original source allowance was not exhausted")
	}
	key := fmt.Sprintf("request-%032x", 2200)
	inventory.FollowUps = []model.FollowUpRequest{{RequestKey: key, WindowKey: "window-a", ParentCapture: inventory.Observation.Capture.CaptureKey, Purpose: model.PurposeDetail, URL: "https://example.com/detail", PolicyHash: fixtureDigest, BudgetHash: fixtureDigest, Budget: f.frozen.Request.Budget}}
	plan := f.plan(key, "", model.PurposeDetail)
	plan.URL, plan.ParentCapture = "https://example.com/detail", inventory.Observation.Capture.CaptureKey
	detailRequest := SourceRequestPlan{Plan: plan, ParentObservation: requests[0].ParentObservation, HostKey: "example.com", RetryStartedAt: f.frozen.Request.Cutoff}
	if _, err := f.sources.RegisterRequests(f.db.ctx, sourceWork(f.doc, "new-detail-plan", key), inventory, []SourceRequestPlan{detailRequest}); err != nil {
		t.Fatal(err)
	}
	claim, err := f.sources.ClaimRequest(f.db.ctx, f.doc)
	if err != nil {
		t.Fatal(err)
	}
	capture := f.capture
	capture.CaptureKey = fmt.Sprintf("capture-%032x", 2200)
	now := f.frozen.Request.Start
	if _, err := f.sources.RecordTransport(f.db.ctx, sourceWork(f.doc, "detail-transport", key), SourceTransportReceipt{RequestKey: key, AttemptKey: claim.AttemptKey, Attempt: claim.NextAttempt, Capture: &capture, Next: now, RetryAfter: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.ApplyCapture(f.db.ctx, sourceWork(f.doc, "verify-detail-only", key), CaptureAcceptance{RequestKey: key, Capture: capture}); err != nil {
		t.Fatal(err)
	}
	detail := inventory
	detail.Observation.Capture, detail.FollowUps = capture, nil
	acceptance := CaptureAcceptance{RequestKey: key, Capture: capture, Detail: &detail}
	work := sourceWork(f.doc, "accept-verified-detail", key)
	receipt, err := f.sources.AcceptDetail(f.db.ctx, work, acceptance)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.sources.AcceptDetail(f.db.ctx, work, acceptance)
	if err != nil || !reflect.DeepEqual(receipt, replay) {
		t.Fatal("verified detail acceptance did not replay")
	}
	changedDetail := detail
	changedDetail.Observation.Ordinal++
	changed := acceptance
	changed.Detail = &changedDetail
	if _, err := f.sources.AcceptDetail(f.db.ctx, sourceWork(f.doc, "changed-detail-work", key), changed); !errors.Is(err, run.ErrPayloadConflict) {
		t.Fatalf("new work rewrote accepted detail: %v", err)
	}
	verified, err := f.sources.ReadRequest(f.db.ctx, f.doc, key)
	if err != nil || verified.State != "verified" || verified.SourceAttempts != 1 || !captureMatches(verified.CaptureKey, verified.CaptureDigest, verified.CaptureBytes, verified.TransportReceipt, capture) {
		t.Fatal("detail decoding changed terminal transport receipt or counters")
	}
	observations, err := f.sources.AcceptedObservations(f.db.ctx, f.doc, receipt.ResultID)
	if err != nil || len(observations) != 1 {
		t.Fatal(err)
	}
	replacement := requests[0]
	replacement.ParentObservation = observations[0].ObservationKey
	replacement.Plan.ParentCapture = capture.CaptureKey
	replacement.Plan.RequestKey = fmt.Sprintf("request-%032x", 2201)
	replacement.RetryStartedAt = time.Now()
	if _, err := f.sources.RegisterRequests(f.db.ctx, sourceWork(f.doc, "reset-parent-allowance", key), detail, []SourceRequestPlan{replacement}); !errors.Is(err, run.ErrPayloadConflict) {
		t.Fatalf("new detail parent allocated unchanged artifact allowance: %v", err)
	}
	replacement.Plan.RequestKey, replacement.AccountingRequestKey = original.RequestKey, original.RequestKey
	if _, err := f.sources.RegisterRequests(f.db.ctx, sourceWork(f.doc, "link-parent-accounting", key), detail, []SourceRequestPlan{replacement}); err != nil {
		t.Fatal(err)
	}
	reused, err := f.sources.ReadRequest(f.db.ctx, f.doc, original.RequestKey)
	if err != nil || !reflect.DeepEqual(original, reused) || f.count(t, `SELECT count(*) FROM ingest.artifact_request WHERE purpose = 'artifact'`) != 1 {
		t.Fatal("explicit reuse changed frozen source accounting")
	}
}

func TestSourcesAdapterConcurrentRegistrationUsesGlobalKeyOrder(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	inventoryA, _ := f.requests(t, 0)
	appB := boundedSourceAppConnection(t, f.db)
	sourcesB, err := NewSources(appB, "writer-a", fixtureObjectStore{content: "synthetic source bytes"})
	if err != nil {
		t.Fatal(err)
	}
	ledgerB, err := NewLedger(appB, "writer-a", fixtureObjectStore{content: "synthetic source bytes"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second := *f
	second.app, second.sources, second.ledger = appB, sourcesB, ledgerB
	second.window.Execution.Scope.Subject.SourceWindow = "window-b"
	second.window.Execution.WorkflowID = "source-sync/vn/source-a/window-b"
	second.doc.Execution.Scope.Subject.DocumentVersion = "version-b"
	second.doc.Execution.WorkflowID = "document-ingest/vn/source-a/version-b"
	second.frozen.Request.WindowKey = "window-b"
	for _, work := range []*run.IntakeWork{&second.window, &second.doc} {
		lease, err := ledgerB.ClaimLease(f.db.ctx, 1, work.Execution.Scope, work.Execution.Owner, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		work.Execution.AttemptFence = lease.AttemptFence
		if _, err := ledgerB.RegisterExecution(f.db.ctx, *work, "race-second", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sourcesB.OpenWindow(f.db.ctx, second.window, second.frozen); err != nil {
		t.Fatal(err)
	}
	pageB := second.page(t, 0, false, true)
	receiptB := second.accept(t, pageB, 0)
	inventoryB := api.DecodedDetailResult{Observation: pageB.Observations[0], Limits: pageB.Limits}
	var resultA string
	if err := f.app.QueryRow(f.db.ctx, `SELECT r.result_key FROM ingest.discovery_page p JOIN ingest.execution_result r ON r.id = p.acceptance_result_id JOIN ingest.source_window w ON w.id = p.source_window_id WHERE w.window_key = 'window-a'`).Scan(&resultA); err != nil {
		t.Fatal(err)
	}
	observationsA, err := f.sources.AcceptedObservations(f.db.ctx, f.window, resultA)
	if err != nil || len(observationsA) != 1 {
		t.Fatal(err)
	}
	observationsB, err := second.sources.AcceptedObservations(f.db.ctx, second.window, receiptB.ResultID)
	if err != nil || len(observationsB) != 1 {
		t.Fatal(err)
	}
	for round := 0; round < 4; round++ {
		inventoryA.Artifacts, inventoryB.Artifacts = nil, nil
		var requestsA, requestsB []SourceRequestPlan
		for i := 0; i < 2; i++ {
			key := fmt.Sprintf("request-%032x", 2300+round*2+i)
			artifact := model.ArtifactInventoryEntry{ArtifactKey: fmt.Sprintf("race-artifact-%d-%d", round, i), Role: "main", URL: fmt.Sprintf("https://example.com/race/%d/%d", round, i), Ordinal: i, Required: true, Format: "html"}
			inventoryA.Artifacts = append(inventoryA.Artifacts, artifact)
			inventoryB.Artifacts = append(inventoryB.Artifacts, artifact)
			planA, planB := f.plan(key, "", model.PurposeArtifact), second.plan(key, "", model.PurposeArtifact)
			planA.URL, planB.URL = artifact.URL, artifact.URL
			planA.ParentCapture, planB.ParentCapture = inventoryA.Observation.Capture.CaptureKey, inventoryB.Observation.Capture.CaptureKey
			requestsA = append(requestsA, SourceRequestPlan{Plan: planA, ParentObservation: observationsA[0].ObservationKey, Role: model.ArtifactRoleMain, Required: true, HostKey: "example.com", RetryStartedAt: f.frozen.Request.Cutoff, ArtifactKey: artifact.ArtifactKey})
			requestsB = append(requestsB, SourceRequestPlan{Plan: planB, ParentObservation: observationsB[0].ObservationKey, Role: model.ArtifactRoleMain, Required: true, HostKey: "example.com", RetryStartedAt: f.frozen.Request.Cutoff, ArtifactKey: artifact.ArtifactKey})
		}
		requestsB[0], requestsB[1] = requestsB[1], requestsB[0]
		ctx, cancel := context.WithTimeout(f.db.ctx, 5*time.Second)
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			_, err := f.sources.RegisterRequests(ctx, sourceWork(f.doc, "race", fmt.Sprint(round)), inventoryA, requestsA)
			results <- err
		}()
		go func() {
			<-start
			_, err := second.sources.RegisterRequests(ctx, sourceWork(second.doc, "race", fmt.Sprint(round)), inventoryB, requestsB)
			results <- err
		}()
		close(start)
		succeeded, conflicted := 0, 0
		for i := 0; i < 2; i++ {
			select {
			case err := <-results:
				if err == nil {
					succeeded++
				} else if errors.Is(err, run.ErrPayloadConflict) {
					conflicted++
				} else {
					cancel()
					t.Fatalf("concurrent registration failed without classifying conflict: %v", err)
				}
			case <-ctx.Done():
				cancel()
				t.Fatal("concurrent registration exceeded bounded wait")
			}
		}
		cancel()
		if succeeded != 1 || conflicted != 1 || f.count(t, `SELECT count(*) FROM ingest.artifact_request`) != int64((round+1)*2) {
			t.Fatal("concurrent registration lost atomic batch or immutable comparison")
		}
	}
}

func TestSourcesAdapterRequestWatermarkSurvivesClosure(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	_, requests := f.requests(t, 1)
	claim, err := f.sources.ClaimRequest(f.db.ctx, f.doc)
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(30*time.Minute).UTC().Truncate(time.Microsecond)
	if _, err := f.sources.RecordTransport(f.db.ctx, sourceWork(f.doc, "future-backoff", "watermark"), SourceTransportReceipt{RequestKey: claim.RequestKey, AttemptKey: claim.AttemptKey, Attempt: claim.NextAttempt, Next: future, RetryAfter: f.frozen.Request.Start}); err != nil {
		t.Fatal(err)
	}
	due, err := f.sources.ReadRequest(f.db.ctx, f.doc, requests[0].Plan.RequestKey)
	if err != nil || due.EligibilityWatermark == nil || !due.EligibilityWatermark.Equal(future) || due.NextEligibleAt == nil || !due.NextEligibleAt.Equal(future) {
		t.Fatal("durable backoff did not retain eligibility watermark")
	}
	if _, err := f.sources.ClaimRequest(f.db.ctx, f.doc); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("host eligibility bypassed request watermark: %v", err)
	}
	if _, err := f.sources.ExpireRequest(f.db.ctx, sourceWork(f.doc, "close-watermark", "request"), due.RequestKey, "processing_closed"); err != nil {
		t.Fatal(err)
	}
	closed, err := f.sources.ReadRequest(f.db.ctx, f.doc, due.RequestKey)
	if err != nil || closed.State != "expired" || closed.NextEligibleAt != nil || closed.EligibilityWatermark == nil || !closed.EligibilityWatermark.Equal(future) || closed.SourceAttempts != 1 {
		t.Fatal("processing closure reset eligibility watermark or source count")
	}
}

func TestSourcesAdapterDiscoveryAndDocumentShareHostCapacity(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	_, requests := f.requests(t, 1)
	key := fmt.Sprintf("page-%032x", 2)
	plan := SourcePagePlan{PageKey: key, Ordinal: 1, Plan: f.plan(fmt.Sprintf("request-%032x", 101), "2", model.PurposeListing), RetryStartedAt: f.frozen.Request.Cutoff}
	plan.Plan.ParentCapture = f.capture.CaptureKey
	if _, err := f.sources.RegisterPage(f.db.ctx, sourceWork(f.window, "plan-shared-host", key), plan); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.ClaimRequest(f.db.ctx, f.doc); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "occupied-by-document", key), key); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("discovery bypassed document host slot: %v", err)
	}
	if _, err := f.sources.ExpireRequest(f.db.ctx, sourceWork(f.doc, "release-for-discovery", "request"), requests[0].Plan.RequestKey, "processing_closed"); err != nil {
		t.Fatal(err)
	}
	claim, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "claim-shared-host", key), key)
	if err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 1 {
		t.Fatal("discovery omitted shared host slot")
	}
	capture := f.capture
	capture.CaptureKey = fmt.Sprintf("capture-%032x", 2)
	if _, err := f.sources.RecordPageCapture(f.db.ctx, sourceWork(f.window, "capture-shared-host", key), SourceCaptureReceipt{Key: key, ClaimResult: claim.ResultID, AttemptKey: claim.AttemptKey, Attempt: 1, Capture: capture}); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 {
		t.Fatal("discovery did not release shared slot")
	}
}

func TestSourcesAdapterNumericAndWebFormsContinuationProvenance(t *testing.T) {
	for _, webForms := range []bool{false, true} {
		t.Run(fmt.Sprint(webForms), func(t *testing.T) {
			f := newSourcesAdapterFixture(t)
			f.page(t, 0, false, false, func(page *api.DecodedDiscoveryPage) {
				page.FollowUps = nil
				if webForms {
					page.Continuation = &api.DecodedContinuation{Target: "grid", Fields: []api.FormField{{Name: "state", Value: "synthetic-state"}}}
				}
			})
			second := f.page(t, 1, true, false)
			p, err := f.sources.ReadPage(f.db.ctx, f.window, second.PageKey)
			if err != nil || p.RequestCursor != "2" || p.State != "decoded" {
				t.Fatal("recorded numeric or WebForms continuation was rejected")
			}
		})
	}
}

func TestSourcesAdapterDistinctChildrenRequireBothAcknowledgements(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	other := f.doc
	other.Execution.WorkflowID = "document-ingest/vn/source-a/version-b"
	other.Execution.Scope.Subject.DocumentVersion = "version-b"
	lease, err := f.ledger.ClaimLease(f.db.ctx, 1, other.Execution.Scope, other.Execution.Owner, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	other.Execution.AttemptFence = lease.AttemptFence
	if _, err := f.ledger.RegisterExecution(f.db.ctx, other, "second-child", nil); err != nil {
		t.Fatal(err)
	}
	page := f.page(t, 0, true, true, func(page *api.DecodedDiscoveryPage) {
		second := page.Observations[0]
		second.ExternalID, second.VersionKey, second.Ordinal = "native-b", "version-b", 1
		page.Observations = append(page.Observations, second)
		for i := 0; i < 2; i++ {
			page.FollowUps = append(page.FollowUps, model.FollowUpRequest{RequestKey: fmt.Sprintf("request-%032x", 2400+i), WindowKey: "window-a", ParentCapture: page.Capture.CaptureKey, Purpose: model.PurposeDetail, URL: "https://example.com/detail", Ordinal: i, PolicyHash: fixtureDigest, BudgetHash: fixtureDigest, Budget: f.frozen.Request.Budget})
		}
	})
	children := []run.IntakeWork{f.doc, other}
	acceptance := DiscoveryAcceptance{Page: page, Ordinal: 0}
	for i, child := range children {
		acceptance.Launches = append(acceptance.Launches, SourceLaunch{RequestKey: page.FollowUps[i].RequestKey, Work: child, DueAt: f.frozen.Request.Cutoff})
	}
	if _, err := f.sources.ApplyDiscoveryPage(f.db.ctx, sourceWork(f.window, "accept-two", page.PageKey), acceptance); err != nil {
		t.Fatal(err)
	}
	control := f.control(t)
	for i, child := range children {
		request := page.FollowUps[i].RequestKey
		ack := SourceLaunchAcknowledgement{WindowKey: "window-a", RequestKey: request, Child: child, ExecutionKey: f.childExecutionKey(t, child)}
		if _, err := f.sources.AcknowledgeLaunch(f.db.ctx, sourceWork(control, "ack-two", request), ack); err != nil {
			t.Fatal(err)
		}
		w, err := f.sources.ReadWindow(f.db.ctx, f.window)
		if err != nil || i == 0 && (w.State != "progressing" || w.CommittedCursor != "1") || i == 1 && (w.State != "complete" || w.CommittedCursor != "checkpoint") {
			t.Fatalf("completion did not wait for exact child acknowledgements: %v", err)
		}
	}
}
