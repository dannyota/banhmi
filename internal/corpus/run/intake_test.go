package run

import (
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
)

func intakeFixture() IntakeApplyRequest {
	d := model.Digest(strings.Repeat("a", 64))
	r := IntakeApplyRequest{
		Work: IntakeWork{
			Execution: ScopedExecution{Country: "vn", WorkflowID: "document-ingest/vn/version/attempt", WorkflowKind: "DocumentIngest", AttemptKey: "attempt", AttemptFence: 1, AdmissionEpoch: 1, Owner: "worker", Scope: ExecutionScope{Kind: ScopeIntake, DocumentVersion: "version"}},
			Stage: Extract, Substep: "convert", Shard: "artifact-1", InputDigest: d, RecipeDigest: d, BudgetDigest: d,
			Budget: Budget{MaxItems: 1, MaxInputBytes: 100, MaxPages: 1, MaxElapsed: time.Second, MaxConcurrent: 1},
		},
		OutputDigest: d, Outcome: OutcomeSucceeded, Counts: Counts{Expected: 1, Processed: 1, Succeeded: 1}, Complete: true,
	}
	r.IdempotencyKey = IntakeIdempotencyKey(r.Work)
	return r
}

func TestIntakeIdentityAndReplay(t *testing.T) {
	r := intakeFixture()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	changed := r
	changed.Work.Execution.Owner = "new-worker"
	changed.Work.Execution.AttemptFence++
	if IntakeIdempotencyKey(changed.Work) != r.IdempotencyKey || !SameImmutableIntakePayload(r, changed) {
		t.Fatal("mutable scheduler state changed immutable identity")
	}
	mutations := []func(*IntakeWork){
		func(w *IntakeWork) { w.Execution.Country = "my" },
		func(w *IntakeWork) { w.Execution.WorkflowID += "2" },
		func(w *IntakeWork) { w.Execution.WorkflowKind += "2" },
		func(w *IntakeWork) { w.Execution.AttemptKey += "2" },
		func(w *IntakeWork) { w.Execution.AdmissionEpoch++ },
		func(w *IntakeWork) { w.Execution.Scope.DocumentVersion += "2" },
		func(w *IntakeWork) { w.Execution.Scope = ExecutionScope{Kind: ScopeBatch, BatchCollection: "batch"} },
		func(w *IntakeWork) { w.Stage = Normalize },
		func(w *IntakeWork) { w.Substep += "2" },
		func(w *IntakeWork) { w.Shard += "2" },
		func(w *IntakeWork) { w.InputDigest = model.Digest(strings.Repeat("b", 64)) },
		func(w *IntakeWork) { w.RecipeDigest = model.Digest(strings.Repeat("b", 64)) },
		func(w *IntakeWork) { w.BudgetDigest = model.Digest(strings.Repeat("b", 64)) },
		func(w *IntakeWork) { w.DecisionRevision++ },
		func(w *IntakeWork) { w.Budget.MaxItems++ },
		func(w *IntakeWork) { w.Budget.MaxInputBytes++ },
		func(w *IntakeWork) { w.Budget.MaxPages++ },
		func(w *IntakeWork) { w.Budget.MaxElapsed++ },
		func(w *IntakeWork) { w.Budget.MaxConcurrent++ },
	}
	for i, mutate := range mutations {
		changed := r
		mutate(&changed.Work)
		if IntakeIdempotencyKey(changed.Work) == r.IdempotencyKey || SameImmutableIntakePayload(r, changed) {
			t.Fatalf("immutable mutation %d retained identity", i)
		}
	}
	changed = r
	changed.OutputDigest = model.Digest(strings.Repeat("c", 64))
	if SameImmutableIntakePayload(r, changed) {
		t.Fatal("changed output replay accepted")
	}
	changed = r
	changed.IdempotencyKey = "wrong"
	if changed.Validate() == nil {
		t.Fatal("accepted mismatched key")
	}
	if IntakeOutcome("unknown").Valid() {
		t.Fatal("accepted unknown outcome")
	}
}

// The new value validators import no file/network/environment capability.
func TestIntakeValidatorsHaveNoIOImports(t *testing.T) {
	allowed := map[string]bool{"context": true, "crypto/sha256": true, "encoding/hex": true, "fmt": true, "reflect": true, "time": true,
		"github.com/google/uuid": true, "danny.vn/banhmi/internal/corpus/blob": true, "danny.vn/banhmi/internal/corpus/model": true}
	for _, name := range []string{"intake.go", "scopes.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil || !allowed[path] {
				t.Fatalf("unexpected dependency %s in %s", imp.Path.Value, name)
			}
		}
	}
}
