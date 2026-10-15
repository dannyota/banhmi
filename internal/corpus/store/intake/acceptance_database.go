package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

// acceptanceWrites keeps the shared recording helpers while preventing their
// RETURNING clauses from materializing uncharged JSON columns.
type acceptanceWrites struct{ intakedb.Querier }

func (q acceptanceWrites) InsertExecutionResult(ctx context.Context, p intakedb.InsertExecutionResultParams) (intakedb.InsertExecutionResultRow, error) {
	id, err := q.InsertAcceptanceResult(ctx, intakedb.InsertAcceptanceResultParams(p))
	return intakedb.InsertExecutionResultRow{ID: id}, err
}

func (q acceptanceWrites) InsertRevisionAcceptance(ctx context.Context, p intakedb.InsertRevisionAcceptanceParams) (intakedb.IngestRevisionAcceptance, error) {
	id, err := q.InsertAcceptanceEvent(ctx, intakedb.InsertAcceptanceEventParams(p))
	return intakedb.IngestRevisionAcceptance{ID: id}, err
}

// databaseJSON charges immutable (column role, row identity) dependencies before
// fetching their bytes. The size predicate on the second read fails closed even
// if a privileged actor corrupts the immutable row between statements.
func (v *acceptanceEvidence) databaseJSON(ctx context.Context, kind, identity string) (json.RawMessage, error) {
	dependency := kind + ":" + identity
	if data, ok := v.database[dependency]; ok {
		return data, v.bound(ctx, 0, 0)
	}
	size, err := v.q.ReadAcceptanceJSONSize(ctx, intakedb.ReadAcceptanceJSONSizeParams{Kind: kind, Identity: identity})
	if err != nil {
		return nil, err
	}
	if size < 0 {
		return nil, fmt.Errorf("acceptance database JSON size is invalid")
	}
	if err := v.bound(ctx, 1, size); err != nil {
		return nil, err
	}
	data, err := v.q.ReadAcceptanceJSON(ctx, intakedb.ReadAcceptanceJSONParams{Kind: kind, Identity: identity, ByteSize: size})
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size {
		return nil, fmt.Errorf("acceptance database JSON changed after charging")
	}
	v.database[dependency] = data
	return data, nil
}

func (v *acceptanceEvidence) execution(ctx context.Context) (int64, error) {
	w, e := v.r.Work, v.r.Work.Execution
	kind, key, err := leaseResource(e.Scope)
	if err != nil {
		return 0, err
	}
	row, err := v.q.ResolveAcceptanceExecution(ctx, intakedb.ResolveAcceptanceExecutionParams{
		WorkflowID: e.WorkflowID, WorkflowKind: string(e.WorkflowKind), ProcessingAttemptKey: e.AttemptKey,
		AttemptFence: e.AttemptFence, AdmissionEpoch: e.AdmissionEpoch, ScopeKind: string(e.Scope.Kind),
		ResourceKind: kind, ResourceKey: key, InputDigest: string(w.InputDigest), RecipeDigest: string(w.RecipeDigest), BudgetDigest: string(w.BudgetDigest),
	})
	if err != nil {
		return 0, grantError("resolve acceptance execution", err)
	}
	data, err := v.databaseJSON(ctx, "budget", row.ExecutionKey)
	if err != nil {
		return 0, err
	}
	var frozen run.Budget
	if err := json.Unmarshal(data, &frozen); err != nil {
		return 0, err
	}
	if frozen != w.Budget {
		return 0, fmt.Errorf("frozen budget: %w", run.ErrStaleGrant)
	}
	return row.ID, nil
}

func (v *acceptanceEvidence) result(ctx context.Context) (intakedb.IngestExecutionResult, error) {
	row, err := v.q.FindAcceptanceResult(ctx, v.r.IdempotencyKey)
	if err != nil {
		return intakedb.IngestExecutionResult{}, err
	}
	data, err := v.databaseJSON(ctx, "payload", v.r.IdempotencyKey)
	return intakedb.IngestExecutionResult{ID: row.ID, ExecutionID: row.ExecutionID, ResultKey: row.ResultKey, CreatedAt: row.CreatedAt, Payload: data}, err
}

func (v *acceptanceEvidence) receipt(ctx context.Context, result intakedb.IngestExecutionResult) (run.IntakeReceipt, error) {
	receipt := run.IntakeReceipt{ResultID: result.ResultKey}
	identity, err := v.q.ReadAcceptanceReceiptExecution(ctx, result.ExecutionID)
	if err != nil {
		return receipt, err
	}
	receipt.ExecutionID = identity
	data, err := v.databaseJSON(ctx, "receipts", identity)
	if err != nil {
		return receipt, err
	}
	var receipts []string
	if err := json.Unmarshal(data, &receipts); err != nil {
		return receipt, err
	}
	if len(receipts) != 0 {
		receipt.RunReceipt = receipts[0]
	}
	if err := json.Unmarshal(result.Payload, &receipt.Result); err != nil {
		return receipt, err
	}
	return receipt, nil
}

func (v *acceptanceEvidence) eventGaps(ctx context.Context, id int64) (json.RawMessage, error) {
	return v.databaseJSON(ctx, "gaps", strconv.FormatInt(id, 10))
}
