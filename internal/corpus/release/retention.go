package release

import (
	"context"
	"fmt"
	"time"
)

type RetentionService struct {
	country  string
	store    RetentionStore
	claimTTL time.Duration
}

func NewRetentionService(country string, store RetentionStore, claimTTL time.Duration) *RetentionService {
	return &RetentionService{country: country, store: store, claimTTL: claimTTL}
}

func (s *RetentionService) Claim(ctx context.Context, owner, batchKey string, limit int, now time.Time) ([]RetentionClaim, error) {
	if s.country == "" || s.store == nil {
		return nil, fmt.Errorf("retention service is not configured")
	}
	if limit <= 0 {
		return nil, fmt.Errorf("retention limit must be positive")
	}
	if owner == "" || batchKey == "" {
		return nil, fmt.Errorf("retention owner and batch key are required")
	}
	if s.claimTTL <= 0 {
		return nil, fmt.Errorf("retention claim TTL must be positive")
	}
	request := RetentionRequest{
		Country: s.country, Owner: owner, BatchKey: batchKey,
		Limit: limit, Now: now, TTL: s.claimTTL,
	}
	claims, err := s.store.ClaimEligible(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("claim releases eligible for retention: %w", err)
	}
	for _, claim := range claims {
		release := claim.Release
		if release.Country != s.country {
			return nil, fmt.Errorf("%w: retention candidate %q has %q", ErrWrongCountry, release.ID, release.Country)
		}
		if release.State != StateRetired && release.State != StateFailed {
			return nil, fmt.Errorf("retention candidate %q has protected state %q", release.ID, release.State)
		}
		if claim.ClaimKey == "" || claim.Owner != owner || claim.Fence <= 0 ||
			claim.ClaimedAt.IsZero() || !claim.ExpiresAt.After(claim.ClaimedAt) ||
			claim.ExpiresAt.Before(now.Add(s.claimTTL/2)) {
			return nil, fmt.Errorf("retention candidate %q has an invalid claim", release.ID)
		}
	}
	return claims, nil
}
