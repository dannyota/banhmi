package intake

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/blob"
	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/quality/formats"
	"danny.vn/banhmi/internal/corpus/run"
)

type catalogObjects struct {
	fixtureObjectStore
	mu         sync.Mutex
	data       map[string][]byte
	opened     []string
	beforeOpen func(context.Context, string) error
}

func (s *catalogObjects) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	s.mu.Lock()
	s.opened = append(s.opened, path)
	content, exists := s.data[path]
	s.mu.Unlock()
	if s.beforeOpen != nil {
		if err := s.beforeOpen(ctx, path); err != nil {
			return nil, err
		}
	}
	if !exists {
		return nil, errors.New("synthetic object absent")
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

func catalogMetadata(objects *catalogObjects, path, content string) blob.Metadata {
	raw := []byte(content)
	sum := sha256.Sum256(raw)
	m := blob.Metadata{SHA256: hex.EncodeToString(sum[:]), ByteSize: int64(len(raw)),
		MediaType: "text/plain", ObjectRef: path, CaptureSource: "conversion",
		CaptureRef: "producer-a", CapturedAt: time.Unix(1000, 0).UTC()}
	objects.data[path] = raw
	return m
}

func catalogRef(m blob.Metadata) model.AssetRef {
	return model.AssetRef{Path: m.ObjectRef, SHA256: model.Digest(m.SHA256), ByteSize: m.ByteSize}
}

type catalogFixture struct {
	*sourcesAdapterFixture
	objects *catalogObjects
	catalog *ConversionCatalog
	work    run.IntakeWork
	input   ConversionRegistration
}

func newCatalogFixture(t *testing.T) catalogFixture {
	t.Helper()
	f := newSourcesAdapterFixture(t)
	objects := &catalogObjects{data: map[string][]byte{}}
	root := catalogMetadata(objects, "logical/root", "synthetic root")
	member := catalogMetadata(objects, "logical/member", "synthetic member")
	c, err := NewConversionCatalog(f.app, "writer-a", objects, formats.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	w := catalogWork(f.doc, root)
	return catalogFixture{f, objects, c, w, ConversionRegistration{Manifest: root, Members: []blob.Metadata{member}, MemberRefs: []model.AssetRef{catalogRef(member)}}}
}

func catalogWork(base run.IntakeWork, root blob.Metadata) run.IntakeWork {
	base.Stage, base.Substep, base.Shard = run.Normalize, "conversion-catalog", root.SHA256
	return base
}

func catalogSnapshot(t *testing.T, f *sourcesAdapterFixture) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	for _, table := range []string{"bronze.blob", "bronze.blob_retention_reference", "bronze.observation_revision", "ingest.execution_result", "ingest.artifact_request", "ingest.source_window", "ingest.source_rate_limit", "ingest.delivery_record", "ingest.revision_acceptance"} {
		var raw json.RawMessage
		if err := f.db.conn.QueryRow(f.db.ctx, "SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.id), '[]'::jsonb) FROM "+table+" r").Scan(&raw); err != nil {
			t.Fatal(err)
		}
		snapshot[table] = string(raw)
	}
	return snapshot
}

func requireCatalogSnapshot(t *testing.T, f *sourcesAdapterFixture, before map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(before, catalogSnapshot(t, f)) {
		t.Fatal("rejected invocation changed catalog, protection, request or result rows")
	}
}

func TestConversionCatalogRegistrationReplay(t *testing.T) {
	f := newCatalogFixture(t)
	member := f.input.Members[0]
	// A previous role wins the catalog path. Registration retains aliases in
	// its hashed inputs but receipts and reads use that earlier physical path.
	if _, err := f.catalog.RegisterConversionOutputs(f.db.ctx, catalogWork(f.doc, member), ConversionRegistration{Manifest: member}); err != nil {
		t.Fatal(err)
	}
	alias := catalogRef(member)
	alias.Path = "different/logical/path"
	stagedAlias := member
	stagedAlias.ObjectRef = alias.Path
	stagedAlias.CapturedAt = member.CapturedAt.Add(time.Second)
	f.input.Members = []blob.Metadata{stagedAlias}
	f.input.MemberRefs = append(f.input.MemberRefs, alias)
	first, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Validate(); err != nil || first.Result.Work.Stage != run.Normalize || first.Result.OutputDigest != model.Digest(f.input.Manifest.SHA256) || first.Result.Complete {
		t.Fatalf("catalog receipt lost its nested result: %v", err)
	}
	if len(first.Result.Outputs) != 2 || first.RunReceipt != "synthetic-run" {
		t.Fatal("catalog receipt lost physical outputs or original transport")
	}
	for _, path := range f.objects.opened {
		if path == alias.Path {
			t.Fatal("catalog opened a logical alias instead of the winning path")
		}
	}
	before := catalogSnapshot(t, f.sourcesAdapterFixture)
	again, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatalf("lost response changed original receipt: %v", err)
	}
	requireCatalogSnapshot(t, f.sourcesAdapterFixture, before)
	if _, err := f.db.conn.Exec(f.db.ctx, "UPDATE ingest.execution_lease SET cancelled_at = clock_timestamp() WHERE resource_key = 'version-a'"); err != nil {
		t.Fatal(err)
	}
	reclaimed := f.work
	reclaimed.Execution.Owner = "owner-b"
	lease, err := f.ledger.ReclaimLease(f.db.ctx, 1, reclaimed.Execution.Scope, reclaimed.Execution.Owner, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	reclaimed.Execution.AttemptFence = lease.AttemptFence
	if _, err := f.ledger.RegisterExecution(f.db.ctx, reclaimed, "new-transport", nil); err != nil {
		t.Fatal(err)
	}
	again, err = f.catalog.RegisterConversionOutputs(f.db.ctx, reclaimed, f.input)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatalf("reclaimed replay transferred execution ownership: %v", err)
	}
	requireCatalogSnapshot(t, f.sourcesAdapterFixture, before)
	if f.count(t, "SELECT count(*) FROM bronze.blob_retention_reference WHERE owner_key <> $1", first.ExecutionID) != 0 {
		t.Fatal("replay transferred protection")
	}
}

func TestConversionCatalogRejectionsAreAtomic(t *testing.T) {
	for _, name := range []string{"owner", "fence", "epoch", "writer", "cancelled", "expired", "context", "corrupt", "wrong-size", "missing-dependency", "outside-members", "self", "metadata-conflict", "path-conflict", "blob-bound", "row-bound", "json-bound", "object-bound", "work-items", "work-bytes", "wrong-stage", "decision"} {
		t.Run(name, func(t *testing.T) {
			f := newCatalogFixture(t)
			ctx := f.db.ctx
			want := error(ErrCatalogInvalid)
			switch name {
			case "owner":
				f.work.Execution.Owner = "wrong-owner"
				want = run.ErrStaleGrant
			case "fence":
				f.work.Execution.AttemptFence++
				want = run.ErrStaleGrant
			case "epoch":
				f.work.Execution.AdmissionEpoch++
				want = run.ErrStaleGrant
			case "writer":
				f.catalog.ledger.writer = "wrong-writer"
				want = run.ErrStaleGrant
			case "cancelled", "expired":
				statement := "UPDATE ingest.execution_lease SET cancelled_at = clock_timestamp() WHERE resource_key = 'version-a'"
				if name == "expired" {
					statement = "UPDATE ingest.execution_lease SET expires_at = clock_timestamp() WHERE resource_key = 'version-a'"
				}
				if _, err := f.db.conn.Exec(ctx, statement); err != nil {
					t.Fatal(err)
				}
				want = run.ErrStaleGrant
			case "context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			case "corrupt":
				f.objects.data[f.input.Manifest.ObjectRef] = []byte("corruption")
				want = ErrCatalogIntegrity
			case "wrong-size":
				f.input.Manifest.ByteSize++
				want = ErrCatalogIntegrity
			case "missing-dependency":
				f.input.Members = nil
				want = ErrCatalogIntegrity
			case "outside-members":
				f.input.MemberRefs = nil
			case "self":
				f.input.MemberRefs = append(f.input.MemberRefs, catalogRef(f.input.Manifest))
			case "metadata-conflict":
				conflict := f.input.Members[0]
				conflict.CapturedAt = conflict.CapturedAt.Add(time.Second)
				f.input.Members = append(f.input.Members, conflict)
			case "path-conflict":
				f.input.MemberRefs[0].Path = f.input.Manifest.ObjectRef
			case "blob-bound":
				f.catalog.limits.MaxBlobs = 1
			case "row-bound":
				f.catalog.limits.MaxRows = 1
			case "json-bound":
				f.catalog.limits.MaxJSONBytes = 100
			case "object-bound":
				f.catalog.limits.MaxObjectBytes = 1
			case "work-items":
				f.work.Budget.MaxItems = 1
			case "work-bytes":
				f.work.Budget.MaxInputBytes = 1
			case "wrong-stage":
				f.work.Stage = run.Extract
			case "decision":
				f.work.DecisionRevision = 1
			}
			before := catalogSnapshot(t, f.sourcesAdapterFixture)
			if _, err := f.catalog.RegisterConversionOutputs(ctx, f.work, f.input); !errors.Is(err, want) {
				t.Fatalf("rejection class: got %v, want %v", err, want)
			}
			requireCatalogSnapshot(t, f.sourcesAdapterFixture, before)
		})
	}
}

func TestConversionCatalogChangedReplayPrecedesObjectIO(t *testing.T) {
	for _, field := range []string{"timestamp", "member-set", "reference-path", "metadata-path"} {
		t.Run(field, func(t *testing.T) {
			f := newCatalogFixture(t)
			if _, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input); err != nil {
				t.Fatal(err)
			}
			before := catalogSnapshot(t, f.sourcesAdapterFixture)
			opened := len(f.objects.opened)
			switch field {
			case "timestamp":
				f.input.Manifest.CapturedAt = f.input.Manifest.CapturedAt.Add(time.Second)
			case "member-set":
				f.input.Members = nil
			case "reference-path":
				f.input.MemberRefs[0].Path = "changed/logical"
			case "metadata-path":
				f.input.Manifest.ObjectRef = "changed/root"
			}
			if _, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input); !errors.Is(err, run.ErrPayloadConflict) {
				t.Fatalf("changed replay did not conflict: %v", err)
			}
			if len(f.objects.opened) != opened {
				t.Fatal("changed replay opened object bytes")
			}
			requireCatalogSnapshot(t, f.sourcesAdapterFixture, before)
		})
	}
}

func TestConversionCatalogReplayNeverRepairsProtection(t *testing.T) {
	f := newCatalogFixture(t)
	if _, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input); err != nil {
		t.Fatal(err)
	}
	// Corrupt only the isolated fixture using its migrator connection.
	if _, err := f.db.conn.Exec(f.db.ctx, "ALTER TABLE bronze.blob_retention_reference DISABLE TRIGGER USER"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.conn.Exec(f.db.ctx, "DELETE FROM bronze.blob_retention_reference"); err != nil {
		t.Fatal(err)
	}
	before := catalogSnapshot(t, f.sourcesAdapterFixture)
	if _, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input); !errors.Is(err, ErrCatalogIntegrity) {
		t.Fatalf("missing replay protection was repaired: %v", err)
	}
	requireCatalogSnapshot(t, f.sourcesAdapterFixture, before)
}

func TestConversionCatalogCancellationAndExpiryAfterWaits(t *testing.T) {
	for _, wait := range []string{"guard", "blob", "object"} {
		for _, failure := range []string{"cancel", "expiry"} {
			t.Run(wait+"/"+failure, func(t *testing.T) {
				f := newCatalogFixture(t)
				ctx, cancel := context.WithCancel(f.db.ctx)
				defer cancel()
				observer := boundedSourceAppConnection(t, f.db)
				blocker := boundedSourceAppConnection(t, f.db)
				entered, resume := make(chan struct{}), make(chan struct{})
				release := sync.OnceFunc(func() { close(resume) })
				t.Cleanup(release)
				var held pgx.Tx
				if wait == "object" {
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
				} else {
					if wait == "blob" {
						if _, err := f.catalog.RegisterConversionOutputs(f.db.ctx, catalogWork(f.doc, f.input.Manifest), ConversionRegistration{Manifest: f.input.Manifest}); err != nil {
							t.Fatal(err)
						}
						// Use another root so the blocked call remains fresh.
						newRoot := catalogMetadata(f.objects, "logical/other", "other root")
						f.input = ConversionRegistration{Manifest: newRoot, MemberRefs: []model.AssetRef{catalogRef(f.input.Manifest)}}
						f.work = catalogWork(f.doc, newRoot)
					}
					var err error
					held, err = blocker.Begin(f.db.ctx)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { sourceCleanup(t, f.db.ctx, held.Rollback) })
					statement := "SELECT pg_advisory_xact_lock(1129271888, 2)"
					var args []any
					if wait == "blob" {
						statement, args = "SELECT id FROM bronze.blob WHERE sha256 = $1 FOR UPDATE", []any{string(f.input.MemberRefs[0].SHA256)}
					}
					if _, err := held.Exec(f.db.ctx, statement, args...); err != nil {
						t.Fatal(err)
					}
				}
				if failure == "expiry" {
					if _, err := f.db.conn.Exec(f.db.ctx, "UPDATE ingest.execution_lease SET expires_at = clock_timestamp() + interval '2 seconds' WHERE resource_key = 'version-a'"); err != nil {
						t.Fatal(err)
					}
				}
				before := catalogSnapshot(t, f.sourcesAdapterFixture)
				done := make(chan error, 1)
				go func() {
					_, err := f.catalog.RegisterConversionOutputs(ctx, f.work, f.input)
					done <- err
				}()
				if wait == "object" {
					awaitAdapterSignal(t, f.db.ctx, entered)
				} else {
					waitAdapterBlockedBy(t, f.db.ctx, observer, f.app.PgConn().PID(), blocker.PgConn().PID())
				}
				if failure == "cancel" {
					cancel()
				} else {
					// Database time supplies the boundary; no sleep guesses
					// whether the blocked lease actually expired.
					for {
						var expired bool
						if err := observer.QueryRow(f.db.ctx, "SELECT expires_at <= clock_timestamp() FROM ingest.execution_lease WHERE resource_key = 'version-a'").Scan(&expired); err != nil {
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
				}
				if held != nil {
					if err := held.Rollback(f.db.ctx); err != nil {
						t.Fatal(err)
					}
				}
				release()
				select {
				case err := <-done:
					want := error(context.Canceled)
					if failure == "expiry" {
						want = run.ErrStaleGrant
					}
					if !errors.Is(err, want) {
						t.Fatalf("wait lost rejection cause: %v", err)
					}
				case <-f.db.ctx.Done():
					t.Fatal("catalog wait did not terminate")
				}
				requireCatalogSnapshot(t, f.sourcesAdapterFixture, before)
			})
		}
	}
}

func TestConversionCatalogRegistrationOrderAndLogicalAliases(t *testing.T) {
	f := newCatalogFixture(t)
	member := f.input.Members[0]
	f.input.Members = append(f.input.Members, member)
	alias := catalogRef(member)
	alias.Path = "alias/member"
	f.input.MemberRefs = append(f.input.MemberRefs, alias)
	receipt, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(f.input.MemberRefs, func(i, j int) bool { return f.input.MemberRefs[i].Path > f.input.MemberRefs[j].Path })
	again, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input)
	if err != nil || !reflect.DeepEqual(receipt, again) {
		t.Fatalf("canonical input ordering changed replay: %v", err)
	}
	if f.count(t, "SELECT count(*) FROM bronze.blob") != 2 || f.count(t, "SELECT count(*) FROM bronze.blob_retention_reference") != 2 {
		t.Fatal("logical aliases duplicated physical blobs or edges")
	}
}

func TestConversionCatalogReceiptRejectsMalformedEnvelope(t *testing.T) {
	f := newCatalogFixture(t)
	receipt, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input)
	if err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	if err := f.db.conn.QueryRow(f.db.ctx, "SELECT payload FROM ingest.execution_result WHERE result_key = $1", receipt.ResultID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		strings.Replace(string(raw), "catalog-registration-v1", "unknown-version", 1),
		string(raw) + " {}",
		"{\"Unknown\":true," + string(raw)[1:],
		"{}",
	} {
		if _, err := decodeCatalogPayload([]byte(input)); !errors.Is(err, ErrCatalogIntegrity) {
			t.Fatal("invalid catalog envelope escaped integrity validation")
		}
	}
}

func TestConversionCatalogLimitsAndConstructor(t *testing.T) {
	if got := formats.DefaultLimits(); got != (formats.Limits{MaxBlobs: 1024, MaxRows: 10000, MaxJSONBytes: 4194304, MaxObjectBytes: 67108864, MaxElapsedMS: 60000}) {
		t.Fatal("catalog defaults differ from approved proposed bounds")
	}
	probe := &sourceConstructorProbe{}
	if _, err := NewConversionCatalog(probe, "writer-a", fixtureObjectStore{}, formats.DefaultLimits()); err != nil || probe.calls != 0 {
		t.Fatal("catalog constructor performed I/O")
	}
	if _, err := NewConversionCatalog(probe, "writer-a", fixtureObjectStore{}, formats.Limits{}); !errors.Is(err, ErrCatalogInvalid) {
		t.Fatal("unbounded catalog configuration accepted")
	}
	if _, err := NewConversionCatalog(probe, "writer-a", nil, formats.DefaultLimits()); err == nil {
		t.Fatal("catalog accepted missing object reader")
	}
}

func TestConversionCatalogCatalogBytesRemainAuthoritative(t *testing.T) {
	for _, corruption := range []string{"size", "bytes", "path-collision", "foreign-edge"} {
		t.Run(corruption, func(t *testing.T) {
			f := newCatalogFixture(t)
			receipt, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input)
			if err != nil {
				t.Fatal(err)
			}
			switch corruption {
			case "size", "path-collision":
				if _, err := f.db.conn.Exec(f.db.ctx, "ALTER TABLE bronze.blob DISABLE TRIGGER USER"); err != nil {
					t.Fatal(err)
				}
				statement := "UPDATE bronze.blob SET byte_size = byte_size + 1 WHERE sha256 = $1"
				if corruption == "path-collision" {
					statement = "UPDATE bronze.blob SET object_ref = (SELECT object_ref FROM bronze.blob WHERE sha256 <> $1 LIMIT 1) WHERE sha256 = $1"
				}
				if _, err := f.db.conn.Exec(f.db.ctx, statement, f.input.Manifest.SHA256); err != nil {
					t.Fatal(err)
				}
			case "bytes":
				f.objects.data[f.input.Manifest.ObjectRef] = []byte("changed retained bytes")
			case "foreign-edge":
				if _, err := f.db.conn.Exec(f.db.ctx, "ALTER TABLE bronze.blob_retention_reference DISABLE TRIGGER USER"); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.conn.Exec(f.db.ctx, "UPDATE bronze.blob_retention_reference SET owner_key = $1 WHERE owner_key = $2", "execution-00000000000000000000000000000000", receipt.ExecutionID); err != nil {
					t.Fatal(err)
				}
			}
			before := catalogSnapshot(t, f.sourcesAdapterFixture)
			if _, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input); !errors.Is(err, ErrCatalogIntegrity) {
				t.Fatalf("catalog corruption did not reject replay: %v", err)
			}
			requireCatalogSnapshot(t, f.sourcesAdapterFixture, before)
		})
	}
}

func TestConversionCatalogCancelledReadReleasesGuard(t *testing.T) {
	f := newCatalogFixture(t)
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	entered := make(chan struct{})
	f.catalog.ledger.objects = blockingObjectStore{reader: reader, entered: entered}
	ctx, cancel := context.WithCancel(f.db.ctx)
	defer cancel()
	before := catalogSnapshot(t, f.sourcesAdapterFixture)
	done := make(chan error, 1)
	go func() {
		_, err := f.catalog.RegisterConversionOutputs(ctx, f.work, f.input)
		done <- err
	}()
	awaitAdapterSignal(t, f.db.ctx, entered)
	cancel()
	if err := catalogAwaitError(t, f.db.ctx, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked Read did not preserve cancellation: %v", err)
	}
	requireCatalogSnapshot(t, f.sourcesAdapterFixture, before)
	other := boundedSourceAppConnection(t, f.db)
	tx, err := other.Begin(f.db.ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sourceCleanup(t, f.db.ctx, tx.Rollback) })
	var acquired bool
	if err := tx.QueryRow(f.db.ctx, "SELECT pg_try_advisory_xact_lock(1129271888, 2)").Scan(&acquired); err != nil || !acquired {
		t.Fatalf("cancelled read retained its guard: %v", err)
	}
}

func TestConversionCatalogTrustedElapsedBound(t *testing.T) {
	f := newCatalogFixture(t)
	f.catalog.limits.MaxElapsedMS = 20
	f.objects.beforeOpen = func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	before := catalogSnapshot(t, f.sourcesAdapterFixture)
	if _, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("trusted elapsed bound did not cancel work: %v", err)
	}
	requireCatalogSnapshot(t, f.sourcesAdapterFixture, before)
}
