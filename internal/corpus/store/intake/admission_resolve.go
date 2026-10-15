package intake

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

// Resolve authorizes the registered target and actor in one SQL snapshot.
// It acquires no grant, makes no observer call and writes no row.
func (a *DocumentAdmission) Resolve(ctx context.Context, r run.AdmissionResolveRequest) (run.AdmissionResolution, error) {
	if err := r.Validate(); err != nil {
		return run.AdmissionResolution{}, err
	}
	actor, err := json.Marshal(r.Actor)
	if err != nil {
		return run.AdmissionResolution{}, err
	}
	document, err := json.Marshal(r.DocumentWork)
	if err != nil {
		return run.AdmissionResolution{}, err
	}
	workflow := r.WorkflowID
	if r.DocumentWork != nil {
		workflow = r.DocumentWork.Execution.WorkflowID
	}
	return ledgerTransaction(ctx, a.ledger, func(_ pgx.Tx, q intakedb.Querier) (run.AdmissionResolution, error) {
		var result run.AdmissionResolution
		raw, err := q.ResolveDocumentAdmission(ctx, intakedb.ResolveDocumentAdmissionParams{
			Actor: actor, Writer: a.ledger.writer, WorkflowID: workflow, DocumentWork: document})
		if err != nil {
			return result, grantError("resolve document admission", err)
		}
		var snapshot struct {
			Resolution run.AdmissionResolution
			Receipts   []admissionWireReceipt
		}
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return run.AdmissionResolution{}, errors.Join(run.ErrAdmissionIntegrity, err)
		}
		result = snapshot.Resolution
		if result.WorkflowID != workflow || result.Found && (!result.State.Valid() || result.Revision < 1 || result.LifecycleReceipt == "") {
			return run.AdmissionResolution{}, run.ErrAdmissionIntegrity
		}
		seen := make(map[string]bool)
		for _, retained := range snapshot.Receipts {
			_, receipt, err := admissionRetainedReceipt(retained.row())
			if err != nil || receipt.Receipt.SlotKey != result.SlotKey || receipt.Receipt.Revision > result.Revision {
				return run.AdmissionResolution{}, errors.Join(run.ErrAdmissionIntegrity, err)
			}
			seen[receipt.Receipt.Key] = true
		}
		for _, reference := range []string{result.LifecycleReceipt, result.SettlementReceipt, result.CloseReceipt, result.ReleaseReceipt} {
			if reference != "" && !seen[reference] {
				return run.AdmissionResolution{}, run.ErrAdmissionIntegrity
			}
		}
		return result, nil
	})
}

// PostgreSQL row JSON uses SQL column names; sqlc structs intentionally remain
// private and have no JSON tags. Decode this projection explicitly.
type admissionWireReceipt struct {
	ReceiptKey       string          `json:"receipt_key"`
	OperationKey     string          `json:"operation_key"`
	SlotID           *int64          `json:"slot_id"`
	ActorExecutionID int64           `json:"actor_execution_id"`
	Operation        string          `json:"operation"`
	PayloadDigest    string          `json:"payload_digest"`
	LimitsRevision   int64           `json:"limits_revision"`
	ReceiptJSONBytes int32           `json:"receipt_json_bytes"`
	Request          json.RawMessage `json:"request"`
	Response         json.RawMessage `json:"response"`
}

func (r admissionWireReceipt) row() intakedb.IngestDocumentAdmissionReceipt {
	return intakedb.IngestDocumentAdmissionReceipt{ReceiptKey: r.ReceiptKey, OperationKey: r.OperationKey,
		SlotID: r.SlotID, ActorExecutionID: r.ActorExecutionID, Operation: r.Operation, PayloadDigest: r.PayloadDigest,
		LimitsRevision: r.LimitsRevision, ReceiptJsonBytes: r.ReceiptJSONBytes, Request: r.Request, Response: r.Response}
}

// Scan returns a bounded page under a live registered country control grant.
// An empty page denotes sweep end; the next sweep starts with an empty cursor.
func (a *DocumentAdmission) Scan(ctx context.Context, r run.AdmissionScan) ([]run.AdmissionItem, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return ledgerTransaction(ctx, a.ledger, func(tx pgx.Tx, q intakedb.Querier) ([]run.AdmissionItem, error) {
		if _, err := a.grants(ctx, tx, q, r.Actor, "", false); err != nil {
			return nil, err
		}
		after := r.AfterUpdatedAt
		if after.IsZero() {
			after = time.Unix(0, 0).UTC()
		}
		rows, err := q.ScanDocumentAdmissionSlots(ctx, intakedb.ScanDocumentAdmissionSlotsParams{
			AfterID: r.AfterID, AfterUpdatedAt: after, RowLimit: r.Limit})
		if err != nil {
			return nil, err
		}
		items := make([]run.AdmissionItem, 0, len(rows))
		for _, row := range rows {
			items = append(items, run.AdmissionItem{SlotKey: row.SlotKey, Revision: row.Revision, UpdatedAt: row.UpdatedAt, ID: row.ID})
		}
		return items, a.checkActor(ctx, q, r.Actor, false)
	})
}
