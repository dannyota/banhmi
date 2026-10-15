package intake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/source/api"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

// SourceLaunch binds a decoded follow-up to an already registered child
// execution. The child must name the discovered document version and source.
type SourceLaunch struct {
	// RequestKey matches one decoded detail follow-up and deduplicates delivery.
	RequestKey string
	// Work binds the child's registered execution and immutable inputs.
	Work run.IntakeWork
	// DueAt is the immutable launch scheduling time.
	DueAt time.Time
}

func registerSourceLaunches(ctx context.Context, tx pgx.Tx, q intakedb.Querier, work run.IntakeWork, w intakedb.IngestSourceWindow, a DiscoveryAcceptance) error {
	launches := make(map[string]SourceLaunch, len(a.Launches))
	for _, launch := range a.Launches {
		if _, exists := launches[launch.RequestKey]; exists {
			return fmt.Errorf("duplicate child launch: %w", run.ErrPayloadConflict)
		}
		launches[launch.RequestKey] = launch
	}
	var deliveries []intakedb.InsertDeliveryRecordParams
	for _, follow := range a.Page.FollowUps {
		if follow.Purpose == model.PurposeListing || follow.Purpose == model.PurposeNumber {
			continue
		}
		launch, exists := launches[follow.RequestKey]
		if !exists || follow.Purpose != model.PurposeDetail {
			return fmt.Errorf("decoded follow-up requires a detail child launch: %w", run.ErrPayloadConflict)
		}
		delete(launches, follow.RequestKey)
		child := launch.Work.Execution
		if err := launch.Work.Validate(); err != nil {
			return fmt.Errorf("child work: %w", err)
		}
		if child.Country != work.Execution.Country || child.AdmissionEpoch != work.Execution.AdmissionEpoch || child.WorkflowKind != run.DocumentIngest || launch.DueAt.IsZero() || launch.Work.BudgetDigest != work.BudgetDigest || launch.Work.Budget != work.Budget {
			return fmt.Errorf("child launch changes country, epoch or frozen budget")
		}
		matches := 0
		var associated api.DecodedObservation
		for _, o := range a.Page.Observations {
			if o.Ordinal == follow.Ordinal && o.Capture.CaptureKey == follow.ParentCapture {
				matches++
				associated = o
			}
		}
		if matches != 1 || associated.VersionKey != child.Scope.Subject.LeaseKey() || child.WorkflowID != "document-ingest/"+child.Country+"/"+associated.VersionKey+"/"+child.AttemptKey {
			return fmt.Errorf("child launch differs from exact follow-up observation: %w", run.ErrPayloadConflict)
		}
		_, executionKey, err := resolveExecution(ctx, tx, launch.Work)
		if err != nil {
			return fmt.Errorf("resolve launch child: %w", err)
		}
		deliveries = append(deliveries, intakedb.InsertDeliveryRecordParams{DeduplicationKey: follow.RequestKey, DeliveryKind: "launch", RecipientWorkflowID: child.WorkflowID, ReceiptKind: "execution", ReceiptKey: executionKey, ExpectedEpoch: child.AdmissionEpoch, ExpectedHash: string(launch.Work.InputDigest), DueAt: launch.DueAt})
	}
	if len(launches) != 0 {
		return fmt.Errorf("child launch is absent from decoded follow-ups: %w", run.ErrPayloadConflict)
	}
	// UNIQUE conflicts wait on uncommitted inserts. Every window must insert
	// launch keys in byte order, independently of the retained decoder order.
	sort.Slice(deliveries, func(i, j int) bool { return deliveries[i].DeduplicationKey < deliveries[j].DeduplicationKey })
	for _, delivery := range deliveries {
		if _, err := insertDelivery(ctx, q, delivery); err != nil {
			return err
		}
	}
	return nil
}

// SourceLaunchTransfer explicitly assigns an existing source launch intent to
// country control. It does not authorize creating or starting another child.
type SourceLaunchTransfer struct {
	// RequestKey selects the existing immutable launch intent.
	RequestKey string
	// RecipientWorkflowID names the country ControlReconcile workflow.
	RecipientWorkflowID string
}

// TransferLaunch records a source owner's explicit handoff before its schedule
// is removed. Normal live SourceSync admission and lease checks apply.
func (s *Sources) TransferLaunch(ctx context.Context, work run.IntakeWork, transfer SourceLaunchTransfer) (run.IntakeReceipt, error) {
	control := work.Execution
	control.WorkflowKind, control.WorkflowID = run.ControlReconcile, transfer.RecipientWorkflowID
	control.Scope = run.ExecutionScope{Kind: run.ScopeControl, CountryControl: run.CountryControlKey(work.Execution.Country)}
	if work.Execution.WorkflowKind != run.SourceSync || control.Validate() != nil {
		return run.IntakeReceipt{}, fmt.Errorf("source launch transfer owner or recipient: %w", run.ErrStaleGrant)
	}
	work.Substep, work.Shard = "source-launch-transfer", transfer.RequestKey
	return s.sourceMutation(ctx, work, run.SubjectSourceWindow, "transfer_launch", transfer, func(_ pgx.Tx, _ intakedb.Querier, _ intakedb.IngestExecutionResult) error { return nil })
}

type sourceLaunchTransferBinding struct {
	RequestKey          string
	Country             string
	WindowKey           string
	ResourceKind        string
	ResourceKey         string
	DeliveryID          int64
	DeliveryKind        string
	DueAt               time.Time
	RecipientWorkflowID string
	ReceiptKind         string
	ReceiptKey          string
	ExpectedEpoch       int64
	ExpectedHash        string
}

// Transfer binds the immutable
// original delivery and its accepted window provenance, never a scheduled ID.
func readSourceLaunchTransferBinding(ctx context.Context, tx pgx.Tx, country, window, request string) (sourceLaunchTransferBinding, error) {
	b := sourceLaunchTransferBinding{RequestKey: request, Country: country, WindowKey: window, ResourceKind: "control", ResourceKey: country}
	row, err := intakedb.New(tx).ReadSourceLaunchTransferBinding(ctx, intakedb.ReadSourceLaunchTransferBindingParams{P1: window, P2: request})
	b.DeliveryID, b.DeliveryKind, b.RecipientWorkflowID = row.ID, row.DeliveryKind, row.RecipientWorkflowID
	b.ReceiptKind, b.ReceiptKey, b.ExpectedEpoch, b.ExpectedHash, b.DueAt = row.ReceiptKind, row.ReceiptKey, row.ExpectedEpoch, row.ExpectedHash, row.DueAt
	if err != nil {
		return b, grantError("read launch transfer binding", err)
	}
	return b, nil
}

// Window and original delivery locks
// enforce one transfer per intent across issuing executions and provenance.
func findSourceLaunchTransfer(ctx context.Context, tx pgx.Tx, request string) (string, json.RawMessage, error) {
	row, err := intakedb.New(tx).FindSourceLaunchTransfer(ctx, request)
	return row.ResultKey, row.Payload, err
}

// SourceTransferredLaunch retains the original child identity for control.
type SourceTransferredLaunch struct {
	WindowKey string
	Delivery  intakedb.IngestDeliveryRecord
	Child     run.IntakeWork
}

// DueTransferredLaunches reads a bounded batch under the current country
// control lease. It never substitutes the control workflow for the child.
func (s *Sources) DueTransferredLaunches(ctx context.Context, work run.IntakeWork, limit int32) ([]SourceTransferredLaunch, error) {
	if limit <= 0 || int(limit) > work.Budget.MaxItems {
		return nil, fmt.Errorf("transferred launch read exceeds work budget")
	}
	return sourceTransaction(ctx, s, work, "", func(tx pgx.Tx, q intakedb.Querier) ([]SourceTransferredLaunch, error) {
		// Join due deliveries to
		// the exact transfer receipt, original delivery binding and window.
		rows, err := q.ReadDueTransferredSourceLaunches(ctx, intakedb.ReadDueTransferredSourceLaunchesParams{P1: work.Execution.Country, P2: limit})
		if err != nil {
			return nil, err
		}
		var intents []SourceTransferredLaunch
		for _, row := range rows {
			var intent SourceTransferredLaunch
			intent.WindowKey = row.WindowKey
			d := &intent.Delivery
			d.ID, d.DeduplicationKey, d.DeliveryKind, d.RecipientWorkflowID = row.ID, row.DeduplicationKey, row.DeliveryKind, row.RecipientWorkflowID
			d.ReceiptKind, d.ReceiptKey, d.ExpectedEpoch, d.ExpectedHash = row.ReceiptKind, row.ReceiptKey, row.ExpectedEpoch, row.ExpectedHash
			d.DueAt, d.AcknowledgedAt = row.DueAt, row.AcknowledgedAt
			if err := json.Unmarshal(row.Work, &intent.Child); err != nil {
				return nil, err
			}
			intents = append(intents, intent)
		}
		return intents, nil
	})
}

// SourceLaunchAcknowledgement binds a confirmed child start to its exact
// persisted launch intent. Child and ExecutionKey identify the started execution.
type SourceLaunchAcknowledgement struct {
	// WindowKey selects the parent accounting window.
	WindowKey string
	// RequestKey selects its immutable launch delivery.
	RequestKey string
	// Child retains the exact registered work in the accepted launch intent.
	Child run.IntakeWork
	// ExecutionKey is the confirmed child execution receipt, not a workflow ID.
	ExecutionKey string
}

// AcknowledgeLaunch reconciles an existing started child under its SourceSync
// window owner at Discover. Current admission, lease fence, expiry and frozen
// execution inputs remain mandatory after cancellation. It cannot create launches.
// Country control requires TransferLaunch for that exact intent and recipient.
// Replay preserves the original acknowledgement time and result receipt.
func (s *Sources) AcknowledgeLaunch(ctx context.Context, work run.IntakeWork, acknowledgement SourceLaunchAcknowledgement) (run.IntakeReceipt, error) {
	if err := acknowledgement.Child.Validate(); err != nil {
		return run.IntakeReceipt{}, fmt.Errorf("acknowledged child: %w", err)
	}
	if err := model.ValidateEnvelopeKey(acknowledgement.ExecutionKey, "execution"); err != nil {
		return run.IntakeReceipt{}, fmt.Errorf("child execution receipt: %w", err)
	}
	kind := run.SubjectSourceWindow
	if work.Execution.Scope.Kind == run.ScopeControl && work.Execution.WorkflowKind == run.ControlReconcile {
		kind = ""
	} else if work.Execution.WorkflowKind != run.SourceSync || work.Execution.Scope.Subject.LeaseKey() != acknowledgement.WindowKey {
		return run.IntakeReceipt{}, fmt.Errorf("launch acknowledgement owner: %w", run.ErrStaleGrant)
	}
	return s.sourceMutation(ctx, work, kind, "acknowledge_launch", acknowledgement, func(tx pgx.Tx, q intakedb.Querier, _ intakedb.IngestExecutionResult) error {
		// Country admission and
		// the owning source or transferred-control lease are already validated.
		if _, err := q.LockSourceWindowForReconciliation(ctx, acknowledgement.WindowKey); err != nil {
			return grantError("lock acknowledgement window", err)
		}
		w, err := q.ReadSourceWindow(ctx, acknowledgement.WindowKey)
		if err != nil {
			return grantError("read acknowledgement window", err)
		}
		child := acknowledgement.Child
		if child.Execution.Country != work.Execution.Country || child.Execution.WorkflowKind != run.DocumentIngest {
			return fmt.Errorf("acknowledgement child country or workflow: %w", run.ErrStaleGrant)
		}
		_, executionKey, err := resolveExecution(ctx, tx, child)
		if err != nil {
			return err
		}
		if executionKey != acknowledgement.ExecutionKey {
			return fmt.Errorf("acknowledgement execution receipt: %w", run.ErrPayloadConflict)
		}
		// Resolve only a launch recorded by
		// acceptance of this window, including its exact immutable child work.
		bound, err := q.ReadBoundSourceLaunch(ctx, intakedb.ReadBoundSourceLaunchParams{P1: w.ID, P2: acknowledgement.RequestKey,
			P3: executionKey, P4: child.Execution.WorkflowID, P5: child.Execution.AdmissionEpoch, P6: string(child.InputDigest)})
		if err != nil {
			return grantError("resolve bound source launch", err)
		}
		expected, err := sourceJSON(child)
		if err != nil {
			return err
		}
		if !sameSourceJSON(bound.Work, expected) {
			return fmt.Errorf("acknowledgement child payload: %w", run.ErrPayloadConflict)
		}
		_, transferred, transferErr := findSourceLaunchTransfer(ctx, tx, acknowledgement.RequestKey)
		if transferErr != nil && !errors.Is(transferErr, pgx.ErrNoRows) {
			return transferErr
		}
		if kind == "" {
			if transferErr != nil {
				return grantError("read source launch transfer", transferErr)
			}
			binding, err := readSourceLaunchTransferBinding(ctx, tx, work.Execution.Country, acknowledgement.WindowKey, acknowledgement.RequestKey)
			if err != nil {
				return err
			}
			expected, err := sourceJSON(binding)
			if err != nil {
				return err
			}
			if !sameSourceJSON(transferred, expected) {
				return fmt.Errorf("transferred launch binding: %w", run.ErrPayloadConflict)
			}
		} else if transferErr == nil {
			return fmt.Errorf("source launch authority transferred: %w", run.ErrStaleGrant)
		}

		// The bound delivery lock makes
		// retries preserve the first acknowledgement timestamp.
		if err := q.AcknowledgeSourceLaunch(ctx, bound.ID); err != nil {
			return fmt.Errorf("acknowledge source launch: %w", err)
		}
		return reconcileSourceWindow(ctx, tx, w)
	})
}

func insertDelivery(ctx context.Context, q intakedb.Querier, p intakedb.InsertDeliveryRecordParams) (intakedb.IngestDeliveryRecord, error) {
	// PostgreSQL timestamps retain microseconds; compare the persisted intent
	// at that precision, including on the first insertion.
	p.DueAt = p.DueAt.UTC().Truncate(time.Microsecond)
	_, err := q.InsertDeliveryRecord(ctx, p)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return intakedb.IngestDeliveryRecord{}, fmt.Errorf("insert delivery: %w", err)
	}
	v, err := q.FindDeliveryRecord(ctx, p.DeduplicationKey)
	if err != nil {
		return v, fmt.Errorf("resolve delivery winner: %w", err)
	}
	if v.DeliveryKind != p.DeliveryKind || v.RecipientWorkflowID != p.RecipientWorkflowID || v.ReceiptKind != p.ReceiptKind ||
		v.ReceiptKey != p.ReceiptKey || v.ExpectedEpoch != p.ExpectedEpoch || v.ExpectedHash != p.ExpectedHash || !v.DueAt.Equal(p.DueAt) {
		return v, fmt.Errorf("delivery payload: %w", run.ErrPayloadConflict)
	}
	return v, nil
}

// RecordDelivery creates an immutable launch, decision or completion intent
// under a current execution grant. Apply creates its completion intent itself.
func (l *Ledger) RecordDelivery(ctx context.Context, work run.IntakeWork, intent intakedb.InsertDeliveryRecordParams) (intakedb.IngestDeliveryRecord, error) {
	if err := work.Validate(); err != nil {
		return intakedb.IngestDeliveryRecord{}, fmt.Errorf("delivery work: %w", err)
	}
	if intent.ExpectedEpoch != work.Execution.AdmissionEpoch || intent.ExpectedHash == "" || intent.DueAt.IsZero() {
		return intakedb.IngestDeliveryRecord{}, fmt.Errorf("delivery must bind epoch, hash and due time")
	}
	return ledgerTransaction(ctx, l, func(tx pgx.Tx, q intakedb.Querier) (intakedb.IngestDeliveryRecord, error) {
		if err := l.lockAdmission(ctx, tx, work.Execution); err != nil {
			return intakedb.IngestDeliveryRecord{}, err
		}
		if _, err := lockLease(ctx, tx, work.Execution); err != nil {
			return intakedb.IngestDeliveryRecord{}, err
		}
		executionID, executionKey, err := resolveExecution(ctx, tx, work)
		if err != nil {
			return intakedb.IngestDeliveryRecord{}, err
		}
		if err := bindDelivery(ctx, tx, work, executionID, executionKey, intent); err != nil {
			return intakedb.IngestDeliveryRecord{}, err
		}
		v, err := insertDelivery(ctx, q, intent)
		if err != nil {
			return v, err
		}
		if _, err := lockLease(ctx, tx, work.Execution); err != nil {
			return v, err
		}
		return v, nil
	})
}

// A valid key
// grammar alone never authorizes delivery of somebody else's receipt.
func bindDelivery(ctx context.Context, tx pgx.Tx, work run.IntakeWork, executionID int64, executionKey string, p intakedb.InsertDeliveryRecordParams) error {
	var hash string
	var err error
	switch p.ReceiptKind {
	case "execution":
		if p.DeliveryKind != "launch" || p.ReceiptKey != executionKey || p.RecipientWorkflowID != work.Execution.WorkflowID {
			return fmt.Errorf("launch must reference and target its granted execution")
		}
		hash = string(work.InputDigest)
	case "result":
		if p.DeliveryKind != "completion" {
			return fmt.Errorf("result delivery must be completion")
		}
		hash, err = intakedb.New(tx).ResolveDeliveryResult(ctx, intakedb.ResolveDeliveryResultParams{P1: p.ReceiptKey, P2: executionID})
	case "decision":
		if p.DeliveryKind != "decision" {
			return fmt.Errorf("decision receipt requires decision delivery")
		}
		hash, err = intakedb.New(tx).ResolveDeliveryDecision(ctx, intakedb.ResolveDeliveryDecisionParams{
			P1: p.ReceiptKey, P2: executionID, P3: work.Execution.AdmissionEpoch, P4: work.Execution.AttemptFence})
	default:
		return fmt.Errorf("unknown delivery receipt kind")
	}
	if err != nil {
		return grantError("resolve delivery receipt", err)
	}
	if hash != p.ExpectedHash {
		return fmt.Errorf("delivery hash: %w", run.ErrPayloadConflict)
	}
	return nil
}

// DueDeliveries scans at most limit unacknowledged intents using database time.
func (l *Ledger) DueDeliveries(ctx context.Context, limit int32) ([]intakedb.IngestDeliveryRecord, error) {
	if limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("delivery limit must be between 1 and 1000")
	}
	return ledgerTransaction(ctx, l, func(_ pgx.Tx, q intakedb.Querier) ([]intakedb.IngestDeliveryRecord, error) {
		v, err := q.ListDueUnacknowledgedDelivery(ctx, limit)
		if err != nil {
			return nil, fmt.Errorf("scan due deliveries: %w", err)
		}
		return v, nil
	})
}
