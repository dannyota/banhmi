package intake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

// DocumentAdmission persists country slots independently of evidence acceptance.
// It performs no transport calls while holding a database transaction.
type DocumentAdmission struct {
	ledger   *Ledger
	observer run.DocumentExecutionObserver
}

var _ run.DocumentAdmission = (*DocumentAdmission)(nil)

// NewDocumentAdmission validates dependencies without opening a connection.
func NewDocumentAdmission(db TransactionStarter, writer string, observer run.DocumentExecutionObserver) (*DocumentAdmission, error) {
	if observer == nil {
		return nil, fmt.Errorf("document execution observer is required")
	}
	l, err := NewLedger(db, writer, nil, nil)
	if err != nil {
		return nil, err
	}
	return &DocumentAdmission{ledger: l, observer: observer}, nil
}

type admissionGrant struct {
	actor       run.IntakeWork
	actorID     int64
	targetLease intakedb.IngestExecutionLease
	reconcile   bool
}

// Discovery happens before this helper. It never creates or discovers a lease
// while holding capacity. Duplicate actor/target resources are locked once.
func (a *DocumentAdmission) grants(ctx context.Context, tx pgx.Tx, q intakedb.Querier, actor run.IntakeWork, version string, reconciliation bool, extra ...[2]string) (admissionGrant, error) {
	g := admissionGrant{actor: actor, reconcile: reconciliation}
	if err := a.ledger.lockAdmission(ctx, tx, actor.Execution); err != nil {
		return g, err
	}
	kind, resource, err := leaseResource(actor.Execution.Scope)
	if err != nil {
		return g, err
	}
	resources := [][2]string{{kind, resource}}
	if version != "" && (kind != "document_version" || resource != version) {
		resources = append(resources, [2]string{"document_version", version})
	}
	for _, pair := range extra {
		duplicate := false
		for _, existing := range resources {
			duplicate = duplicate || pair == existing
		}
		if !duplicate {
			resources = append(resources, pair)
		}
	}
	sort.Slice(resources, func(i, j int) bool {
		return resources[i][0] < resources[j][0] || resources[i][0] == resources[j][0] && resources[i][1] < resources[j][1]
	})
	for _, pair := range resources {
		l, err := q.LockAdmissionTargetLease(ctx, intakedb.LockAdmissionTargetLeaseParams{ResourceKind: pair[0], ResourceKey: pair[1]})
		if err != nil {
			return g, grantError("lock admission lease", err)
		}
		if pair[0] == "document_version" && pair[1] == version {
			g.targetLease = l
		}
		// Validate the actor after each potentially blocking lock, even when
		// its own lease is later in the ordering. This read adds no row lock.
		if err := a.checkActor(ctx, q, actor, reconciliation); err != nil {
			return g, err
		}
	}
	g.actorID, _, err = resolveExecution(ctx, tx, actor)
	return g, err
}

func (a *DocumentAdmission) checkActor(ctx context.Context, q intakedb.Querier, actor run.IntakeWork, reconciliation bool) error {
	ok, err := q.AdmissionActorLive(ctx, intakedb.AdmissionActorLiveParams{Country: actor.Execution.Country,
		Writer: a.ledger.writer, Epoch: actor.Execution.AdmissionEpoch, Owner: actor.Execution.Owner,
		Fence: actor.Execution.AttemptFence, ResourceKind: admissionResourceKind(actor), ResourceKey: admissionResourceKey(actor), Reconciliation: reconciliation})
	if err != nil {
		return err
	}
	if !ok {
		return run.ErrStaleGrant
	}
	return nil
}

func admissionResourceKind(w run.IntakeWork) string {
	kind, _, _ := leaseResource(w.Execution.Scope)
	return kind
}

func admissionResourceKey(w run.IntakeWork) string {
	_, key, _ := leaseResource(w.Execution.Scope)
	return key
}

func admissionConfigured(l intakedb.IngestDocumentAdmissionLimit) error {
	if l.Revision == 0 {
		return run.ErrAdmissionUnconfigured
	}
	if l.ProcessingLimit == nil || l.OpenLimit == nil || l.ReceiptJsonBytes == nil || l.QuarantineExpiryUs == nil ||
		(run.DocumentAdmissionLimits{Processing: *l.ProcessingLimit, Open: *l.OpenLimit, ReceiptJSONBytes: *l.ReceiptJsonBytes,
			QuarantineExpiry: time.Duration(*l.QuarantineExpiryUs) * time.Microsecond}).Validate() != nil {
		return run.ErrAdmissionIntegrity
	}
	return nil
}

// Configure commits every bound and its receipt under a live control execution.
func (a *DocumentAdmission) Configure(ctx context.Context, r run.AdmissionConfigureRequest) (run.AdmissionReceipt, error) {
	if err := r.Validate(); err != nil {
		return run.AdmissionReceipt{}, err
	}
	return ledgerTransaction(ctx, a.ledger, func(tx pgx.Tx, q intakedb.Querier) (run.AdmissionReceipt, error) {
		g, err := a.grants(ctx, tx, q, r.Actor, "", false)
		if err != nil {
			return run.AdmissionReceipt{}, err
		}
		p := admissionPayload{Method: "configure", Country: r.Actor.Execution.Country, Target: "limits", Revision: r.ExpectedRevision, Limits: &r.Limits}
		l, err := q.LockDocumentAdmissionLimit(ctx)
		if err != nil {
			return run.AdmissionReceipt{}, err
		}
		if err := a.checkActor(ctx, q, r.Actor, false); err != nil {
			return run.AdmissionReceipt{}, err
		}
		stored, err := admissionReplay(ctx, q, p)
		if err != nil {
			return run.AdmissionReceipt{}, err
		}
		if stored != nil {
			return stored.Receipt, a.checkActor(ctx, q, r.Actor, false)
		}
		if l.Revision != r.ExpectedRevision {
			return run.AdmissionReceipt{}, run.ErrAdmissionConflict
		}
		l, err = q.ConfigureDocumentAdmissionLimit(ctx, intakedb.ConfigureDocumentAdmissionLimitParams{
			ProcessingLimit: r.Limits.Processing, OpenLimit: r.Limits.Open, ReceiptJsonBytes: r.Limits.ReceiptJSONBytes,
			QuarantineExpiryUs: r.Limits.QuarantineExpiry.Microseconds(), ExpectedRevision: r.ExpectedRevision})
		if err != nil {
			return run.AdmissionReceipt{}, err
		}
		response, err := admissionWriteReceipt(ctx, q, l, p, admissionWorkBinding(r.Actor, g.actorID), nil,
			admissionStoredResponse{Receipt: run.AdmissionReceipt{Revision: l.Revision}})
		if err != nil {
			return run.AdmissionReceipt{}, err
		}
		return response.Receipt, a.checkActor(ctx, q, r.Actor, false)
	})
}

// Reserve atomically accounts for both classes before permission to start.
// A normal refusal commits no slot, receipt, delivery acknowledgement or turn.
func (a *DocumentAdmission) Reserve(ctx context.Context, r run.AdmissionReserveRequest) (run.AdmissionResult, error) {
	if err := r.Validate(); err != nil {
		return run.AdmissionResult{}, err
	}
	return ledgerTransaction(ctx, a.ledger, func(tx pgx.Tx, q intakedb.Querier) (run.AdmissionResult, error) {
		var zero run.AdmissionResult
		// Resolve before locks solely to discover the immutable source window.
		launch, err := q.ResolveAdmissionLaunch(ctx, r.DeliveryKey)
		if err != nil {
			return zero, grantError("discover admission launch", err)
		}
		g, err := a.grants(ctx, tx, q, r.Actor, launch.DocumentVersionKey, false, [2]string{"source_window", launch.WindowKey})
		if err != nil {
			return zero, err
		}
		if _, err := q.LockSourceWindowForReconciliation(ctx, launch.WindowKey); err != nil {
			return zero, grantError("lock admission window", err)
		}
		if err := a.checkActor(ctx, q, r.Actor, false); err != nil {
			return zero, err
		}
		if _, err := q.LockAdmissionDelivery(ctx, r.DeliveryKey); err != nil {
			return zero, err
		}
		launch, err = q.ResolveAdmissionLaunch(ctx, r.DeliveryKey)
		if err != nil {
			return zero, grantError("recheck admission launch", err)
		}
		if err := a.launchAuthority(ctx, tx, q, r, launch); err != nil {
			return zero, err
		}
		childID, _, err := resolveExecution(ctx, tx, r.Child)
		if err != nil {
			return zero, err
		}
		if _, err := lockLease(ctx, tx, r.Child.Execution); err != nil {
			return zero, err
		}
		l, err := q.LockDocumentAdmissionLimit(ctx)
		if err != nil {
			return zero, err
		}
		if err := a.checkActor(ctx, q, r.Actor, false); err != nil {
			return zero, err
		}
		p := admissionPayload{Method: "reserve", Country: r.Actor.Execution.Country, Target: r.DeliveryKey, Child: admissionChildPayload(r.Child)}
		stored, err := admissionReplay(ctx, q, p)
		if err != nil {
			return zero, err
		}
		if stored != nil {
			return admissionResult(*stored), a.reserveFinal(ctx, q, r)
		}
		if err := admissionConfigured(l); err != nil {
			return zero, err
		}
		if launch.AcknowledgedAt != nil {
			return zero, run.ErrAdmissionConflict
		}
		now, err := q.AdmissionDatabaseTime(ctx)
		if err != nil {
			return zero, err
		}
		if launch.DueAt.After(now) {
			return zero, run.ErrAdmissionConflict
		}
		counts, err := q.CountDocumentAdmissionSlots(ctx)
		if err != nil {
			return zero, err
		}
		result := run.AdmissionResult{LimitsRevision: l.Revision, Processing: counts.Processing, Open: counts.Open}
		busy, err := q.AdmissionVersionBusy(ctx, launch.DocumentVersionKey)
		if err != nil {
			return zero, err
		}
		if busy {
			result.Refusal = run.AdmissionResourceBusy
			return result, a.reserveFinal(ctx, q, r)
		}
		result.Refusal = admissionCapacityRefusal(counts.Processing >= int64(*l.ProcessingLimit), counts.Open >= int64(*l.OpenLimit))
		if result.Refusal != "" {
			return result, a.reserveFinal(ctx, q, r)
		}
		heads, err := q.ReadAdmissionQueueHeads(ctx)
		if err != nil {
			return zero, err
		}
		contested := heads.Resume != ""
		if heads.Launch != r.DeliveryKey || contested && l.NextQueue != "launch" {
			result.Refusal = run.AdmissionNotTurn
			return result, a.reserveFinal(ctx, q, r)
		}
		key, err := newLedgerKey("slot")
		if err != nil {
			return zero, err
		}
		e := r.Child.Execution
		slot, err := q.InsertDocumentAdmissionSlot(ctx, intakedb.InsertDocumentAdmissionSlotParams{
			SlotKey: key, DeliveryID: launch.DeliveryID, WorkflowID: e.WorkflowID, DocumentVersionKey: launch.DocumentVersionKey,
			ProcessingAttemptKey: e.AttemptKey, OriginExecutionID: launch.OriginExecutionID, CurrentExecutionID: childID,
			AdmissionEpoch: e.AdmissionEpoch, LeaseOwner: e.Owner, AttemptFence: e.AttemptFence, QuarantineExpiryUs: *l.QuarantineExpiryUs})
		if err != nil {
			return zero, err
		}
		response, err := admissionWriteReceipt(ctx, q, l, p, admissionWorkBinding(r.Actor, g.actorID), &slot,
			admissionStoredResponse{Processing: counts.Processing + 1, Open: counts.Open + 1})
		if err != nil {
			return zero, err
		}
		if contested {
			if _, err := q.AdvanceAdmissionQueueTurn(ctx, "launch"); err != nil {
				return zero, err
			}
		}
		return admissionResult(response), a.reserveFinal(ctx, q, r)
	})
}

func admissionResult(r admissionStoredResponse) run.AdmissionResult {
	return run.AdmissionResult{Receipt: &r.Receipt, LimitsRevision: r.LimitsRevision, Processing: r.Processing, Open: r.Open}
}

func admissionCapacityRefusal(processing, open bool) run.AdmissionRefusal {
	if processing && open {
		return run.AdmissionBothFull
	}
	if processing {
		return run.AdmissionProcessingFull
	}
	if open {
		return run.AdmissionOpenFull
	}
	return ""
}

func (a *DocumentAdmission) reserveFinal(ctx context.Context, q intakedb.Querier, r run.AdmissionReserveRequest) error {
	if err := a.checkActor(ctx, q, r.Actor, false); err != nil {
		return err
	}
	return a.checkActor(ctx, q, r.Child, false)
}

func (a *DocumentAdmission) launchAuthority(ctx context.Context, tx pgx.Tx, q intakedb.Querier, r run.AdmissionReserveRequest, l intakedb.ResolveAdmissionLaunchRow) error {
	var original run.IntakeWork
	if err := json.Unmarshal(l.Work, &original); err != nil {
		return errors.Join(run.ErrAdmissionIntegrity, err)
	}
	if !reflect.DeepEqual(admissionChildPayload(original), admissionChildPayload(r.Child)) ||
		l.DocumentVersionKey != r.Child.Execution.Scope.Subject.LeaseKey() || l.WorkflowID != r.Child.Execution.WorkflowID ||
		l.ProcessingAttemptKey != r.Child.Execution.AttemptKey || l.InputDigest != string(r.Child.InputDigest) ||
		l.RecipeDigest != string(r.Child.RecipeDigest) || l.BudgetDigest != string(r.Child.BudgetDigest) {
		return run.ErrPayloadConflict
	}
	_, transferred, transferErr := findSourceLaunchTransfer(ctx, tx, r.DeliveryKey)
	if transferErr != nil && !errors.Is(transferErr, pgx.ErrNoRows) {
		return transferErr
	}
	if r.Actor.Execution.WorkflowKind == run.SourceSync {
		if r.Actor.Execution.Scope.Subject.LeaseKey() != l.WindowKey || transferErr == nil {
			return run.ErrStaleGrant
		}
	} else {
		if transferErr != nil {
			return run.ErrStaleGrant
		}
		binding, err := readSourceLaunchTransferBinding(ctx, tx, r.Actor.Execution.Country, l.WindowKey, r.DeliveryKey)
		if err != nil {
			return err
		}
		expected, err := sourceJSON(binding)
		if err != nil || !sameSourceJSON(transferred, expected) {
			return errors.Join(run.ErrAdmissionIntegrity, err)
		}
	}
	// Transfer never overrides cancellation of the source resource.
	cancelled, err := q.AdmissionSourceCancelled(ctx, l.WindowKey)
	if err != nil {
		return err
	}
	if cancelled {
		return run.ErrStaleGrant
	}
	return nil
}
