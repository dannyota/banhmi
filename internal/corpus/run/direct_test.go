package run

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDirectCarriesOneExecutionAndOutputsIntoAtomicApply(t *testing.T) {
	shard := testShard()
	wantResult := testResult()
	wantOutput := wantResult.Outputs[0]
	leases := &fakeLeases{lease: testLease(shard)}
	uow := &fakeUnitOfWork{}
	direct := newTestDirect(t, leases, executorFunc(func(_ context.Context, got Shard) (Result, error) {
		wantShard := shard
		wantShard.Execution.LeaseFence = 7
		if got != wantShard {
			t.Fatalf("executor shard = %+v, want %+v", got, wantShard)
		}
		return wantResult, nil
	}), uow, newFakeClock(time.Unix(1_000, 0)))

	got, err := direct.Execute(context.Background(), shard)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got.Shard.Execution.LeaseFence != 7 {
		t.Fatalf("committed fence = %d, want acquired fence 7", got.Shard.Execution.LeaseFence)
	}
	if len(uow.requests) != 1 {
		t.Fatalf("Apply() calls = %d, want 1", len(uow.requests))
	}
	request := uow.requests[0]
	if !reflect.DeepEqual(request.Result, wantResult) {
		t.Fatalf("Apply() result = %+v, want %+v", request.Result, wantResult)
	}
	if len(request.Outputs) != 1 || request.Outputs[0] != wantOutput {
		t.Fatalf("Apply() outputs = %+v, want %+v", request.Outputs, []OutputRef{wantOutput})
	}
	if request.IdempotencyKey != IdempotencyKey(shard) {
		t.Fatalf("Apply() key = %q, want %q", request.IdempotencyKey, IdempotencyKey(shard))
	}
	if leases.acquireCountry != shard.Execution.Country ||
		leases.acquireBuild != shard.Execution.BuildID ||
		leases.acquireOwner != shard.Execution.Owner {
		t.Fatalf(
			"Acquire() identity = %q/%s/%q, want %q/%s/%q",
			leases.acquireCountry,
			leases.acquireBuild,
			leases.acquireOwner,
			shard.Execution.Country,
			shard.Execution.BuildID,
			shard.Execution.Owner,
		)
	}
	if !leases.acquireHasDeadline {
		t.Fatal("Acquire() context has no elapsed-budget deadline")
	}
	if wantExpiry := time.Unix(1_010, 0); !leases.acquireExpiry.Equal(wantExpiry) {
		t.Fatalf("Acquire() expiry = %s, want %s", leases.acquireExpiry, wantExpiry)
	}
}

func TestDirectRejectsWrongLeaseIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Lease)
	}{
		{name: "country", mutate: func(l *Lease) { l.Country = "sg" }},
		{name: "build", mutate: func(l *Lease) { l.BuildID = uuid.MustParse("30000000-0000-0000-0000-000000000003") }},
		{name: "owner", mutate: func(l *Lease) { l.Owner = "another-worker" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shard := testShard()
			lease := testLease(shard)
			tt.mutate(&lease)
			leases := &fakeLeases{lease: lease}
			uow := &fakeUnitOfWork{}
			direct := newTestDirect(t, leases, executorFunc(func(context.Context, Shard) (Result, error) {
				t.Fatal("executor called after invalid lease")
				return Result{}, nil
			}), uow, newFakeClock(time.Unix(1_000, 0)))

			_, err := direct.Execute(context.Background(), shard)
			if !errors.Is(err, ErrLeaseIdentity) {
				t.Fatalf("Execute() error = %v, want ErrLeaseIdentity", err)
			}
			if len(uow.requests) != 0 {
				t.Fatalf("Apply() calls = %d, want 0", len(uow.requests))
			}
		})
	}
}

func TestDirectRejectsWrongExecutorResultIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Execution)
	}{
		{name: "country", mutate: func(e *Execution) { e.Country = "my" }},
		{name: "run", mutate: func(e *Execution) { e.RunID = uuid.MustParse("30000000-0000-0000-0000-000000000003") }},
		{name: "build", mutate: func(e *Execution) { e.BuildID = uuid.MustParse("30000000-0000-0000-0000-000000000003") }},
		{name: "owner", mutate: func(e *Execution) { e.Owner = "wrong-worker" }},
		{name: "fence", mutate: func(e *Execution) { e.LeaseFence++ }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shard := testShard()
			result := testResult()
			tt.mutate(&result.Shard.Execution)
			uow := &fakeUnitOfWork{}
			direct := newTestDirect(t, &fakeLeases{lease: testLease(shard)}, executorFunc(func(
				context.Context,
				Shard,
			) (Result, error) {
				return result, nil
			}), uow, newFakeClock(time.Unix(1_000, 0)))

			_, err := direct.Execute(context.Background(), shard)
			if !errors.Is(err, ErrStaleResult) {
				t.Fatalf("Execute() error = %v, want ErrStaleResult", err)
			}
			if len(uow.requests) != 0 {
				t.Fatalf("Apply() calls = %d, want 0", len(uow.requests))
			}
		})
	}
}

func TestDirectRecoversCommittedResultAfterResponseLoss(t *testing.T) {
	shard := testShard()
	uow := &replayUnitOfWork{loseFirstResponse: true}
	direct := newTestDirect(t, &fakeLeases{
		lease: testLease(shard), incrementFence: true,
	}, executorFunc(func(
		_ context.Context,
		executing Shard,
	) (Result, error) {
		result := testResult()
		result.Shard = executing
		return result, nil
	}), uow, newFakeClock(time.Unix(1_000, 0)))

	_, err := direct.Execute(context.Background(), shard)
	if err == nil || !strings.Contains(err.Error(), "response lost") {
		t.Fatalf("first Execute() error = %v, want simulated response loss", err)
	}
	second, err := direct.Execute(context.Background(), shard)
	if err != nil {
		t.Fatalf("replayed Execute() error = %v", err)
	}
	wantReplay := testResult()
	wantReplay.Shard.Execution.LeaseFence = 8
	if !reflect.DeepEqual(second, wantReplay) {
		t.Fatalf("replayed result = %+v, want current execution result %+v", second, wantReplay)
	}
	if uow.calls != 2 || uow.keys[0] != uow.keys[1] {
		t.Fatalf("Apply() calls = %d, keys = %v, want two calls with one deterministic key", uow.calls, uow.keys)
	}
}

func TestDirectRejectsSameKeyWithDifferentPayload(t *testing.T) {
	shard := testShard()
	calls := 0
	uow := &replayUnitOfWork{}
	direct := newTestDirect(t, &fakeLeases{lease: testLease(shard)}, executorFunc(func(
		context.Context,
		Shard,
	) (Result, error) {
		calls++
		result := testResult()
		if calls == 2 {
			result.Counts = Counts{Expected: 2, Processed: 2, Succeeded: 2}
		}
		return result, nil
	}), uow, newFakeClock(time.Unix(1_000, 0)))

	if _, err := direct.Execute(context.Background(), shard); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	if _, err := direct.Execute(context.Background(), shard); err == nil ||
		!strings.Contains(err.Error(), "idempotency payload mismatch") {
		t.Fatalf("second Execute() error = %v, want payload mismatch", err)
	}
}

func TestDirectPropagatesUnitOfWorkStaleFenceRejection(t *testing.T) {
	shard := testShard()
	uow := &fencedUnitOfWork{currentFence: 8}
	direct := newTestDirect(t, &fakeLeases{lease: testLease(shard)}, executorFunc(func(
		context.Context,
		Shard,
	) (Result, error) {
		return testResult(), nil
	}), uow, newFakeClock(time.Unix(1_000, 0)))

	if _, err := direct.Execute(context.Background(), shard); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Execute() error = %v, want stale-fence rejection", err)
	}
	if uow.committed != 0 {
		t.Fatalf("committed results = %d, want 0", uow.committed)
	}
}

func TestDirectRenewsLeaseDuringLongExecution(t *testing.T) {
	shard := testShard()
	clock := newFakeClock(time.Unix(1_000, 0))
	started := make(chan struct{})
	finish := make(chan struct{})
	renewed := make(chan struct{})
	leases := &fakeLeases{lease: testLease(shard), renewed: renewed}
	direct := newTestDirect(t, leases, executorFunc(func(ctx context.Context, _ Shard) (Result, error) {
		close(started)
		select {
		case <-finish:
			return testResult(), nil
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}), &fakeUnitOfWork{}, clock)

	done := make(chan error, 1)
	go func() {
		_, err := direct.Execute(context.Background(), shard)
		done <- err
	}()
	<-started
	waitForSignal(t, clock.scheduled)
	clock.advance(5 * time.Second)
	waitForSignal(t, renewed)
	close(finish)
	if err := <-done; err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if leases.renewCalls != 1 {
		t.Fatalf("Renew() calls = %d, want 1", leases.renewCalls)
	}
}

func TestDirectStopsBeforeApplyWhenRenewalLosesLease(t *testing.T) {
	shard := testShard()
	clock := newFakeClock(time.Unix(1_000, 0))
	started := make(chan struct{})
	leases := &fakeLeases{lease: testLease(shard), renewErr: ErrLeaseLost}
	uow := &fakeUnitOfWork{}
	direct := newTestDirect(t, leases, executorFunc(func(ctx context.Context, _ Shard) (Result, error) {
		close(started)
		<-ctx.Done()
		return Result{}, ctx.Err()
	}), uow, clock)

	done := make(chan error, 1)
	go func() {
		_, err := direct.Execute(context.Background(), shard)
		done <- err
	}()
	<-started
	waitForSignal(t, clock.scheduled)
	clock.advance(5 * time.Second)
	if err := <-done; !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Execute() error = %v, want ErrLeaseLost", err)
	}
	if len(uow.requests) != 0 {
		t.Fatalf("Apply() calls = %d, want 0", len(uow.requests))
	}
}

func TestDirectRejectsLeaseExpiredDuringRenewal(t *testing.T) {
	shard := testShard()
	clock := newFakeClock(time.Unix(1_000, 0))
	started := make(chan struct{})
	leases := &fakeLeases{
		lease: testLease(shard), renewExpiry: time.Unix(1_005, 0),
	}
	uow := &fakeUnitOfWork{}
	direct := newTestDirect(t, leases, executorFunc(func(ctx context.Context, _ Shard) (Result, error) {
		close(started)
		<-ctx.Done()
		return Result{}, ctx.Err()
	}), uow, clock)

	done := make(chan error, 1)
	go func() {
		_, err := direct.Execute(context.Background(), shard)
		done <- err
	}()
	<-started
	waitForSignal(t, clock.scheduled)
	clock.advance(5 * time.Second)
	if err := <-done; !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Execute() error = %v, want expired renewal rejection", err)
	}
	if len(uow.requests) != 0 {
		t.Fatalf("Apply() calls = %d, want 0", len(uow.requests))
	}
}

func TestDirectStopsAtShardElapsedBudget(t *testing.T) {
	shard := testShard()
	shard.Budget.MaxElapsed = 4 * time.Second
	clock := newFakeClock(time.Unix(1_000, 0))
	started := make(chan struct{})
	uow := &fakeUnitOfWork{}
	direct := newTestDirect(t, &fakeLeases{lease: testLease(shard)}, executorFunc(func(
		ctx context.Context,
		_ Shard,
	) (Result, error) {
		close(started)
		<-ctx.Done()
		return Result{}, ctx.Err()
	}), uow, clock)

	done := make(chan error, 1)
	go func() {
		_, err := direct.Execute(context.Background(), shard)
		done <- err
	}()
	<-started
	waitForSignal(t, clock.scheduled)
	waitForSignal(t, clock.scheduled)
	clock.advance(4 * time.Second)
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute() error = %v, want context.DeadlineExceeded", err)
	}
	if len(uow.requests) != 0 {
		t.Fatalf("Apply() calls = %d, want 0", len(uow.requests))
	}
}

func TestDirectCancellationStopsBeforeApplyAndReleasesLease(t *testing.T) {
	shard := testShard()
	started := make(chan struct{})
	leases := &fakeLeases{lease: testLease(shard)}
	uow := &fakeUnitOfWork{}
	direct := newTestDirect(t, leases, executorFunc(func(ctx context.Context, _ Shard) (Result, error) {
		close(started)
		<-ctx.Done()
		return Result{}, ctx.Err()
	}), uow, newFakeClock(time.Unix(1_000, 0)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := direct.Execute(ctx, shard)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v, want context.Canceled", err)
	}
	if len(uow.requests) != 0 {
		t.Fatalf("Apply() calls = %d, want 0", len(uow.requests))
	}
	if leases.releaseCalls != 1 {
		t.Fatalf("Release() calls = %d, want 1", leases.releaseCalls)
	}
	if !leases.releaseHasDeadline || leases.releaseContextErr != nil {
		t.Fatalf(
			"Release() deadline = %t, context error = %v, want bounded uncanceled cleanup",
			leases.releaseHasDeadline,
			leases.releaseContextErr,
		)
	}
}

func TestDirectRejectsNonProgress(t *testing.T) {
	shard := testShard()
	result := Result{Shard: testFencedShard(), NextCursor: shard.Cursor}
	uow := &fakeUnitOfWork{}
	direct := newTestDirect(t, &fakeLeases{lease: testLease(shard)}, executorFunc(func(
		context.Context,
		Shard,
	) (Result, error) {
		return result, nil
	}), uow, newFakeClock(time.Unix(1_000, 0)))

	_, err := direct.Execute(context.Background(), shard)
	if !errors.Is(err, ErrNoProgress) {
		t.Fatalf("Execute() error = %v, want ErrNoProgress", err)
	}
	if len(uow.requests) != 0 {
		t.Fatalf("Apply() calls = %d, want 0", len(uow.requests))
	}
}

func TestDirectAcceptsObservableProgressWithoutCursorChange(t *testing.T) {
	tests := []struct {
		name   string
		result Result
	}{
		{
			name: "processed item",
			result: Result{
				Shard: testFencedShard(),
				NextCursor: testShard().Cursor,
				Counts: Counts{Expected: 1, Processed: 1, Succeeded: 1},
			},
		},
		{
			name: "immutable output",
			result: Result{
				Shard: testFencedShard(),
				NextCursor: testShard().Cursor,
				Outputs: []OutputRef{{Ref: "blob://sha256/progress", Digest: "sha256:progress"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shard := testShard()
			uow := &fakeUnitOfWork{}
			direct := newTestDirect(t, &fakeLeases{lease: testLease(shard)}, executorFunc(func(
				context.Context,
				Shard,
			) (Result, error) {
				return tt.result, nil
			}), uow, newFakeClock(time.Unix(1_000, 0)))

			if _, err := direct.Execute(context.Background(), shard); err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if len(uow.requests) != 1 {
				t.Fatalf("Apply() calls = %d, want 1", len(uow.requests))
			}
		})
	}
}

func TestDirectRejectsInvalidApplyResponse(t *testing.T) {
	shard := testShard()
	stored := testResult()
	stored.Shard.Execution.Country = "sg"
	uow := &fakeUnitOfWork{stored: &stored}
	direct := newTestDirect(t, &fakeLeases{lease: testLease(shard)}, executorFunc(func(
		context.Context,
		Shard,
	) (Result, error) {
		return testResult(), nil
	}), uow, newFakeClock(time.Unix(1_000, 0)))

	_, err := direct.Execute(context.Background(), shard)
	if !errors.Is(err, ErrStaleResult) {
		t.Fatalf("Execute() error = %v, want ErrStaleResult", err)
	}
}

type executorFunc func(context.Context, Shard) (Result, error)

func (f executorFunc) Execute(ctx context.Context, shard Shard) (Result, error) {
	return f(ctx, shard)
}

type fakeUnitOfWork struct {
	requests []ApplyRequest
	stored   *Result
	err      error
}

type replayUnitOfWork struct {
	calls             int
	keys              []string
	stored            *ApplyRequest
	loseFirstResponse bool
	lost              bool
}

func (u *replayUnitOfWork) Apply(_ context.Context, request ApplyRequest) (Result, error) {
	u.calls++
	u.keys = append(u.keys, request.IdempotencyKey)
	if u.stored == nil {
		stored := request
		u.stored = &stored
		if u.loseFirstResponse && !u.lost {
			u.lost = true
			return Result{}, errors.New("response lost after commit")
		}
		return request.Result, nil
	}
	if !SameImmutablePayload(*u.stored, request) {
		return Result{}, errors.New("idempotency payload mismatch")
	}
	return u.stored.Result, nil
}

type fencedUnitOfWork struct {
	currentFence int64
	committed    int
}

func (u *fencedUnitOfWork) Apply(_ context.Context, request ApplyRequest) (Result, error) {
	if request.Result.Shard.Execution.LeaseFence != u.currentFence {
		return Result{}, ErrLeaseLost
	}
	u.committed++
	return request.Result, nil
}

func (f *fakeUnitOfWork) Apply(_ context.Context, request ApplyRequest) (Result, error) {
	f.requests = append(f.requests, request)
	if f.err != nil {
		return Result{}, f.err
	}
	if f.stored != nil {
		return *f.stored, nil
	}
	return request.Result, nil
}

type fakeLeases struct {
	mu                 sync.Mutex
	lease              Lease
	renewErr           error
	renewCalls         int
	releaseCalls       int
	renewed            chan struct{}
	acquireCountry     string
	acquireBuild       uuid.UUID
	acquireOwner       string
	acquireCalls       int
	incrementFence     bool
	acquireHasDeadline bool
	acquireExpiry      time.Time
	releaseHasDeadline bool
	releaseContextErr  error
	renewExpiry        time.Time
}

func (f *fakeLeases) Acquire(
	ctx context.Context,
	country string,
	buildID uuid.UUID,
	owner string,
	expiresAt time.Time,
) (Lease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquireCountry = country
	f.acquireBuild = buildID
	f.acquireOwner = owner
	_, f.acquireHasDeadline = ctx.Deadline()
	f.acquireExpiry = expiresAt
	if f.incrementFence && f.acquireCalls > 0 {
		f.lease.Fence++
	}
	f.acquireCalls++
	f.lease.ExpiresAt = expiresAt
	return f.lease, nil
}

func (f *fakeLeases) Renew(_ context.Context, lease Lease, expiresAt time.Time) (Lease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewCalls++
	if f.renewed != nil {
		close(f.renewed)
		f.renewed = nil
	}
	if f.renewErr != nil {
		return Lease{}, f.renewErr
	}
	if f.renewExpiry.IsZero() {
		lease.ExpiresAt = expiresAt
	} else {
		lease.ExpiresAt = f.renewExpiry
	}
	f.lease = lease
	return lease, nil
}

func (f *fakeLeases) Release(ctx context.Context, _ Lease) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	_, f.releaseHasDeadline = ctx.Deadline()
	f.releaseContextErr = ctx.Err()
	return nil
}

type fakeClock struct {
	mu        sync.Mutex
	now       time.Time
	waiters   []fakeWaiter
	scheduled chan struct{}
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock(now time.Time) *fakeClock {
	return &fakeClock{now: now, scheduled: make(chan struct{}, 8)}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1)
	f.waiters = append(f.waiters, fakeWaiter{at: f.now.Add(d), ch: ch})
	f.scheduled <- struct{}{}
	return ch
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	now := f.now
	var pending []fakeWaiter
	for _, waiter := range f.waiters {
		if !waiter.at.After(now) {
			waiter.ch <- now
			close(waiter.ch)
			continue
		}
		pending = append(pending, waiter)
	}
	f.waiters = pending
	f.mu.Unlock()
}

func newTestDirect(t *testing.T, leases SchedulerLease, executor Executor, uow UnitOfWork, clock Clock) *Direct {
	t.Helper()
	direct, err := NewDirect(leases, executor, uow, DirectConfig{
		LeaseDuration: 10 * time.Second,
		RenewInterval: 5 * time.Second,
		Clock: clock,
	})
	if err != nil {
		t.Fatalf("NewDirect() error = %v", err)
	}
	return direct
}

func testLease(shard Shard) Lease {
	return Lease{
		Country: shard.Execution.Country,
		BuildID: shard.Execution.BuildID,
		Owner: shard.Execution.Owner,
		Fence: 7,
	}
}

func waitForSignal(t *testing.T, signal chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for renewal")
	}
}

