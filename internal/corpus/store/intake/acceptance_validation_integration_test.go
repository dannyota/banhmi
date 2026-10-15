package intake

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/blob"
	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/quality/formats"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

// TestAcceptanceReclaimedReplayUsesOriginalProtection checks receipt recovery under a new fence.
func TestAcceptanceReclaimedReplayUsesOriginalProtection(t *testing.T) {
	f := newAcceptanceFixture(t, model.QualityQuarantined)
	first, err := f.adapter.Apply(f.db.ctx, f.request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Exec(f.db.ctx, `UPDATE ingest.execution_lease SET cancelled_at=clock_timestamp() WHERE resource_kind='document_version'`); err != nil {
		t.Fatal(err)
	}
	lease, err := f.ledger.ReclaimLease(f.db.ctx, 1, f.request.Work.Execution.Scope, "owner-b", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	f.request.Work.Execution.Owner, f.request.Work.Execution.AttemptFence = lease.Owner, lease.AttemptFence
	if _, err := f.ledger.RegisterExecution(f.db.ctx, f.request.Work, "reclaimed-acceptance", nil); err != nil {
		t.Fatal(err)
	}
	before := acceptanceSnapshot(t, f)
	again, err := f.adapter.Apply(f.db.ctx, f.request)
	if err != nil || !reflect.DeepEqual(first, again) || !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
		t.Fatal("reclaimed replay did not retain the original receipt and protection", err)
	}
}

// TestAcceptanceNestedReportEvidence checks qualification closure and logical aliases.
func TestAcceptanceNestedReportEvidence(t *testing.T) {
	for _, name := range []string{"retained", "alias", "alias-size", "missing", "outside-span"} {
		t.Run(name, func(t *testing.T) {
			f := newAcceptanceFixture(t, model.QualityQuarantined)
			witness := catalogRef(f.register(t, []byte("synthetic qualification witness")))
			if strings.HasPrefix(name, "alias") {
				witness = f.input.Artifacts[0].Capture.Asset
				witness.Path = "qualification-alias"
				if name == "alias-size" {
					witness.ByteSize++
				}
			}
			gate := f.gate
			gate.Artifact.Findings = append([]model.Finding(nil), gate.Artifact.Findings...)
			gate.Artifact.Findings[0].Evidence = []model.AssetRef{witness}
			if name == "outside-span" {
				gate.Artifact.Findings[0].Spans = []model.Span{{ArtifactHash: fixtureDigest, BlockID: "document", Space: model.OriginalBytes, Range: &model.ByteRange{Start: 0, End: 1}}}
			}
			codec, err := formats.GateReportCodec(f.policy, formats.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			body, _, err := codec.Encode(gate)
			if err != nil {
				t.Fatal(err)
			}
			members := []model.AssetRef{witness}
			if name == "alias-size" {
				members = nil
			}
			gateMetadata := f.register(t, body, members...)
			rollup := f.rollup
			rollup.ArtifactReports = append([]formats.ArtifactReport(nil), rollup.ArtifactReports...)
			oldGate := rollup.ArtifactReports[0].Report.SHA256
			rollup.ArtifactReports[0].Report = catalogRef(gateMetadata)
			rollupCodec, err := formats.GateRollupCodec(f.policy, formats.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			body, _, err = rollupCodec.Encode(rollup)
			if err != nil {
				t.Fatal(err)
			}
			rollupMetadata := f.register(t, body, catalogRef(gateMetadata))
			if name != "outside-span" && name != "alias-size" {
				parent, err := formats.ProjectArtifactParent(gate, catalogRef(gateMetadata), f.policy, formats.DefaultLimits())
				if err != nil {
					t.Fatal(err)
				}
				f.persistReport(t, gateMetadata, parent, gate.Artifact.Metrics, gate.Artifact.Findings, []formats.Gap{})
				parent, err = formats.ProjectRollupParent(rollup, catalogRef(rollupMetadata), f.policy, formats.DefaultLimits())
				if err != nil {
					t.Fatal(err)
				}
				f.persistReport(t, rollupMetadata, parent, rollup.Metrics, rollup.Findings, rollup.Gaps)
			}
			replaceAcceptanceOutput(f, oldGate, catalogRef(gateMetadata))
			replaceAcceptanceOutput(f, f.request.QualityReportHash, catalogRef(rollupMetadata))
			f.request.QualityReportHash, f.request.OutputDigest = model.Digest(rollupMetadata.SHA256), model.Digest(rollupMetadata.SHA256)
			if name != "missing" && !strings.HasPrefix(name, "alias") {
				f.request.Outputs = append(f.request.Outputs, witness)
				sort.Slice(f.request.Outputs, func(i, j int) bool { return f.request.Outputs[i].SHA256 < f.request.Outputs[j].SHA256 })
			} else if name == "missing" {
				// Remove only the staged bytes, preserving closure and protection.
				f.request.Outputs = append(f.request.Outputs, witness)
				sort.Slice(f.request.Outputs, func(i, j int) bool { return f.request.Outputs[i].SHA256 < f.request.Outputs[j].SHA256 })
				delete(f.objects.data, witness.Path)
			}
			before := acceptanceSnapshot(t, f)
			_, err = f.adapter.Apply(f.db.ctx, f.request)
			if name == "retained" || name == "alias" {
				if err != nil {
					t.Fatal("qualification evidence outside the sandbox must remain usable", err)
				}
			} else if err == nil || !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
				t.Fatal("invalid nested evidence changed acceptance rows", err)
			}
			if name == "alias-size" {
				requireAcceptanceCause(t, err, "logical aliases disagree on size")
			} else if name == "outside-span" {
				requireAcceptanceCause(t, err, "asset path must")
			} else if name == "missing" {
				requireAcceptanceCause(t, err, "synthetic object absent")
			}
		})
	}
}

func replaceAcceptanceOutput(f *acceptanceFixture, old model.Digest, replacement model.AssetRef) {
	for k, ref := range f.request.Outputs {
		if ref.SHA256 == old {
			f.request.Outputs[k] = replacement
		}
	}
	sort.Slice(f.request.Outputs, func(i, j int) bool { return f.request.Outputs[i].SHA256 < f.request.Outputs[j].SHA256 })
}

// TestAcceptanceRejectsMalformedReportsInEveryIneligibleState checks report integrity before disposition.
func TestAcceptanceRejectsMalformedReportsInEveryIneligibleState(t *testing.T) {
	for _, state := range []model.AcceptanceState{model.Candidate, model.Quarantined} {
		for _, name := range []string{"version", "candidate", "empty-metrics", "missing-metric", "duplicate-metric", "extra-metric", "mandatory", "threshold", "unknown-pass", "unknown-score", "zero-denominator", "not-applicable", "missing-artifact-report", "missing-ordinal", "retained-missing-conversion", "foreign-gap", "duplicate-gap", "unknown-reason"} {
			t.Run(string(state)+"/"+name, func(t *testing.T) {
				f := newAcceptanceFixture(t, model.QualityQuarantined)
				if state == model.Candidate {
					f.request.Acceptance, f.request.Outcome = state, run.OutcomeSucceeded
					f.request.Work.Substep = "evidence-candidate"
					f.request.Counts = run.Counts{Expected: 1, Processed: 1, Succeeded: 1}
					f.request.IdempotencyKey = run.IntakeIdempotencyKey(f.request.Work)
				}
				r := f.rollup
				r.Metrics = append([]model.QualityMetric(nil), r.Metrics...)
				r.Gaps = append([]formats.Gap(nil), r.Gaps...)
				r.Retained = append([]formats.Retained(nil), r.Retained...)
				switch name {
				case "version":
					r.SchemaVersion = "gate-rollup-v2"
				case "candidate":
					r.CandidateDigest = fixtureDigest
				case "empty-metrics":
					r.Metrics = []model.QualityMetric{}
				case "missing-metric":
					r.Metrics = r.Metrics[:1]
				case "duplicate-metric":
					r.Metrics[1] = r.Metrics[0]
				case "extra-metric":
					extra := r.Metrics[0]
					extra.Kind = model.MetricTextCoverage
					r.Metrics = append(r.Metrics, extra)
				case "mandatory":
					r.Metrics[0].Mandatory = false
				case "threshold":
					r.Metrics[0].Threshold = "forged"
				case "unknown-pass":
					r.Metrics[0].Passed = true
				case "unknown-score":
					r.Metrics[0].Numerator = 1
				case "zero-denominator":
					r.Metrics[0].State = model.MetricMeasured
				case "not-applicable":
					r.Metrics[0].State = model.MetricNotApplicable
					r.Metrics[0].Evidence = []model.AssetRef{f.input.Artifacts[0].Capture.Asset}
				case "missing-artifact-report":
					r.ArtifactReports = []formats.ArtifactReport{}
				case "missing-ordinal":
					r.MissingOrdinals = []int{}
				case "retained-missing-conversion":
					r.Retained[0].Spans = []model.Span{{ArtifactHash: f.request.OriginalHash, BlockID: "document", Space: model.NormalizedText, Range: &model.ByteRange{Start: 0, End: 1}}}
				case "foreign-gap":
					r.Gaps[0].ScopeKey = "foreign-artifact"
				case "duplicate-gap":
					r.Gaps = append(r.Gaps, r.Gaps[0])
				case "unknown-reason":
					r.Gaps[0].ReasonCode = "UNKNOWN_REASON"
				}
				// Invalid reports deliberately bypass the producer codec. Acceptance
				// must independently reject their retained, correctly hashed bytes.
				metadata := f.register(t, acceptanceJSON(t, r))
				switch name {
				case "unknown-pass", "unknown-score", "zero-denominator":
					// These violate intrinsic metric or SQL child constraints and
					// cannot be projected as otherwise valid Silver records.
				default:
					parent := formats.QualityParent{Report: catalogRef(metadata), PolicyHash: r.PolicyHash, ThresholdVersion: r.ThresholdVersion, Outcome: r.Outcome, RecordCount: int32(len(r.Metrics) + len(r.Findings) + len(r.Gaps))}
					f.persistReport(t, metadata, parent, r.Metrics, r.Findings, r.Gaps)
				}
				replaceAcceptanceOutput(f, f.request.QualityReportHash, catalogRef(metadata))
				f.request.QualityReportHash, f.request.OutputDigest = model.Digest(metadata.SHA256), model.Digest(metadata.SHA256)
				before := acceptanceSnapshot(t, f)
				_, err := f.adapter.Apply(f.db.ctx, f.request)
				stage := "acceptance roll-up decode:"
				if name == "candidate" {
					stage = "acceptance roll-up binding:"
				} else if name == "foreign-gap" {
					stage = "retained evidence differs from its deterministic projection"
				}
				requireAcceptanceCause(t, err, stage)
				if !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
					t.Fatal("malformed report changed the commit set or synchronization rows")
				}
			})
		}
	}
}

func requireAcceptanceCause(t *testing.T, err error, want string) {
	t.Helper()
	var contains func(error) bool
	contains = func(cause error) bool {
		if cause == nil {
			return false
		}
		if strings.Contains(cause.Error(), want) {
			return true
		}
		if joined, ok := cause.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				if contains(child) {
					return true
				}
			}
		}
		return contains(errors.Unwrap(cause))
	}
	if !contains(err) {
		t.Fatalf("acceptance did not reject at %q: %s", want, acceptanceTestCause(err))
	}
}

// Rebuild every downstream digest and Silver report after changing the root.
// The malformed root remains the sole intended contract violation.
func rebindAcceptanceCandidate(t *testing.T, f *acceptanceFixture, input formats.CandidateInput) {
	t.Helper()
	metadata := f.register(t, acceptanceJSON(t, input))
	replaceAcceptanceOutput(f, f.request.CandidateDigest, catalogRef(metadata))
	f.request.CandidateDigest = model.Digest(metadata.SHA256)
	f.input = input
	gate := f.gate
	gate.Artifact.CandidateDigest, gate.Artifact.PolicyHash = f.request.CandidateDigest, input.GatePolicyDigest
	gateMetadata := f.register(t, acceptanceJSON(t, gate))
	oldGate := f.rollup.ArtifactReports[0].Report.SHA256
	f.rollup.ArtifactReports = append([]formats.ArtifactReport(nil), f.rollup.ArtifactReports...)
	f.rollup.ArtifactReports[0].Report = catalogRef(gateMetadata)
	f.rollup.CandidateDigest, f.rollup.PolicyHash = f.request.CandidateDigest, input.GatePolicyDigest
	rollupMetadata := f.register(t, acceptanceJSON(t, f.rollup), catalogRef(gateMetadata))
	artifactHash := gate.Artifact.ArtifactHash
	f.persistReport(t, gateMetadata, formats.QualityParent{Report: catalogRef(gateMetadata), ArtifactHash: &artifactHash, PolicyHash: gate.Artifact.PolicyHash, ThresholdVersion: gate.Artifact.ThresholdVersion, Outcome: gate.Artifact.Outcome, RecordCount: int32(len(gate.Artifact.Metrics) + len(gate.Artifact.Findings))}, gate.Artifact.Metrics, gate.Artifact.Findings, []formats.Gap{})
	f.persistReport(t, rollupMetadata, formats.QualityParent{Report: catalogRef(rollupMetadata), PolicyHash: f.rollup.PolicyHash, ThresholdVersion: f.rollup.ThresholdVersion, Outcome: f.rollup.Outcome, RecordCount: int32(len(f.rollup.Metrics) + len(f.rollup.Findings) + len(f.rollup.Gaps))}, f.rollup.Metrics, f.rollup.Findings, f.rollup.Gaps)
	replaceAcceptanceOutput(f, oldGate, catalogRef(gateMetadata))
	replaceAcceptanceOutput(f, f.request.QualityReportHash, catalogRef(rollupMetadata))
	f.request.QualityReportHash, f.request.OutputDigest = model.Digest(rollupMetadata.SHA256), model.Digest(rollupMetadata.SHA256)
	f.gate = gate
}

// TestAcceptanceRejectsUnresolvedCandidateContracts checks closed candidate and comparison contracts.
func TestAcceptanceRejectsUnresolvedCandidateContracts(t *testing.T) {
	for _, name := range []string{"version", "policy", "origin", "budget", "recipe", "reprocess", "comparison", "adjudication"} {
		t.Run(name, func(t *testing.T) {
			f := newAcceptanceFixture(t, model.QualityQuarantined)
			input := f.input
			switch name {
			case "version":
				input.SchemaVersion = "candidate-input-v2"
			case "policy":
				input.GatePolicyDigest = model.Digest(strings.Repeat("e", 64))
			case "origin":
				input.ProcessingExecutionKey = "execution-" + strings.Repeat("f", 32)
			case "budget":
				input.BudgetDigest = model.Digest(strings.Repeat("e", 64))
			case "recipe":
				input.RecipeDigest = model.Digest(strings.Repeat("e", 64))
			case "reprocess":
				input.Mode = "reprocess"
				input.Predecessor = &formats.Predecessor{DocumentVersionKey: "version-a", RevisionKey: f.request.RevisionKey, CandidateDigest: f.request.CandidateDigest, AcceptanceWatermark: 1}
			case "comparison", "adjudication":
				f.request.Comparison = &run.ComparisonBinding{PriorRevisionKey: f.request.RevisionKey, PriorCandidateDigest: fixtureDigest, ComparisonHash: fixtureDigest, RequiresAdjudication: name == "adjudication"}
			}
			if name != "comparison" && name != "adjudication" {
				rebindAcceptanceCandidate(t, f, input)
			}
			before := acceptanceSnapshot(t, f)
			_, err := f.adapter.Apply(f.db.ctx, f.request)
			stage := "candidate execution binding or comparison contract is unavailable"
			switch name {
			case "version":
				stage = "acceptance input decode:"
			case "adjudication":
				stage = "critical changes require owner adjudication"
			case "policy", "origin":
				if !errors.Is(err, pgx.ErrNoRows) {
					t.Fatal("missing retained binding did not fail its lookup", err)
				}
				stage = ""
			}
			if stage != "" {
				requireAcceptanceCause(t, err, stage)
			}
			if !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
				t.Fatal("candidate contract failure changed rows")
			}
		})
	}
}

// TestAcceptanceReplayNeverRepairsMissingCommitRows checks fail-closed replay of damaged commits.
func TestAcceptanceReplayNeverRepairsMissingCommitRows(t *testing.T) {
	for _, table := range []string{"ingest.acceptance_lock", "ingest.revision_receipt", "ingest.delivery_record", "bronze.blob_retention_reference"} {
		t.Run(table, func(t *testing.T) {
			f := newAcceptanceFixture(t, model.QualityQuarantined)
			receipt, err := f.adapter.Apply(f.db.ctx, f.request)
			if err != nil {
				t.Fatal(err)
			}
			// Only the isolated fixture migrator can introduce this corruption.
			// The acceptance connection remains the runtime login throughout.
			statement := "DELETE FROM " + table
			args := []any{}
			switch table {
			case "ingest.revision_receipt":
				statement += " WHERE execution_result_id=(SELECT id FROM ingest.execution_result WHERE result_key=$1)"
				args = append(args, receipt.ResultID)
				if _, err := f.db.conn.Exec(f.db.ctx, `ALTER TABLE ingest.revision_receipt DISABLE TRIGGER revision_receipt_immutable`); err != nil {
					t.Fatal(err)
				}
			case "ingest.delivery_record":
				statement += " WHERE receipt_key=$1"
				args = append(args, receipt.ResultID)
			case "bronze.blob_retention_reference":
				statement += " WHERE owner_kind='execution' AND blob_id=(SELECT id FROM bronze.blob WHERE sha256=$1)"
				args = append(args, string(f.request.CandidateDigest))
			}
			if _, err := f.db.conn.Exec(f.db.ctx, statement, args...); err != nil {
				t.Fatal(err)
			}
			if table == "ingest.revision_receipt" {
				if _, err := f.db.conn.Exec(f.db.ctx, `ALTER TABLE ingest.revision_receipt ENABLE TRIGGER revision_receipt_immutable`); err != nil {
					t.Fatal(err)
				}
			}
			before := acceptanceSnapshot(t, f)
			_, err = f.adapter.Apply(f.db.ctx, f.request)
			switch table {
			case "ingest.acceptance_lock":
				if !errors.Is(err, pgx.ErrNoRows) {
					t.Fatal("missing synchronization row did not fail its lock", err)
				}
			case "ingest.revision_receipt":
				requireAcceptanceCause(t, err, "revision receipt is missing or duplicated")
			case "ingest.delivery_record":
				requireAcceptanceCause(t, err, "acceptance commit set is incomplete")
			case "bronze.blob_retention_reference":
				requireAcceptanceCause(t, err, "closure lacks original execution protection")
			}
			if !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
				t.Fatal("replay repaired or changed pre-existing rows")
			}
		})
	}
}

// TestAcceptanceReportProjectionMustMatchRetainedBytes checks deterministic finding projections.
func TestAcceptanceReportProjectionMustMatchRetainedBytes(t *testing.T) {
	f := newAcceptanceFixture(t, model.QualityQuarantined)
	var reportID int64
	if err := f.app.QueryRow(f.db.ctx, `SELECT id FROM silver.quality_report WHERE report_hash=$1`, string(f.request.QualityReportHash)).Scan(&reportID); err != nil {
		t.Fatal(err)
	}
	// Changing the trusted indexed projection requires disabling its immutable
	// guard on this disposable database. Re-enable it before invoking acceptance.
	if _, err := f.db.conn.Exec(f.db.ctx, `ALTER TABLE silver.quality_finding DISABLE TRIGGER quality_finding_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.conn.Exec(f.db.ctx, `UPDATE silver.quality_finding SET threshold='forged' WHERE quality_report_id=$1 AND record_kind='metric'`, reportID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.conn.Exec(f.db.ctx, `ALTER TABLE silver.quality_finding ENABLE TRIGGER quality_finding_immutable`); err != nil {
		t.Fatal(err)
	}
	before := acceptanceSnapshot(t, f)
	_, err := f.adapter.Apply(f.db.ctx, f.request)
	requireAcceptanceCause(t, err, "retained evidence differs from its deterministic projection")
	if !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
		t.Fatal("projection integrity failure changed rows")
	}
}

// TestAcceptanceConstructorPerformsNoIO checks dependency validation without object access.
func TestAcceptanceConstructorPerformsNoIO(t *testing.T) {
	objects := &catalogObjects{data: map[string][]byte{}}
	_, err := NewAcceptance(nil, "writer-a", objects, nil)
	requireAcceptanceCause(t, err, "transaction")
	_, err = NewAcceptance(nil, "writer-a", nil, nil)
	requireAcceptanceCause(t, err, "acceptance object store is required")
	if len(objects.opened) != 0 {
		t.Fatal("constructor opened an object")
	}
}

// TestAcceptanceClosureOverflowRollsBackSynchronization checks policy bounds and rollback.
func TestAcceptanceClosureOverflowRollsBackSynchronization(t *testing.T) {
	f := newAcceptanceFixture(t, model.QualityQuarantined)
	policy := f.policy
	policy.AcceptanceLimits.MaxBlobs = 2
	data, digest, err := formats.PolicyBundleCodec(formats.DefaultLimits()).Encode(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Exec(f.db.ctx, `INSERT INTO config.policy_bundle(digest,manifest) VALUES($1,$2)`, string(digest), data); err != nil {
		t.Fatal(err)
	}
	input := f.input
	input.GatePolicyDigest = digest
	rebindAcceptanceCandidate(t, f, input)
	before := acceptanceSnapshot(t, f)
	_, err = f.adapter.Apply(f.db.ctx, f.request)
	requireAcceptanceCause(t, err, "acceptance closure exceeds blob or byte bound")
	if !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
		t.Fatal("closure overflow left synchronization or commit rows")
	}
}

// TestAcceptanceJSONReaderIgnoresCatalogMedia checks the shared reader once,
// including catalog winners and cumulative accounting. Routing tests are separate.
func TestAcceptanceJSONReaderIgnoresCatalogMedia(t *testing.T) {
	f := newAcceptanceFixture(t, model.QualityQuarantined)
	for _, role := range []string{"structured-object"} {
		for _, shared := range []bool{false, true} {
			t.Run(role+fmt.Sprint("/shared=", shared), func(t *testing.T) {
				label := fmt.Sprint(role, "/", shared)
				data := acceptanceJSON(t, struct{ Value string }{label + strings.Repeat("x", 128)})
				m := catalogMetadata(f.objects, "non-json/"+label, string(data))
				m.MediaType = "application/octet-stream"
				if _, err := f.catalog.RegisterConversionOutputs(f.db.ctx, catalogWork(f.work, m), ConversionRegistration{Manifest: m}); err != nil {
					t.Fatal(err)
				}
				ref := catalogRef(m)
				if shared {
					// A later JSON registration must reuse the earlier non-JSON
					// catalog winner rather than change accounting semantics.
					ref.Path = "logical/" + role
					later := m
					later.MediaType, later.ObjectRef = "application/json", ref.Path
					parent := catalogMetadata(f.objects, "shared/"+role, string(acceptanceJSON(t, ref)))
					parent.MediaType = "application/json"
					if _, err := f.catalog.RegisterConversionOutputs(f.db.ctx, catalogWork(f.work, parent), ConversionRegistration{Manifest: parent, Members: []blob.Metadata{later}, MemberRefs: []model.AssetRef{ref}}); err != nil {
						t.Fatal(err)
					}
				}
				v := newAcceptanceEvidence(f.adapter, nil, intakedb.New(f.app), f.request, time.Now())
				v.limits.MaxJSONBytes = int64(len(data)) - 1
				f.objects.opened = nil
				var decoded struct{ Value string }
				err := v.decodeObject(f.db.ctx, ref, &decoded)
				requireAcceptanceCause(t, err, "acceptance work exceeds pinned bounds")
				if len(f.objects.opened) != 0 {
					t.Fatal("over-limit JSON was opened before charging its role")
				}
				v.limits.MaxJSONBytes = 2*int64(len(data)) - 1
				if err := v.decodeObject(f.db.ctx, ref, &decoded); err != nil {
					t.Fatal(err)
				}
				ref.Path = "second-alias/" + role
				if err := v.decodeObject(f.db.ctx, ref, &decoded); err != nil || v.jsonBytes != int64(len(data)) {
					t.Fatal("shared JSON hash was charged more than once", err)
				}
				other := f.register(t, acceptanceJSON(t, struct{ Value string }{label + strings.Repeat("y", 128)}))
				f.objects.opened = nil
				err = v.decodeObject(f.db.ctx, catalogRef(other), &decoded)
				requireAcceptanceCause(t, err, "acceptance work exceeds pinned bounds")
				if len(f.objects.opened) != 0 {
					t.Fatal("cumulative JSON overflow opened its object")
				}
			})
		}
	}
}
