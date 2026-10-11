package release

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"danny.vn/banhmi/internal/evidence/api"
)

func TestResolverRejectsWrongCountry(t *testing.T) {
	catalog := newFakeCatalog()
	catalog.releases["vn-1"] = validRelease("vn-1")
	resolver := NewResolver("my", catalog, &fakePins{}, NewPoolSet(fakeBundleFactory{}), time.Minute)

	_, err := resolver.Pin(context.Background(), "vn", "vn-1")
	if !errors.Is(err, ErrWrongCountry) {
		t.Fatalf("Pin() error = %v, want ErrWrongCountry", err)
	}
}

func TestResolverReportsExpiredRelease(t *testing.T) {
	catalog := newFakeCatalog()
	catalog.releases["old"] = validRelease("old")
	pins := &fakePins{acquireErr: &UnavailableError{Country: "vn", ReleaseID: "old", Reason: UnavailableExpired}}
	resolver := NewResolver("vn", catalog, pins, NewPoolSet(fakeBundleFactory{}), time.Minute)

	_, err := resolver.Pin(context.Background(), "vn", "old")
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) || unavailable.Reason != UnavailableExpired {
		t.Fatalf("Pin() error = %v, want expired UnavailableError", err)
	}
}

func TestResolverNeverPassesRequestedReleaseAsSnapshotReference(t *testing.T) {
	catalog := newFakeCatalog()
	factory := &recordingBundleFactory{}
	resolver := NewResolver("vn", catalog, &fakePins{}, NewPoolSet(factory), time.Minute)

	_, err := resolver.Pin(context.Background(), "vn", "postgres://attacker.invalid/corpus")
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("Pin() error = %v, want UnavailableError", err)
	}
	if factory.openCalls != 0 {
		t.Fatalf("bundle open calls = %d, want 0", factory.openCalls)
	}
}

func TestPinCloseIsIdempotentAndLastCloseDrainsPool(t *testing.T) {
	catalog := newFakeCatalog()
	catalog.releases["r1"] = validRelease("r1")
	pins := &fakePins{}
	bundle := &fakeBundle{ops: fakeOperations{}}
	pools := NewPoolSet(fakeBundleFactory{bundle: bundle})
	resolver := NewResolver("vn", catalog, pins, pools, time.Minute)

	first, err := resolver.Pin(context.Background(), "vn", "r1")
	if err != nil {
		t.Fatalf("first Pin() error = %v", err)
	}
	second, err := resolver.Pin(context.Background(), "vn", "r1")
	if err != nil {
		t.Fatalf("second Pin() error = %v", err)
	}
	if err := pools.Drain("vn", "r1"); err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if bundle.closeCalls != 0 {
		t.Fatalf("bundle closed with live pins")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("second first.Close() error = %v", err)
	}
	if bundle.closeCalls != 0 {
		t.Fatalf("bundle closed before final pin")
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if bundle.closeCalls != 1 {
		t.Fatalf("bundle close calls = %d, want 1", bundle.closeCalls)
	}
	if pins.releaseCalls != 2 {
		t.Fatalf("durable release calls = %d, want 2", pins.releaseCalls)
	}
}

func TestPoolDrainRetriesFailedBundleClose(t *testing.T) {
	catalog := newFakeCatalog()
	catalog.releases["r1"] = validRelease("r1")
	pins := &fakePins{}
	closeErr := errors.New("close failed")
	bundle := &fakeBundle{ops: fakeOperations{}, closeErrors: []error{closeErr, nil}}
	pools := NewPoolSet(fakeBundleFactory{bundle: bundle})
	resolver := NewResolver("vn", catalog, pins, pools, time.Minute)
	pin, err := resolver.Pin(context.Background(), "vn", "r1")
	if err != nil {
		t.Fatalf("Pin() error = %v", err)
	}
	if err := pools.Drain("vn", "r1"); err != nil {
		t.Fatalf("Drain() with live pin error = %v", err)
	}
	if err := pin.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("Close() error = %v, want close failure", err)
	}
	if err := pools.Drain("vn", "r1"); err != nil {
		t.Fatalf("retry Drain() error = %v", err)
	}
	if bundle.closeCalls != 2 {
		t.Fatalf("bundle close calls = %d, want 2", bundle.closeCalls)
	}
}

func TestNamedAcquireRetriesFailedFinalDrain(t *testing.T) {
	catalog := newFakeCatalog()
	catalog.releases["r1"] = validRelease("r1")
	pins := &fakePins{}
	closeErr := errors.New("close failed")
	oldBundle := &fakeBundle{ops: fakeOperations{}, closeErrors: []error{closeErr, nil}}
	newBundle := &fakeBundle{ops: fakeOperations{}}
	factory := &sequenceBundleFactory{bundles: []Bundle{oldBundle, newBundle}}
	pools := NewPoolSet(factory)
	resolver := NewResolver("vn", catalog, pins, pools, time.Minute)

	first, err := resolver.Pin(context.Background(), "vn", "r1")
	if err != nil {
		t.Fatalf("first Pin() error = %v", err)
	}
	if err := pools.Drain("vn", "r1"); err != nil {
		t.Fatalf("Drain() with live pin error = %v", err)
	}
	if err := first.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("first Close() error = %v, want close failure", err)
	}

	second, err := resolver.Pin(context.Background(), "vn", "r1")
	if err != nil {
		t.Fatalf("second Pin() error = %v", err)
	}
	if oldBundle.closeCalls != 2 || factory.opens != 2 {
		t.Fatalf("old close calls = %d, bundle opens = %d", oldBundle.closeCalls, factory.opens)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestDurablePinLossCancelsEvidenceOperation(t *testing.T) {
	catalog := newFakeCatalog()
	catalog.releases["r1"] = validRelease("r1")
	pins := &fakePins{}
	bundle := &fakeBundle{ops: blockingOperations{}}
	resolver := NewResolver("vn", catalog, pins, NewPoolSet(fakeBundleFactory{bundle: bundle}), time.Minute)
	pin, err := resolver.Pin(context.Background(), "vn", "r1")
	if err != nil {
		t.Fatalf("Pin() error = %v", err)
	}

	result := make(chan error, 1)
	go func() {
		_, callErr := pin.Operations().Search(context.Background(), api.SearchInput{Query: "payments"})
		result <- callErr
	}()
	pins.last.cancel(ErrPinLost)
	if err := <-result; !errors.Is(err, ErrPinLost) {
		t.Fatalf("Search() error = %v, want ErrPinLost", err)
	}
}

func TestLostDurablePinBlocksNewEvidenceOperation(t *testing.T) {
	catalog := newFakeCatalog()
	catalog.releases["r1"] = validRelease("r1")
	pins := &fakePins{}
	bundle := &fakeBundle{ops: fakeOperations{}}
	resolver := NewResolver("vn", catalog, pins, NewPoolSet(fakeBundleFactory{bundle: bundle}), time.Minute)
	pin, err := resolver.Pin(context.Background(), "vn", "r1")
	if err != nil {
		t.Fatalf("Pin() error = %v", err)
	}
	pins.last.cancel(ErrPinLost)

	_, err = pin.Operations().Search(context.Background(), api.SearchInput{Query: "payments"})
	if !errors.Is(err, ErrPinLost) {
		t.Fatalf("Search() error = %v, want ErrPinLost", err)
	}
}

func TestRetentionReturnsOnlyDurablyEligibleReleases(t *testing.T) {
	retired := validRelease("retired")
	retired.State = StateRetired
	now := time.Now()
	store := fakeRetentionStore{claims: []RetentionClaim{{
		Release: retired, ClaimKey: "claim-1", Owner: "collector", Fence: 1,
		ClaimedAt: now, ExpiresAt: now.Add(time.Minute),
	}}}
	got, err := NewRetentionService("vn", store, time.Minute).Claim(
		context.Background(), "collector", "batch-1", 5, now)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if len(got) != 1 || got[0].Release.ID != "retired" {
		t.Fatalf("Claim() = %+v", got)
	}
}

func TestRequestScopedHoldsRequireExpiry(t *testing.T) {
	for _, kind := range []HoldKind{HoldNamed, HoldInFlight} {
		hold := Hold{Country: "vn", ReleaseID: "r1", Kind: kind, Key: "request"}
		if err := hold.Validate(); err == nil {
			t.Fatalf("Hold{%q}.Validate() error = nil, want expiry error", kind)
		}
	}
}

type fakePins struct {
	mu           sync.Mutex
	next         int
	acquireErr   error
	releaseCalls int
	last         *fakeDurableLease
}

func (f *fakePins) Acquire(_ context.Context, country, releaseID string, ttl time.Duration) (DurableLease, error) {
	if f.acquireErr != nil {
		return nil, f.acquireErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	leaseContext, cancel := context.WithCancelCause(context.Background())
	lease := &fakeDurableLease{
		owner:   f,
		pin:     DurablePin{Country: country, ReleaseID: releaseID, Key: string(rune(f.next)), ExpiresAt: time.Now().Add(ttl)},
		context: leaseContext, cancel: cancel,
	}
	f.last = lease
	return lease, nil
}

type fakeDurableLease struct {
	owner   *fakePins
	pin     DurablePin
	once    sync.Once
	context context.Context
	cancel  context.CancelCauseFunc
}

func (f *fakeDurableLease) Pin() DurablePin          { return f.pin }
func (f *fakeDurableLease) Context() context.Context { return f.context }
func (f *fakeDurableLease) Close() error {
	f.once.Do(func() {
		f.cancel(context.Canceled)
		f.owner.mu.Lock()
		defer f.owner.mu.Unlock()
		f.owner.releaseCalls++
	})
	return nil
}

type fakeBundleFactory struct{ bundle *fakeBundle }

func (f fakeBundleFactory) Open(context.Context, Release) (Bundle, error) {
	if f.bundle != nil {
		return f.bundle, nil
	}
	return &fakeBundle{ops: fakeOperations{}}, nil
}

type recordingBundleFactory struct{ openCalls int }

func (f *recordingBundleFactory) Open(context.Context, Release) (Bundle, error) {
	f.openCalls++
	return &fakeBundle{ops: fakeOperations{}}, nil
}

type sequenceBundleFactory struct {
	bundles []Bundle
	opens   int
}

func (f *sequenceBundleFactory) Open(context.Context, Release) (Bundle, error) {
	if f.opens >= len(f.bundles) {
		return nil, errors.New("no bundle configured")
	}
	bundle := f.bundles[f.opens]
	f.opens++
	return bundle, nil
}

type fakeBundle struct {
	ops         api.Operations
	closeCalls  int
	closeErrors []error
}

func (f *fakeBundle) Operations() api.Operations { return f.ops }
func (f *fakeBundle) Close() error {
	f.closeCalls++
	if len(f.closeErrors) > 0 {
		err := f.closeErrors[0]
		f.closeErrors = f.closeErrors[1:]
		return err
	}
	return nil
}

type fakeRetentionStore struct{ claims []RetentionClaim }

func (f fakeRetentionStore) ClaimEligible(context.Context, RetentionRequest) ([]RetentionClaim, error) {
	return f.claims, nil
}
func (fakeRetentionStore) RenewClaim(_ context.Context, claim RetentionClaim, now time.Time, ttl time.Duration) (RetentionClaim, error) {
	claim.ExpiresAt = now.Add(ttl)
	return claim, nil
}
func (fakeRetentionStore) ReleaseClaim(context.Context, RetentionClaim) error             { return nil }
func (fakeRetentionStore) FinalizeClaim(context.Context, RetentionClaim, time.Time) error { return nil }

type fakeOperations struct{}

type blockingOperations struct{ fakeOperations }

func (blockingOperations) Search(ctx context.Context, _ api.SearchInput) (api.SearchOutput, error) {
	<-ctx.Done()
	return api.SearchOutput{}, context.Cause(ctx)
}

func (fakeOperations) Guide(context.Context, api.GuideInput) (api.GuideOutput, error) {
	return api.GuideOutput{}, nil
}
func (fakeOperations) CorpusStatus(context.Context, api.CorpusStatusInput) (api.CorpusStatusOutput, error) {
	return api.CorpusStatusOutput{}, nil
}
func (fakeOperations) QualityGaps(context.Context, api.QualityGapsInput) (api.QualityGapsOutput, error) {
	return api.QualityGapsOutput{}, nil
}
func (fakeOperations) Search(context.Context, api.SearchInput) (api.SearchOutput, error) {
	return api.SearchOutput{}, nil
}
func (fakeOperations) Document(context.Context, api.DocumentInput) (api.DocumentOutput, error) {
	return api.DocumentOutput{}, nil
}
