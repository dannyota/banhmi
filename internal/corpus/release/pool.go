package release

import (
	"context"
	"fmt"
	"sync"

	"danny.vn/banhmi/internal/evidence/api"
)

type Bundle interface {
	Operations() api.Operations
	// Close must be safe to retry when it returns an error.
	Close() error
}

type BundleFactory interface {
	// Open receives a catalog-resolved release. Implementations must resolve the
	// trusted SnapshotRef through operator configuration and must not treat it as
	// a caller-supplied DSN.
	Open(context.Context, Release) (Bundle, error)
}

type PoolSet struct {
	mu      sync.Mutex
	factory BundleFactory
	entries map[poolKey]*poolEntry
}

type poolKey struct {
	country   string
	releaseID string
}

type poolEntry struct {
	bundle   Bundle
	refs     int
	draining bool
	closing  bool
}

type poolLease struct {
	set   *PoolSet
	key   poolKey
	entry *poolEntry
	once  sync.Once
	err   error
}

func NewPoolSet(factory BundleFactory) *PoolSet {
	return &PoolSet{factory: factory, entries: make(map[poolKey]*poolEntry)}
}

func (p *PoolSet) acquire(ctx context.Context, release Release) (*poolLease, error) {
	if p == nil || p.factory == nil {
		return nil, fmt.Errorf("release bundle factory is not configured")
	}
	key := poolKey{country: release.Country, releaseID: release.ID}
	for {
		p.mu.Lock()
		if entry := p.entries[key]; entry != nil {
			if !entry.draining {
				entry.refs++
				p.mu.Unlock()
				return &poolLease{set: p, key: key, entry: entry}, nil
			}
			if entry.refs != 0 || entry.closing {
				p.mu.Unlock()
				return nil, ErrPoolDraining
			}
			entry.closing = true
			p.mu.Unlock()
			if err := p.finishClose(key, entry); err != nil {
				return nil, fmt.Errorf("%w: retry bundle close: %v", ErrPoolDraining, err)
			}
			continue
		}
		bundle, err := p.factory.Open(ctx, release)
		if err != nil {
			p.mu.Unlock()
			return nil, fmt.Errorf("open release bundle: %w", err)
		}
		if bundle == nil || bundle.Operations() == nil {
			p.mu.Unlock()
			if bundle != nil {
				_ = bundle.Close()
			}
			return nil, fmt.Errorf("open release bundle: operations are required")
		}
		entry := &poolEntry{bundle: bundle, refs: 1}
		p.entries[key] = entry
		p.mu.Unlock()
		return &poolLease{set: p, key: key, entry: entry}, nil
	}
}

func (p *PoolSet) Drain(country, releaseID string) error {
	key := poolKey{country: country, releaseID: releaseID}
	p.mu.Lock()
	entry := p.entries[key]
	if entry == nil {
		p.mu.Unlock()
		return nil
	}
	entry.draining = true
	if entry.refs != 0 || entry.closing {
		p.mu.Unlock()
		return nil
	}
	entry.closing = true
	p.mu.Unlock()
	return p.finishClose(key, entry)
}

func (l *poolLease) operations() api.Operations { return l.entry.bundle.Operations() }

func (l *poolLease) close() error {
	l.once.Do(func() {
		l.set.mu.Lock()
		if l.entry.refs > 0 {
			l.entry.refs--
		}
		if l.entry.refs == 0 && l.entry.draining {
			if l.entry.closing {
				l.set.mu.Unlock()
				return
			}
			l.entry.closing = true
			l.set.mu.Unlock()
			l.err = l.set.finishClose(l.key, l.entry)
			return
		}
		l.set.mu.Unlock()
	})
	return l.err
}

func (p *PoolSet) finishClose(key poolKey, entry *poolEntry) error {
	err := entry.bundle.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.entries[key] != entry {
		return err
	}
	if err == nil {
		delete(p.entries, key)
	} else {
		entry.closing = false
	}
	return err
}
