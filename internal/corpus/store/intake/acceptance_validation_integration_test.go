package intake

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/quality/formats"
	"danny.vn/banhmi/internal/corpus/run"
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
				// Remove only this disposable fixture's protection and staged bytes.
				if _, err := f.db.conn.Exec(f.db.ctx, `DELETE FROM bronze.blob_retention_reference WHERE blob_id=(SELECT id FROM bronze.blob WHERE sha256=$1)`, string(witness.SHA256)); err != nil {
					t.Fatal(err)
				}
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
					r.Metrics = append(r.Metrics, r.Metrics[0])
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
				if name == "foreign-gap" {
					parent, err := formats.ProjectRollupParent(r, catalogRef(metadata), f.policy, formats.DefaultLimits())
					if err != nil {
						t.Fatal(err)
					}
					f.persistReport(t, metadata, parent, r.Metrics, r.Findings, r.Gaps)
				}
				replaceAcceptanceOutput(f, f.request.QualityReportHash, catalogRef(metadata))
				f.request.QualityReportHash, f.request.OutputDigest = model.Digest(metadata.SHA256), model.Digest(metadata.SHA256)
				before := acceptanceSnapshot(t, f)
				if _, err := f.adapter.Apply(f.db.ctx, f.request); err == nil {
					t.Fatal("malformed retained report was accepted")
				}
				if !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
					t.Fatal("malformed report changed the commit set or synchronization rows")
				}
			})
		}
	}
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
				metadata := f.register(t, acceptanceJSON(t, input))
				replaceAcceptanceOutput(f, f.request.CandidateDigest, catalogRef(metadata))
				f.request.CandidateDigest = model.Digest(metadata.SHA256)
			}
			before := acceptanceSnapshot(t, f)
			if _, err := f.adapter.Apply(f.db.ctx, f.request); err == nil {
				t.Fatal("unresolved candidate or comparison contract authorized acceptance")
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
			before := acceptanceSnapshot(t, f)
			if _, err := f.adapter.Apply(f.db.ctx, f.request); err == nil {
				t.Fatal("replay accepted an incomplete original commit")
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
	if _, err := f.adapter.Apply(f.db.ctx, f.request); err == nil {
		t.Fatal("indexed report projection overrode retained report bytes")
	}
	if !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
		t.Fatal("projection integrity failure changed rows")
	}
}

// TestAcceptanceConstructorPerformsNoIO checks dependency validation without object access.
func TestAcceptanceConstructorPerformsNoIO(t *testing.T) {
	objects := &catalogObjects{data: map[string][]byte{}}
	if _, err := NewAcceptance(nil, "writer-a", objects, nil); err == nil {
		t.Fatal("missing transaction dependency was accepted")
	}
	if _, err := NewAcceptance(nil, "writer-a", nil, nil); err == nil {
		t.Fatal("missing object dependency was accepted")
	}
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
	root := f.register(t, acceptanceJSON(t, input))
	replaceAcceptanceOutput(f, f.request.CandidateDigest, catalogRef(root))
	f.request.CandidateDigest = model.Digest(root.SHA256)
	before := acceptanceSnapshot(t, f)
	if _, err := f.adapter.Apply(f.db.ctx, f.request); err == nil {
		t.Fatal("over-limit closure was truncated or accepted")
	}
	if !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
		t.Fatal("closure overflow left synchronization or commit rows")
	}
}
