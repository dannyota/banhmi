package intake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"danny.vn/banhmi/internal/corpus/blob"
	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/quality/formats"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

// The no-guard switch belongs only to the deadlock control. Production has no
// option to bypass the guard. Callbacks record attempts before delegation.
type catalogProbeStarter struct {
	TransactionStarter
	skipGuard     bool
	begin         func()
	before        func(context.Context, string, []any) error
	after         func(context.Context, string, []any, error) error
	rollbackError error
}

func (s catalogProbeStarter) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	if s.begin != nil {
		s.begin()
	}
	tx, err := s.TransactionStarter.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return catalogProbeTx{Tx: tx, probe: s}, nil
}

type catalogProbeTx struct {
	pgx.Tx
	probe catalogProbeStarter
}

func (tx catalogProbeTx) Rollback(ctx context.Context) error {
	err := tx.Tx.Rollback(ctx)
	if errors.Is(err, pgx.ErrTxClosed) {
		return err
	}
	return errors.Join(err, tx.probe.rollbackError)
}

func (tx catalogProbeTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if tx.probe.before != nil {
		if err := tx.probe.before(ctx, query, args); err != nil {
			return pgconn.CommandTag{}, err
		}
	}
	var tag pgconn.CommandTag
	var err error
	if !tx.probe.skipGuard || !strings.Contains(query, "pg_advisory_xact_lock(1129271888, 2)") {
		tag, err = tx.Tx.Exec(ctx, query, args...)
	}
	if tx.probe.after != nil {
		if afterErr := tx.probe.after(ctx, query, args, err); afterErr != nil {
			return tag, afterErr
		}
	}
	return tag, err
}

func (tx catalogProbeTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if tx.probe.before != nil {
		if err := tx.probe.before(ctx, query, args); err != nil {
			return catalogProbeRow{cause: err}
		}
	}
	return catalogProbeRow{Row: tx.Tx.QueryRow(ctx, query, args...), ctx: ctx, query: query, args: args, after: tx.probe.after}
}

type catalogProbeRow struct {
	pgx.Row
	ctx   context.Context
	query string
	args  []any
	cause error
	after func(context.Context, string, []any, error) error
}

func (row catalogProbeRow) Scan(dest ...any) error {
	if row.cause != nil {
		return row.cause
	}
	err := row.Row.Scan(dest...)
	if row.after != nil {
		if afterErr := row.after(row.ctx, row.query, row.args, err); afterErr != nil {
			return afterErr
		}
	}
	return err
}

func catalogOtherWork(t *testing.T, f catalogFixture, conn *pgx.Conn, suffix string, root blob.Metadata) run.IntakeWork {
	t.Helper()
	l, err := NewLedger(conn, "writer-a", f.objects, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := catalogWork(f.doc, root)
	w.Execution.Scope.Subject.DocumentVersion = run.DocumentVersionKey("version-" + suffix)
	w.Execution.WorkflowID = "document-ingest/vn/source-a/version-" + suffix
	lease, err := l.ClaimLease(f.db.ctx, 1, w.Execution.Scope, w.Execution.Owner, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	w.Execution.AttemptFence = lease.AttemptFence
	if _, err := l.RegisterExecution(f.db.ctx, w, "catalog-race", nil); err != nil {
		t.Fatal(err)
	}
	return w
}

func catalogDatabaseState(err error) string {
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		return state.SQLState()
	}
	return ""
}

func catalogAwaitError(t *testing.T, ctx context.Context, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		t.Fatal("catalog race exceeded its bound")
		return ctx.Err()
	}
}

func requireCatalogGuardWait(t *testing.T, f catalogFixture, waiter, blocker uint32) {
	t.Helper()
	waitAdapterBlockedBy(t, f.db.ctx, f.db.conn, waiter, blocker)
	var guarded, blobLock bool
	if err := f.db.conn.QueryRow(f.db.ctx, `SELECT
		EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND locktype = 'advisory'
			AND classid = 1129271888 AND objid = 2 AND NOT granted),
		EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1
			AND relation = 'bronze.blob'::regclass)`, waiter).Scan(&guarded, &blobLock); err != nil {
		t.Fatal(err)
	}
	if !guarded || blobLock {
		t.Fatal("competitor reached a blob before acquiring the advisory guard")
	}
}

func TestConversionCatalogBlobGuard(t *testing.T) {
	for _, guarded := range []bool{true, false} {
		t.Run(fmt.Sprintf("guarded-%t", guarded), func(t *testing.T) {
			f := newCatalogFixture(t)
			a, b := f.input.Manifest, f.input.Members[0]
			if a.SHA256 > b.SHA256 {
				a, b = b, a
			}
			connB, connC := boundedSourceAppConnection(t, f.db), boundedSourceAppConnection(t, f.db)
			workB := catalogOtherWork(t, f, connB, "b", b)
			workC := catalogOtherWork(t, f, connC, "c", a)
			var attemptsB, attemptsC int
			bProbe := catalogProbeStarter{TransactionStarter: connB, begin: func() { attemptsB++ }}
			bCatalog, err := NewConversionCatalog(bProbe, "writer-a", f.objects, formats.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			probe := catalogProbeStarter{TransactionStarter: connC, skipGuard: !guarded, begin: func() { attemptsC++ }}
			bLocked := make(chan struct{})
			if !guarded {
				probe.after = func(_ context.Context, query string, args []any, err error) error {
					if strings.Contains(query, "-- name: LockCatalogBlob") && len(args) > 0 && args[0] == b.SHA256 && err == nil {
						close(bLocked)
					}
					return nil
				}
			}
			cCatalog, err := NewConversionCatalog(probe, "writer-a", f.objects, formats.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			entered, resume := make(chan struct{}), make(chan struct{})
			prepareB, bReady := make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(resume) })
			allowB := sync.OnceFunc(func() { close(prepareB) })
			t.Cleanup(release)
			t.Cleanup(allowB)
			sourceDone, bDone, cDone := make(chan error, 1), make(chan error, 1), make(chan error, 1)
			go func() {
				_, err := ledgerTransaction(f.db.ctx, f.ledger, func(tx pgx.Tx, q intakedb.Querier) (bool, error) {
					if err := f.ledger.lockAdmission(f.db.ctx, tx, f.doc.Execution); err != nil {
						return false, err
					}
					if _, err := lockLease(f.db.ctx, tx, f.doc.Execution); err != nil {
						return false, err
					}
					_, owner, err := resolveExecution(f.db.ctx, tx, f.doc)
					if err != nil {
						return false, err
					}
					if guarded {
						if err := q.AcquireBlobCatalogGuard(f.db.ctx); err != nil {
							return false, err
						}
					}
					for _, m := range []blob.Metadata{a, b} {
						if _, err := q.LockCatalogBlob(f.db.ctx, m.SHA256); !errors.Is(err, pgx.ErrNoRows) {
							return false, fmt.Errorf("absent-row setup: %w", err)
						}
					}
					insert := func(m blob.Metadata) (int64, error) {
						return q.RegisterSourceBlob(f.db.ctx, intakedb.RegisterSourceBlobParams{Sha256: m.SHA256, ByteSize: m.ByteSize, MediaType: m.MediaType, ObjectRef: m.ObjectRef, CaptureSource: m.CaptureSource, CaptureRef: m.CaptureRef, CapturedAt: m.CapturedAt})
					}
					aID, err := insert(a)
					if err != nil {
						return false, err
					}
					close(entered)
					firstRelease := resume
					if !guarded {
						firstRelease = prepareB
					}
					select {
					case <-firstRelease:
					case <-f.db.ctx.Done():
						return false, f.db.ctx.Err()
					}
					bID, err := insert(b)
					if err != nil {
						return false, err
					}
					if _, err := q.AddBlobRetentionReference(f.db.ctx, intakedb.AddBlobRetentionReferenceParams{ID: aID, OwnerKind: "execution", OwnerKey: owner}); err != nil {
						return false, err
					}
					if !guarded {
						close(bReady)
						select {
						case <-resume:
						case <-f.db.ctx.Done():
							return false, f.db.ctx.Err()
						}
					}
					if _, err := q.AddBlobRetentionReference(f.db.ctx, intakedb.AddBlobRetentionReferenceParams{ID: bID, OwnerKind: "execution", OwnerKey: owner}); err != nil {
						return false, err
					}
					return true, nil
				})
				sourceDone <- err
			}()
			awaitAdapterSignal(t, f.db.ctx, entered)
			go func() {
				_, err := bCatalog.RegisterConversionOutputs(f.db.ctx, workB, ConversionRegistration{Manifest: b})
				bDone <- err
			}()
			if guarded {
				requireCatalogGuardWait(t, f, connB.PgConn().PID(), f.app.PgConn().PID())
			} else {
				if err := catalogAwaitError(t, f.db.ctx, bDone); err != nil {
					t.Fatal(err)
				}
				allowB()
				awaitAdapterSignal(t, f.db.ctx, bReady)
			}
			go func() {
				_, err := cCatalog.RegisterConversionOutputs(f.db.ctx, workC, ConversionRegistration{Manifest: a, Members: []blob.Metadata{b}, MemberRefs: []model.AssetRef{catalogRef(b)}})
				cDone <- err
			}()
			if guarded {
				requireCatalogGuardWait(t, f, connC.PgConn().PID(), f.app.PgConn().PID())
			} else {
				// B committed before C's discovery. C locks B and waits at
				// the A unique index while S still holds uncommitted A.
				awaitAdapterSignal(t, f.db.ctx, bLocked)
				waitAdapterBlockedBy(t, f.db.ctx, f.db.conn, connC.PgConn().PID(), f.app.PgConn().PID())
				var indexWait bool
				if err := f.db.conn.QueryRow(f.db.ctx, "SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND locktype = 'transactionid' AND NOT granted)", connC.PgConn().PID()).Scan(&indexWait); err != nil || !indexWait {
					t.Fatalf("C did not establish its unique-index wait on A: %v", err)
				}
			}
			release()
			sErr, cErr := catalogAwaitError(t, f.db.ctx, sourceDone), catalogAwaitError(t, f.db.ctx, cDone)
			if guarded {
				if bErr := catalogAwaitError(t, f.db.ctx, bDone); bErr != nil || sErr != nil || cErr != nil {
					t.Fatalf("guarded writers failed: source=%v B=%v C=%v", sErr, bErr, cErr)
				}
				if attemptsB != 1 || attemptsC != 1 || f.count(t, "SELECT count(*) FROM bronze.blob") != 2 || f.count(t, "SELECT count(*) FROM bronze.blob_retention_reference") != 5 {
					t.Fatalf("guarded writers retried or lost expected rows and edges: B attempts=%d C attempts=%d", attemptsB, attemptsC)
				}
				t.Log("guarded catalog contention retries: 0")
			} else {
				deadlocks := 0
				for _, err := range []error{sErr, cErr} {
					if catalogDatabaseState(err) == "40P01" {
						deadlocks++
					} else if err != nil {
						t.Fatalf("control failed for an unrelated cause: %v", err)
					}
				}
				if deadlocks != 1 || attemptsB != 1 || attemptsC != 1 {
					t.Fatalf("no-guard control needs exactly one 40P01 without retry: deadlocks=%d B attempts=%d C attempts=%d", deadlocks, attemptsB, attemptsC)
				}
			}
		})
	}
}

func TestConversionCatalogConflictWinnerRetry(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		t.Run(fmt.Sprintf("exhausted-%t", exhausted), func(t *testing.T) {
			f := newCatalogFixture(t)
			other := boundedSourceAppConnection(t, f.db)
			var attempts, misses int
			var once sync.Once
			probe := catalogProbeStarter{TransactionStarter: f.app, begin: func() { attempts++ }}
			if exhausted {
				high := f.input.Manifest.SHA256
				if f.input.Members[0].SHA256 > high {
					high = f.input.Members[0].SHA256
				}
				probe.before = func(_ context.Context, query string, args []any) error {
					if strings.Contains(query, "-- name: InsertCatalogBlob") && args[0] == high {
						misses++
						return pgx.ErrNoRows
					}
					return nil
				}
			} else {
				probe.after = func(ctx context.Context, query string, args []any, err error) error {
					if strings.Contains(query, "-- name: LockCatalogBlob") && errors.Is(err, pgx.ErrNoRows) && args[0] == f.input.Manifest.SHA256 {
						var winnerErr error
						once.Do(func() {
							misses++
							m := f.input.Manifest
							_, winnerErr = intakedb.New(other).InsertCatalogBlob(ctx, intakedb.InsertCatalogBlobParams{Sha256: m.SHA256, ByteSize: m.ByteSize, MediaType: m.MediaType, ObjectRef: m.ObjectRef, CaptureSource: m.CaptureSource, CaptureRef: m.CaptureRef, CapturedAt: m.CapturedAt})
						})
						return winnerErr
					}
					return nil
				}
			}
			c, err := NewConversionCatalog(probe, "writer-a", f.objects, formats.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			before := catalogSnapshot(t, f.sourcesAdapterFixture)
			_, err = c.RegisterConversionOutputs(f.db.ctx, f.work, f.input)
			if exhausted {
				if !errors.Is(err, ErrCatalogContention) || attempts != 3 || misses != 3 {
					t.Fatalf("contention bound was not enforced: attempts=%d misses=%d err=%v", attempts, misses, err)
				}
				requireCatalogSnapshot(t, f.sourcesAdapterFixture, before)
			} else if err != nil || attempts != 2 || misses != 1 || f.count(t, "SELECT count(*) FROM bronze.blob") != 2 {
				t.Fatalf("winner was not rediscovered: attempts=%d misses=%d err=%v", attempts, misses, err)
			}
			t.Logf("catalog attempts=%d contention retries=%d", attempts, attempts-1)
		})
	}
}

type catalogBlobVerifier struct {
	hash    string
	entered chan struct{}
	resume  chan struct{}
}

func (v catalogBlobVerifier) VerifyAcceptance(ctx context.Context, tx pgx.Tx, _ run.IntakeApplyRequest, _ json.RawMessage) ([]model.GapRef, error) {
	if _, err := intakedb.New(tx).LockCatalogBlob(ctx, v.hash); err != nil {
		return nil, err
	}
	close(v.entered)
	select {
	case <-v.resume:
		return []model.GapRef{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestConversionCatalogCaptureAcceptanceAndOppositeOrder(t *testing.T) {
	f := newCatalogFixture(t)
	// Existing Sources fixtures retain these seed bytes before the new capture.
	f.objects.data[f.capture.Asset.Path] = []byte("synthetic source bytes")
	s, err := NewSources(f.app, "writer-a", f.objects, fixtureSourceContinuer{})
	if err != nil {
		t.Fatal(err)
	}
	f.sources = s
	inventory, requests := f.requests(t, 1)
	claim, err := s.ClaimRequest(f.db.ctx, sourceWork(f.doc, "race-claim", "artifact"))
	if err != nil || claim.RequestKey != requests[0].Plan.RequestKey {
		t.Fatalf("capture race target was not claimed: %v", err)
	}
	low, high := f.input.Manifest, f.input.Members[0]
	if low.SHA256 > high.SHA256 {
		low, high = high, low
	}
	// The low hash exists, the high hash remains absent. Catalogs receive
	// opposite root/member order, sharing both hashes with the real capture.
	if _, err := f.catalog.RegisterConversionOutputs(f.db.ctx, catalogWork(f.doc, low), ConversionRegistration{Manifest: low}); err != nil {
		t.Fatal(err)
	}
	capture := f.capture
	capture.CaptureKey = fmt.Sprintf("capture-%032x", 5000)
	capture.Asset, capture.TransportReceipt = catalogRef(high), catalogRef(low)
	now := f.frozen.Request.Cutoff
	if _, err := s.RecordTransport(f.db.ctx, sourceWork(f.doc, "transport", claim.RequestKey), SourceTransportReceipt{RequestKey: claim.RequestKey, AttemptKey: claim.AttemptKey, Attempt: claim.NextAttempt, Capture: &capture, Next: now, RetryAfter: now}); err != nil {
		t.Fatal(err)
	}
	artifact := inventory.Artifacts[0]
	artifact.Capture = &capture
	acceptance := CaptureAcceptance{RequestKey: claim.RequestKey, Capture: capture, Artifact: &artifact, CaptureURL: artifact.URL}
	connB, connC, connD := boundedSourceAppConnection(t, f.db), boundedSourceAppConnection(t, f.db), boundedSourceAppConnection(t, f.db)
	wB, wC, wD := catalogOtherWork(t, f, connB, "b", high), catalogOtherWork(t, f, connC, "c", low), catalogOtherWork(t, f, connD, "d", low)
	verifier := catalogBlobVerifier{low.SHA256, make(chan struct{}), make(chan struct{})}
	releaseAcceptance := sync.OnceFunc(func() { close(verifier.resume) })
	t.Cleanup(releaseAcceptance)
	ledgerD, err := NewLedger(connD, "writer-a", f.objects, verifier)
	if err != nil {
		t.Fatal(err)
	}
	wD.Substep = "synthetic-acceptance"
	requestD := fixtureAcceptance(catalogRequest(wD, low.SHA256, []model.AssetRef{catalogRef(low)}))
	dDone := make(chan error, 1)
	go func() {
		_, err := ledgerD.Apply(f.db.ctx, requestD)
		dDone <- err
	}()
	awaitAdapterSignal(t, f.db.ctx, verifier.entered)
	gate := sourceInsertGate{TransactionStarter: f.app, inserted: make(chan struct{}), resume: make(chan struct{}), query: "pg_advisory_xact_lock"}
	releaseSource := sync.OnceFunc(func() { close(gate.resume) })
	t.Cleanup(releaseSource)
	gated, err := NewSources(gate, "writer-a", f.objects)
	if err != nil {
		t.Fatal(err)
	}
	sDone := make(chan error, 1)
	go func() {
		_, err := gated.ApplyCapture(f.db.ctx, sourceWork(f.doc, "race-capture", claim.RequestKey), acceptance)
		sDone <- err
	}()
	awaitAdapterSignal(t, f.db.ctx, gate.inserted)
	var attemptsB, attemptsC int
	cB, err := NewConversionCatalog(catalogProbeStarter{TransactionStarter: connB, begin: func() { attemptsB++ }}, "writer-a", f.objects, formats.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	cC, err := NewConversionCatalog(catalogProbeStarter{TransactionStarter: connC, begin: func() { attemptsC++ }}, "writer-a", f.objects, formats.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	bDone, cDone := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := cB.RegisterConversionOutputs(f.db.ctx, wB, ConversionRegistration{Manifest: high, Members: []blob.Metadata{low}, MemberRefs: []model.AssetRef{catalogRef(low)}})
		bDone <- err
	}()
	go func() {
		_, err := cC.RegisterConversionOutputs(f.db.ctx, wC, ConversionRegistration{Manifest: low, Members: []blob.Metadata{high}, MemberRefs: []model.AssetRef{catalogRef(high)}})
		cDone <- err
	}()
	requireCatalogGuardWait(t, f, connB.PgConn().PID(), f.app.PgConn().PID())
	requireCatalogGuardWait(t, f, connC.PgConn().PID(), f.app.PgConn().PID())
	releaseSource()
	waitAdapterBlockedBy(t, f.db.ctx, f.db.conn, f.app.PgConn().PID(), connD.PgConn().PID())
	if f.count(t, "SELECT count(*) FROM bronze.blob WHERE sha256 = $1", high.SHA256) != 0 {
		t.Fatal("source inserted the new high hash before locking the existing low hash")
	}
	releaseAcceptance()
	for _, done := range []<-chan error{dDone, sDone, bDone, cDone} {
		if err := catalogAwaitError(t, f.db.ctx, done); err != nil {
			t.Fatal(err)
		}
	}
	if attemptsB != 1 || attemptsC != 1 || f.count(t, "SELECT count(*) FROM bronze.blob WHERE sha256 IN ($1, $2)", low.SHA256, high.SHA256) != 2 {
		t.Fatal("opposite-order writers retried or lost a shared blob")
	}
	t.Log("capture/catalog/acceptance race: zero contention retries")
}

func TestConversionCatalogRollbackFailureStopsRediscovery(t *testing.T) {
	f := newCatalogFixture(t)
	cleanupFailure := errors.New("synthetic rollback failure")
	var attempts int
	probe := catalogProbeStarter{TransactionStarter: f.app, rollbackError: cleanupFailure, begin: func() { attempts++ },
		before: func(_ context.Context, query string, _ []any) error {
			if strings.Contains(query, "-- name: InsertCatalogBlob") {
				return pgx.ErrNoRows
			}
			return nil
		}}
	c, err := NewConversionCatalog(probe, "writer-a", f.objects, formats.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	before := catalogSnapshot(t, f.sourcesAdapterFixture)
	if _, err := c.RegisterConversionOutputs(f.db.ctx, f.work, f.input); !errors.Is(err, cleanupFailure) || attempts != 1 {
		t.Fatalf("catalog hid rollback failure behind a retry: attempts=%d err=%v", attempts, err)
	}
	requireCatalogSnapshot(t, f.sourcesAdapterFixture, before)
}

func TestConversionCatalogStaleGrantPrecedesReplayConflictAfterWait(t *testing.T) {
	f := newCatalogFixture(t)
	if _, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input); err != nil {
		t.Fatal(err)
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	t.Cleanup(release)
	var once sync.Once
	probe := catalogProbeStarter{TransactionStarter: f.app,
		after: func(ctx context.Context, query string, _ []any, err error) error {
			if strings.Contains(query, "-- name: FindExecutionResult") && err == nil {
				once.Do(func() { close(entered) })
				select {
				case <-resume:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}}
	c, err := NewConversionCatalog(probe, "writer-a", f.objects, formats.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.conn.Exec(f.db.ctx, "UPDATE ingest.execution_lease SET expires_at = clock_timestamp() + interval '2 seconds' WHERE resource_key = 'version-a'"); err != nil {
		t.Fatal(err)
	}
	changed := f.input
	changed.Manifest.CapturedAt = changed.Manifest.CapturedAt.Add(time.Second)
	before := catalogSnapshot(t, f.sourcesAdapterFixture)
	opened := len(f.objects.opened)
	done := make(chan error, 1)
	go func() {
		_, err := c.RegisterConversionOutputs(f.db.ctx, f.work, changed)
		done <- err
	}()
	awaitAdapterSignal(t, f.db.ctx, entered)
	for {
		var expired bool
		if err := f.db.conn.QueryRow(f.db.ctx, "SELECT expires_at <= clock_timestamp() FROM ingest.execution_lease WHERE resource_key = 'version-a'").Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-f.db.ctx.Done():
			t.Fatal(f.db.ctx.Err())
		}
	}
	release()
	if err := catalogAwaitError(t, f.db.ctx, done); !errors.Is(err, run.ErrStaleGrant) || len(f.objects.opened) != opened {
		t.Fatalf("changed replay won over an expired grant or read bytes: %v", err)
	}
	requireCatalogSnapshot(t, f.sourcesAdapterFixture, before)
}

func TestConversionCatalogConcurrentIdenticalReplay(t *testing.T) {
	f := newCatalogFixture(t)
	other, observer := boundedSourceAppConnection(t, f.db), boundedSourceAppConnection(t, f.db)
	c, err := NewConversionCatalog(other, "writer-a", f.objects, formats.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	t.Cleanup(release)
	var once sync.Once
	f.objects.beforeOpen = func(ctx context.Context, _ string) error {
		once.Do(func() { close(entered) })
		select {
		case <-resume:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	type response struct {
		receipt run.IntakeReceipt
		err     error
	}
	done := make(chan response, 2)
	go func() {
		receipt, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input)
		done <- response{receipt, err}
	}()
	awaitAdapterSignal(t, f.db.ctx, entered)
	go func() {
		receipt, err := c.RegisterConversionOutputs(f.db.ctx, f.work, f.input)
		done <- response{receipt, err}
	}()
	waitAdapterBlockedBy(t, f.db.ctx, observer, other.PgConn().PID(), f.app.PgConn().PID())
	release()
	var first run.IntakeReceipt
	for i := 0; i < 2; i++ {
		select {
		case result := <-done:
			if result.err != nil {
				t.Fatal(result.err)
			}
			if i == 0 {
				first = result.receipt
			} else if !run.SameImmutableIntakePayload(first.Result, result.receipt.Result) || first.ResultID != result.receipt.ResultID || first.ExecutionID != result.receipt.ExecutionID || first.RunReceipt != result.receipt.RunReceipt {
				t.Fatal("concurrent identical registration returned different provenance")
			}
		case <-f.db.ctx.Done():
			t.Fatal(f.db.ctx.Err())
		}
	}
	if f.count(t, "SELECT count(*) FROM ingest.execution_result WHERE idempotency_key = $1", run.IntakeIdempotencyKey(f.work)) != 1 ||
		f.count(t, "SELECT count(*) FROM bronze.blob") != 2 || f.count(t, "SELECT count(*) FROM bronze.blob_retention_reference") != 2 {
		t.Fatal("concurrent replay duplicated results, catalog rows or edges")
	}
}

func TestConversionCatalogUnexpectedResultWinnerRollsBack(t *testing.T) {
	f := newCatalogFixture(t)
	other := boundedSourceAppConnection(t, f.db)
	var attempts int
	var once sync.Once
	var winnerSnapshot map[string]string
	probe := catalogProbeStarter{TransactionStarter: f.app, begin: func() { attempts++ },
		before: func(ctx context.Context, query string, args []any) error {
			if !strings.Contains(query, "-- name: InsertExecutionResult") {
				return nil
			}
			var insertErr error
			once.Do(func() {
				// An unguarded stand-in commits a result without a catalog
				// commit set. The adapter must roll back its own writes and
				// reject the damaged replay rather than repair that winner.
				values := append([]any(nil), args[6:]...)
				values[1], insertErr = newLedgerKey("result")
				if insertErr != nil {
					return
				}
				_, insertErr = other.Exec(ctx, `INSERT INTO ingest.execution_result
					(execution_id, result_key, stage, substep, shard, idempotency_key,
					input_digest, output_digest, recipe_digest, budget_digest,
					decision_revision, outcome, expected, processed, succeeded,
					pending_retry, terminal_gap, rejected, failed, accepted_gaps,
					cursor, next_eligible_at, gap_acceptance_ref, complete, payload)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,
						$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)`, values...)
				if insertErr == nil {
					winnerSnapshot = catalogSnapshot(t, f.sourcesAdapterFixture)
				}
			})
			return insertErr
		}}
	c, err := NewConversionCatalog(probe, "writer-a", f.objects, formats.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RegisterConversionOutputs(f.db.ctx, f.work, f.input); !errors.Is(err, ErrCatalogIntegrity) || attempts != 2 {
		t.Fatalf("unexpected result winner did not roll back into replay: attempts=%d err=%v", attempts, err)
	}
	if winnerSnapshot == nil {
		t.Fatal("result winner was not established")
	}
	requireCatalogSnapshot(t, f.sourcesAdapterFixture, winnerSnapshot)
}
