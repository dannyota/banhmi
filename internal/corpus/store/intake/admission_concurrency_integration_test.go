package intake

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

type admissionCommitStarter struct {
	TransactionStarter
	entered chan struct{}
	release chan struct{}
	fail    error
	commit  bool
}

func (s *admissionCommitStarter) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	tx, err := s.TransactionStarter.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &admissionCommitTx{Tx: tx, starter: s}, nil
}

type admissionCommitTx struct {
	pgx.Tx
	starter *admissionCommitStarter
}

func (tx *admissionCommitTx) Commit(ctx context.Context) error {
	s := tx.starter
	if s.entered != nil {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.commit {
		if err := tx.Tx.Commit(ctx); err != nil {
			return err
		}
	}
	return s.fail
}

func TestAdmissionCommitUncertaintyAndRollback(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			f := newAdmissionFixture(t, true)
			before := admissionRows(t, f)
			lost := errors.New("synthetic admission response loss")
			fault := &admissionCommitStarter{TransactionStarter: f.source.app, fail: lost, commit: committed}
			adapter, err := NewDocumentAdmission(fault, "writer-a", f.adapter.observer)
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Reserve(f.source.db.ctx, f.request)
			if !errors.Is(err, lost) || result.Refusal != "" || result.Receipt != nil {
				t.Fatal("uncertain commit was classified as capacity or success")
			}
			if !committed {
				requireAdmissionUnchanged(t, f, before)
			}
			receipt := f.reserve(t)
			before = admissionRows(t, f)
			if replay := f.reserve(t); replay != receipt {
				t.Fatal("commit recovery changed its immutable receipt")
			}
			requireAdmissionUnchanged(t, f, before)
		})
	}
}

func TestAdmissionConcurrentParentsHaveOneFinalSlot(t *testing.T) {
	for _, ceiling := range []int32{1, 32} {
		t.Run(fmt.Sprint(ceiling), func(t *testing.T) {
			f := newAdmissionFixture(t, false)
			ctx := f.source.db.ctx
			limits := run.DefaultDocumentAdmissionLimits()
			limits.Processing, limits.Open = ceiling, ceiling
			if _, err := f.adapter.Configure(ctx, run.AdmissionConfigureRequest{Actor: f.control, Limits: limits}); err != nil {
				t.Fatal(err)
			}
			base := time.Now().Add(-time.Second).UTC().Truncate(time.Microsecond)
			var contenders []run.AdmissionReserveRequest
			for i := 0; i < int(ceiling)+1; i++ {
				request := f.request
				if i > 0 {
					request = admissionSeedLaunch(t, f, i, base.Add(time.Duration(i)*time.Microsecond))
				}
				if i < int(ceiling)-1 {
					admissionUse(t, f, request)
				} else {
					contenders = append(contenders, request)
				}
			}
			blocker := sourceAppConnection(t, f.source.db)
			tx, err := blocker.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			if _, err := tx.Exec(ctx, `SELECT id FROM ingest.document_admission_limit FOR UPDATE`); err != nil {
				t.Fatal(err)
			}
			type outcome struct {
				result run.AdmissionResult
				err    error
			}
			done := make(chan outcome, 2)
			// Arrival is deliberately opposite immutable FIFO order.
			for _, i := range []int{1, 0} {
				conn := sourceAppConnection(t, f.source.db)
				adapter, err := NewDocumentAdmission(conn, "writer-a", f.adapter.observer)
				if err != nil {
					t.Fatal(err)
				}
				request := contenders[i]
				go func() {
					result, err := adapter.Reserve(ctx, request)
					done <- outcome{result, err}
				}()
				waitAdapterBlockedBy(t, ctx, f.source.db.conn, conn.PgConn().PID(), blocker.PgConn().PID())
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			winners := 0
			for range 2 {
				select {
				case got := <-done:
					if got.err != nil {
						t.Fatal(got.err)
					}
					if got.result.Receipt != nil {
						winners++
					} else if got.result.Refusal != run.AdmissionBothFull && got.result.Refusal != run.AdmissionNotTurn {
						t.Fatal("unexpected arbitration refusal")
					}
				case <-ctx.Done():
					t.Fatal("concurrent admission did not complete")
				}
			}
			counts, err := intakedb.New(f.source.app).CountDocumentAdmissionSlots(ctx)
			if err != nil || winners != 1 || counts.Processing != int64(ceiling) || counts.Open != int64(ceiling) {
				t.Fatal("concurrent parents exceeded the final slot")
			}
			before := admissionRows(t, f)
			if result, err := f.adapter.Reserve(ctx, contenders[1]); err != nil || result.Refusal != run.AdmissionBothFull {
				t.Fatal("losing parent did not retain its pending intent")
			}
			requireAdmissionUnchanged(t, f, before)
		})
	}
}

func TestAdmissionExpiryAfterEveryLockWait(t *testing.T) {
	for _, lock := range []string{"lease", "window", "delivery", "capacity", "slot"} {
		t.Run(lock, func(t *testing.T) {
			f := newAdmissionFixture(t, true)
			ctx := f.source.db.ctx
			var reserved run.AdmissionReceipt
			if lock == "slot" {
				reserved = f.reserve(t)
			}
			resource := "source_window"
			if lock == "slot" {
				resource = "document_version"
			}
			if _, err := f.source.app.Exec(ctx, `UPDATE ingest.execution_lease SET expires_at=clock_timestamp()+interval '1 second' WHERE resource_kind=$1`, resource); err != nil {
				t.Fatal(err)
			}
			blocker := sourceAppConnection(t, f.source.db)
			tx, err := blocker.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			query := map[string]string{
				"lease": `SELECT id FROM ingest.execution_lease WHERE resource_kind='source_window' FOR UPDATE`,
				"window": `SELECT id FROM ingest.source_window FOR UPDATE`,
				"delivery": `SELECT id FROM ingest.delivery_record WHERE delivery_kind='launch' FOR UPDATE`,
				"capacity": `SELECT id FROM ingest.document_admission_limit FOR UPDATE`,
				"slot": `SELECT id FROM ingest.document_admission_slot FOR UPDATE`,
			}[lock]
			if _, err := tx.Exec(ctx, query); err != nil {
				t.Fatal(err)
			}
			conn := sourceAppConnection(t, f.source.db)
			adapter, err := NewDocumentAdmission(conn, "writer-a", f.adapter.observer)
			if err != nil {
				t.Fatal(err)
			}
			before := admissionRows(t, f)
			done := make(chan error, 1)
			go func() {
				if lock == "slot" {
					_, err := adapter.Bind(ctx, admissionChange(f, reserved, false))
					done <- err
				} else {
					_, err := adapter.Reserve(ctx, f.request)
					done <- err
				}
			}()
			waitAdapterBlockedBy(t, ctx, f.source.db.conn, conn.PgConn().PID(), blocker.PgConn().PID())
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var expired bool
				if err := f.source.db.conn.QueryRow(ctx, `SELECT expires_at<=clock_timestamp() FROM ingest.execution_lease WHERE resource_kind=$1`, resource).Scan(&expired); err != nil {
					t.Fatal(err)
				}
				if expired {
					break
				}
				select {
				case <-ticker.C:
				case <-ctx.Done():
					t.Fatal("lease did not reach database expiry")
				}
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := awaitAcceptanceError(t, ctx, done); !errors.Is(err, run.ErrStaleGrant) {
				t.Fatal("expiry after lock wait did not fence the operation")
			}
			requireAdmissionUnchanged(t, f, before)
		})
	}
}

func TestAdmissionApplyAndFailureSerializeOnTargetLease(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := f.source.db.ctx
	reserved := f.reserve(t)
	gate := newGatedVerifier(t)
	f.source.ledger.verifier = gate
	w := f.request.Child
	w.Stage, w.Substep, w.Shard = run.Normalize, "evidence-acceptance", fixtureRevision
	r := run.IntakeApplyRequest{Work: w, IdempotencyKey: run.IntakeIdempotencyKey(w), OutputDigest: fixtureDigest,
		Outcome: run.OutcomeSucceeded, Acceptance: model.Accepted, RevisionKey: fixtureRevision,
		CandidateDigest: fixtureDigest, QualityReportHash: fixtureDigest, OriginalHash: fixtureDigest,
		Counts: run.Counts{Expected: 1, Processed: 1, Succeeded: 1}, Complete: true}
	applied := make(chan error, 1)
	go func() {
		_, err := f.source.ledger.Apply(ctx, r)
		applied <- err
	}()
	awaitAdapterSignal(t, ctx, gate.entered)
	f.status = run.DocumentExecutionTerminal
	conn := sourceAppConnection(t, f.source.db)
	adapter, err := NewDocumentAdmission(conn, "writer-a", f.adapter.observer)
	if err != nil {
		t.Fatal(err)
	}
	reconciled := make(chan error, 1)
	go func() {
		_, err := adapter.Reconcile(ctx, admissionChange(f, reserved, true))
		reconciled <- err
	}()
	waitAdapterBlockedBy(t, ctx, f.source.db.conn, conn.PgConn().PID(), f.source.app.PgConn().PID())
	gate.release()
	if err := awaitAcceptanceError(t, ctx, applied); err != nil {
		t.Fatal(err)
	}
	if err := awaitAcceptanceError(t, ctx, reconciled); err != nil {
		t.Fatal(err)
	}
	slot := admissionCurrentSlot(t, f, reserved)
	if slot.State != "closing" || slot.TerminalReason == nil || *slot.TerminalReason != "completed" || slot.SettledWatermark == 0 {
		t.Fatal("failure did not settle the acceptance that won the lease lock")
	}
}

func TestAdmissionConfigureLoweringWinsBeforeWaitingReserve(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := f.source.db.ctx
	f.reserve(t)
	other := admissionSeedLaunch(t, f, 1, time.Now().Add(-time.Second))
	conn := sourceAppConnection(t, f.source.db)
	gate := &admissionCommitStarter{TransactionStarter: conn, entered: make(chan struct{}), release: make(chan struct{}), commit: true}
	adapter, err := NewDocumentAdmission(gate, "writer-a", f.adapter.observer)
	if err != nil {
		t.Fatal(err)
	}
	configured := make(chan error, 1)
	go func() {
		limits := run.DefaultDocumentAdmissionLimits()
		limits.Processing, limits.Open = 1, 1
		_, err := adapter.Configure(ctx, run.AdmissionConfigureRequest{Actor: f.control, ExpectedRevision: 1, Limits: limits})
		configured <- err
	}()
	awaitAdapterSignal(t, ctx, gate.entered)
	reserved := make(chan error, 1)
	go func() {
		result, err := f.adapter.Reserve(ctx, other)
		if err == nil && result.Refusal != run.AdmissionBothFull {
			err = errors.New("waiting reserve ignored lowered limits")
		}
		reserved <- err
	}()
	waitAdapterBlockedBy(t, ctx, f.source.db.conn, f.source.app.PgConn().PID(), conn.PgConn().PID())
	close(gate.release)
	if err := awaitAcceptanceError(t, ctx, configured); err != nil {
		t.Fatal(err)
	}
	if err := awaitAcceptanceError(t, ctx, reserved); err != nil {
		t.Fatal(err)
	}
}
