package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"

	"danny.vn/banhmi/internal/corpus/model"
)

// IntakeWork pins processing inputs independently of mutable scheduler ownership.
type IntakeWork struct {
	Execution        ScopedExecution
	Stage            Stage
	Substep          string
	Shard            string
	InputDigest      model.Digest
	RecipeDigest     model.Digest
	BudgetDigest     model.Digest
	Budget           Budget
	DecisionRevision int64
}

// Validate checks execution scope, immutable processing hashes and frozen budget.
func (w IntakeWork) Validate() error {
	if err := w.Execution.Validate(); err != nil {
		return fmt.Errorf("execution: %w", err)
	}
	if w.Execution.Scope.Kind == ScopeBuild {
		return fmt.Errorf("intake work cannot use build scope")
	}
	if !w.Stage.Valid() || w.Substep == "" || w.Shard == "" || w.DecisionRevision < 0 {
		return fmt.Errorf("valid stage, substep, shard and nonnegative decision revision are required")
	}
	for name, d := range map[string]model.Digest{"input digest": w.InputDigest, "recipe digest": w.RecipeDigest, "budget digest": w.BudgetDigest} {
		if err := d.Validate(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if err := w.Budget.Validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	return nil
}

// IntakeIdempotencyKey excludes owner and attempt fence, but pins every work input.
// BudgetDigest is the effective policy manifest hash, not a hash of a locator.
func IntakeIdempotencyKey(w IntakeWork) string {
	h := sha256.New()
	e := w.Execution
	for _, v := range []string{"corpus-intake-result-v1", e.Country, e.WorkflowID, string(e.WorkflowKind), e.AttemptKey,
		string(e.Scope.Kind), string(e.Scope.Subject.Kind), e.Scope.Subject.LeaseKey(), string(e.Scope.BatchCollection), uuid.UUID(e.Scope.BuildID).String(),
		string(e.Scope.CountryControl), string(w.Stage), w.Substep, w.Shard, string(w.InputDigest), string(w.RecipeDigest), string(w.BudgetDigest)} {
		writeCanonicalString(h, v)
	}
	for _, v := range []int64{e.AdmissionEpoch, w.DecisionRevision, int64(w.Budget.MaxItems), w.Budget.MaxInputBytes,
		int64(w.Budget.MaxPages), int64(w.Budget.MaxElapsed), int64(w.Budget.MaxConcurrent)} {
		writeCanonicalUint64(h, uint64(v))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// IntakeOutcome names the closed set of intake execution results.
type IntakeOutcome string

const (
	// OutcomeSucceeded records successful intake work.
	OutcomeSucceeded IntakeOutcome = "succeeded"
	// OutcomePendingRetry records work awaiting retry eligibility.
	OutcomePendingRetry IntakeOutcome = "pending_retry"
	// OutcomeTerminalGap records a terminal coverage gap.
	OutcomeTerminalGap IntakeOutcome = "terminal_gap"
	// OutcomeRejected records rejected intake work.
	OutcomeRejected IntakeOutcome = "rejected"
	// OutcomeFailed records failed intake work.
	OutcomeFailed IntakeOutcome = "failed"
)

// Valid reports membership in the closed contract vocabulary.
func (o IntakeOutcome) Valid() bool {
	return o == OutcomeSucceeded || o == OutcomePendingRetry || o == OutcomeTerminalGap || o == OutcomeRejected || o == OutcomeFailed
}

// IntakeApplyRequest is a Vietnam-local transaction value, never a transport envelope.
// Outputs are staged references; all metadata and cursor bytes remain local.
type IntakeApplyRequest struct {
	Work              IntakeWork
	IdempotencyKey    string
	Outputs           []model.AssetRef
	OutputDigest      model.Digest
	Outcome           IntakeOutcome
	Counts            Counts
	Cursor            string
	NextEligible      *time.Time
	Acceptance        model.AcceptanceState
	RevisionKey       string
	CandidateDigest   model.CandidateDigest
	QualityReportHash model.Digest
	OriginalHash      model.Digest
	Decision          *DecisionBinding
	Comparison        *ComparisonBinding
	GapAcceptanceRef  string
	Complete          bool
}

// Validate checks local payload accounting and exact decision/comparison bindings.
func (r IntakeApplyRequest) Validate() error {
	if err := r.Work.Validate(); err != nil {
		return fmt.Errorf("work: %w", err)
	}
	if r.IdempotencyKey != IntakeIdempotencyKey(r.Work) {
		return fmt.Errorf("idempotency key does not match immutable work")
	}
	if err := r.OutputDigest.Validate(); err != nil {
		return fmt.Errorf("output digest: %w", err)
	}
	if !r.Outcome.Valid() || (r.Acceptance != "" && !r.Acceptance.Valid()) {
		return fmt.Errorf("unknown outcome or acceptance state")
	}
	if r.Acceptance != "" {
		if err := model.ValidateRevisionKey(r.RevisionKey, r.Work.Execution.Country); err != nil {
			return fmt.Errorf("ledger key: %w", err)
		}
		for name, d := range map[string]model.Digest{"candidate digest": r.CandidateDigest, "quality report hash": r.QualityReportHash, "original hash": r.OriginalHash} {
			if err := d.Validate(); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	} else if r.RevisionKey != "" || r.CandidateDigest != "" || r.QualityReportHash != "" || r.OriginalHash != "" || r.Decision != nil || r.Comparison != nil {
		return fmt.Errorf("revision provenance requires acceptance state")
	}
	if r.Decision != nil {
		if err := r.Decision.Validate(r); err != nil {
			return fmt.Errorf("decision: %w", err)
		}
	}
	if r.Acceptance == model.AcceptedWithGaps || r.Acceptance == model.Excluded || r.GapAcceptanceRef != "" || r.Work.DecisionRevision > 0 {
		if r.Decision == nil {
			return fmt.Errorf("decision authority and binding are required")
		}
	}
	if r.GapAcceptanceRef != "" && (r.Decision == nil || r.GapAcceptanceRef != r.Decision.DecisionKey) {
		return fmt.Errorf("gap acceptance must name the bound decision")
	}
	if r.Acceptance == model.AcceptedWithGaps && r.Decision.Action != model.DecisionAcceptGaps || r.Acceptance == model.Excluded && r.Decision.Action != model.DecisionExclude {
		return fmt.Errorf("decision action does not authorize acceptance state")
	}
	if r.Comparison != nil {
		if err := r.Comparison.Validate(r); err != nil {
			return fmt.Errorf("comparison: %w", err)
		}
	}

	if err := r.Counts.Validate(); err != nil {
		return fmt.Errorf("counts: %w", err)
	}
	if r.Counts.Expected > r.Work.Budget.MaxItems {
		return fmt.Errorf("expected items exceed budget")
	}
	if (r.Counts.PendingRetry > 0) != (r.NextEligible != nil) || (r.Counts.AcceptedGaps > 0) != (r.GapAcceptanceRef != "") {
		return fmt.Errorf("retry time and gap acceptance must match counts")
	}
	if r.Complete && (r.Counts.Processed != r.Counts.Expected || r.Counts.PendingRetry != 0 || r.Counts.Rejected != 0 || r.Counts.Failed != 0 || r.Counts.TerminalGap != r.Counts.AcceptedGaps) {
		return fmt.Errorf("complete result must close all input with success or accepted gaps")
	}
	seen := make(map[string]bool)
	for _, output := range r.Outputs {
		if err := output.Validate(); err != nil {
			return fmt.Errorf("output: %w", err)
		}
		if seen[output.Path] {
			return fmt.Errorf("duplicate output reference")
		}
		seen[output.Path] = true
	}
	return nil
}

// IntakeReceipt is the append-only execution_result in neutral form. Result
// retains hashes, recipe, budget, outcome, counts, cursor and retry eligibility.
// ExecutionID and ResultID are ledger keys; RunReceipt is transport provenance.
type IntakeReceipt struct {
	ExecutionID string
	ResultID    string
	RunReceipt  string
	Result      IntakeApplyRequest
}

// Validate checks receipt keys and the complete local transaction payload.
func (r IntakeReceipt) Validate() error {
	if r.ExecutionID == "" || r.ResultID == "" {
		return fmt.Errorf("execution and result receipt keys are required")
	}
	if err := r.Result.Validate(); err != nil {
		return fmt.Errorf("result: %w", err)
	}
	return nil
}

// IntakeUnitOfWork checks all grants in one transaction, including on replay:
// current country admission epoch; execution identity and exact scope/subject;
// live lease resource, owner, fence and expiry; cancellation; input, recipe and
// frozen budget digests; and decision revision. The lease resource is the intake
// subject's kind/key, batch collection or country control, never a build lease.
// It verifies staged bytes and hashes, original/candidate/report/output bindings,
// trusted mandatory quality gates, decision actor authority and action, expiry,
// current fence and epoch, revision and exact gap scope. The adapter resolves
// decision and comparison records rather than trusting caller assertions.
// Reprocessing acceptance requires the exact prior revision and candidate,
// a passing comparison bound to both revisions and reports, and owner
// adjudication for critical changes. Reuse never skips these checks.
// It commits references, acceptance, receipt and delivery records atomically.
// A stale grant returns ErrStaleGrant and writes nothing, even on replay.
// A changed immutable payload under an existing key returns ErrPayloadConflict
// and writes nothing. Only an identical replay under a current grant returns
// the stored receipt. Grant liveness and expiry are checked against database time.
type IntakeUnitOfWork interface {
	// Apply validates all transactional grants and commits atomically or returns a typed error.
	Apply(context.Context, IntakeApplyRequest) (IntakeReceipt, error)
}

// SameImmutableIntakePayload compares all immutable work, outcome and output fields; owner and execution fence may change.
func SameImmutableIntakePayload(left, right IntakeApplyRequest) bool {
	if !equalOptionalTime(left.NextEligible, right.NextEligible) {
		return false
	}
	left.NextEligible, right.NextEligible = nil, nil
	left.Work.Execution.Owner, right.Work.Execution.Owner = "", ""
	left.Work.Execution.AttemptFence, right.Work.Execution.AttemptFence = 0, 0
	return reflect.DeepEqual(left, right)
}

// DecisionBinding carries keys and hashes to resolve an authoritative local decision.
// Actor authority, expiry at database time and exact retained/excluded gap ranges
// are checked from the ledger record inside Apply, never from this value alone.
type DecisionBinding struct {
	DecisionKey      string
	ActorKey         string
	Action           model.DecisionKind
	ExpiresAt        time.Time
	AttemptFence     int64
	AdmissionEpoch   int64
	DecisionRevision int64
	Scope            ExecutionScope
	RevisionKey      string
	CandidateDigest  model.Digest
	ReportHash       model.Digest
	OriginalHash     model.Digest
	OutputDigest     model.Digest
	ScopeManifest    model.Digest
}

// Validate checks the syntactic and exact-attempt bindings of a decision.
func (d DecisionBinding) Validate(r IntakeApplyRequest) error {
	if err := model.ValidateRevisionKey(d.RevisionKey, r.Work.Execution.Country); err != nil {
		return fmt.Errorf("revision key: %w", err)
	}
	for name, key := range map[string]string{"decision key": d.DecisionKey, "actor key": d.ActorKey} {
		if err := model.ValidateKey(key); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if !d.Action.Valid() || d.ExpiresAt.IsZero() || d.DecisionRevision <= 0 {
		return fmt.Errorf("decision requires action, expiry and positive revision")
	}
	if err := d.Scope.Validate(); err != nil {
		return fmt.Errorf("decision execution scope: %w", err)
	}
	for name, digest := range map[string]model.Digest{"scope manifest": d.ScopeManifest, "candidate digest": d.CandidateDigest, "report hash": d.ReportHash, "original hash": d.OriginalHash, "output digest": d.OutputDigest} {
		if err := digest.Validate(); err != nil {
			return fmt.Errorf("decision %s: %w", name, err)
		}
	}
	if d.AttemptFence != r.Work.Execution.AttemptFence || d.AdmissionEpoch != r.Work.Execution.AdmissionEpoch || d.DecisionRevision != r.Work.DecisionRevision || d.Scope != r.Work.Execution.Scope || d.RevisionKey != r.RevisionKey || d.CandidateDigest != r.CandidateDigest || d.ReportHash != r.QualityReportHash || d.OriginalHash != r.OriginalHash || d.OutputDigest != r.OutputDigest {
		return fmt.Errorf("decision does not bind exact attempt, scope and hashes")
	}
	return nil
}

// ComparisonBinding references a prior revision comparison and required adjudication.
// The transaction derives critical-change requirements from the trusted comparison.
type ComparisonBinding struct {
	PriorRevisionKey     string
	PriorCandidateDigest model.Digest
	ComparisonHash       model.Digest
	RequiresAdjudication bool
	Adjudication         *DecisionBinding
}

// Validate checks comparison identity and any declared adjudication binding.
func (c ComparisonBinding) Validate(r IntakeApplyRequest) error {
	if err := model.ValidateRevisionKey(c.PriorRevisionKey, r.Work.Execution.Country); err != nil {
		return fmt.Errorf("prior revision: %w", err)
	}
	for name, digest := range map[string]model.Digest{"prior candidate": c.PriorCandidateDigest, "comparison hash": c.ComparisonHash} {
		if err := digest.Validate(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if c.RequiresAdjudication && c.Adjudication == nil {
		return fmt.Errorf("critical changes require owner adjudication")
	}
	if c.Adjudication != nil {
		if err := c.Adjudication.Validate(r); err != nil {
			return fmt.Errorf("adjudication: %w", err)
		}
	}
	return nil
}

// PayloadConflictError identifies a changed immutable result under an existing key.
type PayloadConflictError struct{}

// Error returns a payload-free diagnostic.
func (*PayloadConflictError) Error() string {
	return "immutable intake payload conflict"
}

// StaleGrantError identifies an obsolete or expired admission, lease or decision grant.
type StaleGrantError struct{}

// Error returns a payload-free diagnostic.
func (*StaleGrantError) Error() string {
	return "stale intake grant"
}

// ErrPayloadConflict is the error adapters return for a conflicting replay.
var ErrPayloadConflict = &PayloadConflictError{}

// ErrStaleGrant is the error adapters return for a stale grant, including on replay.
var ErrStaleGrant = &StaleGrantError{}

// ValidateIntakeReplay checks an existing receipt after transactional grant checks.
// currentGrant must be derived inside the adapter's transaction, never by callers.
func ValidateIntakeReplay(stored, proposed IntakeApplyRequest, currentGrant bool) error {
	if !currentGrant {
		return ErrStaleGrant
	}
	if !SameImmutableIntakePayload(stored, proposed) {
		return ErrPayloadConflict
	}
	if err := proposed.Validate(); err != nil {
		return fmt.Errorf("replayed request: %w", err)
	}
	return nil
}

// IntakeWorkEnvelope carries only a ledger key and a screened Vietnam-local manifest digest.
// Work, staged metadata, source cursors and transport receipts stay in that manifest.
type IntakeWorkEnvelope struct {
	Key      string
	Manifest model.Digest
}

// Validate checks the transport projection's grammar and digest.
func (e IntakeWorkEnvelope) Validate() error {
	if err := model.ValidateEnvelope(e.Key, "execution", e.Manifest); err != nil {
		return fmt.Errorf("execution envelope: %w", err)
	}
	return nil
}

// MarshalJSON validates the projection before serialization.
func (e IntakeWorkEnvelope) MarshalJSON() ([]byte, error) {
	encoded, err := model.MarshalEnvelope(e.Key, "execution", e.Manifest)
	if err != nil {
		return nil, fmt.Errorf("serialize execution envelope: %w", err)
	}
	return encoded, nil
}

// IntakeReceiptEnvelope carries only a ledger key and a screened Vietnam-local manifest digest.
// Work, staged metadata, source cursors and transport receipts stay in that manifest.
type IntakeReceiptEnvelope struct {
	Key      string
	Manifest model.Digest
}

// Validate checks the transport projection's grammar and digest.
func (e IntakeReceiptEnvelope) Validate() error {
	if err := model.ValidateEnvelope(e.Key, "result", e.Manifest); err != nil {
		return fmt.Errorf("result envelope: %w", err)
	}
	return nil
}

// MarshalJSON validates the projection before serialization.
func (e IntakeReceiptEnvelope) MarshalJSON() ([]byte, error) {
	encoded, err := model.MarshalEnvelope(e.Key, "result", e.Manifest)
	if err != nil {
		return nil, fmt.Errorf("serialize result envelope: %w", err)
	}
	return encoded, nil
}

// ValidateAt checks syntax and decision expiry against a transaction-supplied time.
// Apply must still resolve authority, current grants and immutable ledger bindings.
func (r IntakeApplyRequest) ValidateAt(now time.Time) error {
	if err := r.Validate(); err != nil {
		return fmt.Errorf("intake request: %w", err)
	}
	if now.IsZero() {
		return fmt.Errorf("transaction time is required")
	}
	if r.Decision != nil && !now.Before(r.Decision.ExpiresAt) {
		return fmt.Errorf("decision expiry: %w", ErrStaleGrant)
	}
	if r.Comparison != nil && r.Comparison.Adjudication != nil && !now.Before(r.Comparison.Adjudication.ExpiresAt) {
		return fmt.Errorf("adjudication expiry: %w", ErrStaleGrant)
	}
	return nil
}
