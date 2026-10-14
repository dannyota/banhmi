package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/source/api"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

type sourcesAdapterFixture struct {
	db           sourceDatabaseFixture
	app          *pgx.Conn
	sources      *Sources
	ledger       *Ledger
	window       run.IntakeWork
	doc          run.IntakeWork
	frozen       FrozenSourceWindow
	capture      model.CaptureRef
	continuation *api.DecodedContinuation
}

func newSourcesAdapterFixture(t *testing.T, options ...func(*FrozenSourceWindow)) *sourcesAdapterFixture {
	t.Helper()
	db := boundedAdapterDatabaseFixture(t)
	app := boundedSourceAppConnection(t, db)
	objects := fixtureObjectStore{content: "synthetic source bytes"}
	s, err := NewSources(app, "writer-a", objects, fixtureSourceContinuer{})
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
		if f.continuation != nil {
			form := url.Values{"page_size": {"50"}}
			for _, field := range f.continuation.Fields {
				form.Set(field.Name, field.Value)
			}
			form.Set("__EVENTTARGET", f.continuation.Target)
			form.Set("__EVENTARGUMENT", "Page$"+cursor)
			plan.Plan.Method, plan.Plan.Body = "POST", []byte(form.Encode())
			plan.Plan.Headers = []api.RequestHeader{{Name: "Content-Type", Value: "application/x-www-form-urlencoded"}, {Name: "Origin", Value: "https://example.com"}, {Name: "Referer", Value: plan.Plan.URL}}
		}
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
		page.FollowUps = []model.FollowUpRequest{{RequestKey: fmt.Sprintf("request-%032x", ordinal+101), WindowKey: f.frozen.Request.WindowKey, ParentCapture: capture.CaptureKey, Purpose: model.PurposeListing, URL: plan.Plan.URL, Ordinal: int(ordinal + 1), PolicyHash: fixtureDigest, BudgetHash: fixtureDigest, Budget: f.frozen.Request.Budget}}
	}
	if observations {
		page.Observations = []api.DecodedObservation{{SourceID: "source-a", ExternalID: "native-" + f.doc.Execution.Scope.Subject.LeaseKey(), VersionKey: f.doc.Execution.Scope.Subject.LeaseKey(), Capture: capture, Limits: page.Limits}}
	}
	for _, fn := range decorate {
		fn(&page)
	}
	if _, err := f.sources.RecordPageDecode(f.db.ctx, sourceWork(f.window, "decode", key), page); err != nil {
		t.Fatal(err)
	}
	f.continuation = page.Continuation
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
	control := f.window
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
		{"capture", func() (run.IntakeReceipt, error) {
			return f.sources.RecordPageCapture(f.db.ctx, sourceWork(f.window, "capture", page.PageKey), capture)
		}, func() (run.IntakeReceipt, error) {
			changed := capture
			changed.Capture.Asset.ByteSize++
			return f.sources.RecordPageCapture(f.db.ctx, sourceWork(f.window, "capture", page.PageKey), changed)
		}},
		{"decode", func() (run.IntakeReceipt, error) {
			return f.sources.RecordPageDecode(f.db.ctx, sourceWork(f.window, "decode", page.PageKey), page)
		}, func() (run.IntakeReceipt, error) {
			changed := page
			changed.Cursor.Ordinal++
			return f.sources.RecordPageDecode(f.db.ctx, sourceWork(f.window, "decode", page.PageKey), changed)
		}},
		{"accept", func() (run.IntakeReceipt, error) {
			return f.sources.ApplyDiscoveryPage(f.db.ctx, sourceWork(f.window, "accept", page.PageKey), acceptance)
		}, func() (run.IntakeReceipt, error) {
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
			sourceCleanup(t, f.db.ctx, tx.Rollback)
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
			page.FollowUps = append(page.FollowUps, model.FollowUpRequest{RequestKey: fmt.Sprintf("request-%032x", i+1100), WindowKey: "window-a", ParentCapture: page.Capture.CaptureKey, Purpose: model.PurposeListing, URL: "https://example.com/source", Ordinal: i + 1, PolicyHash: fixtureDigest, BudgetHash: fixtureDigest, Budget: f.frozen.Request.Budget})
		}
	})
	f.accept(t, seed, 0)
	keys := []string{fmt.Sprintf("page-%032x", 2), fmt.Sprintf("page-%032x", 3)}
	for i, key := range keys {
		plan := SourcePagePlan{PageKey: key, Ordinal: int32(i + 1), Plan: f.plan(fmt.Sprintf("request-%032x", i+1100), fmt.Sprint(i+2), model.PurposeListing), RetryStartedAt: time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)}
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
	f := newSourcesAdapterFixture(t, func(window *FrozenSourceWindow) {
		window.Deadline = time.Now().Add(3 * time.Second).UTC().Truncate(time.Microsecond)
	})
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
	control := f.window
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
	if newClaim.NextAttempt != claim.NextAttempt || newClaim.AttemptKey == claim.AttemptKey {
		t.Fatal("replacement did not retain source ordinal and allocate a fresh claim identity")
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
		sourceCleanup(t, db.ctx, conn.Close)
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
	sourcesB, err := NewSources(boundedAdapterTransactions{appB}, "writer-a", fixtureObjectStore{content: "synthetic source bytes"})
	if err != nil {
		t.Fatal(err)
	}
	ledgerB, err := NewLedger(boundedAdapterTransactions{appB}, "writer-a", fixtureObjectStore{content: "synthetic source bytes"}, nil)
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
	future := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Microsecond)
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
	control := f.window
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

// boundedAdapterDatabaseFixture keeps every adapter cleanup independent of the
// work deadline. The shared schema fixture remains owned by its separate slice.
func boundedAdapterDatabaseFixture(t *testing.T) sourceDatabaseFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("source adapter tests require hosted PostgreSQL")
	}
	u := ledgerTestURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	adminURL := *u
	adminURL.User, adminURL.Path = url.User("postgres"), "/postgres"
	admin, err := pgx.Connect(ctx, adminURL.String())
	if err != nil {
		t.Fatal("connect disposable source administrator failed")
	}
	t.Cleanup(func() {
		sourceCleanup(t, ctx, admin.Close)
	})
	name := fmt.Sprintf("banhmi_source_%d_%d", os.Getpid(), time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier+" TEMPLATE banhmi_corpus_writer_ci OWNER banhmi_corpus_writer_migrator"); err != nil {
		t.Fatalf("copy replayed source database: %v", err)
	}
	t.Cleanup(func() {
		sourceCleanup(t, ctx, func(cleanup context.Context) error {
			_, err := admin.Exec(cleanup, "DROP DATABASE "+identifier+" WITH (FORCE)")
			return err
		})
	})
	u.Path = "/" + name
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal("connect disposable source migrator failed")
	}
	t.Cleanup(func() {
		sourceCleanup(t, ctx, conn.Close)
	})
	if _, err := conn.Exec(ctx, `INSERT INTO ingest.country_admission
		(id, country_code, admission_epoch, admitted_writer, enabled) VALUES (1,'vn',1,'writer-a',true)`); err != nil {
		t.Fatal(err)
	}
	return sourceDatabaseFixture{ctx: ctx, conn: conn, u: u}
}

func (f *sourcesAdapterFixture) requestedPage(t *testing.T, key string) SourcePagePlan {
	t.Helper()
	plan := SourcePagePlan{PageKey: key, Plan: f.plan(fmt.Sprintf("request-%032x", 100), "1", model.PurposeListing), RetryStartedAt: f.frozen.Request.Cutoff}
	if _, err := f.sources.RegisterPage(f.db.ctx, sourceWork(f.window, "plan-requested", key), plan); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.RegisterHost(f.db.ctx, sourceWork(f.window, "host-requested", "example.com"), SourceHostPacing{HostKey: "example.com", NextAllowedAt: f.frozen.Request.Start, MaxInFlight: 1}); err != nil {
		t.Fatal(err)
	}
	return plan
}

func (f *sourcesAdapterFixture) reclaimWindow(t *testing.T, epoch int64) {
	t.Helper()
	if epoch == f.window.Execution.AdmissionEpoch {
		if _, err := f.app.Exec(f.db.ctx, `UPDATE ingest.execution_lease SET expires_at = clock_timestamp() - interval '1 second' WHERE resource_kind = 'source_window' AND resource_key = $1`, f.window.Execution.Scope.Subject.LeaseKey()); err != nil {
			t.Fatal(err)
		}
	}
	lease, err := f.ledger.ReclaimLease(f.db.ctx, epoch, f.window.Execution.Scope, f.window.Execution.Owner, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	f.window.Execution.AdmissionEpoch, f.window.Execution.AttemptFence = epoch, lease.AttemptFence
	if _, err := f.ledger.RegisterExecution(f.db.ctx, f.window, "source-reconciliation", nil); err != nil {
		t.Fatal(err)
	}
}

func TestSourcesAdapterRotationBeforeLaunchAcknowledgement(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	request := fmt.Sprintf("request-%032x", 2500)
	page := f.page(t, 0, true, true, func(page *api.DecodedDiscoveryPage) {
		page.FollowUps = []model.FollowUpRequest{{RequestKey: request, WindowKey: "window-a", ParentCapture: page.Capture.CaptureKey, Purpose: model.PurposeDetail, URL: "https://example.com/detail", PolicyHash: fixtureDigest, BudgetHash: fixtureDigest, Budget: f.frozen.Request.Budget}}
	})
	if _, err := f.sources.ApplyDiscoveryPage(f.db.ctx, sourceWork(f.window, "accept-rotation", page.PageKey), DiscoveryAcceptance{Page: page, Launches: []SourceLaunch{{RequestKey: request, Work: f.doc, DueAt: f.frozen.Request.Cutoff}}}); err != nil {
		t.Fatal(err)
	}
	old := f.window
	ack := SourceLaunchAcknowledgement{WindowKey: "window-a", RequestKey: request, Child: f.doc, ExecutionKey: f.childExecutionKey(t, f.doc)}
	if _, err := f.ledger.RotateAdmission(f.db.ctx, 1, "writer-a", true); err != nil {
		t.Fatal(err)
	}
	f.reclaimWindow(t, 2)
	if _, err := f.sources.AcknowledgeLaunch(f.db.ctx, sourceWork(old, "old-ack", request), ack); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("old admission acknowledged launch: %v", err)
	}
	changed := ack
	changed.Child.Execution.AdmissionEpoch = 2
	if _, err := f.sources.AcknowledgeLaunch(f.db.ctx, sourceWork(f.window, "rewritten-child", request), changed); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("rewritten historical child acknowledged: %v", err)
	}
	work := sourceWork(f.window, "rotated-ack", request)
	first, err := f.sources.AcknowledgeLaunch(f.db.ctx, work, ack)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.sources.AcknowledgeLaunch(f.db.ctx, work, ack)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatalf("historical acknowledgement replay changed: %v", err)
	}
	w, err := f.sources.ReadWindow(f.db.ctx, f.window)
	if err != nil || w.State != "complete" || f.count(t, `SELECT expected_epoch FROM ingest.delivery_record WHERE delivery_kind = 'launch'`) != 1 {
		t.Fatalf("rotation changed launch provenance or prevented completion: %v", err)
	}
}

func TestSourcesAdapterControlRequiresExplicitLaunchTransfer(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	request := fmt.Sprintf("request-%032x", 2600)
	page := f.page(t, 0, true, true, func(page *api.DecodedDiscoveryPage) {
		page.FollowUps = []model.FollowUpRequest{{RequestKey: request, WindowKey: "window-a", ParentCapture: page.Capture.CaptureKey, Purpose: model.PurposeDetail, URL: "https://example.com/detail", PolicyHash: fixtureDigest, BudgetHash: fixtureDigest, Budget: f.frozen.Request.Budget}}
	})
	if _, err := f.sources.ApplyDiscoveryPage(f.db.ctx, sourceWork(f.window, "accept-transfer", page.PageKey), DiscoveryAcceptance{Page: page, Launches: []SourceLaunch{{RequestKey: request, Work: f.doc, DueAt: f.frozen.Request.Cutoff}}}); err != nil {
		t.Fatal(err)
	}
	control := f.control(t)
	ack := SourceLaunchAcknowledgement{WindowKey: "window-a", RequestKey: request, Child: f.doc, ExecutionKey: f.childExecutionKey(t, f.doc)}
	work := sourceWork(control, "transferred-ack", request)
	if _, err := f.sources.AcknowledgeLaunch(f.db.ctx, work, ack); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("country control consumed untransferred intent: %v", err)
	}
	if _, err := f.sources.TransferLaunch(f.db.ctx, sourceWork(f.window, "transfer", request), SourceLaunchTransfer{RequestKey: request, RecipientWorkflowID: control.Execution.WorkflowID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.AcknowledgeLaunch(f.db.ctx, work, ack); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT count(*) FROM ingest.delivery_record WHERE delivery_kind = 'launch'`) != 1 {
		t.Fatal("transfer created a new child launch")
	}
}

func TestSourcesAdapterCaptureCannotConsumeAnotherPageClaim(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	first := f.page(t, 0, false, false)
	var prior SourceCaptureReceipt
	if err := f.app.QueryRow(f.db.ctx, `SELECT payload->'SourcePayload' FROM ingest.execution_result WHERE payload->>'SourceOperation' = 'page_capture'`).Scan(&prior); err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("page-%032x", 2)
	plan := SourcePagePlan{PageKey: key, Ordinal: 1, Plan: f.plan(fmt.Sprintf("request-%032x", 101), "2", model.PurposeListing), RetryStartedAt: f.frozen.Request.Cutoff}
	plan.Plan.ParentCapture = first.Capture.CaptureKey
	if _, err := f.sources.RegisterPage(f.db.ctx, sourceWork(f.window, "plan-second", key), plan); err != nil {
		t.Fatal(err)
	}
	claim, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "claim-second", key), key)
	if err != nil {
		t.Fatal(err)
	}
	changed := prior
	changed.ClaimResult = claim.ResultID
	if _, err := f.sources.RecordPageCapture(f.db.ctx, sourceWork(f.window, "substitute-claim", first.PageKey), changed); !errors.Is(err, run.ErrPayloadConflict) {
		t.Fatalf("capture substitution consumed another page claim: %v", err)
	}
	if f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 1 {
		t.Fatal("rejected substitution released another page slot")
	}
	f.reclaimWindow(t, 1)
	if _, err := f.sources.RecoverPage(f.db.ctx, sourceWork(f.window, "recover-second", key), key, claim.ResultID); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 {
		t.Fatal("substitution stranded the real claim")
	}
}

// fixtureSourceContinuer represents the trusted planner dependency. It emits a
// complete encoded POST, including a fixed planner field absent from form state.
type fixtureSourceContinuer struct{}

// NextPage returns a complete synthetic planner POST without I/O.
func (fixtureSourceContinuer) NextPage(r api.ContinuationRequest) (api.RequestPlan, error) {
	if err := r.Validate(); err != nil {
		return api.RequestPlan{}, err
	}
	cursor, err := strconv.Atoi(r.Origin.Cursor)
	if err != nil {
		return api.RequestPlan{}, err
	}
	form := url.Values{"page_size": {"50"}, "__EVENTTARGET": {r.Target}, "__EVENTARGUMENT": {fmt.Sprintf("Page$%d", cursor+1)}}
	for _, field := range r.Fields {
		form.Set(field.Name, field.Value)
	}
	next := r.Origin
	next.RequestKey = fmt.Sprintf("request-%032x", cursor+100)
	next.Method, next.Body, next.Cursor, next.ParentCapture = "POST", []byte(form.Encode()), fmt.Sprint(cursor+1), r.ParentCapture.CaptureKey
	next.Headers = []api.RequestHeader{{Name: "Content-Type", Value: "application/x-www-form-urlencoded"}, {Name: "Origin", Value: "https://example.com"}, {Name: "Referer", Value: r.Origin.URL}}
	return next, nil
}

type boundedAdapterTransactions struct{ TransactionStarter }

// BeginTx wraps cleanup while preserving the transaction options.
func (db boundedAdapterTransactions) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	tx, err := db.TransactionStarter.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return boundedAdapterTx{tx}, nil
}

type boundedAdapterTx struct{ pgx.Tx }

// Rollback uses its own five-second deadline after the work context expires.
func (tx boundedAdapterTx) Rollback(_ context.Context) error {
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return tx.Tx.Rollback(cleanup)
}

func TestSourcesAdapterWebFormsRejectsChangedContinuationPlan(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	previous := f.page(t, 0, false, false, func(page *api.DecodedDiscoveryPage) {
		page.FollowUps = nil
		page.Continuation = &api.DecodedContinuation{Target: "grid", Fields: []api.FormField{{Name: "state", Value: "synthetic+&="}}}
	})
	origin := f.plan(fmt.Sprintf("request-%032x", 100), "1", model.PurposeListing)
	planned, err := (fixtureSourceContinuer{}).NextPage(api.ContinuationRequest{Origin: origin, WindowKey: "window-a", PolicyHash: fixtureDigest, BudgetHash: fixtureDigest, Budget: origin.Budget, Mode: f.frozen.Request.Mode, ParentCapture: previous.Capture, Target: previous.Continuation.Target, Fields: previous.Continuation.Fields})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*api.RequestPlan){
		"method": func(p *api.RequestPlan) { p.Method, p.Body = "GET", nil },
		"state": func(p *api.RequestPlan) {
			p.Body = []byte(strings.ReplaceAll(string(p.Body), "synthetic%2B%26%3D", "changed"))
		},
		"target": func(p *api.RequestPlan) { p.Body = []byte(strings.ReplaceAll(string(p.Body), "grid", "other")) },
		"page": func(p *api.RequestPlan) {
			p.Body = []byte(strings.ReplaceAll(string(p.Body), "Page%242", "Page%24999"))
		},
		"default": func(p *api.RequestPlan) {
			p.Body = []byte(strings.ReplaceAll(string(p.Body), "page_size=50", "page_size=999"))
		},
		"locator": func(p *api.RequestPlan) { p.URL = "https://example.com/other" },
		"cursor":  func(p *api.RequestPlan) { p.Cursor = "999" },
		"headers": func(p *api.RequestPlan) { p.Headers = nil },
	} {
		t.Run(name, func(t *testing.T) {
			changed := planned
			mutate(&changed)
			page := SourcePagePlan{PageKey: fmt.Sprintf("page-%032x", 2), Ordinal: 1, Plan: changed, RetryStartedAt: f.frozen.Request.Cutoff}
			before := f.count(t, `SELECT count(*) FROM ingest.execution_result`)
			if _, err := f.sources.RegisterPage(f.db.ctx, sourceWork(f.window, "alter-continuation", name), page); !errors.Is(err, run.ErrPayloadConflict) {
				t.Fatalf("changed continuation registered: %v", err)
			}
			if f.count(t, `SELECT count(*) FROM ingest.discovery_page`) != 1 || f.count(t, `SELECT count(*) FROM ingest.execution_result`) != before {
				t.Fatal("rejected continuation left accounting writes")
			}
		})
	}
	unconfigured, err := NewSources(boundedAdapterTransactions{f.app}, "writer-a", fixtureObjectStore{content: "synthetic source bytes"})
	if err != nil {
		t.Fatal(err)
	}
	page := SourcePagePlan{PageKey: fmt.Sprintf("page-%032x", 2), Ordinal: 1, Plan: planned, RetryStartedAt: f.frozen.Request.Cutoff}
	if _, err := unconfigured.RegisterPage(f.db.ctx, sourceWork(f.window, "unconfigured-continuation", "plan"), page); !errors.Is(err, run.ErrPayloadConflict) {
		t.Fatalf("WebForms accepted without trusted planner: %v", err)
	}
	if _, err := f.sources.RegisterPage(f.db.ctx, sourceWork(f.window, "trusted-continuation", "plan"), page); err != nil {
		t.Fatal(err)
	}
}

func TestSourcesAdapterClosureReleasesDiscoveryClaimOnce(t *testing.T) {
	for _, intrinsic := range []bool{false, true} {
		t.Run(fmt.Sprint(intrinsic), func(t *testing.T) {
			f := newSourcesAdapterFixture(t)
			first := f.page(t, 0, false, false)
			key := fmt.Sprintf("page-%032x", 2)
			plan := SourcePagePlan{PageKey: key, Ordinal: 1, Plan: f.plan(fmt.Sprintf("request-%032x", 101), "2", model.PurposeListing), RetryStartedAt: f.frozen.Request.Cutoff}
			plan.Plan.ParentCapture = first.Capture.CaptureKey
			if _, err := f.sources.RegisterPage(f.db.ctx, sourceWork(f.window, "plan-active", key), plan); err != nil {
				t.Fatal(err)
			}
			claim, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "claim-active", key), key)
			if err != nil {
				t.Fatal(err)
			}
			closure := SourceWindowClosure{Reason: "owner_blocker"}
			if intrinsic {
				closure.PageKey = first.PageKey
			}
			work := sourceWork(f.window, "close-active", key)
			firstReceipt, err := f.sources.CloseWindow(f.db.ctx, work, closure)
			if err != nil {
				t.Fatal(err)
			}
			secondReceipt, err := f.sources.CloseWindow(f.db.ctx, work, closure)
			if err != nil || !reflect.DeepEqual(firstReceipt, secondReceipt) || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 || f.count(t, `SELECT count(*) FROM ingest.discovery_page WHERE page_key = $1 AND claim_state = 'released'`, key) != 1 {
				t.Fatalf("closure leaked or double-released a claim: %v", err)
			}
			if _, err := f.sources.RecoverPage(f.db.ctx, sourceWork(f.window, "recover-closed", key), key, claim.ResultID); !errors.Is(err, run.ErrStaleGrant) {
				t.Fatalf("closure claim remained recoverable: %v", err)
			}
			p, err := f.sources.ReadPage(f.db.ctx, f.window, key)
			if err != nil || p.SourceAttempts != 0 || intrinsic && (p.State != "failed" || p.Reason == nil || *p.Reason != "window_closed") || !intrinsic && p.State != "requested" {
				t.Fatalf("closure changed source accounting or sibling outcome: %v", err)
			}
		})
	}
}

func TestSourcesAdapterIssuedFailureReconcilesAfterDeadlineClosure(t *testing.T) {
	f := newSourcesAdapterFixture(t, func(w *FrozenSourceWindow) {
		w.Deadline = time.Now().Add(3 * time.Second).UTC().Truncate(time.Microsecond)
	})
	key := fmt.Sprintf("page-%032x", 2700)
	f.requestedPage(t, key)
	claim, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "claim-deadline", key), key)
	if err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(time.Until(f.frozen.Deadline) + time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-f.db.ctx.Done():
		t.Fatal(f.db.ctx.Err())
	}
	if _, err := f.sources.ReconcileWindow(f.db.ctx, sourceWork(f.window, "close-deadline", key)); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 {
		t.Fatal("deadline reconciliation retained active slot")
	}
	retry := SourceRetryReceipt{PageKey: key, ClaimResult: claim.ResultID, AttemptKey: claim.AttemptKey, Attempt: claim.NextAttempt, Next: f.frozen.Request.Cutoff, RetryAfter: f.frozen.Request.Cutoff}
	work := sourceWork(f.window, "late-failure", key)
	first, err := f.sources.RecordPageRetry(f.db.ctx, work, retry)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.sources.RecordPageRetry(f.db.ctx, work, retry)
	if err != nil || !reflect.DeepEqual(first, again) || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 {
		t.Fatalf("late failure replay released twice: %v", err)
	}
	if replay, err := f.sources.RecordPageRetry(f.db.ctx, sourceWork(f.window, "duplicate-late-failure", key), retry); err != nil || !reflect.DeepEqual(replay, first) {
		t.Fatalf("identical claim outcome did not return its committed receipt: %v", err)
	}
	p, err := f.sources.ReadPage(f.db.ctx, f.window, key)
	if err != nil || p.SourceAttempts != 1 || p.State != "requested" {
		t.Fatalf("late failure lost source attempt or fabricated intrinsic failure: %v", err)
	}
	w, err := f.sources.ReadWindow(f.db.ctx, f.window)
	if err != nil || w.State != "incomplete" || w.Reason == nil || *w.Reason != "window_deadline" {
		t.Fatalf("late failure cleared deadline blocker: %v", err)
	}
}

func TestSourcesAdapterInfrastructureRecoveryCapsUnchangedAttempts(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	key := fmt.Sprintf("page-%032x", 2800)
	f.requestedPage(t, key)
	for infrastructure := int32(1); infrastructure <= 3; infrastructure++ {
		claim, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "infra-claim", fmt.Sprint(infrastructure)), key)
		if err != nil || claim.NextAttempt != 1 || claim.AttemptKey != discoveryAttemptKey("window-a", key, infrastructure) {
			t.Fatalf("infrastructure retry changed source allowance: %v", err)
		}
		f.reclaimWindow(t, 1)
		work := sourceWork(f.window, "infra-recover", fmt.Sprint(infrastructure))
		first, err := f.sources.RecoverPage(f.db.ctx, work, key, claim.ResultID)
		if err != nil {
			t.Fatal(err)
		}
		again, err := f.sources.RecoverPage(f.db.ctx, work, key, claim.ResultID)
		if err != nil || !reflect.DeepEqual(first, again) || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 {
			t.Fatalf("infrastructure recovery replay leaked accounting: %v", err)
		}
		p, err := f.sources.ReadPage(f.db.ctx, f.window, key)
		if err != nil || p.InfrastructureAttempts != infrastructure || p.SourceAttempts != 0 || infrastructure == 3 && (p.State != "failed" || p.Reason == nil || *p.Reason != "infrastructure_retry_exhausted") {
			t.Fatalf("infrastructure accounting was not durable: %v", err)
		}
	}
	if _, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "fourth-infra-claim", key), key); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("fourth unchanged infrastructure attempt admitted: %v", err)
	}
	if _, err := f.sources.ResumeWindow(f.db.ctx, sourceWork(f.window, "resume-infra", key)); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("resumption reset infrastructure exhaustion: %v", err)
	}
}

func TestSourcesAdapterExistingBlockerSurvivesCombinedReconciliation(t *testing.T) {
	for _, mode := range []string{"finding", "finding-budget", "finding-deadline", "finding-budget-deadline"} {
		t.Run(mode, func(t *testing.T) {
			f := newSourcesAdapterFixture(t, func(w *FrozenSourceWindow) {
				if strings.Contains(mode, "budget") {
					w.PageBudget = 1
				}
				if strings.Contains(mode, "deadline") {
					w.Deadline = time.Now().Add(3 * time.Second).UTC().Truncate(time.Microsecond)
				}
			})
			page := f.page(t, 0, false, false, func(page *api.DecodedDiscoveryPage) {
				page.Findings = []model.Finding{{ReasonCode: "coverage_unknown", Severity: model.SeverityBlocking, Scope: model.GapSourceWindow, ScopeKey: "window-a"}}
			})
			if _, err := f.sources.CloseWindow(f.db.ctx, sourceWork(f.window, "owner-blocker", mode), SourceWindowClosure{Reason: "owner_blocker"}); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(mode, "deadline") {
				timer := time.NewTimer(time.Until(f.frozen.Deadline) + time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-f.db.ctx.Done():
					t.Fatal(f.db.ctx.Err())
				}
			}
			f.accept(t, page, 0)
			w, err := f.sources.ReconcileWindow(f.db.ctx, sourceWork(f.window, "reconcile-blocker", mode))
			if err != nil || w.State != "incomplete" || w.Reason == nil || *w.Reason != "owner_blocker" || w.CommittedCursor != "1" {
				t.Fatalf("non-completing reconciliation overwrote owner blocker: %v", err)
			}
		})
	}
}

func TestSourcesAdapterTwoConnectionsCompeteForOneDiscoveryHostSlot(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	keyA, keyB := fmt.Sprintf("page-%032x", 2900), fmt.Sprintf("page-%032x", 2901)
	f.requestedPage(t, keyA)
	other := f.window
	other.Execution.WorkflowID, other.Execution.Scope.Subject.SourceWindow = "source-sync/vn/source-a/window-b", "window-b"
	lease, err := f.ledger.ClaimLease(f.db.ctx, 1, other.Execution.Scope, other.Execution.Owner, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	other.Execution.AttemptFence = lease.AttemptFence
	if _, err := f.ledger.RegisterExecution(f.db.ctx, other, "other-source-run", nil); err != nil {
		t.Fatal(err)
	}
	frozen := f.frozen
	frozen.Request.WindowKey = "window-b"
	if _, err := f.sources.OpenWindow(f.db.ctx, other, frozen); err != nil {
		t.Fatal(err)
	}
	plan := f.plan(fmt.Sprintf("request-%032x", 2901), "1", model.PurposeListing)
	plan.WindowKey = "window-b"
	if _, err := f.sources.RegisterPage(f.db.ctx, sourceWork(other, "plan-race", keyB), SourcePagePlan{PageKey: keyB, Plan: plan, RetryStartedAt: frozen.Request.Cutoff}); err != nil {
		t.Fatal(err)
	}
	conn := boundedSourceAppConnection(t, f.db)
	second, err := NewSources(boundedAdapterTransactions{conn}, "writer-a", fixtureObjectStore{content: "synthetic source bytes"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.db.ctx, 5*time.Second)
	defer cancel()
	start, results := make(chan struct{}), make(chan error, 2)
	go func() {
		<-start
		_, err := f.sources.ClaimPage(ctx, sourceWork(f.window, "race-slot", keyA), keyA)
		results <- err
	}()
	go func() {
		<-start
		_, err := second.ClaimPage(ctx, sourceWork(other, "race-slot", keyB), keyB)
		results <- err
	}()
	close(start)
	admitted, denied := 0, 0
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err == nil {
				admitted++
			} else if errors.Is(err, run.ErrStaleGrant) {
				denied++
			} else {
				t.Fatalf("unexpected racing claim error: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("discovery host race exceeded bounded wait")
		}
	}
	if admitted != 1 || denied != 1 || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 1 || f.count(t, `SELECT count(*) FROM ingest.execution_result WHERE payload->>'SourceOperation' = 'claim_page'`) != 1 {
		t.Fatal("one discovery host slot did not admit exactly one claim")
	}
}

func TestSourcesAdapterIncompleteFailureReconcilesUnderNewFence(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	key := fmt.Sprintf("page-%032x", 3000)
	f.requestedPage(t, key)
	claim, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "claim-before-closure", key), key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.CloseWindow(f.db.ctx, sourceWork(f.window, "close-before-reclaim", key), SourceWindowClosure{Reason: "owner_blocker"}); err != nil {
		t.Fatal(err)
	}
	f.reclaimWindow(t, 1)
	future := time.Now().Add(time.Minute).UTC().Truncate(time.Microsecond)
	retry := SourceRetryReceipt{PageKey: key, ClaimResult: claim.ResultID, AttemptKey: claim.AttemptKey, Attempt: claim.NextAttempt, Next: future, RetryAfter: future}
	if _, err := f.sources.RecordPageRetry(f.db.ctx, sourceWork(f.window, "reconcile-old-failure", key), retry); err != nil {
		t.Fatal(err)
	}
	w, err := f.sources.ReadWindow(f.db.ctx, f.window)
	if err != nil || w.State != "incomplete" || w.Reason == nil || *w.Reason != "owner_blocker" || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 {
		t.Fatalf("new owner could not reconcile without clearing blocker or releasing twice: %v", err)
	}
	if f.count(t, `SELECT source_attempts FROM ingest.discovery_page WHERE page_key = $1`, key) != 1 || f.count(t, `SELECT count(*) FROM ingest.source_rate_limit WHERE next_allowed_at >= $1`, future) != 1 || f.count(t, `SELECT count(*) FROM ingest.execution_lease WHERE resource_kind = 'source_window' AND next_eligible_at >= $1`, future) != 1 {
		t.Fatal("new-owner failure reconciliation lost source accounting or pacing")
	}
}

func TestSourcesAdapterClaimIssueMarkers(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	key := fmt.Sprintf("page-%032x", 3100)
	f.requestedPage(t, key)
	work := sourceWork(f.window, "issue-marker", key)
	claim, err := f.sources.ClaimPage(f.db.ctx, work, key)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.sources.ClaimPage(f.db.ctx, work, key)
	if err != nil || !reflect.DeepEqual(claim, replay) {
		t.Fatalf("claim replay: %v", err)
	}
	p, err := f.sources.ReadPage(f.db.ctx, f.window, key)
	if err != nil || p.ClaimOrdinal != 1 || p.ClaimState == nil || *p.ClaimState != "issued" || p.InfrastructureAttempts != 1 || p.SourceAttempts != 0 || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 1 {
		t.Fatalf("issue accounting: %v", err)
	}
}

func TestSourcesAdapterSuccessfulCaptureSettlesClaim(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	key := fmt.Sprintf("page-%032x", 3100)
	f.requestedPage(t, key)
	claim, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "capture-marker-issue", key), key)
	if err != nil {
		t.Fatal(err)
	}
	var p SourcePage
	capture := SourceCaptureReceipt{Key: key, ClaimResult: claim.ResultID, AttemptKey: claim.AttemptKey, Attempt: claim.NextAttempt, Capture: f.capture}
	first, err := f.sources.RecordPageCapture(f.db.ctx, sourceWork(f.window, "capture-marker", key), capture)
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.sources.ReadPage(f.db.ctx, f.window, key)
	if err != nil || p.State != "captured" || *p.ClaimState != "settled" || p.SourceAttempts != 1 || p.InfrastructureAttempts != 1 || p.LastAttemptKey == nil || *p.LastAttemptKey != claim.AttemptKey || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 {
		t.Fatalf("capture accounting: %v", err)
	}
	replayed, err := f.sources.RecordPageCapture(f.db.ctx, sourceWork(f.window, "capture-another-execution-substep", key), capture)
	if err != nil || !reflect.DeepEqual(first, replayed) {
		t.Fatalf("claim outcome replay: %v", err)
	}
}

func TestSourcesAdapterLateSuccessUsesCurrentScheduledOwner(t *testing.T) {
	f := newSourcesAdapterFixture(t, func(w *FrozenSourceWindow) {
		w.Deadline = time.Now().Add(3 * time.Second).UTC().Truncate(time.Microsecond)
	})
	key := fmt.Sprintf("page-%032x", 3101)
	f.requestedPage(t, key)
	claim, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "issue-late", key), key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.CloseWindow(f.db.ctx, sourceWork(f.window, "close-late", key), SourceWindowClosure{Reason: "owner_blocker"}); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(time.Until(f.frozen.Deadline) + time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-f.db.ctx.Done():
		t.Fatal(f.db.ctx.Err())
	}
	old := f.window
	f.window.Execution.WorkflowID = "source-sync/vn/source-a/reconcile-2026-10-14T00:15:00Z"
	f.window.Execution.AttemptKey = "scheduled-reconcile"
	f.reclaimWindow(t, 1)
	future := time.Now().Add(time.Minute).UTC().Truncate(time.Microsecond)
	if _, err := f.app.Exec(f.db.ctx, `UPDATE ingest.execution_lease SET next_eligible_at = $1 WHERE resource_kind = 'source_window' AND resource_key = 'window-a'`, future); err != nil {
		t.Fatal(err)
	}
	receipt := SourceCaptureReceipt{Key: key, ClaimResult: claim.ResultID, AttemptKey: claim.AttemptKey, Attempt: claim.NextAttempt, Capture: f.capture}
	if _, err := f.sources.RecordPageCapture(f.db.ctx, sourceWork(old, "stale-late", key), receipt); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("stale late capture: %v", err)
	}
	work := sourceWork(f.window, "late-success", key)
	first, err := f.sources.RecordPageCapture(f.db.ctx, work, receipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, work := range []run.IntakeWork{work, sourceWork(f.window, "late-success-replay", key)} {
		replay, err := f.sources.RecordPageCapture(f.db.ctx, work, receipt)
		if err != nil || !reflect.DeepEqual(first, replay) {
			t.Fatalf("late success replay: %v", err)
		}
	}
	changed := receipt
	changed.Capture.ResponseHash = model.Digest(strings.Repeat("b", 64))
	if _, err := f.sources.RecordPageCapture(f.db.ctx, sourceWork(f.window, "late-changed", key), changed); !errors.Is(err, run.ErrPayloadConflict) {
		t.Fatalf("changed late capture: %v", err)
	}
	p, err := f.sources.ReadPage(f.db.ctx, f.window, key)
	if err != nil || p.State != "captured" || *p.ClaimState != "settled" || p.SourceAttempts != 1 || p.InfrastructureAttempts != 1 || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 {
		t.Fatalf("late capture accounting: %v", err)
	}
	w, err := f.sources.ReadWindow(f.db.ctx, f.window)
	if err != nil || w.State != "incomplete" || w.Reason == nil || *w.Reason != "owner_blocker" {
		t.Fatalf("late success cleared blocker: %v", err)
	}
	if _, err := f.sources.CloseWindow(f.db.ctx, sourceWork(f.window, "close-after-capture", key), SourceWindowClosure{PageKey: key, Reason: "decode_integrity"}); err != nil {
		t.Fatal(err)
	}
	p, err = f.sources.ReadPage(f.db.ctx, f.window, key)
	if err != nil || p.CaptureKey == nil || *p.ClaimState != "settled" || p.SourceAttempts != 1 {
		t.Fatalf("closure lost committed capture: %v", err)
	}
	replay, err := f.sources.RecordPageCapture(f.db.ctx, sourceWork(f.window, "terminal-late-replay", key), receipt)
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatalf("terminal page lost receipt replay: %v", err)
	}
}

func TestSourcesAdapterLateRetryUsesDifferentReconciliationWorkflow(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	key := fmt.Sprintf("page-%032x", 3102)
	f.requestedPage(t, key)
	claim, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "issue-late-retry", key), key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.CloseWindow(f.db.ctx, sourceWork(f.window, "close-late-retry", key), SourceWindowClosure{Reason: "owner_blocker"}); err != nil {
		t.Fatal(err)
	}
	f.window.Execution.WorkflowID = "source-sync/vn/source-a/reconcile-2026-10-14T00:30:00Z"
	f.window.Execution.AttemptKey = "reconcile-b"
	f.reclaimWindow(t, 1)
	future := time.Now().Add(time.Minute).UTC().Truncate(time.Microsecond)
	leaseFuture := future.Add(time.Minute)
	if _, err := f.app.Exec(f.db.ctx, `UPDATE ingest.execution_lease SET next_eligible_at = $1 WHERE resource_kind = 'source_window' AND resource_key = 'window-a'`, leaseFuture); err != nil {
		t.Fatal(err)
	}
	retry := SourceRetryReceipt{PageKey: key, ClaimResult: claim.ResultID, AttemptKey: claim.AttemptKey, Attempt: claim.NextAttempt, Next: future, RetryAfter: future}
	first, err := f.sources.RecordPageRetry(f.db.ctx, sourceWork(f.window, "reconcile-late-retry", key), retry)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.sources.RecordPageRetry(f.db.ctx, sourceWork(f.window, "replay-late-retry", key), retry)
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatalf("late retry replay: %v", err)
	}
	changed := retry
	changed.Next = future.Add(time.Second)
	if _, err := f.sources.RecordPageRetry(f.db.ctx, sourceWork(f.window, "changed-late-retry", key), changed); !errors.Is(err, run.ErrPayloadConflict) {
		t.Fatalf("late retry changed payload: %v", err)
	}
	p, err := f.sources.ReadPage(f.db.ctx, f.window, key)
	if err != nil || p.State != "requested" || *p.ClaimState != "settled" || p.SourceAttempts != 1 || p.InfrastructureAttempts != 1 || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 {
		t.Fatalf("late retry accounting: %v", err)
	}
	if f.count(t, `SELECT count(*) FROM ingest.execution_lease WHERE resource_kind = 'source_window' AND next_eligible_at >= $1`, leaseFuture) != 1 {
		t.Fatal("late outcome reset existing lease eligibility")
	}
	if f.count(t, `SELECT count(*) FROM ingest.source_rate_limit WHERE next_allowed_at >= $1`, future) != 1 {
		t.Fatal("late Retry-After did not pace host")
	}
}

func TestSourcesAdapterIntrinsicFailureSettlesIssuedAndReleasedClaims(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprint(late), func(t *testing.T) {
			f := newSourcesAdapterFixture(t)
			key := fmt.Sprintf("page-%032x", 3103)
			f.requestedPage(t, key)
			claim, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "issue-intrinsic", key), key)
			if err != nil {
				t.Fatal(err)
			}
			if late {
				if _, err := f.sources.CloseWindow(f.db.ctx, sourceWork(f.window, "release-intrinsic", key), SourceWindowClosure{Reason: "owner_blocker"}); err != nil {
					t.Fatal(err)
				}
			}
			failure := SourcePageFailure{PageKey: key, ClaimResult: claim.ResultID, AttemptKey: claim.AttemptKey, Attempt: claim.NextAttempt, Reason: "source_intrinsic"}
			work := sourceWork(f.window, "intrinsic-outcome", key)
			first, err := f.sources.RecordPageFailure(f.db.ctx, work, failure)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := f.sources.RecordPageFailure(f.db.ctx, sourceWork(f.window, "intrinsic-replay", key), failure)
			if err != nil || !reflect.DeepEqual(first, replay) {
				t.Fatalf("intrinsic replay: %v", err)
			}
			p, err := f.sources.ReadPage(f.db.ctx, f.window, key)
			if err != nil || p.State != "failed" || *p.ClaimState != "settled" || p.SourceAttempts != 1 || p.InfrastructureAttempts != 1 || p.LastAttemptKey == nil || *p.LastAttemptKey != claim.AttemptKey || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 {
				t.Fatalf("intrinsic accounting: %v", err)
			}
			w, err := f.sources.ReadWindow(f.db.ctx, f.window)
			if err != nil || w.State != "failed" || w.Reason == nil || *w.Reason != failure.Reason {
				t.Fatalf("intrinsic window: %v", err)
			}
		})
	}
}

func TestSourcesAdapterPolicyRejectionSpendsNoSourceAttempt(t *testing.T) {
	for _, issued := range []bool{false, true} {
		t.Run(fmt.Sprint(issued), func(t *testing.T) {
			f := newSourcesAdapterFixture(t)
			key := fmt.Sprintf("page-%032x", 3104)
			f.requestedPage(t, key)
			if issued {
				if _, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "policy-issue", key), key); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.sources.CloseWindow(f.db.ctx, sourceWork(f.window, "policy-rejection", key), SourceWindowClosure{PageKey: key, Reason: "policy_rejection"}); err != nil {
				t.Fatal(err)
			}
			p, err := f.sources.ReadPage(f.db.ctx, f.window, key)
			if err != nil || p.State != "failed" || p.SourceAttempts != 0 || p.InfrastructureAttempts != 1 || issued && (p.ClaimState == nil || *p.ClaimState != "released") || !issued && p.ClaimState != nil || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 {
				t.Fatalf("policy rejection accounting: %v", err)
			}
		})
	}
}

func TestSourcesAdapterCloseReclaimResumeCannotBypassInfrastructureCap(t *testing.T) {
	for _, denyAt := range []string{"resume", "replacement"} {
		t.Run(denyAt, func(t *testing.T) {
			f := newSourcesAdapterFixture(t)
			key := fmt.Sprintf("page-%032x", 3105)
			f.requestedPage(t, key)
			for ordinal := int32(1); ordinal <= 3; ordinal++ {
				claim, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "loop-claim", fmt.Sprint(ordinal)), key)
				if err != nil || claim.NextAttempt != 1 || claim.AttemptKey != discoveryAttemptKey("window-a", key, ordinal) {
					t.Fatalf("loop issue: %v", err)
				}
				if _, err := f.sources.CloseWindow(f.db.ctx, sourceWork(f.window, "loop-close", fmt.Sprint(ordinal)), SourceWindowClosure{Reason: "worker_lost"}); err != nil {
					t.Fatal(err)
				}
				f.reclaimWindow(t, 1)
				p, err := f.sources.ReadPage(f.db.ctx, f.window, key)
				if err != nil || p.ClaimOrdinal != ordinal || *p.ClaimState != "released" || p.InfrastructureAttempts != ordinal || p.SourceAttempts != 0 {
					t.Fatalf("closure advanced attempts: %v", err)
				}
				if ordinal < 3 {
					if _, err := f.sources.ResumeWindow(f.db.ctx, sourceWork(f.window, "loop-resume", fmt.Sprint(ordinal))); err != nil {
						t.Fatal(err)
					}
				}
			}
			if denyAt == "resume" {
				work := sourceWork(f.window, "deny-resume", key)
				first, err := f.sources.ResumeWindow(f.db.ctx, work)
				if !errors.Is(err, run.ErrStaleGrant) {
					t.Fatalf("third released claim resumed: %v", err)
				}
				before := f.count(t, `SELECT count(*) FROM ingest.execution_result`)
				pageBefore, err := f.sources.ReadPage(f.db.ctx, f.window, key)
				if err != nil {
					t.Fatal(err)
				}
				slotsBefore := f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`)
				again, err := f.sources.ResumeWindow(f.db.ctx, work)
				if !errors.Is(err, run.ErrStaleGrant) || !reflect.DeepEqual(first, again) {
					t.Fatalf("exhausted resumption replay changed receipt or denial: %v", err)
				}
				if f.count(t, `SELECT count(*) FROM ingest.execution_result`) != before {
					t.Fatal("exhausted resumption replay inserted another receipt")
				}
				pageAfter, err := f.sources.ReadPage(f.db.ctx, f.window, key)
				if err != nil || !reflect.DeepEqual(pageBefore, pageAfter) || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != slotsBefore {
					t.Fatalf("exhausted resumption replay changed page accounting or slots: %v", err)
				}
			} else {
				if _, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "deny-replacement", key), key); !errors.Is(err, run.ErrStaleGrant) {
					t.Fatalf("fourth fetch admitted: %v", err)
				}
			}
			p, err := f.sources.ReadPage(f.db.ctx, f.window, key)
			if err != nil || p.State != "failed" || *p.ClaimState != "released" || p.ClaimOrdinal != 3 || p.InfrastructureAttempts != 3 || p.SourceAttempts != 0 || p.Reason == nil || *p.Reason != "infrastructure_retry_exhausted" || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 {
				t.Fatalf("cap was not durable: %v", err)
			}
			if f.count(t, `SELECT count(*) FROM ingest.execution_result WHERE payload->>'ExhaustionReason' = 'infrastructure_retry_exhausted'`) != 1 {
				t.Fatal("cap denial lost its exhaustion receipt")
			}
			w, err := f.sources.ReadWindow(f.db.ctx, f.window)
			if err != nil || w.State != "failed" || w.Reason == nil || *w.Reason != "infrastructure_retry_exhausted" {
				t.Fatalf("cap window: %v", err)
			}
		})
	}
}

func TestSourcesAdapterTransferSurvivesSeparatelyScheduledControl(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	request := fmt.Sprintf("request-%032x", 3110)
	page := f.page(t, 0, true, true, func(page *api.DecodedDiscoveryPage) {
		page.FollowUps = []model.FollowUpRequest{{RequestKey: request, WindowKey: "window-a", ParentCapture: page.Capture.CaptureKey, Purpose: model.PurposeDetail, URL: "https://example.com/detail", PolicyHash: fixtureDigest, BudgetHash: fixtureDigest, Budget: f.frozen.Request.Budget}}
	})
	if _, err := f.sources.ApplyDiscoveryPage(f.db.ctx, sourceWork(f.window, "scheduled-transfer-accept", page.PageKey), DiscoveryAcceptance{Page: page, Launches: []SourceLaunch{{RequestKey: request, Work: f.doc, DueAt: f.frozen.Request.Cutoff}}}); err != nil {
		t.Fatal(err)
	}
	var original json.RawMessage
	if err := f.app.QueryRow(f.db.ctx, `SELECT to_jsonb(d) - 'acknowledged_at' FROM ingest.delivery_record d WHERE deduplication_key = $1`, request).Scan(&original); err != nil {
		t.Fatal(err)
	}
	control := f.control(t)
	control.Execution.WorkflowID = "corpus-control/vn-2026-10-14T00:15:00Z"
	control.Execution.AttemptKey = "control-a"
	if _, err := f.ledger.RegisterExecution(f.db.ctx, control, "scheduled-control-a", nil); err != nil {
		t.Fatal(err)
	}
	transfer := SourceLaunchTransfer{RequestKey: request, RecipientWorkflowID: control.Execution.WorkflowID}
	first, err := f.sources.TransferLaunch(f.db.ctx, sourceWork(f.window, "transfer-a", request), transfer)
	if err != nil {
		t.Fatal(err)
	}
	f.reclaimWindow(t, 1)
	transfer.RecipientWorkflowID = "corpus-control/vn-2026-10-14T00:30:00Z"
	replay, err := f.sources.TransferLaunch(f.db.ctx, sourceWork(f.window, "transfer-b", request), transfer)
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatalf("cross-execution transfer created a new receipt: %v", err)
	}
	if f.count(t, `SELECT count(*) FROM ingest.execution_result WHERE substep = 'source-launch-transfer' AND shard = $1`, request) != 1 {
		t.Fatal("transfer receipt uniqueness was not enforced")
	}
	ack := SourceLaunchAcknowledgement{WindowKey: "window-a", RequestKey: request, Child: f.doc, ExecutionKey: f.childExecutionKey(t, f.doc)}
	if _, err := f.sources.AcknowledgeLaunch(f.db.ctx, sourceWork(f.window, "source-after-transfer", request), ack); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("source retained transferred authority: %v", err)
	}
	oldControl := control
	if _, err := f.app.Exec(f.db.ctx, `UPDATE ingest.execution_lease SET expires_at = clock_timestamp() - interval '1 second' WHERE resource_kind = 'control' AND resource_key = 'vn'`); err != nil {
		t.Fatal(err)
	}
	control.Execution.Owner = "control-b"
	lease, err := f.ledger.ReclaimLease(f.db.ctx, 1, control.Execution.Scope, control.Execution.Owner, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	control.Execution.AttemptFence = lease.AttemptFence
	if _, err := f.sources.AcknowledgeLaunch(f.db.ctx, sourceWork(oldControl, "old-scheduled-control", request), ack); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("old scheduled control retained authority: %v", err)
	}
	control.Execution.WorkflowID = transfer.RecipientWorkflowID
	control.Execution.AttemptKey = "control-b"
	if _, err := f.ledger.RegisterExecution(f.db.ctx, control, "scheduled-control-b", nil); err != nil {
		t.Fatal(err)
	}
	intents, err := f.sources.DueTransferredLaunches(f.db.ctx, sourceWork(control, "due-transferred", "vn"), 10)
	if err != nil || len(intents) != 1 || intents[0].Delivery.RecipientWorkflowID != f.doc.Execution.WorkflowID || !reflect.DeepEqual(intents[0].Child, f.doc) {
		t.Fatalf("scheduled control could not find unchanged child intent: %v", err)
	}
	if _, err := f.sources.AcknowledgeLaunch(f.db.ctx, sourceWork(control, "scheduled-control-ack", request), ack); err != nil {
		t.Fatal(err)
	}
	var unchanged json.RawMessage
	if err := f.app.QueryRow(f.db.ctx, `SELECT to_jsonb(d) - 'acknowledged_at' FROM ingest.delivery_record d WHERE deduplication_key = $1`, request).Scan(&unchanged); err != nil {
		t.Fatal(err)
	}
	if !sameSourceJSON(original, unchanged) || f.count(t, `SELECT count(*) FROM ingest.delivery_record WHERE deduplication_key = $1 AND acknowledged_at IS NOT NULL`, request) != 1 {
		t.Fatal("transfer rewrote original delivery identity")
	}
}

type sourceInsertGate struct {
	TransactionStarter
	inserted chan struct{}
	resume   chan struct{}
	query    string
}

func (s sourceInsertGate) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	tx, err := s.TransactionStarter.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	query := s.query
	if query == "" {
		query = "INSERT INTO ingest.artifact_request"
	}
	return &sourceInsertGateTx{Tx: tx, inserted: s.inserted, resume: s.resume, query: query}, nil
}

type sourceInsertGateTx struct {
	pgx.Tx
	inserted chan struct{}
	resume   chan struct{}
	paused   bool
	query    string
}

func (tx *sourceInsertGateTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	row := tx.Tx.QueryRow(ctx, query, args...)
	if !tx.paused && strings.Contains(query, tx.query) {
		tx.paused = true
		return sourceInsertGateRow{Row: row, ctx: ctx, inserted: tx.inserted, resume: tx.resume}
	}
	return row
}

type sourceInsertGateRow struct {
	pgx.Row
	ctx      context.Context
	inserted chan struct{}
	resume   chan struct{}
}

func (row sourceInsertGateRow) Scan(dest ...any) error {
	if err := row.Row.Scan(dest...); err != nil {
		return err
	}
	close(row.inserted)
	select {
	case <-row.resume:
		return nil
	case <-row.ctx.Done():
		return row.ctx.Err()
	}
}

func TestSourcesAdapterPrelocksHostsBeforeOppositeOrderRequestInserts(t *testing.T) {
	for _, operation := range []string{"registration", "claim", "settlement"} {
		t.Run(operation, func(t *testing.T) {
			f := newSourcesAdapterFixture(t)
			inventory, _ := f.requests(t, 0)
			var acceptance string
			if err := f.app.QueryRow(f.db.ctx, `SELECT r.result_key FROM ingest.discovery_page p JOIN ingest.execution_result r ON r.id = p.acceptance_result_id`).Scan(&acceptance); err != nil {
				t.Fatal(err)
			}
			observations, err := f.sources.AcceptedObservations(f.db.ctx, f.window, acceptance)
			if err != nil || len(observations) != 1 {
				t.Fatalf("parent observation: %v", err)
			}
			for _, host := range []string{"z.example", "a.example"} {
				if _, err := f.sources.RegisterHost(f.db.ctx, sourceWork(f.window, "race-host", host), SourceHostPacing{HostKey: host, NextAllowedAt: f.frozen.Request.Start, MaxInFlight: 2}); err != nil {
					t.Fatal(err)
				}
			}
			connB := boundedSourceAppConnection(t, f.db)
			sourcesB, err := NewSources(connB, "writer-a", fixtureObjectStore{content: "synthetic source bytes"})
			if err != nil {
				t.Fatal(err)
			}
			ledgerB, err := NewLedger(connB, "writer-a", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			windowB := f.window
			windowB.Execution.Scope.Subject.SourceWindow = "window-b"
			windowB.Execution.WorkflowID = "source-sync/vn/source-a/window-b"
			lease, err := ledgerB.ClaimLease(f.db.ctx, 1, windowB.Execution.Scope, windowB.Execution.Owner, time.Now().Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			windowB.Execution.AttemptFence = lease.AttemptFence
			if _, err := ledgerB.RegisterExecution(f.db.ctx, windowB, "host-cycle-window", nil); err != nil {
				t.Fatal(err)
			}
			frozenB := f.frozen
			frozenB.Request.WindowKey = "window-b"
			if _, err := sourcesB.OpenWindow(f.db.ctx, windowB, frozenB); err != nil {
				t.Fatal(err)
			}
			planA := f.plan(fmt.Sprintf("request-%032x", 3120), "1", model.PurposeListing)
			planA.WindowKey, planA.URL = "window-b", "https://a.example/list"
			pageA := fmt.Sprintf("page-%032x", 3120)
			if _, err := sourcesB.RegisterPage(f.db.ctx, sourceWork(windowB, "cycle-plan-a", pageA), SourcePagePlan{PageKey: pageA, Plan: planA, RetryStartedAt: f.frozen.Request.Cutoff}); err != nil {
				t.Fatal(err)
			}
			claimA, err := sourcesB.ClaimPage(f.db.ctx, sourceWork(windowB, "cycle-claim-a", pageA), pageA)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := sourcesB.RecordPageCapture(f.db.ctx, sourceWork(windowB, "cycle-capture-a", pageA), SourceCaptureReceipt{Key: pageA, ClaimResult: claimA.ResultID, AttemptKey: claimA.AttemptKey, Attempt: claimA.NextAttempt, Capture: f.capture}); err != nil {
				t.Fatal(err)
			}
			planZ := planA
			planZ.RequestKey, planZ.URL, planZ.Cursor, planZ.ParentCapture = fmt.Sprintf("request-%032x", 3121), "https://z.example/list", "2", f.capture.CaptureKey
			decoded := api.DecodedDiscoveryPage{PageKey: pageA, Capture: f.capture, Cursor: model.CursorPosition{WindowKey: "window-b", SliceKey: f.frozen.Request.SliceKey, InitialCursor: "1", CommittedCursor: "1", NextCursor: "1"}, Limits: inventory.Limits,
				FollowUps: []model.FollowUpRequest{{RequestKey: planZ.RequestKey, WindowKey: "window-b", ParentCapture: f.capture.CaptureKey, Purpose: model.PurposeListing, URL: planZ.URL, Ordinal: 1, PolicyHash: fixtureDigest, BudgetHash: fixtureDigest, Budget: f.frozen.Request.Budget}}}
			if _, err := sourcesB.RecordPageDecode(f.db.ctx, sourceWork(windowB, "cycle-decode-a", pageA), decoded); err != nil {
				t.Fatal(err)
			}
			pageZ := fmt.Sprintf("page-%032x", 3121)
			if _, err := sourcesB.RegisterPage(f.db.ctx, sourceWork(windowB, "cycle-plan-z", pageZ), SourcePagePlan{PageKey: pageZ, Ordinal: 1, Plan: planZ, RetryStartedAt: f.frozen.Request.Cutoff}); err != nil {
				t.Fatal(err)
			}
			if _, err := sourcesB.ClaimPage(f.db.ctx, sourceWork(windowB, "cycle-claim-z", pageZ), pageZ); err != nil {
				t.Fatal(err)
			}
			var requests []SourceRequestPlan
			for i, host := range []string{"z.example", "a.example"} {
				artifact := model.ArtifactInventoryEntry{ArtifactKey: fmt.Sprintf("host-cycle-%d", i), Role: "main", URL: "https://" + host + "/file", Ordinal: i, Format: "html", Required: true}
				inventory.Artifacts = append(inventory.Artifacts, artifact)
				plan := f.plan(fmt.Sprintf("request-%032x", 3130+i), "", model.PurposeArtifact)
				plan.URL, plan.ParentCapture = artifact.URL, inventory.Observation.Capture.CaptureKey
				requests = append(requests, SourceRequestPlan{Plan: plan, ParentObservation: observations[0].ObservationKey, Role: model.ArtifactRoleMain, Required: true, HostKey: host, RetryStartedAt: f.frozen.Request.Cutoff, ArtifactKey: artifact.ArtifactKey})
			}
			var transport SourceTransportReceipt
			if operation != "registration" {
				if _, err := f.sources.RegisterRequests(f.db.ctx, sourceWork(f.doc, "host-cycle-seed", "requests"), inventory, requests); err != nil {
					t.Fatal(err)
				}
				if operation == "settlement" {
					claim, err := f.sources.ClaimRequest(f.db.ctx, f.doc)
					if err != nil {
						t.Fatal(err)
					}
					now := time.Now().Add(-time.Second)
					transport = SourceTransportReceipt{RequestKey: claim.RequestKey, AttemptKey: claim.AttemptKey, Attempt: claim.NextAttempt, Next: now, RetryAfter: now}
				}
			}
			ctx, cancel := context.WithTimeout(f.db.ctx, 5*time.Second)
			defer cancel()
			gate := sourceInsertGate{TransactionStarter: f.app, inserted: make(chan struct{}), resume: make(chan struct{})}
			if operation != "registration" {
				gate.query = "SELECT id FROM ingest.source_rate_limit WHERE host_key"
			}
			gated, err := NewSources(gate, "writer-a", nil)
			if err != nil {
				t.Fatal(err)
			}
			results := make(chan error, 2)
			go func() {
				var err error
				switch operation {
				case "registration":
					_, err = gated.RegisterRequests(ctx, sourceWork(f.doc, "host-cycle-register", "requests"), inventory, requests)
				case "claim":
					_, err = gated.ClaimRequest(ctx, f.doc)
				case "settlement":
					_, err = gated.RecordTransport(ctx, sourceWork(f.doc, "host-cycle-settle", "request"), transport)
				}
				results <- err
			}()
			awaitAdapterSignal(t, ctx, gate.inserted)
			go func() {
				_, err := sourcesB.CloseWindow(ctx, sourceWork(windowB, "host-cycle-close", "window-b"), SourceWindowClosure{Reason: "owner_blocker"})
				results <- err
			}()
			// With prelocking, closure waits on registration's earlier a-host lock.
			// Without it, closure owns a and waits on the first insert's z FK lock;
			// resuming the second insert completes the old deadlock cycle.
			waitAdapterBlockedBy(t, ctx, f.db.conn, connB.PgConn().PID(), f.app.PgConn().PID())
			close(gate.resume)
			for i := 0; i < 2; i++ {
				select {
				case err := <-results:
					if err != nil {
						t.Fatalf("host-cycle API failed: %v", err)
					}
				case <-ctx.Done():
					t.Fatal("host-cycle API exceeded bounded wait")
				}
			}
			wantSlots := int64(0)
			if operation == "claim" {
				wantSlots = 1
			}
			if f.count(t, `SELECT count(*) FROM ingest.artifact_request`) != 2 || f.count(t, `SELECT sum(in_flight) FROM ingest.source_rate_limit`) != wantSlots {
				t.Fatal("host race lost accounting")
			}
		})
	}
}

func TestSourcesAdapterRetryableFailureSettlesClaim(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	key := fmt.Sprintf("page-%032x", 3140)
	f.requestedPage(t, key)
	claim, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "retry-marker-issue", key), key)
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute).UTC().Truncate(time.Microsecond)
	retry := SourceRetryReceipt{PageKey: key, ClaimResult: claim.ResultID, AttemptKey: claim.AttemptKey, Attempt: claim.NextAttempt, Next: future, RetryAfter: future}
	first, err := f.sources.RecordPageRetry(f.db.ctx, sourceWork(f.window, "retry-marker-outcome", key), retry)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.sources.RecordPageRetry(f.db.ctx, sourceWork(f.window, "retry-marker-replay", key), retry)
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatalf("retry marker replay: %v", err)
	}
	p, err := f.sources.ReadPage(f.db.ctx, f.window, key)
	if err != nil || *p.ClaimState != "settled" || p.State != "requested" || p.SourceAttempts != 1 || p.InfrastructureAttempts != 1 || p.LastAttemptKey == nil || *p.LastAttemptKey != claim.AttemptKey || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 {
		t.Fatalf("retry marker accounting: %v", err)
	}
	if f.count(t, `SELECT count(*) FROM ingest.source_rate_limit WHERE next_allowed_at >= $1`, future) != 1 {
		t.Fatal("retry did not pace host")
	}
}

func TestSourcesAdapterEligibleResumptionReferencesClosure(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	key := fmt.Sprintf("page-%032x", 3141)
	f.requestedPage(t, key)
	if _, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "resume-issue", key), key); err != nil {
		t.Fatal(err)
	}
	closure, err := f.sources.CloseWindow(f.db.ctx, sourceWork(f.window, "resume-closure", key), SourceWindowClosure{Reason: "owner_blocker"})
	if err != nil {
		t.Fatal(err)
	}
	work := sourceWork(f.window, "eligible-resumption", key)
	first, err := f.sources.ResumeWindow(f.db.ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.sources.ResumeWindow(f.db.ctx, work)
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatalf("resumption replay: %v", err)
	}
	if f.count(t, `SELECT count(*) FROM ingest.execution_result WHERE result_key = $1 AND payload->>'ClosureResult' = $2`, first.ResultID, closure.ResultID) != 1 {
		t.Fatal("resumption lost closure binding")
	}
	p, err := f.sources.ReadPage(f.db.ctx, f.window, key)
	if err != nil || *p.ClaimState != "released" || p.ClaimOrdinal != 1 || p.SourceAttempts != 0 || p.InfrastructureAttempts != 1 || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 {
		t.Fatalf("resumption changed attempts: %v", err)
	}
	w, err := f.sources.ReadWindow(f.db.ctx, f.window)
	if err != nil || w.State != "frozen" || w.Reason != nil {
		t.Fatalf("eligible resumption did not clear blocker: %v", err)
	}
}

func TestSourcesAdapterDecodeFailureRetainsSettledCapture(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	page := f.page(t, 0, false, false)
	before, err := f.sources.ReadPage(f.db.ctx, f.window, page.PageKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.CloseWindow(f.db.ctx, sourceWork(f.window, "decode-integrity-failure", page.PageKey), SourceWindowClosure{PageKey: page.PageKey, Reason: "decode_integrity"}); err != nil {
		t.Fatal(err)
	}
	after, err := f.sources.ReadPage(f.db.ctx, f.window, page.PageKey)
	if err != nil || after.State != "failed" || *after.ClaimState != "settled" || after.SourceAttempts != before.SourceAttempts || after.InfrastructureAttempts != before.InfrastructureAttempts || !reflect.DeepEqual(after.CaptureKey, before.CaptureKey) || !reflect.DeepEqual(after.DecodedDigest, before.DecodedDigest) || !sameSourceJSON(after.TransportReceipt, before.TransportReceipt) || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 {
		t.Fatalf("decode failure lost capture or changed accounting: %v", err)
	}
}

func TestSourcesAdapterStaleOutcomeCannotSettleReplacement(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	key := fmt.Sprintf("page-%032x", 3142)
	f.requestedPage(t, key)
	old, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "old-claim", key), key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.CloseWindow(f.db.ctx, sourceWork(f.window, "old-closure", key), SourceWindowClosure{Reason: "owner_blocker"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.ResumeWindow(f.db.ctx, sourceWork(f.window, "old-resume", key)); err != nil {
		t.Fatal(err)
	}
	current, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "replacement-claim", key), key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.RecordPageCapture(f.db.ctx, sourceWork(f.window, "obsolete-capture", key), SourceCaptureReceipt{Key: key, ClaimResult: old.ResultID, AttemptKey: old.AttemptKey, Attempt: old.NextAttempt, Capture: f.capture}); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("obsolete claim settled replacement: %v", err)
	}
	p, err := f.sources.ReadPage(f.db.ctx, f.window, key)
	if err != nil || p.ClaimOrdinal != 2 || *p.ClaimState != "issued" || p.SourceAttempts != 0 || p.InfrastructureAttempts != 2 || old.AttemptKey == current.AttemptKey || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 1 {
		t.Fatalf("stale outcome changed accounting: %v", err)
	}
}

func TestSourcesAdapterReconciliationModeCannotIssueFetch(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	key := fmt.Sprintf("page-%032x", 3143)
	f.requestedPage(t, key)
	sources, err := NewSourceReconciler(f.app, "writer-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sources.ClaimPage(f.db.ctx, sourceWork(f.window, "reconciliation-issue", key), key); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("reconciliation issued fetch: %v", err)
	}
	if f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 || f.count(t, `SELECT claim_ordinal FROM ingest.discovery_page`) != 0 {
		t.Fatal("reconciliation changed issue accounting")
	}
}

func TestSourcesAdapterConcurrentBlobRegistrationAndRetention(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	pageA := f.page(t, 0, true, true)
	connB := boundedSourceAppConnection(t, f.db)
	sourcesB, err := NewSources(connB, "writer-a", fixtureObjectStore{content: "synthetic source bytes"})
	if err != nil {
		t.Fatal(err)
	}
	ledgerB, err := NewLedger(connB, "writer-a", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	windowB, docB := f.window, f.doc
	windowB.Execution.Scope.Subject.SourceWindow, windowB.Execution.WorkflowID = "window-b", "source-sync/vn/source-a/window-b"
	docB.Execution.Scope.Subject.DocumentVersion, docB.Execution.WorkflowID = "version-b", "document-ingest/vn/source-a/version-b"
	for _, work := range []*run.IntakeWork{&windowB, &docB} {
		lease, err := ledgerB.ClaimLease(f.db.ctx, 1, work.Execution.Scope, work.Execution.Owner, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		work.Execution.AttemptFence = lease.AttemptFence
		if _, err := ledgerB.RegisterExecution(f.db.ctx, *work, "blob-race", nil); err != nil {
			t.Fatal(err)
		}
	}
	frozenB := f.frozen
	frozenB.Request.WindowKey = "window-b"
	if _, err := sourcesB.OpenWindow(f.db.ctx, windowB, frozenB); err != nil {
		t.Fatal(err)
	}
	if _, err := sourcesB.RegisterHost(f.db.ctx, sourceWork(windowB, "blob-race-host", "other.example"), SourceHostPacing{HostKey: "other.example", NextAllowedAt: f.frozen.Request.Start, MaxInFlight: 1}); err != nil {
		t.Fatal(err)
	}
	plan := f.plan(fmt.Sprintf("request-%032x", 3150), "1", model.PurposeListing)
	plan.WindowKey, plan.URL = "window-b", "https://other.example/list"
	key := fmt.Sprintf("page-%032x", 3150)
	if _, err := sourcesB.RegisterPage(f.db.ctx, sourceWork(windowB, "blob-race-plan", key), SourcePagePlan{PageKey: key, Plan: plan, RetryStartedAt: f.frozen.Request.Cutoff}); err != nil {
		t.Fatal(err)
	}
	claim, err := sourcesB.ClaimPage(f.db.ctx, sourceWork(windowB, "blob-race-claim", key), key)
	if err != nil {
		t.Fatal(err)
	}
	capture := f.capture
	capture.CaptureKey = fmt.Sprintf("capture-%032x", 3150)
	if _, err := sourcesB.RecordPageCapture(f.db.ctx, sourceWork(windowB, "blob-race-capture", key), SourceCaptureReceipt{Key: key, ClaimResult: claim.ResultID, AttemptKey: claim.AttemptKey, Attempt: claim.NextAttempt, Capture: capture}); err != nil {
		t.Fatal(err)
	}
	pageB := pageA
	pageB.PageKey, pageB.Capture, pageB.Cursor.WindowKey = key, capture, "window-b"
	pageB.Observations = append([]api.DecodedObservation(nil), pageA.Observations...)
	pageB.Observations[0].ExternalID, pageB.Observations[0].VersionKey, pageB.Observations[0].Capture = "native-version-b", "version-b", capture
	if _, err := sourcesB.RecordPageDecode(f.db.ctx, sourceWork(windowB, "blob-race-decode", key), pageB); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.db.ctx, 5*time.Second)
	defer cancel()
	gate := sourceInsertGate{TransactionStarter: f.app, inserted: make(chan struct{}), resume: make(chan struct{}), query: "INSERT INTO bronze.blob"}
	gated, err := NewSources(gate, "writer-a", fixtureObjectStore{content: "synthetic source bytes"})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() {
		_, err := gated.ApplyDiscoveryPage(ctx, sourceWork(f.window, "blob-race-accept-a", pageA.PageKey), DiscoveryAcceptance{Page: pageA})
		results <- err
	}()
	awaitAdapterSignal(t, ctx, gate.inserted)
	go func() {
		_, err := sourcesB.ApplyDiscoveryPage(ctx, sourceWork(windowB, "blob-race-accept-b", key), DiscoveryAcceptance{Page: pageB})
		results <- err
	}()
	waitAdapterBlockedBy(t, ctx, f.db.conn, connB.PgConn().PID(), f.app.PgConn().PID())
	close(gate.resume)
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("blob registration/retention race: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("blob registration/retention exceeded bounded wait")
		}
	}
	if f.count(t, `SELECT count(*) FROM bronze.blob`) != 1 || f.count(t, `SELECT count(*) FROM bronze.observation_revision`) != 2 || f.count(t, `SELECT count(*) FROM bronze.blob_retention_reference`) != 4 {
		t.Fatal("concurrent acceptance lost immutable blobs or retention edges")
	}
}

func TestSourcesAdapterTransferRejectsChangedWindowBinding(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	request := fmt.Sprintf("request-%032x", 3160)
	addLaunch := func(window string) func(*api.DecodedDiscoveryPage) {
		return func(page *api.DecodedDiscoveryPage) {
			page.FollowUps = []model.FollowUpRequest{{RequestKey: request, WindowKey: window, ParentCapture: page.Capture.CaptureKey, Purpose: model.PurposeDetail, URL: "https://example.com/detail", PolicyHash: fixtureDigest, BudgetHash: fixtureDigest, Budget: f.frozen.Request.Budget}}
		}
	}
	pageA := f.page(t, 0, true, true, addLaunch("window-a"))
	if _, err := f.sources.ApplyDiscoveryPage(f.db.ctx, sourceWork(f.window, "bind-transfer-a", pageA.PageKey), DiscoveryAcceptance{Page: pageA, Launches: []SourceLaunch{{RequestKey: request, Work: f.doc, DueAt: f.frozen.Request.Cutoff}}}); err != nil {
		t.Fatal(err)
	}
	transfer := SourceLaunchTransfer{RequestKey: request, RecipientWorkflowID: "corpus-control/vn"}
	if _, err := f.sources.TransferLaunch(f.db.ctx, sourceWork(f.window, "bind-transfer", request), transfer); err != nil {
		t.Fatal(err)
	}
	second := *f
	second.window.Execution.Scope.Subject.SourceWindow, second.window.Execution.WorkflowID = "window-b", "source-sync/vn/source-a/window-b"
	second.frozen.Request.WindowKey = "window-b"
	lease, err := f.ledger.ClaimLease(f.db.ctx, 1, second.window.Execution.Scope, second.window.Execution.Owner, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	second.window.Execution.AttemptFence = lease.AttemptFence
	if _, err := f.ledger.RegisterExecution(f.db.ctx, second.window, "changed-transfer-window", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.OpenWindow(f.db.ctx, second.window, second.frozen); err != nil {
		t.Fatal(err)
	}
	pageB := second.page(t, 0, true, true, addLaunch("window-b"))
	if _, err := f.sources.ApplyDiscoveryPage(f.db.ctx, sourceWork(second.window, "bind-transfer-b", pageB.PageKey), DiscoveryAcceptance{Page: pageB, Launches: []SourceLaunch{{RequestKey: request, Work: f.doc, DueAt: f.frozen.Request.Cutoff}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sources.TransferLaunch(f.db.ctx, sourceWork(second.window, "changed-binding-transfer", request), transfer); !errors.Is(err, run.ErrPayloadConflict) {
		t.Fatalf("changed window rewrote transfer binding: %v", err)
	}
	ack := SourceLaunchAcknowledgement{WindowKey: "window-b", RequestKey: request, Child: f.doc, ExecutionKey: f.childExecutionKey(t, f.doc)}
	if _, err := f.sources.AcknowledgeLaunch(f.db.ctx, sourceWork(second.window, "changed-binding-ack", request), ack); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("alternate window consumed transferred intent: %v", err)
	}
	if f.count(t, `SELECT count(*) FROM ingest.execution_result WHERE substep = 'source-launch-transfer' AND shard = $1`, request) != 1 || f.count(t, `SELECT count(*) FROM ingest.delivery_record WHERE deduplication_key = $1 AND acknowledged_at IS NOT NULL`, request) != 0 {
		t.Fatal("changed bindings added transfer receipts or acknowledged delivery")
	}
}

func TestSourcesAdapterLostClaimRemainsReleasedWithoutReplacement(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	key := fmt.Sprintf("page-%032x", 3170)
	f.requestedPage(t, key)
	claim, err := f.sources.ClaimPage(f.db.ctx, sourceWork(f.window, "unreplaced-issue", key), key)
	if err != nil {
		t.Fatal(err)
	}
	f.reclaimWindow(t, 1)
	future := time.Now().Add(time.Minute).UTC().Truncate(time.Microsecond)
	if _, err := f.app.Exec(f.db.ctx, `UPDATE ingest.execution_lease SET next_eligible_at = $1 WHERE resource_kind = 'source_window' AND resource_key = 'window-a'`, future); err != nil {
		t.Fatal(err)
	}
	work := sourceWork(f.window, "unreplaced-loss", key)
	first, err := f.sources.RecoverPage(f.db.ctx, work, key, claim.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.sources.RecoverPage(f.db.ctx, work, key, claim.ResultID)
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatalf("unreplaced loss replay: %v", err)
	}
	p, err := f.sources.ReadPage(f.db.ctx, f.window, key)
	if err != nil || p.State != "requested" || p.ClaimState == nil || *p.ClaimState != "released" || p.ClaimOrdinal != 1 || p.SourceAttempts != 0 || p.InfrastructureAttempts != 1 || p.LastAttemptKey != nil || f.count(t, `SELECT in_flight FROM ingest.source_rate_limit`) != 0 {
		t.Fatalf("unreplaced loss disappeared from accounting: %v", err)
	}
	if f.count(t, `SELECT count(*) FROM ingest.execution_result WHERE result_key = $1 AND payload->>'SourceOperation' = 'recover_page' AND payload->'SourcePayload'->>'ClaimResult' = $2`, first.ResultID, claim.ResultID) != 1 || f.count(t, `SELECT count(*) FROM ingest.execution_lease WHERE resource_kind = 'source_window' AND next_eligible_at >= $1`, future) != 1 {
		t.Fatal("loss receipt or lease watermark was not retained")
	}
}

func TestSourcesAdapterForeignCaptureRejectsBeforeConcurrentWindowClosure(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	_, requests := f.requests(t, 1)
	connB := boundedSourceAppConnection(t, f.db)
	sourcesB, err := NewSources(connB, "writer-a", fixtureObjectStore{content: "synthetic source bytes"})
	if err != nil {
		t.Fatal(err)
	}
	foreignWork := f.doc
	foreignWork.Execution.Scope.Subject.DocumentVersion = "version-b"
	foreignWork.Execution.WorkflowID = "document-ingest/vn/source-a/version-b"
	lease, err := f.ledger.ClaimLease(f.db.ctx, 1, foreignWork.Execution.Scope, foreignWork.Execution.Owner, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	foreignWork.Execution.AttemptFence = lease.AttemptFence
	if _, err := f.ledger.RegisterExecution(f.db.ctx, foreignWork, "foreign-capture", nil); err != nil {
		t.Fatal(err)
	}
	before := f.count(t, `SELECT count(*) FROM ingest.execution_result`)
	ctx, cancel := context.WithTimeout(f.db.ctx, 5*time.Second)
	defer cancel()
	gate := sourceInsertGate{TransactionStarter: f.app, inserted: make(chan struct{}), resume: make(chan struct{}), query: "SELECT id FROM ingest.source_window WHERE window_key"}
	gated, err := NewSources(gate, "writer-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() {
		_, err := gated.CloseWindow(ctx, sourceWork(f.window, "foreign-capture-close", "window-a"), SourceWindowClosure{Reason: "owner_blocker"})
		closed <- err
	}()
	awaitAdapterSignal(t, ctx, gate.inserted)
	// Closure owns the foreign window. Rejection must finish while that lock
	// is still held, rather than waiting for a window outside the prelock set.
	_, err = sourcesB.ApplyCapture(ctx, sourceWork(foreignWork, "foreign-capture", "version-b"), CaptureAcceptance{RequestKey: requests[0].Plan.RequestKey, Capture: f.capture})
	if !errors.Is(err, run.ErrStaleGrant) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("foreign request reached the window lock: %v", err)
	}
	if f.count(t, `SELECT count(*) FROM ingest.execution_result`) != before {
		t.Fatal("foreign capture committed a receipt")
	}
	close(gate.resume)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("concurrent closure failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("concurrent closure exceeded bounded wait")
	}
	if f.count(t, `SELECT count(*) FROM ingest.artifact_request WHERE state = 'due'`) != 1 || f.count(t, `SELECT sum(in_flight) FROM ingest.source_rate_limit`) != 0 {
		t.Fatal("foreign capture or closure changed request accounting")
	}
}

func TestSourcesAdapterCancelledBodyPreservesTypedCause(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	for _, original := range []error{run.ErrStaleGrant, run.ErrPayloadConflict} {
		t.Run(original.Error(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(f.db.ctx)
			defer cancel()
			_, err := sourceTransaction(ctx, f.sources, f.window, run.SubjectSourceWindow, func(pgx.Tx, intakedb.Querier) (int, error) {
				cancel()
				return 0, original
			})
			if !errors.Is(err, original) || !errors.Is(err, context.Canceled) {
				t.Fatalf("source wrapper replaced body cause or lost cancellation: %v", err)
			}
		})
	}
}

func TestSourcesAdapterOppositeOrderLaunchAcceptanceUsesByteOrder(t *testing.T) {
	f := newSourcesAdapterFixture(t)
	childB := f.doc
	childB.Execution.Scope.Subject.DocumentVersion = "version-b"
	childB.Execution.WorkflowID = "document-ingest/vn/source-a/version-b"
	lease, err := f.ledger.ClaimLease(f.db.ctx, 1, childB.Execution.Scope, childB.Execution.Owner, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	childB.Execution.AttemptFence = lease.AttemptFence
	if _, err := f.ledger.RegisterExecution(f.db.ctx, childB, "launch-order-child", nil); err != nil {
		t.Fatal(err)
	}
	// Existing records must not serialize acceptance before delivery inserts.
	for _, external := range []string{"native-version-a", "native-version-b"} {
		if _, err := f.app.Exec(f.db.ctx, `INSERT INTO bronze.source_record (source_id, external_id) VALUES ('source-a', $1)`, external); err != nil {
			t.Fatal(err)
		}
	}
	keys := []string{fmt.Sprintf("request-%032x", 3180), fmt.Sprintf("request-%032x", 3181)}
	children := []run.IntakeWork{f.doc, childB}
	decorate := func(page *api.DecodedDiscoveryPage) {
		page.Observations, page.FollowUps = nil, nil
		for i, child := range children {
			version := child.Execution.Scope.Subject.LeaseKey()
			page.Observations = append(page.Observations, api.DecodedObservation{SourceID: "source-a", ExternalID: "native-" + version, VersionKey: version, Capture: page.Capture, Ordinal: i, Limits: page.Limits})
			page.FollowUps = append(page.FollowUps, model.FollowUpRequest{RequestKey: keys[i], WindowKey: page.Cursor.WindowKey, ParentCapture: page.Capture.CaptureKey, Purpose: model.PurposeDetail, URL: "https://example.com/detail/" + version, Ordinal: i, PolicyHash: fixtureDigest, BudgetHash: fixtureDigest, Budget: f.frozen.Request.Budget})
		}
	}
	pageA := f.page(t, 0, true, false, decorate)
	connB := boundedSourceAppConnection(t, f.db)
	objectsB := fixtureObjectStore{content: "distinct launch-order source bytes"}
	sourcesB, err := NewSources(connB, "writer-a", objectsB)
	if err != nil {
		t.Fatal(err)
	}
	windowB := f.window
	windowB.Execution.Scope.Subject.SourceWindow = "window-b"
	windowB.Execution.WorkflowID = "source-sync/vn/source-a/window-b"
	lease, err = f.ledger.ClaimLease(f.db.ctx, 1, windowB.Execution.Scope, windowB.Execution.Owner, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	windowB.Execution.AttemptFence = lease.AttemptFence
	if _, err := f.ledger.RegisterExecution(f.db.ctx, windowB, "launch-order-window", nil); err != nil {
		t.Fatal(err)
	}
	frozenB := f.frozen
	frozenB.Request.WindowKey = "window-b"
	if _, err := sourcesB.OpenWindow(f.db.ctx, windowB, frozenB); err != nil {
		t.Fatal(err)
	}
	if _, err := sourcesB.RegisterHost(f.db.ctx, sourceWork(windowB, "launch-order-host", "other.example"), SourceHostPacing{HostKey: "other.example", NextAllowedAt: f.frozen.Request.Start, MaxInFlight: 1}); err != nil {
		t.Fatal(err)
	}
	keyB := fmt.Sprintf("page-%032x", 3180)
	planB := f.plan(fmt.Sprintf("request-%032x", 3182), "1", model.PurposeListing)
	planB.WindowKey, planB.URL = "window-b", "https://other.example/list"
	if _, err := sourcesB.RegisterPage(f.db.ctx, sourceWork(windowB, "launch-order-plan", keyB), SourcePagePlan{PageKey: keyB, Plan: planB, RetryStartedAt: f.frozen.Request.Cutoff}); err != nil {
		t.Fatal(err)
	}
	claim, err := sourcesB.ClaimPage(f.db.ctx, sourceWork(windowB, "launch-order-claim", keyB), keyB)
	if err != nil {
		t.Fatal(err)
	}
	captureB := f.capture
	captureB.CaptureKey = fmt.Sprintf("capture-%032x", 3180)
	hashB := sha256.Sum256([]byte(objectsB.content))
	captureB.Asset.Path, captureB.Asset.SHA256, captureB.Asset.ByteSize = "staged/launch-order-b", model.Digest(hex.EncodeToString(hashB[:])), int64(len(objectsB.content))
	captureB.TransportReceipt = captureB.Asset
	if _, err := sourcesB.RecordPageCapture(f.db.ctx, sourceWork(windowB, "launch-order-capture", keyB), SourceCaptureReceipt{Key: keyB, ClaimResult: claim.ResultID, AttemptKey: claim.AttemptKey, Attempt: claim.NextAttempt, Capture: captureB}); err != nil {
		t.Fatal(err)
	}
	pageB := pageA
	pageB.PageKey, pageB.Capture, pageB.Cursor.WindowKey = keyB, captureB, "window-b"
	decorate(&pageB)
	pageB.FollowUps[0], pageB.FollowUps[1] = pageB.FollowUps[1], pageB.FollowUps[0]
	if _, err := sourcesB.RecordPageDecode(f.db.ctx, sourceWork(windowB, "launch-order-decode", keyB), pageB); err != nil {
		t.Fatal(err)
	}
	launches := []SourceLaunch{{RequestKey: keys[0], Work: f.doc, DueAt: f.frozen.Request.Cutoff}, {RequestKey: keys[1], Work: childB, DueAt: f.frozen.Request.Cutoff}}
	acceptanceA := DiscoveryAcceptance{Page: pageA, Launches: launches}
	acceptanceB := DiscoveryAcceptance{Page: pageB, Launches: []SourceLaunch{launches[1], launches[0]}}
	workA := sourceWork(f.window, "launch-order-accept", pageA.PageKey)
	workB := sourceWork(windowB, "launch-order-accept", keyB)
	ctx, cancel := context.WithTimeout(f.db.ctx, 5*time.Second)
	defer cancel()
	gate := sourceInsertGate{TransactionStarter: f.app, inserted: make(chan struct{}), resume: make(chan struct{}), query: "INSERT INTO ingest.delivery_record"}
	gated, err := NewSources(gate, "writer-a", fixtureObjectStore{content: "synthetic source bytes"})
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		receipt run.IntakeReceipt
		err     error
	}
	resultA, resultB := make(chan outcome, 1), make(chan outcome, 1)
	go func() {
		receipt, err := gated.ApplyDiscoveryPage(ctx, workA, acceptanceA)
		resultA <- outcome{receipt, err}
	}()
	awaitAdapterSignal(t, ctx, gate.inserted)
	go func() {
		receipt, err := sourcesB.ApplyDiscoveryPage(ctx, workB, acceptanceB)
		resultB <- outcome{receipt, err}
	}()
	// B must reach its UNIQUE wait. With decoder-order inserts B holds y
	// while waiting on x; resuming A would then create the old x/y cycle.
	waitAdapterBlockedBy(t, ctx, f.db.conn, connB.PgConn().PID(), f.app.PgConn().PID())
	close(gate.resume)
	for i, result := range []chan outcome{resultA, resultB} {
		select {
		case got := <-result:
			if got.err != nil {
				t.Fatalf("opposite-order launch acceptance: %v", got.err)
			}
			acceptance, work, sources := acceptanceA, workA, f.sources
			if i == 1 {
				acceptance, work, sources = acceptanceB, workB, sourcesB
			}
			var raw json.RawMessage
			if err := f.db.conn.QueryRow(ctx, `SELECT payload->'SourcePayload' FROM ingest.execution_result WHERE result_key = $1`, got.receipt.ResultID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			expected, err := sourceJSON(acceptance)
			if err != nil || !sameSourceJSON(raw, expected) {
				t.Fatalf("acceptance reordered the decoder receipt: %v", err)
			}
			replay, err := sources.ApplyDiscoveryPage(ctx, work, acceptance)
			if err != nil || !reflect.DeepEqual(got.receipt, replay) {
				t.Fatalf("acceptance changed the returned receipt on replay: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("launch acceptance exceeded bounded wait")
		}
	}
	if f.count(t, `SELECT count(*) FROM ingest.delivery_record WHERE delivery_kind = 'launch'`) != 2 || f.count(t, `SELECT count(*) FROM ingest.discovery_page WHERE state = 'accepted'`) != 2 || f.count(t, `SELECT count(*) FROM bronze.observation_revision`) != 4 {
		t.Fatal("opposite-order acceptance lost launches, pages or observations")
	}
}
