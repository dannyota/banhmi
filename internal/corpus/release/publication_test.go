package release

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

var (
	testBuildID  = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	otherBuildID = uuid.MustParse("22222222-2222-4222-8222-222222222222")
)

func TestActivateReadinessFailureLeavesSelectionUnchanged(t *testing.T) {
	catalog := newFakeCatalog()
	catalog.selection = Selection{Country: "vn", Version: 4, CurrentReleaseID: "old"}
	catalog.releases["candidate"] = validRelease("candidate")
	readinessErr := errors.New("bundle is not ready")
	service := NewPublicationService(catalog, fakeReadiness{err: readinessErr}, nil)

	_, err := service.Activate(context.Background(), ActivationRequest{
		Country: "vn", ReleaseID: "candidate", BuildID: testBuildID, ExpectedSelectionVersion: 4,
		ExpectedCurrentReleaseID: "old", ActivatedBy: "operator",
	})
	if !errors.Is(err, readinessErr) {
		t.Fatalf("Activate() error = %v, want readiness error", err)
	}
	if catalog.compareAndSetCalls != 0 {
		t.Fatalf("compare-and-set calls = %d, want 0", catalog.compareAndSetCalls)
	}
	if catalog.selection.CurrentReleaseID != "old" {
		t.Fatalf("current release = %q, want old", catalog.selection.CurrentReleaseID)
	}
}

func TestPublishReadinessFailureRecordsFailureWithoutChangingSelection(t *testing.T) {
	catalog := newFakeCatalog()
	catalog.selection = Selection{Country: "vn", Version: 4, CurrentReleaseID: "old"}
	candidate := validRelease("candidate")
	candidate.State = StateCandidate
	candidate.ValidatedAt = time.Time{}
	candidate.ReadyAt = time.Time{}
	catalog.releases["candidate"] = candidate
	readinessErr := errors.New("bundle is not ready")
	service := NewPublicationService(catalog, fakeReadiness{err: readinessErr}, nil)

	_, err := service.Publish(context.Background(), "vn", "candidate")
	if !errors.Is(err, readinessErr) {
		t.Fatalf("Publish() error = %v, want readiness error", err)
	}
	if catalog.readiness.State != StateFailed || catalog.readiness.ValidationError == "" {
		t.Fatalf("readiness record = %+v", catalog.readiness)
	}
	if catalog.selection.CurrentReleaseID != "old" || catalog.compareAndSetCalls != 0 {
		t.Fatalf("selection changed during failed publication: %+v", catalog.selection)
	}
}

func TestPublishRecordsValidatedThenReady(t *testing.T) {
	catalog := newFakeCatalog()
	candidate := validRelease("candidate")
	candidate.State = StateCandidate
	candidate.ValidatedAt = time.Time{}
	candidate.ReadyAt = time.Time{}
	catalog.releases["candidate"] = candidate
	service := NewPublicationService(catalog, fakeReadiness{}, nil)

	ready, err := service.Publish(context.Background(), "vn", "candidate")
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if ready.State != StateReady {
		t.Fatalf("published state = %q, want ready", ready.State)
	}
	if len(catalog.readinessRecords) != 2 ||
		catalog.readinessRecords[0].State != StateValidated ||
		catalog.readinessRecords[1].State != StateReady {
		t.Fatalf("readiness transitions = %+v", catalog.readinessRecords)
	}
}

func TestPublishRejectsReadinessTransitionBuildMutation(t *testing.T) {
	catalog := newFakeCatalog()
	candidate := validRelease("candidate")
	candidate.State = StateCandidate
	candidate.ValidatedAt = time.Time{}
	candidate.ReadyAt = time.Time{}
	catalog.releases["candidate"] = candidate
	catalog.readinessBuildID = otherBuildID
	service := NewPublicationService(catalog, fakeReadiness{}, nil)

	_, err := service.Publish(context.Background(), "vn", "candidate")
	if !errors.Is(err, ErrWrongBuild) {
		t.Fatalf("Publish() error = %v, want ErrWrongBuild", err)
	}
}

func TestActivateImageMismatchLeavesSelectionUnchanged(t *testing.T) {
	catalog := newFakeCatalog()
	catalog.selection = Selection{Country: "vn", Version: 4, CurrentReleaseID: "old"}
	catalog.releases["candidate"] = validRelease("candidate")
	service := NewPublicationService(catalog, incompatibleReadiness{}, nil)

	_, err := service.Activate(context.Background(), ActivationRequest{
		Country: "vn", ReleaseID: "candidate", BuildID: testBuildID, ExpectedSelectionVersion: 4,
		ExpectedCurrentReleaseID: "old", ActivatedBy: "operator",
	})
	if !errors.Is(err, ErrIncompatibleRuntime) {
		t.Fatalf("Activate() error = %v, want ErrIncompatibleRuntime", err)
	}
	if catalog.compareAndSetCalls != 0 || catalog.selection.CurrentReleaseID != "old" {
		t.Fatalf("selection changed after incompatible readiness: %+v", catalog.selection)
	}
}

func TestActivateReportsCompareAndSetRace(t *testing.T) {
	catalog := newFakeCatalog()
	catalog.selection = Selection{Country: "vn", Version: 4, CurrentReleaseID: "old"}
	catalog.releases["candidate"] = validRelease("candidate")
	catalog.casErr = ErrSelectionConflict
	service := NewPublicationService(catalog, fakeReadiness{}, nil)

	_, err := service.Activate(context.Background(), ActivationRequest{
		Country: "vn", ReleaseID: "candidate", BuildID: testBuildID, ExpectedSelectionVersion: 4,
		ExpectedCurrentReleaseID: "old", ActivatedBy: "operator",
	})
	if !errors.Is(err, ErrSelectionConflict) {
		t.Fatalf("Activate() error = %v, want ErrSelectionConflict", err)
	}
	if catalog.compareAndSetCalls != 1 {
		t.Fatalf("compare-and-set calls = %d, want 1", catalog.compareAndSetCalls)
	}
}

func TestActivateRejectsWrongBuildBeforeReadinessOrCompareAndSet(t *testing.T) {
	catalog := newFakeCatalog()
	catalog.selection = Selection{Country: "vn", Version: 4, CurrentReleaseID: "old"}
	catalog.releases["candidate"] = validRelease("candidate")
	readiness := &countingReadiness{}
	service := NewPublicationService(catalog, readiness, nil)

	_, err := service.Activate(context.Background(), ActivationRequest{
		Country: "vn", ReleaseID: "candidate", BuildID: otherBuildID, ExpectedSelectionVersion: 4,
		ExpectedCurrentReleaseID: "old", ActivatedBy: "operator",
	})
	if !errors.Is(err, ErrWrongBuild) {
		t.Fatalf("Activate() error = %v, want ErrWrongBuild", err)
	}
	if readiness.calls != 0 || catalog.compareAndSetCalls != 0 {
		t.Fatalf("readiness calls = %d, compare-and-set calls = %d", readiness.calls, catalog.compareAndSetCalls)
	}
}

func TestActivateRejectsRetentionClaimedTarget(t *testing.T) {
	catalog := newFakeCatalog()
	catalog.selection = Selection{Country: "vn", Version: 4, CurrentReleaseID: "old"}
	catalog.releases["candidate"] = validRelease("candidate")
	catalog.casErr = ErrRetentionConflict
	service := NewPublicationService(catalog, fakeReadiness{}, nil)

	_, err := service.Activate(context.Background(), ActivationRequest{
		Country: "vn", ReleaseID: "candidate", BuildID: testBuildID, ExpectedSelectionVersion: 4,
		ExpectedCurrentReleaseID: "old", ActivatedBy: "operator",
	})
	if !errors.Is(err, ErrRetentionConflict) {
		t.Fatalf("Activate() error = %v, want ErrRetentionConflict", err)
	}
	if catalog.selection.CurrentReleaseID != "old" {
		t.Fatalf("selection changed to %q", catalog.selection.CurrentReleaseID)
	}
}

func TestActivateReconcilesLostCompareAndSetResponse(t *testing.T) {
	catalog := newFakeCatalog()
	catalog.selection = Selection{Country: "vn", Version: 4, CurrentReleaseID: "old"}
	catalog.releases["candidate"] = validRelease("candidate")
	catalog.applyThenError = errors.New("response lost")
	service := NewPublicationService(catalog, fakeReadiness{}, nil)

	result, err := service.Activate(context.Background(), ActivationRequest{
		Country: "vn", ReleaseID: "candidate", BuildID: testBuildID, ExpectedSelectionVersion: 4,
		ExpectedCurrentReleaseID: "old", ActivatedBy: "operator",
	})
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if result.Outcome != ActivationReconciled {
		t.Fatalf("outcome = %q, want %q", result.Outcome, ActivationReconciled)
	}
	if result.BuildID != testBuildID {
		t.Fatalf("build ID = %q, want %q", result.BuildID, testBuildID)
	}
	if result.Selection.PreviousReleaseID != "old" || result.Selection.CurrentReleaseID != "candidate" {
		t.Fatalf("selection = %+v", result.Selection)
	}
}

func TestActivateReconcilesReplayAfterCommittedActivation(t *testing.T) {
	catalog := newFakeCatalog()
	catalog.selection = Selection{
		Country: "vn", Version: 5, CurrentReleaseID: "candidate", PreviousReleaseID: "old",
	}
	catalog.releases["candidate"] = validRelease("candidate")
	service := NewPublicationService(catalog, fakeReadiness{}, nil)

	result, err := service.Activate(context.Background(), ActivationRequest{
		Country: "vn", ReleaseID: "candidate", BuildID: testBuildID, ExpectedSelectionVersion: 4,
		ExpectedCurrentReleaseID: "old", ActivatedBy: "operator",
	})
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if result.Outcome != ActivationReconciled || catalog.compareAndSetCalls != 0 {
		t.Fatalf("result = %+v, compare-and-set calls = %d", result, catalog.compareAndSetCalls)
	}
	if result.BuildID != testBuildID {
		t.Fatalf("build ID = %q, want %q", result.BuildID, testBuildID)
	}
}

type fakeReadiness struct{ err error }

func (f fakeReadiness) Check(_ context.Context, release Release) (RuntimeIdentity, error) {
	return RuntimeIdentity{
		Bundle: release.Manifest.Identity(),
		Coverage: release.Manifest.Coverage(),
		ServerImageDigest: release.Manifest.CompatibleServerImageDigest,
		EmbedderImageDigest: release.Manifest.CompatibleEmbedderImageDigest,
	}, f.err
}

type incompatibleReadiness struct{}

func (incompatibleReadiness) Check(_ context.Context, release Release) (RuntimeIdentity, error) {
	return RuntimeIdentity{
		Bundle: release.Manifest.Identity(),
		Coverage: release.Manifest.Coverage(),
		ServerImageDigest: "sha256:other-server",
		EmbedderImageDigest: release.Manifest.CompatibleEmbedderImageDigest,
	}, nil
}

type countingReadiness struct{ calls int }

func (r *countingReadiness) Check(_ context.Context, release Release) (RuntimeIdentity, error) {
	r.calls++
	return RuntimeIdentity{
		Bundle: release.Manifest.Identity(), Coverage: release.Manifest.Coverage(),
		ServerImageDigest: release.Manifest.CompatibleServerImageDigest,
		EmbedderImageDigest: release.Manifest.CompatibleEmbedderImageDigest,
	}, nil
}

type fakeCatalog struct {
	releases           map[string]Release
	selection          Selection
	casErr             error
	applyThenError     error
	compareAndSetCalls int
	readiness          ReadinessRecord
	readinessRecords   []ReadinessRecord
	readinessBuildID   uuid.UUID
}

func (f *fakeCatalog) SetReadiness(_ context.Context, record ReadinessRecord) (Release, error) {
	f.readiness = record
	f.readinessRecords = append(f.readinessRecords, record)
	release := f.releases[record.ReleaseID]
	release.State = record.State
	release.ValidatedAt = record.ValidatedAt
	release.ReadyAt = record.ReadyAt
	release.ValidationError = record.ValidationError
	if f.readinessBuildID != uuid.Nil {
		release.BuildID = f.readinessBuildID
	}
	f.releases[record.ReleaseID] = release
	return release, nil
}

func newFakeCatalog() *fakeCatalog { return &fakeCatalog{releases: make(map[string]Release)} }

func (f *fakeCatalog) Release(_ context.Context, country, releaseID string) (Release, error) {
	r, ok := f.releases[releaseID]
	if !ok {
		return Release{}, &UnavailableError{Country: country, ReleaseID: releaseID, Reason: UnavailableUnknown}
	}
	return r, nil
}

func (f *fakeCatalog) Selection(context.Context, string) (Selection, error) { return f.selection, nil }

func (f *fakeCatalog) CompareAndSet(_ context.Context, change ActivationChange) (Selection, error) {
	f.compareAndSetCalls++
	if f.casErr != nil {
		return Selection{}, f.casErr
	}
	if f.selection.Version != change.ExpectedVersion {
		return Selection{}, ErrSelectionConflict
	}
	f.selection = Selection{
		Country: f.selection.Country, Version: f.selection.Version + 1,
		CurrentReleaseID: change.ReleaseID, PreviousReleaseID: f.selection.CurrentReleaseID,
	}
	if f.applyThenError != nil {
		return Selection{}, f.applyThenError
	}
	return f.selection, nil
}

func validRelease(id string) Release {
	return Release{
		ID: id, BuildID: testBuildID, Country: "vn", SnapshotRef: "catalog:snapshot:" + id,
		State: StateReady, Manifest: validManifest(),
		ValidatedAt: time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC),
		ReadyAt: time.Date(2026, 10, 11, 2, 0, 0, 0, time.UTC),
	}
}
