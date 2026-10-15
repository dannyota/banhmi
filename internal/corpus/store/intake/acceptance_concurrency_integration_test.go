package intake

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/quality/formats"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/document"
)

// TestAcceptanceAndSourceCaptureShareProtection exercises the real capture replay.
func TestAcceptanceAndSourceCaptureShareProtection(t *testing.T) {
	f := newAcceptanceFixture(t, model.QualityQuarantined)
	actor, capturer := boundedSourceAppConnection(t, f.db), boundedSourceAppConnection(t, f.db)
	locked, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	defer resume()
	adapter, err := NewAcceptance(sourceInsertGate{TransactionStarter: actor, query: "-- name: LockCatalogBlob :one", inserted: locked, resume: release}, "writer-a", f.objects, nil)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := NewSources(capturer, "writer-a", f.objects, fixtureSourceContinuer{})
	if err != nil {
		t.Fatal(err)
	}
	capture := f.input.Artifacts[0].Capture
	artifact := model.ArtifactInventoryEntry{ArtifactKey: "artifact-0", Role: "main", URL: "https://example.com/source", Required: true, Format: "html", Capture: &capture}
	input := CaptureAcceptance{RequestKey: fmt.Sprintf("request-%032x", 300), Capture: capture, Artifact: &artifact, CaptureURL: artifact.URL}
	accepted, captured := make(chan error, 1), make(chan error, 1)
	go func() { _, err := adapter.Apply(f.db.ctx, f.request); accepted <- err }()
	awaitAdapterSignal(t, f.db.ctx, locked)
	go func() {
		_, err := sources.ApplyCapture(f.db.ctx, sourceWork(f.doc, "bronze", "acceptance-original"), input)
		captured <- err
	}()
	waitAdapterBlockedBy(t, f.db.ctx, f.app, capturer.PgConn().PID(), actor.PgConn().PID())
	resume()
	if err := awaitAcceptanceError(t, f.db.ctx, accepted); err != nil {
		t.Fatal(err)
	}
	if err := awaitAcceptanceError(t, f.db.ctx, captured); err != nil {
		t.Fatal(err)
	}
	before := acceptanceSnapshot(t, f)
	if _, err := f.adapter.Apply(f.db.ctx, f.request); err != nil || !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
		t.Fatal("shared capture protection or acceptance replay changed", err)
	}
}

// TestAcceptanceFreshSnapshotWinner exercises a hidden committed conflict winner.
func TestAcceptanceFreshSnapshotWinner(t *testing.T) {
	f := newAcceptanceFixture(t, model.QualityQuarantined)
	first, err := f.adapter.Apply(f.db.ctx, f.request)
	if err != nil {
		t.Fatal(err)
	}
	before := acceptanceSnapshot(t, f)
	actor := boundedSourceAppConnection(t, f.db)
	begins, lookups, conflicts := 0, 0, 0
	probe := catalogProbeStarter{TransactionStarter: actor, begin: func() { begins++ }}
	probe.before = func(_ context.Context, query string, _ []any) error {
		if strings.Contains(query, "-- name: FindAcceptanceResult :one") {
			lookups++
			// Model the two earlier statement snapshots without hiding the
			// winner from the mandatory statement after ON CONFLICT.
			if lookups <= 2 {
				return pgx.ErrNoRows
			}
		}
		return nil
	}
	probe.after = func(_ context.Context, query string, _ []any, err error) error {
		if strings.Contains(query, "-- name: InsertAcceptanceResult :one") && errors.Is(err, pgx.ErrNoRows) {
			conflicts++
		}
		return nil
	}
	adapter, err := NewAcceptance(probe, "writer-a", f.objects, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := adapter.Apply(f.db.ctx, f.request)
	if err != nil || begins != 2 || conflicts != 1 || lookups < 4 || !reflect.DeepEqual(first, again) || !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
		t.Fatal("conflict winner did not roll back and replay through a fresh statement", err)
	}
}

func awaitAcceptanceError(t *testing.T, ctx context.Context, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		t.Fatal("acceptance did not finish within its bound")
		return ctx.Err()
	}
}

func waitAcceptanceExpiry(t *testing.T, f *acceptanceFixture) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var expired bool
		if err := f.app.QueryRow(f.db.ctx, `SELECT expires_at <= clock_timestamp() FROM ingest.execution_lease WHERE resource_kind='document_version' AND resource_key='version-a'`).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			return
		}
		select {
		case <-ticker.C:
		case <-f.db.ctx.Done():
			t.Fatal("lease did not expire within the fixture bound")
		}
	}
}

// TestAcceptanceExpiryAfterIdentityAndBlobWaits checks database-time precedence after lock waits.
func TestAcceptanceExpiryAfterIdentityAndBlobWaits(t *testing.T) {
	for _, point := range []string{"identity", "identity-conflict", "blob"} {
		t.Run(point, func(t *testing.T) {
			f := newAcceptanceFixture(t, model.QualityQuarantined)
			actor := boundedSourceAppConnection(t, f.db)
			blocker := boundedSourceAppConnection(t, f.db)
			r := f.request
			if point == "identity-conflict" {
				if _, err := f.adapter.Apply(f.db.ctx, r); err != nil {
					t.Fatal(err)
				}
				r.OutputDigest = fixtureDigest
			}
			if strings.HasPrefix(point, "identity") {
				if _, err := f.app.Exec(f.db.ctx, `INSERT INTO ingest.acceptance_lock(revision_key,processing_attempt_key) VALUES($1,'identity') ON CONFLICT DO NOTHING`, strings.Split(f.request.RevisionKey, ":rev:")[0]); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := blocker.Begin(f.db.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer sourceCleanup(t, f.db.ctx, tx.Rollback)
			var id int64
			if strings.HasPrefix(point, "identity") {
				err = tx.QueryRow(f.db.ctx, `SELECT id FROM ingest.acceptance_lock WHERE processing_attempt_key='identity' FOR UPDATE`).Scan(&id)
			} else {
				err = tx.QueryRow(f.db.ctx, `SELECT id FROM bronze.blob ORDER BY sha256 LIMIT 1 FOR UPDATE`).Scan(&id)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.app.Exec(f.db.ctx, `UPDATE ingest.execution_lease SET expires_at=clock_timestamp()+interval '5 seconds' WHERE resource_kind='document_version'`); err != nil {
				t.Fatal(err)
			}
			before := acceptanceSnapshot(t, f)
			adapter, err := NewAcceptance(actor, "writer-a", f.objects, nil)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := adapter.Apply(f.db.ctx, r); done <- err }()
			waitAdapterBlockedBy(t, f.db.ctx, f.app, actor.PgConn().PID(), blocker.PgConn().PID())
			waitAcceptanceExpiry(t, f)
			sourceCleanup(t, f.db.ctx, tx.Rollback)
			if err := awaitAcceptanceError(t, f.db.ctx, done); !errors.Is(err, run.ErrStaleGrant) {
				t.Fatalf("expired waiter lost stale-grant precedence: %v", err)
			}
			if !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
				t.Fatal("expired waiter changed evidence or synchronization rows")
			}
		})
	}
}

// TestAcceptanceExpiryDuringObjectRead checks lease expiry after a blocked object open.
func TestAcceptanceExpiryDuringObjectRead(t *testing.T) {
	f := newAcceptanceFixture(t, model.QualityQuarantined)
	actor := boundedSourceAppConnection(t, f.db)
	opened, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.objects.beforeOpen = func(ctx context.Context, _ string) error {
		once.Do(func() { close(opened) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if _, err := f.app.Exec(f.db.ctx, `UPDATE ingest.execution_lease SET expires_at=clock_timestamp()+interval '5 seconds' WHERE resource_kind='document_version'`); err != nil {
		t.Fatal(err)
	}
	before := acceptanceSnapshot(t, f)
	adapter, err := NewAcceptance(actor, "writer-a", f.objects, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := adapter.Apply(f.db.ctx, f.request); done <- err }()
	awaitAdapterSignal(t, f.db.ctx, opened)
	waitAcceptanceExpiry(t, f)
	close(release)
	if err := awaitAcceptanceError(t, f.db.ctx, done); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("object wait expiry lost stale-grant precedence: %v", err)
	}
	if !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
		t.Fatal("object wait expiry changed rows")
	}
}

type acceptanceBlockingReader struct {
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
	read    sync.Once
}

// Read blocks until concurrent Close interrupts the reader.
func (r *acceptanceBlockingReader) Read([]byte) (int, error) {
	r.read.Do(func() { close(r.started) })
	<-r.closed
	return 0, io.ErrClosedPipe
}

// Close releases a blocked synthetic read once.
func (r *acceptanceBlockingReader) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

type acceptanceBlockingObjects struct {
	*catalogObjects
	reader *acceptanceBlockingReader
}

// Open returns the cancellation probe's reader.
func (s acceptanceBlockingObjects) Open(context.Context, string) (io.ReadCloser, error) {
	return s.reader, nil
}

// TestAcceptanceCancellationInterruptsReadAndReleasesLocks checks cancellation and reader cleanup.
func TestAcceptanceCancellationInterruptsReadAndReleasesLocks(t *testing.T) {
	f := newAcceptanceFixture(t, model.QualityQuarantined)
	actor := boundedSourceAppConnection(t, f.db)
	reader := &acceptanceBlockingReader{started: make(chan struct{}), closed: make(chan struct{})}
	adapter, err := NewAcceptance(actor, "writer-a", acceptanceBlockingObjects{f.objects, reader}, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := acceptanceSnapshot(t, f)
	ctx, cancel := context.WithCancel(f.db.ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := adapter.Apply(ctx, f.request); done <- err }()
	awaitAdapterSignal(t, f.db.ctx, reader.started)
	cancel()
	if err := awaitAcceptanceError(t, f.db.ctx, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost its cause: %v", err)
	}
	awaitAdapterSignal(t, f.db.ctx, reader.closed)
	if !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
		t.Fatal("cancelled read changed rows")
	}
	if _, err := f.adapter.Apply(f.db.ctx, f.request); err != nil {
		t.Fatal("cancelled reader retained transaction locks")
	}
}

type acceptanceCommitFault struct {
	TransactionStarter
	commitFirst bool
	rollbackErr error
	commits     int
	begins      int
}

var acceptanceCommitFailure = errors.New("synthetic acceptance commit response lost")

// BeginTx counts attempts and wraps commit and rollback responses.
func (s *acceptanceCommitFault) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	s.begins++
	tx, err := s.TransactionStarter.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &acceptanceCommitFaultTx{Tx: tx, owner: s}, nil
}

type acceptanceCommitFaultTx struct {
	pgx.Tx
	owner *acceptanceCommitFault
}

// Commit optionally persists before returning a synthetic response loss.
func (tx *acceptanceCommitFaultTx) Commit(ctx context.Context) error {
	tx.owner.commits++
	if tx.owner.commitFirst {
		if err := tx.Tx.Commit(ctx); err != nil {
			return err
		}
	}
	return acceptanceCommitFailure
}

// Rollback preserves the database failure and the injected cleanup failure.
func (tx *acceptanceCommitFaultTx) Rollback(ctx context.Context) error {
	err := tx.Tx.Rollback(ctx)
	if errors.Is(err, pgx.ErrTxClosed) {
		return err
	}
	return errors.Join(err, tx.owner.rollbackErr)
}

// TestAcceptanceCommitLossAndRollbackCauses checks commit ambiguity and preserved rollback failures.
func TestAcceptanceCommitLossAndRollbackCauses(t *testing.T) {
	for _, name := range []string{"before-commit", "after-commit", "rollback-error"} {
		t.Run(name, func(t *testing.T) {
			f := newAcceptanceFixture(t, model.QualityQuarantined)
			fault := &acceptanceCommitFault{TransactionStarter: f.app, commitFirst: name == "after-commit"}
			rollbackErr := errors.New("synthetic acceptance rollback failure")
			if name == "rollback-error" {
				fault.rollbackErr = rollbackErr
			}
			adapter, err := NewAcceptance(fault, "writer-a", f.objects, nil)
			if err != nil {
				t.Fatal(err)
			}
			before := acceptanceSnapshot(t, f)
			if _, err := adapter.Apply(f.db.ctx, f.request); !errors.Is(err, acceptanceCommitFailure) || name == "rollback-error" && !errors.Is(err, rollbackErr) {
				t.Fatalf("commit or rollback failure lost its typed cause: %v", err)
			}
			if fault.begins != 1 || fault.commits != 1 {
				t.Fatal("adapter retried a failed commit or rollback")
			}
			if name != "after-commit" {
				if !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
					t.Fatal("crash before commit left partial rows")
				}
				return
			}
			committed := acceptanceSnapshot(t, f)
			receipt, err := f.adapter.Apply(f.db.ctx, f.request)
			if err != nil || receipt.ResultID == "" || !reflect.DeepEqual(committed, acceptanceSnapshot(t, f)) {
				t.Fatalf("lost commit response did not replay the original commit: %v", err)
			}
		})
	}
}

// TestAcceptanceCatalogReadsAndBlobLocksAreDeduplicatedAndOrdered checks closure caching and lock order.
func TestAcceptanceCatalogReadsAndBlobLocksAreDeduplicatedAndOrdered(t *testing.T) {
	f := newAcceptanceFixture(t, model.QualityQuarantined)
	reads := map[string]int{}
	var locked []string
	gate := sourceInsertGate{TransactionStarter: f.app, query: "synthetic-no-pause", record: func(query string, args []any, scanned bool, _ error) {
		if scanned || len(args) != 1 {
			return
		}
		if strings.Contains(query, "-- name: ReadSourceBlob :one") {
			reads[args[0].(string)]++
		}
		if strings.Contains(query, "-- name: LockCatalogBlob :one") {
			locked = append(locked, args[0].(string))
		}
	}}
	adapter, err := NewAcceptance(gate, "writer-a", f.objects, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Apply(f.db.ctx, f.request); err != nil {
		t.Fatal(err)
	}
	if len(locked) != len(f.request.Outputs) || len(reads) != len(locked) {
		t.Fatal("closure discovery omitted or repeated catalog identities")
	}
	for k, ref := range f.request.Outputs {
		if reads[string(ref.SHA256)] != 1 || locked[k] != string(ref.SHA256) {
			t.Fatal("logical aliases repeated a catalog read or blob locks left hash order")
		}
	}
}

// TestAcceptanceAndCatalogShareBlobsWithoutLockCycle checks opposite-order catalog contention.
func TestAcceptanceAndCatalogShareBlobsWithoutLockCycle(t *testing.T) {
	f := newAcceptanceFixture(t, model.QualityQuarantined)
	actor := boundedSourceAppConnection(t, f.db)
	registrar := boundedSourceAppConnection(t, f.db)
	work := f.work
	work.Execution.WorkflowID, work.Execution.AttemptKey = "document-ingest/vn/source-a/version-b", "catalog-race-attempt"
	work.Execution.Scope.Subject.DocumentVersion = "version-b"
	lease, err := f.ledger.ClaimLease(f.db.ctx, 1, work.Execution.Scope, work.Execution.Owner, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	work.Execution.AttemptFence = lease.AttemptFence
	if _, err := f.ledger.RegisterExecution(f.db.ctx, work, "synthetic catalog contender", nil); err != nil {
		t.Fatal(err)
	}
	root := catalogMetadata(f.objects, "acceptance/catalog-race", "synthetic shared-blob catalog race")
	refs := append([]model.AssetRef(nil), f.request.Outputs...)
	for left, right := 0, len(refs)-1; left < right; left, right = left+1, right-1 {
		refs[left], refs[right] = refs[right], refs[left]
	}
	catalog, err := NewConversionCatalog(registrar, "writer-a", f.objects, formats.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	locked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	resume := func() { once.Do(func() { close(release) }) }
	defer resume()
	gate := sourceInsertGate{TransactionStarter: actor, query: "-- name: LockCatalogBlob :one", inserted: locked, resume: release}
	adapter, err := NewAcceptance(gate, "writer-a", f.objects, nil)
	if err != nil {
		t.Fatal(err)
	}
	accepted, registered := make(chan error, 1), make(chan error, 1)
	go func() { _, err := adapter.Apply(f.db.ctx, f.request); accepted <- err }()
	awaitAdapterSignal(t, f.db.ctx, locked)
	go func() {
		_, err := catalog.RegisterConversionOutputs(f.db.ctx, catalogWork(work, root), ConversionRegistration{Manifest: root, MemberRefs: refs})
		registered <- err
	}()
	waitAdapterBlockedBy(t, f.db.ctx, f.app, registrar.PgConn().PID(), actor.PgConn().PID())
	resume()
	if err := awaitAcceptanceError(t, f.db.ctx, accepted); err != nil {
		t.Fatal(err)
	}
	if err := awaitAcceptanceError(t, f.db.ctx, registered); err != nil {
		t.Fatal("catalog and acceptance did not complete without a lock cycle")
	}
	for _, ref := range refs {
		if f.count(t, `SELECT count(*) FROM bronze.blob WHERE sha256=$1`, string(ref.SHA256)) != 1 {
			t.Fatal("shared-blob race duplicated a catalog identity")
		}
	}
}

// TestAcceptanceFreezesDocumentChildrenUnderItsLease checks serialization with the conversion writer.
func TestAcceptanceFreezesDocumentChildrenUnderItsLease(t *testing.T) {
	f := newAcceptanceFixture(t, model.QualityQuarantined)
	actor := boundedSourceAppConnection(t, f.db)
	writer := boundedSourceAppConnection(t, f.db)
	report := f.gate
	report.Artifact.Findings = append(append([]model.Finding{}, report.Artifact.Findings...), model.Finding{ReasonCode: "ADDITIONAL_OBSERVATION", Severity: model.SeverityInfo, Scope: model.GapArtifact, ScopeKey: f.input.Artifacts[0].ArtifactKey, Spans: []model.Span{}, Evidence: []model.AssetRef{}})
	codec, err := formats.GateReportCodec(f.policy, formats.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	data, _, err := codec.Encode(report)
	if err != nil {
		t.Fatal(err)
	}
	metadata := f.register(t, data)
	parent, err := formats.ProjectArtifactParent(report, catalogRef(metadata), f.policy, formats.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	rows, err := formats.ProjectQualityRows(report.Artifact.Metrics, report.Artifact.Findings, []formats.Gap{}, f.request.Outputs, formats.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	var records []document.ConversionQualityRecord
	for _, row := range rows {
		records = append(records, document.ConversionQualityRecord{Ordinal: row.Ordinal, Kind: row.RecordKind, Code: row.Code, Severity: row.Severity, Scope: row.Scope, ScopeKey: row.ScopeKey, Reason: row.Reason, MetricKind: row.MetricKind, MetricState: row.MetricState, Numerator: row.Numerator, Denominator: row.Denominator, Threshold: row.Threshold, Mandatory: row.Mandatory, Passed: row.Passed, Evidence: acceptanceJSON(t, row.Evidence), SourceSpans: acceptanceJSON(t, row.SourceSpans)})
	}
	conversion, err := document.NewConversion(writer, "writer-a")
	if err != nil {
		t.Fatal(err)
	}
	locked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	resume := func() { once.Do(func() { close(release) }) }
	defer resume()
	gate := sourceInsertGate{TransactionStarter: actor, query: "-- name: LockCatalogBlob :one", inserted: locked, resume: release}
	adapter, err := NewAcceptance(gate, "writer-a", f.objects, nil)
	if err != nil {
		t.Fatal(err)
	}
	accepted, inserted := make(chan error, 1), make(chan error, 1)
	go func() { _, err := adapter.Apply(f.db.ctx, f.request); accepted <- err }()
	awaitAdapterSignal(t, f.db.ctx, locked)
	go func() {
		_, err := conversion.RecordConversionQualityReport(f.db.ctx, f.grant, document.ConversionTarget{RevisionKey: f.request.RevisionKey, CandidateDigest: f.request.CandidateDigest}, document.ConversionQualityInput{Report: metadata, ArtifactHash: parent.ArtifactHash, PolicyDigest: parent.PolicyHash, ThresholdVersion: parent.ThresholdVersion, Outcome: parent.Outcome, RecordCount: parent.RecordCount, Records: records})
		inserted <- err
	}()
	waitAdapterBlockedBy(t, f.db.ctx, f.app, writer.PgConn().PID(), actor.PgConn().PID())
	if f.count(t, `SELECT count(*) FROM silver.quality_report WHERE report_hash=$1`, metadata.SHA256) != 0 {
		t.Fatal("child report appeared while acceptance held the version lease")
	}
	resume()
	if err := awaitAcceptanceError(t, f.db.ctx, accepted); err != nil {
		t.Fatal(err)
	}
	if err := awaitAcceptanceError(t, f.db.ctx, inserted); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT count(*) FROM silver.quality_report WHERE report_hash=$1`, metadata.SHA256) != 1 {
		t.Fatal("serialized child insertion did not commit exactly once")
	}
	before := acceptanceSnapshot(t, f)
	if _, err := f.adapter.Apply(f.db.ctx, f.request); err != nil || !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
		t.Fatal("a later report changed replay of the selected immutable report")
	}
}
