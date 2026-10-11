package release

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type ActivationRequest struct {
	Country                  string
	ReleaseID                string
	BuildID                  uuid.UUID
	ExpectedSelectionVersion int64
	ExpectedCurrentReleaseID string
	ActivatedBy              string
}

type ActivationOutcome string

const (
	ActivationApplied    ActivationOutcome = "applied"
	ActivationReconciled ActivationOutcome = "reconciled"
)

type ActivationResult struct {
	Outcome                  ActivationOutcome
	BuildID                  uuid.UUID
	Selection                Selection
	LocalReconciliationError error
}

type PublicationService struct {
	catalog   Catalog
	readiness Readiness
	drainer   PoolDrainer
	now       func() time.Time
}

func NewPublicationService(catalog Catalog, readiness Readiness, drainer PoolDrainer) *PublicationService {
	return &PublicationService{catalog: catalog, readiness: readiness, drainer: drainer, now: time.Now}
}

func (s *PublicationService) Publish(ctx context.Context, country, releaseID string) (Release, error) {
	if !validCountry(country) || releaseID == "" {
		return Release{}, fmt.Errorf("publication country and release ID are required")
	}
	if s.catalog == nil || s.readiness == nil {
		return Release{}, fmt.Errorf("release catalog and readiness are required")
	}
	release, err := s.catalog.Release(ctx, country, releaseID)
	if err != nil {
		return Release{}, fmt.Errorf("load publication candidate: %w", err)
	}
	if release.ID != releaseID {
		return Release{}, fmt.Errorf("release catalog returned ID %q for requested ID %q", release.ID, releaseID)
	}
	if release.Country != country {
		return Release{}, fmt.Errorf("%w: requested %q, release has %q", ErrWrongCountry, country, release.Country)
	}
	if release.BuildID == uuid.Nil {
		return s.recordPublicationFailure(ctx, release, fmt.Errorf("%w: build ID is required", ErrIncompleteManifest))
	}
	if release.SnapshotRef == "" {
		return s.recordPublicationFailure(ctx, release, fmt.Errorf("%w: release identity is required", ErrIncompleteManifest))
	}
	if release.Manifest.Country != release.Country {
		return s.recordPublicationFailure(ctx, release, fmt.Errorf("%w: release country %q, manifest country %q",
			ErrWrongCountry, release.Country, release.Manifest.Country))
	}
	if err := release.Manifest.Validate(); err != nil {
		return s.recordPublicationFailure(ctx, release, err)
	}
	if release.State == StateReady || release.State == StateActive {
		if err := release.Validate(); err != nil {
			return Release{}, fmt.Errorf("validate published release: %w", err)
		}
		return s.checkPublishedReadiness(ctx, release)
	}
	if release.State != StateCandidate && release.State != StateValidated {
		return Release{}, &UnavailableError{Country: country, ReleaseID: releaseID, Reason: UnavailableNotReady}
	}
	if release.State == StateCandidate {
		validatedAt := s.now()
		validated, err := s.catalog.SetReadiness(ctx, ReadinessRecord{
			Country: country, ReleaseID: release.ID, State: StateValidated, ValidatedAt: validatedAt,
		})
		if err != nil {
			return Release{}, fmt.Errorf("record release validation: %w", err)
		}
		if err := validateReadinessTransition(release, validated, StateValidated); err != nil {
			return Release{}, err
		}
		release = validated
	}
	if err := validateReadinessTransition(release, release, StateValidated); err != nil {
		return Release{}, err
	}
	runtime, err := s.readiness.Check(ctx, release)
	if err != nil {
		return s.recordPublicationFailure(ctx, release, err)
	}
	if err := release.Manifest.CompatibleWith(runtime); err != nil {
		return s.recordPublicationFailure(ctx, release, err)
	}
	readyAt := s.now()
	ready, err := s.catalog.SetReadiness(ctx, ReadinessRecord{
		Country: country, ReleaseID: release.ID, State: StateReady,
		ValidatedAt: release.ValidatedAt, ReadyAt: readyAt,
	})
	if err != nil {
		return Release{}, fmt.Errorf("record release readiness: %w", err)
	}
	if err := validateReadinessTransition(release, ready, StateReady); err != nil {
		return Release{}, err
	}
	return ready, nil
}

func (s *PublicationService) checkPublishedReadiness(ctx context.Context, release Release) (Release, error) {
	runtime, err := s.readiness.Check(ctx, release)
	if err != nil {
		return Release{}, fmt.Errorf("check published release readiness: %w", err)
	}
	if err := release.Manifest.CompatibleWith(runtime); err != nil {
		return Release{}, fmt.Errorf("check published release compatibility: %w", err)
	}
	return release, nil
}

func validateReadinessTransition(before, after Release, state State) error {
	if after.ID != before.ID {
		return fmt.Errorf("release catalog returned ID %q after updating ID %q", after.ID, before.ID)
	}
	if after.Country != before.Country {
		return fmt.Errorf("%w: updated release has %q", ErrWrongCountry, after.Country)
	}
	if after.BuildID != before.BuildID {
		return fmt.Errorf("%w: readiness transition returned build %q for build %q",
			ErrWrongBuild, after.BuildID, before.BuildID)
	}
	if after.SnapshotRef != before.SnapshotRef || after.Manifest != before.Manifest {
		return fmt.Errorf("release catalog changed immutable release identity during readiness transition")
	}
	if after.State != state || after.ValidatedAt.IsZero() || after.ValidationError != "" {
		return fmt.Errorf("release catalog returned invalid %q readiness state", state)
	}
	if state == StateValidated && !after.ReadyAt.IsZero() {
		return fmt.Errorf("validated release has a ready time")
	}
	if state == StateReady && after.ReadyAt.IsZero() {
		return fmt.Errorf("ready release has no ready time")
	}
	return nil
}

func (s *PublicationService) recordPublicationFailure(ctx context.Context, release Release, cause error) (Release, error) {
	if release.State != StateCandidate && release.State != StateValidated {
		return Release{}, fmt.Errorf("publish release: %w", cause)
	}
	_, recordErr := s.catalog.SetReadiness(ctx, ReadinessRecord{
		Country: release.Country, ReleaseID: release.ID, State: StateFailed,
		ValidatedAt: s.now(), ValidationError: cause.Error(),
	})
	if recordErr != nil {
		return Release{}, fmt.Errorf("publish release: %w; record failure: %v", cause, recordErr)
	}
	return Release{}, fmt.Errorf("publish release: %w", cause)
}

func (s *PublicationService) Activate(ctx context.Context, request ActivationRequest) (ActivationResult, error) {
	if !validCountry(request.Country) || request.ReleaseID == "" || request.BuildID == uuid.Nil || request.ActivatedBy == "" {
		return ActivationResult{}, fmt.Errorf("activation country, release ID, build ID, and actor are required")
	}
	if request.ExpectedSelectionVersion < 0 {
		return ActivationResult{}, fmt.Errorf("expected selection version must not be negative")
	}
	if s.catalog == nil {
		return ActivationResult{}, fmt.Errorf("release catalog is not configured")
	}
	release, err := s.catalog.Release(ctx, request.Country, request.ReleaseID)
	if err != nil {
		return ActivationResult{}, fmt.Errorf("load activation candidate: %w", err)
	}
	if release.ID != request.ReleaseID {
		return ActivationResult{}, fmt.Errorf("release catalog returned ID %q for requested ID %q", release.ID, request.ReleaseID)
	}
	if release.BuildID != request.BuildID {
		return ActivationResult{}, fmt.Errorf("%w: requested %q, release has %q",
			ErrWrongBuild, request.BuildID, release.BuildID)
	}
	if release.Country != request.Country {
		return ActivationResult{}, fmt.Errorf("%w: requested %q, release has %q", ErrWrongCountry, request.Country, release.Country)
	}
	if err := release.Validate(); err != nil {
		return ActivationResult{}, fmt.Errorf("validate activation candidate: %w", err)
	}
	before, err := s.catalog.Selection(ctx, request.Country)
	if err != nil {
		return ActivationResult{}, fmt.Errorf("load release selection: %w", err)
	}
	if before.Country != request.Country {
		return ActivationResult{}, fmt.Errorf("%w: selection has %q", ErrWrongCountry, before.Country)
	}
	if activationReplayed(before, request) {
		return s.finishLocal(request.Country, release.BuildID, ActivationReconciled, before), nil
	}
	if before.Version != request.ExpectedSelectionVersion {
		return ActivationResult{}, fmt.Errorf("%w: expected version %d, found %d", ErrSelectionConflict, request.ExpectedSelectionVersion, before.Version)
	}
	if before.CurrentReleaseID != request.ExpectedCurrentReleaseID {
		return ActivationResult{}, fmt.Errorf("%w: expected current release %q, found %q", ErrSelectionConflict,
			request.ExpectedCurrentReleaseID, before.CurrentReleaseID)
	}
	if s.readiness == nil {
		return ActivationResult{}, fmt.Errorf("release readiness is not configured")
	}
	runtime, err := s.readiness.Check(ctx, release)
	if err != nil {
		return ActivationResult{}, fmt.Errorf("check release readiness: %w", err)
	}
	if err := release.Manifest.CompatibleWith(runtime); err != nil {
		return ActivationResult{}, fmt.Errorf("check release compatibility: %w", err)
	}
	change := ActivationChange{
		Country: request.Country, ReleaseID: request.ReleaseID,
		ExpectedVersion: request.ExpectedSelectionVersion,
		ActivatedBy:     request.ActivatedBy, ActivatedAt: s.now(),
	}
	selection, casErr := s.catalog.CompareAndSet(ctx, change)
	result := ActivationResult{Outcome: ActivationApplied, Selection: selection}
	if casErr != nil || !activationMatches(selection, change, before) {
		selection, err = s.reconcile(ctx, change, before)
		if err != nil {
			if errors.Is(casErr, ErrSelectionConflict) {
				return ActivationResult{}, casErr
			}
			if casErr == nil {
				return ActivationResult{}, err
			}
			return ActivationResult{}, fmt.Errorf("activate release: %w; reconcile: %v", casErr, err)
		}
		result.Outcome = ActivationReconciled
		result.Selection = selection
	}
	return s.finishLocal(request.Country, release.BuildID, result.Outcome, result.Selection), nil
}

func (s *PublicationService) finishLocal(country string, buildID uuid.UUID, outcome ActivationOutcome, selection Selection) ActivationResult {
	result := ActivationResult{Outcome: outcome, BuildID: buildID, Selection: selection}
	// Draining is process-local. Other serving hosts observe the durable
	// selection independently and drain their own old bundle.
	if s.drainer != nil && selection.PreviousReleaseID != "" {
		result.LocalReconciliationError = s.drainer.Drain(country, selection.PreviousReleaseID)
	}
	return result
}

func activationReplayed(selection Selection, request ActivationRequest) bool {
	return selection.Version == request.ExpectedSelectionVersion+1 &&
		selection.CurrentReleaseID == request.ReleaseID &&
		selection.PreviousReleaseID == request.ExpectedCurrentReleaseID
}

func activationMatches(selection Selection, change ActivationChange, before Selection) bool {
	return selection.Country == change.Country &&
		selection.Version == change.ExpectedVersion+1 &&
		selection.CurrentReleaseID == change.ReleaseID &&
		selection.PreviousReleaseID == before.CurrentReleaseID
}

func (s *PublicationService) reconcile(ctx context.Context, change ActivationChange, before Selection) (Selection, error) {
	selection, err := s.catalog.Selection(ctx, change.Country)
	if err != nil {
		return Selection{}, fmt.Errorf("reload release selection: %w", err)
	}
	if selection.Country != change.Country {
		return Selection{}, fmt.Errorf("%w: selection has %q", ErrWrongCountry, selection.Country)
	}
	if activationMatches(selection, change, before) {
		return selection, nil
	}
	return Selection{}, fmt.Errorf("%w: current release %q at version %d", ErrSelectionConflict,
		selection.CurrentReleaseID, selection.Version)
}
