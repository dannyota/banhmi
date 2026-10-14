package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/source/api"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

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
	err := tx.QueryRow(ctx, `SELECT r.result_key, r.payload->'Work', p.request_plan->>'URL', r.payload->'Claim'
		FROM ingest.discovery_page p JOIN ingest.source_window w ON w.id = p.source_window_id
		JOIN ingest.execution e ON e.resource_kind = 'source_window' AND e.resource_key = w.window_key
		JOIN ingest.execution_result r ON r.execution_id = e.id
		WHERE w.window_key = $1 AND p.page_key = $2 AND p.claim_state = $3 AND r.id <> $4
		AND r.payload->>'SourceOperation' = 'claim_page'
		AND r.payload->'Claim'->>'WindowKey' = w.window_key AND r.payload->'Claim'->>'PageKey' = p.page_key
		AND (r.payload->'Claim'->>'Ordinal')::integer = p.claim_ordinal
		ORDER BY r.id DESC LIMIT 1`, window, page, state, currentResult).Scan(&claim.ResultKey, &raw, &claim.HostKey, &identity)
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
