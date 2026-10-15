package intake

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

func TestAcceptanceOversizedRegisteredReceipt(t *testing.T) {
	f := newAcceptanceFixtureWith(t, model.QualityQuarantined, strings.Repeat("r", (1<<20)+1), "text/html")
	before := acceptanceSnapshot(t, f)
	_, err := f.adapter.Apply(f.db.ctx, f.request)
	requireAcceptanceCause(t, err, "acceptance work exceeds pinned bounds")
	if !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
		t.Fatal("oversized registered receipt left a partial acceptance")
	}
}

// TestAcceptanceDatabaseJSONBounds exercises production receipt, replay and
// event decoders. Corruption is confined to the disposable migrator connection.
func TestAcceptanceDatabaseJSONBounds(t *testing.T) {
	for _, role := range []string{"receipts", "payload", "gaps"} {
		t.Run(role, func(t *testing.T) {
			f := newAcceptanceFixture(t, model.QualityQuarantined)
			receipt, err := f.adapter.Apply(f.db.ctx, f.request)
			if err != nil {
				t.Fatal(err)
			}
			table, trigger := "ingest.execution", "execution_immutable"
			statement := `UPDATE ingest.execution SET run_receipts=jsonb_build_array($1::text) WHERE execution_key=$2`
			identity := receipt.ExecutionID
			switch role {
			case "payload":
				table, trigger = "ingest.execution_result", "execution_result_immutable"
				statement = `UPDATE ingest.execution_result SET payload=payload || jsonb_build_object('Oversized',$1::text) WHERE result_key=$2`
				identity = receipt.ResultID
			case "gaps":
				table, trigger = "ingest.revision_acceptance", "revision_acceptance_immutable"
				statement = `UPDATE ingest.revision_acceptance SET scoped_gaps=scoped_gaps || jsonb_build_array(jsonb_build_object('Oversized',$1::text)) WHERE execution_result_id=(SELECT id FROM ingest.execution_result WHERE result_key=$2)`
				identity = receipt.ResultID
			}
			if _, err := f.db.conn.Exec(f.db.ctx, "ALTER TABLE "+table+" DISABLE TRIGGER "+trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.conn.Exec(f.db.ctx, statement, strings.Repeat("x", int(f.request.Work.Budget.MaxInputBytes)+1), identity); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.conn.Exec(f.db.ctx, "ALTER TABLE "+table+" ENABLE TRIGGER "+trigger); err != nil {
				t.Fatal(err)
			}
			// Observe the metadata lookup, and prove the oversized value never
			// reaches the byte-fetch statement or its production decoder.
			reads := 0
			gate := sourceInsertGate{TransactionStarter: f.app, query: "synthetic-no-pause", record: func(query string, args []any, scanned bool, _ error) {
				if !scanned && strings.Contains(query, "-- name: ReadAcceptanceJSON :one") && len(args) > 0 && args[0] == role {
					reads++
				}
			}}
			adapter, err := NewAcceptance(gate, "writer-a", f.objects, nil)
			if err != nil {
				t.Fatal(err)
			}
			before := acceptanceSnapshot(t, f)
			_, err = adapter.Apply(f.db.ctx, f.request)
			requireAcceptanceCause(t, err, "acceptance work exceeds pinned bounds")
			if reads != 0 || !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
				t.Fatal("oversized database JSON was fetched or changed the commit set")
			}
		})
	}
}

// TestAcceptanceDatabaseJSONDeduplicates tests the current/origin execution
// overlap and repeated result lookups by immutable dependency identity.
func TestAcceptanceDatabaseJSONDeduplicates(t *testing.T) {
	f := newAcceptanceFixture(t, model.QualityQuarantined)
	receipt, err := f.adapter.Apply(f.db.ctx, f.request)
	if err != nil {
		t.Fatal(err)
	}
	v := newAcceptanceEvidence(f.adapter, nil, intakedb.New(f.app), f.request, time.Now())
	for _, dependency := range []struct{ role, identity string }{{"budget", receipt.ExecutionID}, {"receipts", receipt.ExecutionID}, {"payload", f.request.IdempotencyKey}, {"policy", string(f.input.GatePolicyDigest)}} {
		first, err := v.databaseJSON(f.db.ctx, dependency.role, dependency.identity)
		if err != nil {
			t.Fatal(err)
		}
		charged, rows := v.jsonBytes, v.rows
		second, err := v.databaseJSON(f.db.ctx, dependency.role, dependency.identity)
		if err != nil || !reflect.DeepEqual(first, second) || v.jsonBytes != charged || v.rows != rows {
			t.Fatal("repeated database dependency was fetched or charged twice", err)
		}
	}
}
