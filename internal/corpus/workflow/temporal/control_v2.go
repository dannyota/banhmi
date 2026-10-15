package temporal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"go.temporal.io/sdk/workflow"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
)

// ControlReconcileV2 dispatches a bounded country-control sweep even when
// sources are disabled. Activities use the country control lease/cursor and
// require an explicit transfer before consuming any source launch intent.
// Signals wake readers; only a durable receipt establishes acceptance.
func ControlReconcileV2(ctx workflow.Context, s IntakeStateV2) (receipt IntakeCompletionV2, err error) {
	if err = intakeBeginV2(ctx, &s, run.ControlReconcile); err != nil {
		return receipt, err
	}
	defer intakeCancellationV2(ctx, &s, &err)
	sweeps, starts := 0, 0
	progressed := false
	for {
		if ctx.Err() != nil {
			return receipt, ctx.Err()
		}
		if intakeRotateV2(ctx, s, 0) || sweeps >= s.Config.Pages || starts >= s.Config.ChildStarts {
			return receipt, intakeContinueV2(ctx, s, ControlReconcileNameV2, progressed)
		}
		frame, callErr := intakeCallLimitV2(ctx, s, "control", s.Config.ChildStarts-starts)
		if callErr != nil {
			return receipt, callErr
		}
		if len(frame.Launches) > s.Config.ChildStarts-starts ||
			(len(frame.Launches) != 0 && len(frame.Notifications) != 0) {
			return receipt, intakeFailureV2("control_command_limit", false)
		}
		s.Work, receipt = frame.Work, intakeCompletionV2(frame)
		progressed = true
		if err = intakeStartGroupV2(ctx, &s, frame.Launches); err != nil {
			return receipt, err
		}
		starts += len(frame.Launches)
		// Dispatch sequentially, so there is only one pending signal and one
		// pending activity. A lost response leaves the original inbox row due.
		for _, notification := range frame.Notifications {
			name := IntakeReceiptSignalV2
			var payload any = notification.Receipt
			if notification.Decision != nil {
				name, payload = QuarantineDecisionSignalV2, *notification.Decision
			}
			if err = workflow.SignalExternalWorkflow(ctx, notification.WorkflowID, "", name, payload).Get(ctx, nil); err != nil {
				return receipt, err
			}
			ack, ackErr := intakeCallV2(ctx, s, "delivered", nil, &notification, nil)
			if ackErr != nil {
				return receipt, ackErr
			}
			s.Work = ack.Work
		}
		sweeps++
		if frame.Status == "done" || frame.Status == "idle" {
			return receipt, nil
		}
		if frame.Status == "wait" {
			if err = workflow.Sleep(ctx, intakeDelayV2(ctx, frame.WaitUntil, s.SourceDeadline)); err != nil {
				return receipt, err
			}
			continue
		}
		if frame.Status != "progress" {
			return receipt, intakeFailureV2("invalid_control_transition", false)
		}
	}
}

func (a *IntakeActivitiesV2) drain(ctx context.Context, cmd IntakeCommandV2) (IntakeFrameV2, error) {
	if cmd.State.Stop == nil || cmd.State.Stop.Validate() != nil || a.Stops == nil {
		return IntakeFrameV2{}, fmt.Errorf("missing screened stop")
	}
	stop, err := a.Stops.Resolve(ctx, *cmd.State.Stop)
	if err != nil {
		return IntakeFrameV2{}, err
	}
	if err := a.checkStop(ctx, *cmd.State.Stop, stop, cmd.State.Country, cmd.State.WorkflowID, cmd.State.Config.EnvelopeBytes); err != nil && !errors.Is(err, run.ErrAdmissionUnchanged) {
		return IntakeFrameV2{}, err
	}
	return IntakeFrameV2{Work: cmd.State.Work, Status: "stopped", Reason: "abandoned"}, nil
}

func (a *IntakeActivitiesV2) checkStop(ctx context.Context, ref IntakeStopReferenceV2, stop IntakeLocalStopV2, country, workflowID string, maxBytes int) error {
	// Private manifest bytes never enter a Temporal envelope. Bound before
	// hashing/decoding; the enclosing command already applies its smaller bound.
	if maxBytes <= 0 || len(stop.Manifest) == 0 || len(stop.Manifest) > maxBytes {
		return fmt.Errorf("invalid stop manifest size")
	}
	hash := sha256.Sum256(stop.Manifest)
	var manifest IntakeStopManifestV2
	if model.Digest(hex.EncodeToString(hash[:])) != ref.Manifest || json.Unmarshal(stop.Manifest, &manifest) != nil ||
		manifest != (IntakeStopManifestV2{Country: country, WorkflowID: workflowID, VersionKey: stop.VersionKey,
			AttemptKey: stop.AttemptKey, SlotKey: stop.SlotKey, CloseReceipt: ref.Key}) {
		return fmt.Errorf("stop manifest mismatch")
	}
	e := stop.Control.Execution
	if ref.Validate() != nil || stop.Control.Validate() != nil || e.WorkflowKind != run.ControlReconcile || e.Country != country ||
		stop.Country != country || stop.WorkflowID != workflowID || stop.Receipt.Key != ref.Key || stop.Receipt.SlotKey != stop.SlotKey ||
		workflowID != "document-ingest/"+country+"/"+stop.VersionKey+"/"+stop.AttemptKey {
		return fmt.Errorf("stop target mismatch")
	}
	r, err := a.Admission.Resolve(ctx, run.AdmissionResolveRequest{Actor: stop.Control, WorkflowID: workflowID})
	if err != nil {
		return err
	}
	if !r.Found || r.WorkflowID != workflowID || r.VersionKey != stop.VersionKey || r.AttemptKey != stop.AttemptKey ||
		r.SlotKey != stop.SlotKey || r.CloseReceipt != ref.Key || r.TerminalReason != run.AdmissionAbandoned {
		return fmt.Errorf("stop receipt mismatch")
	}
	if r.State == run.AdmissionReleased {
		return run.ErrAdmissionUnchanged
	}
	if r.State != run.AdmissionClosing {
		return fmt.Errorf("stop is not closing")
	}
	return nil
}

func (a *IntakeActivitiesV2) reconcileSweep(ctx context.Context, cmd IntakeCommandV2, cp *IntakeLocalCheckpointV2) error {
	if a.ControlCheckpoints == nil {
		return fmt.Errorf("missing control checkpoint binding")
	}
	configure, err := a.ControlCheckpoints.Configuration(ctx, cp.Work)
	if err != nil {
		return err
	}
	if configure != nil {
		if configure.Actor != cp.Work {
			return fmt.Errorf("configuration actor mismatch")
		}
		if _, err := a.Admission.Configure(ctx, *configure); err != nil {
			return err
		}
	}
	// Retry an uncertain control mutation before scanning current revisions.
	if cp.Pending != nil {
		if _, err := a.controlChange(ctx, cp, cp.Pending.Change, cp.Pending.Operation, cp.Pending.Reason, cp.Pending.Cause); err != nil && !errors.Is(err, run.ErrAdmissionConflict) && !errors.Is(err, run.ErrAdmissionUnchanged) {
			return err
		}
	}
	limit := min(cmd.State.Config.AdmissionScanItems, 1000, int(cp.Work.Budget.MaxItems))
	items, err := a.Admission.Scan(ctx, run.AdmissionScan{Actor: cp.Work, AfterUpdatedAt: cp.ScanTime, AfterID: cp.ScanID, Limit: int32(limit)})
	if err != nil {
		return err
	}
	if len(items) > limit {
		return fmt.Errorf("admission scan exceeded limit")
	}
	for _, item := range items {
		target, err := a.ControlCheckpoints.Slot(ctx, cp.Work, item)
		if err != nil || target.Work.Validate() != nil || target.Work.Execution.Country != cp.Work.Execution.Country ||
			target.Work.Execution.WorkflowKind != run.DocumentIngest {
			return fmt.Errorf("invalid control checkpoint: %w", err)
		}
		r, err := a.Admission.Resolve(ctx, run.AdmissionResolveRequest{Actor: cp.Work, WorkflowID: target.Work.Execution.WorkflowID})
		if err != nil || !r.Found || r.SlotKey != item.SlotKey {
			return fmt.Errorf("control slot mismatch: %w", err)
		}
		change := run.AdmissionChange{Actor: cp.Work, SlotKey: r.SlotKey, ExpectedRevision: r.Revision}
		_, err = a.controlChange(ctx, cp, change, "reconcile", "", "")
		if err != nil && !errors.Is(err, run.ErrAdmissionUnchanged) && !errors.Is(err, run.ErrAdmissionConflict) {
			return err
		}
		// Reconcile never expires quarantine. The real result is retained privately.
		r, err = a.Admission.Resolve(ctx, run.AdmissionResolveRequest{Actor: cp.Work, WorkflowID: target.Work.Execution.WorkflowID})
		if err != nil {
			return err
		}
		change.ExpectedRevision = r.Revision
		if r.State == run.AdmissionOpenOnly && r.WaitReason == "quarantine" {
			if !validQuarantineV2(target) {
				return fmt.Errorf("missing retained quarantine result")
			}
			_, err = a.controlChange(ctx, cp, change, "close", run.AdmissionExpired, target.Quarantine.ResultID)
			if err != nil && !errors.Is(err, run.ErrAdmissionConflict) {
				return err
			}
		}
		if r.State == run.AdmissionReserved || r.State == run.AdmissionOpenOnly && r.WaitReason == "worker_loss" {
			// The adapter proves lost grant and unacknowledged launch. A live or
			// acknowledged target refuses without transport side effects.
			_, err = a.controlChange(ctx, cp, change, "close", run.AdmissionAbandoned, "")
			if err != nil && !errors.Is(err, run.ErrAdmissionConflict) {
				return err
			}
		}
		r, err = a.Admission.Resolve(ctx, run.AdmissionResolveRequest{Actor: cp.Work, WorkflowID: target.Work.Execution.WorkflowID})
		if err != nil {
			return err
		}
		if r.State == run.AdmissionClosing && r.TerminalReason == run.AdmissionAbandoned {
			if a.Stops == nil || a.Transport == nil {
				return fmt.Errorf("missing drain binding")
			}
			ref, err := a.Stops.Screen(ctx, cp.Work, r.CloseReceipt)
			if err != nil || ref.Key != r.CloseReceipt || ref.Validate() != nil {
				return fmt.Errorf("invalid stop projection: %w", err)
			}
			stop, err := a.Stops.Resolve(ctx, ref)
			if err != nil {
				return err
			}
			if err := a.checkStop(ctx, ref, stop, cmd.State.Country, target.Work.Execution.WorkflowID, cmd.State.Config.EnvelopeBytes); err != nil {
				if !errors.Is(err, run.ErrAdmissionUnchanged) {
					return err
				}
			} else if err := a.Transport.Drain(ctx, target.Work.Execution.WorkflowID, ref); err != nil {
				return err
			}
		}
		cp.ScanTime, cp.ScanID = item.UpdatedAt, item.ID
		if _, err := a.Checkpoints.Retain(ctx, *cp); err != nil {
			return err
		}
	}
	if len(items) < limit {
		cp.ScanTime, cp.ScanID = time.Time{}, 0
	}
	_, err = a.Checkpoints.Retain(ctx, *cp)
	return err
}

func (a *IntakeActivitiesV2) controlChange(ctx context.Context, cp *IntakeLocalCheckpointV2, c run.AdmissionChange, op string, reason run.AdmissionTerminalReason, cause string) (run.AdmissionReceipt, error) {
	if cp.Pending == nil {
		cp.Pending = &IntakePendingChangeV2{Operation: op, Change: c, Reason: reason, Cause: cause}
		if _, err := a.Checkpoints.Retain(ctx, *cp); err != nil {
			return run.AdmissionReceipt{}, err
		}
	}
	p := cp.Pending
	if p.Operation != op || p.Change.SlotKey != c.SlotKey || p.Reason != reason || p.Cause != cause {
		return run.AdmissionReceipt{}, fmt.Errorf("unreconciled control mutation")
	}
	c = p.Change
	c.Actor = cp.Work
	var receipt run.AdmissionReceipt
	var err error
	if op == "reconcile" {
		receipt, err = a.Admission.Reconcile(ctx, c)
	} else if op == "close" {
		receipt, err = a.Admission.Close(ctx, run.AdmissionCloseRequest{Change: c, Reason: reason, CauseReceipt: cause})
	} else {
		return receipt, fmt.Errorf("invalid control operation")
	}
	if err == nil || errors.Is(err, run.ErrAdmissionUnchanged) || errors.Is(err, run.ErrAdmissionConflict) {
		cp.Pending = nil
		if _, keepErr := a.Checkpoints.Retain(ctx, *cp); keepErr != nil {
			return receipt, keepErr
		}
	}
	return receipt, err
}
