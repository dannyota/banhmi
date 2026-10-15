package intake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/source/api"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

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
	// The transaction wrapper validated
	// current admission and lease; cancellation permits closure and release.
	if _, err := q.LockSourceAccountingWindow(ctx, key); err != nil {
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
			// Only a recorded decode can
			// authorize a continuation; acceptance may arrive out of order.
			predecessor, err := q.ReadDiscoveryPredecessor(ctx, intakedb.ReadDiscoveryPredecessorParams{
				P1: w.WindowKey, P2: w.ID, P3: page.Ordinal, P4: page.Plan.RequestKey, P5: page.Plan.ParentCapture})
			if err != nil {
				return grantError("read discovery predecessor", err)
			}
			raw, priorPlanJSON, priorOrdinal := predecessor.Payload, predecessor.RequestPlan, predecessor.Ordinal
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

// Window identity is part of every page lookup.
func readSourcePage(ctx context.Context, tx pgx.Tx, window, key string) (SourcePage, error) {
	row, err := intakedb.New(tx).ReadDiscoveryPage(ctx, intakedb.ReadDiscoveryPageParams{P1: window, P2: key})
	p := SourcePage(row)
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
		locks, err := sourceLocks(tx)
		if err != nil {
			return err
		}
		windowID, ok := locks.windows[page.Cursor.WindowKey]
		if !ok {
			return fmt.Errorf("discovery window outside source lock set")
		}
		w, err := s.lockWindow(ctx, tx, q, work, page.Cursor.WindowKey)
		if err != nil {
			return err
		}
		if w.ID != windowID {
			return fmt.Errorf("discovery window differs from source lock set")
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
		// Column shape alone cannot prove
		// the full sandbox payload was accepted under this window and recipe.
		decodedPayload, err := q.ResolveSourceDecodeReceipt(ctx, intakedb.ResolveSourceDecodeReceiptParams{P1: w.WindowKey, P2: p.PageKey, P3: string(work.RecipeDigest)})
		if err != nil {
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
		var params []intakedb.InsertObservationRevisionParams
		for _, decoded := range page.Observations {
			observation, err := sourceObservation(decoded)
			if err != nil {
				return err
			}
			p, err := prepareObservation(ctx, tx, observation, w.WindowKey, blobID, executionKey, result, observedAt)
			if err != nil {
				return err
			}
			params = append(params, p)
		}
		revisions, err := insertObservationBatch(ctx, tx, q, params)
		if err != nil {
			return err
		}
		var observationKeys []string
		for _, revision := range revisions {
			observationKeys = append(observationKeys, revision.ObservationKey)
		}
		if err := protectSourceCapture(ctx, tx, q, page.Capture, executionKey, observationKeys); err != nil {
			return err
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
			// Evidence acceptance does
			// not authorize resumption or reset the owner's unresolved blocker.
			if err := q.PreserveSourceWindowBlocker(ctx, intakedb.PreserveSourceWindowBlockerParams{P1: w.ID, P2: w.Reason}); err != nil {
				return fmt.Errorf("preserve window blocker: %w", err)
			}
		}
		return reconcileSourceWindow(ctx, tx, w)
	})
}

// sqlc 1.31.1 :many buffers rows and cannot preserve this reader's streaming and per-row error handling.
// Read accepted receipt payloads in ledger order after acceptance.
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
		// The caller holds the window lock.
		err = intakedb.New(tx).CompleteSourceWindow(ctx, intakedb.CompleteSourceWindowParams{P1: w.ID, P2: next})
		if err != nil {
			return fmt.Errorf("complete source window: %w", err)
		}
		return nil
	}
	// A budget/deadline
	// blocker never fabricates a failed page. Completion took precedence above.
	eligibility, err := intakedb.New(tx).ReadSourceWindowClosureEligibility(ctx, w.ID)
	if err != nil {
		return fmt.Errorf("read window closure eligibility: %w", err)
	}
	reason := ""
	switch {
	case w.State == string(model.SourceWindowIncomplete) && w.Reason != nil:
		reason = *w.Reason
	case hasFindings:
		reason = "coverage_finding"
	case !eligibility.Now.Before(w.DeadlineAt):
		reason = "window_deadline"
	case allAccepted && !exhausted && count >= int(w.PageBudget), eligibility.Requests >= int64(w.RequestBudget) && (!allAccepted || !exhausted):
		reason = "window_budget"
	}
	if reason != "" {
		// Preserve all nonterminal page states.
		if err := intakedb.New(tx).CloseSourceWindowForAccounting(ctx, intakedb.CloseSourceWindowForAccountingParams{P1: w.ID, P2: &reason}); err != nil {
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
			if err := q.FailDiscoveryPage(ctx, intakedb.FailDiscoveryPageParams{P1: p.ID, P2: &closure.Reason}); err != nil {
				return fmt.Errorf("fail discovery page: %w", err)
			}
			if err := q.CloseSiblingDiscoveryPages(ctx, intakedb.CloseSiblingDiscoveryPagesParams{P1: w.ID, P2: p.ID}); err != nil {
				return fmt.Errorf("close sibling pages: %w", err)
			}
		}
		if err := q.CloseSourceWindow(ctx, intakedb.CloseSourceWindowParams{P1: w.ID, P2: state, P3: &closure.Reason}); err != nil {
			return fmt.Errorf("close source window: %w", err)
		}
		return closeDiscoveryClaims(ctx, tx, w)
	})
}

// ResumeWindow clears a resolved blocker without resetting frozen inputs.
// A spent request budget or deadline prevents resumption. Page registration
// still enforces its frozen page limit. Acceptance and reconciliation may continue.
func (s *Sources) ResumeWindow(ctx context.Context, work run.IntakeWork) (run.IntakeReceipt, error) {
	result, err := s.sourceMutationWithReason(ctx, work, run.SubjectSourceWindow, "resume_window", work.Execution.Scope.Subject.LeaseKey(), func(tx pgx.Tx, q intakedb.Querier, _ intakedb.IngestExecutionResult) error {
		w, err := s.lockWindow(ctx, tx, q, work, work.Execution.Scope.Subject.LeaseKey())
		if err != nil {
			return err
		}
		// Replacement eligibility
		// cannot be reset by clearing an incomplete-window blocker.
		exhausted, err := q.ReadDiscoveryResumptionExhaustion(ctx, w.ID)
		if err == nil {
			return failDiscoveryInfrastructure(ctx, tx, w, exhausted)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// Counts use a new statement after lock.
		_, err = q.ResumeSourceWindow(ctx, w.ID)
		if err != nil {
			return grantError("resume source window", err)
		}
		return nil
	})
	if err != nil {
		return run.IntakeReceipt{}, err
	}
	if s.afterResumeCommit != nil {
		s.afterResumeCommit()
	}
	if result.exhaustionReason != "" {
		return result.receipt, fmt.Errorf("discovery resumption exhausted: %w", run.ErrStaleGrant)
	}
	return result.receipt, nil
}
