package intake

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/blob"
	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/source/api"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

// Sources implements source accounting and Bronze acceptance. All operations
// use independent READ COMMITTED transactions and the Ledger grant checks.
// Give each receipt-bearing operation a distinct work substep and shard. A new
// source attempt needs new immutable work; response-loss replay reuses that work.
type Sources struct {
	ledger         *Ledger
	continuer      api.Continuer
	reconciliation bool
}

// NewSources constructs an adapter without I/O. Object readers have the same
// cancellation and Close requirements as NewLedger. An optional trusted
// continuer must be supplied for WebForms registration. Its deterministic output
// is compared in full; construction performs no source I/O.
func NewSources(db TransactionStarter, writer string, objects blob.Store, continuers ...api.Continuer) (*Sources, error) {
	if len(continuers) > 1 {
		return nil, fmt.Errorf("at most one trusted source continuer is permitted")
	}
	l, err := NewLedger(db, writer, objects, nil)
	if err != nil {
		return nil, err
	}
	s := &Sources{ledger: l}
	if len(continuers) == 1 {
		s.continuer = continuers[0]
	}
	return s, nil
}

// NewSourceReconciler exposes source accounting for a reconciliation-mode
// SourceSync. It rejects new fetch claims; grants still gate every operation.
func NewSourceReconciler(db TransactionStarter, writer string, objects blob.Store) (*Sources, error) {
	s, err := NewSources(db, writer, objects)
	if err == nil {
		s.reconciliation = true
	}
	return s, err
}

func sourceTransaction[T any](ctx context.Context, s *Sources, work run.IntakeWork, kind run.IntakeSubjectKind, fn func(pgx.Tx, intakedb.Querier) (T, error)) (T, error) {
	return sourceTransactionMode(ctx, s, work, kind, false, nil, fn)
}

func sourceTransactionMode[T any](ctx context.Context, s *Sources, work run.IntakeWork, kind run.IntakeSubjectKind, reconciliation bool, payload any, fn func(pgx.Tx, intakedb.Querier) (T, error)) (T, error) {
	var zero T
	if err := work.Validate(); err != nil {
		return zero, fmt.Errorf("source work: %w", err)
	}
	control := kind == "" && work.Execution.Scope.Kind == run.ScopeControl && work.Execution.WorkflowKind == run.ControlReconcile && work.Stage == run.Discover
	if !control && (work.Execution.Scope.Kind != run.ScopeIntake || work.Execution.Scope.Subject.Kind != kind) {
		return zero, fmt.Errorf("source subject: %w", run.ErrStaleGrant)
	}
	if kind == run.SubjectSourceWindow && work.Stage != run.Discover || kind == run.SubjectDocumentVersion && work.Stage != run.Fetch {
		return zero, fmt.Errorf("source stage binding: %w", run.ErrStaleGrant)
	}
	value, err := ledgerTransaction(ctx, s.ledger, func(tx pgx.Tx, q intakedb.Querier) (T, error) {
		if err := s.ledger.lockAdmission(ctx, tx, work.Execution); err != nil {
			return zero, err
		}
		if err := lockSourceLease(ctx, tx, work.Execution, reconciliation); err != nil {
			return zero, err
		}
		if err := prelockSourceAccounting(ctx, tx, work, payload); err != nil {
			return zero, fmt.Errorf("prelock source accounting: %w", err)
		}
		if err := lockSourceLease(ctx, tx, work.Execution, reconciliation); err != nil {
			return zero, err
		}
		if _, _, err := resolveExecution(ctx, tx, work); err != nil {
			return zero, err
		}
		value, err := fn(tx, q)
		var databaseError interface{ SQLState() string }
		if err != nil && errors.As(err, &databaseError) {
			// A server error aborts the transaction. Preserve that reason rather
			// than masking it with a final query's transaction-aborted error.
			return zero, err
		}
		// Expiry takes precedence even over a changed replay after a lock wait.
		if grantErr := lockSourceLease(ctx, tx, work.Execution, reconciliation); grantErr != nil {
			return zero, grantErr
		}
		return value, err
	})
	if err != nil {
		return zero, fmt.Errorf("source %s %s: %w", kind, work.Execution.Scope.Subject.LeaseKey(), err)
	}
	return value, nil
}

// prelockSourceAccounting acquires the shared source lock order after admission
// and the owning lease. Candidate reads are bounded by the immutable work budget.
// Window locks freeze page membership; the owning document lease freezes its
// request membership. Generated queries may reacquire these same locks.
func prelockSourceAccounting(ctx context.Context, tx pgx.Tx, work run.IntakeWork, payload any) error {
	windows, hosts, hashes := map[string]bool{}, map[string]bool{}, map[string]bool{}
	if work.Execution.Scope.Subject.Kind == run.SubjectSourceWindow {
		windows[work.Execution.Scope.Subject.LeaseKey()] = true
	}
	// Only typed authority fields select locks. Source metadata may contain
	// arbitrary keys named URL or WindowKey and must never choose database rows.
	addPlan := func(plan api.RequestPlan) error {
		windows[plan.WindowKey] = true
		u, err := url.Parse(plan.URL)
		if err != nil {
			return err
		}
		host, err := sourceHostKey(u.Host)
		if err != nil {
			return err
		}
		hosts[host] = true
		return nil
	}
	addCapture := func(capture model.CaptureRef) {
		hashes[string(capture.Asset.SHA256)] = true
		hashes[string(capture.TransportReceipt.SHA256)] = true
	}
	addRequests := func(requests []SourceRequestPlan) error {
		for _, request := range requests {
			if err := addPlan(request.Plan); err != nil {
				return err
			}
		}
		return nil
	}
	switch value := payload.(type) {
	case SourcePagePlan:
		if err := addPlan(value.Plan); err != nil {
			return err
		}
	case SourceCaptureReceipt:
		addCapture(value.Capture)
	case api.DecodedDiscoveryPage:
		addCapture(value.Capture)
	case DiscoveryAcceptance:
		addCapture(value.Page.Capture)
	case CaptureAcceptance:
		addCapture(value.Capture)
		if err := addRequests(value.Requests); err != nil {
			return err
		}
	case sourceRequestInventoryPayload:
		if err := addRequests(value.Requests); err != nil {
			return err
		}
	case SourceLaunchAcknowledgement:
		windows[value.WindowKey] = true
	case SourceHostPacing:
		host, err := sourceHostKey(value.HostKey)
		if err != nil {
			return err
		}
		hosts[host] = true
	case SourceTransportReceipt:
		if value.Capture != nil {
			addCapture(*value.Capture)
		}
	}
	var err error
	requestKeys := []string{}
	if work.Execution.Scope.Subject.Kind == run.SubjectDocumentVersion {
		// RAW QUERY: SourceLockRequestCandidates. No row lock before windows.
		rows, err := tx.Query(ctx, `SELECT r.request_key, w.window_key, h.host_key
			FROM ingest.artifact_request r JOIN ingest.source_window w ON w.id = r.source_window_id
			JOIN ingest.source_rate_limit h ON h.id = r.source_rate_limit_id
			WHERE r.document_version_key = $1 ORDER BY r.request_key COLLATE "C" LIMIT $2`, work.Execution.Scope.Subject.LeaseKey(), work.Budget.MaxItems+1)
		if err != nil {
			return err
		}
		for rows.Next() {
			var key, window, host string
			if err := rows.Scan(&key, &window, &host); err != nil {
				rows.Close()
				return err
			}
			requestKeys = append(requestKeys, key)
			windows[window], hosts[host] = true, true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(requestKeys) > work.Budget.MaxItems {
			return fmt.Errorf("source lock request bound: %w", run.ErrStaleGrant)
		}
	}
	keys := func(set map[string]bool) []string {
		out := make([]string, 0, len(set))
		for key := range set {
			out = append(out, key)
		}
		sort.Strings(out)
		return out
	}
	// RAW QUERY: PrelockSourceWindows. Fixed byte order across parent windows.
	for _, key := range keys(windows) {
		var id int64
		err := tx.QueryRow(ctx, `SELECT id FROM ingest.source_window WHERE window_key = $1 FOR UPDATE`, key).Scan(&id)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	type pageLock struct {
		id                   int64
		key, window, locator string
	}
	var pages []pageLock
	// RAW QUERY: SourceLockPageCandidates. Window locks freeze membership;
	// discover the bounded set before acquiring page locks in global byte order.
	for _, key := range keys(windows) {
		rows, err := tx.Query(ctx, `SELECT p.id, p.page_key, p.request_plan->>'URL' FROM ingest.discovery_page p
			JOIN ingest.source_window w ON w.id = p.source_window_id WHERE w.window_key = $1
			ORDER BY p.page_key COLLATE "C" LIMIT $2`, key, work.Budget.MaxPages+1)
		if err != nil {
			return err
		}
		count := 0
		for rows.Next() {
			page := pageLock{window: key}
			if err := rows.Scan(&page.id, &page.key, &page.locator); err != nil {
				rows.Close()
				return err
			}
			pages = append(pages, page)
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if count > work.Budget.MaxPages {
			return fmt.Errorf("source lock page bound: %w", run.ErrStaleGrant)
		}
	}
	sort.Slice(pages, func(i, j int) bool {
		if pages[i].key != pages[j].key {
			return pages[i].key < pages[j].key
		}
		return pages[i].window < pages[j].window
	})
	// RAW QUERY: PrelockSourcePages. All windows precede globally sorted pages.
	for _, page := range pages {
		var id int64
		if err := tx.QueryRow(ctx, `SELECT id FROM ingest.discovery_page WHERE id = $1 FOR UPDATE`, page.id).Scan(&id); err != nil {
			return err
		}
		u, err := url.Parse(page.locator)
		if err != nil {
			return err
		}
		host, err := sourceHostKey(u.Host)
		if err != nil {
			return err
		}
		hosts[host] = true
	}
	// RAW QUERY: PrelockSourceHosts. Precede artifact inserts' FK KEY SHARE.
	for _, key := range keys(hosts) {
		var id int64
		err := tx.QueryRow(ctx, `SELECT id FROM ingest.source_rate_limit WHERE host_key = $1 FOR UPDATE`, key).Scan(&id)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	// RAW QUERY: PrelockSourceRequests. Hosts are already held.
	for _, key := range requestKeys {
		var id int64
		if err := tx.QueryRow(ctx, `SELECT id FROM ingest.artifact_request WHERE request_key = $1 FOR UPDATE`, key).Scan(&id); err != nil {
			return err
		}
	}
	// RAW QUERY: PrelockSourceBlobs. Lock before any referencing Bronze insert.
	for _, hash := range keys(hashes) {
		var id int64
		err := tx.QueryRow(ctx, `SELECT id FROM bronze.blob WHERE sha256 = $1 FOR UPDATE`, hash).Scan(&id)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	return nil
}

// Cancellation permits closure, release, and replay of retained receipts.
// Fresh outcomes still require the noncancelled result-insertion gate.
func lockSourceLease(ctx context.Context, tx pgx.Tx, e run.ScopedExecution, reconciliation bool) error {
	if !reconciliation {
		_, err := lockLease(ctx, tx, e)
		return err
	}
	// RAW QUERY: LockCancelledSourceReconciliationLease. Cancellation does not
	// waive current ownership, admission, fence or expiry checks.
	kind, key, err := leaseResource(e.Scope)
	if err != nil {
		return err
	}
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM ingest.execution_lease
		WHERE resource_kind = $1 AND resource_key = $2 AND owner = $3
		AND attempt_fence = $4 AND admission_epoch = $5 FOR UPDATE`, kind, key, e.Owner, e.AttemptFence, e.AdmissionEpoch).Scan(&id)
	if err != nil {
		return grantError("lock source reconciliation lease", err)
	}
	// RAW QUERY: CheckSourceReconciliationLeaseExpiry. Use a fresh clock after lock.
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT expires_at > clock_timestamp() FROM ingest.execution_lease WHERE id = $1`, id).Scan(&valid); err != nil {
		return grantError("read reconciliation expiry", err)
	}
	if !valid {
		return fmt.Errorf("source reconciliation expiry: %w", run.ErrStaleGrant)
	}
	return nil
}

type sourcePayload struct {
	run.IntakeApplyRequest
	SourceOperation  string
	SourcePayload    json.RawMessage
	Claim            *sourceClaimIdentity `json:",omitempty"`
	ClosureResult    string               `json:",omitempty"`
	ExhaustionReason string               `json:",omitempty"`
}

func sourceJSON(value any) (json.RawMessage, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode source payload: %w", err)
	}
	return b, nil
}

func sourceDigest(value any) (string, error) {
	b, err := sourceJSON(value)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func sameSourceJSON(a, b json.RawMessage) bool {
	var left, right any
	// UseNumber avoids losing distinct int64 lengths in float64 conversion.
	decode := func(raw json.RawMessage, target *any) error {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		return decoder.Decode(target)
	}
	return decode(a, &left) == nil && decode(b, &right) == nil && reflect.DeepEqual(left, right)
}

func classifySourcePayload(storedJSON json.RawMessage, request run.IntakeApplyRequest, operation string, raw json.RawMessage) error {
	if err := classifyPayload(storedJSON, request); err != nil {
		return err
	}
	var stored sourcePayload
	if err := json.Unmarshal(storedJSON, &stored); err != nil {
		return fmt.Errorf("decode source replay: %w", run.ErrPayloadConflict)
	}
	if stored.SourceOperation != operation || !sameSourceJSON(stored.SourcePayload, raw) {
		return fmt.Errorf("source replay payload: %w", run.ErrPayloadConflict)
	}
	// JSONB changes object key order. Digest the expected typed payload after
	// semantic equality, rather than hashing PostgreSQL's serialized JSONB.
	digest, err := sourceDigest(struct {
		Operation string
		Payload   json.RawMessage
	}{operation, raw})
	if err != nil {
		return err
	}
	if stored.OutputDigest != model.Digest(digest) {
		return fmt.Errorf("source replay digest: %w", run.ErrPayloadConflict)
	}
	return nil
}

// sourceMutation stores the full operation payload alongside a normal Ledger
// result. The result is inserted first so same-transaction Bronze references
// resolve to a real result; every later failure rolls the result back as well.
func (s *Sources) sourceMutation(ctx context.Context, work run.IntakeWork, kind run.IntakeSubjectKind, operation string, payload any, apply func(pgx.Tx, intakedb.Querier, intakedb.IngestExecutionResult) error) (run.IntakeReceipt, error) {
	raw, err := sourceJSON(payload)
	if err != nil {
		return run.IntakeReceipt{}, err
	}
	digest, err := sourceDigest(struct {
		Operation string
		Payload   json.RawMessage
	}{operation, raw})
	if err != nil {
		return run.IntakeReceipt{}, err
	}
	r := run.IntakeApplyRequest{Work: work, IdempotencyKey: run.IntakeIdempotencyKey(work), OutputDigest: model.Digest(digest), Outcome: run.OutcomeSucceeded}
	if err := r.Validate(); err != nil {
		return run.IntakeReceipt{}, fmt.Errorf("source receipt: %w", err)
	}
	return sourceTransactionMode(ctx, s, work, kind, operation == "acknowledge_launch" || operation == "close_window" || operation == "recover_page" || operation == "page_capture" || operation == "page_retry" || operation == "page_failure", payload, func(tx pgx.Tx, q intakedb.Querier) (run.IntakeReceipt, error) {
		if operation == "transfer_launch" {
			transfer := payload.(SourceLaunchTransfer)
			binding, err := readSourceLaunchTransferBinding(ctx, tx, work.Execution.Country, work.Execution.Scope.Subject.LeaseKey(), transfer.RequestKey)
			if err != nil {
				return run.IntakeReceipt{}, err
			}
			raw, err = sourceJSON(binding)
			if err != nil {
				return run.IntakeReceipt{}, err
			}
			digest, err := sourceDigest(struct {
				Operation string
				Payload   json.RawMessage
			}{operation, raw})
			if err != nil {
				return run.IntakeReceipt{}, err
			}
			r.OutputDigest = model.Digest(digest)
			key, stored, err := findSourceLaunchTransfer(ctx, tx, binding.RequestKey)
			if err == nil {
				if !sameSourceJSON(stored, raw) {
					return run.IntakeReceipt{}, fmt.Errorf("transfer launch binding: %w", run.ErrPayloadConflict)
				}
				// RAW QUERY: ReadSourceTransferReceipt. Return the issuing receipt.
				var idempotency string
				if err := tx.QueryRow(ctx, `SELECT idempotency_key FROM ingest.execution_result WHERE result_key = $1`, key).Scan(&idempotency); err != nil {
					return run.IntakeReceipt{}, err
				}
				prior, err := q.FindExecutionResult(ctx, idempotency)
				if err != nil {
					return run.IntakeReceipt{}, err
				}
				return receiptFromResult(ctx, tx, prior)
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return run.IntakeReceipt{}, err
			}
		}
		if operation == "page_capture" || operation == "page_retry" || operation == "page_failure" {
			var proposed struct{ ClaimResult string }
			if err := json.Unmarshal(raw, &proposed); err != nil {
				return run.IntakeReceipt{}, err
			}
			// RAW QUERY: FindDiscoveryClaimOutcome. Look up settled receipts
			// before testing current markers or terminal page state.
			var idempotency string
			err := tx.QueryRow(ctx, `SELECT r.idempotency_key FROM ingest.execution_result r
				JOIN ingest.execution e ON e.id = r.execution_id WHERE e.resource_kind = 'source_window'
				AND e.resource_key = $1 AND r.payload->>'SourceOperation' IN ('page_capture','page_retry','page_failure')
				AND r.payload->'SourcePayload'->>'ClaimResult' = $2 ORDER BY r.id LIMIT 1`, work.Execution.Scope.Subject.LeaseKey(), proposed.ClaimResult).Scan(&idempotency)
			if err == nil {
				prior, err := q.FindExecutionResult(ctx, idempotency)
				if err != nil {
					return run.IntakeReceipt{}, err
				}
				var committed sourcePayload
				if err := json.Unmarshal(prior.Payload, &committed); err != nil {
					return run.IntakeReceipt{}, err
				}
				if committed.SourceOperation != operation || !sameSourceJSON(committed.SourcePayload, raw) {
					return run.IntakeReceipt{}, fmt.Errorf("discovery outcome replay: %w", run.ErrPayloadConflict)
				}
				return receiptFromResult(ctx, tx, prior)
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return run.IntakeReceipt{}, err
			}
		}
		prior, lookupErr := q.FindExecutionResult(ctx, r.IdempotencyKey)
		if lookupErr == nil {
			if err := classifySourcePayload(prior.Payload, r, operation, raw); err != nil {
				return run.IntakeReceipt{}, err
			}
			return receiptFromResult(ctx, tx, prior)
		}
		if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return run.IntakeReceipt{}, fmt.Errorf("find source receipt: %w", lookupErr)
		}
		executionID, _, err := resolveExecution(ctx, tx, work)
		if err != nil {
			return run.IntakeReceipt{}, err
		}
		// Verify staged references after grant and replay checks, before any write.
		var capture *model.CaptureRef
		switch value := payload.(type) {
		case DiscoveryAcceptance:
			capture = &value.Page.Capture
		case CaptureAcceptance:
			capture = &value.Capture
		}
		if capture != nil {
			if err := s.ledger.verifyOutputs(ctx, []model.AssetRef{capture.Asset, capture.TransportReceipt}); err != nil {
				return run.IntakeReceipt{}, fmt.Errorf("verify source capture: %w", err)
			}
		}
		exhaustionReason := ""
		var claimIdentity *sourceClaimIdentity
		if operation == "claim_page" {
			key := payload.(sourcePageClaimPayload).PageKey
			page, err := readSourcePage(ctx, tx, work.Execution.Scope.Subject.LeaseKey(), key)
			if err != nil {
				return run.IntakeReceipt{}, err
			}
			claimIdentity = &sourceClaimIdentity{Country: work.Execution.Country, WindowKey: work.Execution.Scope.Subject.LeaseKey(), PageKey: key, Ordinal: page.ClaimOrdinal + 1, Attempt: page.SourceAttempts + 1}
			claimIdentity.AttemptKey = discoveryAttemptKey(claimIdentity.WindowKey, key, claimIdentity.Ordinal)
		}
		params, err := resultParams(r, s.ledger.writer, executionID)
		if err != nil {
			return run.IntakeReceipt{}, err
		}
		if claimIdentity != nil {
			page, err := readSourcePage(ctx, tx, claimIdentity.WindowKey, claimIdentity.PageKey)
			if err != nil {
				return run.IntakeReceipt{}, err
			}
			if page.ClaimState != nil && *page.ClaimState == "released" && page.InfrastructureAttempts == 3 {
				params.Substep = "source-discovery-exhaustion"
				exhaustionReason = "infrastructure_retry_exhausted"
				claimIdentity.Ordinal = page.ClaimOrdinal
				claimIdentity.AttemptKey = discoveryAttemptKey(claimIdentity.WindowKey, claimIdentity.PageKey, page.ClaimOrdinal)
			}
		}
		if operation == "resume_window" || operation == "recover_page" {
			// RAW QUERY: ReadDiscoveryOperationExhaustion. Bind a denial/loss
			// receipt to its durable reason before any state change.
			var exhausted bool
			pageKey := ""
			if operation == "recover_page" {
				pageKey = payload.(struct{ Key, ClaimResult string }).Key
			}
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ingest.discovery_page p
				JOIN ingest.source_window w ON w.id = p.source_window_id WHERE w.window_key = $1
				AND p.state = 'requested' AND p.infrastructure_attempts = 3
				AND (($2 = '' AND p.claim_state = 'released') OR (p.page_key = $2 AND p.claim_state = 'issued')))`, work.Execution.Scope.Subject.LeaseKey(), pageKey).Scan(&exhausted); err != nil {
				return run.IntakeReceipt{}, err
			}
			if exhausted {
				exhaustionReason = "infrastructure_retry_exhausted"
			}
		}
		closureResult := ""
		if operation == "resume_window" {
			// RAW QUERY: ReadSourceResumptionClosure. Preserve immutable closure
			// provenance without resetting any claim or retry allowance.
			if err := tx.QueryRow(ctx, `SELECT r.result_key FROM ingest.execution_result r
				JOIN ingest.execution e ON e.id = r.execution_id JOIN ingest.source_window w ON w.window_key = e.resource_key
				WHERE e.resource_kind = 'source_window' AND e.resource_key = $1
				AND r.payload->>'SourceOperation' IN ('close_window','reconcile_window','page_retry','accept_page')
				ORDER BY CASE WHEN r.payload->>'SourceOperation' = 'close_window'
				AND r.payload->'SourcePayload'->>'Reason' = w.reason THEN 0 ELSE 1 END, r.id DESC LIMIT 1`, work.Execution.Scope.Subject.LeaseKey()).Scan(&closureResult); err != nil {
				return run.IntakeReceipt{}, grantError("read resumption closure", err)
			}
		}
		params.Payload, err = sourceJSON(sourcePayload{IntakeApplyRequest: r, SourceOperation: operation, SourcePayload: raw, Claim: claimIdentity, ClosureResult: closureResult, ExhaustionReason: exhaustionReason})
		if err != nil {
			return run.IntakeReceipt{}, err
		}
		var inserted intakedb.IngestExecutionResult
		var insertErr error
		if operation == "acknowledge_launch" || operation == "close_window" || operation == "recover_page" {
			// RAW QUERY: InsertSourceReconciliationResult. Admission, owning
			// lease and immutable execution were checked above. Cancellation
			// allows acknowledgement, window closure, and claim release. Fresh
			// source outcomes still use the noncancelled generated result gate.
			var id int64
			insertErr = tx.QueryRow(ctx, `INSERT INTO ingest.execution_result
				(execution_id, result_key, stage, substep, shard, idempotency_key,
				input_digest, output_digest, recipe_digest, budget_digest, decision_revision,
				outcome, expected, processed, succeeded, pending_retry, terminal_gap,
				rejected, failed, accepted_gaps, cursor, next_eligible_at, complete, payload)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'succeeded',0,0,0,0,0,0,0,0,'',NULL,false,$12)
				ON CONFLICT (idempotency_key) DO NOTHING RETURNING id`, executionID, params.ResultKey,
				params.Stage, params.Substep, params.Shard, params.IdempotencyKey, params.InputDigest,
				params.OutputDigest, params.RecipeDigest, params.BudgetDigest, params.DecisionRevision, params.Payload).Scan(&id)
			inserted.ResultKey = params.ResultKey
		} else {
			inserted, insertErr = q.InsertExecutionResult(ctx, params)
		}
		if insertErr != nil && !errors.Is(insertErr, pgx.ErrNoRows) {
			return run.IntakeReceipt{}, fmt.Errorf("insert source receipt: %w", insertErr)
		}
		// Always a fresh statement snapshot after ON CONFLICT waits.
		stored, err := q.FindExecutionResult(ctx, r.IdempotencyKey)
		if err != nil {
			return run.IntakeReceipt{}, grantError("resolve source receipt", err)
		}
		if err := classifySourcePayload(stored.Payload, r, operation, raw); err != nil {
			return run.IntakeReceipt{}, err
		}
		if insertErr == nil && inserted.ResultKey == params.ResultKey {
			if err := apply(tx, q, stored); err != nil {
				return run.IntakeReceipt{}, err
			}
			if operation == "accept_page" || operation == "accept_capture" {
				if _, err := insertDelivery(ctx, q, intakedb.InsertDeliveryRecordParams{DeduplicationKey: stored.ResultKey, DeliveryKind: "completion", RecipientWorkflowID: work.Execution.WorkflowID, ReceiptKind: "result", ReceiptKey: stored.ResultKey, ExpectedEpoch: work.Execution.AdmissionEpoch, ExpectedHash: string(r.OutputDigest), DueAt: stored.CreatedAt}); err != nil {
					return run.IntakeReceipt{}, err
				}
			}
		}
		return receiptFromResult(ctx, tx, stored)
	})
}

// FrozenSourceWindow supplies the immutable source inputs and retry allowances.
type FrozenSourceWindow struct {
	// Request fixes source, slice, cadence, cutoff, policy and original budget.
	Request api.DiscoverRequest
	// Deadline bounds new discovery, not reconciliation of recorded receipts.
	Deadline time.Time
	// PageBudget limits the number of registered discovery pages.
	PageBudget int32
	// RequestBudget limits document requests registered against this window.
	RequestBudget int32
	// AttemptLimit freezes the per-request source attempt allowance.
	AttemptLimit int32
	// RetryInterval freezes the per-request retry duration, at most 24 hours.
	RetryInterval time.Duration
}

// OpenWindow freezes one window under its source-window subject.
func (s *Sources) OpenWindow(ctx context.Context, work run.IntakeWork, window FrozenSourceWindow) (run.IntakeReceipt, error) {
	if err := window.Request.Validate(); err != nil {
		return run.IntakeReceipt{}, fmt.Errorf("window: %w", err)
	}
	r := window.Request
	if r.Country != work.Execution.Country || r.WindowKey != work.Execution.Scope.Subject.LeaseKey() || window.Deadline.IsZero() || window.PageBudget <= 0 || window.RequestBudget <= 0 || window.AttemptLimit < 1 || window.AttemptLimit > 5 || window.RetryInterval <= 0 || window.RetryInterval > 24*time.Hour || int(window.PageBudget) > r.Budget.MaxPages {
		return run.IntakeReceipt{}, fmt.Errorf("invalid frozen window")
	}
	if r.Budget != model.ResourceBudget(work.Budget) || r.BudgetHash != work.BudgetDigest || int(window.RequestBudget) > r.Budget.MaxItems {
		return run.IntakeReceipt{}, fmt.Errorf("window differs from frozen work budget")
	}
	return s.sourceMutation(ctx, work, run.SubjectSourceWindow, "open_window", window, func(tx pgx.Tx, q intakedb.Querier, _ intakedb.IngestExecutionResult) error {
		e := work.Execution
		budget, err := sourceJSON(r.Budget)
		if err != nil {
			return err
		}
		p := intakedb.OpenSourceWindowParams{Column1: e.AdmissionEpoch, AdmittedWriter: s.ledger.writer, Column3: "source_window", WindowKey: r.WindowKey, Column5: e.Owner, Column6: e.AttemptFence, SourceID: r.SourceID, SliceKey: r.SliceKey, Mode: string(r.Mode), StartsAt: r.Start, CutoffAt: r.Cutoff, DeadlineAt: window.Deadline, PolicyDigest: string(r.PolicyHash), BudgetDigest: string(r.BudgetHash), Budget: budget, PageBudget: window.PageBudget, RequestBudget: window.RequestBudget, SourceAttemptLimit: window.AttemptLimit, InitialCursor: r.Cursor}
		p.SourceRetryInterval.Microseconds, p.SourceRetryInterval.Valid = window.RetryInterval.Microseconds(), true
		_, err = q.OpenSourceWindow(ctx, p)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return grantError("open window", err)
		}
		stored, err := q.ReadSourceWindow(ctx, r.WindowKey)
		if err != nil {
			return grantError("read frozen window", err)
		}
		if stored.SourceID != p.SourceID || stored.SliceKey != p.SliceKey || stored.Mode != p.Mode || !sameSourceTime(stored.StartsAt, p.StartsAt) || !sameSourceTime(stored.CutoffAt, p.CutoffAt) || !sameSourceTime(stored.DeadlineAt, p.DeadlineAt) || stored.PolicyDigest != p.PolicyDigest || stored.BudgetDigest != p.BudgetDigest || !sameSourceJSON(stored.Budget, budget) || stored.PageBudget != p.PageBudget || stored.RequestBudget != p.RequestBudget || stored.SourceAttemptLimit != p.SourceAttemptLimit || stored.SourceRetryInterval != p.SourceRetryInterval || stored.InitialCursor != p.InitialCursor {
			return fmt.Errorf("frozen window payload: %w", run.ErrPayloadConflict)
		}
		return nil
	})
}

func sameSourceTime(a, b time.Time) bool {
	return a.Equal(b.UTC().Truncate(time.Microsecond))
}

func (s *Sources) lockWindow(ctx context.Context, tx pgx.Tx, q intakedb.Querier, work run.IntakeWork, key string) (intakedb.IngestSourceWindow, error) {
	if work.Execution.Scope.Subject.Kind == run.SubjectSourceWindow && key != work.Execution.Scope.Subject.LeaseKey() {
		return intakedb.IngestSourceWindow{}, fmt.Errorf("source window binding: %w", run.ErrStaleGrant)
	}
	// RAW QUERY: LockSourceAccountingWindow. The transaction wrapper validated
	// current admission and lease; cancellation permits closure and release.
	var id int64
	if err := tx.QueryRow(ctx, `SELECT id FROM ingest.source_window WHERE window_key = $1 FOR UPDATE`, key).Scan(&id); err != nil {
		return intakedb.IngestSourceWindow{}, grantError("lock source window", err)
	}
	w, err := q.ReadSourceWindow(ctx, key)
	if err != nil {
		return w, grantError("read source window", err)
	}
	return w, nil
}

// ReadWindow reads a source window under its current registered execution.
func (s *Sources) ReadWindow(ctx context.Context, work run.IntakeWork) (intakedb.IngestSourceWindow, error) {
	return sourceTransaction(ctx, s, work, run.SubjectSourceWindow, func(_ pgx.Tx, q intakedb.Querier) (intakedb.IngestSourceWindow, error) {
		w, err := q.ReadSourceWindow(ctx, work.Execution.Scope.Subject.LeaseKey())
		if err != nil {
			return w, grantError("read window", err)
		}
		return w, nil
	})
}

// SourcePagePlan binds a ledger page ordinal to its frozen request plan.
type SourcePagePlan struct {
	// PageKey is the opaque page identity, separate from the HTTP request key.
	PageKey string
	// Ordinal is ledger order, not the decoder's cursor ordinal.
	Ordinal int32
	// Plan contains the screened local request and originating frozen budget.
	Plan api.RequestPlan
	// RetryStartedAt anchors the frozen source retry deadline.
	RetryStartedAt time.Time
}

func windowBudget(w intakedb.IngestSourceWindow) (model.ResourceBudget, error) {
	var b model.ResourceBudget
	if err := json.Unmarshal(w.Budget, &b); err != nil {
		return b, fmt.Errorf("decode frozen source budget: %w", err)
	}
	return b, b.Validate()
}

func validateSourcePlan(work run.IntakeWork, w intakedb.IngestSourceWindow, plan api.RequestPlan) error {
	b, err := windowBudget(w)
	if err != nil {
		return err
	}
	if err := plan.ValidateOrigin(w.WindowKey, model.Digest(w.BudgetDigest), b); err != nil {
		return fmt.Errorf("plan origin: %w", err)
	}
	if plan.Country != work.Execution.Country || plan.SourceID != w.SourceID || string(plan.PolicyHash) != w.PolicyDigest {
		return fmt.Errorf("plan crosses frozen source or policy")
	}
	return nil
}

// RegisterPage locks the window in a separate statement before counting pages.
// A recorded WebForms continuation requires the complete trusted Continuer plan.
// Numeric fallback preserves frozen transport fields; locator changes require
// an explicit decoded listing follow-up.
func (s *Sources) RegisterPage(ctx context.Context, work run.IntakeWork, page SourcePagePlan) (run.IntakeReceipt, error) {
	if err := page.Plan.Validate(); err != nil {
		return run.IntakeReceipt{}, fmt.Errorf("page request plan: %w", err)
	}
	if err := model.ValidateEnvelopeKey(page.PageKey, "page"); err != nil {
		return run.IntakeReceipt{}, fmt.Errorf("page key: %w", err)
	}
	if page.Ordinal < 0 || page.RetryStartedAt.IsZero() || (page.Plan.Purpose != model.PurposeListing && page.Plan.Purpose != model.PurposeNumber) {
		return run.IntakeReceipt{}, fmt.Errorf("invalid page plan")
	}
	return s.sourceMutation(ctx, work, run.SubjectSourceWindow, "register_page", page, func(tx pgx.Tx, q intakedb.Querier, _ intakedb.IngestExecutionResult) error {
		w, err := s.lockWindow(ctx, tx, q, work, page.Plan.WindowKey)
		if err != nil {
			return err
		}
		if err := validateSourcePlan(work, w, page.Plan); err != nil {
			return err
		}
		if page.Ordinal == 0 {
			if page.Plan.Cursor != w.InitialCursor || page.Plan.ParentCapture != "" {
				return fmt.Errorf("first page differs from frozen initial cursor: %w", run.ErrPayloadConflict)
			}
		} else {
			// RAW QUERY: ReadDiscoveryPredecessor. Only a recorded decode can
			// authorize a continuation; acceptance may arrive out of order.
			var raw, priorPlanJSON json.RawMessage
			var priorOrdinal int32
			if err := tx.QueryRow(ctx, `SELECT r.payload->'SourcePayload', p.request_plan, p.ordinal FROM ingest.discovery_page p
				JOIN ingest.execution_result r ON r.payload->>'SourceOperation' = 'page_decode'
				AND r.payload->'SourcePayload'->>'PageKey' = p.page_key
				JOIN ingest.execution e ON e.id = r.execution_id AND e.resource_kind = 'source_window' AND e.resource_key = $1
				WHERE p.source_window_id = $2 AND p.ordinal < $3 AND p.decoded_digest IS NOT NULL
				AND r.payload->'SourcePayload'->'Capture'->>'CaptureKey' = $5
				AND (p.ordinal = $3 - 1 OR EXISTS (SELECT 1 FROM jsonb_array_elements(
					CASE WHEN jsonb_typeof(r.payload->'SourcePayload'->'FollowUps') = 'array'
					THEN r.payload->'SourcePayload'->'FollowUps' ELSE '[]'::jsonb END) f(value)
					WHERE f.value->>'RequestKey' = $4 AND f.value->>'ParentCapture' = $5))
				ORDER BY r.id LIMIT 1`, w.WindowKey, w.ID, page.Ordinal, page.Plan.RequestKey, page.Plan.ParentCapture).Scan(&raw, &priorPlanJSON, &priorOrdinal); err != nil {
				return grantError("read discovery predecessor", err)
			}
			var previous api.DecodedDiscoveryPage
			if err := json.Unmarshal(raw, &previous); err != nil {
				return fmt.Errorf("decode discovery predecessor: %w", err)
			}
			var priorPlan api.RequestPlan
			if err := json.Unmarshal(priorPlanJSON, &priorPlan); err != nil {
				return fmt.Errorf("decode predecessor plan: %w", err)
			}
			matched := false
			for _, follow := range previous.FollowUps {
				if previous.Continuation == nil && follow.RequestKey == page.Plan.RequestKey && follow.ParentCapture == page.Plan.ParentCapture && follow.URL == page.Plan.URL && follow.Purpose == page.Plan.Purpose {
					matched = page.Plan.Cursor == strconv.FormatInt(int64(follow.Ordinal)+1, 10) && page.Plan.Method == "GET" && len(page.Plan.Body) == 0 && reflect.DeepEqual(page.Plan.Headers, priorPlan.Headers)
				}
			}
			if !matched && priorOrdinal+1 == page.Ordinal && !previous.Cursor.Complete && len(previous.Findings) == 0 {
				matched = s.matchesDiscoveryContinuation(w, priorPlan, previous, page.Plan)
			}
			if !matched || page.Plan.ParentCapture != previous.Capture.CaptureKey || previous.Cursor.Complete && previous.Continuation == nil {
				return fmt.Errorf("page lacks recorded continuation provenance: %w", run.ErrPayloadConflict)
			}
		}
		plan, err := sourceJSON(page.Plan)
		if err != nil {
			return err
		}
		e := work.Execution
		_, err = q.RegisterDiscoveryPage(ctx, intakedb.RegisterDiscoveryPageParams{AdmissionEpoch: e.AdmissionEpoch, AdmittedWriter: s.ledger.writer, ResourceKind: "source_window", ResourceKey: w.WindowKey, Column5: e.Owner, Column6: e.AttemptFence, PageKey: page.PageKey, Ordinal: page.Ordinal, Purpose: string(page.Plan.Purpose), RequestCursor: page.Plan.Cursor, RequestPlan: plan, RetryStartedAt: page.RetryStartedAt})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return grantError("register page", err)
		}
		stored, err := readSourcePage(ctx, tx, w.WindowKey, page.PageKey)
		if err != nil {
			return err
		}
		if stored.Ordinal != page.Ordinal || stored.Purpose != string(page.Plan.Purpose) || stored.RequestCursor != page.Plan.Cursor || !sameSourceJSON(stored.RequestPlan, plan) || !sameSourceTime(stored.RetryStartedAt, page.RetryStartedAt) {
			return fmt.Errorf("page plan payload: %w", run.ErrPayloadConflict)
		}
		return nil
	})
}

// matchesDiscoveryContinuation compares the complete trusted continuation plan.
// Numeric continuations retain frozen transport semantics. WebForms requires a
// deterministic Continuer, since decoded state alone does not contain defaults.
func (s *Sources) matchesDiscoveryContinuation(w intakedb.IngestSourceWindow, origin api.RequestPlan, page api.DecodedDiscoveryPage, next api.RequestPlan) bool {
	cursor, err := strconv.ParseUint(origin.Cursor, 10, 32)
	if err != nil || cursor == 0 || cursor == 1<<32-1 {
		return false
	}
	if page.Continuation != nil {
		if s.continuer == nil {
			return false
		}
		expected, err := s.continuer.NextPage(api.ContinuationRequest{
			Origin: origin, WindowKey: w.WindowKey, PolicyHash: model.Digest(w.PolicyDigest),
			BudgetHash: model.Digest(w.BudgetDigest), Budget: origin.Budget, Mode: api.DiscoveryMode(w.Mode),
			ParentCapture: page.Capture, Target: page.Continuation.Target, Fields: page.Continuation.Fields,
		})
		return err == nil && expected.Validate() == nil && expected.Cursor == strconv.FormatUint(cursor+1, 10) && reflect.DeepEqual(expected, next)
	}
	expected := origin
	expected.RequestKey, expected.Cursor, expected.ParentCapture = next.RequestKey, strconv.FormatUint(cursor+1, 10), page.Capture.CaptureKey
	return reflect.DeepEqual(expected, next)
}

// SourcePage is the persisted accounting projection, including immutable receipts.
type SourcePage struct {
	// ID is the database row identity.
	ID int64
	// PageKey is the opaque page receipt identity.
	PageKey string
	// Ordinal is the ledger-assigned order within the window.
	Ordinal int32
	// Purpose is listing or number.
	Purpose string
	// RequestCursor is frozen independently of the window checkpoint.
	RequestCursor string
	// RequestPlan retains the exact local screened plan.
	RequestPlan json.RawMessage
	// RetryStartedAt is the start of the frozen source allowance.
	RetryStartedAt time.Time
	// RetryDeadlineAt is the end of the frozen source allowance.
	RetryDeadlineAt time.Time
	// State belongs to model.DiscoveryPageState.
	State string
	// Reason is present exactly for failed pages.
	Reason *string
	// CaptureKey is absent until a successful transport receipt exists.
	CaptureKey *string
	// CaptureDigest is the receipt's exact staged content hash.
	CaptureDigest *string
	// CaptureBytes is the receipt's exact staged byte length.
	CaptureBytes *int64
	// TransportReceipt retains the full immutable local CaptureRef.
	TransportReceipt json.RawMessage
	// DecodedDigest hashes the complete validated decoder payload.
	DecodedDigest *string
	// InfrastructureAttempts reserves the current infrastructure ordinal, capped at three.
	InfrastructureAttempts int32
	// SourceAttempts counts durable source outcomes, including success.
	SourceAttempts int32
	// AttemptLimit is inherited from the frozen window.
	AttemptLimit int32
	// LastAttemptKey identifies the most recent durable source outcome.
	LastAttemptKey *string
	// ClaimOrdinal counts issued fetches; ClaimState owns the host slot.
	ClaimOrdinal int32
	ClaimState   *string
}

// RAW QUERY: ReadDiscoveryPage. Window identity is part of every page lookup.
func readSourcePage(ctx context.Context, tx pgx.Tx, window, key string) (SourcePage, error) {
	var p SourcePage
	err := tx.QueryRow(ctx, `SELECT p.id, p.page_key, p.ordinal, p.purpose, p.request_cursor,
		p.request_plan, p.retry_started_at, p.retry_deadline_at, p.state, p.reason,
		p.capture_key, p.capture_digest, p.capture_bytes, p.transport_receipt,
		p.decoded_digest, p.infrastructure_attempts, p.source_attempts, p.source_attempt_limit, p.last_attempt_key, p.claim_ordinal, p.claim_state
		FROM ingest.discovery_page p JOIN ingest.source_window w ON w.id = p.source_window_id
		WHERE w.window_key = $1 AND p.page_key = $2`, window, key).Scan(&p.ID, &p.PageKey, &p.Ordinal, &p.Purpose, &p.RequestCursor, &p.RequestPlan, &p.RetryStartedAt, &p.RetryDeadlineAt, &p.State, &p.Reason, &p.CaptureKey, &p.CaptureDigest, &p.CaptureBytes, &p.TransportReceipt, &p.DecodedDigest, &p.InfrastructureAttempts, &p.SourceAttempts, &p.AttemptLimit, &p.LastAttemptKey, &p.ClaimOrdinal, &p.ClaimState)
	if err != nil {
		return p, grantError("read source page", err)
	}
	return p, nil
}

// ReadPage reads one page under its owning window grant.
func (s *Sources) ReadPage(ctx context.Context, work run.IntakeWork, key string) (SourcePage, error) {
	return sourceTransaction(ctx, s, work, run.SubjectSourceWindow, func(tx pgx.Tx, _ intakedb.Querier) (SourcePage, error) {
		return readSourcePage(ctx, tx, work.Execution.Scope.Subject.LeaseKey(), key)
	})
}

// SourceCaptureReceipt freezes one durable transport attempt and staged capture.
type SourceCaptureReceipt struct {
	// ClaimResult binds this outcome to the durable host-slot claim receipt.
	ClaimResult string
	// Key selects the page under the owning window.
	Key string
	// AttemptKey is the durable source attempt identity.
	AttemptKey string
	// Attempt is the next source ordinal, independent of infrastructure retries.
	Attempt int32
	// Capture identifies immutable staged bytes and transport provenance.
	Capture model.CaptureRef
}

func validateCapture(work run.IntakeWork, w intakedb.IngestSourceWindow, c model.CaptureRef) error {
	if err := c.Validate(); err != nil {
		return fmt.Errorf("capture receipt: %w", err)
	}
	if err := model.ValidateEnvelopeKey(c.CaptureKey, "capture"); err != nil {
		return fmt.Errorf("capture key: %w", err)
	}
	if c.Country != work.Execution.Country || c.SourceID != w.SourceID {
		return fmt.Errorf("capture crosses country or source")
	}
	b, err := windowBudget(w)
	if err != nil {
		return err
	}
	if c.Asset.ByteSize > b.MaxInputBytes || c.Asset.ByteSize > work.Budget.MaxInputBytes || c.TransportReceipt.ByteSize > work.Budget.MaxInputBytes {
		return fmt.Errorf("capture exceeds frozen byte allowance")
	}
	return nil
}

func captureMatches(key, digest *string, size *int64, receipt json.RawMessage, capture model.CaptureRef) bool {
	encoded, err := sourceJSON(capture)
	return err == nil && key != nil && digest != nil && size != nil && *key == capture.CaptureKey && *digest == string(capture.Asset.SHA256) && *size == capture.Asset.ByteSize && sameSourceJSON(receipt, encoded)
}

type sourcePageClaimPayload struct {
	PageKey string
}

type sourceClaimIdentity struct {
	Country    string
	WindowKey  string
	PageKey    string
	Ordinal    int32
	Attempt    int32
	AttemptKey string
}

type sourcePageClaim struct {
	ResultKey string
	Work      run.IntakeWork
	HostKey   string
	Digest    string
	Identity  sourceClaimIdentity
}

func discoveryAttemptKey(window, page string, ordinal int32) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("discovery-claim-v1:%s:%s:%d", window, page, ordinal)))
	return "request-" + hex.EncodeToString(h[:16])
}

// RAW QUERY: ReadLatestDiscoveryClaim. The page marker, not receipt absence,
// owns the slot. Historical execution identity supplies provenance only.
func latestDiscoveryClaim(ctx context.Context, tx pgx.Tx, window, page, state string, currentResult int64) (sourcePageClaim, error) {
	var claim sourcePageClaim
	var raw, identity json.RawMessage
	err := tx.QueryRow(ctx, `SELECT r.result_key, r.payload->'Work', p.request_plan->>'URL', r.output_digest, r.payload->'Claim'
		FROM ingest.discovery_page p JOIN ingest.source_window w ON w.id = p.source_window_id
		JOIN ingest.execution e ON e.resource_kind = 'source_window' AND e.resource_key = w.window_key
		JOIN ingest.execution_result r ON r.execution_id = e.id
		WHERE w.window_key = $1 AND p.page_key = $2 AND p.claim_state = $3 AND r.id <> $4
		AND r.payload->>'SourceOperation' = 'claim_page'
		AND r.payload->'Claim'->>'WindowKey' = w.window_key AND r.payload->'Claim'->>'PageKey' = p.page_key
		AND (r.payload->'Claim'->>'Ordinal')::integer = p.claim_ordinal
		ORDER BY r.id DESC LIMIT 1`, window, page, state, currentResult).Scan(&claim.ResultKey, &raw, &claim.HostKey, &claim.Digest, &identity)
	if err != nil {
		return claim, err
	}
	if err := json.Unmarshal(raw, &claim.Work); err != nil {
		return claim, err
	}
	if err := json.Unmarshal(identity, &claim.Identity); err != nil {
		return claim, err
	}
	u, err := url.Parse(claim.HostKey)
	if err != nil {
		return claim, err
	}
	claim.HostKey, err = sourceHostKey(u.Host)
	return claim, err
}

func activeDiscoveryClaim(ctx context.Context, tx pgx.Tx, window, page string, currentResult int64) (sourcePageClaim, error) {
	return latestDiscoveryClaim(ctx, tx, window, page, string(model.DiscoveryClaimIssued), currentResult)
}

// SourceDiscoveryClaim carries the durable host claim receipt and its issued
// transport attempt key and source ordinal. Infrastructure recovery retains
// the source ordinal; replacement issue allocates a new claim key.
type SourceDiscoveryClaim struct {
	run.IntakeReceipt
	// AttemptKey is stable on replay and changes with each issued claim.
	AttemptKey string
	// NextAttempt is the source ordinal recorded by capture or retry.
	NextAttempt int32
}

// ClaimPage claims one requested discovery page and a canonical shared host
// slot. The result is the claim identity for RecordPageCapture/RecordPageRetry.
// Reuse the work for response-loss replay; recovery needs a new claim substep.
// New source work requires window, lease and host eligibility and capacity.
func (s *Sources) ClaimPage(ctx context.Context, work run.IntakeWork, key string) (SourceDiscoveryClaim, error) {
	if s.reconciliation {
		return SourceDiscoveryClaim{}, fmt.Errorf("reconciliation cannot issue discovery fetch: %w", run.ErrStaleGrant)
	}
	receipt, err := s.sourceMutation(ctx, work, run.SubjectSourceWindow, "claim_page", sourcePageClaimPayload{key}, func(tx pgx.Tx, q intakedb.Querier, result intakedb.IngestExecutionResult) error {
		w, err := s.lockWindow(ctx, tx, q, work, work.Execution.Scope.Subject.LeaseKey())
		if err != nil {
			return err
		}
		p, err := readSourcePage(ctx, tx, w.WindowKey, key)
		if err != nil {
			return err
		}
		if p.State != "requested" || w.State == "complete" || w.State == "failed" {
			return fmt.Errorf("terminal discovery claim: %w", run.ErrStaleGrant)
		}
		if p.ClaimState != nil && *p.ClaimState == "issued" {
			return fmt.Errorf("discovery page already claimed: %w", run.ErrStaleGrant)
		}
		if p.ClaimState != nil && *p.ClaimState == "released" && p.InfrastructureAttempts == 3 {
			return failDiscoveryInfrastructure(ctx, tx, w, key)
		}
		var plan api.RequestPlan
		if err := json.Unmarshal(p.RequestPlan, &plan); err != nil {
			return fmt.Errorf("decode discovery request: %w", err)
		}
		u, err := url.Parse(plan.URL)
		if err != nil {
			return fmt.Errorf("discovery request host")
		}
		host, err := sourceHostKey(u.Host)
		if err != nil {
			return err
		}
		// RAW QUERY: LockDiscoveryHost. Window precedes canonical host.
		var hostID int64
		if err := tx.QueryRow(ctx, `SELECT id FROM ingest.source_rate_limit WHERE host_key = $1 FOR UPDATE`, host).Scan(&hostID); err != nil {
			return grantError("lock discovery host", err)
		}
		// RAW QUERY: ClaimDiscoveryHost. Eligibility uses a fresh database
		// clock after the host lock wait, including the window lease watermark.
		if err := tx.QueryRow(ctx, `UPDATE ingest.source_rate_limit h SET in_flight = h.in_flight + 1,
			next_allowed_at = clock_timestamp() + h.min_interval
			FROM ingest.source_window w, ingest.discovery_page p, ingest.execution_lease l
			WHERE h.id = $1 AND h.next_allowed_at <= clock_timestamp() AND h.in_flight < h.max_in_flight
			AND w.id = $2 AND w.state IN ('frozen','progressing') AND w.deadline_at > clock_timestamp()
			AND p.id = $3 AND p.state = 'requested' AND p.retry_deadline_at > clock_timestamp()
			AND p.infrastructure_attempts <= 3 AND p.source_attempts < p.source_attempt_limit AND (p.retry_after IS NULL OR p.retry_after <= clock_timestamp())
			AND l.resource_kind = 'source_window' AND l.resource_key = w.window_key AND l.next_eligible_at <= clock_timestamp()
			RETURNING h.id`, hostID, w.ID, p.ID).Scan(&hostID); err != nil {
			return grantError("claim discovery host slot", err)
		}
		// RAW QUERY: IssueDiscoveryClaim. Advance the infrastructure ordinal
		// only when replacing a released claim, then acquire the new identity.
		if _, err := tx.Exec(ctx, `UPDATE ingest.discovery_page SET claim_ordinal = claim_ordinal + 1,
			infrastructure_attempts = infrastructure_attempts + CASE WHEN claim_state = 'released' THEN 1 ELSE 0 END,
			claim_state = 'issued' WHERE id = $1`, p.ID); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return SourceDiscoveryClaim{}, err
	}
	// RAW QUERY: ReadDiscoveryClaimAttempt. Immutable claim identity survives
	// later claims, source outcomes, and separately scheduled reconciliation.
	return sourceTransaction(ctx, s, work, run.SubjectSourceWindow, func(tx pgx.Tx, _ intakedb.Querier) (SourceDiscoveryClaim, error) {
		var identity sourceClaimIdentity
		var substep string
		if err := tx.QueryRow(ctx, `SELECT payload->'Claim', substep FROM ingest.execution_result WHERE result_key = $1`, receipt.ResultID).Scan(&identity, &substep); err != nil {
			return SourceDiscoveryClaim{}, grantError("read issued claim identity", err)
		}
		page, err := readSourcePage(ctx, tx, work.Execution.Scope.Subject.LeaseKey(), key)
		if err != nil {
			return SourceDiscoveryClaim{}, err
		}
		if substep == "source-discovery-exhaustion" || page.ClaimOrdinal < identity.Ordinal {
			return SourceDiscoveryClaim{}, fmt.Errorf("discovery replacement exhausted: %w", run.ErrStaleGrant)
		}
		return SourceDiscoveryClaim{receipt, identity.AttemptKey, identity.Attempt}, nil
	})
}

func requireDiscoveryClaim(ctx context.Context, tx pgx.Tx, work run.IntakeWork, key, resultKey string, resultID int64, attempt int32, attemptKey string, p SourcePage) (sourcePageClaim, error) {
	claim, err := activeDiscoveryClaim(ctx, tx, work.Execution.Scope.Subject.LeaseKey(), key, resultID)
	if err != nil {
		return claim, grantError("resolve discovery host claim", err)
	}
	if p.State != "requested" || claim.ResultKey != resultKey || claim.Identity.Country != work.Execution.Country || claim.Identity.WindowKey != work.Execution.Scope.Subject.LeaseKey() || claim.Identity.PageKey != key || claim.Identity.Ordinal != p.ClaimOrdinal || attempt != p.SourceAttempts+1 || attempt != claim.Identity.Attempt || attemptKey != claim.Identity.AttemptKey {
		return claim, fmt.Errorf("discovery claim binding: %w", run.ErrStaleGrant)
	}
	return claim, nil
}

func releaseDiscoveryHost(ctx context.Context, tx pgx.Tx, claim sourcePageClaim, retryAfter *time.Time) error {
	// RAW QUERY: ReleaseDiscoveryHost. Only an active claim under the locked
	// window authorizes release; the committed outcome consumes that claim.
	var id int64
	if err := tx.QueryRow(ctx, `UPDATE ingest.source_rate_limit SET in_flight = in_flight - 1,
		next_allowed_at = GREATEST(next_allowed_at, $2::timestamptz)
		WHERE host_key = $1 AND in_flight > 0 RETURNING id`, claim.HostKey, retryAfter).Scan(&id); err != nil {
		return grantError("release discovery host slot", err)
	}
	return nil
}

// closeDiscoveryClaims releases only page markers that still own slots. All
// page and host locks were acquired by prelockSourceAccounting before writes.
func closeDiscoveryClaims(ctx context.Context, tx pgx.Tx, w intakedb.IngestSourceWindow) error {
	// RAW QUERY: ReadIssuedDiscoveryClosurePages. Bound by frozen page budget.
	rows, err := tx.Query(ctx, `SELECT page_key FROM ingest.discovery_page
		WHERE source_window_id = $1 AND claim_state = 'issued' ORDER BY page_key COLLATE "C" LIMIT $2`, w.ID, w.PageBudget)
	if err != nil {
		return err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, key := range keys {
		claim, err := activeDiscoveryClaim(ctx, tx, w.WindowKey, key, 0)
		if err != nil {
			return grantError("read closure claim", err)
		}
		if err := releaseDiscoveryHost(ctx, tx, claim, nil); err != nil {
			return err
		}
		// RAW QUERY: ReleaseDiscoveryClaim. Closure changes no attempt counter.
		if _, err := tx.Exec(ctx, `UPDATE ingest.discovery_page SET claim_state = 'released'
			WHERE source_window_id = $1 AND page_key = $2 AND claim_state = 'issued'`, w.ID, key); err != nil {
			return err
		}
	}
	return nil
}

// releasedDiscoveryClaim permits the current owner to settle only the latest
// released fetch while the page remains requested. Current grant checks precede
// this lookup; historical workflow IDs never authorize settlement.
func releasedDiscoveryClaim(ctx context.Context, tx pgx.Tx, work run.IntakeWork, retry SourceRetryReceipt, resultID int64, p SourcePage) (sourcePageClaim, error) {
	claim, err := latestDiscoveryClaim(ctx, tx, work.Execution.Scope.Subject.LeaseKey(), retry.PageKey, "released", resultID)
	if err != nil {
		return claim, grantError("read released discovery claim", err)
	}
	if p.State != "requested" || claim.ResultKey != retry.ClaimResult || claim.Identity.Country != work.Execution.Country || claim.Identity.WindowKey != work.Execution.Scope.Subject.LeaseKey() || claim.Identity.PageKey != retry.PageKey || claim.Identity.Ordinal != p.ClaimOrdinal || retry.Attempt != p.SourceAttempts+1 || retry.Attempt != claim.Identity.Attempt || retry.AttemptKey != claim.Identity.AttemptKey {
		return claim, fmt.Errorf("released discovery claim binding: %w", run.ErrStaleGrant)
	}
	return claim, nil
}

// failDiscoveryInfrastructure denies replacement at the reserved third ordinal.
// Its caller's immutable operation receipt records exhaustion atomically.
func failDiscoveryInfrastructure(ctx context.Context, tx pgx.Tx, w intakedb.IngestSourceWindow, key string) error {
	if err := closeDiscoveryClaims(ctx, tx, w); err != nil {
		return err
	}
	// RAW QUERY: ExhaustDiscoveryInfrastructure. Keep all earlier receipt groups.
	if _, err := tx.Exec(ctx, `UPDATE ingest.discovery_page SET state = 'failed',
		reason = CASE WHEN page_key = $2 THEN 'infrastructure_retry_exhausted' ELSE 'window_closed' END
		WHERE source_window_id = $1 AND state IN ('requested','captured','decoded')`, w.ID, key); err != nil {
		return err
	}
	// RAW QUERY: ExhaustDiscoveryWindow.
	_, err := tx.Exec(ctx, `UPDATE ingest.source_window SET state = 'failed', reason = 'infrastructure_retry_exhausted'
		WHERE id = $1 AND state IN ('frozen','progressing','incomplete')`, w.ID)
	return err
}

// RecoverPage releases an abandoned discovery host claim under a newer owning
// fence. It retains source attempts, receipts and the lease eligibility watermark.
// Recovery releases a slot; replacement issue advances infrastructure accounting.
// The third loss fails the window and releases all remaining claims once.
// Recovery never fetches; captured/decoded pages reconcile their stored bytes.
func (s *Sources) RecoverPage(ctx context.Context, work run.IntakeWork, key, claimResult string) (run.IntakeReceipt, error) {
	payload := struct{ Key, ClaimResult string }{key, claimResult}
	return s.sourceMutation(ctx, work, run.SubjectSourceWindow, "recover_page", payload, func(tx pgx.Tx, q intakedb.Querier, result intakedb.IngestExecutionResult) error {
		w, err := s.lockWindow(ctx, tx, q, work, work.Execution.Scope.Subject.LeaseKey())
		if err != nil {
			return err
		}
		claim, err := activeDiscoveryClaim(ctx, tx, w.WindowKey, key, result.ID)
		if err != nil {
			return grantError("resolve abandoned discovery claim", err)
		}
		old, current := claim.Work.Execution, work.Execution
		if claim.ResultKey != claimResult || !(old.AdmissionEpoch < current.AdmissionEpoch || old.AdmissionEpoch == current.AdmissionEpoch && old.AttemptFence < current.AttemptFence) {
			return fmt.Errorf("discovery recovery fence: %w", run.ErrStaleGrant)
		}
		// RAW QUERY: RecoverDiscoveryInfrastructure. Loss releases the marker
		// without advancing the ordinal reserved for replacement issue.
		var exhausted bool
		if err := tx.QueryRow(ctx, `UPDATE ingest.discovery_page SET claim_state = 'released'
			WHERE page_key = $1 AND source_window_id = $2 AND state = 'requested' AND claim_state = 'issued'
			RETURNING infrastructure_attempts = 3`, key, w.ID).Scan(&exhausted); err != nil {
			return grantError("recover discovery infrastructure", err)
		}
		if err := releaseDiscoveryHost(ctx, tx, claim, nil); err != nil {
			return err
		}
		if exhausted {
			return failDiscoveryInfrastructure(ctx, tx, w, key)
		}

		return nil
	})
}

// RecordPageCapture commits transport evidence, counting the source attempt once.
func (s *Sources) RecordPageCapture(ctx context.Context, work run.IntakeWork, receipt SourceCaptureReceipt) (run.IntakeReceipt, error) {
	if err := model.ValidateEnvelopeKey(receipt.AttemptKey, "request"); err != nil {
		return run.IntakeReceipt{}, fmt.Errorf("source attempt: %w", err)
	}
	return s.sourceMutation(ctx, work, run.SubjectSourceWindow, "page_capture", receipt, func(tx pgx.Tx, q intakedb.Querier, result intakedb.IngestExecutionResult) error {
		w, err := s.lockWindow(ctx, tx, q, work, work.Execution.Scope.Subject.LeaseKey())
		if err != nil {
			return err
		}
		if err := validateCapture(work, w, receipt.Capture); err != nil {
			return err
		}
		p, err := readSourcePage(ctx, tx, w.WindowKey, receipt.Key)
		if err != nil {
			return err
		}
		if p.CaptureKey != nil {
			// RAW QUERY: ReadHistoricalDiscoveryCaptureOutcome. Reuse compares
			// the complete outcome, including its original page and claim binding.
			var prior json.RawMessage
			if err := tx.QueryRow(ctx, `SELECT r.payload->'SourcePayload' FROM ingest.execution_result r
				JOIN ingest.execution e ON e.id = r.execution_id
				WHERE e.resource_kind = 'source_window' AND e.resource_key = $1 AND r.id <> $2
				AND r.payload->>'SourceOperation' = 'page_capture' AND r.payload->'SourcePayload'->>'Key' = $3
				ORDER BY r.id LIMIT 1`, w.WindowKey, result.ID, receipt.Key).Scan(&prior); err != nil {
				return grantError("read historical page capture", err)
			}
			expected, err := sourceJSON(receipt)
			if err != nil {
				return err
			}
			if !sameSourceJSON(prior, expected) {
				return fmt.Errorf("historical capture claim binding: %w", run.ErrPayloadConflict)
			}
			if !captureMatches(p.CaptureKey, p.CaptureDigest, p.CaptureBytes, p.TransportReceipt, receipt.Capture) || p.LastAttemptKey == nil || *p.LastAttemptKey != receipt.AttemptKey || p.SourceAttempts != receipt.Attempt {
				return fmt.Errorf("page capture payload: %w", run.ErrPayloadConflict)
			}
			return nil
		}
		claim, err := requireDiscoveryClaim(ctx, tx, work, receipt.Key, receipt.ClaimResult, result.ID, receipt.Attempt, receipt.AttemptKey, p)
		released := false
		if err != nil && errors.Is(err, run.ErrStaleGrant) {
			claim, err = releasedDiscoveryClaim(ctx, tx, work, SourceRetryReceipt{PageKey: receipt.Key, ClaimResult: receipt.ClaimResult, Attempt: receipt.Attempt, AttemptKey: receipt.AttemptKey}, result.ID, p)
			released = err == nil
		}
		if err != nil {
			return err
		}
		encoded, err := sourceJSON(receipt.Capture)
		if err != nil {
			return err
		}
		digest := string(receipt.Capture.Asset.SHA256)
		// RAW QUERY: SettleDiscoveryCapture. A late issued outcome may commit
		// after the fetch deadline; it preserves an incomplete window's blocker.
		var id int64
		if err := tx.QueryRow(ctx, `UPDATE ingest.discovery_page p SET state = 'captured', claim_state = 'settled',
			capture_key = $2, capture_digest = $3, capture_bytes = $4, transport_receipt = $5,
			source_attempts = source_attempts + 1, last_attempt_key = $6
			FROM ingest.source_window w WHERE p.id = $1 AND w.id = p.source_window_id
			AND w.state IN ('frozen','progressing','incomplete') AND p.state = 'requested'
			AND p.claim_state IN ('issued','released') AND p.source_attempts + 1 = $7
			AND p.source_attempts < p.source_attempt_limit RETURNING p.id`, p.ID, receipt.Capture.CaptureKey, digest, receipt.Capture.Asset.ByteSize, encoded, receipt.AttemptKey, receipt.Attempt).Scan(&id); err != nil {
			return grantError("settle discovery capture", err)
		}
		if released {
			return nil
		}
		return releaseDiscoveryHost(ctx, tx, claim, nil)
	})
}

func validatePage(work run.IntakeWork, w intakedb.IngestSourceWindow, p SourcePage, page api.DecodedDiscoveryPage) error {
	if err := page.Validate(); err != nil {
		return fmt.Errorf("decoded page: %w", err)
	}
	if err := validateCapture(work, w, page.Capture); err != nil {
		return err
	}
	if page.PageKey != p.PageKey || page.Cursor.WindowKey != w.WindowKey || page.Cursor.SliceKey != w.SliceKey || page.Cursor.InitialCursor != p.RequestCursor || page.Cursor.CommittedCursor != p.RequestCursor || !captureMatches(p.CaptureKey, p.CaptureDigest, p.CaptureBytes, p.TransportReceipt, page.Capture) {
		return fmt.Errorf("decoded page differs from frozen request or capture")
	}
	b, err := windowBudget(w)
	if err != nil {
		return err
	}
	if page.Limits.MaxItems > b.MaxItems || page.Limits.MaxBytes > b.MaxInputBytes {
		return fmt.Errorf("decoded page exceeds frozen rule allowances")
	}
	for _, f := range page.FollowUps {
		if err := f.ValidateOrigin(w.WindowKey, model.Digest(w.BudgetDigest), b); err != nil {
			return err
		}
		if string(f.PolicyHash) != w.PolicyDigest {
			return fmt.Errorf("follow-up changes frozen policy")
		}
	}
	return nil
}

// RecordPageDecode validates a decoded result against the frozen request and
// preserves the complete local payload in its immutable execution receipt.
func (s *Sources) RecordPageDecode(ctx context.Context, work run.IntakeWork, page api.DecodedDiscoveryPage) (run.IntakeReceipt, error) {
	if page.Limits.MaxItems > work.Budget.MaxItems || page.Limits.MaxBytes > work.Budget.MaxInputBytes {
		return run.IntakeReceipt{}, fmt.Errorf("decoded page exceeds work budget")
	}
	if err := page.Validate(); err != nil {
		return run.IntakeReceipt{}, fmt.Errorf("decoded page: %w", err)
	}
	return s.sourceMutation(ctx, work, run.SubjectSourceWindow, "page_decode", page, func(tx pgx.Tx, q intakedb.Querier, _ intakedb.IngestExecutionResult) error {
		w, err := s.lockWindow(ctx, tx, q, work, page.Cursor.WindowKey)
		if err != nil {
			return err
		}
		p, err := readSourcePage(ctx, tx, w.WindowKey, page.PageKey)
		if err != nil {
			return err
		}
		if err := validatePage(work, w, p, page); err != nil {
			return err
		}
		digest, err := sourceDigest(page)
		if err != nil {
			return err
		}
		if p.DecodedDigest != nil {
			if *p.DecodedDigest != digest {
				return fmt.Errorf("page decode payload: %w", run.ErrPayloadConflict)
			}
			return nil
		}
		if page.Cursor.Ordinal > int(^uint32(0)>>1) {
			return fmt.Errorf("decoder cursor ordinal exceeds database bound")
		}
		ordinal := int32(page.Cursor.Ordinal)
		findings := page.Findings
		if findings == nil {
			findings = []model.Finding{}
		}
		encoded, err := sourceJSON(findings)
		if err != nil {
			return err
		}
		e := work.Execution
		params := intakedb.RecordDiscoveryPageDecodeParams{Column1: e.AdmissionEpoch, AdmittedWriter: s.ledger.writer, Column3: "source_window", WindowKey: w.WindowKey, Column5: e.Owner, Column6: e.AttemptFence, ID: p.ID, DecodedDigest: &digest, CursorInitial: &page.Cursor.InitialCursor, CursorCommitted: &page.Cursor.CommittedCursor, CursorNext: &page.Cursor.NextCursor, CursorOrdinal: &ordinal, Findings: encoded}
		params.Complete.Bool, params.Complete.Valid = page.Cursor.Complete, true
		_, err = q.RecordDiscoveryPageDecode(ctx, params)
		if err != nil {
			return grantError("decode page", err)
		}
		return nil
	})
}

// SourceRetryReceipt freezes backoff and Retry-After for one source attempt.
type SourceRetryReceipt struct {
	// ClaimResult binds this outcome to the durable host-slot claim receipt.
	ClaimResult string
	// PageKey selects the failed source request, not an accepted page.
	PageKey string
	// AttemptKey identifies the durable source outcome.
	AttemptKey string
	// Attempt is the next source ordinal.
	Attempt int32
	// Next is the recorded backoff, including jitter.
	Next time.Time
	// RetryAfter preserves the source's not-before clock.
	RetryAfter time.Time
}

// RecordPageRetry spends one source attempt and preserves lease eligibility.
// An issued failure may reconcile after incomplete closure under a current
// owner, including a newer admission or lease fence. A durable closure marker prevents a second host release.
func (s *Sources) RecordPageRetry(ctx context.Context, work run.IntakeWork, retry SourceRetryReceipt) (run.IntakeReceipt, error) {
	if err := model.ValidateEnvelopeKey(retry.AttemptKey, "request"); err != nil {
		return run.IntakeReceipt{}, err
	}
	if retry.Next.IsZero() || retry.RetryAfter.IsZero() {
		return run.IntakeReceipt{}, fmt.Errorf("retry scheduling clocks are required")
	}
	return s.sourceMutation(ctx, work, run.SubjectSourceWindow, "page_retry", retry, func(tx pgx.Tx, q intakedb.Querier, result intakedb.IngestExecutionResult) error {
		w, err := s.lockWindow(ctx, tx, q, work, work.Execution.Scope.Subject.LeaseKey())
		if err != nil {
			return err
		}
		p, err := readSourcePage(ctx, tx, w.WindowKey, retry.PageKey)
		if err != nil {
			return err
		}
		claim, err := requireDiscoveryClaim(ctx, tx, work, retry.PageKey, retry.ClaimResult, result.ID, retry.Attempt, retry.AttemptKey, p)
		released := false
		if err != nil && errors.Is(err, run.ErrStaleGrant) {
			claim, err = releasedDiscoveryClaim(ctx, tx, work, retry, result.ID, p)
			released = err == nil
		}
		if err != nil {
			return err
		}
		// RAW QUERY: RecordDiscoveryFailureReceipt. Current owner reconciliation
		// accepts issued failures on incomplete windows without clearing blockers.
		var failed bool
		if err := tx.QueryRow(ctx, `UPDATE ingest.discovery_page p SET source_attempts = source_attempts + 1, claim_state = 'settled',
			last_attempt_key = $2, retry_after = GREATEST(retry_after, $3::timestamptz),
			state = CASE WHEN w.state <> 'incomplete' AND w.deadline_at > clock_timestamp()
				AND (p.source_attempts + 1 >= p.source_attempt_limit OR p.retry_deadline_at <= clock_timestamp()) THEN 'failed' ELSE 'requested' END,
			reason = CASE WHEN w.state <> 'incomplete' AND w.deadline_at > clock_timestamp()
				AND (p.source_attempts + 1 >= p.source_attempt_limit OR p.retry_deadline_at <= clock_timestamp()) THEN 'source_retry_exhausted' ELSE NULL END
			FROM ingest.source_window w WHERE p.id = $1 AND w.id = p.source_window_id
			AND w.state IN ('frozen','progressing','incomplete') AND p.state = 'requested'
			AND p.source_attempts + 1 = $4 AND p.source_attempts < p.source_attempt_limit
			AND p.last_attempt_key IS DISTINCT FROM $2::text RETURNING p.state = 'failed'`, p.ID, retry.AttemptKey, retry.RetryAfter, retry.Attempt).Scan(&failed); err != nil {
			return grantError("record discovery failure", err)
		}
		// RAW QUERY: ScheduleDiscoveryFailure. Eligibility only advances.
		if _, err := tx.Exec(ctx, `UPDATE ingest.execution_lease SET next_eligible_at = GREATEST(next_eligible_at, $2::timestamptz, $3::timestamptz) WHERE resource_kind = 'source_window' AND resource_key = $1`, w.WindowKey, retry.Next, retry.RetryAfter); err != nil {
			return fmt.Errorf("schedule discovery failure: %w", err)
		}
		if !released {
			if err := releaseDiscoveryHost(ctx, tx, claim, &retry.RetryAfter); err != nil {
				return err
			}
		}
		if released {
			// RAW QUERY: AdvanceReleasedDiscoveryHostEligibility. Closure already
			// released the slot, but the issued Retry-After still advances pacing.
			if _, err := tx.Exec(ctx, `UPDATE ingest.source_rate_limit SET next_allowed_at = GREATEST(next_allowed_at, $2::timestamptz) WHERE host_key = $1`, claim.HostKey, retry.RetryAfter); err != nil {
				return fmt.Errorf("reconcile released discovery pacing: %w", err)
			}
		}
		if failed {
			if err := closeDiscoveryClaims(ctx, tx, w); err != nil {
				return err
			}
			// RAW QUERY: CloseSourceRetryExhaustedWindow.
			if _, err := tx.Exec(ctx, `UPDATE ingest.source_window SET state = 'failed', reason = 'source_retry_exhausted' WHERE id = $1`, w.ID); err != nil {
				return fmt.Errorf("close source retry window: %w", err)
			}
			// RAW QUERY: CloseSourceRetrySiblingDiscoveryPages.
			if _, err := tx.Exec(ctx, `UPDATE ingest.discovery_page SET state = 'failed', reason = 'window_closed' WHERE source_window_id = $1 AND id <> $2 AND state IN ('requested','captured','decoded')`, w.ID, p.ID); err != nil {
				return fmt.Errorf("close source retry siblings: %w", err)
			}
			return closeDiscoveryClaims(ctx, tx, w)
		}
		return reconcileSourceWindow(ctx, tx, w)
	})
}

// SourcePageFailure records an intrinsic source outcome from an issued fetch.
// Policy rejection before fetch and decode failures use CloseWindow instead.
type SourcePageFailure struct {
	PageKey     string
	ClaimResult string
	AttemptKey  string
	Attempt     int32
	Reason      string
}

// RecordPageFailure settles the latest issued or released claim and preserves
// all successful receipt groups when closing the failed page and its siblings.
func (s *Sources) RecordPageFailure(ctx context.Context, work run.IntakeWork, failure SourcePageFailure) (run.IntakeReceipt, error) {
	if err := model.ValidateKey(failure.Reason); err != nil {
		return run.IntakeReceipt{}, err
	}
	if err := model.ValidateEnvelopeKey(failure.AttemptKey, "request"); err != nil {
		return run.IntakeReceipt{}, err
	}
	return s.sourceMutation(ctx, work, run.SubjectSourceWindow, "page_failure", failure, func(tx pgx.Tx, q intakedb.Querier, result intakedb.IngestExecutionResult) error {
		w, err := s.lockWindow(ctx, tx, q, work, work.Execution.Scope.Subject.LeaseKey())
		if err != nil {
			return err
		}
		if w.State == "failed" || w.State == "complete" {
			return fmt.Errorf("terminal failure window: %w", run.ErrStaleGrant)
		}
		page, err := readSourcePage(ctx, tx, w.WindowKey, failure.PageKey)
		if err != nil {
			return err
		}
		claim, err := requireDiscoveryClaim(ctx, tx, work, failure.PageKey, failure.ClaimResult, result.ID, failure.Attempt, failure.AttemptKey, page)
		released := false
		if err != nil && errors.Is(err, run.ErrStaleGrant) {
			claim, err = releasedDiscoveryClaim(ctx, tx, work, SourceRetryReceipt{PageKey: failure.PageKey, ClaimResult: failure.ClaimResult, AttemptKey: failure.AttemptKey, Attempt: failure.Attempt}, result.ID, page)
			released = err == nil
		}
		if err != nil {
			return err
		}
		// RAW QUERY: SettleIntrinsicDiscoveryFailure. State and marker change
		// in one statement, keeping the claim CHECK valid.
		var id int64
		if err := tx.QueryRow(ctx, `UPDATE ingest.discovery_page SET state = 'failed', reason = $2,
			claim_state = 'settled', source_attempts = source_attempts + 1, last_attempt_key = $3
			WHERE id = $1 AND state = 'requested' AND claim_state IN ('issued','released')
			AND source_attempts + 1 = $4 AND source_attempts < source_attempt_limit RETURNING id`, page.ID, failure.Reason, failure.AttemptKey, failure.Attempt).Scan(&id); err != nil {
			return grantError("intrinsic source failure", err)
		}
		if !released {
			if err := releaseDiscoveryHost(ctx, tx, claim, nil); err != nil {
				return err
			}
		}
		if err := closeDiscoveryClaims(ctx, tx, w); err != nil {
			return err
		}
		// RAW QUERY: CloseIntrinsicDiscoverySiblings.
		if _, err := tx.Exec(ctx, `UPDATE ingest.discovery_page SET state = 'failed', reason = 'window_closed'
			WHERE source_window_id = $1 AND id <> $2 AND state IN ('requested','captured','decoded')`, w.ID, page.ID); err != nil {
			return err
		}
		// RAW QUERY: CloseIntrinsicDiscoveryWindow.
		_, err = tx.Exec(ctx, `UPDATE ingest.source_window SET state = 'failed', reason = $2 WHERE id = $1`, w.ID, failure.Reason)
		return err
	})
}

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

// DiscoveryAcceptance commits the exact decoded payload and required launches.
// Ordinal is ledger order, independent of Page.Cursor.Ordinal.
type DiscoveryAcceptance struct {
	// Page must exactly match its committed decode receipt.
	Page api.DecodedDiscoveryPage
	// Ordinal must match the page's ledger-assigned order.
	Ordinal int32
	// Launches accounts for every decoded detail follow-up.
	Launches []SourceLaunch
}

// ApplyDiscoveryPage atomically registers observations, retention, launch
// intents and page acceptance. It advances the checkpoint only after persisted
// exhaustion and launch acknowledgements prove complete-window accounting.
func (s *Sources) ApplyDiscoveryPage(ctx context.Context, work run.IntakeWork, acceptance DiscoveryAcceptance) (run.IntakeReceipt, error) {
	if acceptance.Page.Limits.MaxItems > work.Budget.MaxItems || acceptance.Page.Limits.MaxBytes > work.Budget.MaxInputBytes || len(acceptance.Launches) > work.Budget.MaxItems {
		return run.IntakeReceipt{}, fmt.Errorf("page acceptance exceeds work budget")
	}
	if err := acceptance.Page.Validate(); err != nil {
		return run.IntakeReceipt{}, fmt.Errorf("page acceptance: %w", err)
	}
	return s.sourceMutation(ctx, work, run.SubjectSourceWindow, "accept_page", acceptance, func(tx pgx.Tx, q intakedb.Querier, result intakedb.IngestExecutionResult) error {
		page := acceptance.Page
		w, err := s.lockWindow(ctx, tx, q, work, page.Cursor.WindowKey)
		if err != nil {
			return err
		}
		p, err := readSourcePage(ctx, tx, w.WindowKey, page.PageKey)
		if err != nil {
			return err
		}
		if err := validatePage(work, w, p, page); err != nil {
			return err
		}
		digest, err := sourceDigest(page)
		if err != nil {
			return err
		}
		if p.Ordinal != acceptance.Ordinal || p.DecodedDigest == nil || *p.DecodedDigest != digest {
			return fmt.Errorf("acceptance differs from decoded receipt: %w", run.ErrPayloadConflict)
		}
		if p.State != string(model.DiscoveryPageDecoded) {
			return fmt.Errorf("page acceptance transition: %w", run.ErrStaleGrant)
		}
		// RAW QUERY: ResolveSourceDecodeReceipt. Column shape alone cannot prove
		// the full sandbox payload was accepted under this window and recipe.
		var decodedPayload json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT r.payload->'SourcePayload' FROM ingest.execution_result r
			JOIN ingest.execution e ON e.id = r.execution_id
			WHERE e.resource_kind = 'source_window' AND e.resource_key = $1
			AND r.payload->>'SourceOperation' = 'page_decode'
			AND r.payload->'SourcePayload'->>'PageKey' = $2
			AND r.recipe_digest = $3 ORDER BY r.id LIMIT 1`, w.WindowKey, p.PageKey, string(work.RecipeDigest)).Scan(&decodedPayload); err != nil {
			return grantError("resolve decoded receipt", err)
		}
		exact, err := sourceJSON(page)
		if err != nil {
			return err
		}
		if !sameSourceJSON(decodedPayload, exact) {
			return fmt.Errorf("accepted decode payload: %w", run.ErrPayloadConflict)
		}
		executionID, executionKey, err := resolveExecution(ctx, tx, work)
		if err != nil {
			return err
		}
		if result.ExecutionID != executionID {
			return fmt.Errorf("page result execution: %w", run.ErrStaleGrant)
		}
		observedAt, err := sourceCaptureTime(ctx, tx, work, "page_capture", p.PageKey, page.Capture.CaptureKey)
		if err != nil {
			return err
		}
		if err := insertSourceRecords(ctx, q, page.Observations); err != nil {
			return err
		}
		blobID, err := s.registerCaptureBlob(ctx, q, page.Capture, observedAt)
		if err != nil {
			return err
		}
		observations := append([]api.DecodedObservation(nil), page.Observations...)
		sort.Slice(observations, func(i, j int) bool { return observations[i].ExternalID < observations[j].ExternalID })
		var revisions []intakedb.BronzeObservationRevision
		for _, decoded := range observations {
			observation, err := sourceObservation(decoded)
			if err != nil {
				return err
			}
			revision, err := registerObservation(ctx, tx, q, observation, w.WindowKey, blobID, executionKey, result, observedAt)
			if err != nil {
				return err
			}
			revisions = append(revisions, revision)
		}
		if err := protectCaptureExecution(ctx, tx, q, page.Capture, executionKey); err != nil {
			return err
		}
		sort.Slice(revisions, func(i, j int) bool { return revisions[i].ObservationKey < revisions[j].ObservationKey })
		for _, revision := range revisions {
			if err := protectObservationCapture(ctx, tx, q, page.Capture, blobID, revision.ObservationKey); err != nil {
				return err
			}
		}
		if err := registerSourceLaunches(ctx, tx, q, work, w, acceptance); err != nil {
			return err
		}
		e := work.Execution
		// Do not ask the generated CTE to infer exhaustion. Reconcile after
		// page acceptance in a fresh statement snapshot in this transaction.
		if _, err := q.AcceptDiscoveryPage(ctx, intakedb.AcceptDiscoveryPageParams{AdmissionEpoch: e.AdmissionEpoch, AdmittedWriter: s.ledger.writer, ResourceKind: "source_window", ResourceKey: w.WindowKey, Column5: e.Owner, AttemptFence: e.AttemptFence, ID: p.ID, Ordinal: p.Ordinal, Column9: false, AcceptanceResultID: &result.ID}); err != nil {
			return grantError("accept discovery page", err)
		}
		if w.State == string(model.SourceWindowIncomplete) {
			// RAW QUERY: PreserveSourceWindowBlocker. Evidence acceptance does
			// not authorize resumption or reset the owner's unresolved blocker.
			if _, err := tx.Exec(ctx, `UPDATE ingest.source_window SET state = 'incomplete', reason = $2 WHERE id = $1`, w.ID, w.Reason); err != nil {
				return fmt.Errorf("preserve window blocker: %w", err)
			}
		}
		return reconcileSourceWindow(ctx, tx, w)
	})
}

func sourceObservation(decoded api.DecodedObservation) (model.Observation, error) {
	if err := decoded.Validate(); err != nil {
		return model.Observation{}, fmt.Errorf("decoded observation: %w", err)
	}
	metadata, err := sourceDigest(decoded.Fields)
	if err != nil {
		return model.Observation{}, err
	}
	tree, err := sourceDigest(decoded.SourceTree)
	if err != nil {
		return model.Observation{}, err
	}
	relations, err := sourceDigest(decoded.Relations)
	if err != nil {
		return model.Observation{}, err
	}
	digest, err := sourceDigest(decoded)
	if err != nil {
		return model.Observation{}, err
	}
	return model.Observation{SourceID: decoded.SourceID, ExternalID: decoded.ExternalID, VersionKey: decoded.VersionKey, ObservationHash: model.Digest(digest), MetadataHash: model.Digest(metadata), SourceTreeHash: model.Digest(tree), RelationsHash: model.Digest(relations), Capture: decoded.Capture, Ordinal: decoded.Ordinal}, nil
}

func registerSourceLaunches(ctx context.Context, tx pgx.Tx, q intakedb.Querier, work run.IntakeWork, w intakedb.IngestSourceWindow, a DiscoveryAcceptance) error {
	launches := make(map[string]SourceLaunch, len(a.Launches))
	for _, launch := range a.Launches {
		if _, exists := launches[launch.RequestKey]; exists {
			return fmt.Errorf("duplicate child launch: %w", run.ErrPayloadConflict)
		}
		launches[launch.RequestKey] = launch
	}
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
		if _, err := insertDelivery(ctx, q, intakedb.InsertDeliveryRecordParams{DeduplicationKey: follow.RequestKey, DeliveryKind: "launch", RecipientWorkflowID: child.WorkflowID, ReceiptKind: "execution", ReceiptKey: executionKey, ExpectedEpoch: child.AdmissionEpoch, ExpectedHash: string(launch.Work.InputDigest), DueAt: launch.DueAt}); err != nil {
			return err
		}
	}
	if len(launches) != 0 {
		return fmt.Errorf("child launch is absent from decoded follow-ups: %w", run.ErrPayloadConflict)
	}
	return nil
}

// RAW QUERY: ReadSourceWindowAccounting. Read accepted receipt payloads in
// ledger order after acceptance. Stream rows; do not collect the whole window.
// Findings are unresolved until a future reviewed gap-resolution contract.
func reconcileSourceWindow(ctx context.Context, tx pgx.Tx, w intakedb.IngestSourceWindow) error {
	rows, err := tx.Query(ctx, `SELECT p.state, p.ordinal, p.request_plan, r.payload->'SourcePayload',
		NOT EXISTS (SELECT 1 FROM jsonb_array_elements(
			CASE WHEN jsonb_typeof(r.payload->'SourcePayload'->'Page'->'FollowUps') = 'array'
			THEN r.payload->'SourcePayload'->'Page'->'FollowUps' ELSE '[]'::jsonb END) AS f(value)
			WHERE NOT CASE WHEN f.value->>'Purpose' IN ('listing','number') THEN EXISTS (
				SELECT 1 FROM ingest.discovery_page other WHERE other.source_window_id = p.source_window_id
				AND other.state = 'accepted' AND other.ordinal > p.ordinal
				AND other.request_plan->>'RequestKey' = f.value->>'RequestKey'
				AND other.request_plan->>'ParentCapture' = f.value->>'ParentCapture'
				AND other.request_plan->>'URL' = f.value->>'URL' AND other.purpose = f.value->>'Purpose')
			WHEN f.value->>'Purpose' = 'detail' THEN EXISTS (SELECT 1 FROM ingest.delivery_record d
				WHERE d.deduplication_key = f.value->>'RequestKey' AND d.delivery_kind = 'launch'
				AND d.acknowledged_at IS NOT NULL) ELSE false END)
		FROM ingest.discovery_page p LEFT JOIN ingest.execution_result r ON r.id = p.acceptance_result_id
		WHERE p.source_window_id = $1 ORDER BY p.ordinal LIMIT $2`, w.ID, w.PageBudget)
	if err != nil {
		return fmt.Errorf("read window accounting: %w", err)
	}
	allAccepted, exhausted, blockers, count := true, false, false, 0
	hasFindings := false
	next := w.InitialCursor
	continuationCapture := ""
	for rows.Next() {
		var state string
		var ordinal int32
		var raw, planJSON json.RawMessage
		var followUpsResolved bool
		if err := rows.Scan(&state, &ordinal, &planJSON, &raw, &followUpsResolved); err != nil {
			rows.Close()
			return fmt.Errorf("read page accounting: %w", err)
		}
		count++
		if state != string(model.DiscoveryPageAccepted) {
			allAccepted, exhausted = false, false
			continue
		}
		var accepted DiscoveryAcceptance
		if err := json.Unmarshal(raw, &accepted); err != nil {
			rows.Close()
			return fmt.Errorf("decode accepted page accounting: %w", err)
		}
		if accepted.Ordinal != ordinal {
			rows.Close()
			return fmt.Errorf("accepted page ordinal differs from ledger")
		}
		var plan api.RequestPlan
		if err := json.Unmarshal(planJSON, &plan); err != nil {
			rows.Close()
			return fmt.Errorf("decode accepted request plan: %w", err)
		}
		if ordinal != int32(count-1) || count == 1 && plan.Cursor != w.InitialCursor || continuationCapture != "" && plan.ParentCapture != continuationCapture {
			blockers = true
		}
		continuationCapture = ""
		exhausted, next = accepted.Page.Cursor.Complete, accepted.Page.Cursor.NextCursor
		hasFindings = hasFindings || len(accepted.Page.Findings) != 0
		blockers = blockers || hasFindings || !followUpsResolved
		// A continuation on the final page cannot prove exhaustion. Earlier
		// continuations are resolved only by an accepted later page below.
		if accepted.Page.Continuation != nil {
			exhausted = false
			continuationCapture = accepted.Page.Capture.CaptureKey
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		return fmt.Errorf("scan source accounting: %w", rowErr)
	}
	if count > 0 && allAccepted && exhausted && !blockers {
		// RAW QUERY: CompleteSourceWindow. The caller holds the window lock.
		_, err = tx.Exec(ctx, `UPDATE ingest.source_window SET state = 'complete', reason = NULL,
			committed_cursor = $2 WHERE id = $1 AND state IN ('frozen','progressing','incomplete')`, w.ID, next)
		if err != nil {
			return fmt.Errorf("complete source window: %w", err)
		}
		return nil
	}
	// RAW QUERY: ReadSourceWindowClosureEligibility. A budget/deadline
	// blocker never fabricates a failed page. Completion took precedence above.
	var now time.Time
	var requests int64
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp(), (SELECT count(*) FROM ingest.artifact_request WHERE source_window_id = $1)`, w.ID).Scan(&now, &requests); err != nil {
		return fmt.Errorf("read window closure eligibility: %w", err)
	}
	reason := ""
	switch {
	case w.State == string(model.SourceWindowIncomplete) && w.Reason != nil:
		reason = *w.Reason
	case hasFindings:
		reason = "coverage_finding"
	case !now.Before(w.DeadlineAt):
		reason = "window_deadline"
	case allAccepted && !exhausted && count >= int(w.PageBudget), requests >= int64(w.RequestBudget) && (!allAccepted || !exhausted):
		reason = "window_budget"
	}
	if reason != "" {
		// RAW QUERY: CloseSourceWindow. Preserve all nonterminal page states.
		if _, err := tx.Exec(ctx, `UPDATE ingest.source_window SET state = 'incomplete', reason = $2
			WHERE id = $1 AND state IN ('frozen','progressing','incomplete')`, w.ID, reason); err != nil {
			return fmt.Errorf("record incomplete window: %w", err)
		}
		return closeDiscoveryClaims(ctx, tx, w)
	}
	return nil
}

// ReconcileWindow completes launch accounting after asynchronous acknowledgements
// without accepting a page twice or spending any frozen fetch allowance.
func (s *Sources) ReconcileWindow(ctx context.Context, work run.IntakeWork) (intakedb.IngestSourceWindow, error) {
	_, err := s.sourceMutation(ctx, work, run.SubjectSourceWindow, "reconcile_window", work.Execution.Scope.Subject.LeaseKey(), func(tx pgx.Tx, q intakedb.Querier, _ intakedb.IngestExecutionResult) error {
		w, err := s.lockWindow(ctx, tx, q, work, work.Execution.Scope.Subject.LeaseKey())
		if err != nil {
			return err
		}
		if w.State == string(model.SourceWindowComplete) || w.State == string(model.SourceWindowFailed) {
			return nil
		}
		return reconcileSourceWindow(ctx, tx, w)
	})
	if err != nil {
		return intakedb.IngestSourceWindow{}, err
	}
	return s.ReadWindow(ctx, work)
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

// SourceWindowClosure records a budget/coverage blocker or intrinsic page failure.
// PageKey is required for intrinsic failure and absent for incompleteness.
type SourceWindowClosure struct {
	// PageKey selects intrinsic failure; empty means coverage incompleteness.
	PageKey string
	// Reason is a non-empty machine code, never source text.
	Reason string
}

// CloseWindow preserves pending pages on incompleteness. Intrinsic failure
// closes only nonterminal siblings with window_closed, retaining prior receipts.
// Both closure kinds consume active discovery claims and release their slots
// atomically, using durable markers to prevent replay from releasing twice.
func (s *Sources) CloseWindow(ctx context.Context, work run.IntakeWork, closure SourceWindowClosure) (run.IntakeReceipt, error) {
	if err := model.ValidateKey(closure.Reason); err != nil {
		return run.IntakeReceipt{}, fmt.Errorf("closure reason: %w", err)
	}
	return s.sourceMutation(ctx, work, run.SubjectSourceWindow, "close_window", closure, func(tx pgx.Tx, q intakedb.Querier, _ intakedb.IngestExecutionResult) error {
		w, err := s.lockWindow(ctx, tx, q, work, work.Execution.Scope.Subject.LeaseKey())
		if err != nil {
			return err
		}
		if w.State == string(model.SourceWindowComplete) || w.State == string(model.SourceWindowFailed) {
			return fmt.Errorf("terminal window closure: %w", run.ErrStaleGrant)
		}
		if err := closeDiscoveryClaims(ctx, tx, w); err != nil {
			return err
		}
		state := string(model.SourceWindowIncomplete)
		if closure.PageKey != "" {
			p, err := readSourcePage(ctx, tx, w.WindowKey, closure.PageKey)
			if err != nil {
				return err
			}
			if p.State == string(model.DiscoveryPageAccepted) || p.State == string(model.DiscoveryPageFailed) {
				return fmt.Errorf("terminal page closure: %w", run.ErrStaleGrant)
			}
			state = string(model.SourceWindowFailed)
			// RAW QUERY: FailDiscoveryPage.
			if _, err := tx.Exec(ctx, `UPDATE ingest.discovery_page SET state = 'failed', reason = $2 WHERE id = $1`, p.ID, closure.Reason); err != nil {
				return fmt.Errorf("fail discovery page: %w", err)
			}
			// RAW QUERY: CloseSiblingDiscoveryPages.
			if _, err := tx.Exec(ctx, `UPDATE ingest.discovery_page SET state = 'failed', reason = 'window_closed'
				WHERE source_window_id = $1 AND id <> $2 AND state IN ('requested','captured','decoded')`, w.ID, p.ID); err != nil {
				return fmt.Errorf("close sibling pages: %w", err)
			}
		}
		// RAW QUERY: CloseSourceWindow.
		if _, err := tx.Exec(ctx, `UPDATE ingest.source_window SET state = $2, reason = $3 WHERE id = $1`, w.ID, state, closure.Reason); err != nil {
			return fmt.Errorf("close source window: %w", err)
		}
		return closeDiscoveryClaims(ctx, tx, w)
	})
}

// insertSourceRecords orders the first Bronze table before any blob insert.
func insertSourceRecords(ctx context.Context, q intakedb.Querier, observations []api.DecodedObservation) error {
	ordered := append([]api.DecodedObservation(nil), observations...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].SourceID != ordered[j].SourceID {
			return ordered[i].SourceID < ordered[j].SourceID
		}
		return ordered[i].ExternalID < ordered[j].ExternalID
	})
	for _, o := range ordered {
		_, err := q.InsertSourceRecord(ctx, intakedb.InsertSourceRecordParams{SourceID: o.SourceID, ExternalID: o.ExternalID})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("insert source record: %w", err)
		}
	}
	return nil
}

// protectCaptureExecution runs after Bronze catalog/link inserts. Both blob
// parents were locked before the first referencing insert.
func protectCaptureExecution(ctx context.Context, tx pgx.Tx, q intakedb.Querier, capture model.CaptureRef, executionKey string) error {
	refs := []model.AssetRef{capture.Asset, capture.TransportReceipt}
	sort.Slice(refs, func(i, j int) bool { return refs[i].SHA256 < refs[j].SHA256 })
	for _, ref := range refs {
		b, err := q.ReadSourceBlob(ctx, string(ref.SHA256))
		if err != nil {
			return err
		}
		if err := addSourceRetention(ctx, tx, q, b.ID, model.RetentionOwnerExecution, executionKey); err != nil {
			return err
		}
	}
	return nil
}

func (s *Sources) registerCaptureBlob(ctx context.Context, q intakedb.Querier, capture model.CaptureRef, at time.Time) (int64, error) {
	refs := []model.AssetRef{capture.Asset, capture.TransportReceipt}
	sort.Slice(refs, func(i, j int) bool { return refs[i].SHA256 < refs[j].SHA256 })
	var captureID int64
	for _, ref := range refs {
		mediaType := capture.MediaType
		if ref == capture.TransportReceipt {
			mediaType = "application/json"
		}
		_, err := q.RegisterSourceBlob(ctx, intakedb.RegisterSourceBlobParams{Sha256: string(ref.SHA256), ByteSize: ref.ByteSize, MediaType: mediaType, ObjectRef: ref.Path, CaptureSource: capture.SourceID, CaptureRef: capture.CaptureKey, CapturedAt: at})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("register capture blob: %w", err)
		}
		// A new statement resolves a concurrent hash winner invisible to INSERT.
		b, err := q.ReadSourceBlob(ctx, string(ref.SHA256))
		if err != nil {
			return 0, grantError("resolve capture blob", err)
		}
		if b.ByteSize != ref.ByteSize {
			return 0, fmt.Errorf("blob hash payload: %w", run.ErrPayloadConflict)
		}
		if ref.SHA256 == capture.Asset.SHA256 {
			captureID = b.ID
		}
	}
	return captureID, nil
}

func registerObservation(ctx context.Context, tx pgx.Tx, q intakedb.Querier, o model.Observation, window string, blobID int64, executionKey string, result intakedb.IngestExecutionResult, observedAt time.Time) (intakedb.BronzeObservationRevision, error) {
	var zero intakedb.BronzeObservationRevision
	if err := o.Validate(); err != nil {
		return zero, fmt.Errorf("observation: %w", err)
	}
	if err := model.ValidateKey(o.VersionKey); err != nil {
		return zero, fmt.Errorf("observation version: %w", err)
	}
	// RAW QUERY: ReadSourceRecord. Fresh lookup after insert conflict waits.
	var recordID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM bronze.source_record WHERE source_id = $1 AND external_id = $2`, o.SourceID, o.ExternalID).Scan(&recordID); err != nil {
		return zero, grantError("resolve source record", err)
	}
	key, err := newLedgerKey("observation")
	if err != nil {
		return zero, err
	}
	if err := model.ValidateEnvelopeKey(key, "observation"); err != nil {
		return zero, fmt.Errorf("issued observation key: %w", err)
	}
	p := intakedb.InsertObservationRevisionParams{ObservationKey: key, SourceRecordID: recordID, ObservationDigest: string(o.ObservationHash), MetadataDigest: string(o.MetadataHash), SourceTreeDigest: string(o.SourceTreeHash), RelationsDigest: string(o.RelationsHash), SourceWindowKey: window, DocumentVersionKey: o.VersionKey, ExecutionKey: executionKey, ResultKey: result.ResultKey, CaptureBlobID: blobID, ObservedAt: observedAt}
	_, err = q.InsertObservationRevision(ctx, p)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return zero, fmt.Errorf("insert observation revision: %w", err)
	}
	stored, err := q.ReadObservationRevision(ctx, intakedb.ReadObservationRevisionParams{SourceRecordID: recordID, ObservationDigest: p.ObservationDigest})
	if err != nil {
		return zero, grantError("resolve observation revision", err)
	}
	if stored.MetadataDigest != p.MetadataDigest || stored.SourceTreeDigest != p.SourceTreeDigest || stored.RelationsDigest != p.RelationsDigest || stored.CaptureBlobID != p.CaptureBlobID || stored.DocumentVersionKey != p.DocumentVersionKey {
		return zero, fmt.Errorf("observation payload: %w", run.ErrPayloadConflict)
	}
	if err := validateObservationProvenance(ctx, tx, stored); err != nil {
		return zero, err
	}
	// Reuse retains original window/execution/result provenance. Current apply
	// adds its own execution edge rather than rewriting immutable provenance.
	if err := model.ValidateEnvelopeKey(stored.ObservationKey, "observation"); err != nil {
		return zero, fmt.Errorf("stored observation key: %w", err)
	}

	return stored, nil
}

// SourceHostPacing freezes a shared host capacity and minimum interval.
type SourceHostPacing struct {
	// HostKey supplies a URL authority; persistence canonicalizes its hostname.
	HostKey string
	// NextAllowedAt initializes the clock only when the host is first inserted.
	NextAllowedAt time.Time
	// MaxInFlight is the immutable shared host capacity.
	MaxInFlight int32
	// MinInterval is the immutable minimum interval between claims.
	MinInterval time.Duration
}

// RegisterHost registers immutable pacing policy under a live source-window
// grant. Existing runtime clocks and in-flight counts are never reset.
func (s *Sources) RegisterHost(ctx context.Context, work run.IntakeWork, host SourceHostPacing) (run.IntakeReceipt, error) {
	if host.HostKey == "" || host.NextAllowedAt.IsZero() || host.MaxInFlight <= 0 || host.MinInterval < 0 {
		return run.IntakeReceipt{}, fmt.Errorf("invalid host pacing")
	}
	key, err := sourceHostKey(host.HostKey)
	if err != nil {
		return run.IntakeReceipt{}, err
	}
	return s.sourceMutation(ctx, work, run.SubjectSourceWindow, "register_host", host, func(tx pgx.Tx, q intakedb.Querier, _ intakedb.IngestExecutionResult) error {
		p := intakedb.RegisterSourceRateLimitParams{HostKey: key, NextAllowedAt: host.NextAllowedAt, MaxInFlight: host.MaxInFlight}
		p.MinInterval.Microseconds, p.MinInterval.Valid = host.MinInterval.Microseconds(), true
		_, err := q.RegisterSourceRateLimit(ctx, p)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("register host pacing: %w", err)
		}
		// RAW QUERY: ReadSourceHostPolicy. Compare policy, not mutable clocks.
		var capacity int32
		var microseconds int64
		if err := tx.QueryRow(ctx, `SELECT max_in_flight, (EXTRACT(EPOCH FROM min_interval) * 1000000)::bigint
			FROM ingest.source_rate_limit WHERE host_key = $1`, key).Scan(&capacity, &microseconds); err != nil {
			return grantError("read host pacing", err)
		}
		if capacity != host.MaxInFlight || microseconds != host.MinInterval.Microseconds() {
			return fmt.Errorf("host pacing payload: %w", run.ErrPayloadConflict)
		}
		return nil
	})
}

func sourceHostKey(authority string) (string, error) {
	u, err := url.Parse("https://" + authority)
	if err != nil || u.Host != authority || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("host pacing requires a URL host authority")
	}
	// Pacing is shared by hostname across schemes and ports. Case, DNS's
	// absolute-name trailing dot and alternate IP spellings cannot add slots.
	key := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if ip := net.ParseIP(key); ip != nil {
		key = ip.String()
	}
	if key == "" {
		return "", fmt.Errorf("host pacing requires a hostname")
	}
	return key, nil
}

// SourceRequestPlan binds a decoded inventory entry to a screened request and
// existing opaque parent observation. HostKey supplies the URL's host authority;
// pacing is shared by canonical hostname across schemes and ports.
type SourceRequestPlan struct {
	// AccountingRequestKey explicitly reuses unchanged source work from an
	// earlier parent observation. Plan.RequestKey must retain that canonical
	// key. No new row, attempts, deadline or eligibility is allocated.
	AccountingRequestKey string
	// Plan retains the screened request and originating frozen budget.
	Plan api.RequestPlan
	// ParentObservation is ledger-issued; ApplyCapture fills an empty value.
	ParentObservation string
	// Role is required exactly for artifact requests.
	Role model.ArtifactRole
	// Required retains the decoded inventory's coverage requirement.
	Required bool
	// HostKey must resolve to the same hostname as the screened URL authority.
	HostKey string
	// RetryStartedAt anchors the inherited frozen retry duration.
	RetryStartedAt time.Time
	// ArtifactKey selects the exact inventory entry; empty for detail requests.
	ArtifactKey string
}

type frozenSourceRequest struct {
	api.RequestPlan
	SourceArtifact *model.ArtifactInventoryEntry
}

func validateDecodedInventory(work run.IntakeWork, inventory api.DecodedDetailResult) error {
	if inventory.Limits.MaxItems > work.Budget.MaxItems || inventory.Limits.MaxBytes > work.Budget.MaxInputBytes {
		return fmt.Errorf("decoded inventory exceeds work budget")
	}
	if err := inventory.Validate(); err != nil {
		return fmt.Errorf("decoded inventory: %w", err)
	}
	for _, artifact := range inventory.Artifacts {
		if !model.ArtifactRole(artifact.Role).Valid() || !model.ArtifactFormatValid(artifact.Format) || artifact.Ordinal > int(^uint32(0)>>1) {
			return fmt.Errorf("inventory artifact %s has invalid role, format or ordinal", artifact.ArtifactKey)
		}
	}
	return nil
}

type sourceRequestInventoryPayload struct {
	Inventory api.DecodedDetailResult
	Requests  []SourceRequestPlan
}

// RegisterRequests freezes detail and artifact requests from decoded inventory.
// ApplyCapture uses the same helper inside its Bronze acceptance transaction.
func (s *Sources) RegisterRequests(ctx context.Context, work run.IntakeWork, inventory api.DecodedDetailResult, requests []SourceRequestPlan) (run.IntakeReceipt, error) {
	if inventory.Limits.MaxItems > work.Budget.MaxItems || inventory.Limits.MaxBytes > work.Budget.MaxInputBytes || len(requests) > work.Budget.MaxItems {
		return run.IntakeReceipt{}, fmt.Errorf("request inventory exceeds work budget")
	}
	if err := validateDecodedInventory(work, inventory); err != nil {
		return run.IntakeReceipt{}, err
	}
	for _, request := range requests {
		if err := request.Plan.Validate(); err != nil {
			return run.IntakeReceipt{}, fmt.Errorf("inventory request plan: %w", err)
		}
	}
	payload := sourceRequestInventoryPayload{inventory, requests}
	return s.sourceMutation(ctx, work, run.SubjectDocumentVersion, "register_requests", payload, func(tx pgx.Tx, q intakedb.Querier, _ intakedb.IngestExecutionResult) error {
		return s.registerRequests(ctx, tx, q, work, inventory, requests)
	})
}

func (s *Sources) registerRequests(ctx context.Context, tx pgx.Tx, q intakedb.Querier, work run.IntakeWork, inventory api.DecodedDetailResult, requests []SourceRequestPlan) error {
	if err := validateDecodedInventory(work, inventory); err != nil {
		return err
	}
	if len(requests) > work.Budget.MaxItems {
		return fmt.Errorf("request inventory exceeds frozen item budget")
	}
	requests = append([]SourceRequestPlan(nil), requests...)
	for _, request := range requests {
		if err := request.Plan.Validate(); err != nil {
			return fmt.Errorf("inventory request plan: %w", err)
		}
		if request.Plan.WindowKey != requests[0].Plan.WindowKey {
			return fmt.Errorf("one request inventory must retain one source window")
		}
	}
	sort.Slice(requests, func(i, j int) bool { return requests[i].Plan.RequestKey < requests[j].Plan.RequestKey })
	for i := 1; i < len(requests); i++ {
		if requests[i-1].Plan.RequestKey == requests[i].Plan.RequestKey {
			return fmt.Errorf("duplicate source request key: %w", run.ErrPayloadConflict)
		}
	}
	type registration struct {
		params   intakedb.RegisterArtifactRequestParams
		windowID int64
	}
	pending := make([]registration, 0, len(requests))
	identities := make(map[string]string, len(requests))
	for _, request := range requests {
		if request.Plan.WindowKey != requests[0].Plan.WindowKey {
			return fmt.Errorf("one request inventory must retain one source window")
		}
		w, err := s.lockWindow(ctx, tx, q, work, request.Plan.WindowKey)
		if err != nil {
			return err
		}
		if err := validateSourcePlan(work, w, request.Plan); err != nil {
			return err
		}
		if request.RetryStartedAt.IsZero() || inventory.Observation.VersionKey != work.Execution.Scope.Subject.LeaseKey() || inventory.Observation.SourceID != w.SourceID {
			return fmt.Errorf("request inventory differs from document subject")
		}
		if err := model.ValidateEnvelopeKey(request.Plan.RequestKey, "request"); err != nil {
			return fmt.Errorf("request key: %w", err)
		}
		parent, err := resolveSourceObservation(ctx, tx, request.ParentObservation, w.WindowKey, work.Execution.Scope.Subject.LeaseKey(), w.SourceID)
		if err != nil {
			return err
		}
		observed, err := sourceObservation(inventory.Observation)
		if err != nil {
			return err
		}
		if parent.ObservationDigest != string(observed.ObservationHash) {
			return fmt.Errorf("inventory differs from accepted parent observation")
		}
		locator, err := url.Parse(request.Plan.URL)
		if err != nil {
			return fmt.Errorf("invalid screened request locator")
		}
		hostKey, err := sourceHostKey(request.HostKey)
		if err != nil {
			return err
		}
		locatorHost, err := sourceHostKey(locator.Host)
		if err != nil || hostKey != locatorHost {
			return fmt.Errorf("request host differs from screened URL")
		}
		matched := false
		var role *string
		var frozenArtifact *model.ArtifactInventoryEntry
		switch request.Plan.Purpose {
		case model.PurposeArtifact:
			if !request.Role.Valid() {
				return fmt.Errorf("invalid requested artifact role")
			}
			value := string(request.Role)
			role = &value
			for _, artifact := range inventory.Artifacts {
				if artifact.ArtifactKey == request.ArtifactKey && artifact.URL == request.Plan.URL && artifact.Role == value && artifact.Required == request.Required {
					matched = true
					frozen := artifact
					frozenArtifact = &frozen
				}
			}
		case model.PurposeDetail:
			if request.Role != "" {
				return fmt.Errorf("detail request cannot declare an artifact role")
			}
			for _, follow := range inventory.FollowUps {
				if follow.RequestKey == request.Plan.RequestKey && follow.URL == request.Plan.URL && follow.Purpose == model.PurposeDetail {
					matched = true
				}
			}
		default:
			return fmt.Errorf("document request purpose must be detail or artifact")
		}
		if !matched || request.Plan.ParentCapture != inventory.Observation.Capture.CaptureKey {
			return fmt.Errorf("request absent from decoded inventory or parent capture")
		}
		identity := "detail:" + request.Plan.URL + ":" + request.Plan.ExpectedResponseKind
		if frozenArtifact != nil {
			identity = "artifact:" + frozenArtifact.ArtifactKey
			locatorIdentity := fmt.Sprintf("artifact-locator:%s:%s:%d", frozenArtifact.URL, frozenArtifact.Role, frozenArtifact.Ordinal)
			if previous, exists := identities[locatorIdentity]; exists && previous != request.Plan.RequestKey {
				return fmt.Errorf("batch repeats source accounting identity: %w", run.ErrPayloadConflict)
			}
			identities[locatorIdentity] = request.Plan.RequestKey
		}
		if previous, exists := identities[identity]; exists && previous != request.Plan.RequestKey {
			return fmt.Errorf("batch repeats source accounting identity: %w", run.ErrPayloadConflict)
		}
		identities[identity] = request.Plan.RequestKey
		// RAW QUERY: FindSourceRequestIdentity. A fresh opaque key cannot reset
		// source eligibility for the same accepted inventory. Reuse its row.
		var priorKey string
		var artifactOrdinal *int
		if frozenArtifact != nil {
			artifactOrdinal = &frozenArtifact.Ordinal
		}
		err = tx.QueryRow(ctx, `SELECT r.request_key FROM ingest.artifact_request r
			WHERE r.document_version_key = $1 AND r.purpose = $3
			AND r.owner_key IN (SELECT o.observation_key FROM bronze.observation_revision o
				JOIN bronze.source_record source ON source.id = o.source_record_id WHERE source.source_id = $2)
			AND (($3::text = 'detail' AND r.request_plan->>'URL' = $4 AND r.expected_response_kind = $5)
			OR ($3::text = 'artifact' AND (r.request_plan->'SourceArtifact'->>'ArtifactKey' = $6
			OR (r.request_plan->'SourceArtifact'->>'URL' = $4 AND r.expected_role = $7
			AND (r.request_plan->'SourceArtifact'->>'Ordinal')::integer = $8))))
			ORDER BY r.id LIMIT 1`, work.Execution.Scope.Subject.LeaseKey(), w.SourceID, string(request.Plan.Purpose), request.Plan.URL, request.Plan.ExpectedResponseKind, request.ArtifactKey, role, artifactOrdinal).Scan(&priorKey)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("find frozen request identity: %w", err)
		}
		if request.AccountingRequestKey != "" {
			if err != nil || request.AccountingRequestKey != priorKey || request.Plan.RequestKey != priorKey {
				return fmt.Errorf("accounting link has no unchanged source request: %w", run.ErrPayloadConflict)
			}
			prior, err := q.ReadArtifactRequest(ctx, priorKey)
			if err != nil {
				return grantError("read reused source accounting", err)
			}
			var frozen frozenSourceRequest
			if err := json.Unmarshal(prior.RequestPlan, &frozen); err != nil {
				return fmt.Errorf("decode reused source plan: %w", err)
			}
			if prior.Purpose != string(request.Plan.Purpose) || prior.Required != request.Required || !reflect.DeepEqual(prior.ExpectedRole, role) || prior.ExpectedResponseKind != request.Plan.ExpectedResponseKind || frozen.SourceID != request.Plan.SourceID || frozen.Country != request.Plan.Country || frozen.PolicyHash != request.Plan.PolicyHash || frozen.URL != request.Plan.URL || frozen.Method != request.Plan.Method || frozen.Cursor != request.Plan.Cursor || !reflect.DeepEqual(frozen.SourceArtifact, frozenArtifact) {
				return fmt.Errorf("reused source accounting payload: %w", run.ErrPayloadConflict)
			}
			continue
		}
		if err == nil && priorKey != request.Plan.RequestKey {
			return fmt.Errorf("request key would reset frozen source allowance: %w", run.ErrPayloadConflict)
		}
		// RAW QUERY: ReadSourceHostID. The common helper already locked every
		// referenced host before any request insert can take an FK lock.
		var hostID int64
		if err := tx.QueryRow(ctx, `SELECT id FROM ingest.source_rate_limit WHERE host_key = $1`, hostKey).Scan(&hostID); err != nil {
			return grantError("resolve request host", err)
		}
		encoded, err := sourceJSON(frozenSourceRequest{request.Plan, frozenArtifact})
		if err != nil {
			return err
		}
		e := work.Execution
		pending = append(pending, registration{intakedb.RegisterArtifactRequestParams{AdmissionEpoch: e.AdmissionEpoch, AdmittedWriter: s.ledger.writer, ResourceKind: "document_version", DocumentVersionKey: e.Scope.Subject.LeaseKey(), Column5: e.Owner, Column6: e.AttemptFence, RequestKey: request.Plan.RequestKey, WindowKey: w.WindowKey, SourceRateLimitID: hostID, OwnerKey: request.ParentObservation, Purpose: string(request.Plan.Purpose), ExpectedRole: role, ExpectedResponseKind: request.Plan.ExpectedResponseKind, Required: request.Required, RequestPlan: encoded, RequestCursor: request.Plan.Cursor, RetryStartedAt: request.RetryStartedAt}, w.ID})
	}
	// Validate the entire batch before any request insert. The pending slice
	// retains global request-key order, including across unrelated windows.
	for _, candidate := range pending {
		p := candidate.params
		_, err := q.RegisterArtifactRequest(ctx, p)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return grantError("register artifact request", err)
		}
		stored, err := q.ReadArtifactRequest(ctx, p.RequestKey)
		if err != nil {
			return grantError("resolve request registration", err)
		}
		if stored.SourceWindowID != candidate.windowID || stored.SourceRateLimitID != p.SourceRateLimitID || stored.DocumentVersionKey != p.DocumentVersionKey || stored.OwnerKey != p.OwnerKey || stored.Purpose != p.Purpose || !reflect.DeepEqual(stored.ExpectedRole, p.ExpectedRole) || stored.ExpectedResponseKind != p.ExpectedResponseKind || stored.Required != p.Required || !sameSourceJSON(stored.RequestPlan, p.RequestPlan) || stored.RequestCursor != p.RequestCursor || !sameSourceTime(stored.RetryStartedAt, p.RetryStartedAt) {
			return fmt.Errorf("request registration payload: %w", run.ErrPayloadConflict)
		}
	}
	return nil
}

// RAW QUERY: ResolveSourceObservation. Validate all cross-schema owner keys
// against their real source, window and version, not only their key grammar.
func resolveSourceObservation(ctx context.Context, tx pgx.Tx, key, window, version, source string) (intakedb.BronzeObservationRevision, error) {
	var o intakedb.BronzeObservationRevision
	if err := model.ValidateEnvelopeKey(key, "observation"); err != nil {
		return o, fmt.Errorf("parent observation key: %w", err)
	}
	err := tx.QueryRow(ctx, `SELECT o.id, o.observation_key, o.source_record_id, o.observation_digest,
		o.metadata_digest, o.source_tree_digest, o.relations_digest, o.source_window_key,
		o.document_version_key, o.execution_key, o.result_key, o.capture_blob_id, o.observed_at
		FROM bronze.observation_revision o JOIN bronze.source_record s ON s.id = o.source_record_id
		JOIN ingest.execution e ON e.execution_key = o.execution_key
		JOIN ingest.execution_result r ON r.result_key = o.result_key AND r.execution_id = e.id
		WHERE o.observation_key = $1 AND o.source_window_key = $2
		AND o.document_version_key = $3 AND s.source_id = $4
		AND ((e.resource_kind = 'source_window' AND e.resource_key = o.source_window_key AND r.payload->>'SourceOperation' = 'accept_page')
		OR (e.resource_kind = 'document_version' AND e.resource_key = o.document_version_key AND r.payload->>'SourceOperation' = 'accept_capture'))`, key, window, version, source).Scan(&o.ID, &o.ObservationKey, &o.SourceRecordID, &o.ObservationDigest, &o.MetadataDigest, &o.SourceTreeDigest, &o.RelationsDigest, &o.SourceWindowKey, &o.DocumentVersionKey, &o.ExecutionKey, &o.ResultKey, &o.CaptureBlobID, &o.ObservedAt)
	if err != nil {
		return o, grantError("resolve parent observation", err)
	}
	return o, nil
}

// RAW QUERY: ValidateObservationProvenance. Reuse preserves its original
// execution and result, but must still resolve their actual source binding.
func validateObservationProvenance(ctx context.Context, tx pgx.Tx, o intakedb.BronzeObservationRevision) error {
	var id int64
	err := tx.QueryRow(ctx, `SELECT r.id FROM ingest.execution e
		JOIN ingest.execution_result r ON r.execution_id = e.id
		JOIN ingest.source_window w ON w.window_key = $3
		JOIN bronze.source_record s ON s.id = $5 AND s.source_id = w.source_id
		WHERE e.execution_key = $1 AND r.result_key = $2
		AND ((e.resource_kind = 'source_window' AND e.resource_key = $3 AND r.payload->>'SourceOperation' = 'accept_page')
		OR (e.resource_kind = 'document_version' AND e.resource_key = $4 AND r.payload->>'SourceOperation' = 'accept_capture'))`, o.ExecutionKey, o.ResultKey, o.SourceWindowKey, o.DocumentVersionKey, o.SourceRecordID).Scan(&id)
	if err != nil {
		return grantError("validate observation provenance", err)
	}
	return nil
}

func readOwnedRequest(ctx context.Context, tx pgx.Tx, q intakedb.Querier, work run.IntakeWork, key string) (intakedb.IngestArtifactRequest, error) {
	// RAW QUERY: LockOwnedArtifactRequest. The common helper already locked
	// the window and hosts. Recheck document ownership after those waits.
	var id int64
	if err := tx.QueryRow(ctx, `SELECT id FROM ingest.artifact_request WHERE request_key = $1
		AND document_version_key = $2 FOR UPDATE`, key, work.Execution.Scope.Subject.LeaseKey()).Scan(&id); err != nil {
		return intakedb.IngestArtifactRequest{}, grantError("lock owned request", err)
	}
	r, err := q.ReadArtifactRequest(ctx, key)
	if err != nil {
		return r, grantError("read request", err)
	}
	return r, nil
}

// ReadRequest reads a request under its registered document execution and lease.
func (s *Sources) ReadRequest(ctx context.Context, work run.IntakeWork, key string) (intakedb.IngestArtifactRequest, error) {
	return sourceTransaction(ctx, s, work, run.SubjectDocumentVersion, func(tx pgx.Tx, q intakedb.Querier) (intakedb.IngestArtifactRequest, error) {
		return readOwnedRequest(ctx, tx, q, work, key)
	})
}

// ClaimRequest claims eligible work and its host slot. Empty eligibility returns
// ErrStaleGrant, matching every empty fenced query. Scheduling uses database time.
func (s *Sources) ClaimRequest(ctx context.Context, work run.IntakeWork) (SourceRequestClaim, error) {
	if s.reconciliation {
		return SourceRequestClaim{}, fmt.Errorf("reconciliation cannot issue fetch: %w", run.ErrStaleGrant)
	}
	return sourceTransaction(ctx, s, work, run.SubjectDocumentVersion, func(tx pgx.Tx, q intakedb.Querier) (SourceRequestClaim, error) {
		e := work.Execution
		now, err := lockLease(ctx, tx, e)
		if err != nil {
			return SourceRequestClaim{}, err
		}
		r, err := q.ClaimArtifactRequest(ctx, intakedb.ClaimArtifactRequestParams{ClaimedEpoch: &e.AdmissionEpoch, AdmittedWriter: s.ledger.writer, ResourceKind: "document_version", ResourceKey: e.Scope.Subject.LeaseKey(), Column5: e.Owner, ClaimedFence: &e.AttemptFence, Column7: now})
		if err != nil {
			return SourceRequestClaim{}, grantError("claim source request", err)
		}
		// The generated query takes a scheduling clock. Refresh it after any
		// host lock wait so neither a deadline nor minimum pacing is spent by
		// waiting for an unrelated document's host slot transaction.
		now, err = lockLease(ctx, tx, e)
		if err != nil {
			return SourceRequestClaim{}, err
		}
		claimed, err := q.ReadArtifactRequest(ctx, r.RequestKey)
		if err != nil {
			return SourceRequestClaim{}, grantError("resolve claimed request", err)
		}
		if !now.Before(claimed.RetryDeadlineAt) {
			return SourceRequestClaim{}, fmt.Errorf("request deadline after host wait: %w", run.ErrStaleGrant)
		}
		// RAW QUERY: AdvanceClaimHostClock. The claim already holds the host
		// row lock. This adjusts only its clock, never another in-flight slot.
		if _, err := tx.Exec(ctx, `UPDATE ingest.source_rate_limit h SET
			next_allowed_at = GREATEST(h.next_allowed_at, $2::timestamptz + h.min_interval)
			FROM ingest.artifact_request r WHERE r.id = $1 AND h.id = r.source_rate_limit_id`, r.ID, now); err != nil {
			return SourceRequestClaim{}, fmt.Errorf("advance claimed host clock: %w", err)
		}
		return SourceRequestClaim{r, sourceAttemptKey(r.RequestKey, r.NextAttempt)}, nil
	})
}

// SourceRequestClaim includes the adapter-issued source attempt key. The key
// is stable for a request and source ordinal across infrastructure recovery.
type SourceRequestClaim struct {
	intakedb.ClaimArtifactRequestRow
	// AttemptKey is allocated deterministically before Fetch and reused on loss.
	AttemptKey string
}

func sourceAttemptKey(request string, ordinal int32) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("source-attempt-v1:%s:%d", request, ordinal)))
	return "request-" + hex.EncodeToString(h[:16])
}

// SourceTransportReceipt records one successful capture or one retryable source
// failure. Capture is nil on failure; Next and RetryAfter preserve recorded S
// backoff. Attempt is the claim's durable NextAttempt, never a worker retry count.
type SourceTransportReceipt struct {
	// RequestKey selects the owned request.
	RequestKey string
	// AttemptKey must equal the adapter-issued claim key.
	AttemptKey string
	// Attempt must equal the claim's NextAttempt.
	Attempt int32
	// Capture is nil on source failure and non-nil on successful Fetch.
	Capture *model.CaptureRef
	// Next preserves recorded source backoff.
	Next time.Time
	// RetryAfter preserves the source and shared host not-before clock.
	RetryAfter time.Time
}

// RecordTransport counts one durable source attempt and releases the claimed
// slot once. Identical replay returns the original immutable result receipt.
func (s *Sources) RecordTransport(ctx context.Context, work run.IntakeWork, receipt SourceTransportReceipt) (run.IntakeReceipt, error) {
	if err := model.ValidateEnvelopeKey(receipt.AttemptKey, "request"); err != nil {
		return run.IntakeReceipt{}, fmt.Errorf("transport attempt: %w", err)
	}
	if receipt.Attempt <= 0 || receipt.Next.IsZero() || receipt.RetryAfter.IsZero() {
		return run.IntakeReceipt{}, fmt.Errorf("transport attempt and scheduling clocks are required")
	}
	return s.sourceMutation(ctx, work, run.SubjectDocumentVersion, "transport", receipt, func(tx pgx.Tx, q intakedb.Querier, _ intakedb.IngestExecutionResult) error {
		r, err := readOwnedRequest(ctx, tx, q, work, receipt.RequestKey)
		if err != nil {
			return err
		}
		if receipt.AttemptKey != sourceAttemptKey(r.RequestKey, receipt.Attempt) {
			return fmt.Errorf("transport attempt differs from adapter claim")
		}
		e := work.Execution
		now, err := lockLease(ctx, tx, e)
		if err != nil {
			return err
		}
		p := intakedb.RecordArtifactTransportParams{AdmissionEpoch: e.AdmissionEpoch, AdmittedWriter: s.ledger.writer, ResourceKind: "document_version", ResourceKey: e.Scope.Subject.LeaseKey(), Column5: e.Owner, ClaimedFence: &e.AttemptFence, ID: r.ID, LastAttemptKey: &receipt.AttemptKey, Column9: receipt.Capture != nil, Column14: receipt.Next, Column15: receipt.RetryAfter, Column16: receipt.Attempt, Column17: now}
		if receipt.Capture != nil {
			// RAW QUERY: ReadRequestSourceWindow. No window lock is acquired
			// after the request lock; these source identities are immutable.
			var window string
			if err := tx.QueryRow(ctx, `SELECT window_key FROM ingest.source_window WHERE id = $1`, r.SourceWindowID).Scan(&window); err != nil {
				return grantError("resolve request window", err)
			}
			w, err := q.ReadSourceWindow(ctx, window)
			if err != nil {
				return grantError("read transport window", err)
			}
			if err := validateCapture(work, w, *receipt.Capture); err != nil {
				return err
			}
			digest := string(receipt.Capture.Asset.SHA256)
			p.CaptureKey, p.CaptureDigest, p.CaptureBytes = &receipt.Capture.CaptureKey, &digest, &receipt.Capture.Asset.ByteSize
			p.TransportReceipt, err = sourceJSON(receipt.Capture)
			if err != nil {
				return err
			}
		}
		if _, err := q.RecordArtifactTransport(ctx, p); err != nil {
			return grantError("record source transport", err)
		}
		return nil
	})
}

// RecoverRequest returns abandoned leased work to due under a fresh fence.
// Committed fetched/verified receipts are kept in place without another attempt.
func (s *Sources) RecoverRequest(ctx context.Context, work run.IntakeWork, key string) (run.IntakeReceipt, error) {
	return s.sourceMutation(ctx, work, run.SubjectDocumentVersion, "recover_request", key, func(tx pgx.Tx, q intakedb.Querier, _ intakedb.IngestExecutionResult) error {
		r, err := readOwnedRequest(ctx, tx, q, work, key)
		if err != nil {
			return err
		}
		if r.State == string(model.ArtifactRequestFetched) || r.State == string(model.ArtifactRequestVerified) {
			return nil
		}
		e := work.Execution
		now, err := lockLease(ctx, tx, e)
		if err != nil {
			return err
		}
		if _, err := q.RecoverArtifactRequest(ctx, intakedb.RecoverArtifactRequestParams{AdmissionEpoch: e.AdmissionEpoch, AdmittedWriter: s.ledger.writer, ResourceKind: "document_version", ResourceKey: e.Scope.Subject.LeaseKey(), Column5: e.Owner, ClaimedFence: &e.AttemptFence, ID: r.ID, Column8: now}); err != nil {
			return grantError("recover source request", err)
		}
		return nil
	})
}

// ExpireRequest closes due, leased or fetched work under its current owner.
// It uses the generated closure at the infrastructure retry cap under a fresh
// fence. Every leased exit releases its host slot once and preserves receipts.
func (s *Sources) ExpireRequest(ctx context.Context, work run.IntakeWork, key, reason string) (run.IntakeReceipt, error) {
	if err := model.ValidateKey(reason); err != nil {
		return run.IntakeReceipt{}, fmt.Errorf("request closure reason: %w", err)
	}
	payload := struct{ Key, Reason string }{key, reason}
	return s.sourceMutation(ctx, work, run.SubjectDocumentVersion, "expire_request", payload, func(tx pgx.Tx, q intakedb.Querier, _ intakedb.IngestExecutionResult) error {
		r, err := readOwnedRequest(ctx, tx, q, work, key)
		if err != nil {
			return err
		}
		if r.State == string(model.ArtifactRequestExpired) {
			if r.Reason == nil || *r.Reason != reason {
				return fmt.Errorf("request closure payload: %w", run.ErrPayloadConflict)
			}
			return nil
		}
		e := work.Execution
		if r.State == string(model.ArtifactRequestLeased) && r.InfrastructureAttempts == 3 && r.ClaimedEpoch != nil && r.ClaimedFence != nil && (*r.ClaimedEpoch < e.AdmissionEpoch || *r.ClaimedEpoch == e.AdmissionEpoch && *r.ClaimedFence < e.AttemptFence) {
			if _, err := q.ExpireArtifactRequest(ctx, intakedb.ExpireArtifactRequestParams{AdmissionEpoch: e.AdmissionEpoch, AdmittedWriter: s.ledger.writer, ResourceKind: "document_version", ResourceKey: e.Scope.Subject.LeaseKey(), Column5: e.Owner, ClaimedFence: &e.AttemptFence, ID: r.ID, Reason: &reason}); err != nil {
				return grantError("expire source request", err)
			}
			return nil
		}
		return closeSourceRequest(ctx, tx, r, model.ArtifactRequestExpired, reason)
	})
}

// CloseRequest records a policy/permanent failure or reconciles source
// exhaustion. ExpireRequest owns processing closure. Successful verified
// receipts never regress; fetched receipts cannot become source exhaustion.
func (s *Sources) CloseRequest(ctx context.Context, work run.IntakeWork, key string, state model.ArtifactRequestState, reason string) (run.IntakeReceipt, error) {
	if state != model.ArtifactRequestFailed && state != model.ArtifactRequestExhausted {
		return run.IntakeReceipt{}, fmt.Errorf("request closure must be failed or exhausted")
	}
	if err := model.ValidateKey(reason); err != nil {
		return run.IntakeReceipt{}, fmt.Errorf("request closure reason: %w", err)
	}
	payload := struct {
		Key    string
		State  model.ArtifactRequestState
		Reason string
	}{key, state, reason}
	return s.sourceMutation(ctx, work, run.SubjectDocumentVersion, "close_request", payload, func(tx pgx.Tx, q intakedb.Querier, _ intakedb.IngestExecutionResult) error {
		r, err := readOwnedRequest(ctx, tx, q, work, key)
		if err != nil {
			return err
		}
		if state == model.ArtifactRequestExhausted {
			now, err := lockLease(ctx, tx, work.Execution)
			if err != nil {
				return err
			}
			if r.CaptureKey != nil || r.SourceAttempts < r.SourceAttemptLimit && now.Before(r.RetryDeadlineAt) {
				return fmt.Errorf("request still has source allowance or successful capture")
			}
		}
		return closeSourceRequest(ctx, tx, r, state, reason)
	})
}

func closeSourceRequest(ctx context.Context, tx pgx.Tx, r intakedb.IngestArtifactRequest, state model.ArtifactRequestState, reason string) error {
	if r.State == string(state) {
		if r.Reason == nil || *r.Reason != reason {
			return fmt.Errorf("terminal request payload: %w", run.ErrPayloadConflict)
		}
		return nil
	}
	if r.State != string(model.ArtifactRequestDue) && r.State != string(model.ArtifactRequestLeased) && r.State != string(model.ArtifactRequestFetched) {
		return fmt.Errorf("terminal request transition: %w", run.ErrStaleGrant)
	}
	// RAW QUERY: CloseArtifactRequest. The owning grant and request row are
	// locked. All receipt groups and source counters remain unchanged.
	if _, err := tx.Exec(ctx, `UPDATE ingest.artifact_request SET state = $2, reason = $3,
		next_eligible_at = NULL WHERE id = $1`, r.ID, string(state), reason); err != nil {
		return fmt.Errorf("close source request: %w", err)
	}
	if r.State == string(model.ArtifactRequestLeased) {
		// RAW QUERY: ReleaseClosedRequestHost. The previous state, read under
		// the request lock, authorizes exactly one host-slot release.
		var id int64
		if err := tx.QueryRow(ctx, `UPDATE ingest.source_rate_limit SET in_flight = in_flight - 1
			WHERE id = $1 AND in_flight > 0 RETURNING id`, r.SourceRateLimitID).Scan(&id); err != nil {
			return grantError("release closed request host", err)
		}
	}
	return nil
}

// CaptureAcceptance supplies verified staged capture and purpose-specific
// decoded metadata or artifact identity. Detail may be nil: verified transport
// does not assert successful decoding. Artifact is required only for artifact
// requests; it links to the already accepted parent observation.
type CaptureAcceptance struct {
	// RequestKey selects the previously fetched request.
	RequestKey string
	// Capture must match every field of the immutable transport receipt.
	Capture model.CaptureRef
	// Detail optionally accepts decoded detail metadata and inventory.
	Detail *api.DecodedDetailResult
	// Artifact is required only for an artifact request.
	Artifact *model.ArtifactInventoryEntry
	// CaptureURL preserves the screened final artifact locator.
	CaptureURL string
	// Requests are committed with detail observation acceptance.
	Requests []SourceRequestPlan
}

// ApplyCapture verifies staged bytes, then atomically registers Bronze,
// observation/artifact links, retention and the verified request transition.
// A later decoded detail apply may reuse verified bytes under new immutable
// work. It never rewrites the successful transport receipt or spends an attempt.
func (s *Sources) ApplyCapture(ctx context.Context, work run.IntakeWork, acceptance CaptureAcceptance) (run.IntakeReceipt, error) {
	if err := acceptance.Capture.Validate(); err != nil {
		return run.IntakeReceipt{}, fmt.Errorf("capture acceptance: %w", err)
	}
	if len(acceptance.Requests) > work.Budget.MaxItems {
		return run.IntakeReceipt{}, fmt.Errorf("capture request inventory exceeds work budget")
	}
	if acceptance.Detail != nil {
		if err := validateDecodedInventory(work, *acceptance.Detail); err != nil {
			return run.IntakeReceipt{}, err
		}
	}
	for _, request := range acceptance.Requests {
		if err := request.Plan.Validate(); err != nil {
			return run.IntakeReceipt{}, fmt.Errorf("capture inventory plan: %w", err)
		}
	}
	return s.sourceMutation(ctx, work, run.SubjectDocumentVersion, "accept_capture", acceptance, func(tx pgx.Tx, q intakedb.Querier, result intakedb.IngestExecutionResult) error {
		// Window precedes request. Read its immutable identity first, without
		// taking a request lock; ownership is checked again after window lock.
		initial, err := q.ReadArtifactRequest(ctx, acceptance.RequestKey)
		if err != nil {
			return grantError("read capture request", err)
		}
		var window string
		// RAW QUERY: ReadRequestSourceWindow.
		if err := tx.QueryRow(ctx, `SELECT window_key FROM ingest.source_window WHERE id = $1`, initial.SourceWindowID).Scan(&window); err != nil {
			return grantError("read capture window", err)
		}
		w, err := s.lockWindow(ctx, tx, q, work, window)
		if err != nil {
			return err
		}
		r, err := readOwnedRequest(ctx, tx, q, work, acceptance.RequestKey)
		if err != nil {
			return err
		}
		if err := validateCapture(work, w, acceptance.Capture); err != nil {
			return err
		}
		if !captureMatches(r.CaptureKey, r.CaptureDigest, r.CaptureBytes, r.TransportReceipt, acceptance.Capture) {
			return fmt.Errorf("Bronze capture differs from transport: %w", run.ErrPayloadConflict)
		}
		if r.State != string(model.ArtifactRequestFetched) && r.State != string(model.ArtifactRequestVerified) {
			return fmt.Errorf("capture acceptance transition: %w", run.ErrStaleGrant)
		}
		if acceptance.Detail != nil {
			// RAW QUERY: ReadAcceptedSourceDetail. Verified transport can accept
			// decoded metadata separately, but a second decode cannot rewrite it.
			var prior json.RawMessage
			err := tx.QueryRow(ctx, `SELECT result.payload->'SourcePayload' FROM ingest.execution_result result
				JOIN ingest.execution e ON e.id = result.execution_id
				WHERE e.resource_kind = 'document_version' AND e.resource_key = $1 AND result.id <> $2
				AND result.payload->>'SourceOperation' = 'accept_capture'
				AND result.payload->'SourcePayload'->>'RequestKey' = $3
				AND jsonb_typeof(result.payload->'SourcePayload'->'Detail') = 'object'
				ORDER BY result.id LIMIT 1`, work.Execution.Scope.Subject.LeaseKey(), result.ID, acceptance.RequestKey).Scan(&prior)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("read accepted detail: %w", err)
			}
			if err == nil {
				exact, err := sourceJSON(acceptance)
				if err != nil {
					return err
				}
				if !sameSourceJSON(prior, exact) {
					return fmt.Errorf("accepted detail payload: %w", run.ErrPayloadConflict)
				}
				return nil
			}
		}
		parent, err := resolveSourceObservation(ctx, tx, r.OwnerKey, w.WindowKey, work.Execution.Scope.Subject.LeaseKey(), w.SourceID)
		if err != nil {
			return err
		}
		for _, request := range acceptance.Requests {
			if request.Plan.WindowKey != w.WindowKey {
				return fmt.Errorf("accepted capture inventory changes source window")
			}
		}
		_, executionKey, err := resolveExecution(ctx, tx, work)
		if err != nil {
			return err
		}
		observedAt, err := sourceCaptureTime(ctx, tx, work, "transport", r.RequestKey, acceptance.Capture.CaptureKey)
		if err != nil {
			return err
		}
		if acceptance.Detail != nil {
			if err := insertSourceRecords(ctx, q, []api.DecodedObservation{acceptance.Detail.Observation}); err != nil {
				return err
			}
		}
		blobID, err := s.registerCaptureBlob(ctx, q, acceptance.Capture, observedAt)
		if err != nil {
			return err
		}
		observationKey := ""
		switch model.RequestPurpose(r.Purpose) {
		case model.PurposeDetail:
			if acceptance.Artifact != nil {
				return fmt.Errorf("detail capture cannot invent an artifact")
			}
			if acceptance.Detail != nil {
				if err := acceptance.Detail.Validate(); err != nil {
					return fmt.Errorf("captured detail: %w", err)
				}
				if acceptance.Detail.Observation.Capture != acceptance.Capture || acceptance.Detail.Observation.VersionKey != r.DocumentVersionKey {
					return fmt.Errorf("detail differs from captured document version")
				}
				o, err := sourceObservation(acceptance.Detail.Observation)
				if err != nil {
					return err
				}
				revision, err := registerObservation(ctx, tx, q, o, w.WindowKey, blobID, executionKey, result, observedAt)
				if err != nil {
					return err
				}
				observationKey = revision.ObservationKey
				requests := append([]SourceRequestPlan(nil), acceptance.Requests...)
				for i := range requests {
					if requests[i].ParentObservation != "" && requests[i].ParentObservation != revision.ObservationKey {
						return fmt.Errorf("inventory request must use accepted detail observation key")
					}
					requests[i].ParentObservation = revision.ObservationKey
				}
				if revision.SourceRecordID != parent.SourceRecordID {
					return fmt.Errorf("captured detail changes parent source identity")
				}
				if err := s.registerRequests(ctx, tx, q, work, *acceptance.Detail, requests); err != nil {
					return err
				}
			} else if len(acceptance.Requests) != 0 {
				return fmt.Errorf("request planning requires decoded detail inventory")
			}
		case model.PurposeArtifact:
			if acceptance.Artifact == nil || acceptance.Detail != nil || len(acceptance.Requests) != 0 {
				return fmt.Errorf("artifact capture requires only its artifact inventory")
			}
			a := *acceptance.Artifact
			if err := a.Validate(); err != nil {
				return fmt.Errorf("captured artifact: %w", err)
			}
			if !model.ArtifactRole(a.Role).Valid() || !model.ArtifactFormatValid(a.Format) || r.ExpectedRole == nil || a.Role != *r.ExpectedRole || a.Required != r.Required || a.Ordinal > int(^uint32(0)>>1) || a.Capture == nil || *a.Capture != acceptance.Capture {
				return fmt.Errorf("artifact differs from frozen expected role or capture")
			}
			var plan frozenSourceRequest
			if err := json.Unmarshal(r.RequestPlan, &plan); err != nil {
				return fmt.Errorf("decode artifact plan: %w", err)
			}
			if a.URL != plan.URL {
				return fmt.Errorf("artifact locator differs from frozen request")
			}
			proposed := a
			proposed.Capture = nil
			if plan.SourceArtifact == nil {
				return fmt.Errorf("artifact request has no frozen inventory")
			}
			frozen := *plan.SourceArtifact
			frozen.Capture = nil
			if !reflect.DeepEqual(proposed, frozen) {
				return fmt.Errorf("artifact differs from decoded inventory: %w", run.ErrPayloadConflict)
			}
			if err := model.ValidatePublicURL(acceptance.CaptureURL); err != nil {
				return fmt.Errorf("artifact capture URL: %w", err)
			}
			if err := registerSourceArtifact(ctx, tx, q, parent, blobID, a, acceptance.CaptureURL); err != nil {
				return err
			}
			if err := addSourceRetention(ctx, tx, q, blobID, model.RetentionOwnerObservationRevision, parent.ObservationKey); err != nil {
				return fmt.Errorf("protect artifact observation: %w", err)
			}
			if err := protectObservationTransport(ctx, tx, q, acceptance.Capture, parent.ObservationKey); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown capture request purpose")
		}
		if err := protectCaptureExecution(ctx, tx, q, acceptance.Capture, executionKey); err != nil {
			return err
		}
		if observationKey != "" {
			if err := protectObservationCapture(ctx, tx, q, acceptance.Capture, blobID, observationKey); err != nil {
				return err
			}
		}
		// RAW QUERY: VerifyArtifactRequest. The fetched receipt and host slot
		// were already reconciled by transport. Verification releases no slot.
		if _, err := tx.Exec(ctx, `UPDATE ingest.artifact_request SET state = 'verified' WHERE id = $1 AND state = 'fetched'`, r.ID); err != nil {
			return fmt.Errorf("verify artifact request: %w", err)
		}
		return nil
	})
}

// AcceptDetail accepts decoded detail and inventory against the identical
// fetched or verified transport receipt through ApplyCapture's fenced atomic
// transaction. It never changes transport counters or releases a host slot.
func (s *Sources) AcceptDetail(ctx context.Context, work run.IntakeWork, acceptance CaptureAcceptance) (run.IntakeReceipt, error) {
	if acceptance.Detail == nil || acceptance.Artifact != nil {
		return run.IntakeReceipt{}, fmt.Errorf("decoded detail is required")
	}
	return s.ApplyCapture(ctx, work, acceptance)
}

// RAW QUERY: ReadSourceCaptureTime. Record observation time at the original
// fenced capture receipt, not at a later decode or Bronze acceptance. Historical
// receipt grants are provenance; only the current apply grant authorizes writes.
func sourceCaptureTime(ctx context.Context, tx pgx.Tx, work run.IntakeWork, operation, key, captureKey string) (time.Time, error) {
	kind, resource, err := leaseResource(work.Execution.Scope)
	if err != nil {
		return time.Time{}, err
	}
	var observedAt time.Time
	err = tx.QueryRow(ctx, `SELECT r.created_at FROM ingest.execution_result r
		JOIN ingest.execution e ON e.id = r.execution_id WHERE e.resource_kind = $1 AND e.resource_key = $2
		AND r.payload->>'SourceOperation' = $3
		AND COALESCE(r.payload->'SourcePayload'->>'Key', r.payload->'SourcePayload'->>'RequestKey') = $4
		AND r.payload->'SourcePayload'->'Capture'->>'CaptureKey' = $5
		ORDER BY r.id LIMIT 1`, kind, resource, operation, key, captureKey).Scan(&observedAt)
	if err != nil {
		return observedAt, grantError("resolve source capture time", err)
	}
	return observedAt, nil
}

func addSourceRetention(ctx context.Context, tx pgx.Tx, q intakedb.Querier, blobID int64, kind model.RetentionOwnerKind, key string) error {
	// Callers supply only the execution resolved under the grant or the
	// observation read/inserted in this apply. No public arbitrary owner port.
	_, err := q.AddBlobRetentionReference(ctx, intakedb.AddBlobRetentionReferenceParams{ID: blobID, OwnerKind: string(kind), OwnerKey: key})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("add source retention: %w", err)
	}
	// RAW QUERY: ReadBlobRetentionReference. Fresh lookup after a concurrent
	// edge winner that was invisible while waiting on the catalog row lock.
	var id int64
	if err := tx.QueryRow(ctx, `SELECT id FROM bronze.blob_retention_reference
		WHERE blob_id = $1 AND owner_kind = $2 AND owner_key = $3`, blobID, string(kind), key).Scan(&id); err != nil {
		return grantError("resolve source retention", err)
	}
	return nil
}

func protectObservationCapture(ctx context.Context, tx pgx.Tx, q intakedb.Querier, capture model.CaptureRef, blobID int64, observationKey string) error {
	if err := addSourceRetention(ctx, tx, q, blobID, model.RetentionOwnerObservationRevision, observationKey); err != nil {
		return err
	}
	return protectObservationTransport(ctx, tx, q, capture, observationKey)
}

func protectObservationTransport(ctx context.Context, tx pgx.Tx, q intakedb.Querier, capture model.CaptureRef, observationKey string) error {
	b, err := q.ReadSourceBlob(ctx, string(capture.TransportReceipt.SHA256))
	if err != nil {
		return grantError("resolve transport receipt blob", err)
	}
	if b.ByteSize != capture.TransportReceipt.ByteSize {
		return fmt.Errorf("transport receipt blob payload: %w", run.ErrPayloadConflict)
	}
	return addSourceRetention(ctx, tx, q, b.ID, model.RetentionOwnerObservationRevision, observationKey)
}

// ResumeWindow clears a resolved blocker without resetting frozen inputs.
// A spent request budget or deadline prevents resumption. Page registration
// still enforces its frozen page limit. Acceptance and reconciliation may continue.
func (s *Sources) ResumeWindow(ctx context.Context, work run.IntakeWork) (run.IntakeReceipt, error) {
	denied := false
	receipt, err := s.sourceMutation(ctx, work, run.SubjectSourceWindow, "resume_window", work.Execution.Scope.Subject.LeaseKey(), func(tx pgx.Tx, q intakedb.Querier, _ intakedb.IngestExecutionResult) error {
		w, err := s.lockWindow(ctx, tx, q, work, work.Execution.Scope.Subject.LeaseKey())
		if err != nil {
			return err
		}
		// RAW QUERY: ReadDiscoveryResumptionExhaustion. Replacement eligibility
		// cannot be reset by clearing an incomplete-window blocker.
		var exhausted string
		err = tx.QueryRow(ctx, `SELECT page_key FROM ingest.discovery_page WHERE source_window_id = $1
			AND state = 'requested' AND claim_state = 'released' AND infrastructure_attempts = 3
			ORDER BY page_key COLLATE "C" LIMIT 1`, w.ID).Scan(&exhausted)
		if err == nil {
			denied = true
			return failDiscoveryInfrastructure(ctx, tx, w, exhausted)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// RAW QUERY: ResumeSourceWindow. Counts use a new statement after lock.
		var id int64
		err = tx.QueryRow(ctx, `UPDATE ingest.source_window w SET reason = NULL,
			state = CASE WHEN EXISTS (SELECT 1 FROM ingest.discovery_page p WHERE p.source_window_id = w.id AND p.state = 'accepted') THEN 'progressing' ELSE 'frozen' END
			WHERE w.id = $1 AND w.state = 'incomplete' AND w.deadline_at > clock_timestamp()
			AND (SELECT count(*) FROM ingest.artifact_request r WHERE r.source_window_id = w.id) < w.request_budget
			AND NOT EXISTS (SELECT 1 FROM ingest.discovery_page p WHERE p.source_window_id = w.id AND p.state = 'failed')
			RETURNING w.id`, w.ID).Scan(&id)
		if err != nil {
			return grantError("resume source window", err)
		}
		return nil
	})
	if err != nil {
		return run.IntakeReceipt{}, err
	}
	if denied {
		return receipt, fmt.Errorf("discovery resumption exhausted: %w", run.ErrStaleGrant)
	}
	return receipt, nil
}

func registerSourceArtifact(ctx context.Context, tx pgx.Tx, q intakedb.Querier, parent intakedb.BronzeObservationRevision, blobID int64, a model.ArtifactInventoryEntry, captureURL string) error {
	_, err := q.InsertSourceArtifact(ctx, intakedb.InsertSourceArtifactParams{ArtifactKey: a.ArtifactKey, BlobID: blobID, Role: a.Role, DeclaredFilename: a.DeclaredFilename, Format: a.Format, SourceUrl: a.URL, CaptureUrl: captureURL})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("register source artifact: %w", err)
	}
	// RAW QUERY: ReadSourceArtifact. Fresh snapshot after a key conflict.
	var id, storedBlob int64
	var role, filename, format, sourceURL, storedCaptureURL string
	if err := tx.QueryRow(ctx, `SELECT id, blob_id, role, declared_filename, format, source_url, capture_url
		FROM bronze.artifact WHERE artifact_key = $1`, a.ArtifactKey).Scan(&id, &storedBlob, &role, &filename, &format, &sourceURL, &storedCaptureURL); err != nil {
		return grantError("read source artifact", err)
	}
	if storedBlob != blobID || role != a.Role || filename != a.DeclaredFilename || format != a.Format || sourceURL != a.URL || storedCaptureURL != captureURL {
		return fmt.Errorf("artifact payload: %w", run.ErrPayloadConflict)
	}
	_, err = q.LinkObservationArtifact(ctx, intakedb.LinkObservationArtifactParams{ObservationRevisionID: parent.ID, ID: id, Role: a.Role, Ordinal: int32(a.Ordinal), Required: a.Required})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("link observation artifact: %w", err)
	}
	// RAW QUERY: ReadObservationArtifact. A uniqueness collision must compare
	// the artifact and required flag rather than silently accepting a new link.
	var linkedID int64
	var required bool
	if err := tx.QueryRow(ctx, `SELECT artifact_id, required FROM bronze.observation_artifact
		WHERE observation_revision_id = $1 AND role = $2 AND ordinal = $3`, parent.ID, a.Role, a.Ordinal).Scan(&linkedID, &required); err != nil {
		return grantError("read artifact link", err)
	}
	if linkedID != id || required != a.Required {
		return fmt.Errorf("artifact link payload: %w", run.ErrPayloadConflict)
	}
	return nil
}

// AcceptedObservations resolves ledger-issued observation keys from an owning
// apply receipt, including immutable observation reuse. It reads no document
// bytes and requires a current registered execution for the same resource.
func (s *Sources) AcceptedObservations(ctx context.Context, work run.IntakeWork, resultKey string) ([]intakedb.BronzeObservationRevision, error) {
	if err := model.ValidateEnvelopeKey(resultKey, "result"); err != nil {
		return nil, fmt.Errorf("source result key: %w", err)
	}
	kind := work.Execution.Scope.Subject.Kind
	if kind != run.SubjectSourceWindow && kind != run.SubjectDocumentVersion {
		return nil, fmt.Errorf("source observation scope: %w", run.ErrStaleGrant)
	}
	return sourceTransaction(ctx, s, work, kind, func(tx pgx.Tx, q intakedb.Querier) ([]intakedb.BronzeObservationRevision, error) {
		resourceKind, resourceKey, err := leaseResource(work.Execution.Scope)
		if err != nil {
			return nil, err
		}
		// RAW QUERY: ReadOwnedSourceResult. Bind receipt to the owning resource
		// before resolving any cross-schema observation or request reference.
		var raw json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT r.payload FROM ingest.execution_result r
			JOIN ingest.execution e ON e.id = r.execution_id WHERE r.result_key = $1
			AND e.resource_kind = $2 AND e.resource_key = $3`, resultKey, resourceKind, resourceKey).Scan(&raw); err != nil {
			return nil, grantError("resolve owning source receipt", err)
		}
		var payload sourcePayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, fmt.Errorf("decode source receipt: %w", err)
		}
		var decoded []api.DecodedObservation
		switch payload.SourceOperation {
		case "accept_page":
			var a DiscoveryAcceptance
			if err := json.Unmarshal(payload.SourcePayload, &a); err != nil {
				return nil, fmt.Errorf("decode accepted page: %w", err)
			}
			decoded = a.Page.Observations
		case "accept_capture":
			var a CaptureAcceptance
			if err := json.Unmarshal(payload.SourcePayload, &a); err != nil {
				return nil, fmt.Errorf("decode accepted capture: %w", err)
			}
			if a.Detail != nil {
				decoded = []api.DecodedObservation{a.Detail.Observation}
			}
		default:
			return nil, fmt.Errorf("source receipt does not accept observations")
		}
		if len(decoded) > work.Budget.MaxItems {
			return nil, fmt.Errorf("accepted observations exceed current read budget")
		}
		observations := make([]intakedb.BronzeObservationRevision, 0, len(decoded))
		for _, value := range decoded {
			o, err := sourceObservation(value)
			if err != nil {
				return nil, err
			}
			var recordID int64
			// RAW QUERY: ReadSourceRecord.
			if err := tx.QueryRow(ctx, `SELECT id FROM bronze.source_record WHERE source_id = $1 AND external_id = $2`, o.SourceID, o.ExternalID).Scan(&recordID); err != nil {
				return nil, grantError("resolve accepted source record", err)
			}
			stored, err := q.ReadObservationRevision(ctx, intakedb.ReadObservationRevisionParams{SourceRecordID: recordID, ObservationDigest: string(o.ObservationHash)})
			if err != nil {
				return nil, grantError("read accepted observation", err)
			}
			observations = append(observations, stored)
		}
		return observations, nil
	})
}
