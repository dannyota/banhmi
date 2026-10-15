package intake

import (
	"context"
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
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

type admissionObserverFunc func(context.Context, string) (run.DocumentExecutionObservation, error)

func (f admissionObserverFunc) Observe(ctx context.Context, workflow string) (run.DocumentExecutionObservation, error) {
	return f(ctx, workflow)
}

type admissionFixture struct {
	source  *sourcesAdapterFixture
	adapter *DocumentAdmission
	control run.IntakeWork
	request run.AdmissionReserveRequest
	status  run.DocumentExecutionStatus
}

func newAdmissionFixture(t *testing.T, configured bool) *admissionFixture {
	t.Helper()
	s := newSourcesAdapterFixture(t)
	f := &admissionFixture{source: s, status: run.DocumentExecutionUnknown}
	f.control = s.window
	f.control.Execution.WorkflowKind, f.control.Execution.WorkflowID = run.ControlReconcile, "corpus-control/vn"
	f.control.Execution.Scope = run.ExecutionScope{Kind: run.ScopeControl, CountryControl: "vn"}
	lease, err := s.ledger.ClaimLease(s.db.ctx, 1, f.control.Execution.Scope, f.control.Execution.Owner, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	f.control.Execution.AttemptFence = lease.AttemptFence
	if _, err := s.ledger.RegisterExecution(s.db.ctx, f.control, "synthetic-control", nil); err != nil {
		t.Fatal(err)
	}
	f.adapter, err = NewDocumentAdmission(s.app, "writer-a", admissionObserverFunc(func(_ context.Context, workflow string) (run.DocumentExecutionObservation, error) {
		return run.DocumentExecutionObservation{WorkflowID: workflow, Status: f.status, ChainReceipt: "synthetic-chain"}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	request := "request-00000000000000000000000000000400"
	page := s.page(t, 0, true, true, func(page *api.DecodedDiscoveryPage) {
		page.FollowUps = []model.FollowUpRequest{{RequestKey: request, WindowKey: "window-a", ParentCapture: page.Capture.CaptureKey,
			Purpose: model.PurposeDetail, URL: "https://example.com/detail", PolicyHash: fixtureDigest, BudgetHash: fixtureDigest, Budget: s.frozen.Request.Budget}}
	})
	if _, err := s.sources.ApplyDiscoveryPage(s.db.ctx, sourceWork(s.window, "accept", page.PageKey),
		DiscoveryAcceptance{Page: page, Ordinal: 0, Launches: []SourceLaunch{{RequestKey: request, Work: s.doc, DueAt: time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)}}}); err != nil {
		t.Fatal(err)
	}
	f.request = run.AdmissionReserveRequest{Actor: s.window, Child: s.doc, DeliveryKey: request}
	if configured {
		if _, err := f.adapter.Configure(s.db.ctx, run.AdmissionConfigureRequest{Actor: f.control, Limits: run.DefaultDocumentAdmissionLimits()}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// Include every writer row, not just counts or the admission tables. Identity
// sequences are not transactional and are deliberately not row-set evidence.
func admissionRows(t *testing.T, f *admissionFixture) string {
	t.Helper()
	db := f.source.db
	rows, err := db.conn.Query(db.ctx, `SELECT n.nspname, c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname IN ('ingest','bronze','silver','gold','config') AND c.relkind='r' ORDER BY n.nspname,c.relname`)
	if err != nil {
		t.Fatal(err)
	}
	var tables [][2]string
	for rows.Next() {
		var table [2]string
		if err := rows.Scan(&table[0], &table[1]); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	var result strings.Builder
	for _, table := range tables {
		var content string
		if err := db.conn.QueryRow(db.ctx, "SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY id),'[]'::jsonb)::text FROM "+pgx.Identifier{table[0], table[1]}.Sanitize()+" r").Scan(&content); err != nil {
			t.Fatal(err)
		}
		result.WriteString(table[0] + "." + table[1] + ":" + content + "\n")
	}
	return result.String()
}

func requireAdmissionUnchanged(t *testing.T, f *admissionFixture, before string) {
	t.Helper()
	if admissionRows(t, f) != before {
		t.Fatal("rejection or read changed the full writer row set")
	}
}

func (f *admissionFixture) reserve(t *testing.T) run.AdmissionReceipt {
	t.Helper()
	r, err := f.adapter.Reserve(f.source.db.ctx, f.request)
	if err != nil || r.Receipt == nil || r.Refusal != "" {
		t.Fatalf("reserve: %+v %v", r, err)
	}
	return *r.Receipt
}

func admissionChange(f *admissionFixture, receipt run.AdmissionReceipt, control bool) run.AdmissionChange {
	actor := f.request.Child
	if control {
		actor = f.control
	}
	return run.AdmissionChange{Actor: actor, SlotKey: receipt.SlotKey, ExpectedRevision: receipt.Revision}
}

func TestAdmissionUnconfiguredAndInvalidHaveNoWrites(t *testing.T) {
	f := newAdmissionFixture(t, false)
	before := admissionRows(t, f)
	_, err := f.adapter.Reserve(f.source.db.ctx, f.request)
	if !errors.Is(err, run.ErrAdmissionUnconfigured) {
		t.Fatal(err)
	}
	requireAdmissionUnchanged(t, f, before)
	bad := run.DefaultDocumentAdmissionLimits()
	bad.Open = 0
	_, err = f.adapter.Configure(f.source.db.ctx, run.AdmissionConfigureRequest{Actor: f.control, Limits: bad})
	if !errors.Is(err, run.ErrInvalidAdmission) {
		t.Fatal(err)
	}
	requireAdmissionUnchanged(t, f, before)
	r := f.request
	r.Actor.Execution.WorkflowKind, r.Actor.Execution.WorkflowID = run.Reprocess, "reprocess/vn/selection-a"
	r.Actor.Execution.Scope.Subject = run.IntakeSubject{Kind: run.SubjectReprocessSelection, ReprocessSelection: "selection-a"}
	_, err = f.adapter.Reserve(f.source.db.ctx, r)
	if !errors.Is(err, run.ErrInvalidAdmission) {
		t.Fatal("reprocess dispatcher was not refused")
	}
	requireAdmissionUnchanged(t, f, before)
}

func TestAdmissionResolveAndImmutableReplay(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := f.source.db.ctx
	before := admissionRows(t, f)
	request := run.AdmissionResolveRequest{Actor: f.request.Child, DocumentWork: &f.request.Child}
	resolved, err := f.adapter.Resolve(ctx, request)
	if err != nil || resolved.Found {
		t.Fatalf("registered work before reservation: %+v %v", resolved, err)
	}
	requireAdmissionUnchanged(t, f, before)
	reserved := f.reserve(t)
	bound, err := f.adapter.Bind(ctx, admissionChange(f, reserved, false))
	if err != nil || bound.State != run.AdmissionProcessing {
		t.Fatalf("bind: %+v %v", bound, err)
	}
	before = admissionRows(t, f)
	replay, err := f.adapter.Reserve(ctx, f.request)
	if err != nil || replay.Receipt == nil || *replay.Receipt != reserved {
		t.Fatalf("response-loss replay after Bind: %+v %v", replay, err)
	}
	resolved, err = f.adapter.Resolve(ctx, request)
	if err != nil || !resolved.Found || resolved.Revision != bound.Revision || resolved.State != run.AdmissionProcessing || resolved.LifecycleReceipt != bound.Key {
		t.Fatalf("resolution after later transition: %+v %v", resolved, err)
	}
	requireAdmissionUnchanged(t, f, before)
	_, err = f.adapter.Bind(ctx, run.AdmissionChange{Actor: f.request.Child, SlotKey: reserved.SlotKey, ExpectedRevision: 99})
	if !errors.Is(err, run.ErrAdmissionConflict) {
		t.Fatal(err)
	}
	requireAdmissionUnchanged(t, f, before)
}

func TestAdmissionGrantNegativesAndIdentityHaveNoWrites(t *testing.T) {
	for _, name := range []string{"epoch", "writer", "owner", "fence", "country", "cancelled", "expired", "workflow", "version", "attempt", "payload"} {
		t.Run(name, func(t *testing.T) {
			f := newAdmissionFixture(t, true)
			f.reserve(t)
			r := f.request
			switch name {
			case "epoch":
				r.Actor.Execution.AdmissionEpoch++
			case "writer":
				f.adapter.ledger.writer = "writer-b"
			case "owner":
				r.Actor.Execution.Owner = "owner-b"
			case "fence":
				r.Actor.Execution.AttemptFence++
			case "country":
				r.Actor.Execution.Country = "my"
			case "cancelled", "expired":
				column, expression := "cancelled_at", "clock_timestamp()"
				if name == "expired" {
					column, expression = "expires_at", "clock_timestamp() - interval '1 second'"
				}
				if _, err := f.source.db.conn.Exec(f.source.db.ctx, "UPDATE ingest.execution_lease SET "+column+"="+expression+" WHERE resource_kind='source_window'"); err != nil {
					t.Fatal(err)
				}
			case "workflow":
				r.Child.Execution.WorkflowID = "document-ingest/vn/source-a/version-a"
			case "version":
				r.Child.Execution.Scope.Subject.DocumentVersion = "version-b"
			case "attempt":
				r.Child.Execution.AttemptKey = "attempt-b"
			case "payload":
				r.Child.RecipeDigest = model.Digest(strings.Repeat("b", 64))
			}
			before := admissionRows(t, f)
			if _, err := f.adapter.Reserve(f.source.db.ctx, r); err == nil {
				t.Fatal("invalid replay succeeded")
			}
			requireAdmissionUnchanged(t, f, before)
		})
	}
}

func TestAdmissionFailureFenceClosureAndLateApply(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := f.source.db.ctx
	reserved := f.reserve(t)
	bound, err := f.adapter.Bind(ctx, admissionChange(f, reserved, false))
	if err != nil {
		t.Fatal(err)
	}
	f.status = run.DocumentExecutionTerminal
	closing, err := f.adapter.Reconcile(ctx, admissionChange(f, bound, true))
	if err != nil || closing.State != run.AdmissionClosing {
		t.Fatalf("failed chain: %+v %v", closing, err)
	}
	var cancelled bool
	if err := f.source.app.QueryRow(ctx, `SELECT cancelled_at IS NOT NULL FROM ingest.execution_lease WHERE resource_kind='document_version'`).Scan(&cancelled); err != nil || !cancelled {
		t.Fatal("failure did not fence orphan grant")
	}
	before := admissionRows(t, f)
	r := run.IntakeApplyRequest{Work: f.request.Child, IdempotencyKey: run.IntakeIdempotencyKey(f.request.Child), OutputDigest: fixtureDigest,
		Outcome: run.OutcomeSucceeded, Counts: run.Counts{Expected: 1, Processed: 1, Succeeded: 1}, Complete: true}
	if _, err := f.source.ledger.Apply(ctx, r); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatal("late orphan Apply was not fenced")
	}
	requireAdmissionUnchanged(t, f, before)
	f.status = run.DocumentExecutionUnknown
	if _, err := f.adapter.Reconcile(ctx, admissionChange(f, closing, true)); !errors.Is(err, run.ErrAdmissionUnchanged) {
		t.Fatal("unknown closure released capacity")
	}
	requireAdmissionUnchanged(t, f, before)
	f.status = run.DocumentExecutionTerminal
	released, err := f.adapter.Reconcile(ctx, admissionChange(f, closing, true))
	if err != nil || released.State != run.AdmissionReleased {
		t.Fatalf("confirmed release: %+v %v", released, err)
	}
	before = admissionRows(t, f)
	replay, err := f.adapter.Reconcile(ctx, admissionChange(f, closing, true))
	if err != nil || replay != released {
		t.Fatal("release response loss was not idempotent")
	}
	requireAdmissionUnchanged(t, f, before)
}

func TestAdmissionLostGrantStopAndSupersededLease(t *testing.T) {
	for _, superseded := range []bool{false, true} {
		t.Run(fmt.Sprint(superseded), func(t *testing.T) {
			f := newAdmissionFixture(t, true)
			ctx := f.source.db.ctx
			reserved := f.reserve(t)
			closeRequest := run.AdmissionCloseRequest{Change: admissionChange(f, reserved, true), Reason: run.AdmissionAbandoned}
			before := admissionRows(t, f)
			if _, err := f.adapter.Close(ctx, closeRequest); !errors.Is(err, run.ErrAdmissionConflict) {
				t.Fatal("live matching grant could be abandoned")
			}
			requireAdmissionUnchanged(t, f, before)
			if _, err := f.source.app.Exec(ctx, `UPDATE ingest.execution_lease SET expires_at=clock_timestamp()-interval '1 second' WHERE resource_kind='document_version'`); err != nil {
				t.Fatal(err)
			}
			fresh := f.request.Child
			if superseded {
				fresh.Execution.Owner = "owner-b"
				lease, err := f.source.ledger.ReclaimLease(ctx, 1, fresh.Execution.Scope, fresh.Execution.Owner, time.Now().Add(time.Hour))
				if err != nil {
					t.Fatal(err)
				}
				fresh.Execution.AttemptFence = lease.AttemptFence
				if _, err := f.source.ledger.RegisterExecution(ctx, fresh, "reclaimed-run", nil); err != nil {
					t.Fatal(err)
				}
			}
			stop, err := f.adapter.Close(ctx, closeRequest)
			if err != nil || stop.State != run.AdmissionClosing {
				t.Fatalf("durable stop: %+v %v", stop, err)
			}
			before = admissionRows(t, f)
			replay, err := f.adapter.Close(ctx, closeRequest)
			if err != nil || replay != stop {
				t.Fatal("lost stop response changed the receipt")
			}
			if _, err := f.adapter.Bind(ctx, run.AdmissionChange{Actor: fresh, SlotKey: stop.SlotKey, ExpectedRevision: stop.Revision}); err == nil {
				t.Fatal("delayed start bound a stopped slot")
			}
			requireAdmissionUnchanged(t, f, before)
			var cancelled bool
			if err := f.source.app.QueryRow(ctx, `SELECT cancelled_at IS NOT NULL FROM ingest.execution_lease WHERE resource_kind='document_version'`).Scan(&cancelled); err != nil || cancelled == superseded {
				t.Fatal("stop cancelled the wrong grant")
			}
		})
	}
}

func TestAdmissionConfigureReplayAndLowering(t *testing.T) {
	f := newAdmissionFixture(t, false)
	ctx := f.source.db.ctx
	r := run.AdmissionConfigureRequest{Actor: f.control, Limits: run.DefaultDocumentAdmissionLimits()}
	first, err := f.adapter.Configure(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	reserved := f.reserve(t)
	r.ExpectedRevision, r.Limits.Processing, r.Limits.Open = first.Revision, 1, 1
	if _, err := f.adapter.Configure(ctx, r); err != nil {
		t.Fatal(err)
	}
	before := admissionRows(t, f)
	r.ExpectedRevision, r.Limits = 0, run.DefaultDocumentAdmissionLimits()
	replay, err := f.adapter.Configure(ctx, r)
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatal("lowering invalidated configuration replay")
	}
	requireAdmissionUnchanged(t, f, before)
	if _, err := f.adapter.Bind(ctx, admissionChange(f, reserved, false)); err != nil {
		t.Fatal("lowering prevented existing occupancy from binding")
	}
	var turn string
	if err := f.source.app.QueryRow(ctx, `SELECT next_queue FROM ingest.document_admission_limit`).Scan(&turn); err != nil || turn != "launch" {
		t.Fatal("uncontested progress or Configure consumed the turn")
	}
}

func admissionCurrentSlot(t *testing.T, f *admissionFixture, receipt run.AdmissionReceipt) intakedb.IngestDocumentAdmissionSlot {
	t.Helper()
	s, err := intakedb.New(f.source.app).ReadDocumentAdmissionSlot(f.source.db.ctx, receipt.SlotKey)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func admissionJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
