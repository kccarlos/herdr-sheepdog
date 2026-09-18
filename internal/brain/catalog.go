package brain

import (
	"context"
	"sync"
	"time"
)

// CatalogTTL is how long a fetched model list is served from the cache
// before Models fetches again.
const CatalogTTL = 10 * time.Minute

// Catalog is the model list as Brain.Models serves it: the provider it
// belongs to, the models, when they were fetched, and — when the fetch
// that was due failed — the error alongside the last list that did
// arrive (never an empty list in place of a known one).
type Catalog struct {
	Provider  string
	Models    []ModelInfo
	FetchedAt time.Time
	// Stale marks a list an attempted refresh could not replace.
	Stale bool
	Err   error
}

// catalogCaches holds one cache per provider under its own lock (never
// b.mu: a fetch is a network call and turns read b.mu).
type catalogCaches struct {
	mu   sync.Mutex
	byID map[string]*catalogCache
}

// catalogCache is the last fetched list of one provider; its lock
// serialises that provider's fetches.
type catalogCache struct {
	mu        sync.Mutex
	models    []ModelInfo
	fetchedAt time.Time
}

// of returns provider's cache, creating it on first use.
func (c *catalogCaches) of(provider string) *catalogCache {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byID == nil {
		c.byID = map[string]*catalogCache{}
	}
	cc, ok := c.byID[provider]
	if !ok {
		cc = &catalogCache{}
		c.byID[provider] = cc
	}
	return cc
}

// Models returns provider's model catalog ("" for the current
// provider): from the cache while it is younger than CatalogTTL, else
// fetched from the provider's ModelCatalog; refresh fetches regardless.
// ErrUnknownProvider (in Catalog.Err) for an id nothing is registered
// under, ErrNoCatalog when its model has no catalog. One fetch per
// provider at a time; ctx bounds it.
func (b *Brain) Models(ctx context.Context, provider string, refresh bool) Catalog {
	provider, m, ok := b.modelOf(provider)
	if !ok {
		return Catalog{Provider: provider, Models: []ModelInfo{}, Err: ErrUnknownProvider}
	}
	c := b.catalogs.of(provider)
	c.mu.Lock()
	defer c.mu.Unlock()
	now := b.cfg.Now()
	if !refresh && c.models != nil && now.Sub(c.fetchedAt) < CatalogTTL {
		return c.snapshotLocked(provider, nil)
	}
	src, ok := m.(ModelCatalog)
	if !ok {
		return c.snapshotLocked(provider, ErrNoCatalog)
	}
	models, err := src.ListModels(ctx)
	if err != nil {
		b.log.Log(logSource, "brain.catalog_error", "", map[string]any{"provider": provider, "error": err.Error(), "cached": len(c.models), "refresh": refresh})
		return c.snapshotLocked(provider, err)
	}
	if models == nil {
		models = []ModelInfo{}
	}
	c.models, c.fetchedAt = models, b.cfg.Now()
	b.log.Log(logSource, "brain.catalog", "", map[string]any{"provider": provider, "models": len(models), "refresh": refresh})
	return c.snapshotLocked(provider, nil)
}

// CachedModels returns provider's catalog ("" for the current provider)
// as cached, without fetching (an empty list and no error when nothing
// was fetched yet; ErrUnknownProvider for an unregistered id).
func (b *Brain) CachedModels(provider string) Catalog {
	provider, _, ok := b.modelOf(provider)
	if !ok {
		return Catalog{Provider: provider, Models: []ModelInfo{}, Err: ErrUnknownProvider}
	}
	c := b.catalogs.of(provider)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked(provider, nil)
}

func (c *catalogCache) snapshotLocked(provider string, err error) Catalog {
	out := Catalog{Provider: provider, Models: append([]ModelInfo{}, c.models...), FetchedAt: c.fetchedAt, Err: err}
	out.Stale = err != nil && len(c.models) > 0
	return out
}
