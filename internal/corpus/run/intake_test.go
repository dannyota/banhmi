package run

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"danny.vn/banhmi/internal/corpus/model"
)

func intakeFixture() IntakeApplyRequest {
	d := model.Digest(strings.Repeat("a", 64))
	r := IntakeApplyRequest{
		Work: IntakeWork{
			Execution: ScopedExecution{Country: "vn", WorkflowID: "document-ingest/vn/version/attempt", WorkflowKind: "DocumentIngest", AttemptKey: "attempt", AttemptFence: 1, AdmissionEpoch: 1, Owner: "worker", Scope: ExecutionScope{Kind: ScopeIntake, Subject: IntakeSubject{Kind: SubjectDocumentVersion, DocumentVersion: "version"}}},
			Stage:     Extract, Substep: "convert", Shard: "artifact-1", InputDigest: d, RecipeDigest: d, BudgetDigest: d,
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
		func(w *IntakeWork) { w.Execution.Scope.Subject.DocumentVersion += "2" },
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
	allowed := map[string]bool{
		"context": true, "crypto/sha256": true, "encoding/hex": true,
		"fmt": true, "reflect": true, "strings": true, "time": true,
		"github.com/google/uuid": true, "danny.vn/banhmi/internal/corpus/blob": true,
		"danny.vn/banhmi/internal/corpus/model": true,
	}
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

func TestIntakeRejectsInventedBuild(t *testing.T) {
	r := intakeFixture()
	r.Work.Execution.Scope = ExecutionScope{Kind: ScopeBuild, BuildID: BuildKey(uuid.MustParse("22222222-2222-2222-2222-222222222222"))}
	r.IdempotencyKey = IntakeIdempotencyKey(r.Work)
	if r.Validate() == nil {
		t.Fatal("DocumentIngest accepted invented build")
	}
	r.Work.Execution.WorkflowKind = CorpusBuild
	r.Work.Execution.WorkflowID = "corpus-build/vn/22222222-2222-2222-2222-222222222222"
	r.IdempotencyKey = IntakeIdempotencyKey(r.Work)
	if r.Validate() == nil {
		t.Fatal("intake accepted correctly labeled build work")
	}
}

func TestIntakeReplayConflicts(t *testing.T) {
	r := intakeFixture()
	r.Outputs = []model.AssetRef{{Path: "objects/a", SHA256: r.OutputDigest, ByteSize: 1}, {Path: "objects/b", SHA256: r.OutputDigest, ByteSize: 2}}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*IntakeApplyRequest){
		func(r *IntakeApplyRequest) { r.Outcome = OutcomeFailed },
		func(r *IntakeApplyRequest) { r.Outputs[0].Path = "objects/c" },
		func(r *IntakeApplyRequest) { r.Outputs[1].ByteSize++ },
		func(r *IntakeApplyRequest) { r.Outputs[1].SHA256 = model.Digest(strings.Repeat("b", 64)) },
	}
	for i, mutate := range mutations {
		changed := r
		changed.Outputs = append([]model.AssetRef(nil), r.Outputs...)
		mutate(&changed)
		if changed.IdempotencyKey != r.IdempotencyKey {
			t.Fatal("test changed key")
		}
		if err := ValidateIntakeReplay(r, changed, true); !errors.Is(err, ErrPayloadConflict) {
			t.Fatalf("mutation %d did not return payload conflict: %v", i, err)
		}
	}
	changed := r
	changed.Work.Execution.Owner = "reclaimed-worker"
	changed.Work.Execution.AttemptFence++
	if err := ValidateIntakeReplay(r, changed, true); err != nil {
		t.Fatal(err)
	}
	if err := ValidateIntakeReplay(r, changed, false); !errors.Is(err, ErrStaleGrant) {
		t.Fatal("stale identical replay did not fail")
	}
}

func decisionFixture() IntakeApplyRequest {
	r := intakeFixture()
	r.Acceptance = model.AcceptedWithGaps
	r.RevisionKey = "vn:doc:11111111-1111-1111-1111-111111111111:rev:" + strings.Repeat("a", 64)
	r.CandidateDigest, r.QualityReportHash, r.OriginalHash = r.OutputDigest, r.OutputDigest, r.OutputDigest
	r.Work.DecisionRevision = 1
	r.Decision = &DecisionBinding{DecisionKey: "decision", ActorKey: "reviewer", Action: model.DecisionAcceptGaps, ExpiresAt: time.Unix(2000000000, 0),
		AttemptFence: r.Work.Execution.AttemptFence, AdmissionEpoch: r.Work.Execution.AdmissionEpoch, DecisionRevision: 1,
		Scope: r.Work.Execution.Scope, RevisionKey: r.RevisionKey, CandidateDigest: r.CandidateDigest, ReportHash: r.QualityReportHash,
		OriginalHash: r.OriginalHash, OutputDigest: r.OutputDigest, ScopeManifest: r.OutputDigest}
	r.IdempotencyKey = IntakeIdempotencyKey(r.Work)
	return r
}

func TestDecisionAndComparisonBindings(t *testing.T) {
	r := decisionFixture()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	for i, mutate := range []func(*DecisionBinding){
		func(d *DecisionBinding) { d.ActorKey = "" },
		func(d *DecisionBinding) { d.ExpiresAt = time.Time{} },
		func(d *DecisionBinding) { d.AttemptFence++ },
		func(d *DecisionBinding) { d.AdmissionEpoch++ },
		func(d *DecisionBinding) { d.DecisionRevision++ },
		func(d *DecisionBinding) { d.Scope.Subject.DocumentVersion = "another" },
		func(d *DecisionBinding) {
			d.RevisionKey = "vn:doc:11111111-1111-1111-1111-111111111111:rev:" + strings.Repeat("b", 64)
		},
		func(d *DecisionBinding) { d.CandidateDigest = model.Digest(strings.Repeat("b", 64)) },
		func(d *DecisionBinding) { d.ReportHash = model.Digest(strings.Repeat("b", 64)) },
		func(d *DecisionBinding) { d.OriginalHash = model.Digest(strings.Repeat("b", 64)) },
		func(d *DecisionBinding) { d.OutputDigest = model.Digest(strings.Repeat("b", 64)) },
		func(d *DecisionBinding) { d.ScopeManifest = "" },
		func(d *DecisionBinding) { d.Action = model.DecisionRetryConverter },
	} {
		changed := decisionFixture()
		mutate(changed.Decision)
		if changed.Validate() == nil {
			t.Fatalf("accepted decision mutation %d", i)
		}
	}
	r.Decision = nil
	if r.Validate() == nil {
		t.Fatal("gap acceptance omitted decision")
	}
	r = decisionFixture()
	r.Comparison = &ComparisonBinding{PriorRevisionKey: "vn:doc:11111111-1111-1111-1111-111111111111:rev:" + strings.Repeat("b", 64), PriorCandidateDigest: r.OutputDigest, ComparisonHash: r.OutputDigest, RequiresAdjudication: true}
	if r.Validate() == nil {
		t.Fatal("critical comparison omitted adjudication")
	}
	r.Comparison.Adjudication = r.Decision
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	r.Comparison.PriorCandidateDigest = "bad"
	if r.Validate() == nil {
		t.Fatal("comparison accepted missing prior candidate binding")
	}
}

func TestRevisionBindingsRejectMalformedAndCrossCountryKeys(t *testing.T) {
	for _, key := range []string{"revision", "vn:doc:invalid:rev:" + strings.Repeat("a", 64), "my:doc:11111111-1111-1111-1111-111111111111:rev:" + strings.Repeat("a", 64)} {
		r := decisionFixture()
		r.RevisionKey, r.Decision.RevisionKey = key, key
		if r.Validate() == nil {
			t.Fatal("acceptance accepted invalid revision")
		}
		if r.Decision.Validate(r) == nil {
			t.Fatal("decision accepted invalid revision")
		}
		c := ComparisonBinding{PriorRevisionKey: key, PriorCandidateDigest: r.OutputDigest, ComparisonHash: r.OutputDigest}
		if c.Validate(decisionFixture()) == nil {
			t.Fatal("comparison accepted invalid prior revision")
		}
	}
}

func TestEnvelopeSentinels(t *testing.T) {
	hash := model.Digest(strings.Repeat("a", 64))
	for _, envelope := range []interface{ Validate() error }{
		IntakeWorkEnvelope{Key: "execution-" + strings.Repeat("a", 32), Manifest: hash},
		IntakeReceiptEnvelope{Key: "result-" + strings.Repeat("a", 32), Manifest: hash},
	} {
		if err := envelope.Validate(); err != nil {
			t.Fatal(err)
		}
		if _, err := json.Marshal(envelope); err != nil {
			t.Fatal(err)
		}
		typ := reflect.TypeOf(envelope)
		if typ.NumField() != 2 || typ.Field(0).Name != "Key" || typ.Field(1).Name != "Manifest" {
			t.Fatalf("unexpected transport payload shape: %s", typ)
		}
		for _, sentinel := range []string{"Jane.Doe", "synthetic.person@example.invalid", "Jane Doe", "people/Jane.Doe", "https://example.invalid/person/Jane.Doe?email=synthetic.person@example.invalid", "/tmp/Jane.Doe"} {
			for field := 0; field < typ.NumField(); field++ {
				changed := reflect.New(typ).Elem()
				changed.Set(reflect.ValueOf(envelope))
				changed.Field(field).SetString(sentinel)
				invalid := changed.Interface().(interface{ Validate() error })
				err := invalid.Validate()
				if err == nil || strings.Contains(err.Error(), sentinel) {
					t.Fatalf("%s field %d accepted or echoed sentinel", typ, field)
				}
				encoded, err := json.Marshal(invalid)
				if err == nil || len(encoded) != 0 || strings.Contains(err.Error(), sentinel) {
					t.Fatalf("%s field %d serialized or echoed sentinel", typ, field)
				}
			}
		}
	}
}

func TestExpiredDecisionIsStaleGrant(t *testing.T) {
	r := decisionFixture()
	if err := r.ValidateAt(r.Decision.ExpiresAt.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := r.ValidateAt(r.Decision.ExpiresAt); !errors.Is(err, ErrStaleGrant) {
		t.Fatal("expired decision did not return stale grant")
	}
}

func TestIntakeValidationFieldContext(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*IntakeApplyRequest)
	}{
		{"execution", func(r *IntakeApplyRequest) { r.Work.Execution.Owner = "" }},
		{"input digest", func(r *IntakeApplyRequest) { r.Work.InputDigest = "bad" }},
		{"recipe digest", func(r *IntakeApplyRequest) { r.Work.RecipeDigest = "bad" }},
		{"budget digest", func(r *IntakeApplyRequest) { r.Work.BudgetDigest = "bad" }},
		{"output digest", func(r *IntakeApplyRequest) { r.OutputDigest = "bad" }},
	} {
		r := intakeFixture()
		test.mutate(&r)
		err := r.Validate()
		if err == nil || !strings.Contains(err.Error(), test.name) || errors.Unwrap(err) == nil {
			t.Fatalf("missing wrapped field context for %s", test.name)
		}
	}
}

func TestExportedContractDocumentation(t *testing.T) {
	for _, name := range []string{"intake.go", "scopes.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				if declaration.Name.IsExported() && declaration.Doc == nil {
					t.Errorf("%s: exported method %s lacks documentation", name, declaration.Name.Name)
				}
			case *ast.GenDecl:
				for _, spec := range declaration.Specs {
					if value, ok := spec.(*ast.ValueSpec); ok {
						for _, valueName := range value.Names {
							if valueName.IsExported() && value.Doc == nil && (declaration.Lparen.IsValid() || declaration.Doc == nil) {
								t.Errorf("%s: exported value %s lacks documentation", name, valueName.Name)
							}
						}
						continue
					}
					typ, ok := spec.(*ast.TypeSpec)
					if !ok || !typ.Name.IsExported() {
						continue
					}
					if typ.Doc == nil && declaration.Doc == nil {
						t.Errorf("%s: exported type %s lacks documentation", name, typ.Name.Name)
					}
					if contract, ok := typ.Type.(*ast.InterfaceType); ok {
						for _, method := range contract.Methods.List {
							if len(method.Names) > 0 && method.Names[0].IsExported() && method.Doc == nil {
								t.Errorf("%s: interface method %s lacks documentation", name, method.Names[0].Name)
							}
						}
					}
				}
			}
		}
	}
}
