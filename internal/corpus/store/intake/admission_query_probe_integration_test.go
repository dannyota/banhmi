package intake

import (
	"context"
	_ "embed"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

//go:embed queries/admission.sql
var admissionProbeQueries string

// This temporary hosted probe measures synthetic query cost. It establishes no
// production throughput target. Its query phase leaves writer rows unchanged.
func TestAdmissionQueryCostProbe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	started := time.Now()
	f := newAdmissionFixture(t, true)
	f.source.db.ctx = ctx
	for _, conn := range []*pgx.Conn{f.source.app, f.source.db.conn} {
		if _, err := conn.Exec(ctx, "SET statement_timeout = '10s'"); err != nil {
			t.Fatal(err)
		}
	}
	for _, setting := range []string{"server_version", "jit", "jit_above_cost", "jit_inline_above_cost", "jit_optimize_above_cost", "plan_cache_mode", "statement_timeout"} {
		var value string
		if err := f.source.app.QueryRow(ctx, "SHOW "+setting).Scan(&value); err != nil {
			t.Fatal(err)
		}
		t.Logf("setting name=%s value=%s", setting, value)
	}
	seeded := time.Now()
	base := time.Now().Add(-time.Second).UTC().Truncate(time.Microsecond)
	for i := 1; i < 256; i++ {
		admissionSeedLaunch(t, f, i, base.Add(time.Duration(i)*time.Microsecond))
	}
	t.Logf("fixture launches=256 seed_elapsed_us=%d setup_elapsed_us=%d", time.Since(seeded).Microseconds(), time.Since(started).Microseconds())
	before := admissionRows(t, f)
	q := intakedb.New(f.source.app)
	counts, err := q.CountDocumentAdmissionSlots(ctx)
	if err != nil || counts.Processing != 0 || counts.Open != 0 {
		t.Fatal("probe fixture contains admitted slots", counts, err)
	}
	var expectedLaunch intakedb.ResolveAdmissionLaunchRow
	var expectedHeads intakedb.ReadAdmissionQueueHeadsRow
	for modeIndex, mode := range []string{"inherited", "jit_off"} {
		if modeIndex == 1 {
			if _, err := f.source.app.Exec(ctx, "SET jit = off"); err != nil {
				t.Fatal(err)
			}
		}
		// Repeated parameterized calls reach generic-plan consideration when
		// the inherited plan_cache_mode is auto. The counters below record it.
		for sample := 1; sample <= 6; sample++ {
			queryStarted := time.Now()
			launch, err := q.ResolveAdmissionLaunch(ctx, f.request.DeliveryKey)
			t.Logf("sample mode=%s statement=ResolveAdmissionLaunch sample=%d elapsed_us=%d", mode, sample, time.Since(queryStarted).Microseconds())
			if err != nil {
				t.Fatal(err)
			}
			if launch.WorkflowID != f.request.Child.Execution.WorkflowID || launch.DocumentVersionKey != f.request.Child.Execution.Scope.Subject.LeaseKey() ||
				launch.WindowKey != f.request.Actor.Execution.Scope.Subject.LeaseKey() || launch.OriginExecutionID < 1 || launch.AcknowledgedAt != nil {
				t.Fatal("probe resolved a different launch")
			}
			queryStarted = time.Now()
			heads, err := q.ReadAdmissionQueueHeads(ctx)
			t.Logf("sample mode=%s statement=ReadAdmissionQueueHeads sample=%d elapsed_us=%d", mode, sample, time.Since(queryStarted).Microseconds())
			if err != nil {
				t.Fatal(err)
			}
			if heads.Launch != f.request.DeliveryKey || heads.Resume != "" || heads.LaunchWaiting != 256 || heads.ResumeWaiting != 0 {
				t.Fatal("probe queue differs from 256 pending launches", heads)
			}
			if modeIndex == 0 && sample == 1 {
				expectedLaunch, expectedHeads = launch, heads
			} else if !reflect.DeepEqual(launch, expectedLaunch) || heads != expectedHeads {
				t.Fatal("query results changed across samples or JIT settings")
			}
		}
		var prepared json.RawMessage
		if err := f.source.app.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object(
'statement', split_part(statement, E'\n', 1), 'generic_plans', generic_plans, 'custom_plans', custom_plans)), '[]'::jsonb)
FROM pg_prepared_statements WHERE statement LIKE '-- name: ResolveAdmissionLaunch %'
OR statement LIKE '-- name: ReadAdmissionQueueHeads %'`).Scan(&prepared); err != nil {
			t.Fatal(err)
		}
		t.Logf("prepared_plans mode=%s json=%s", mode, prepared)
		for _, statement := range []string{"ResolveAdmissionLaunch", "ReadAdmissionQueueHeads"} {
			body := admissionProbeQuery(t, statement)
			var args []any
			if statement == "ResolveAdmissionLaunch" {
				body = strings.ReplaceAll(body, "sqlc.arg(delivery_key)", "$1")
				args = []any{f.request.DeliveryKey}
			}
			if strings.Contains(body, "sqlc.") {
				t.Fatal("unresolved probe query parameter", statement)
			}
			var plan json.RawMessage
			if err := f.source.app.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, SETTINGS, FORMAT JSON) "+body, args...).Scan(&plan); err != nil {
				t.Fatal("explain probe query", statement, err)
			}
			if !json.Valid(plan) {
				t.Fatal("invalid JSON query plan", statement)
			}
			t.Logf("plan mode=%s statement=%s json=%s", mode, statement, plan)
		}
		requireAdmissionUnchanged(t, f, before)
	}
	t.Logf("probe elapsed_us=%d", time.Since(started).Microseconds())
}

func admissionProbeQuery(t *testing.T, name string) string {
	t.Helper()
	_, tail, ok := strings.Cut(admissionProbeQueries, "-- name: "+name+" :")
	if !ok {
		t.Fatal("missing admission probe query", name)
	}
	_, tail, ok = strings.Cut(tail, "\n")
	if !ok {
		t.Fatal("missing admission probe query body", name)
	}
	body, _, _ := strings.Cut(tail, "\n-- name:")
	return body
}
