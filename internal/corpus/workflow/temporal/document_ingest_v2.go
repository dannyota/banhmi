package temporal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.temporal.io/sdk/workflow"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
)

const (
	// QuarantineDecisionSignalV2 carries a retained decision reference only.
	QuarantineDecisionSignalV2 = "quarantine.decision.v1"
	// IntakeReceiptSignalV2 wakes durable receipt readers across continuations.
	IntakeReceiptSignalV2 = "intake.receipt.v1"
)

// DocumentIngestV2 executes ledger-owned work without a source deadline.
// Quarantine and resume never reset frozen processing budgets or create approval.
func DocumentIngestV2(ctx workflow.Context, s IntakeStateV2) (result IntakeCompletionV2, err error) {
	if err = intakeBeginV2(ctx, &s, run.DocumentIngest); err != nil {
		return result, err
	}
	defer intakeCancellationV2(ctx, &s, &err)
	if s.Stop != nil {
		f, err := intakeCallV2(ctx, s, "drain", nil, nil, nil)
		return intakeCompletionV2(f), err
	}
	signals := 0
	progressed := false
	for {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if intakeRotateV2(ctx, s, signals) {
			return result, intakeContinueV2(ctx, s, DocumentIngestNameV2, progressed)
		}
		var f IntakeFrameV2
		if s.Phase == "decision" {
			f, err = intakeQuarantineWaitV2(ctx, &s, &signals, &progressed)
		} else {
			f, err = intakeCallV2(ctx, s, s.Phase, nil, nil, nil)
		}
		if err != nil {
			return result, err
		}
		progressed = true
		if len(f.Launches) != 0 || len(f.Notifications) != 0 {
			return result, intakeFailureV2("document_fanout_forbidden", false)
		}
		s.Work = f.Work
		switch f.Status {
		case "accepted", "terminal_gap":
			if s.Phase != "evidence" {
				return result, intakeFailureV2("acceptance_requires_apply", false)
			}
			return intakeCompletionV2(f), nil
		case "stopped", "cancelled":
			return intakeCompletionV2(f), nil
		case "quarantined":
			if s.Phase != "quarantine" && s.Phase != "resume" && s.Phase != "decision" {
				return result, intakeFailureV2("quarantine_requires_apply", false)
			}
			s.Phase, s.DecisionEpoch, s.ReportHash = "decision", f.DecisionEpoch, f.ReportHash
			s.ReminderAt, s.ExpiresAt = f.QuarantineAt.Add(s.Config.Reminder), f.QuarantineAt.Add(s.Config.Expiry)
			continue
		case "wait":
			if err = intakeReceiptWaitV2(ctx, s, f.WaitUntil, &signals); err != nil {
				return result, err
			}
			s.Phase = "resume"
			continue
		case "progress":
		default:
			return result, intakeFailureV2("invalid_document_status", false)
		}
		if s.Phase == "decision" {
			if f.Next != "normalize" && f.Next != "gate" && f.Next != "resume" {
				return result, intakeFailureV2("decision_must_rerun_gates", false)
			}
			s.ExpiresAt, s.ReminderAt, s.DecisionEpoch, s.ReportHash = time.Time{}, time.Time{}, 0, ""
		} else if !documentTransitionV2(s.Phase, f.Next) {
			return result, intakeFailureV2("invalid_document_transition", false)
		}
		s.Phase = f.Next
	}
}

func documentTransitionV2(from, to string) bool {
	switch from {
	case "resume":
		return to == "fetch" || to == "capture" || to == "detect" || to == "convert" || to == "normalize" || to == "structure" || to == "gate" || to == "candidate" || to == "quarantine" || to == "evidence" || to == "ocr-request"
	case "fetch":
		return to == "capture" || to == "resume"
	case "capture":
		return to == "detect"
	case "detect":
		return to == "convert" || to == "quarantine" || to == "ocr-request"
	case "convert":
		return to == "normalize" || to == "quarantine" || to == "ocr-request"
	case "normalize":
		return to == "structure" || to == "quarantine"
	case "structure":
		return to == "gate" || to == "quarantine"
	case "gate":
		return to == "candidate" || to == "quarantine" || to == "ocr-request"
	case "candidate":
		return to == "evidence" || to == "quarantine" || to == "resume"
	case "ocr-request":
		return to == "normalize" || to == "resume" || to == "quarantine"
	default:
		return false
	}
}

func intakeQuarantineWaitV2(ctx workflow.Context, s *IntakeStateV2, signals *int, progressed *bool) (IntakeFrameV2, error) {
	for {
		if intakeRotateV2(ctx, *s, *signals) {
			return IntakeFrameV2{}, intakeContinueV2(ctx, *s, DocumentIngestNameV2, *progressed)
		}
		now := workflow.Now(ctx)
		if !s.ReminderAt.IsZero() && !now.Before(s.ReminderAt) {
			f, err := intakeCallV2(ctx, *s, "reminder", nil, nil, nil)
			if err != nil {
				return f, err
			}
			s.Work, s.ReminderAt = f.Work, time.Time{}
			*progressed = true
		}
		// The periodic operation, not this hint or Reconcile alone, attempts
		// expiry using the actual private quarantine result and DB due time.
		due := now.Add(s.Config.Reconcile)
		if s.ReminderAt.After(now) && s.ReminderAt.Before(due) {
			due = s.ReminderAt
		}
		timerCtx, cancel := workflow.WithCancel(ctx)
		timer := workflow.NewTimer(timerCtx, due.Sub(now))
		var decision IntakeDecisionV2
		received := false
		selector := workflow.NewSelector(ctx)
		selector.AddFuture(timer, func(workflow.Future) {})
		selector.AddReceive(workflow.GetSignalChannel(ctx, QuarantineDecisionSignalV2), func(c workflow.ReceiveChannel, _ bool) {
			c.Receive(ctx, &decision)
			received = true
			*signals++
		})
		selector.Select(ctx)
		cancel()
		if ctx.Err() != nil {
			return IntakeFrameV2{}, ctx.Err()
		}
		op := "expiry"
		var signal *IntakeDecisionV2
		if received && decision.DecisionRef.Validate() == nil && decision.ExpectedEpoch == s.DecisionEpoch && decision.ReportHash == s.ReportHash {
			op, signal = "decision", &decision
		}
		f, err := intakeCallV2(ctx, *s, op, nil, nil, signal)
		if err == nil {
			*progressed = true
		}
		if err != nil || f.Status != "wait" && f.Status != "quarantined" {
			return f, err
		}
		s.Work = f.Work
	}
}

func intakeReceiptWaitV2(ctx workflow.Context, s IntakeStateV2, due time.Time, signals *int) error {
	timerCtx, cancel := workflow.WithCancel(ctx)
	defer cancel()
	periodic := workflow.Now(ctx).Add(s.Config.Reconcile)
	if due.After(periodic) {
		due = periodic
	}
	timer := workflow.NewTimer(timerCtx, intakeDelayV2(ctx, due, time.Time{}))
	selector := workflow.NewSelector(ctx)
	selector.AddFuture(timer, func(workflow.Future) {})
	selector.AddReceive(workflow.GetSignalChannel(ctx, IntakeReceiptSignalV2), func(c workflow.ReceiveChannel, _ bool) {
		var receipt run.IntakeReceiptEnvelope
		c.Receive(ctx, &receipt)
		*signals++
	})
	selector.Select(ctx)
	return ctx.Err()
}

func admissionMatchesV2(cp IntakeLocalCheckpointV2, r run.AdmissionResolution) bool {
	e := cp.Work.Execution
	return r.Found && r.State.Valid() && r.WorkflowID == e.WorkflowID && r.VersionKey == e.Scope.Subject.LeaseKey() &&
		r.AttemptKey == e.AttemptKey && r.Binding.ExecutionID == cp.ExecutionID && r.Binding.Owner == e.Owner &&
		r.Binding.Epoch == e.AdmissionEpoch && r.Binding.Fence == e.AttemptFence
}

func (a *IntakeActivitiesV2) resolveDocument(ctx context.Context, cp IntakeLocalCheckpointV2) (run.AdmissionResolution, error) {
	r, err := a.Admission.Resolve(ctx, run.AdmissionResolveRequest{Actor: cp.Work, DocumentWork: &cp.Work})
	e := cp.Work.Execution
	if err == nil && r.Found && (r.Revision < 1 || !r.State.Valid() || r.WorkflowID != e.WorkflowID ||
		r.VersionKey != e.Scope.Subject.LeaseKey() || r.AttemptKey != e.AttemptKey) {
		return run.AdmissionResolution{}, run.ErrAdmissionIntegrity
	}
	return r, err
}

func (a *IntakeActivitiesV2) retain(ctx context.Context, cp IntakeLocalCheckpointV2, f IntakeFrameV2) (IntakeFrameV2, error) {
	ref, err := a.Checkpoints.Retain(ctx, cp)
	f.Work = ref
	return f, err
}

func (a *IntakeActivitiesV2) waiting(ctx context.Context, cmd IntakeCommandV2, cp IntakeLocalCheckpointV2) (IntakeFrameV2, error) {
	return a.retain(ctx, cp, IntakeFrameV2{Status: "wait", Next: "resume", WaitUntil: time.Now().UTC().Add(cmd.State.Config.Reconcile)})
}

// change persists the immutable operation revision before any uncertain commit.
// A retry reuses it. Conflict clears it only after a fresh read, never blind retry.
func (a *IntakeActivitiesV2) change(ctx context.Context, cp *IntakeLocalCheckpointV2, r run.AdmissionResolution, op string, reason run.AdmissionTerminalReason, cause string) (run.AdmissionReceipt, error) {
	if cp.Pending == nil {
		postPhase := cp.Phase
		if op == "resume" && r.WaitReason == "quarantine" {
			postPhase = "normalize"
		}
		cp.Pending = &IntakePendingChangeV2{Operation: op, Change: run.AdmissionChange{Actor: cp.Work, SlotKey: r.SlotKey, ExpectedRevision: r.Revision}, Reason: reason, Cause: cause, Resolution: r, PostPhase: postPhase}
		if _, err := a.Checkpoints.Retain(ctx, *cp); err != nil {
			return run.AdmissionReceipt{}, err
		}
	}
	p := cp.Pending
	if p.Operation != op || p.Reason != reason || p.Cause != cause {
		return run.AdmissionReceipt{}, fmt.Errorf("unreconciled admission operation")
	}
	if op == "resume" && p.Resolution.WaitReason == "quarantine" && p.PostPhase != "normalize" {
		return run.AdmissionReceipt{}, run.ErrAdmissionIntegrity
	}
	change := p.Change
	change.Actor = cp.Work
	var receipt run.AdmissionReceipt
	var err error
	switch op {
	case "bind":
		receipt, err = a.Admission.Bind(ctx, change)
	case "settle":
		receipt, err = a.Admission.Settle(ctx, change)
	case "request_resume":
		receipt, err = a.Admission.RequestResume(ctx, change)
	case "resume":
		var result run.AdmissionResult
		result, err = a.Admission.Resume(ctx, change)
		if err == nil && result.Refusal != "" {
			if !result.Refusal.Valid() || result.Receipt != nil {
				return receipt, fmt.Errorf("invalid resume refusal")
			}
			err = run.ErrAdmissionUnchanged
		} else if err == nil && result.Receipt != nil {
			receipt = *result.Receipt
		} else if err == nil {
			err = run.ErrAdmissionIntegrity
		}
	case "close":
		receipt, err = a.Admission.Close(ctx, run.AdmissionCloseRequest{Change: change, Reason: reason, CauseReceipt: cause})
	default:
		return receipt, fmt.Errorf("invalid admission operation")
	}
	if err == nil || errors.Is(err, run.ErrAdmissionConflict) || errors.Is(err, run.ErrAdmissionUnchanged) {
		if err != nil {
			if _, resolveErr := a.resolveDocument(ctx, *cp); resolveErr != nil {
				return receipt, resolveErr
			}
		}
		// Publish the mandatory next phase in the same durable checkpoint that
		// clears a successful Resume. Its prior pending record already retains
		// PostPhase, so response loss or failed retention replays it identically.
		if err == nil && op == "resume" {
			cp.Phase = p.PostPhase
		}
		cp.Pending = nil
		if _, keepErr := a.Checkpoints.Retain(ctx, *cp); keepErr != nil {
			return receipt, keepErr
		}
	}
	return receipt, err
}

func (a *IntakeActivitiesV2) documentAdmission(ctx context.Context, cmd IntakeCommandV2, cp *IntakeLocalCheckpointV2) (IntakeFrameV2, bool, error) {
	if p := cp.Pending; p != nil {
		if p.Operation == "request_resume" || p.Operation == "resume" {
			// Validation rereads authoritative local evidence. The retained
			// snapshot identifies the old cause; it grants no permission.
			if a.ResumeValidation == nil || a.ResumeValidation.Validate(ctx, cp.Work, p.Resolution, cp.Decision) != nil {
				return IntakeFrameV2{}, true, fmt.Errorf("pending resume lost authority")
			}
		}
		if _, err := a.change(ctx, cp, p.Resolution, p.Operation, p.Reason, p.Cause); err != nil {
			return IntakeFrameV2{}, true, err
		}
	}
	r, err := a.resolveDocument(ctx, *cp)
	if err != nil || !r.Found {
		return IntakeFrameV2{}, true, fmt.Errorf("missing document admission: %w", err)
	}
	if r.State == run.AdmissionClosing || r.State == run.AdmissionReleased {
		if cp.Terminal != nil && a.Inputs != nil {
			if cp.Terminal.Validate() != nil || model.ValidateEnvelopeKey(cp.Terminal.ResultID, "result") != nil ||
				cp.Terminal.Result.Work.Execution.WorkflowID != cp.Work.Execution.WorkflowID ||
				cp.Terminal.Result.Work.Execution.Country != cp.Work.Execution.Country {
				return IntakeFrameV2{}, true, fmt.Errorf("invalid terminal checkpoint")
			}
			if cmd.Operation != "evidence" {
				f, err := a.retain(ctx, *cp, IntakeFrameV2{Status: "progress", Next: "evidence"})
				return f, true, err
			}
			f, err := a.Inputs.Project(ctx, cmd, *cp.Terminal)
			if err != nil || f.Receipt == nil || f.Receipt.Key != cp.Terminal.ResultID {
				return IntakeFrameV2{}, true, fmt.Errorf("missing terminal result projection: %w", err)
			}
			f, err = a.retain(ctx, *cp, f)
			return f, true, err
		}
		f, err := a.retain(ctx, *cp, IntakeFrameV2{Status: "stopped", Reason: string(r.TerminalReason)})
		return f, true, err
	}
	if cmd.Operation == "quarantine" && r.State == run.AdmissionOpenOnly && r.WaitReason == "quarantine" && validQuarantineV2(*cp) && a.Inputs != nil {
		f, err := a.Inputs.Project(ctx, cmd, *cp.Quarantine)
		if err != nil || f.Receipt == nil || f.Receipt.Key != cp.Quarantine.ResultID || f.Status != "quarantined" {
			return IntakeFrameV2{}, true, fmt.Errorf("missing quarantine replay: %w", err)
		}
		f, err = a.retain(ctx, *cp, f)
		return f, true, err
	}
	if cmd.Operation == "cancel" {
		_, err := a.change(ctx, cp, r, "close", run.AdmissionCancelled, "")
		f, keepErr := a.retain(ctx, *cp, IntakeFrameV2{Status: "cancelled", Reason: "cancelled"})
		if err != nil {
			return f, true, err
		}
		return f, true, keepErr
	}
	if cmd.Operation == "expiry" || cmd.Operation == "decision" || cmd.Operation == "resume" {
		// Latest acceptance wins over stale quarantine, including after response loss.
		if cp.Quarantine != nil && (cp.Pending == nil || cp.Pending.Operation == "settle") {
			if _, err := a.change(ctx, cp, r, "settle", "", ""); err != nil && !errors.Is(err, run.ErrAdmissionUnchanged) {
				return IntakeFrameV2{}, true, err
			}
			r, err = a.resolveDocument(ctx, *cp)
			if err != nil {
				return IntakeFrameV2{}, true, err
			}
			if r.State == run.AdmissionClosing || r.State == run.AdmissionReleased {
				f, err := a.retain(ctx, *cp, IntakeFrameV2{Status: "stopped", Reason: string(r.TerminalReason)})
				return f, true, err
			}
		}
		if r.State == run.AdmissionReserved || !admissionMatchesV2(*cp, r) {
			if _, err := a.change(ctx, cp, r, "bind", "", ""); err != nil {
				return IntakeFrameV2{}, true, err
			}
			r, err = a.resolveDocument(ctx, *cp)
			if err != nil || !admissionMatchesV2(*cp, r) {
				return IntakeFrameV2{}, true, fmt.Errorf("bind is not current: %w", err)
			}
		}
		if r.State == run.AdmissionOpenOnly {
			if r.WaitReason == "quarantine" && cmd.Operation == "expiry" {
				if !validQuarantineV2(*cp) {
					return IntakeFrameV2{}, true, fmt.Errorf("missing quarantine cause")
				}
				_, err := a.change(ctx, cp, r, "close", run.AdmissionExpired, cp.Quarantine.ResultID)
				if err == nil {
					f, err := a.retain(ctx, *cp, IntakeFrameV2{Status: "stopped", Reason: "expired"})
					return f, true, err
				}
				if !errors.Is(err, run.ErrAdmissionConflict) {
					return IntakeFrameV2{}, true, err
				}
				// A timer checks expiry and also recovers persisted resume demand.
				// Re-read after the failed close; a retained decision still needs
				// fresh authority validation before either FIFO call below.
				r, err = a.resolveDocument(ctx, *cp)
				if err != nil {
					return IntakeFrameV2{}, true, err
				}
				if cp.Decision == nil || r.State != run.AdmissionOpenOnly || r.WaitReason != "quarantine" {
					f, err := a.waiting(ctx, cmd, *cp)
					return f, true, err
				}
			}
			if cmd.Decision == nil {
				cmd.Decision = cp.Decision
			}
			if r.WaitReason == "quarantine" && cmd.Decision == nil {
				if !validQuarantineV2(*cp) {
					return IntakeFrameV2{}, true, fmt.Errorf("missing quarantine checkpoint")
				}
				f, err := a.retain(ctx, *cp, IntakeFrameV2{Status: "quarantined", QuarantineAt: cp.QuarantineAt,
					DecisionEpoch: cp.DecisionEpoch, ReportHash: cp.Quarantine.Result.QualityReportHash})
				return f, true, err
			}
			if a.ResumeValidation == nil || (r.WaitReason != "quarantine" && r.WaitReason != "worker_loss") || r.LifecycleReceipt == "" {
				return IntakeFrameV2{}, true, fmt.Errorf("missing resume cause")
			}
			if err := a.ResumeValidation.Validate(ctx, cp.Work, r, cmd.Decision); err != nil {
				// Invalid wake-ups create no demand and leave durable periodic expiry.
				if r.WaitReason == "quarantine" && validQuarantineV2(*cp) {
					cp.Decision = nil
					f, keepErr := a.retain(ctx, *cp, IntakeFrameV2{Status: "quarantined", QuarantineAt: cp.QuarantineAt,
						DecisionEpoch: cp.DecisionEpoch, ReportHash: cp.Quarantine.Result.QualityReportHash})
					return f, true, keepErr
				}
				f, keepErr := a.waiting(ctx, cmd, *cp)
				return f, true, keepErr
			}
			cp.Decision = cmd.Decision
			if _, err := a.Checkpoints.Retain(ctx, *cp); err != nil {
				return IntakeFrameV2{}, true, err
			}
			if cp.Pending == nil || cp.Pending.Operation == "request_resume" {
				if _, err := a.change(ctx, cp, r, "request_resume", "", ""); err != nil {
					return IntakeFrameV2{}, true, err
				}
			}
			if cp.Pending != nil && cp.Pending.Operation != "resume" {
				return IntakeFrameV2{}, true, fmt.Errorf("unreconciled resume operation")
			}
			r, err = a.resolveDocument(ctx, *cp)
			if err != nil {
				return IntakeFrameV2{}, true, err
			}
			if err := a.ResumeValidation.Validate(ctx, cp.Work, r, cmd.Decision); err != nil {
				return IntakeFrameV2{}, true, err
			}
			if _, err := a.change(ctx, cp, r, "resume", "", ""); err != nil {
				if errors.Is(err, run.ErrAdmissionUnchanged) || errors.Is(err, run.ErrAdmissionConflict) {
					f, keepErr := a.waiting(ctx, cmd, *cp)
					return f, true, keepErr
				}
				return IntakeFrameV2{}, true, err
			}
			r, err = a.resolveDocument(ctx, *cp)
			if err != nil {
				return IntakeFrameV2{}, true, err
			}
		}
		if r.State != run.AdmissionProcessing || !admissionMatchesV2(*cp, r) {
			return IntakeFrameV2{}, true, fmt.Errorf("no current processing permission")
		}
		if cp.Phase == "" {
			cp.Phase = "fetch"
		}
		f, err := a.retain(ctx, *cp, IntakeFrameV2{Status: "progress", Next: cp.Phase})
		return f, true, err
	}
	if cmd.Operation == "reminder" && r.State == run.AdmissionOpenOnly && r.WaitReason == "quarantine" {
		return IntakeFrameV2{}, false, nil
	}
	if r.State != run.AdmissionProcessing || !admissionMatchesV2(*cp, r) {
		return IntakeFrameV2{}, true, fmt.Errorf("no current processing permission")
	}
	return IntakeFrameV2{}, false, nil
}

func validQuarantineV2(cp IntakeLocalCheckpointV2) bool {
	if cp.Quarantine == nil || cp.Quarantine.Validate() != nil || model.ValidateEnvelopeKey(cp.Quarantine.ResultID, "result") != nil ||
		cp.QuarantineAt.IsZero() || cp.DecisionEpoch <= 0 || cp.Quarantine.Result.Acceptance != model.Quarantined {
		return false
	}
	a, b := cp.Quarantine.Result.Work.Execution, cp.Work.Execution
	return a.Country == b.Country && a.WorkflowID == b.WorkflowID && a.AttemptKey == b.AttemptKey && a.Scope == b.Scope
}

func (a *IntakeActivitiesV2) apply(ctx context.Context, cmd IntakeCommandV2, cp *IntakeLocalCheckpointV2) (IntakeFrameV2, error) {
	if a.Inputs == nil || a.Acceptance == nil {
		return IntakeFrameV2{}, fmt.Errorf("missing acceptance binding")
	}
	request, err := a.Inputs.Prepare(ctx, cmd)
	if err != nil {
		return IntakeFrameV2{}, err
	}
	e := request.Work.Execution
	if request.Validate() != nil || e != cp.Work.Execution ||
		cmd.Operation == "candidate" && request.Acceptance != model.Candidate ||
		cmd.Operation == "quarantine" && request.Acceptance != model.Quarantined ||
		cmd.Operation == "evidence" && request.Acceptance != model.Accepted && request.Acceptance != model.AcceptedWithGaps && request.Acceptance != model.Excluded {
		return IntakeFrameV2{}, fmt.Errorf("invalid acceptance binding")
	}
	receipt, err := a.Acceptance.Apply(ctx, request)
	if err != nil {
		return IntakeFrameV2{}, err
	}
	if receipt.Validate() != nil || !run.SameImmutableIntakePayload(receipt.Result, request) {
		return IntakeFrameV2{}, fmt.Errorf("invalid acceptance receipt")
	}
	f, err := a.Inputs.Project(ctx, cmd, receipt)
	if err != nil || f.Receipt == nil || f.Receipt.Key != receipt.ResultID {
		return IntakeFrameV2{}, fmt.Errorf("invalid acceptance projection: %w", err)
	}
	if request.Acceptance == model.Candidate && f.Status != "progress" || request.Acceptance == model.Quarantined && f.Status != "quarantined" ||
		(request.Acceptance == model.Accepted || request.Acceptance == model.AcceptedWithGaps) && f.Status != "accepted" ||
		request.Acceptance == model.Excluded && f.Status != "terminal_gap" {
		return IntakeFrameV2{}, fmt.Errorf("acceptance outcome projection mismatch")
	}
	if request.Acceptance == model.Quarantined {
		cp.Quarantine, cp.QuarantineAt, cp.DecisionEpoch = &receipt, f.QuarantineAt, f.DecisionEpoch
		if !validQuarantineV2(*cp) {
			return IntakeFrameV2{}, fmt.Errorf("invalid quarantine checkpoint")
		}
	}
	if request.Acceptance == model.Accepted || request.Acceptance == model.AcceptedWithGaps || request.Acceptance == model.Excluded {
		cp.Terminal = &receipt
	}
	cp.Phase = f.Next
	f, err = a.retain(ctx, *cp, f)
	if err != nil || request.Acceptance == model.Candidate {
		return f, err
	}
	r, err := a.resolveDocument(ctx, *cp)
	if err != nil {
		return f, err
	}
	_, err = a.change(ctx, cp, r, "settle", "", "")
	return f, err
}
