package intake

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/blob"
	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

// TransactionStarter starts independent transactions, as a pgx pool does.
// Factories must honor READ COMMITTED and must not return nested transactions.
type TransactionStarter interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

// AcceptanceVerifier resolves trusted evidence within the ledger transaction.
// It must verify original/candidate/report/output bindings, mandatory gates on
// exact retained and excluded ranges, current actor authority and action, and
// exact scoped gaps. Reprocessing requires the authoritative prior revision,
// candidate and reports, passing comparison and owner adjudication of critical
// changes, even on replay. It must not commit, roll back or mutate the transaction.
// This port is required for any acceptance until the later cross-store adapter
// supplies these checks. A caller's decision evidence is never authority.
type AcceptanceVerifier interface {
	VerifyAcceptance(context.Context, pgx.Tx, run.IntakeApplyRequest, json.RawMessage) ([]model.GapRef, error)
}

// Ledger implements fenced intake persistence over the generated ledger queries.
// Configure it with the admitted writer identity, immutable object storage and
// a trusted acceptance verifier. Missing storage or verification fails closed
// when the request needs it. Construction performs no I/O.
type Ledger struct {
	db       TransactionStarter
	writer   string
	objects  blob.Store
	verifier AcceptanceVerifier
}

var _ run.IntakeUnitOfWork = (*Ledger)(nil)

// NewLedger constructs a ledger without opening a connection.
func NewLedger(db TransactionStarter, writer string, objects blob.Store, verifier AcceptanceVerifier) (*Ledger, error) {
	if db == nil {
		return nil, fmt.Errorf("transaction starter is required")
	}
	if err := model.ValidateKey(writer); err != nil {
		return nil, fmt.Errorf("writer identity: %w", err)
	}
	return &Ledger{db: db, writer: writer, objects: objects, verifier: verifier}, nil
}

func ledgerTransaction[T any](ctx context.Context, l *Ledger, fn func(pgx.Tx, intakedb.Querier) (T, error)) (T, error) {
	var zero T
	tx, err := l.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return zero, fmt.Errorf("begin ledger transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	value, err := fn(tx, intakedb.New(tx))
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, fmt.Errorf("commit ledger transaction: %w", err)
	}
	return value, nil
}

func grantError(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, run.ErrStaleGrant)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// Admission reads the current country writer grant.
func (l *Ledger) Admission(ctx context.Context) (intakedb.IngestCountryAdmission, error) {
	return ledgerTransaction(ctx, l, func(_ pgx.Tx, q intakedb.Querier) (intakedb.IngestCountryAdmission, error) {
		v, err := q.ReadAdmission(ctx)
		if err != nil {
			return v, fmt.Errorf("read admission: %w", err)
		}
		return v, nil
	})
}

// RotateAdmission advances an expected epoch and sets the replacement writer.
// Recovery must separately enforce its external recovery watermark.
func (l *Ledger) RotateAdmission(ctx context.Context, epoch int64, writer string, enabled bool) (intakedb.IngestCountryAdmission, error) {
	if epoch <= 0 {
		return intakedb.IngestCountryAdmission{}, fmt.Errorf("positive epoch is required")
	}
	if err := model.ValidateKey(writer); err != nil {
		return intakedb.IngestCountryAdmission{}, fmt.Errorf("replacement writer: %w", err)
	}
	return ledgerTransaction(ctx, l, func(_ pgx.Tx, q intakedb.Querier) (intakedb.IngestCountryAdmission, error) {
		v, err := q.RotateAdmissionEpoch(ctx, intakedb.RotateAdmissionEpochParams{AdmissionEpoch: epoch, AdmittedWriter: writer, Enabled: enabled})
		if err != nil {
			return v, grantError("rotate admission", err)
		}
		return v, nil
	})
}

func leaseResource(scope run.ExecutionScope) (string, string, error) {
	if err := scope.Validate(); err != nil {
		return "", "", fmt.Errorf("lease scope: %w", err)
	}
	switch scope.Kind {
	case run.ScopeIntake:
		return string(scope.Subject.Kind), scope.Subject.LeaseKey(), nil
	case run.ScopeBatch:
		return "batch", string(scope.BatchCollection), nil
	case run.ScopeControl:
		return "control", string(scope.CountryControl), nil
	default:
		return "", "", fmt.Errorf("intake ledger cannot use build scope")
	}
}

// ClaimLease allocates the first fence for an unclaimed intake resource.
func (l *Ledger) ClaimLease(ctx context.Context, epoch int64, scope run.ExecutionScope, owner string, expires time.Time) (intakedb.IngestExecutionLease, error) {
	return l.lease(ctx, epoch, scope, owner, expires, false)
}

// ReclaimLease increments the fence of expired, cancelled or older-epoch work.
func (l *Ledger) ReclaimLease(ctx context.Context, epoch int64, scope run.ExecutionScope, owner string, expires time.Time) (intakedb.IngestExecutionLease, error) {
	return l.lease(ctx, epoch, scope, owner, expires, true)
}

func (l *Ledger) lease(ctx context.Context, epoch int64, scope run.ExecutionScope, owner string, expires time.Time, reclaim bool) (intakedb.IngestExecutionLease, error) {
	kind, key, err := leaseResource(scope)
	if err != nil {
		return intakedb.IngestExecutionLease{}, err
	}
	if epoch <= 0 || expires.IsZero() {
		return intakedb.IngestExecutionLease{}, fmt.Errorf("epoch and lease expiry are required")
	}
	if err := model.ValidateKey(owner); err != nil {
		return intakedb.IngestExecutionLease{}, fmt.Errorf("lease owner: %w", err)
	}
	return ledgerTransaction(ctx, l, func(tx pgx.Tx, q intakedb.Querier) (intakedb.IngestExecutionLease, error) {
		if scope.Kind == run.ScopeControl {
			if err := l.lockAdmission(ctx, tx, run.ScopedExecution{Country: string(scope.CountryControl), AdmissionEpoch: epoch}); err != nil {
				return intakedb.IngestExecutionLease{}, err
			}
		}
		p := intakedb.ClaimExecutionLeaseParams{AdmissionEpoch: epoch, AdmittedWriter: l.writer, ResourceKind: kind, ResourceKey: key, Owner: owner, ExpiresAt: expires}
		var v intakedb.IngestExecutionLease
		var err error
		if reclaim {
			v, err = q.ReclaimExecutionLease(ctx, intakedb.ReclaimExecutionLeaseParams(p))
		} else {
			v, err = q.ClaimExecutionLease(ctx, p)
		}
		if err != nil {
			return v, grantError("allocate lease", err)
		}
		return v, nil
	})
}

func newLedgerKey(kind string) (string, error) {
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", fmt.Errorf("allocate ledger key: %w", err)
	}
	return kind + "-" + hex.EncodeToString(key[:]), nil
}

// executionGrant maps sqlc's unnamed grant parameters in one place:
// Column5 is the lease owner; result Column6 is the current attempt fence.
func executionGrant(e run.ScopedExecution, writer string) (intakedb.InsertExecutionParams, intakedb.InsertExecutionResultParams, error) {
	kind, key, err := leaseResource(e.Scope)
	if err != nil {
		return intakedb.InsertExecutionParams{}, intakedb.InsertExecutionResultParams{}, err
	}
	return intakedb.InsertExecutionParams{AdmissionEpoch: e.AdmissionEpoch, AdmittedWriter: writer, ResourceKind: kind, ResourceKey: key, Column5: e.Owner, AttemptFence: e.AttemptFence},
		intakedb.InsertExecutionResultParams{AdmissionEpoch: e.AdmissionEpoch, AdmittedWriter: writer, ResourceKind: kind, ResourceKey: key, Column5: e.Owner, Column6: e.AttemptFence}, nil
}

// RegisterExecution freezes work inputs under the current lease grant.
// Receipt is transport provenance; predecessor links a prior processing attempt.
func (l *Ledger) RegisterExecution(ctx context.Context, work run.IntakeWork, receipt string, predecessor *int64) (intakedb.IngestExecution, error) {
	if err := work.Validate(); err != nil {
		return intakedb.IngestExecution{}, fmt.Errorf("register work: %w", err)
	}
	p, _, err := executionGrant(work.Execution, l.writer)
	if err != nil {
		return intakedb.IngestExecution{}, err
	}
	p.ExecutionKey, err = newLedgerKey("execution")
	if err != nil {
		return intakedb.IngestExecution{}, err
	}
	p.WorkflowID, p.WorkflowKind, p.ProcessingAttemptKey = work.Execution.WorkflowID, string(work.Execution.WorkflowKind), work.Execution.AttemptKey
	p.ScopeKind = string(work.Execution.Scope.Kind)
	p.InputDigest, p.RecipeDigest, p.BudgetDigest = string(work.InputDigest), string(work.RecipeDigest), string(work.BudgetDigest)
	p.Budget, err = json.Marshal(work.Budget)
	if err != nil {
		return intakedb.IngestExecution{}, fmt.Errorf("encode budget: %w", err)
	}
	p.PredecessorID = predecessor
	p.RunReceipts, err = json.Marshal([]string{receipt})
	if err != nil {
		return intakedb.IngestExecution{}, fmt.Errorf("encode run receipts: %w", err)
	}
	return ledgerTransaction(ctx, l, func(tx pgx.Tx, q intakedb.Querier) (intakedb.IngestExecution, error) {
		if err := l.lockAdmission(ctx, tx, work.Execution); err != nil {
			return intakedb.IngestExecution{}, err
		}
		v, err := q.InsertExecution(ctx, p)
		if err != nil {
			return v, grantError("register execution", err)
		}
		if _, err := lockLease(ctx, tx, work.Execution); err != nil {
			return v, err
		}
		return v, nil
	})
}

// RAW QUERY: LockIntakeAdmission. Lock before comparing the grant, so a wait
// observes the new row after rotation. Never acquire a lease lock first.
func (l *Ledger) lockAdmission(ctx context.Context, tx pgx.Tx, e run.ScopedExecution) error {
	var country, writer string
	var epoch int64
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT country_code, admission_epoch, admitted_writer, enabled
		FROM ingest.country_admission WHERE id = 1 FOR SHARE`).Scan(&country, &epoch, &writer, &enabled)
	if err != nil {
		return grantError("lock admission", err)
	}
	if country != e.Country || epoch != e.AdmissionEpoch || writer != l.writer || !enabled {
		return fmt.Errorf("admission grant: %w", run.ErrStaleGrant)
	}
	return nil
}

// RAW QUERIES: LockIntakeLease and ReadIntakeGrantTime. The clock is read in a
// separate statement AFTER the lock wait, including waits on unchanged rows.
func lockLease(ctx context.Context, tx pgx.Tx, e run.ScopedExecution) (time.Time, error) {
	kind, key, err := leaseResource(e.Scope)
	if err != nil {
		return time.Time{}, err
	}
	var owner string
	var fence, epoch int64
	var expires time.Time
	var cancelled *time.Time
	err = tx.QueryRow(ctx, `SELECT owner, attempt_fence, admission_epoch, expires_at, cancelled_at
		FROM ingest.execution_lease WHERE resource_kind = $1 AND resource_key = $2 FOR UPDATE`, kind, key).
		Scan(&owner, &fence, &epoch, &expires, &cancelled)
	if err != nil {
		return time.Time{}, grantError("lock lease", err)
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return now, fmt.Errorf("read grant time: %w", err)
	}
	if owner != e.Owner || fence != e.AttemptFence || epoch != e.AdmissionEpoch || cancelled != nil || !now.Before(expires) {
		return now, fmt.Errorf("lease grant: %w", run.ErrStaleGrant)
	}
	return now, nil
}

// RAW QUERY: ResolveIntakeExecution. Frozen identities and digests must match
// even on replay. A new fence requires registration of a new execution row.
func resolveExecution(ctx context.Context, tx pgx.Tx, w run.IntakeWork) (int64, string, error) {
	kind, key, err := leaseResource(w.Execution.Scope)
	if err != nil {
		return 0, "", err
	}
	var id int64
	var executionKey string
	var budget, receipts json.RawMessage
	e := w.Execution
	err = tx.QueryRow(ctx, `SELECT id, execution_key, budget, run_receipts FROM ingest.execution
		WHERE workflow_id = $1 AND workflow_kind = $2 AND processing_attempt_key = $3
		AND attempt_fence = $4 AND admission_epoch = $5 AND scope_kind = $6
		AND resource_kind = $7 AND resource_key = $8 AND input_digest = $9
		AND recipe_digest = $10 AND budget_digest = $11`, e.WorkflowID, string(e.WorkflowKind), e.AttemptKey,
		e.AttemptFence, e.AdmissionEpoch, string(e.Scope.Kind), kind, key, string(w.InputDigest), string(w.RecipeDigest), string(w.BudgetDigest)).Scan(&id, &executionKey, &budget, &receipts)
	if err != nil {
		return 0, "", grantError("resolve execution", err)
	}
	var frozen run.Budget
	if err := json.Unmarshal(budget, &frozen); err != nil {
		return 0, "", fmt.Errorf("decode frozen budget: %w", err)
	}
	if frozen != w.Budget {
		return 0, "", fmt.Errorf("frozen budget: %w", run.ErrStaleGrant)
	}
	return id, executionKey, nil
}

func (l *Ledger) verifyOutputs(ctx context.Context, outputs []model.AssetRef) error {
	if len(outputs) != 0 && l.objects == nil {
		return fmt.Errorf("immutable object storage is required")
	}
	for _, ref := range outputs {
		reader, err := l.objects.Open(ctx, ref.Path)
		if err != nil {
			return fmt.Errorf("open staged output: %w", err)
		}
		h := sha256.New()
		// Bound each read by the claimed size plus one byte without overflowing.
		n, readErr := io.Copy(h, io.LimitReader(reader, ref.ByteSize))
		var extra [1]byte
		extraN, extraErr := io.ReadFull(reader, extra[:])
		closeErr := reader.Close()
		if readErr != nil {
			return fmt.Errorf("hash staged output: %w", readErr)
		}
		if extraErr != nil && !errors.Is(extraErr, io.EOF) {
			return fmt.Errorf("check staged output length: %w", extraErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close staged output: %w", closeErr)
		}
		if n != ref.ByteSize || extraN != 0 || hex.EncodeToString(h.Sum(nil)) != string(ref.SHA256) {
			return fmt.Errorf("staged output bytes do not match reference")
		}
	}
	return nil
}

// RAW QUERY: ResolveBoundIntakeDecision. Evidence is passed to the trusted
// verifier, which resolves actor authority and exact retained/excluded scope.
func resolveDecision(ctx context.Context, tx pgx.Tx, executionID int64, d *run.DecisionBinding) (json.RawMessage, error) {
	if d == nil {
		return nil, nil
	}
	kind, key, err := leaseResource(d.Scope)
	if err != nil {
		return nil, err
	}
	var evidence json.RawMessage
	err = tx.QueryRow(ctx, `SELECT evidence FROM ingest.quarantine_decision
		WHERE decision_key = $1 AND execution_id = $2 AND actor = $3 AND action = $4
		AND workflow_fence_token = $5 AND expected_decision_epoch = $6 AND decision_revision = $7
		AND scope_kind = $8 AND resource_kind = $9 AND scope_key = $10
		AND revision_key = $11 AND candidate_digest = $12 AND quality_report_hash = $13
		AND original_hash = $14 AND output_hash = $15 AND scope_manifest_digest = $16
		AND (resource_kind <> 'document_version' OR document_version_key = scope_key)
		AND expires_at = $17 AND expires_at > clock_timestamp()`, d.DecisionKey, executionID, d.ActorKey,
		string(d.Action), d.AttemptFence, d.AdmissionEpoch, d.DecisionRevision, string(d.Scope.Kind), kind, key,
		d.RevisionKey, string(d.CandidateDigest), string(d.ReportHash), string(d.OriginalHash), string(d.OutputDigest),
		string(d.ScopeManifest), d.ExpiresAt).Scan(&evidence)
	if err != nil {
		return nil, grantError("resolve decision", err)
	}
	return evidence, nil
}

// Apply validates all grants on every call and atomically commits a result,
// acceptance event/projection, revision receipt and completion delivery intent.
// Replays return the original receipt, including its original transport receipt.
func (l *Ledger) Apply(ctx context.Context, request run.IntakeApplyRequest) (run.IntakeReceipt, error) {
	if err := request.Validate(); err != nil {
		return run.IntakeReceipt{}, fmt.Errorf("validate intake apply: %w", err)
	}
	return ledgerTransaction(ctx, l, func(tx pgx.Tx, q intakedb.Querier) (run.IntakeReceipt, error) {
		var zero run.IntakeReceipt
		if err := l.lockAdmission(ctx, tx, request.Work.Execution); err != nil {
			return zero, err
		}
		now, err := lockLease(ctx, tx, request.Work.Execution)
		if err != nil {
			return zero, err
		}
		if err := request.ValidateAt(now); err != nil {
			return zero, fmt.Errorf("validate current request: %w", err)
		}
		if request.Acceptance != "" {
			// RAW QUERY: LockAcceptanceAttempt. Different resources may name
			// the same revision/attempt before its projection row exists.
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))`,
				request.RevisionKey, request.Work.Execution.AttemptKey); err != nil {
				return zero, fmt.Errorf("lock acceptance attempt: %w", err)
			}
			now, err = lockLease(ctx, tx, request.Work.Execution)
			if err != nil {
				return zero, err
			}
			if err := request.ValidateAt(now); err != nil {
				return zero, fmt.Errorf("acceptance lock grant check: %w", err)
			}
		}
		executionID, _, err := resolveExecution(ctx, tx, request.Work)
		if err != nil {
			return zero, err
		}
		evidence, err := resolveDecision(ctx, tx, executionID, request.Decision)
		if err != nil {
			return zero, err
		}
		if request.Comparison != nil {
			if _, err := resolveDecision(ctx, tx, executionID, request.Comparison.Adjudication); err != nil {
				return zero, err
			}
		}
		gaps := []model.GapRef{}
		if request.Acceptance != "" {
			if l.verifier == nil {
				return zero, fmt.Errorf("trusted acceptance verifier is required")
			}
			gaps, err = l.verifier.VerifyAcceptance(ctx, tx, request, evidence)
			if err != nil {
				return zero, fmt.Errorf("verify acceptance: %w", err)
			}
			if gaps == nil {
				gaps = []model.GapRef{}
			}
			for _, gap := range gaps {
				if err := gap.Validate(); err != nil {
					return zero, fmt.Errorf("verified gap: %w", err)
				}
				if gap.ReportHash != request.QualityReportHash {
					return zero, fmt.Errorf("verified gap report does not match acceptance")
				}
			}
			if (request.Acceptance == model.AcceptedWithGaps || request.Acceptance == model.Excluded) && len(gaps) == 0 {
				return zero, fmt.Errorf("gap acceptance requires exact verified gaps")
			}
		}
		// Classify existing payloads after authority checks and before reading
		// staged content, so changed outputs get a typed conflict.
		stored, lookupErr := q.FindExecutionResult(ctx, request.IdempotencyKey)
		if lookupErr == nil {
			if err := classifyPayload(stored.Payload, request); err != nil {
				return zero, err
			}
		} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return zero, fmt.Errorf("find prior result: %w", lookupErr)
		}
		if err := l.verifyOutputs(ctx, request.Outputs); err != nil {
			return zero, err
		}
		p, err := resultParams(request, l.writer, executionID)
		if err != nil {
			return zero, err
		}
		inserted, insertErr := q.InsertExecutionResult(ctx, p)
		if insertErr != nil && !errors.Is(insertErr, pgx.ErrNoRows) {
			return zero, fmt.Errorf("insert execution result: %w", insertErr)
		}
		// Always use a NEW statement snapshot. ON CONFLICT may have waited for
		// a committed winner invisible to the insert statement's snapshot.
		stored, err = q.FindExecutionResult(ctx, request.IdempotencyKey)
		now, grantErr := lockLease(ctx, tx, request.Work.Execution)
		if grantErr != nil {
			return zero, grantErr
		}
		if grantErr := request.ValidateAt(now); grantErr != nil {
			return zero, fmt.Errorf("result grant check: %w", grantErr)
		}
		if err != nil {
			return zero, grantError("resolve committed result", err)
		}
		if err := classifyPayload(stored.Payload, request); err != nil {
			return zero, err
		}
		if insertErr == nil && inserted.ResultKey == p.ResultKey {
			if request.Acceptance != "" {
				if err := recordAcceptance(ctx, tx, q, request, stored, gaps); err != nil {
					return zero, err
				}
			}
			if _, err := insertDelivery(ctx, q, intakedb.InsertDeliveryRecordParams{
				DeduplicationKey: stored.ResultKey, DeliveryKind: "completion", RecipientWorkflowID: request.Work.Execution.WorkflowID,
				ReceiptKind: "result", ReceiptKey: stored.ResultKey, ExpectedEpoch: request.Work.Execution.AdmissionEpoch,
				ExpectedHash: string(request.OutputDigest), DueAt: stored.CreatedAt,
			}); err != nil {
				return zero, err
			}
		}
		// Recheck database time after content verification and all writes.
		// Locks prevent rotation/reclaim/cancellation; time can still advance.
		receipt, err := receiptFromResult(ctx, tx, stored)
		if err != nil {
			return zero, err
		}
		now, err = lockLease(ctx, tx, request.Work.Execution)
		if err != nil {
			return zero, err
		}
		if err := request.ValidateAt(now); err != nil {
			return zero, fmt.Errorf("final grant check: %w", err)
		}
		return receipt, nil
	})
}

func resultParams(r run.IntakeApplyRequest, writer string, executionID int64) (intakedb.InsertExecutionResultParams, error) {
	_, p, err := executionGrant(r.Work.Execution, writer)
	if err != nil {
		return p, err
	}
	p.ResultKey, err = newLedgerKey("result")
	if err != nil {
		return p, err
	}
	p.ID, p.Stage, p.Substep, p.Shard = executionID, string(r.Work.Stage), r.Work.Substep, r.Work.Shard
	p.IdempotencyKey = r.IdempotencyKey
	p.InputDigest, p.OutputDigest = string(r.Work.InputDigest), string(r.OutputDigest)
	p.RecipeDigest, p.BudgetDigest = string(r.Work.RecipeDigest), string(r.Work.BudgetDigest)
	p.DecisionRevision, p.Outcome = r.Work.DecisionRevision, string(r.Outcome)
	c := r.Counts
	p.Expected, p.Processed, p.Succeeded = int64(c.Expected), int64(c.Processed), int64(c.Succeeded)
	p.PendingRetry, p.TerminalGap, p.Rejected, p.Failed, p.AcceptedGaps = int64(c.PendingRetry), int64(c.TerminalGap), int64(c.Rejected), int64(c.Failed), int64(c.AcceptedGaps)
	p.Cursor, p.NextEligibleAt, p.Complete = r.Cursor, r.NextEligible, r.Complete
	if r.GapAcceptanceRef != "" {
		p.GapAcceptanceRef = &r.GapAcceptanceRef
	}
	p.Payload, err = json.Marshal(r)
	if err != nil {
		return p, fmt.Errorf("encode immutable result: %w", err)
	}
	return p, nil
}

func classifyPayload(payload json.RawMessage, proposed run.IntakeApplyRequest) error {
	var stored, canonical run.IntakeApplyRequest
	if err := json.Unmarshal(payload, &stored); err != nil {
		return fmt.Errorf("decode stored intake payload: %w", err)
	}
	// Round-trip both values to remove process-local time locations and monotonic
	// clocks before using the frozen immutable-payload comparator.
	encoded, err := json.Marshal(proposed)
	if err != nil {
		return fmt.Errorf("encode replay payload: %w", err)
	}
	if err := json.Unmarshal(encoded, &canonical); err != nil {
		return fmt.Errorf("decode replay payload: %w", err)
	}
	if err := run.ValidateIntakeReplay(stored, canonical, true); err != nil {
		return fmt.Errorf("classify intake replay: %w", err)
	}
	return nil
}

// RAW QUERY: ReadResultExecutionReceipt. Replay returns original execution and
// transport provenance, not the current execution that authorized the replay.
func receiptFromResult(ctx context.Context, tx pgx.Tx, result intakedb.IngestExecutionResult) (run.IntakeReceipt, error) {
	receipt := run.IntakeReceipt{ResultID: result.ResultKey}
	var receipts json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT execution_key, run_receipts FROM ingest.execution WHERE id = $1`, result.ExecutionID).Scan(&receipt.ExecutionID, &receipts); err != nil {
		return receipt, fmt.Errorf("read receipt execution: %w", err)
	}
	var runs []string
	if err := json.Unmarshal(receipts, &runs); err != nil {
		return receipt, fmt.Errorf("decode transport receipts: %w", err)
	}
	if len(runs) > 0 {
		receipt.RunReceipt = runs[0]
	}
	if err := json.Unmarshal(result.Payload, &receipt.Result); err != nil {
		return receipt, fmt.Errorf("decode result receipt: %w", err)
	}
	return receipt, nil
}

func recordAcceptance(ctx context.Context, tx pgx.Tx, q intakedb.Querier, r run.IntakeApplyRequest, result intakedb.IngestExecutionResult, gaps []model.GapRef) error {
	// RAW QUERY: LockPriorAcceptanceState. Reuse must not downgrade an
	// already accepted processing attempt to an ineligible state.
	var priorState string
	err := tx.QueryRow(ctx, `SELECT state FROM ingest.revision_acceptance_state
		WHERE revision_key = $1 AND processing_attempt_key = $2 FOR UPDATE`, r.RevisionKey, r.Work.Execution.AttemptKey).Scan(&priorState)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lock prior acceptance: %w", err)
	}
	if model.AcceptanceState(priorState).BuildEligible() && !r.Acceptance.BuildEligible() {
		return fmt.Errorf("acceptance cannot downgrade an accepted attempt")
	}
	scoped, err := json.Marshal(gaps)
	if err != nil {
		return fmt.Errorf("encode scoped gaps: %w", err)
	}
	a, err := q.InsertRevisionAcceptance(ctx, intakedb.InsertRevisionAcceptanceParams{
		RevisionKey: r.RevisionKey, ProcessingAttemptKey: r.Work.Execution.AttemptKey,
		ExecutionID: result.ExecutionID, ExecutionResultID: result.ID, CandidateDigest: string(r.CandidateDigest),
		QualityReportHash: string(r.QualityReportHash), DecisionRevision: r.Work.DecisionRevision,
		State: string(r.Acceptance), ScopedGaps: scoped, Watermark: result.ID,
	})
	if err != nil {
		return fmt.Errorf("record acceptance: %w", err)
	}
	if _, err := q.UpsertRevisionAcceptanceState(ctx, a.ID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("project acceptance: %w", err)
	}
	// RAW QUERY: InsertRevisionReceipt, the immutable reuse provenance link.
	if _, err := tx.Exec(ctx, `INSERT INTO ingest.revision_receipt (revision_key, execution_result_id) VALUES ($1, $2)`, r.RevisionKey, result.ID); err != nil {
		return fmt.Errorf("record revision receipt: %w", err)
	}
	for _, gap := range gaps {
		key, err := newLedgerKey("gap")
		if err != nil {
			return err
		}
		// RAW QUERY: InsertAcceptanceEvidenceGap. Public reason is a code.
		if _, err := tx.Exec(ctx, `INSERT INTO ingest.evidence_gap (gap_key, scope_kind, scope_key, reason, report_hash, acceptance_id)
			VALUES ($1, $2, $3, $4, $5, $6)`, key, string(gap.Kind), gap.Key, gap.ReasonCode, string(gap.ReportHash), a.ID); err != nil {
			return fmt.Errorf("record evidence gap: %w", err)
		}
	}
	return nil
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

// RAW QUERIES: ResolveDeliveryResult and ResolveDeliveryDecision. A valid key
// grammar alone never authorizes delivery of somebody else's receipt.
func bindDelivery(ctx context.Context, tx pgx.Tx, work run.IntakeWork, executionID int64, executionKey string, p intakedb.InsertDeliveryRecordParams) error {
	var hash string
	var err error
	switch p.ReceiptKind {
	case "execution":
		if p.DeliveryKind != "launch" || p.ReceiptKey != executionKey {
			return fmt.Errorf("launch must reference its granted execution")
		}
		hash = string(work.InputDigest)
	case "result":
		if p.DeliveryKind != "completion" {
			return fmt.Errorf("result delivery must be completion")
		}
		err = tx.QueryRow(ctx, `SELECT output_digest FROM ingest.execution_result
			WHERE result_key = $1 AND execution_id = $2`, p.ReceiptKey, executionID).Scan(&hash)
	case "decision":
		if p.DeliveryKind != "decision" {
			return fmt.Errorf("decision receipt requires decision delivery")
		}
		err = tx.QueryRow(ctx, `SELECT output_hash FROM ingest.quarantine_decision
			WHERE decision_key = $1 AND execution_id = $2 AND expected_decision_epoch = $3
			AND workflow_fence_token = $4 AND expires_at > clock_timestamp()`, p.ReceiptKey, executionID,
			work.Execution.AdmissionEpoch, work.Execution.AttemptFence).Scan(&hash)
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

// EligibleAcceptance scans bounded build-eligible state at a result watermark.
func (l *Ledger) EligibleAcceptance(ctx context.Context, watermark int64, limit int32) ([]intakedb.IngestRevisionAcceptanceState, error) {
	if watermark <= 0 || limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("positive watermark and limit between 1 and 1000 are required")
	}
	return ledgerTransaction(ctx, l, func(_ pgx.Tx, q intakedb.Querier) ([]intakedb.IngestRevisionAcceptanceState, error) {
		v, err := q.ListBuildEligibleAcceptance(ctx, intakedb.ListBuildEligibleAcceptanceParams{Watermark: watermark, Limit: limit})
		if err != nil {
			return nil, fmt.Errorf("scan eligible acceptance: %w", err)
		}
		return v, nil
	})
}

// PolicyBundle resolves an immutable policy manifest by its validated digest.
func (l *Ledger) PolicyBundle(ctx context.Context, digest model.Digest) (intakedb.ConfigPolicyBundle, error) {
	if err := digest.Validate(); err != nil {
		return intakedb.ConfigPolicyBundle{}, fmt.Errorf("policy digest: %w", err)
	}
	return ledgerTransaction(ctx, l, func(_ pgx.Tx, q intakedb.Querier) (intakedb.ConfigPolicyBundle, error) {
		v, err := q.FindPolicyBundle(ctx, string(digest))
		if err != nil {
			return v, fmt.Errorf("find policy bundle: %w", err)
		}
		return v, nil
	})
}
