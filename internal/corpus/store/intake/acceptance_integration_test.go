package intake

import (
	"context"
	"encoding/json"
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
	"danny.vn/banhmi/internal/corpus/store/document"
)

type acceptanceFixture struct {
	*sourcesAdapterFixture
	objects *catalogObjects
	adapter *Acceptance
	catalog *ConversionCatalog
	work    run.IntakeWork
	request run.IntakeApplyRequest
	input   formats.CandidateInput
	policy  formats.PolicyBundle
	rollup  formats.GateRollup
	gate    formats.GateReport
	grant   document.Grant
	store   *document.Conversion
	staged  map[model.Digest]blob.Metadata
}

func acceptanceJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (f *acceptanceFixture) register(t *testing.T, data []byte, refs ...model.AssetRef) blob.Metadata {
	t.Helper()
	digest := formats.Hash(data)
	if metadata, ok := f.staged[digest]; ok && len(refs) == 0 {
		return metadata
	}
	m := catalogMetadata(f.objects, "acceptance/"+string(digest), string(data))
	m.MediaType, m.CaptureSource = "application/json", "candidate"
	if !json.Valid(data) {
		m.MediaType = "text/plain"
	}
	if _, err := f.catalog.RegisterConversionOutputs(f.db.ctx, catalogWork(f.work, m), ConversionRegistration{Manifest: m, MemberRefs: refs}); err != nil {
		t.Fatal(err)
	}
	if len(refs) == 0 {
		if f.staged == nil {
			f.staged = make(map[model.Digest]blob.Metadata)
		}
		f.staged[digest] = m
	}
	return m
}

func (f *acceptanceFixture) persistReport(t *testing.T, metadata blob.Metadata, parent formats.QualityParent, metrics []model.QualityMetric, findings []model.Finding, gaps []formats.Gap) {
	t.Helper()
	var refs []model.AssetRef
	for path, data := range f.objects.data {
		refs = append(refs, model.AssetRef{Path: path, SHA256: formats.Hash(data), ByteSize: int64(len(data))})
	}
	rows, err := formats.ProjectQualityRows(metrics, findings, gaps, refs, formats.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	records := make([]document.ConversionQualityRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, document.ConversionQualityRecord{Ordinal: row.Ordinal, Kind: row.RecordKind, Code: row.Code,
			Severity: row.Severity, Scope: row.Scope, ScopeKey: row.ScopeKey, Reason: row.Reason,
			MetricKind: row.MetricKind, MetricState: row.MetricState, Numerator: row.Numerator, Denominator: row.Denominator,
			Threshold: row.Threshold, Mandatory: row.Mandatory, Passed: row.Passed,
			Evidence: acceptanceJSON(t, row.Evidence), SourceSpans: acceptanceJSON(t, row.SourceSpans)})
	}
	_, err = f.store.RecordConversionQualityReport(f.db.ctx, f.grant,
		document.ConversionTarget{RevisionKey: f.request.RevisionKey, CandidateDigest: f.request.CandidateDigest},
		document.ConversionQualityInput{Report: metadata, ArtifactHash: parent.ArtifactHash, PolicyDigest: parent.PolicyHash,
			ThresholdVersion: parent.ThresholdVersion, Outcome: parent.Outcome, RecordCount: parent.RecordCount, Records: records})
	if err != nil {
		t.Fatal(err)
	}
}

func newAcceptanceFixture(t *testing.T, outcome model.QualityOutcome) *acceptanceFixture {
	t.Helper()
	return newAcceptanceFixtureWith(t, outcome, "acceptance-synthetic-run", "text/html")
}

func newAcceptanceFixtureWith(t *testing.T, outcome model.QualityOutcome, receipt, media string, originals ...[]byte) *acceptanceFixture {
	t.Helper()
	started := time.Now()
	defer func() { t.Logf("acceptance setup: %s", time.Since(started)) }()
	s := newSourcesAdapterFixture(t)
	// Keep JIT compilation out of small synthetic fixtures. The acceptance SQL
	// has many nested expressions but only a handful of rows to evaluate.
	if _, err := s.app.Exec(s.db.ctx, "SET jit = off"); err != nil {
		t.Fatal(err)
	}
	inventory, plans := s.requests(t, 1)
	claim, err := s.sources.ClaimRequest(s.db.ctx, s.doc)
	if err != nil {
		t.Fatal(err)
	}
	capture := s.capture
	capture.MediaType = media
	objects := &catalogObjects{data: map[string][]byte{capture.Asset.Path: []byte("synthetic source bytes")}}
	if len(originals) != 0 {
		capture.Asset = model.AssetRef{Path: "acceptance/source-original", SHA256: formats.Hash(originals[0]), ByteSize: int64(len(originals[0]))}
		objects.data[capture.Asset.Path] = originals[0]
		s.sources, err = NewSources(s.app, "writer-a", objects, fixtureSourceContinuer{})
		if err != nil {
			t.Fatal(err)
		}
	}
	capture.CaptureKey = fmt.Sprintf("capture-%032x", 900)
	now := time.Now().Add(-time.Second)
	if _, err := s.sources.RecordTransport(s.db.ctx, sourceWork(s.doc, "transport", "acceptance-original"), SourceTransportReceipt{RequestKey: claim.RequestKey, AttemptKey: claim.AttemptKey, Attempt: claim.NextAttempt, Capture: &capture, Next: now, RetryAfter: now}); err != nil {
		t.Fatal(err)
	}
	artifact := inventory.Artifacts[0]
	artifact.Capture = &capture
	if _, err := s.sources.ApplyCapture(s.db.ctx, sourceWork(s.doc, "bronze", "acceptance-original"), CaptureAcceptance{RequestKey: claim.RequestKey, Capture: capture, Artifact: &artifact, CaptureURL: artifact.URL}); err != nil {
		t.Fatal(err)
	}
	w := s.doc
	w.Execution.WorkflowID = "document-ingest/vn/source-a/version-a-acceptance"
	w.Budget.MaxItems = 10000
	w.Stage = run.Normalize
	registered, err := s.ledger.RegisterExecution(s.db.ctx, w, receipt, nil)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewConversionCatalog(s.app, "writer-a", objects, formats.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewAcceptance(acceptanceTestStarter(t, s.app), "writer-a", objects, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := document.NewConversion(s.app, "writer-a")
	if err != nil {
		t.Fatal(err)
	}
	f := &acceptanceFixture{sourcesAdapterFixture: s, objects: objects, adapter: adapter, catalog: catalog, work: w, store: store,
		grant: document.Grant{Execution: w.Execution, InputDigest: w.InputDigest, RecipeDigest: w.RecipeDigest, BudgetDigest: w.BudgetDigest}}
	f.grant.ExecutionID = registered.ID
	profile := []model.MetricRequirement{{Kind: model.MetricFidelity, Mandatory: true, Threshold: "qualification-required"}, {Kind: model.MetricGeometry, Mandatory: true, Threshold: "qualification-required"}}
	f.policy = formats.PolicyBundle{SchemaVersion: "policy-bundle-v1", Country: "vn", ThresholdVersion: "synthetic-v1", ArtifactProfile: profile, RollupProfile: profile, AcceptanceLimits: formats.DefaultLimits(), GapReasonCodes: []string{"CONVERSION_MISSING"}}
	policyBytes, policyDigest, err := formats.PolicyBundleCodec(formats.DefaultLimits()).Encode(f.policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.app.Exec(s.db.ctx, `INSERT INTO config.policy_bundle(digest,manifest) VALUES($1,$2)`, string(policyDigest), policyBytes); err != nil {
		t.Fatal(err)
	}
	recipe := f.register(t, []byte("synthetic converter recipe"))
	normalizer := f.register(t, acceptanceJSON(t, formats.BaselineNormalizer()))
	parser := f.register(t, acceptanceJSON(t, formats.Parser{SchemaVersion: "parser-v1", Parser: "none"}))
	f.input = formats.CandidateInput{SchemaVersion: "candidate-input-v1", Country: "vn", DocumentVersionKey: "version-a", ProcessingExecutionKey: registered.ExecutionKey,
		InputDigest: w.InputDigest, RecipeDigest: w.RecipeDigest, BudgetDigest: w.BudgetDigest, ObservationKey: plans[0].ParentObservation,
		Artifacts: []formats.CandidateArtifact{{ArtifactKey: artifact.ArtifactKey, ObservationOrdinal: artifact.Ordinal, Role: artifact.Role, Required: true, Capture: capture, Format: "html"}},
		Language:  "vi", HTMLSelector: "body", DetectorDigest: fixtureDigest, ConversionRecipe: catalogRef(recipe), NormalizerManifest: catalogRef(normalizer), ParserManifest: catalogRef(parser), GatePolicyDigest: policyDigest, ThresholdVersion: f.policy.ThresholdVersion, Mode: "first_pass"}
	inputBytes, candidateDigest, err := formats.CandidateInputCodec(formats.DefaultLimits()).Encode(f.input)
	if err != nil {
		t.Fatal(err)
	}
	f.register(t, inputBytes, capture.Asset, capture.TransportReceipt, catalogRef(recipe), catalogRef(normalizer), catalogRef(parser))
	metrics := make([]model.QualityMetric, 0, len(profile))
	for _, requirement := range profile {
		metrics = append(metrics, model.QualityMetric{Kind: requirement.Kind, State: model.MetricUnknown, Threshold: requirement.Threshold, Mandatory: true, ReasonCode: "BASELINE_UNQUALIFIED", Evidence: []model.AssetRef{}, Exclusions: []model.Span{}})
	}
	f.gate = formats.GateReport{SchemaVersion: "gate-report-v1", Artifact: model.QualityReport{ArtifactHash: capture.Asset.SHA256, CandidateDigest: candidateDigest, Report: formats.ReportSentinel(), PolicyHash: policyDigest, ThresholdVersion: f.policy.ThresholdVersion, Outcome: outcome, Metrics: metrics,
		Findings: []model.Finding{{ReasonCode: "CONVERSION_MISSING", Severity: model.SeverityBlocking, Scope: model.GapArtifact, ScopeKey: artifact.ArtifactKey, Spans: []model.Span{}, Evidence: []model.AssetRef{}}}}}
	gateCodec, err := formats.GateReportCodec(f.policy, formats.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	gateBytes, _, err := gateCodec.Encode(f.gate)
	if err != nil {
		t.Fatal(err)
	}
	gate := f.register(t, gateBytes)
	f.rollup = formats.GateRollup{SchemaVersion: "gate-rollup-v1", CandidateDigest: candidateDigest, PolicyHash: policyDigest, ThresholdVersion: f.policy.ThresholdVersion, Outcome: outcome,
		ArtifactReports: []formats.ArtifactReport{{Ordinal: 0, OriginalHash: capture.Asset.SHA256, Report: catalogRef(gate), Outcome: outcome}}, MissingOrdinals: []int{0}, Metrics: metrics, Findings: []model.Finding{},
		Gaps: []formats.Gap{{Ordinal: 0, Scope: model.GapArtifact, ScopeKey: artifact.ArtifactKey, ReasonCode: "CONVERSION_MISSING", Spans: []model.Span{}}}, Retained: []formats.Retained{{Ordinal: 0, Spans: []model.Span{}}}}
	rollupCodec, err := formats.GateRollupCodec(f.policy, formats.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	rollupBytes, _, err := rollupCodec.Encode(f.rollup)
	if err != nil {
		t.Fatal(err)
	}
	rollup := f.register(t, rollupBytes, catalogRef(gate))
	w.Substep, w.Shard = "evidence-quarantine", "vn:doc:00000000-0000-0000-0000-000000000001:rev:"+strings.Repeat("a", 64)
	f.request = run.IntakeApplyRequest{Work: w, IdempotencyKey: run.IntakeIdempotencyKey(w), OutputDigest: model.Digest(rollup.SHA256), Outcome: run.OutcomeRejected,
		Counts: run.Counts{Expected: 1, Processed: 1, Rejected: 1}, Acceptance: model.Quarantined, RevisionKey: w.Shard, CandidateDigest: candidateDigest, QualityReportHash: model.Digest(rollup.SHA256), OriginalHash: capture.Asset.SHA256}
	for path, data := range objects.data {
		f.request.Outputs = append(f.request.Outputs, model.AssetRef{Path: path, SHA256: formats.Hash(data), ByteSize: int64(len(data))})
	}
	sort.Slice(f.request.Outputs, func(i, j int) bool { return f.request.Outputs[i].SHA256 < f.request.Outputs[j].SHA256 })
	gateParent, err := formats.ProjectArtifactParent(f.gate, catalogRef(gate), f.policy, formats.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	f.persistReport(t, gate, gateParent, metrics, f.gate.Artifact.Findings, []formats.Gap{})
	rollupParent, err := formats.ProjectRollupParent(f.rollup, catalogRef(rollup), f.policy, formats.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	f.persistReport(t, rollup, rollupParent, metrics, f.rollup.Findings, f.rollup.Gaps)
	return f
}

func acceptanceAppConnection(t *testing.T, db sourceDatabaseFixture) *pgx.Conn {
	t.Helper()
	conn := boundedSourceAppConnection(t, db)
	if _, err := conn.Exec(db.ctx, "SET jit = off"); err != nil {
		t.Fatal(err)
	}
	return conn
}

// Record slow statements separately from setup so hosted failures distinguish
// query work from cloning, staging and lock barriers. The connection is serial.
func acceptanceTestStarter(t *testing.T, db TransactionStarter) TransactionStarter {
	t.Helper()
	var started time.Time
	return catalogProbeStarter{TransactionStarter: db,
		before: func(context.Context, string, []any) error {
			started = time.Now()
			return nil
		},
		after: func(_ context.Context, query string, _ []any, _ error) error {
			if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
				name, _, _ := strings.Cut(strings.TrimSpace(query), "\n")
				t.Logf("acceptance SQL %s: %s", name, elapsed)
			}
			return nil
		},
	}
}

func acceptanceSnapshot(t *testing.T, f *acceptanceFixture) map[string]string {
	t.Helper()
	rows := catalogSnapshot(t, f.sourcesAdapterFixture)
	for _, table := range []string{"ingest.acceptance_lock", "ingest.revision_acceptance_state", "ingest.revision_receipt", "ingest.evidence_gap"} {
		var data json.RawMessage
		if err := f.app.QueryRow(f.db.ctx, "SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.id),'[]'::jsonb) FROM "+table+" r").Scan(&data); err != nil {
			t.Fatal(err)
		}
		rows[table] = string(data)
	}
	return rows
}

// TestAcceptanceQuarantineAndReceiptReplay checks quarantining outcomes and immutable receipts.
func TestAcceptanceQuarantineAndReceiptReplay(t *testing.T) {
	for _, outcome := range []model.QualityOutcome{model.QualityQuarantined, model.QualityNeedsOCR} {
		t.Run(string(outcome), func(t *testing.T) {
			f := newAcceptanceFixture(t, outcome)
			first, err := f.adapter.Apply(f.db.ctx, f.request)
			if err != nil {
				t.Fatal(err)
			}
			before := acceptanceSnapshot(t, f)
			again, err := f.adapter.Apply(f.db.ctx, f.request)
			if err != nil || !reflect.DeepEqual(first, again) || !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
				t.Fatalf("response-loss replay changed receipt or rows: %v", err)
			}
			if f.count(t, `SELECT count(*) FROM ingest.revision_acceptance WHERE state='quarantined'`) != 1 || f.count(t, `SELECT count(*) FROM ingest.evidence_gap`) != 1 || f.count(t, `SELECT count(*) FROM silver.document_revision`) != 0 || f.count(t, `SELECT count(*) FROM ingest.delivery_record WHERE receipt_key=$1`, first.ResultID) != 1 {
				t.Fatal("quarantine commit set is incomplete or fabricated a revision")
			}
			if f.count(t, `SELECT count(*) FROM ingest.revision_acceptance_state WHERE state IN ('accepted','accepted_with_gaps')`) != 0 {
				t.Fatal("quarantine became eligible")
			}
		})
	}
}

// TestAcceptanceRejectionsLeaveEveryRowUnchanged checks rejection atomicity across all commit tables.
func TestAcceptanceRejectionsLeaveEveryRowUnchanged(t *testing.T) {
	for _, name := range []string{"epoch", "owner", "fence", "writer", "cancelled", "missing-output", "extra-output", "path", "physical-path", "size", "hash", "accepted-quarantine", "accepted-needs-ocr", "missing-gate", "missing-edge", "foreign-edge", "cancel-read"} {
		t.Run(name, func(t *testing.T) {
			outcome := model.QualityQuarantined
			if name == "accepted-needs-ocr" {
				outcome = model.QualityNeedsOCR
			}
			f := newAcceptanceFixture(t, outcome)
			if name == "accepted-quarantine" || name == "accepted-needs-ocr" {
				doc, maps, edits := attachAcceptanceConversion(t, f, "", "")
				completeAcceptancePrerequisites(t, f, doc, maps, edits)
			}
			r := f.request
			r.Outputs = append([]model.AssetRef(nil), r.Outputs...)
			switch name {
			case "epoch":
				r.Work.Execution.AdmissionEpoch++
			case "owner":
				r.Work.Execution.Owner = "owner-stale"
			case "fence":
				r.Work.Execution.AttemptFence++
			case "writer":
				f.adapter, _ = NewAcceptance(f.app, "writer-stale", f.objects, nil)
			case "cancelled":
				if _, err := f.app.Exec(f.db.ctx, `UPDATE ingest.execution_lease SET cancelled_at=clock_timestamp() WHERE resource_kind='document_version'`); err != nil {
					t.Fatal(err)
				}
			case "missing-output":
				r.Outputs = r.Outputs[1:]
			case "extra-output":
				r.Outputs = append(r.Outputs, model.AssetRef{Path: "extra", SHA256: model.Digest(strings.Repeat("f", 64)), ByteSize: 1})
			case "path":
				r.Outputs[0].Path = "wrong-path"
			case "physical-path":
				// The candidate root loads first. Keep the normalizer's embedded
				// logical path distinct while aliasing its catalog path to that root.
				other := f.input.NormalizerManifest.SHA256
				if _, err := f.db.conn.Exec(f.db.ctx, `ALTER TABLE bronze.blob DISABLE TRIGGER blob_immutable`); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.conn.Exec(f.db.ctx, `UPDATE bronze.blob SET object_ref=(SELECT object_ref FROM bronze.blob WHERE sha256=$1) WHERE sha256=$2`, string(r.CandidateDigest), string(other)); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.conn.Exec(f.db.ctx, `ALTER TABLE bronze.blob ENABLE TRIGGER blob_immutable`); err != nil {
					t.Fatal(err)
				}
			case "size":
				r.Outputs[0].ByteSize++
			case "hash":
				f.objects.data[r.Outputs[0].Path] = []byte("corrupt")
			case "accepted-quarantine", "accepted-needs-ocr":
				r.Acceptance, r.Complete, r.Outcome = model.Accepted, true, run.OutcomeSucceeded
				r.Work.Substep, r.Counts = "evidence-acceptance", run.Counts{Expected: 1, Processed: 1, Succeeded: 1}
			case "missing-gate":
				r.QualityReportHash = model.Digest(strings.Repeat("e", 64))
			case "missing-edge", "foreign-edge":
				if _, err := f.db.conn.Exec(f.db.ctx, `DELETE FROM bronze.blob_retention_reference WHERE owner_kind='execution' AND blob_id=(SELECT id FROM bronze.blob WHERE sha256=$1)`, string(r.CandidateDigest)); err != nil {
					t.Fatal(err)
				}
				if name == "foreign-edge" {
					if _, err := f.app.Exec(f.db.ctx, `INSERT INTO bronze.blob_retention_reference(blob_id,owner_kind,owner_key) SELECT b.id,'execution',e.execution_key FROM bronze.blob b CROSS JOIN ingest.execution e WHERE b.sha256=$1 AND e.resource_kind='source_window'`, string(r.CandidateDigest)); err != nil {
						t.Fatal(err)
					}
				}
			case "cancel-read":
				f.objects.beforeOpen = func(_ context.Context, _ string) error { return context.Canceled }
			}
			r.IdempotencyKey = run.IntakeIdempotencyKey(r.Work)
			before := acceptanceSnapshot(t, f)
			_, err := f.adapter.Apply(f.db.ctx, r)
			switch name {
			case "epoch", "owner", "fence", "writer", "cancelled":
				if !errors.Is(err, run.ErrStaleGrant) {
					t.Fatal("invalid grant lost its error class", err)
				}
			case "cancel-read":
				if !errors.Is(err, context.Canceled) {
					t.Fatal("cancelled object read lost its error class", err)
				}
			case "missing-gate":
				if !errors.Is(err, pgx.ErrNoRows) {
					t.Fatal("missing gate did not fail catalog lookup", err)
				}
			case "physical-path":
				requireAcceptanceCause(t, err, "physical path aliases different hashes")
			case "hash":
				requireAcceptanceCause(t, err, "retained bytes differ from catalog")
			case "missing-edge", "foreign-edge":
				requireAcceptanceCause(t, err, "closure lacks original execution protection")
			case "accepted-quarantine", "accepted-needs-ocr":
				requireAcceptanceCause(t, err, "report cannot support requested disposition")
			default:
				requireAcceptanceCause(t, err, "outputs differ from complete catalog closure")
			}
			if !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
				t.Fatal("rejection changed result, event, projection, gap, edge, delivery, or synchronization rows")
			}
		})
	}
}

// TestAcceptanceChangedReplayPrecedesObjectReads checks conflict classification before object access.
func TestAcceptanceChangedReplayPrecedesObjectReads(t *testing.T) {
	f := newAcceptanceFixture(t, model.QualityQuarantined)
	if _, err := f.adapter.Apply(f.db.ctx, f.request); err != nil {
		t.Fatal(err)
	}
	r := f.request
	r.OutputDigest = fixtureDigest
	f.objects.opened = nil
	before := acceptanceSnapshot(t, f)
	if _, err := f.adapter.Apply(f.db.ctx, r); !errors.Is(err, run.ErrPayloadConflict) {
		t.Fatalf("changed replay lost payload conflict: %v", err)
	}
	if len(f.objects.opened) != 0 || !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
		t.Fatal("changed replay read objects or changed rows")
	}
}

// TestAcceptanceCandidateToQuarantineAndHistoricalReplay checks distinct events and historical receipts.
func TestAcceptanceCandidateToQuarantineAndHistoricalReplay(t *testing.T) {
	f := newAcceptanceFixture(t, model.QualityQuarantined)
	candidate := f.request
	candidate.Work.Substep = "evidence-candidate"
	candidate.Acceptance, candidate.Outcome = model.Candidate, run.OutcomeSucceeded
	candidate.Counts = run.Counts{Expected: 1, Processed: 1, Succeeded: 1}
	candidate.IdempotencyKey = run.IntakeIdempotencyKey(candidate.Work)
	first, err := f.adapter.Apply(f.db.ctx, candidate)
	if err != nil {
		t.Fatal(err)
	}
	quarantine, err := f.adapter.Apply(f.db.ctx, f.request)
	if err != nil || first.ResultID == quarantine.ResultID {
		t.Fatalf("candidate-to-quarantine did not create distinct receipts: %v", err)
	}
	before := acceptanceSnapshot(t, f)
	again, err := f.adapter.Apply(f.db.ctx, candidate)
	if err != nil || !reflect.DeepEqual(first, again) || !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
		t.Fatalf("historical replay changed the projection or receipt: %s", acceptanceTestCause(err))
	}
}

// Test failures may display synthetic driver causes; production errors remain
// payload-free. Include joined causes so a fixture deadline cannot hide behind
// AcceptanceError's public message.
func acceptanceTestCause(err error) string {
	if err == nil {
		return "<nil>"
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		parts := []string{err.Error()}
		for _, child := range joined.Unwrap() {
			parts = append(parts, acceptanceTestCause(child))
		}
		return strings.Join(parts, ": ")
	}
	if cause := errors.Unwrap(err); cause != nil {
		return err.Error() + ": " + acceptanceTestCause(cause)
	}
	return err.Error()
}
