package intake

import (
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
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/source/api"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

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
	return sourceTransactionMode(ctx, s, work, run.SubjectDocumentVersion, false, key, func(tx pgx.Tx, q intakedb.Querier) (intakedb.IngestArtifactRequest, error) {
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
// Cancellation permits closure, including the infrastructure retry cap under
// a fresh fence. Every leased exit releases its slot once and preserves receipts.
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
		// Use the locked adapter closure at every infrastructure ordinal.
		// The generated ExpireArtifactRequest rejects cancelled leases.
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
			now, err := lockLeaseMode(ctx, tx, work.Execution, true)
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
