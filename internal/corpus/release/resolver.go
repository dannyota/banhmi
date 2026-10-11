package release

import (
	"context"
	"fmt"
	"sync"
	"time"

	"danny.vn/banhmi/internal/evidence/api"
)

var _ api.ReleaseResolver = (*Resolver)(nil)

type Resolver struct {
	country string
	catalog Catalog
	pins    PinStore
	pools   *PoolSet
	pinTTL  time.Duration
}

func NewResolver(country string, catalog Catalog, pins PinStore, pools *PoolSet, pinTTL time.Duration) *Resolver {
	return &Resolver{country: country, catalog: catalog, pins: pins, pools: pools, pinTTL: pinTTL}
}

func (r *Resolver) Pin(ctx context.Context, country, requested string) (api.ReleasePin, error) {
	if country != r.country {
		return nil, fmt.Errorf("%w: resolver has %q, request has %q", ErrWrongCountry, r.country, country)
	}
	if r.catalog == nil || r.pins == nil || r.pools == nil || r.pinTTL <= 0 {
		return nil, fmt.Errorf("release resolver is not configured")
	}
	releaseID := requested
	if releaseID == "" {
		selection, err := r.catalog.Selection(ctx, country)
		if err != nil {
			return nil, fmt.Errorf("resolve active release: %w", err)
		}
		if selection.Country != country {
			return nil, fmt.Errorf("%w: selection has %q", ErrWrongCountry, selection.Country)
		}
		releaseID = selection.CurrentReleaseID
	}
	if releaseID == "" {
		return nil, &UnavailableError{Country: country, ReleaseID: releaseID, Reason: UnavailableUnknown}
	}
	release, err := r.catalog.Release(ctx, country, releaseID)
	if err != nil {
		return nil, fmt.Errorf("resolve release: %w", err)
	}
	if release.ID != releaseID {
		return nil, fmt.Errorf("release catalog returned ID %q for requested ID %q", release.ID, releaseID)
	}
	if release.Country != country {
		return nil, fmt.Errorf("%w: requested %q, release has %q", ErrWrongCountry, country, release.Country)
	}
	if err := release.Validate(); err != nil {
		return nil, fmt.Errorf("validate release: %w", err)
	}
	durable, err := r.pins.Acquire(ctx, country, release.ID, r.pinTTL)
	if err != nil {
		return nil, fmt.Errorf("acquire durable release pin: %w", err)
	}
	if durable == nil {
		return nil, fmt.Errorf("acquire durable release pin: empty lease")
	}
	if durable.Context() == nil {
		closeErr := durable.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("invalid durable release pin context; close pin: %w", closeErr)
		}
		return nil, fmt.Errorf("invalid durable release pin context")
	}
	durablePin := durable.Pin()
	if durablePin.Country != country || durablePin.ReleaseID != release.ID ||
		durablePin.Key == "" || !durablePin.ExpiresAt.After(time.Now()) {
		closeErr := durable.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("invalid durable release pin; close pin: %w", closeErr)
		}
		return nil, fmt.Errorf("invalid durable release pin")
	}
	local, err := r.pools.acquire(ctx, release)
	if err != nil {
		if releaseErr := durable.Close(); releaseErr != nil {
			return nil, fmt.Errorf("%v; release durable pin: %w", err, releaseErr)
		}
		return nil, err
	}
	operations := guardedOperations{next: local.operations(), lease: durable.Context()}
	return &requestPin{releaseID: release.ID, operations: operations, durable: durable, local: local}, nil
}

type requestPin struct {
	releaseID  string
	operations api.Operations
	durable    DurableLease
	local      *poolLease
	once       sync.Once
	err        error
}

func (p *requestPin) ReleaseID() string          { return p.releaseID }
func (p *requestPin) Operations() api.Operations { return p.operations }
func (p *requestPin) Close() error {
	p.once.Do(func() {
		localErr := p.local.close()
		durableErr := p.durable.Close()
		if localErr != nil {
			p.err = fmt.Errorf("close release bundle: %w", localErr)
		}
		if durableErr != nil {
			if p.err != nil {
				p.err = fmt.Errorf("%v; release durable pin: %w", p.err, durableErr)
			} else {
				p.err = fmt.Errorf("release durable pin: %w", durableErr)
			}
		}
	})
	return p.err
}
