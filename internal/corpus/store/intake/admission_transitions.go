package intake

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

// Bind moves a reserved slot to processing or rebinds existing occupancy.
func (a *DocumentAdmission) Bind(ctx context.Context, r run.AdmissionChange) (run.AdmissionReceipt, error) {
	result, err := a.change(ctx, r, "bind", "", "", nil)
	return admissionReceiptValue(result), err
}

// Settle accounts for the latest committed acceptance in its own transaction.
func (a *DocumentAdmission) Settle(ctx context.Context, r run.AdmissionChange) (run.AdmissionReceipt, error) {
	result, err := a.change(ctx, r, "settle", "", "", nil)
	return admissionReceiptValue(result), err
}

// RequestResume records durable demand under the live bound document grant.
// M1.7 verifies the durable wake-up; no wake-up assertion is stored here.
func (a *DocumentAdmission) RequestResume(ctx context.Context, r run.AdmissionChange) (run.AdmissionReceipt, error) {
	result, err := a.change(ctx, r, "request_resume", "", "", nil)
	return admissionReceiptValue(result), err
}

// Resume competes for processing capacity without adding open occupancy.
func (a *DocumentAdmission) Resume(ctx context.Context, r run.AdmissionChange) (run.AdmissionResult, error) {
	return a.change(ctx, r, "resume", "", "", nil)
}

// Close persists an authorized stop before any transport drain can run.
func (a *DocumentAdmission) Close(ctx context.Context, r run.AdmissionCloseRequest) (run.AdmissionReceipt, error) {
	if err := r.Validate(); err != nil {
		return run.AdmissionReceipt{}, err
	}
	result, err := a.change(ctx, r.Change, "close", r.Reason, r.CauseReceipt, nil)
	return admissionReceiptValue(result), err
}

// Reconcile settles evidence first, then records one worker-loss, failure or
// closure transition. Trusted observation occurs outside the mutation transaction.
func (a *DocumentAdmission) Reconcile(ctx context.Context, r run.AdmissionChange) (run.AdmissionReceipt, error) {
	if err := r.Validate(); err != nil {
		return run.AdmissionReceipt{}, err
	}
	if r.Actor.Execution.WorkflowKind != run.ControlReconcile {
		return run.AdmissionReceipt{}, run.ErrInvalidAdmission
	}
	type preflight struct {
		slot    intakedb.IngestDocumentAdmissionSlot
		receipt *admissionStoredResponse
	}
	before, err := ledgerTransaction(ctx, a.ledger, func(tx pgx.Tx, q intakedb.Querier) (preflight, error) {
		s, err := q.ReadDocumentAdmissionSlot(ctx, r.SlotKey)
		if err != nil {
			return preflight{}, err
		}
		_, err = a.grants(ctx, tx, q, r.Actor, s.DocumentVersionKey, true)
		if err != nil {
			return preflight{}, err
		}
		p := admissionPayload{Method: "reconcile", Country: r.Actor.Execution.Country, Target: r.SlotKey, Revision: r.ExpectedRevision}
		receipt, err := admissionReplay(ctx, q, p)
		if err != nil {
			return preflight{}, err
		}
		allowCancelled := receipt == nil || receipt.Operation != "worker_loss"
		return preflight{slot: s, receipt: receipt}, a.checkActor(ctx, q, r.Actor, allowCancelled)
	})
	if err != nil {
		return run.AdmissionReceipt{}, err
	}
	if before.receipt != nil {
		return before.receipt.Receipt, nil
	}
	observation, err := a.observer.Observe(ctx, before.slot.WorkflowID)
	if err != nil {
		return run.AdmissionReceipt{}, err
	}
	if observation.WorkflowID != before.slot.WorkflowID || !observation.Status.Valid() || observation.Status == run.DocumentExecutionTerminal && observation.ChainReceipt == "" {
		return run.AdmissionReceipt{}, run.ErrAdmissionIntegrity
	}
	observed := admissionObserved{slot: before.slot, observation: observation}
	result, err := a.change(ctx, r, "reconcile", "", "", &observed)
	return admissionReceiptValue(result), err
}

type admissionObserved struct {
	slot        intakedb.IngestDocumentAdmissionSlot
	observation run.DocumentExecutionObservation
}

func admissionReceiptValue(r run.AdmissionResult) run.AdmissionReceipt {
	if r.Receipt == nil {
		return run.AdmissionReceipt{}
	}
	return *r.Receipt
}

func (a *DocumentAdmission) change(ctx context.Context, r run.AdmissionChange, method string, reason run.AdmissionTerminalReason, cause string, observed *admissionObserved) (run.AdmissionResult, error) {
	if err := r.Validate(); err != nil {
		return run.AdmissionResult{}, err
	}
	kind := r.Actor.Execution.WorkflowKind
	if (method == "bind" || method == "request_resume" || method == "resume") && kind != run.DocumentIngest ||
		kind != run.DocumentIngest && kind != run.ControlReconcile {
		return run.AdmissionResult{}, run.ErrInvalidAdmission
	}
	return ledgerTransaction(ctx, a.ledger, func(tx pgx.Tx, q intakedb.Querier) (run.AdmissionResult, error) {
		var zero run.AdmissionResult
		discovered, err := q.ReadDocumentAdmissionSlot(ctx, r.SlotKey)
		if err != nil {
			return zero, admissionConflict(err)
		}
		reconciliation := method == "settle" || method == "close" || method == "reconcile"
		g, err := a.grants(ctx, tx, q, r.Actor, discovered.DocumentVersionKey, reconciliation)
		if err != nil {
			return zero, err
		}
		if kind == run.DocumentIngest && (r.Actor.Execution.WorkflowID != discovered.WorkflowID ||
			r.Actor.Execution.AttemptKey != discovered.ProcessingAttemptKey || admissionResourceKey(r.Actor) != discovered.DocumentVersionKey) {
			return zero, run.ErrStaleGrant
		}
		if kind == run.DocumentIngest {
			origin, err := q.ReadAdmissionExecution(ctx, discovered.OriginExecutionID)
			if err != nil {
				return zero, err
			}
			budget, err := json.Marshal(r.Actor.Budget)
			if err != nil || origin.InputDigest != string(r.Actor.InputDigest) || origin.RecipeDigest != string(r.Actor.RecipeDigest) ||
				origin.BudgetDigest != string(r.Actor.BudgetDigest) || !sameSourceJSON(origin.Budget, budget) {
				return zero, errors.Join(run.ErrAdmissionIntegrity, err)
			}
		}
		// Abandonment needs delivery acknowledgement serialized before capacity.
		if method == "close" && kind == run.ControlReconcile && reason == run.AdmissionAbandoned {
			d, err := q.ReadAdmissionDelivery(ctx, discovered.DeliveryID)
			if err != nil {
				return zero, err
			}
			if _, err := q.LockAdmissionDelivery(ctx, d.DeduplicationKey); err != nil {
				return zero, err
			}
		}
		l, err := q.LockDocumentAdmissionLimit(ctx)
		if err != nil {
			return zero, err
		}
		if err := a.checkActor(ctx, q, r.Actor, reconciliation); err != nil {
			return zero, err
		}
		s, err := q.LockDocumentAdmissionSlot(ctx, r.SlotKey)
		if err != nil {
			return zero, admissionConflict(err)
		}
		if err := a.checkActor(ctx, q, r.Actor, reconciliation); err != nil {
			return zero, err
		}
		p := admissionPayload{Method: method, Country: r.Actor.Execution.Country, Target: r.SlotKey,
			Revision: r.ExpectedRevision, Reason: reason, Cause: cause}
		stored, err := admissionReplay(ctx, q, p)
		if err != nil {
			return zero, err
		}
		if stored != nil {
			return admissionResult(*stored), a.checkActor(ctx, q, r.Actor, reconciliation && stored.Operation != "worker_loss")
		}
		if s.Revision != r.ExpectedRevision || s.State == "released" || observed != nil &&
			(s.Revision != observed.slot.Revision || admissionBinding(s) != admissionBinding(observed.slot) || s.WorkflowID != observed.slot.WorkflowID) {
			return zero, run.ErrAdmissionConflict
		}
		if err := admissionConfigured(l); err != nil {
			return zero, err
		}
		now, err := q.AdmissionDatabaseTime(ctx)
		if err != nil {
			return zero, err
		}
		response := admissionStoredResponse{PriorState: s.State}
		next := s
		bound := admissionBinding(s) == admissionWorkBinding(r.Actor, g.actorID)
		if kind == run.DocumentIngest && method != "bind" && !bound {
			return zero, run.ErrStaleGrant
		}
		contested := false
		switch method {
		case "bind":
			if s.State != "reserved" && s.State != "processing" && s.State != "open_only" {
				return zero, run.ErrAdmissionConflict
			}
			if bound && s.State != "reserved" {
				effect, err := admissionEffect(ctx, q, s, "bind")
				if err != nil {
					return zero, err
				}
				return admissionResult(effect), a.checkActor(ctx, q, r.Actor, false)
			}
			next.CurrentExecutionID, next.AdmissionEpoch, next.AttemptFence, next.LeaseOwner = g.actorID, r.Actor.Execution.AdmissionEpoch, r.Actor.Execution.AttemptFence, r.Actor.Execution.Owner
			if s.State == "reserved" {
				next.State = "processing"
			}
		case "settle":
			event, result, err := admissionAcceptance(ctx, q, s)
			if err != nil {
				return zero, err
			}
			if event != nil && event.State != "candidate" && event.Watermark == s.SettledWatermark {
				effect, err := admissionEffect(ctx, q, s, "settle")
				if err != nil {
					return zero, err
				}
				return admissionResult(effect), a.checkActor(ctx, q, r.Actor, reconciliation)
			}
			if event == nil || event.State == "candidate" || event.Watermark < s.SettledWatermark {
				return zero, run.ErrAdmissionUnchanged
			}
			if err := admissionSettle(&next, *event, result); err != nil {
				return zero, err
			}
		case "request_resume", "resume":
			if s.State != "open_only" {
				return zero, run.ErrAdmissionConflict
			}
			if err := admissionQuarantineTime(ctx, q, s, now, false); err != nil {
				return zero, err
			}
			if method == "request_resume" {
				if s.ResumeRequestedAt != nil {
					effect, err := admissionEffect(ctx, q, s, "request_resume")
					if err != nil {
						return zero, err
					}
					return admissionResult(effect), a.checkActor(ctx, q, r.Actor, false)
				}
				next.ResumeRequestedAt = &now
			} else {
				if s.ResumeRequestedAt == nil {
					return zero, run.ErrAdmissionConflict
				}
				counts, err := q.CountDocumentAdmissionSlots(ctx)
				if err != nil {
					return zero, err
				}
				refused := run.AdmissionResult{LimitsRevision: l.Revision, Processing: counts.Processing, Open: counts.Open}
				heads, err := q.ReadAdmissionQueueHeads(ctx)
				if err != nil {
					return zero, err
				}
				if counts.Processing >= int64(*l.ProcessingLimit) {
					refused.Refusal = run.AdmissionProcessingFull
					admissionWaiting(ctx, r.Actor.Execution.Country, refused.Refusal, heads)
					return refused, a.checkActor(ctx, q, r.Actor, false)
				}
				contested = heads.Launch != "" && counts.Open < int64(*l.OpenLimit)
				if heads.Resume != s.SlotKey || contested && l.NextQueue != "resume" {
					refused.Refusal = run.AdmissionNotTurn
					return refused, a.checkActor(ctx, q, r.Actor, false)
				}
				next.State, next.WaitReason, next.ResumeRequestedAt = "processing", nil, nil
			}
		case "close":
			if err := a.closeSlot(ctx, q, r, s, &next, g.targetLease, reason, cause, now, &response); err != nil {
				return zero, err
			}
		case "reconcile":
			response.Observation = observed.observation
			if err := a.reconcileSlot(ctx, q, s, &next, g.targetLease, r.Actor.Execution.AdmissionEpoch, now, &response); err != nil {
				return zero, err
			}
		}
		next, err = admissionUpdate(ctx, q, s, next)
		if err != nil {
			return zero, err
		}
		counts, err := q.CountDocumentAdmissionSlots(ctx)
		if err != nil {
			return zero, err
		}
		response.Processing, response.Open = counts.Processing, counts.Open
		response, err = admissionWriteReceipt(ctx, q, l, p, admissionWorkBinding(r.Actor, g.actorID), &next, response)
		if err != nil {
			return zero, err
		}
		if contested {
			if _, err := q.AdvanceAdmissionQueueTurn(ctx, "resume"); err != nil {
				return zero, err
			}
		}
		return admissionResult(response), a.checkActor(ctx, q, r.Actor, reconciliation && response.Operation != "worker_loss")
	})
}

func admissionUpdate(ctx context.Context, q intakedb.Querier, old, next intakedb.IngestDocumentAdmissionSlot) (intakedb.IngestDocumentAdmissionSlot, error) {
	return q.UpdateDocumentAdmissionSlot(ctx, intakedb.UpdateDocumentAdmissionSlotParams{
		ID: old.ID, ExpectedRevision: old.Revision, ExpectedState: old.State, State: next.State,
		CurrentExecutionID: next.CurrentExecutionID, AdmissionEpoch: next.AdmissionEpoch, LeaseOwner: next.LeaseOwner,
		AttemptFence: next.AttemptFence, SettledWatermark: next.SettledWatermark, WaitReason: next.WaitReason,
		ResumeRequestedAt: next.ResumeRequestedAt, TerminalReason: next.TerminalReason, TerminalResultID: next.TerminalResultID})
}

func admissionConflict(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return run.ErrAdmissionConflict
	}
	return err
}

func admissionAcceptance(ctx context.Context, q intakedb.Querier, s intakedb.IngestDocumentAdmissionSlot) (*intakedb.IngestRevisionAcceptance, intakedb.IngestExecutionResult, error) {
	var zero intakedb.IngestExecutionResult
	e, err := q.ReadAdmissionAcceptance(ctx, intakedb.ReadAdmissionAcceptanceParams{WorkflowID: s.WorkflowID,
		DocumentVersionKey: s.DocumentVersionKey, ProcessingAttemptKey: s.ProcessingAttemptKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, zero, nil
	}
	if err != nil {
		return nil, zero, err
	}
	r, err := q.ReadAdmissionResult(ctx, e.ExecutionResultID)
	if err != nil {
		return nil, zero, errors.Join(run.ErrAdmissionIntegrity, err)
	}
	if err := admissionResultIdentity(ctx, q, s, r); err != nil {
		return nil, zero, err
	}
	var request run.IntakeApplyRequest
	if json.Unmarshal(r.Payload, &request) != nil || request.Validate() != nil || acceptanceShape(request) != nil ||
		r.ExecutionID != e.ExecutionID || request.Work.Execution.WorkflowID != s.WorkflowID ||
		request.Work.Execution.Scope.Subject.LeaseKey() != s.DocumentVersionKey || request.Work.Execution.AttemptKey != s.ProcessingAttemptKey ||
		request.RevisionKey != e.RevisionKey || string(request.Acceptance) != e.State || r.ID != e.Watermark ||
		string(request.CandidateDigest) != e.CandidateDigest || string(request.QualityReportHash) != e.QualityReportHash {
		return nil, zero, run.ErrAdmissionIntegrity
	}
	var gaps []model.GapRef
	if err := json.Unmarshal(e.ScopedGaps, &gaps); err != nil {
		return nil, zero, errors.Join(run.ErrAdmissionIntegrity, err)
	}
	// The merged ReadAcceptanceAttempt takes a projection lock. Admission
	// instead validates the same consistency predicate without acquiring it.
	count, err := q.CountAcceptanceReceipts(ctx, intakedb.CountAcceptanceReceiptsParams{RevisionKey: e.RevisionKey, ExecutionResultID: r.ID})
	if err != nil || count != 1 {
		return nil, zero, errors.Join(run.ErrAdmissionIntegrity, err)
	}
	projection, err := q.AdmissionAcceptanceProjectionValid(ctx, intakedb.AdmissionAcceptanceProjectionValidParams{
		RevisionKey: e.RevisionKey, ProcessingAttemptKey: e.ProcessingAttemptKey})
	if err != nil || !projection {
		return nil, zero, errors.Join(run.ErrAdmissionIntegrity, err)
	}
	valid, err := q.AcceptanceCommitSetMatches(ctx, intakedb.AcceptanceCommitSetMatchesParams{AcceptanceID: e.ID,
		ResultKey: r.ResultKey, RevisionKey: e.RevisionKey, ScopedGaps: e.ScopedGaps, Eligible: request.Acceptance.BuildEligible()})
	if err != nil || !(valid.Valid && valid.Bool) {
		return nil, zero, errors.Join(run.ErrAdmissionIntegrity, err)
	}
	return &e, r, nil
}

// New lease epochs and fences may recover the same work. They cannot replace
// the input, recipe or budget frozen by the slot's original registration.
func admissionResultIdentity(ctx context.Context, q intakedb.Querier, s intakedb.IngestDocumentAdmissionSlot, result intakedb.IngestExecutionResult) error {
	original, err := q.ReadAdmissionExecution(ctx, s.OriginExecutionID)
	if err != nil {
		return errors.Join(run.ErrAdmissionIntegrity, err)
	}
	accepted, err := q.ReadAdmissionExecution(ctx, result.ExecutionID)
	if err != nil {
		return errors.Join(run.ErrAdmissionIntegrity, err)
	}
	var request run.IntakeApplyRequest
	if json.Unmarshal(result.Payload, &request) != nil || request.Validate() != nil {
		return run.ErrAdmissionIntegrity
	}
	work := request.Work
	budget, err := json.Marshal(work.Budget)
	if err != nil || original.WorkflowID != s.WorkflowID || original.ResourceKind != "document_version" ||
		original.ResourceKey != s.DocumentVersionKey || original.ProcessingAttemptKey != s.ProcessingAttemptKey ||
		accepted.WorkflowID != original.WorkflowID || accepted.WorkflowKind != original.WorkflowKind || accepted.ScopeKind != original.ScopeKind ||
		accepted.ResourceKind != original.ResourceKind || accepted.ResourceKey != original.ResourceKey || accepted.ProcessingAttemptKey != original.ProcessingAttemptKey ||
		accepted.InputDigest != original.InputDigest || accepted.RecipeDigest != original.RecipeDigest || accepted.BudgetDigest != original.BudgetDigest ||
		!sameSourceJSON(accepted.Budget, original.Budget) || result.InputDigest != original.InputDigest ||
		result.RecipeDigest != original.RecipeDigest || result.BudgetDigest != original.BudgetDigest ||
		string(work.InputDigest) != original.InputDigest || string(work.RecipeDigest) != original.RecipeDigest ||
		string(work.BudgetDigest) != original.BudgetDigest || !sameSourceJSON(original.Budget, budget) ||
		work.Execution.WorkflowID != accepted.WorkflowID || string(work.Execution.WorkflowKind) != accepted.WorkflowKind ||
		work.Execution.WorkflowID != "document-ingest/"+work.Execution.Country+"/"+s.DocumentVersionKey+"/"+s.ProcessingAttemptKey ||
		work.Execution.Scope.Subject.LeaseKey() != accepted.ResourceKey || work.Execution.AttemptKey != accepted.ProcessingAttemptKey ||
		work.Execution.AdmissionEpoch != accepted.AdmissionEpoch || work.Execution.AttemptFence != accepted.AttemptFence ||
		string(request.Outcome) != result.Outcome || string(work.Stage) != result.Stage || work.Substep != result.Substep || work.Shard != result.Shard ||
		string(request.IdempotencyKey) != result.IdempotencyKey {
		return errors.Join(run.ErrAdmissionIntegrity, err)
	}
	return nil
}

func admissionSettle(next *intakedb.IngestDocumentAdmissionSlot, e intakedb.IngestRevisionAcceptance, result intakedb.IngestExecutionResult) error {
	if next.State == "closing" || next.State == "released" {
		return run.ErrAdmissionConflict
	}
	next.SettledWatermark, next.ResumeRequestedAt = e.Watermark, nil
	if e.State == "quarantined" {
		if next.State != "reserved" && next.State != "processing" {
			return run.ErrAdmissionConflict
		}
		wait := "quarantine"
		next.State, next.WaitReason = "open_only", &wait
	} else {
		reason := "completed"
		if e.State == "excluded" {
			reason = "terminal_gap"
		}
		next.State, next.WaitReason, next.TerminalReason, next.TerminalResultID = "closing", nil, &reason, &result.ID
	}
	return nil
}

func admissionQuarantineTime(ctx context.Context, q intakedb.Querier, s intakedb.IngestDocumentAdmissionSlot, now time.Time, expired bool) error {
	if s.WaitReason == nil || *s.WaitReason != "quarantine" {
		if expired {
			return run.ErrAdmissionConflict
		}
		return nil
	}
	e, _, err := admissionAcceptance(ctx, q, s)
	if err != nil {
		return err
	}
	if e == nil || e.State != "quarantined" || e.Watermark != s.SettledWatermark ||
		expired == now.Before(e.CreatedAt.Add(time.Duration(s.QuarantineExpiryUs)*time.Microsecond)) {
		return run.ErrAdmissionConflict
	}
	return nil
}

func admissionCloseState(s *intakedb.IngestDocumentAdmissionSlot, reason string) {
	s.State, s.WaitReason, s.ResumeRequestedAt, s.TerminalReason = "closing", nil, nil, &reason
}

func admissionLeaseMatches(s intakedb.IngestDocumentAdmissionSlot, lease intakedb.IngestExecutionLease) bool {
	return s.AdmissionEpoch == lease.AdmissionEpoch && s.AttemptFence == lease.AttemptFence && s.LeaseOwner == lease.Owner
}

func admissionLeaseDisposition(ctx context.Context, q intakedb.Querier, s intakedb.IngestDocumentAdmissionSlot, lease intakedb.IngestExecutionLease) (string, error) {
	if !admissionLeaseMatches(s, lease) {
		return "superseded", nil
	}
	if lease.CancelledAt != nil {
		return "already_cancelled", nil
	}
	changed, err := q.CancelAdmissionLease(ctx, intakedb.CancelAdmissionLeaseParams{DocumentVersionKey: s.DocumentVersionKey,
		AdmissionEpoch: s.AdmissionEpoch, Owner: s.LeaseOwner, AttemptFence: s.AttemptFence})
	if err != nil {
		return "", err
	}
	if changed != 1 {
		return "", run.ErrAdmissionIntegrity
	}
	return "cancelled_matching", nil
}

func (a *DocumentAdmission) closeSlot(ctx context.Context, q intakedb.Querier, r run.AdmissionChange, s intakedb.IngestDocumentAdmissionSlot, next *intakedb.IngestDocumentAdmissionSlot,
	lease intakedb.IngestExecutionLease, reason run.AdmissionTerminalReason, cause string, now time.Time, response *admissionStoredResponse) error {
	if s.State == "closing" {
		return run.ErrAdmissionConflict
	}
	e, result, err := admissionAcceptance(ctx, q, s)
	if err != nil {
		return err
	}
	if e != nil && e.State != "candidate" && e.Watermark > s.SettledWatermark {
		return run.ErrAdmissionConflict
	}
	control := r.Actor.Execution.WorkflowKind == run.ControlReconcile
	switch reason {
	case run.AdmissionAbandoned:
		if control {
			if s.State != "reserved" && !(s.State == "open_only" && s.WaitReason != nil && *s.WaitReason == "worker_loss") {
				return run.ErrAdmissionConflict
			}
			d, err := q.ReadAdmissionDelivery(ctx, s.DeliveryID)
			if err != nil {
				return err
			}
			if d.AcknowledgedAt != nil || admissionLeaseMatches(s, lease) && lease.CancelledAt == nil && lease.ExpiresAt.After(now) && lease.AdmissionEpoch == r.Actor.Execution.AdmissionEpoch {
				return run.ErrAdmissionConflict
			}
			response.LostGrantCause = "superseded"
			if admissionLeaseMatches(s, lease) {
				response.LostGrantCause = "expired_or_cancelled_epoch"
			}
		}
	case run.AdmissionCancelled:
		if control && lease.CancelledAt == nil {
			return run.ErrAdmissionConflict
		}
	case run.AdmissionExpired:
		if s.State != "open_only" || e == nil || result.ResultKey != cause {
			return run.ErrAdmissionConflict
		}
		if err := admissionQuarantineTime(ctx, q, s, now, true); err != nil {
			return err
		}
	case run.AdmissionTerminalGap:
		gap, err := q.ReadAdmissionResultByKey(ctx, cause)
		if err != nil {
			return admissionConflict(err)
		}
		if err := admissionResultIdentity(ctx, q, s, gap); err != nil {
			return err
		}
		origin, err := q.ReadAdmissionExecution(ctx, gap.ExecutionID)
		if err != nil || origin.WorkflowID != s.WorkflowID || origin.ProcessingAttemptKey != s.ProcessingAttemptKey || origin.ResourceKey != s.DocumentVersionKey || gap.Outcome != "terminal_gap" {
			return errors.Join(run.ErrAdmissionConflict, err)
		}
		next.TerminalResultID = &gap.ID
	default:
		return run.ErrInvalidAdmission
	}
	response.ComparedLease = admissionComparedLease(lease)
	response.LeaseDisposition, err = admissionLeaseDisposition(ctx, q, s, lease)
	if err != nil {
		return err
	}
	admissionCloseState(next, string(reason))
	return nil
}

func (a *DocumentAdmission) reconcileSlot(ctx context.Context, q intakedb.Querier, s intakedb.IngestDocumentAdmissionSlot, next *intakedb.IngestDocumentAdmissionSlot,
	lease intakedb.IngestExecutionLease, epoch int64, now time.Time, response *admissionStoredResponse) error {
	e, result, err := admissionAcceptance(ctx, q, s)
	if err != nil {
		return err
	}
	if e != nil && e.State != "candidate" && e.Watermark > s.SettledWatermark {
		response.Operation = "settle"
		return admissionSettle(next, *e, result)
	}
	if s.State == "closing" {
		if response.Observation.Status != run.DocumentExecutionTerminal {
			return run.ErrAdmissionUnchanged
		}
		response.Operation, next.State = "release", "released"
		return nil
	}
	if response.Observation.Status == run.DocumentExecutionTerminal {
		response.Operation = "close"
		response.ComparedLease = admissionComparedLease(lease)
		response.LeaseDisposition, err = admissionLeaseDisposition(ctx, q, s, lease)
		if err != nil {
			return err
		}
		admissionCloseState(next, "execution_failed")
		return nil
	}
	if (s.State == "reserved" || s.State == "processing") && (!admissionLeaseMatches(s, lease) || lease.CancelledAt != nil || !lease.ExpiresAt.After(now) || lease.AdmissionEpoch < epoch) {
		reason := "worker_loss"
		next.State, next.WaitReason, next.ResumeRequestedAt = "open_only", &reason, nil
		response.Operation = "worker_loss"
		return nil
	}
	return run.ErrAdmissionUnchanged
}
