package intake

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"sort"

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
		if err != nil && (ctx.Err() != nil || errors.As(err, &databaseError)) {
			// Cancellation or a server error prevents the final query from
			// checking expiry. Preserve the body cause for the caller.
			return zero, err
		}
		// Expiry takes precedence even over a changed replay after a lock wait.
		if grantErr := lockSourceLease(ctx, tx, work.Execution, reconciliation); grantErr != nil {
			if ctx.Err() != nil {
				return zero, errors.Join(err, grantErr, ctx.Err())
			}
			return zero, grantErr
		}
		return value, err
	})
	if err != nil {
		return zero, databaseError(ctx, fmt.Sprintf("source %s %s", kind, work.Execution.Scope.Subject.LeaseKey()), err)
	}
	return value, nil
}

// prelockSourceAccounting acquires the shared source lock order after admission
// and the owning lease. Candidate reads are bounded by the immutable work budget.
// Window locks freeze page membership; the owning document lease freezes its
// request membership. Generated queries may reacquire these same locks.
func prelockSourceAccounting(ctx context.Context, tx pgx.Tx, work run.IntakeWork, payload any) error {
	windows, hosts, hashes := map[string]bool{}, map[string]bool{}, map[string]bool{}
	// Explicit request targets must belong to the candidate set before any lock.
	targets := map[string]bool{}
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
			if _, exists := targets[request.Plan.RequestKey]; !exists {
				targets[request.Plan.RequestKey] = false
			}
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
		targets[value.RequestKey] = true
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
		targets[value.RequestKey] = true
		if value.Capture != nil {
			addCapture(*value.Capture)
		}
	case string:
		if work.Execution.Scope.Subject.Kind == run.SubjectDocumentVersion {
			targets[value] = true
		}
	case struct{ Key, Reason string }:
		targets[value.Key] = true
	case struct {
		Key    string
		State  model.ArtifactRequestState
		Reason string
	}:
		targets[value.Key] = true
	}
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
	// Resolve every explicit existing target without taking row locks. The
	// document lease freezes membership, so callbacks can only reacquire rows
	// discovered above. New inventory keys may be absent until registration.
	candidates := make(map[string]bool, len(requestKeys))
	for _, key := range requestKeys {
		candidates[key] = true
	}
	for key, required := range targets {
		initial, err := intakedb.New(tx).ReadArtifactRequest(ctx, key)
		if errors.Is(err, pgx.ErrNoRows) && !required {
			continue
		}
		if err != nil {
			return grantError("resolve source request target", err)
		}
		if initial.DocumentVersionKey != work.Execution.Scope.Subject.LeaseKey() || !candidates[key] {
			var cause error = run.ErrStaleGrant
			if !required {
				// Inventory key collisions retain their payload classification.
				cause = run.ErrPayloadConflict
			}
			return fmt.Errorf("source request target ownership: %w", cause)
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
	reconciliation := operation == "acknowledge_launch" || operation == "close_window" || operation == "recover_page" || operation == "expire_request" || operation == "close_request"
	return sourceTransactionMode(ctx, s, work, kind, reconciliation || operation == "page_capture" || operation == "page_retry" || operation == "page_failure", payload, func(tx pgx.Tx, q intakedb.Querier) (run.IntakeReceipt, error) {
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
		var inserted intakedb.InsertExecutionResultRow
		var insertErr error
		if reconciliation {
			// RAW QUERY: InsertSourceReconciliationResult. Admission, owning
			// lease and immutable execution were checked above. Cancellation
			// allows acknowledgement, window/request closure, and claim release. Fresh
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
