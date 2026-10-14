package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"

	"danny.vn/banhmi/internal/corpus/blob"
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

func (w IntakeWork) Validate() error {
	if err := w.Execution.Validate(); err != nil {
		return err
	}
	if !w.Stage.Valid() || w.Substep == "" || w.Shard == "" || w.DecisionRevision < 0 {
		return fmt.Errorf("valid stage, substep, shard and nonnegative decision revision are required")
	}
	for _, d := range []model.Digest{w.InputDigest, w.RecipeDigest, w.BudgetDigest} {
		if err := d.Validate(); err != nil {
			return err
		}
	}
	return w.Budget.Validate()
}

// IntakeIdempotencyKey excludes owner and attempt fence, but pins every work input.
// BudgetDigest is the effective policy manifest hash, not a hash of a locator.
func IntakeIdempotencyKey(w IntakeWork) string {
	h := sha256.New()
	e := w.Execution
	for _, v := range []string{"corpus-intake-result-v1", e.Country, e.WorkflowID, e.WorkflowKind, e.AttemptKey,
		string(e.Scope.Kind), string(e.Scope.DocumentVersion), string(e.Scope.BatchCollection), uuid.UUID(e.Scope.BuildID).String(),
		string(e.Scope.CountryControl), string(w.Stage), w.Substep, w.Shard, string(w.InputDigest), string(w.RecipeDigest), string(w.BudgetDigest)} {
		writeCanonicalString(h, v)
	}
	for _, v := range []int64{e.AdmissionEpoch, w.DecisionRevision, int64(w.Budget.MaxItems), w.Budget.MaxInputBytes,
		int64(w.Budget.MaxPages), int64(w.Budget.MaxElapsed), int64(w.Budget.MaxConcurrent)} {
		writeCanonicalUint64(h, uint64(v))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

type IntakeOutcome string

const (
	OutcomeSucceeded    IntakeOutcome = "succeeded"
	OutcomePendingRetry IntakeOutcome = "pending_retry"
	OutcomeTerminalGap  IntakeOutcome = "terminal_gap"
	OutcomeRejected     IntakeOutcome = "rejected"
	OutcomeFailed       IntakeOutcome = "failed"
)

func (o IntakeOutcome) Valid() bool {
	return o == OutcomeSucceeded || o == OutcomePendingRetry || o == OutcomeTerminalGap || o == OutcomeRejected || o == OutcomeFailed
}

// IntakeApplyRequest contains reference-only outputs staged before the transaction.
type IntakeApplyRequest struct {
	Work             IntakeWork
	IdempotencyKey   string
	Outputs          []blob.Metadata
	OutputDigest     model.Digest
	Outcome          IntakeOutcome
	Counts           Counts
	Cursor           string
	NextEligible     *time.Time
	Acceptance       model.AcceptanceState
	RevisionKey      string
	CandidateDigest  model.CandidateDigest
	QualityReportHash model.Digest
	GapAcceptanceRef string
	Complete         bool
}

func (r IntakeApplyRequest) Validate() error {
	if err := r.Work.Validate(); err != nil {
		return err
	}
	if r.IdempotencyKey != IntakeIdempotencyKey(r.Work) {
		return fmt.Errorf("idempotency key does not match immutable work")
	}
	if err := r.OutputDigest.Validate(); err != nil {
		return err
	}
	if !r.Outcome.Valid() || (r.Acceptance != "" && !r.Acceptance.Valid()) {
		return fmt.Errorf("unknown outcome or acceptance state")
	}
	if r.Acceptance != "" {
		if err := model.ValidateKey(r.RevisionKey); err != nil {
			return err
		}
		for _, d := range []model.Digest{r.CandidateDigest, r.QualityReportHash} {
			if err := d.Validate(); err != nil {
				return err
			}
		}
	} else if r.RevisionKey != "" || r.CandidateDigest != "" || r.QualityReportHash != "" {
		return fmt.Errorf("revision provenance requires acceptance state")
	}
	if err := r.Counts.Validate(); err != nil {
		return err
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
			return err
		}
		if seen[output.ObjectRef] {
			return fmt.Errorf("duplicate output reference")
		}
		seen[output.ObjectRef] = true
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

func (r IntakeReceipt) Validate() error {
	if r.ExecutionID == "" || r.ResultID == "" {
		return fmt.Errorf("execution and result receipt keys are required")
	}
	return r.Result.Validate()
}

// IntakeUnitOfWork checks admission epoch, execution/scope, live lease owner and
// fence, cancellation, input/recipe/budget digests and decision revision in one
// transaction, then commits references, acceptance, receipt and delivery records.
// It verifies staged object bytes/hashes, candidate/report binding, mandatory
// gates, scoped gap decisions and any required comparison/adjudication.
// An identical immutable replay with a current grant returns the stored receipt;
// a stale grant or changed payload writes nothing, including on replay.
type IntakeUnitOfWork interface {
	Apply(context.Context, IntakeApplyRequest) (IntakeReceipt, error)
}

func SameImmutableIntakePayload(left, right IntakeApplyRequest) bool {
	if !equalOptionalTime(left.NextEligible, right.NextEligible) {
		return false
	}
	left.NextEligible, right.NextEligible = nil, nil
	left.Work.Execution.Owner, right.Work.Execution.Owner = "", ""
	left.Work.Execution.AttemptFence, right.Work.Execution.AttemptFence = 0, 0
	return reflect.DeepEqual(left, right)
}
