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

	// afterResumeCommit is a test seam after an acknowledged resumption commit
	// and before classification. Configure before use; never mutate concurrently.
	afterResumeCommit func()
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
		locks, err := prelockSourceAccounting(ctx, tx, work, payload)
		if err != nil {
			return zero, fmt.Errorf("prelock source accounting: %w", err)
		}
		if err := lockSourceLease(ctx, tx, work.Execution, reconciliation); err != nil {
			return zero, err
		}
		if _, _, err := resolveExecution(ctx, tx, work); err != nil {
			return zero, err
		}
		value, err := fn(sourceLockedTx{Tx: tx, locks: locks}, q)
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

type sourceLockSet struct {
	windows, hosts, requests, blobs map[string]int64
}

type sourceLockedTx struct {
	pgx.Tx
	locks sourceLockSet
}

func sourceLocks(tx pgx.Tx) (sourceLockSet, error) {
	locked, ok := tx.(sourceLockedTx)
	if !ok || locked.locks.windows == nil || locked.locks.hosts == nil {
		return sourceLockSet{}, fmt.Errorf("missing source parent lock set")
	}
	return locked.locks, nil
}

// prelockSourceAccounting acquires the shared source lock order after admission
// and the owning lease. Candidate reads are bounded by the immutable work budget.
// Window locks freeze page membership; the owning document lease freezes its
// request membership. Generated queries may reacquire these same locks.
func prelockSourceAccounting(ctx context.Context, tx pgx.Tx, work run.IntakeWork, payload any) (sourceLockSet, error) {
	locks := sourceLockSet{map[string]int64{}, map[string]int64{}, map[string]int64{}, map[string]int64{}}
	err := func() error {
		windows, hosts, hashes := map[string]bool{}, map[string]bool{}, map[string]bool{}
		requiredHosts := map[string]bool{}
		requiredParents, guard := false, false
		switch payload.(type) {
		case DiscoveryAcceptance, CaptureAcceptance:
			requiredParents, guard = true, true
		case sourceRequestInventoryPayload:
			requiredParents = true
		}
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
			if requiredParents {
				requiredHosts[host] = true
			}
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
			// No row lock before windows.
			rows, err := intakedb.New(tx).SourceLockRequestCandidates(ctx, intakedb.SourceLockRequestCandidatesParams{
				P1: work.Execution.Scope.Subject.LeaseKey(), P2: int64(work.Budget.MaxItems + 1)})
			if err != nil {
				return err
			}
			for _, row := range rows {
				requestKeys = append(requestKeys, row.RequestKey)
				windows[row.WindowKey], hosts[row.HostKey] = true, true
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
		// Fixed byte order across parent windows.
		for _, key := range keys(windows) {
			id, err := intakedb.New(tx).PrelockSourceWindows(ctx, key)
			if errors.Is(err, pgx.ErrNoRows) && requiredParents {
				return grantError("prelock source parent", err)
			}
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil {
				locks.windows[key] = id
			}
		}
		type pageLock struct {
			id                   int64
			key, window, locator string
		}
		var pages []pageLock
		// Window locks freeze membership;
		// discover the bounded set before acquiring page locks in global byte order.
		for _, key := range keys(windows) {
			rows, err := intakedb.New(tx).SourceLockPageCandidates(ctx, intakedb.SourceLockPageCandidatesParams{
				P1: key, P2: int64(work.Budget.MaxPages + 1)})
			if err != nil {
				return err
			}
			count := 0
			for _, row := range rows {
				page := pageLock{id: row.ID, key: row.PageKey, window: key, locator: row.Locator}
				pages = append(pages, page)
				count++
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
		// All windows precede globally sorted pages.
		for _, page := range pages {
			if _, err := intakedb.New(tx).PrelockSourcePages(ctx, page.id); err != nil {
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
		// Precede artifact inserts' FK KEY SHARE.
		for _, key := range keys(hosts) {
			id, err := intakedb.New(tx).PrelockSourceHosts(ctx, key)
			if errors.Is(err, pgx.ErrNoRows) && requiredHosts[key] {
				return grantError("prelock source parent", err)
			}
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil {
				locks.hosts[key] = id
			}
		}
		// Hosts are already held.
		for _, key := range requestKeys {
			id, err := intakedb.New(tx).PrelockSourceRequests(ctx, key)
			if err != nil {
				return err
			}
			locks.requests[key] = id
		}
		if guard {
			if err := intakedb.New(tx).AcquireBlobCatalogGuard(ctx); err != nil {
				return err
			}
			if _, err := lockLease(ctx, tx, work.Execution); err != nil {
				return err
			}
		}
		// Lock before any referencing Bronze insert.
		for _, hash := range keys(hashes) {
			id, err := intakedb.New(tx).PrelockSourceBlobs(ctx, hash)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil {
				locks.blobs[hash] = id
			}
		}
		return nil
	}()
	return locks, err
}

// Cancellation permits closure, release, and replay of retained receipts.
// Fresh outcomes still require the noncancelled result-insertion gate.
func lockSourceLease(ctx context.Context, tx pgx.Tx, e run.ScopedExecution, reconciliation bool) error {
	if !reconciliation {
		_, err := lockLease(ctx, tx, e)
		return err
	}
	// Cancellation does not
	// waive current ownership, admission, fence or expiry checks.
	kind, key, err := leaseResource(e.Scope)
	if err != nil {
		return err
	}
	id, err := intakedb.New(tx).LockCancelledSourceReconciliationLease(ctx, intakedb.LockCancelledSourceReconciliationLeaseParams{
		P1: kind, P2: key, P3: e.Owner, P4: e.AttemptFence, P5: e.AdmissionEpoch})
	if err != nil {
		return grantError("lock source reconciliation lease", err)
	}
	// Use a fresh clock after lock.
	valid, err := intakedb.New(tx).CheckSourceReconciliationLeaseExpiry(ctx, id)
	if err != nil {
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

type sourceMutationResult struct {
	receipt          run.IntakeReceipt
	exhaustionReason string
}

// committedSourceResult reads both the receipt and its immutable classification within
// the mutation transaction, including replay and conflict-wait paths.
func committedSourceResult(ctx context.Context, tx pgx.Tx, stored intakedb.IngestExecutionResult) (sourceMutationResult, error) {
	var payload sourcePayload
	if err := json.Unmarshal(stored.Payload, &payload); err != nil {
		return sourceMutationResult{}, fmt.Errorf("decode source result: %w", err)
	}
	receipt, err := receiptFromResult(ctx, tx, stored)
	if err != nil {
		return sourceMutationResult{}, err
	}
	return sourceMutationResult{receipt: receipt, exhaustionReason: payload.ExhaustionReason}, nil
}

func (s *Sources) sourceMutation(ctx context.Context, work run.IntakeWork, kind run.IntakeSubjectKind, operation string, payload any, apply func(pgx.Tx, intakedb.Querier, intakedb.IngestExecutionResult) error) (run.IntakeReceipt, error) {
	result, err := s.sourceMutationWithReason(ctx, work, kind, operation, payload, apply)
	return result.receipt, err
}

// sourceMutationWithReason stores the full operation payload alongside a Ledger
// result. Insert first to resolve same-transaction Bronze references; later
// failures roll back the result. Return its immutable reason with the receipt.
func (s *Sources) sourceMutationWithReason(ctx context.Context, work run.IntakeWork, kind run.IntakeSubjectKind, operation string, payload any, apply func(pgx.Tx, intakedb.Querier, intakedb.IngestExecutionResult) error) (sourceMutationResult, error) {
	raw, err := sourceJSON(payload)
	if err != nil {
		return sourceMutationResult{}, err
	}
	digest, err := sourceDigest(struct {
		Operation string
		Payload   json.RawMessage
	}{operation, raw})
	if err != nil {
		return sourceMutationResult{}, err
	}
	r := run.IntakeApplyRequest{Work: work, IdempotencyKey: run.IntakeIdempotencyKey(work), OutputDigest: model.Digest(digest), Outcome: run.OutcomeSucceeded}
	if err := r.Validate(); err != nil {
		return sourceMutationResult{}, fmt.Errorf("source receipt: %w", err)
	}
	reconciliation := operation == "acknowledge_launch" || operation == "close_window" || operation == "recover_page" || operation == "expire_request" || operation == "close_request"
	return sourceTransactionMode(ctx, s, work, kind, reconciliation || operation == "page_capture" || operation == "page_retry" || operation == "page_failure", payload, func(tx pgx.Tx, q intakedb.Querier) (sourceMutationResult, error) {
		if operation == "transfer_launch" {
			transfer := payload.(SourceLaunchTransfer)
			binding, err := readSourceLaunchTransferBinding(ctx, tx, work.Execution.Country, work.Execution.Scope.Subject.LeaseKey(), transfer.RequestKey)
			if err != nil {
				return sourceMutationResult{}, err
			}
			raw, err = sourceJSON(binding)
			if err != nil {
				return sourceMutationResult{}, err
			}
			digest, err := sourceDigest(struct {
				Operation string
				Payload   json.RawMessage
			}{operation, raw})
			if err != nil {
				return sourceMutationResult{}, err
			}
			r.OutputDigest = model.Digest(digest)
			key, stored, err := findSourceLaunchTransfer(ctx, tx, binding.RequestKey)
			if err == nil {
				if !sameSourceJSON(stored, raw) {
					return sourceMutationResult{}, fmt.Errorf("transfer launch binding: %w", run.ErrPayloadConflict)
				}
				// Return the issuing receipt.
				idempotency, err := q.ReadSourceTransferReceipt(ctx, key)
				if err != nil {
					return sourceMutationResult{}, err
				}
				prior, err := q.FindExecutionResult(ctx, idempotency)
				if err != nil {
					return sourceMutationResult{}, err
				}
				return committedSourceResult(ctx, tx, prior)
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return sourceMutationResult{}, err
			}
		}
		if operation == "page_capture" || operation == "page_retry" || operation == "page_failure" {
			var proposed struct{ ClaimResult string }
			if err := json.Unmarshal(raw, &proposed); err != nil {
				return sourceMutationResult{}, err
			}
			// Look up settled receipts
			// before testing current markers or terminal page state.
			idempotency, err := q.FindDiscoveryClaimOutcome(ctx, intakedb.FindDiscoveryClaimOutcomeParams{
				P1: work.Execution.Scope.Subject.LeaseKey(), P2: proposed.ClaimResult})
			if err == nil {
				prior, err := q.FindExecutionResult(ctx, idempotency)
				if err != nil {
					return sourceMutationResult{}, err
				}
				var committed sourcePayload
				if err := json.Unmarshal(prior.Payload, &committed); err != nil {
					return sourceMutationResult{}, err
				}
				if committed.SourceOperation != operation || !sameSourceJSON(committed.SourcePayload, raw) {
					return sourceMutationResult{}, fmt.Errorf("discovery outcome replay: %w", run.ErrPayloadConflict)
				}
				return committedSourceResult(ctx, tx, prior)
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return sourceMutationResult{}, err
			}
		}
		prior, lookupErr := q.FindExecutionResult(ctx, r.IdempotencyKey)
		if lookupErr == nil {
			if err := classifySourcePayload(prior.Payload, r, operation, raw); err != nil {
				return sourceMutationResult{}, err
			}
			return committedSourceResult(ctx, tx, prior)
		}
		if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return sourceMutationResult{}, fmt.Errorf("find source receipt: %w", lookupErr)
		}
		executionID, _, err := resolveExecution(ctx, tx, work)
		if err != nil {
			return sourceMutationResult{}, err
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
				return sourceMutationResult{}, fmt.Errorf("verify source capture: %w", err)
			}
		}
		exhaustionReason := ""
		var claimIdentity *sourceClaimIdentity
		if operation == "claim_page" {
			key := payload.(sourcePageClaimPayload).PageKey
			page, err := readSourcePage(ctx, tx, work.Execution.Scope.Subject.LeaseKey(), key)
			if err != nil {
				return sourceMutationResult{}, err
			}
			claimIdentity = &sourceClaimIdentity{Country: work.Execution.Country, WindowKey: work.Execution.Scope.Subject.LeaseKey(), PageKey: key, Ordinal: page.ClaimOrdinal + 1, Attempt: page.SourceAttempts + 1}
			claimIdentity.AttemptKey = discoveryAttemptKey(claimIdentity.WindowKey, key, claimIdentity.Ordinal)
		}
		params, err := resultParams(r, s.ledger.writer, executionID)
		if err != nil {
			return sourceMutationResult{}, err
		}
		if claimIdentity != nil {
			page, err := readSourcePage(ctx, tx, claimIdentity.WindowKey, claimIdentity.PageKey)
			if err != nil {
				return sourceMutationResult{}, err
			}
			if page.ClaimState != nil && *page.ClaimState == "released" && page.InfrastructureAttempts == 3 {
				params.Substep = "source-discovery-exhaustion"
				exhaustionReason = "infrastructure_retry_exhausted"
				claimIdentity.Ordinal = page.ClaimOrdinal
				claimIdentity.AttemptKey = discoveryAttemptKey(claimIdentity.WindowKey, claimIdentity.PageKey, page.ClaimOrdinal)
			}
		}
		if operation == "resume_window" || operation == "recover_page" {
			// Bind a denial/loss
			// receipt to its durable reason before any state change.
			pageKey := ""
			if operation == "recover_page" {
				pageKey = payload.(struct{ Key, ClaimResult string }).Key
			}
			exhausted, err := q.ReadDiscoveryOperationExhaustion(ctx, intakedb.ReadDiscoveryOperationExhaustionParams{
				P1: work.Execution.Scope.Subject.LeaseKey(), P2: pageKey})
			if err != nil {
				return sourceMutationResult{}, err
			}
			if exhausted {
				exhaustionReason = "infrastructure_retry_exhausted"
			}
		}
		closureResult := ""
		if operation == "resume_window" {
			// Preserve immutable closure
			// provenance without resetting any claim or retry allowance.
			closureResult, err = q.ReadSourceResumptionClosure(ctx, work.Execution.Scope.Subject.LeaseKey())
			if err != nil {
				return sourceMutationResult{}, grantError("read resumption closure", err)
			}
		}
		params.Payload, err = sourceJSON(sourcePayload{IntakeApplyRequest: r, SourceOperation: operation, SourcePayload: raw, Claim: claimIdentity, ClosureResult: closureResult, ExhaustionReason: exhaustionReason})
		if err != nil {
			return sourceMutationResult{}, err
		}
		var inserted intakedb.InsertExecutionResultRow
		var insertErr error
		if reconciliation {
			// Admission, owning
			// lease and immutable execution were checked above. Cancellation
			// allows acknowledgement, window/request closure, and claim release. Fresh
			// source outcomes still use the noncancelled generated result gate.
			_, insertErr = q.InsertSourceReconciliationResult(ctx, intakedb.InsertSourceReconciliationResultParams{
				P1: executionID, P2: params.ResultKey, P3: params.Stage, P4: params.Substep, P5: params.Shard,
				P6: params.IdempotencyKey, P7: params.InputDigest, P8: params.OutputDigest, P9: params.RecipeDigest,
				P10: params.BudgetDigest, P11: params.DecisionRevision, P12: params.Payload})
			inserted.ResultKey = params.ResultKey
		} else {
			inserted, insertErr = q.InsertExecutionResult(ctx, params)
		}
		if insertErr != nil && !errors.Is(insertErr, pgx.ErrNoRows) {
			return sourceMutationResult{}, fmt.Errorf("insert source receipt: %w", insertErr)
		}
		// Always a fresh statement snapshot after ON CONFLICT waits.
		stored, err := q.FindExecutionResult(ctx, r.IdempotencyKey)
		if err != nil {
			return sourceMutationResult{}, grantError("resolve source receipt", err)
		}
		if err := classifySourcePayload(stored.Payload, r, operation, raw); err != nil {
			return sourceMutationResult{}, err
		}
		if insertErr == nil && inserted.ResultKey == params.ResultKey {
			if err := apply(tx, q, stored); err != nil {
				return sourceMutationResult{}, err
			}
			if operation == "accept_page" || operation == "accept_capture" {
				if _, err := insertDelivery(ctx, q, intakedb.InsertDeliveryRecordParams{DeduplicationKey: stored.ResultKey, DeliveryKind: "completion", RecipientWorkflowID: work.Execution.WorkflowID, ReceiptKind: "result", ReceiptKey: stored.ResultKey, ExpectedEpoch: work.Execution.AdmissionEpoch, ExpectedHash: string(r.OutputDigest), DueAt: stored.CreatedAt}); err != nil {
					return sourceMutationResult{}, err
				}
			}
		}
		return committedSourceResult(ctx, tx, stored)
	})
}
