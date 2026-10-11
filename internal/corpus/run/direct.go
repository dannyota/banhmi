package run

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	ErrLeaseIdentity = errors.New("lease identity mismatch")
	ErrLeaseLost     = errors.New("scheduler lease lost")
	ErrStaleResult   = errors.New("stale stage result")
	ErrNoProgress    = errors.New("stage made no progress")
)

// Clock supplies direct-runner deadlines and renewal signals.
type Clock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// DirectConfig controls the scheduler lease held around one shard.
type DirectConfig struct {
	LeaseDuration time.Duration
	RenewInterval time.Duration
	Clock         Clock
}

// Direct executes one bounded shard under a renewable scheduler lease and
// accepts its immutable outputs through a durable unit of work.
type Direct struct {
	leases        SchedulerLease
	executor      Executor
	uow           UnitOfWork
	leaseDuration time.Duration
	renewInterval time.Duration
	clock         Clock
}

// NewDirect constructs a direct runner over durable injected ports.
func NewDirect(leases SchedulerLease, executor Executor, uow UnitOfWork, cfg DirectConfig) (*Direct, error) {
	if leases == nil {
		return nil, fmt.Errorf("scheduler lease is required")
	}
	if executor == nil {
		return nil, fmt.Errorf("executor is required")
	}
	if uow == nil {
		return nil, fmt.Errorf("unit of work is required")
	}
	if cfg.LeaseDuration <= 0 {
		return nil, fmt.Errorf("lease duration must be positive")
	}
	if cfg.RenewInterval <= 0 || cfg.RenewInterval >= cfg.LeaseDuration {
		return nil, fmt.Errorf("renew interval must be positive and shorter than lease duration")
	}
	if cfg.Clock == nil {
		cfg.Clock = systemClock{}
	}
	return &Direct{
		leases: leases,
		executor: executor,
		uow: uow,
		leaseDuration: cfg.LeaseDuration,
		renewInterval: cfg.RenewInterval,
		clock: cfg.Clock,
	}, nil
}

type executionResponse struct {
	result Result
	err    error
}

// Execute acquires a fence, binds it into the shard, and renews that lease.
// It never calls UnitOfWork.Apply after cancellation, lease loss, expiry,
// stale identity, or an invalid or non-progressing result. Executors must
// honor cancellation.
func (d *Direct) Execute(ctx context.Context, shard Shard) (committed Result, err error) {
	if shard.Execution.LeaseFence != 0 {
		return Result{}, fmt.Errorf("validate shard: lease fence must be zero before acquisition")
	}
	if err := validateUnfencedShard(shard); err != nil {
		return Result{}, fmt.Errorf("validate shard: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	now := d.clock.Now()
	deadlineAt := now.Add(shard.Budget.MaxElapsed)
	runCtx, cancelRun := context.WithTimeout(ctx, shard.Budget.MaxElapsed)
	defer cancelRun()
	wantedExpiry := now.Add(d.leaseDuration)
	if deadlineAt.Before(wantedExpiry) {
		wantedExpiry = deadlineAt
	}
	lease, err := d.leases.Acquire(
		runCtx,
		shard.Execution.Country,
		shard.Execution.BuildID,
		shard.Execution.Owner,
		wantedExpiry,
	)
	if err != nil {
		return Result{}, fmt.Errorf("acquire scheduler lease: %w", err)
	}
	defer func() {
		releaseTimeout := d.renewInterval
		if remaining := lease.ExpiresAt.Sub(d.clock.Now()); remaining > 0 && remaining < releaseTimeout {
			releaseTimeout = remaining
		}
		releaseCtx, cancelRelease := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
		defer cancelRelease()
		if releaseErr := d.leases.Release(releaseCtx, lease); releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("release scheduler lease: %w", releaseErr))
		}
	}()
	if err := validateLeaseOwner(lease, shard.Execution, d.clock.Now()); err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrLeaseIdentity, err)
	}
	shard.Execution.LeaseFence = lease.Fence
	now = d.clock.Now()
	if !now.Before(deadlineAt) {
		return Result{}, context.DeadlineExceeded
	}

	workerCtx, cancel := context.WithCancel(runCtx)
	defer cancel()
	responses := make(chan executionResponse, 1)
	go func() {
		result, executeErr := d.executor.Execute(workerCtx, shard)
		responses <- executionResponse{result: result, err: executeErr}
	}()

	renew := d.clock.After(d.nextRenewal(now, lease.ExpiresAt))
	deadline := d.clock.After(deadlineAt.Sub(now))
	for {
		select {
		case <-runCtx.Done():
			cancel()
			<-responses
			return Result{}, runCtx.Err()
		case <-deadline:
			cancel()
			<-responses
			return Result{}, context.DeadlineExceeded
		case response := <-responses:
			if ctxErr := runCtx.Err(); ctxErr != nil {
				return Result{}, ctxErr
			}
			if !d.clock.Now().Before(deadlineAt) {
				return Result{}, context.DeadlineExceeded
			}
			if response.err != nil {
				return Result{}, fmt.Errorf("execute shard: %w", response.err)
			}
			if !d.clock.Now().Before(lease.ExpiresAt) {
				return Result{}, fmt.Errorf("%w: expired before result acceptance", ErrLeaseLost)
			}
			applyUntil := lease.ExpiresAt
			if deadlineAt.Before(applyUntil) {
				applyUntil = deadlineAt
			}
			applyCtx, cancelApply := context.WithTimeout(runCtx, applyUntil.Sub(d.clock.Now()))
			committed, applyErr := d.apply(applyCtx, shard, response.result)
			applyContextErr := applyCtx.Err()
			cancelApply()
			if applyErr == nil && runCtx.Err() != nil {
				return Result{}, runCtx.Err()
			}
			if applyErr == nil && !d.clock.Now().Before(deadlineAt) {
				return Result{}, context.DeadlineExceeded
			}
			if applyErr == nil && (applyContextErr != nil || !d.clock.Now().Before(lease.ExpiresAt)) {
				return Result{}, fmt.Errorf("%w: expired before commit response", ErrLeaseLost)
			}
			return committed, applyErr
		case <-renew:
			now = d.clock.Now()
			if ctxErr := runCtx.Err(); ctxErr != nil {
				cancel()
				<-responses
				return Result{}, ctxErr
			}
			if !now.Before(deadlineAt) {
				cancel()
				<-responses
				return Result{}, context.DeadlineExceeded
			}
			if !now.Before(lease.ExpiresAt) {
				cancel()
				<-responses
				return Result{}, fmt.Errorf("%w: expired before renewal", ErrLeaseLost)
			}
			renewUntil := now.Add(d.leaseDuration)
			if deadlineAt.Before(renewUntil) {
				renewUntil = deadlineAt
			}
			renewCtx, cancelRenew := context.WithTimeout(runCtx, lease.ExpiresAt.Sub(now))
			renewed, renewErr := d.leases.Renew(renewCtx, lease, renewUntil)
			cancelRenew()
			if renewErr != nil {
				cancel()
				<-responses
				if ctxErr := runCtx.Err(); ctxErr != nil {
					return Result{}, ctxErr
				}
				return Result{}, fmt.Errorf("%w: renew: %w", ErrLeaseLost, renewErr)
			}
			postRenewNow := d.clock.Now()
			if identityErr := validateLeaseIdentity(renewed, shard.Execution, postRenewNow); identityErr != nil {
				cancel()
				<-responses
				return Result{}, fmt.Errorf("%w: renewed lease: %w", ErrLeaseLost, identityErr)
			}
			lease = renewed
			renew = d.clock.After(d.nextRenewal(postRenewNow, lease.ExpiresAt))
		}
	}
}

func validateUnfencedShard(shard Shard) error {
	if err := shard.Execution.validateIdentity(); err != nil {
		return fmt.Errorf("execution: %w", err)
	}
	if !shard.Stage.Valid() {
		return fmt.Errorf("unknown stage %q", shard.Stage)
	}
	if shard.Recipe == "" {
		return fmt.Errorf("recipe is required")
	}
	if err := shard.Budget.Validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	return nil
}

func (d *Direct) nextRenewal(now, expiresAt time.Time) time.Duration {
	remaining := expiresAt.Sub(now)
	if d.renewInterval < remaining {
		return d.renewInterval
	}
	next := remaining / 2
	if next <= 0 {
		return time.Nanosecond
	}
	return next
}

func (d *Direct) apply(ctx context.Context, shard Shard, result Result) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if result.Shard != shard {
		return Result{}, fmt.Errorf("%w: executor returned another shard", ErrStaleResult)
	}
	if err := result.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate executor result: %w", err)
	}
	if !result.Complete && result.Counts.Processed == 0 && len(result.Outputs) == 0 &&
		result.NextEligible == nil &&
		(result.NextCursor == "" || result.NextCursor == shard.Cursor) {
		return Result{}, ErrNoProgress
	}
	// A later continuation must change cursor, manifest, or another immutable
	// shard input. Reusing this key can only replay this accepted result.
	request := applyRequest(result)
	if err := request.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate apply request: %w", err)
	}
	committed, err := d.uow.Apply(ctx, request)
	if err != nil {
		return Result{}, fmt.Errorf("apply stage result: %w", err)
	}
	if err := committed.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate committed result: %w", err)
	}
	if !SameImmutablePayload(request, applyRequest(committed)) {
		return Result{}, fmt.Errorf("%w: unit of work returned another result payload", ErrStaleResult)
	}
	committed.Shard.Execution.Owner = shard.Execution.Owner
	committed.Shard.Execution.LeaseFence = shard.Execution.LeaseFence
	return committed, nil
}
