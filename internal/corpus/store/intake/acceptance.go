package intake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/blob"
	"danny.vn/banhmi/internal/corpus/conversion"
	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/quality/formats"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

// PermitCheck verifies current authority for exact retained decision bindings.
// Implementations are read-only, take no additional locks, and make no network calls.
type PermitCheck interface {
	// Check verifies current authority against the exact database decision.
	Check(context.Context, pgx.Tx, run.IntakeApplyRequest) error
}

// Acceptance validates retained evidence and atomically records its disposition.
// Object readers must satisfy Ledger's cancellable Read and Close contract.
type Acceptance struct {
	ledger  *Ledger
	permits PermitCheck
}

// NewAcceptance constructs the fenced adapter without performing I/O.
// A nil permit dependency permits only decision-free requests.
func NewAcceptance(db TransactionStarter, writer string, objects blob.Store, permits PermitCheck) (*Acceptance, error) {
	if objects == nil {
		return nil, fmt.Errorf("acceptance object store is required")
	}
	l, err := NewLedger(db, writer, objects, nil)
	if err != nil {
		return nil, err
	}
	return &Acceptance{ledger: l, permits: permits}, nil
}

// AcceptanceError preserves a typed cause without displaying evidence or driver text.
type AcceptanceError struct {
	// Cause retains the originating failure without exposing it in Error.
	Cause error
}

// Error returns a payload-free diagnostic.
func (*AcceptanceError) Error() string { return "acceptance rejected" }

// Unwrap preserves cancellation, stale grants, payload conflicts, and SQL causes.
func (e *AcceptanceError) Unwrap() error { return e.Cause }

func acceptanceShape(r run.IntakeApplyRequest) error {
	if err := r.Validate(); err != nil {
		return err
	}
	w, e := r.Work, r.Work.Execution
	if r.Acceptance == "" || len(r.RevisionKey) != 112 || w.Shard != r.RevisionKey || w.Stage != run.Normalize ||
		e.WorkflowKind != run.DocumentIngest || e.Scope.Kind != run.ScopeIntake || e.Scope.Subject.Kind != run.SubjectDocumentVersion || r.Cursor != "" || r.NextEligible != nil {
		return fmt.Errorf("acceptance requires revision-scoped document normalization")
	}
	substep, outcome, complete := "evidence-acceptance", run.OutcomeSucceeded, true
	counts := run.Counts{Expected: 1, Processed: 1}
	switch r.Acceptance {
	case model.Candidate:
		substep, complete, counts.Succeeded = "evidence-candidate", false, 1
	case model.Quarantined:
		substep, outcome, complete, counts.Rejected = "evidence-quarantine", run.OutcomeRejected, false, 1
	case model.Accepted:
		counts.Succeeded = 1
		if r.Decision != nil && r.Decision.Action != model.DecisionAcceptOCR {
			return fmt.Errorf("decision action cannot authorize gap-free acceptance")
		}
	case model.AcceptedWithGaps, model.Excluded:
		counts.TerminalGap, counts.AcceptedGaps = 1, 1
		if r.Acceptance == model.Excluded {
			outcome = run.OutcomeTerminalGap
		}
	}
	if w.Substep != substep || r.Outcome != outcome || r.Complete != complete || r.Counts != counts ||
		((r.Acceptance == model.Candidate || r.Acceptance == model.Quarantined) && r.Decision != nil) {
		return fmt.Errorf("acceptance disposition and accounting disagree")
	}
	for i, ref := range r.Outputs {
		if i > 0 && r.Outputs[i-1].SHA256 >= ref.SHA256 {
			return fmt.Errorf("outputs must be unique and hash ordered")
		}
	}
	return nil
}

func acceptanceGrant(ctx context.Context, tx pgx.Tx, r run.IntakeApplyRequest) error {
	now, err := lockLease(ctx, tx, r.Work.Execution)
	if err != nil {
		return err
	}
	return r.ValidateAt(now)
}

// Apply validates retained evidence and commits atomically, including on replay.
func (a *Acceptance) Apply(ctx context.Context, r run.IntakeApplyRequest) (run.IntakeReceipt, error) {
	if err := acceptanceShape(r); err != nil {
		return run.IntakeReceipt{}, &AcceptanceError{Cause: err}
	}
	ctx, cancel := context.WithTimeout(ctx, r.Work.Budget.MaxElapsed)
	defer cancel()
	for attempt, started := 0, time.Now(); attempt < 3; attempt++ {
		receipt, err := ledgerTransaction(ctx, a.ledger, func(tx pgx.Tx, q intakedb.Querier) (receipt run.IntakeReceipt, cause error) {
			leaseHeld := false
			defer func() {
				if leaseHeld && cause != nil && !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) && ctx.Err() == nil && tx.Conn() != nil && tx.Conn().PgConn().TxStatus() == 'T' {
					if err := acceptanceGrant(ctx, tx, r); err != nil {
						cause = err
					}
				}
				if cause != nil {
					cause = &AcceptanceError{Cause: cause}
				}
			}()
			if err := a.ledger.lockAdmission(ctx, tx, r.Work.Execution); err != nil {
				return receipt, err
			}
			if err := acceptanceGrant(ctx, tx, r); err != nil {
				return receipt, err
			}
			leaseHeld = true
			executionID, _, err := resolveExecution(ctx, tx, r.Work)
			if err != nil {
				return receipt, err
			}
			stored, err := q.FindExecutionResult(ctx, r.IdempotencyKey)
			replay := err == nil
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return receipt, err
			}
			for _, identity := range []struct{ revision, attempt string }{{strings.Split(r.RevisionKey, ":rev:")[0], "identity"}, {r.RevisionKey, r.Work.Execution.AttemptKey}} {
				if !replay {
					if err := q.ProvisionAcceptanceLock(ctx, intakedb.ProvisionAcceptanceLockParams{RevisionKey: identity.revision, ProcessingAttemptKey: identity.attempt}); err != nil {
						return receipt, err
					}
				}
				if _, err := q.LockAcceptanceKey(ctx, intakedb.LockAcceptanceKeyParams{RevisionKey: identity.revision, ProcessingAttemptKey: identity.attempt}); err != nil {
					return receipt, err
				}
			}
			prior, priorErr := q.ReadAcceptanceAttempt(ctx, intakedb.ReadAcceptanceAttemptParams{RevisionKey: r.RevisionKey, ProcessingAttemptKey: r.Work.Execution.AttemptKey})
			if priorErr != nil && !errors.Is(priorErr, pgx.ErrNoRows) {
				return receipt, priorErr
			}
			if priorErr == nil && !prior.Consistent {
				return receipt, fmt.Errorf("acceptance projection is inconsistent")
			}
			if err := acceptanceGrant(ctx, tx, r); err != nil {
				return receipt, err
			}
			if r.Decision != nil || r.Comparison != nil && r.Comparison.Adjudication != nil {
				if a.permits == nil {
					return receipt, fmt.Errorf("decision authority is required")
				}
				if _, err := resolveDecision(ctx, tx, executionID, r.Decision); err != nil {
					return receipt, err
				}
				if r.Comparison != nil {
					if _, err := resolveDecision(ctx, tx, executionID, r.Comparison.Adjudication); err != nil {
						return receipt, err
					}
				}
				if err := a.permits.Check(ctx, tx, r); err != nil {
					return receipt, err
				}
			}
			stored, err = q.FindExecutionResult(ctx, r.IdempotencyKey)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return receipt, err
			}
			if err == nil && !replay {
				return receipt, errCatalogRediscover
			}
			if err := acceptanceGrant(ctx, tx, r); err != nil {
				return receipt, err
			}
			if replay {
				if err := classifyPayload(stored.Payload, r); err != nil {
					return receipt, err
				}
			} else if priorErr == nil && (model.AcceptanceState(prior.State).BuildEligible() || prior.State == string(model.Excluded) || prior.CandidateDigest != string(r.CandidateDigest) || r.Acceptance == model.Candidate || prior.State == string(model.Quarantined) && r.Acceptance.BuildEligible() && r.Decision == nil) {
				return receipt, fmt.Errorf("acceptance transition is not permitted")
			}
			check := newAcceptanceEvidence(a, tx, q, r, started)
			gaps, err := check.validate(ctx, stored, replay)
			if err != nil {
				return receipt, err
			}
			if !replay {
				p, err := resultParams(r, a.ledger.writer, executionID)
				if err != nil {
					return receipt, err
				}
				_, insertErr := q.InsertExecutionResult(ctx, p)
				if insertErr != nil && !errors.Is(insertErr, pgx.ErrNoRows) {
					return receipt, insertErr
				}
				stored, err = q.FindExecutionResult(ctx, r.IdempotencyKey)
				if err != nil {
					return receipt, err
				}
				if err := acceptanceGrant(ctx, tx, r); err != nil {
					return receipt, err
				}
				if err := classifyPayload(stored.Payload, r); err != nil {
					return receipt, err
				}
				if stored.ResultKey != p.ResultKey {
					return receipt, errCatalogRediscover
				}
				if err := recordAcceptance(ctx, tx, q, r, stored, gaps); err != nil {
					return receipt, err
				}
				if r.Acceptance.BuildEligible() {
					for _, ref := range check.catalog {
						if _, err := q.AddBlobRetentionReference(ctx, intakedb.AddBlobRetentionReferenceParams{ID: check.ids[ref.SHA256], OwnerKind: "document_revision", OwnerKey: r.RevisionKey}); err != nil {
							return receipt, err
						}
					}
				}
				if _, err := insertDelivery(ctx, q, intakedb.InsertDeliveryRecordParams{DeduplicationKey: stored.ResultKey, DeliveryKind: "completion", RecipientWorkflowID: r.Work.Execution.WorkflowID, ReceiptKind: "result", ReceiptKey: stored.ResultKey, ExpectedEpoch: r.Work.Execution.AdmissionEpoch, ExpectedHash: string(r.OutputDigest), DueAt: stored.CreatedAt}); err != nil {
					return receipt, err
				}
			}
			if err := check.commitSet(ctx, stored, gaps, !replay); err != nil {
				return receipt, err
			}
			if r.Decision != nil {
				if err := a.permits.Check(ctx, tx, r); err != nil {
					return receipt, err
				}
			}
			receipt, err = receiptFromResult(ctx, tx, stored)
			if err != nil {
				return run.IntakeReceipt{}, err
			}
			return receipt, errors.Join(check.bound(ctx, 0, 0), acceptanceGrant(ctx, tx, r))
		})
		if !catalogRetryOnly(err) || ctx.Err() != nil {
			if err != nil {
				return run.IntakeReceipt{}, &AcceptanceError{Cause: err}
			}
			return receipt, nil
		}
	}
	return run.IntakeReceipt{}, &AcceptanceError{Cause: errCatalogRediscover}
}

type acceptanceEvidence struct {
	a          *Acceptance
	tx         pgx.Tx
	q          intakedb.Querier
	r          run.IntakeApplyRequest
	limits     formats.Limits
	started    time.Time
	bytes      int64
	jsonBytes  int64
	rows       int64
	data       map[model.Digest][]byte
	refs       map[model.Digest]model.AssetRef
	ids        map[model.Digest]int64
	paths      map[string]model.Digest
	evidence   []byte
	catalog    []model.AssetRef
	input      formats.CandidateInput
	output     *formats.CandidateOutput
	rollup     formats.GateRollup
	policy     formats.PolicyBundle
	reports    []formats.GateReport
	normalizer formats.Normalizer
}

func newAcceptanceEvidence(a *Acceptance, tx pgx.Tx, q intakedb.Querier, r run.IntakeApplyRequest, started time.Time) *acceptanceEvidence {
	b := r.Work.Budget
	return &acceptanceEvidence{a: a, tx: tx, q: q, r: r, started: started,
		limits: formats.Limits{MaxBlobs: min(1024, int64(b.MaxItems)), MaxRows: int64(b.MaxItems), MaxJSONBytes: b.MaxInputBytes, MaxObjectBytes: min(b.MaxInputBytes, math.MaxInt64-1), MaxElapsedMS: max(1, b.MaxElapsed.Milliseconds())},
		data: make(map[model.Digest][]byte), refs: make(map[model.Digest]model.AssetRef), ids: make(map[model.Digest]int64), paths: make(map[string]model.Digest)}
}

func (v *acceptanceEvidence) bound(ctx context.Context, rows, jsonBytes int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if rows > v.limits.MaxRows-v.rows || jsonBytes > v.limits.MaxJSONBytes-v.jsonBytes || time.Since(v.started) > v.r.Work.Budget.MaxElapsed || time.Since(v.started).Milliseconds() > v.limits.MaxElapsedMS {
		return fmt.Errorf("acceptance work exceeds pinned bounds")
	}
	v.rows, v.jsonBytes = v.rows+rows, v.jsonBytes+jsonBytes
	return nil
}

func acceptanceDecode(data []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	canonical, err := json.Marshal(dst)
	if err != nil || !bytes.Equal(canonical, data) {
		return fmt.Errorf("retained JSON is not canonical")
	}
	return nil
}

func (v *acceptanceEvidence) root(ctx context.Context, digest model.Digest) ([]byte, error) {
	if data, ok := v.data[digest]; ok {
		return data, nil
	}
	b, err := v.q.ReadSourceBlob(ctx, string(digest))
	if err != nil {
		return nil, err
	}
	return v.load(ctx, model.AssetRef{Path: b.ObjectRef, SHA256: digest, ByteSize: b.ByteSize}, b)
}

func (v *acceptanceEvidence) load(ctx context.Context, logical model.AssetRef, registered ...intakedb.BronzeBlob) ([]byte, error) {
	if err := logical.Validate(); err != nil {
		return nil, err
	}
	if prior, ok := v.paths[logical.Path]; ok && prior != logical.SHA256 {
		return nil, fmt.Errorf("logical path aliases different hashes")
	}
	v.paths[logical.Path] = logical.SHA256
	if prior, ok := v.refs[logical.SHA256]; ok {
		if prior.ByteSize != logical.ByteSize {
			return nil, fmt.Errorf("logical aliases disagree on size")
		}
		return v.data[logical.SHA256], nil
	}
	if int64(len(v.refs)) >= v.limits.MaxBlobs || logical.ByteSize > v.limits.MaxObjectBytes-v.bytes {
		return nil, fmt.Errorf("acceptance closure exceeds blob or byte bound")
	}
	b, err := func() (intakedb.BronzeBlob, error) {
		if len(registered) != 0 {
			return registered[0], nil
		}
		return v.q.ReadSourceBlob(ctx, string(logical.SHA256))
	}()
	if err != nil || b.ByteSize != logical.ByteSize || b.Sha256 != string(logical.SHA256) {
		return nil, errors.Join(err, fmt.Errorf("catalog reference mismatch"))
	}
	ref := model.AssetRef{Path: b.ObjectRef, SHA256: logical.SHA256, ByteSize: b.ByteSize}
	jsonBytes := int64(0)
	if strings.Contains(b.MediaType, "json") {
		jsonBytes = ref.ByteSize
	}
	if err := v.bound(ctx, 1, jsonBytes); err != nil {
		return nil, err
	}
	for _, prior := range v.refs {
		if prior.Path == ref.Path && prior.SHA256 != ref.SHA256 {
			return nil, fmt.Errorf("physical path aliases different hashes")
		}
	}
	reader, err := v.a.ledger.objects.Open(ctx, ref.Path)
	if err != nil {
		return nil, err
	}
	var once sync.Once
	var closeErr error
	closeReader := func() { once.Do(func() { closeErr = reader.Close() }) }
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			closeReader()
		case <-stop:
		}
	}()
	data, err := io.ReadAll(io.LimitReader(stagedContextReader{ctx: ctx, reader: reader}, ref.ByteSize+1))
	close(stop)
	closeReader()
	if err != nil || closeErr != nil || ctx.Err() != nil || int64(len(data)) != ref.ByteSize || formats.Hash(data) != ref.SHA256 {
		return nil, errors.Join(err, closeErr, ctx.Err(), fmt.Errorf("retained bytes differ from catalog"))
	}
	v.refs[ref.SHA256], v.ids[ref.SHA256], v.data[ref.SHA256] = ref, b.ID, data
	v.bytes += ref.ByteSize
	return data, v.bound(ctx, 0, 0)
}

func (v *acceptanceEvidence) dependencies(ctx context.Context, value any) error {
	var spans []model.Span
	var walk func(reflect.Value) error
	walk = func(x reflect.Value) error {
		if x.Kind() == reflect.Pointer {
			if x.IsNil() {
				return nil
			}
			return walk(x.Elem())
		}
		if x.CanInterface() {
			if span, ok := x.Interface().(model.Span); ok {
				spans = append(spans, span)
			}
			if ref, ok := x.Interface().(model.AssetRef); ok {
				if ref == formats.ReportSentinel() {
					return nil
				}
				_, err := v.load(ctx, ref)
				return err
			}
		}
		switch x.Kind() {
		case reflect.Struct:
			for i := 0; i < x.NumField(); i++ {
				if err := walk(x.Field(i)); err != nil {
					return err
				}
			}
		case reflect.Slice, reflect.Array:
			if err := v.bound(ctx, int64(x.Len()), 0); err != nil {
				return err
			}
			for i := 0; i < x.Len(); i++ {
				if err := walk(x.Index(i)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(reflect.ValueOf(value)); err != nil {
		return err
	}
	for _, span := range spans {
		if _, err := formats.ProjectReportSpan(span, v.refs[span.ArtifactHash]); err != nil {
			return err
		}
	}
	return nil
}

func (v *acceptanceEvidence) validate(ctx context.Context, stored intakedb.IngestExecutionResult, replay bool) ([]model.GapRef, error) {
	r := v.r
	data, err := v.root(ctx, r.CandidateDigest)
	if err != nil {
		return nil, err
	}
	v.input, _, err = formats.CandidateInputCodec(v.limits).Decode(data)
	if err != nil {
		return nil, err
	}
	i := v.input
	if i.Country != r.Work.Execution.Country || i.DocumentVersionKey != r.Work.Execution.Scope.Subject.LeaseKey() || i.InputDigest != r.Work.InputDigest || i.RecipeDigest != r.Work.RecipeDigest || i.BudgetDigest != r.Work.BudgetDigest || i.Mode != "first_pass" || r.Comparison != nil {
		return nil, fmt.Errorf("candidate execution binding or comparison contract is unavailable")
	}
	origin, err := v.q.ReadAcceptanceOrigin(ctx, i.ProcessingExecutionKey)
	if err != nil {
		return nil, err
	}
	var budget run.Budget
	if json.Unmarshal(origin.Budget, &budget) != nil || budget != r.Work.Budget || origin.WorkflowID != r.Work.Execution.WorkflowID || origin.ProcessingAttemptKey != r.Work.Execution.AttemptKey || origin.WorkflowKind != string(run.DocumentIngest) || origin.ScopeKind != string(run.ScopeIntake) || origin.ResourceKind != "document_version" || origin.ResourceKey != i.DocumentVersionKey || origin.InputDigest != string(i.InputDigest) || origin.RecipeDigest != string(i.RecipeDigest) || origin.BudgetDigest != string(i.BudgetDigest) {
		return nil, fmt.Errorf("candidate origin differs from registered work")
	}
	p, err := v.q.FindPolicyBundle(ctx, string(i.GatePolicyDigest))
	if err != nil {
		return nil, err
	}
	policy, _, digest, err := formats.PolicyFromJSONB(p.Manifest, v.limits)
	if err != nil || digest != i.GatePolicyDigest || policy.Country != i.Country || policy.ThresholdVersion != i.ThresholdVersion {
		return nil, errors.Join(err, fmt.Errorf("candidate policy binding mismatch"))
	}
	v.policy = policy
	limits := policy.AcceptanceLimits
	v.limits = formats.Limits{MaxBlobs: min(v.limits.MaxBlobs, limits.MaxBlobs), MaxRows: min(v.limits.MaxRows, limits.MaxRows), MaxJSONBytes: min(v.limits.MaxJSONBytes, limits.MaxJSONBytes), MaxObjectBytes: min(v.limits.MaxObjectBytes, limits.MaxObjectBytes), MaxElapsedMS: min(v.limits.MaxElapsedMS, limits.MaxElapsedMS)}
	ctx, cancel := context.WithDeadline(ctx, v.started.Add(min(v.r.Work.Budget.MaxElapsed, time.Duration(min(v.limits.MaxElapsedMS, math.MaxInt64/int64(time.Millisecond)))*time.Millisecond)))
	defer cancel()
	if int64(len(v.refs)) > v.limits.MaxBlobs || v.bytes > v.limits.MaxObjectBytes {
		return nil, fmt.Errorf("candidate exceeds selected policy bounds")
	}
	if err := v.bound(ctx, 1, int64(len(p.Manifest))); err != nil {
		return nil, err
	}
	if err := v.dependencies(ctx, i); err != nil {
		return nil, err
	}
	v.normalizer, _, err = formats.NormalizerCodec(v.limits).Decode(v.data[i.NormalizerManifest.SHA256])
	if err != nil {
		return nil, err
	}
	if _, _, err := formats.ParserCodec(v.limits).Decode(v.data[i.ParserManifest.SHA256]); err != nil {
		return nil, err
	}
	data, err = v.root(ctx, r.QualityReportHash)
	if err != nil {
		return nil, err
	}
	rollupCodec, err := formats.GateRollupCodec(policy, v.limits)
	if err != nil {
		return nil, err
	}
	v.rollup, _, err = rollupCodec.Decode(data)
	if err != nil {
		return nil, err
	}
	if err := v.dependencies(ctx, v.rollup); err != nil {
		return nil, err
	}
	if v.rollup.OutputManifest != nil {
		output, _, err := formats.CandidateOutputCodec(v.limits).Decode(v.data[v.rollup.OutputManifest.SHA256])
		if err != nil {
			return nil, err
		}
		v.output = &output
		if r.OutputDigest != v.rollup.OutputManifest.SHA256 {
			return nil, fmt.Errorf("output manifest digest mismatch")
		}
		if err := v.dependencies(ctx, output); err != nil {
			return nil, err
		}
	} else if r.OutputDigest != r.QualityReportHash {
		return nil, fmt.Errorf("incomplete output must name its roll-up")
	}
	artifactCodec, err := formats.GateReportCodec(policy, v.limits)
	if err != nil {
		return nil, err
	}
	for _, selected := range v.rollup.ArtifactReports {
		report, _, err := artifactCodec.Decode(v.data[selected.Report.SHA256])
		if err != nil {
			return nil, err
		}
		if err := v.dependencies(ctx, report); err != nil {
			return nil, err
		}
		v.reports = append(v.reports, report)
	}
	if err := formats.ValidateRollupBinding(i, v.output, v.reports, v.rollup, policy, v.limits); err != nil {
		return nil, err
	}
	if r.Acceptance == model.Quarantined && v.rollup.Outcome != model.QualityQuarantined && v.rollup.Outcome != model.QualityNeedsOCR || r.Acceptance.BuildEligible() && string(v.rollup.Outcome) != string(r.Acceptance) {
		return nil, fmt.Errorf("report cannot support requested disposition")
	}
	before := int64(math.MaxInt64)
	if replay {
		before = stored.ID
	}
	baseline, baselineErr := v.q.ReadAcceptanceBaseline(ctx, intakedb.ReadAcceptanceBaselineParams{IdentityKey: strings.Split(r.RevisionKey, ":rev:")[0], BeforeWatermark: before})
	if baselineErr != nil && !errors.Is(baselineErr, pgx.ErrNoRows) {
		return nil, baselineErr
	}
	if r.Acceptance.BuildEligible() && baselineErr == nil && baseline.CandidateDigest != string(r.CandidateDigest) {
		return nil, fmt.Errorf("comparison-dependent eligibility is unavailable")
	}
	gaps, err := v.scopedGaps(ctx)
	if err != nil {
		return nil, err
	}
	if err := v.captureAndConversion(ctx); err != nil {
		return nil, err
	}
	for _, ref := range v.refs {
		v.catalog = append(v.catalog, ref)
	}
	sort.Slice(v.catalog, func(i, j int) bool { return v.catalog[i].SHA256 < v.catalog[j].SHA256 })
	if !reflect.DeepEqual(v.catalog, r.Outputs) {
		return nil, fmt.Errorf("outputs differ from complete catalog closure")
	}
	for _, ref := range v.catalog {
		id, err := v.q.LockCatalogBlob(ctx, string(ref.SHA256))
		if err != nil || id != v.ids[ref.SHA256] {
			return nil, errors.Join(err, fmt.Errorf("locked blob differs from discovered closure"))
		}
		if err := acceptanceGrant(ctx, v.tx, r); err != nil {
			return nil, err
		}
	}
	protected, err := v.q.AcceptanceProtectionMatches(ctx, v.evidence)
	if err != nil || !protected {
		return nil, errors.Join(err, fmt.Errorf("closure lacks original execution protection"))
	}
	if err := v.a.ledger.verifyOutputs(ctx, v.catalog); err != nil {
		return nil, err
	}
	return gaps, acceptanceGrant(ctx, v.tx, r)
}

func (v *acceptanceEvidence) captureAndConversion(ctx context.Context) error {
	requests := make([]conversion.ConversionManifest, len(v.reports))
	documents := make([]model.ConvertedDocument, len(v.reports))
	manifests := make([][]model.AssetRef, len(v.reports))
	maps := make([][]model.OffsetMapSegment, len(v.reports))
	edits := make([][]model.EditLedgerEntry, len(v.reports))
	for k, report := range v.reports {
		if report.Document == nil {
			continue
		}
		doc := &documents[k]
		if err := errors.Join(acceptanceDecode(v.data[report.RequestManifest.SHA256], &requests[k]), acceptanceDecode(v.data[report.Document.SHA256], doc), acceptanceDecode(v.data[report.OutputManifest.SHA256], &manifests[k])); err != nil {
			return err
		}
		if err := errors.Join(doc.Validate(v.r.Work.Budget.MaxItems), v.dependencies(ctx, manifests[k]), v.dependencies(ctx, *doc)); err != nil {
			return err
		}
		for _, ref := range manifests[k] {
			if ref.Path != "results/"+string(report.RequestManifest.SHA256)+"/quality.json" {
				continue
			}
			var observation struct {
				SchemaVersion   string
				ArtifactHash    model.Digest
				CandidateDigest model.Digest
				RequestDigest   model.Digest
				RecipeHash      model.Digest
				DetectorHash    model.Digest
				DocumentHash    model.Digest
				Outcome         model.QualityOutcome
				Metrics         []model.QualityMetric
				Findings        []model.Finding
			}
			if err := errors.Join(acceptanceDecode(v.data[ref.SHA256], &observation), v.dependencies(ctx, &observation)); err != nil {
				return err
			}
		}
		if err := errors.Join(acceptanceDecode(v.data[doc.OffsetMap.SHA256], &maps[k]), acceptanceDecode(v.data[doc.EditLedger.SHA256], &edits[k])); err != nil {
			return err
		}
		if err := errors.Join(v.dependencies(ctx, maps[k]), v.dependencies(ctx, edits[k]), v.blockBytes(*doc)); err != nil {
			return err
		}
		for _, entry := range edits[k] {
			var decoded model.DecodedEdit
			if err := acceptanceDecode(v.data[entry.Evidence.SHA256], &decoded); err != nil {
				return err
			}
		}
		if err := formats.ValidateNormalizationBinding(v.normalizer, maps[k], edits[k], v.limits); err != nil {
			return err
		}
	}
	refs := make([]model.AssetRef, 0, len(v.refs))
	for _, ref := range v.refs {
		refs = append(refs, ref)
	}
	parents := make([]formats.QualityParent, 0, len(v.reports)+1)
	children := make([][]formats.QualityRow, 0, len(v.reports)+1)
	for k, report := range v.reports {
		parent, err := formats.ProjectArtifactParent(report, v.refs[v.rollup.ArtifactReports[k].Report.SHA256], v.policy, v.limits)
		if err != nil {
			return err
		}
		rows, err := formats.ProjectQualityRows(report.Artifact.Metrics, report.Artifact.Findings, []formats.Gap{}, refs, v.limits)
		if err != nil {
			return err
		}
		parents, children = append(parents, parent), append(children, rows)
	}
	parent, err := formats.ProjectRollupParent(v.rollup, v.refs[v.r.QualityReportHash], v.policy, v.limits)
	if err != nil {
		return err
	}
	rows, err := formats.ProjectQualityRows(v.rollup.Metrics, v.rollup.Findings, v.rollup.Gaps, refs, v.limits)
	if err != nil {
		return err
	}
	parents, children = append(parents, parent), append(children, rows)
	// This local SQL projection is not a retained format.
	payload, err := json.Marshal(map[string]any{
		"Request": v.r, "Input": v.input, "Output": v.output,
		"Rollup": v.rollup, "Reports": v.reports, "Requests": requests,
		"Documents": documents, "Manifests": manifests,
		"Maps": maps, "Edits": edits,
		"Parents": parents, "Children": children,
		"Contents": v.data, "Catalog": refs,
		"RemainingRows": v.limits.MaxRows - v.rows,
	})
	if err != nil {
		return err
	}
	valid, err := v.q.AcceptanceEvidenceMatches(ctx, payload)
	if err != nil || !valid {
		return errors.Join(err, fmt.Errorf("retained evidence differs from its deterministic projection"))
	}
	v.evidence = payload
	return v.bound(ctx, 0, 0)
}

func (v *acceptanceEvidence) scopedGaps(ctx context.Context) ([]model.GapRef, error) {
	gaps := make([]model.GapRef, 0, len(v.rollup.Gaps))
	seen := make(map[model.GapRef]bool)
	for _, gap := range v.rollup.Gaps {
		if gap.Scope == model.GapDocumentRevision {
			if gap.ScopeKey != v.r.RevisionKey {
				return nil, fmt.Errorf("gap names another revision")
			}
		} else if model.ValidateKey(gap.ScopeKey) != nil {
			return nil, fmt.Errorf("gap key has invalid grammar")
		}
		ref := model.GapRef{Kind: gap.Scope, Key: gap.ScopeKey, ReasonCode: gap.ReasonCode, ReportHash: v.r.QualityReportHash}
		if err := ref.Validate(); err != nil || seen[ref] {
			return nil, errors.Join(err, fmt.Errorf("invalid or duplicated scoped gap"))
		}
		seen[ref], gaps = true, append(gaps, ref)
	}
	if v.r.Decision != nil {
		if v.r.Acceptance == model.AcceptedWithGaps && v.rollup.DecisionRef != v.r.Decision.DecisionKey {
			return nil, fmt.Errorf("roll-up names another decision")
		}
		data, err := v.root(ctx, v.r.Decision.ScopeManifest)
		if err != nil {
			return nil, err
		}
		scope, _, err := formats.GapScopeCodec(v.limits).Decode(data)
		if err != nil {
			return nil, err
		}
		if err := formats.ValidateScopeBinding(scope, v.rollup, v.r.OriginalHash, v.limits); err != nil {
			return nil, err
		}
		if v.r.Acceptance == model.Excluded {
			for _, retained := range scope.Retained {
				if len(retained.Spans) != 0 {
					return nil, fmt.Errorf("exclusion cannot retain text")
				}
			}
		}
	} else if v.r.Acceptance == model.AcceptedWithGaps || v.r.Acceptance == model.Excluded {
		return nil, fmt.Errorf("exact gap authority is required")
	}
	if (v.r.Acceptance == model.AcceptedWithGaps || v.r.Acceptance == model.Excluded) && len(gaps) == 0 {
		return nil, fmt.Errorf("gap disposition requires explicit coverage gaps")
	}
	return gaps, nil
}

func (v *acceptanceEvidence) commitSet(ctx context.Context, result intakedb.IngestExecutionResult, gaps []model.GapRef, fresh bool) error {
	events, err := v.q.ReadAcceptanceEvent(ctx, result.ID)
	if err != nil {
		return err
	}
	if len(events) != 1 {
		return fmt.Errorf("result must have exactly one acceptance event")
	}
	e := events[0]
	var recorded []model.GapRef
	if json.Unmarshal(e.ScopedGaps, &recorded) != nil || !reflect.DeepEqual(recorded, gaps) || e.RevisionKey != v.r.RevisionKey || e.ProcessingAttemptKey != v.r.Work.Execution.AttemptKey || e.ExecutionID != result.ExecutionID || e.ExecutionResultID != result.ID || e.Watermark != result.ID || e.CandidateDigest != string(v.r.CandidateDigest) || e.QualityReportHash != string(v.r.QualityReportHash) || e.State != string(v.r.Acceptance) || e.DecisionRevision != v.r.Work.DecisionRevision {
		return fmt.Errorf("acceptance event differs from immutable result")
	}
	count, err := v.q.CountAcceptanceReceipts(ctx, intakedb.CountAcceptanceReceiptsParams{RevisionKey: v.r.RevisionKey, ExecutionResultID: result.ID})
	if err != nil || count != 1 {
		return errors.Join(err, fmt.Errorf("revision receipt is missing or duplicated"))
	}
	projection, err := v.q.ReadAcceptanceAttempt(ctx, intakedb.ReadAcceptanceAttemptParams{RevisionKey: v.r.RevisionKey, ProcessingAttemptKey: v.r.Work.Execution.AttemptKey})
	if err != nil || !projection.Consistent || fresh && (projection.AcceptanceID != e.ID || projection.Watermark != e.Watermark || projection.State != e.State) {
		return errors.Join(err, fmt.Errorf("acceptance projection is inconsistent"))
	}
	valid, err := v.q.AcceptanceCommitSetMatches(ctx, intakedb.AcceptanceCommitSetMatchesParams{AcceptanceID: e.ID, ResultKey: result.ResultKey, RevisionKey: v.r.RevisionKey, ScopedGaps: e.ScopedGaps, Eligible: v.r.Acceptance.BuildEligible()})
	if err != nil || !valid {
		return errors.Join(err, fmt.Errorf("acceptance commit set is incomplete"))
	}
	return nil
}

func (v *acceptanceEvidence) blockBytes(doc model.ConvertedDocument) error {
	var raw, normalized []byte
	type rawBlock struct {
		ID      string
		Kind    model.BlockKind
		Text    string
		Sources []model.Span
	}
	var rawBlocks []rawBlock
	for k, block := range doc.Blocks {
		mapped, _, err := (conversion.TextNormalizer{}).Normalize(model.TextInput{Text: block.RawText, Sources: block.SourceSpans, Limits: model.RuleLimits{MaxItems: v.r.Work.Budget.MaxItems, MaxBytes: v.limits.MaxObjectBytes}})
		if err != nil {
			return err
		}
		text := mapped.Text
		identity, _ := json.Marshal([]string{"block-v1", string(block.Kind), block.RawText, text})
		if block.Ordinal != k || block.ID != fmt.Sprintf("block-%06d-%s", k, formats.Hash(identity)) || block.ParentID != "" || block.Authority != model.AuthorityUnknown || block.Content != nil || block.Findings != nil || block.Language != v.input.Language || !bytes.Equal(v.data[block.NormalizedText.SHA256], []byte(text)) {
			return fmt.Errorf("block differs from canonical conversion")
		}
		raw, normalized = append(raw, []byte(block.RawText)...), append(normalized, []byte(text)...)
		rawBlocks = append(rawBlocks, rawBlock{block.ID, block.Kind, block.RawText, block.SourceSpans})
	}
	rawJSON, _ := json.Marshal(rawBlocks)
	normalJSON, _ := json.Marshal(doc.Blocks)
	if !bytes.Equal(raw, v.data[doc.RawText.SHA256]) || !bytes.Equal(normalized, v.data[doc.NormalizedText.SHA256]) || !bytes.Equal(rawJSON, v.data[doc.RawBlocks.SHA256]) || !bytes.Equal(normalJSON, v.data[doc.NormalizedBlocks.SHA256]) {
		return fmt.Errorf("aggregate conversion bytes differ from blocks")
	}
	return nil
}
