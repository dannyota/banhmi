package intake

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/run"
)

func TestConversionCatalogReplayReturnsCheckedReceipt(t *testing.T) {
	f := newCatalogFixture(t)
	first, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input)
	if err != nil {
		t.Fatal(err)
	}
	opened := len(f.objects.opened)
	lookups := 0
	probe := catalogProbeStarter{TransactionStarter: f.app,
		before: func(_ context.Context, query string, _ []any) error {
			if strings.Contains(query, "-- name: FindExecutionResult") {
				lookups++
				if len(f.objects.opened) != opened {
					return errors.New("result lookup after object I/O")
				}
			}
			return nil
		}}
	c, err := NewConversionCatalog(probe, "writer-a", f.objects, f.catalog.limits)
	if err != nil {
		t.Fatal(err)
	}
	before := catalogReplaySnapshot(t, f)
	again, err := c.RegisterConversionOutputs(f.db.ctx, f.work, f.input)
	if err != nil || !reflect.DeepEqual(first, again) || lookups != 1 || len(f.objects.opened) != opened+2 {
		t.Fatalf("replay replaced its verified receipt or skipped object verification: lookups=%d err=%v", lookups, err)
	}
	requireCatalogReplaySnapshot(t, f, before)
}

func catalogReplaySnapshot(t *testing.T, f catalogFixture) map[string]string {
	t.Helper()
	snapshot := catalogSnapshot(t, f.sourcesAdapterFixture)
	for _, table := range []string{"ingest.execution", "ingest.execution_lease", "ingest.country_admission"} {
		var raw json.RawMessage
		if err := f.db.conn.QueryRow(f.db.ctx, "SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.id), '[]'::jsonb) FROM "+table+" r").Scan(&raw); err != nil {
			t.Fatal(err)
		}
		snapshot[table] = string(raw)
	}
	return snapshot
}

func requireCatalogReplaySnapshot(t *testing.T, f catalogFixture, before map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(before, catalogReplaySnapshot(t, f)) {
		t.Fatal("replay changed catalog, execution, grant or source rows")
	}
}

func catalogReclaimWork(t *testing.T, f catalogFixture, work run.IntakeWork, owner string) run.IntakeWork {
	t.Helper()
	if _, err := f.db.conn.Exec(f.db.ctx, "UPDATE ingest.execution_lease SET cancelled_at = clock_timestamp() WHERE resource_key = 'version-a'"); err != nil {
		t.Fatal(err)
	}
	work.Execution.Owner = owner
	lease, err := f.ledger.ReclaimLease(f.db.ctx, 1, work.Execution.Scope, owner, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	work.Execution.AttemptFence = lease.AttemptFence
	if _, err := f.ledger.RegisterExecution(f.db.ctx, work, "replacement-transport", nil); err != nil {
		t.Fatal(err)
	}
	return work
}

func TestConversionCatalogReclaimedReplayAtExactJSONLimit(t *testing.T) {
	for _, change := range []string{"longer-owner", "fence-digit"} {
		t.Run(change, func(t *testing.T) {
			f := newCatalogFixture(t)
			if change == "fence-digit" {
				for f.work.Execution.AttemptFence < 9 {
					f.work = catalogReclaimWork(t, f, f.work, f.work.Execution.Owner)
				}
			}
			registration, hashes, _, metadata, err := normalizeCatalogRegistration(f.work, f.input, f.catalog.limits)
			if err != nil {
				t.Fatal(err)
			}
			request := catalogRequest(f.work, registration.Manifest.SHA256, nil)
			for _, hash := range hashes {
				request.Outputs = append(request.Outputs, catalogRef(metadata[hash]))
			}
			compact, err := json.Marshal(catalogRegistrationPayload{"catalog-registration-v1", request, registration})
			if err != nil {
				t.Fatal(err)
			}
			f.catalog.limits.MaxJSONBytes = int64(len(compact))
			first, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input)
			if err != nil {
				t.Fatal(err)
			}
			owner := f.work.Execution.Owner
			if change == "longer-owner" {
				owner = "replacement-owner-with-a-longer-name"
			}
			reclaimed := catalogReclaimWork(t, f, f.work, owner)
			if change == "fence-digit" && reclaimed.Execution.AttemptFence != 10 {
				t.Fatal("fixture did not cross the fence digit boundary")
			}
			before := catalogReplaySnapshot(t, f)
			again, err := f.catalog.RegisterConversionOutputs(f.db.ctx, reclaimed, f.input)
			if err != nil || !reflect.DeepEqual(first, again) {
				t.Fatalf("replacement grant changed exact-limit replay: %v", err)
			}
			requireCatalogReplaySnapshot(t, f, before)
		})
	}
}

func TestConversionCatalogWrongResultExecutionPrecedesObjectIO(t *testing.T) {
	f := newCatalogFixture(t)
	first, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input)
	if err != nil {
		t.Fatal(err)
	}
	other := catalogOtherWork(t, f, f.app, "other", f.input.Manifest)
	second, err := f.catalog.RegisterConversionOutputs(f.db.ctx, other, f.input)
	if err != nil {
		t.Fatal(err)
	}
	if first.ExecutionID == second.ExecutionID || f.count(t, "SELECT count(*) FROM bronze.blob_retention_reference WHERE owner_key = $1", second.ExecutionID) != 2 {
		t.Fatal("fixture did not establish another execution with its own edges")
	}
	if _, err := f.db.conn.Exec(f.db.ctx, "ALTER TABLE ingest.execution_result DISABLE TRIGGER USER"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.conn.Exec(f.db.ctx, "UPDATE ingest.execution_result SET execution_id = (SELECT id FROM ingest.execution WHERE execution_key = $1) WHERE result_key = $2", second.ExecutionID, first.ResultID); err != nil {
		t.Fatal(err)
	}
	before := catalogReplaySnapshot(t, f)
	opened := len(f.objects.opened)
	if _, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input); !errors.Is(err, ErrCatalogIntegrity) {
		t.Fatalf("wrong result execution was accepted: %v", err)
	}
	if len(f.objects.opened) != opened {
		t.Fatal("wrong result execution opened objects")
	}
	requireCatalogReplaySnapshot(t, f, before)
}

func TestConversionCatalogStoredPathMismatchPrecedesObjectIO(t *testing.T) {
	f := newCatalogFixture(t)
	first, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input)
	if err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	if err := f.db.conn.QueryRow(f.db.ctx, "SELECT payload FROM ingest.execution_result WHERE result_key = $1", first.ResultID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	p, err := decodeCatalogPayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	p.Request.Outputs[0].Path = "tampered/path"
	changed, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	// Preserve a valid compact envelope so only the catalog path binding fails.
	if _, err := decodeCatalogPayload(changed); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.conn.Exec(f.db.ctx, "ALTER TABLE ingest.execution_result DISABLE TRIGGER USER"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.conn.Exec(f.db.ctx, "UPDATE ingest.execution_result SET payload = $1 WHERE result_key = $2", changed, first.ResultID); err != nil {
		t.Fatal(err)
	}
	before := catalogReplaySnapshot(t, f)
	opened := len(f.objects.opened)
	if _, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input); !errors.Is(err, ErrCatalogIntegrity) {
		t.Fatalf("stored output path mismatch was accepted: %v", err)
	}
	if len(f.objects.opened) != opened {
		t.Fatal("stored output path mismatch opened objects")
	}
	requireCatalogReplaySnapshot(t, f, before)
}

func TestConversionCatalogDamagedProtectionPrecedesObjectIO(t *testing.T) {
	for _, damage := range []string{"missing", "foreign-owner", "foreign-kind"} {
		t.Run(damage, func(t *testing.T) {
			f := newCatalogFixture(t)
			first, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.conn.Exec(f.db.ctx, "ALTER TABLE bronze.blob_retention_reference DISABLE TRIGGER USER"); err != nil {
				t.Fatal(err)
			}
			statement := "DELETE FROM bronze.blob_retention_reference WHERE owner_key = $1"
			switch damage {
			case "foreign-owner":
				statement = "UPDATE bronze.blob_retention_reference SET owner_key = 'execution-00000000000000000000000000000000' WHERE owner_key = $1"
			case "foreign-kind":
				statement = "UPDATE bronze.blob_retention_reference SET owner_kind = 'release', owner_key = 'other-release' WHERE owner_key = $1"
			}
			if _, err := f.db.conn.Exec(f.db.ctx, statement, first.ExecutionID); err != nil {
				t.Fatal(err)
			}
			before := catalogReplaySnapshot(t, f)
			opened := len(f.objects.opened)
			if _, err := f.catalog.RegisterConversionOutputs(f.db.ctx, f.work, f.input); !errors.Is(err, ErrCatalogIntegrity) {
				t.Fatalf("damaged protection was accepted: %v", err)
			}
			if len(f.objects.opened) != opened {
				t.Fatal("damaged protection opened objects")
			}
			requireCatalogReplaySnapshot(t, f, before)
		})
	}
}
