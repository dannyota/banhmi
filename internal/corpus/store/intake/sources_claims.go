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

// The page marker, not receipt absence,
// owns the slot. Historical execution identity supplies provenance only.
func latestDiscoveryClaim(ctx context.Context, tx pgx.Tx, window, page, state string, currentResult int64) (sourcePageClaim, error) {
	var claim sourcePageClaim
	row, err := intakedb.New(tx).ReadLatestDiscoveryClaim(ctx, intakedb.ReadLatestDiscoveryClaimParams{
		P1: window, P2: page, P3: &state, P4: currentResult})
	claim.ResultKey, claim.HostKey = row.ResultKey, row.Locator
	if err != nil {
		return claim, err
	}
	if err := json.Unmarshal(row.Work, &claim.Work); err != nil {
		return claim, err
	}
	if err := json.Unmarshal(row.Claim, &claim.Identity); err != nil {
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
		// Window precedes canonical host.
		hostID, err := q.LockDiscoveryHost(ctx, host)
		if err != nil {
			return grantError("lock discovery host", err)
		}
		// Eligibility uses a fresh database
		// clock after the host lock wait, including the window lease watermark.
		if _, err := q.ClaimDiscoveryHost(ctx, intakedb.ClaimDiscoveryHostParams{P1: hostID, P2: w.ID, P3: p.ID}); err != nil {
			return grantError("claim discovery host slot", err)
		}
		// Advance the infrastructure ordinal
		// only when replacing a released claim, then acquire the new identity.
		if err := q.IssueDiscoveryClaim(ctx, p.ID); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return SourceDiscoveryClaim{}, err
	}
	// Immutable claim identity survives
	// later claims, source outcomes, and separately scheduled reconciliation.
	return sourceTransaction(ctx, s, work, run.SubjectSourceWindow, func(tx pgx.Tx, q intakedb.Querier) (SourceDiscoveryClaim, error) {
		var identity sourceClaimIdentity
		row, err := q.ReadDiscoveryClaimAttempt(ctx, receipt.ResultID)
		if err != nil {
			return SourceDiscoveryClaim{}, grantError("read issued claim identity", err)
		}
		if err := json.Unmarshal(row.Claim, &identity); err != nil {
			return SourceDiscoveryClaim{}, grantError("read issued claim identity", err)
		}
		page, err := readSourcePage(ctx, tx, work.Execution.Scope.Subject.LeaseKey(), key)
		if err != nil {
			return SourceDiscoveryClaim{}, err
		}
		if row.Substep == "source-discovery-exhaustion" || page.ClaimOrdinal < identity.Ordinal {
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
	// Only an active claim under the locked
	// window authorizes release; the committed outcome consumes that claim.
	if _, err := intakedb.New(tx).ReleaseDiscoveryHost(ctx, intakedb.ReleaseDiscoveryHostParams{P1: claim.HostKey, P2: retryAfter}); err != nil {
		return grantError("release discovery host slot", err)
	}
	return nil
}

// closeDiscoveryClaims releases only page markers that still own slots. All
// page and host locks were acquired by prelockSourceAccounting before writes.
func closeDiscoveryClaims(ctx context.Context, tx pgx.Tx, w intakedb.IngestSourceWindow) error {
	// Bound by frozen page budget.
	keys, err := intakedb.New(tx).ReadIssuedDiscoveryClosurePages(ctx, intakedb.ReadIssuedDiscoveryClosurePagesParams{P1: w.ID, P2: w.PageBudget})
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
		// Closure changes no attempt counter.
		if err := intakedb.New(tx).ReleaseDiscoveryClaim(ctx, intakedb.ReleaseDiscoveryClaimParams{P1: w.ID, P2: key}); err != nil {
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
	// Keep all earlier receipt groups.
	if err := intakedb.New(tx).ExhaustDiscoveryInfrastructure(ctx, intakedb.ExhaustDiscoveryInfrastructureParams{P1: w.ID, P2: key}); err != nil {
		return err
	}
	err := intakedb.New(tx).ExhaustDiscoveryWindow(ctx, w.ID)
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
		// Loss releases the marker
		// without advancing the ordinal reserved for replacement issue.
		exhausted, err := q.RecoverDiscoveryInfrastructure(ctx, intakedb.RecoverDiscoveryInfrastructureParams{P1: key, P2: w.ID})
		if err != nil {
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
			// Reuse compares
			// the complete outcome, including its original page and claim binding.
			prior, err := q.ReadHistoricalDiscoveryCaptureOutcome(ctx, intakedb.ReadHistoricalDiscoveryCaptureOutcomeParams{P1: w.WindowKey, P2: result.ID, P3: receipt.Key})
			if err != nil {
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
		// A late issued outcome may commit
		// after the fetch deadline; it preserves an incomplete window's blocker.
		if _, err := q.SettleDiscoveryCapture(ctx, intakedb.SettleDiscoveryCaptureParams{P1: p.ID, P2: &receipt.Capture.CaptureKey,
			P3: &digest, P4: &receipt.Capture.Asset.ByteSize, P5: encoded, P6: &receipt.AttemptKey, P7: receipt.Attempt}); err != nil {
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
		// Current owner reconciliation
		// accepts issued failures on incomplete windows without clearing blockers.
		failed, err := q.RecordDiscoveryFailureReceipt(ctx, intakedb.RecordDiscoveryFailureReceiptParams{
			P1: p.ID, P2: &retry.AttemptKey, P3: retry.RetryAfter, P4: retry.Attempt})
		if err != nil {
			return grantError("record discovery failure", err)
		}
		// Eligibility only advances.
		if err := q.ScheduleDiscoveryFailure(ctx, intakedb.ScheduleDiscoveryFailureParams{P1: w.WindowKey, P2: retry.Next, P3: retry.RetryAfter}); err != nil {
			return fmt.Errorf("schedule discovery failure: %w", err)
		}
		if !released {
			if err := releaseDiscoveryHost(ctx, tx, claim, &retry.RetryAfter); err != nil {
				return err
			}
		}
		if released {
			// Closure already
			// released the slot, but the issued Retry-After still advances pacing.
			if err := q.AdvanceReleasedDiscoveryHostEligibility(ctx, intakedb.AdvanceReleasedDiscoveryHostEligibilityParams{P1: claim.HostKey, P2: retry.RetryAfter}); err != nil {
				return fmt.Errorf("reconcile released discovery pacing: %w", err)
			}
		}
		if failed {
			if err := closeDiscoveryClaims(ctx, tx, w); err != nil {
				return err
			}
			if err := q.CloseSourceRetryExhaustedWindow(ctx, w.ID); err != nil {
				return fmt.Errorf("close source retry window: %w", err)
			}
			if err := q.CloseSourceRetrySiblingDiscoveryPages(ctx, intakedb.CloseSourceRetrySiblingDiscoveryPagesParams{P1: w.ID, P2: p.ID}); err != nil {
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
		// State and marker change
		// in one statement, keeping the claim CHECK valid.
		if _, err := q.SettleIntrinsicDiscoveryFailure(ctx, intakedb.SettleIntrinsicDiscoveryFailureParams{
			P1: page.ID, P2: &failure.Reason, P3: &failure.AttemptKey, P4: failure.Attempt}); err != nil {
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
		if err := q.CloseIntrinsicDiscoverySiblings(ctx, intakedb.CloseIntrinsicDiscoverySiblingsParams{P1: w.ID, P2: page.ID}); err != nil {
			return err
		}
		err = q.CloseIntrinsicDiscoveryWindow(ctx, intakedb.CloseIntrinsicDiscoveryWindowParams{P1: w.ID, P2: &failure.Reason})
		return err
	})
}
