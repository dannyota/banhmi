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
		if matches != 1 || associated.VersionKey != child.Scope.Subject.LeaseKey() || child.WorkflowID != "document-ingest/"+child.Country+"/"+w.SourceID+"/"+associated.VersionKey {
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

// RAW QUERY: ReadSourceLaunchTransferBinding. Transfer binds the immutable
// original delivery and its accepted window provenance, never a scheduled ID.
func readSourceLaunchTransferBinding(ctx context.Context, tx pgx.Tx, country, window, request string) (sourceLaunchTransferBinding, error) {
	b := sourceLaunchTransferBinding{RequestKey: request, Country: country, WindowKey: window, ResourceKind: "control", ResourceKey: country}
	err := tx.QueryRow(ctx, `SELECT d.id, d.delivery_kind, d.recipient_workflow_id, d.receipt_kind, d.receipt_key, d.expected_epoch, d.expected_hash, d.due_at
		FROM ingest.discovery_page p JOIN ingest.source_window w ON w.id = p.source_window_id
		JOIN ingest.execution_result r ON r.id = p.acceptance_result_id
		CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(r.payload->'SourcePayload'->'Launches') = 'array' THEN r.payload->'SourcePayload'->'Launches' ELSE '[]'::jsonb END) launch(value)
		JOIN ingest.delivery_record d ON d.deduplication_key = launch.value->>'RequestKey'
		WHERE w.window_key = $1 AND p.state = 'accepted' AND r.payload->>'SourceOperation' = 'accept_page'
		AND d.deduplication_key = $2 AND d.delivery_kind = 'launch' AND d.receipt_kind = 'execution'
		FOR UPDATE OF d`, window, request).Scan(&b.DeliveryID, &b.DeliveryKind, &b.RecipientWorkflowID, &b.ReceiptKind, &b.ReceiptKey, &b.ExpectedEpoch, &b.ExpectedHash, &b.DueAt)
	if err != nil {
		return b, grantError("read launch transfer binding", err)
	}
	return b, nil
}

// RAW QUERY: FindSourceLaunchTransfer. Window and original delivery locks
// enforce one transfer per intent across issuing executions and provenance.
func findSourceLaunchTransfer(ctx context.Context, tx pgx.Tx, request string) (string, json.RawMessage, error) {
	var key string
	var payload json.RawMessage
	err := tx.QueryRow(ctx, `SELECT r.result_key, r.payload->'SourcePayload'
		FROM ingest.execution_result r JOIN ingest.execution e ON e.id = r.execution_id
		WHERE e.resource_kind = 'source_window'
		AND r.substep = 'source-launch-transfer' AND r.shard = $1
		AND r.payload->'SourcePayload'->>'WindowKey' = e.resource_key
		AND r.payload->>'SourceOperation' = 'transfer_launch' ORDER BY r.id LIMIT 1`, request).Scan(&key, &payload)
	return key, payload, err
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
	return sourceTransaction(ctx, s, work, "", func(tx pgx.Tx, _ intakedb.Querier) ([]SourceTransferredLaunch, error) {
		// RAW QUERY: ReadDueTransferredSourceLaunches. Join due deliveries to
		// the exact transfer receipt, original delivery binding and window.
		rows, err := tx.Query(ctx, `SELECT w.window_key, d.id, d.deduplication_key, d.delivery_kind,
			d.recipient_workflow_id, d.receipt_kind, d.receipt_key, d.expected_epoch, d.expected_hash,
			d.due_at, d.acknowledged_at, launch.value->'Work'
			FROM ingest.delivery_record d JOIN ingest.execution_result transfer
			ON transfer.substep = 'source-launch-transfer' AND transfer.shard = d.deduplication_key
			JOIN ingest.execution issuer ON issuer.id = transfer.execution_id AND issuer.resource_kind = 'source_window'
			JOIN ingest.source_window w ON w.window_key = issuer.resource_key
			JOIN ingest.discovery_page p ON p.source_window_id = w.id AND p.state = 'accepted'
			JOIN ingest.execution_result accepted ON accepted.id = p.acceptance_result_id
			CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(accepted.payload->'SourcePayload'->'Launches') = 'array' THEN accepted.payload->'SourcePayload'->'Launches' ELSE '[]'::jsonb END) launch(value)
			WHERE d.delivery_kind = 'launch' AND d.acknowledged_at IS NULL AND d.due_at <= clock_timestamp()
			AND launch.value->>'RequestKey' = d.deduplication_key AND accepted.payload->>'SourceOperation' = 'accept_page'
			AND transfer.payload->>'SourceOperation' = 'transfer_launch'
			AND transfer.payload->'SourcePayload'->>'Country' = $1
			AND transfer.payload->'SourcePayload'->>'WindowKey' = w.window_key
			AND transfer.payload->'SourcePayload'->>'ResourceKind' = 'control'
			AND transfer.payload->'SourcePayload'->>'ResourceKey' = $1
			AND (transfer.payload->'SourcePayload'->>'DeliveryID')::bigint = d.id
			AND transfer.payload->'SourcePayload'->>'DeliveryKind' = d.delivery_kind
			AND (transfer.payload->'SourcePayload'->>'DueAt')::timestamptz = d.due_at
			AND transfer.payload->'SourcePayload'->>'RecipientWorkflowID' = d.recipient_workflow_id
			AND transfer.payload->'SourcePayload'->>'ReceiptKind' = d.receipt_kind
			AND transfer.payload->'SourcePayload'->>'ReceiptKey' = d.receipt_key
			AND (transfer.payload->'SourcePayload'->>'ExpectedEpoch')::bigint = d.expected_epoch
			AND transfer.payload->'SourcePayload'->>'ExpectedHash' = d.expected_hash
			ORDER BY d.due_at, d.id LIMIT $2`, work.Execution.Country, limit)
		if err != nil {
			return nil, err
		}
		var intents []SourceTransferredLaunch
		for rows.Next() {
			var intent SourceTransferredLaunch
			var raw json.RawMessage
			d := &intent.Delivery
			if err := rows.Scan(&intent.WindowKey, &d.ID, &d.DeduplicationKey, &d.DeliveryKind, &d.RecipientWorkflowID, &d.ReceiptKind, &d.ReceiptKey, &d.ExpectedEpoch, &d.ExpectedHash, &d.DueAt, &d.AcknowledgedAt, &raw); err != nil {
				rows.Close()
				return nil, err
			}
			if err := json.Unmarshal(raw, &intent.Child); err != nil {
				rows.Close()
				return nil, err
			}
			intents = append(intents, intent)
		}
		err = rows.Err()
		rows.Close()
		return intents, err
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
		// RAW QUERY: LockSourceWindowForReconciliation. Country admission and
		// the owning source or transferred-control lease are already validated.
		var id int64
		if err := tx.QueryRow(ctx, `SELECT id FROM ingest.source_window WHERE window_key = $1 FOR UPDATE`, acknowledgement.WindowKey).Scan(&id); err != nil {
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
		// RAW QUERY: ReadBoundSourceLaunch. Resolve only a launch recorded by
		// acceptance of this window, including its exact immutable child work.
		var deliveryID int64
		var storedWork json.RawMessage
		err = tx.QueryRow(ctx, `SELECT d.id, launch.value->'Work' FROM ingest.discovery_page p
			JOIN ingest.execution_result r ON r.id = p.acceptance_result_id
			CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(r.payload->'SourcePayload'->'Launches') = 'array' THEN r.payload->'SourcePayload'->'Launches' ELSE '[]'::jsonb END) launch(value)
			JOIN ingest.delivery_record d ON d.deduplication_key = launch.value->>'RequestKey'
			WHERE p.source_window_id = $1 AND p.state = 'accepted' AND r.payload->>'SourceOperation' = 'accept_page'
			AND d.deduplication_key = $2 AND d.delivery_kind = 'launch' AND d.receipt_kind = 'execution'
			AND d.receipt_key = $3 AND d.recipient_workflow_id = $4 AND d.expected_epoch = $5 AND d.expected_hash = $6
			FOR UPDATE OF d`, w.ID, acknowledgement.RequestKey, executionKey, child.Execution.WorkflowID, child.Execution.AdmissionEpoch, string(child.InputDigest)).Scan(&deliveryID, &storedWork)
		if err != nil {
			return grantError("resolve bound source launch", err)
		}
		expected, err := sourceJSON(child)
		if err != nil {
			return err
		}
		if !sameSourceJSON(storedWork, expected) {
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

		// RAW QUERY: AcknowledgeSourceLaunch. The bound delivery lock makes
		// retries preserve the first acknowledgement timestamp.
		if _, err := tx.Exec(ctx, `UPDATE ingest.delivery_record SET acknowledged_at = COALESCE(acknowledged_at, clock_timestamp()) WHERE id = $1`, deliveryID); err != nil {
			return fmt.Errorf("acknowledge source launch: %w", err)
		}
		return reconcileSourceWindow(ctx, tx, w)
	})
}
