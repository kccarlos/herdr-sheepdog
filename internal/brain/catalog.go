package brain

import (
	"context"
	"sync"
	"time"
)

// CatalogTTL is how long a fetched model list is served from the cache
// before Models fetches again.
const CatalogTTL = 10 * time.Minute

// Catalog is the model list as Brain.Models serves it: the models, when
// they were fetched, and — when the fetch that was due failed — the
// error alongside the last list that did arrive (never an empty list
// in place of a known one).
type Catalog struct {
	Models    []ModelInfo
	FetchedAt time.Time
	// Stale marks a list an attempted refresh could not replace.
	Stale bool
	Err   error
}

// catalogCache holds the last fetched list under its own lock (never
// b.mu: a fetch is a network call and turns read b.mu).
type catalogCache struct {
	mu        sync.Mutex
	models    []ModelInfo
	fetchedAt time.Time
}

// Models returns the model catalog: from the cache while it is younger
// than CatalogTTL, else fetched from the model's ModelCatalog; refresh
// fetches regardless. ErrNoCatalog (in Catalog.Err) when the model has
// none. One fetch at a time; ctx bounds it.
func (b *Brain) Models(ctx context.Context, refresh bool) Catalog {
	c := &b.catalog
	c.mu.Lock()
	defer c.mu.Unlock()
	now := b.cfg.Now()
	if !refresh && c.models != nil && now.Sub(c.fetchedAt) < CatalogTTL {
		return c.snapshotLocked(nil)
	}
	src, ok := b.model.(ModelCatalog)
	if !ok {
		return c.snapshotLocked(ErrNoCatalog)
	}
	models, err := src.ListModels(ctx)
	if err != nil {
		b.log.Log(logSource, "brain.catalog_error", "", map[string]any{"error": err.Error(), "cached": len(c.models), "refresh": refresh})
		return c.snapshotLocked(err)
	}
	if models == nil {
		models = []ModelInfo{}
	}
	c.models, c.fetchedAt = models, b.cfg.Now()
	b.log.Log(logSource, "brain.catalog", "", map[string]any{"models": len(models), "refresh": refresh})
	return c.snapshotLocked(nil)
}

// CachedModels returns the catalog as cached, without fetching (an
// empty list and no error when nothing was fetched yet).
func (b *Brain) CachedModels() Catalog {
	b.catalog.mu.Lock()
	defer b.catalog.mu.Unlock()
	return b.catalog.snapshotLocked(nil)
}

func (c *catalogCache) snapshotLocked(err error) Catalog {
	out := Catalog{Models: append([]ModelInfo{}, c.models...), FetchedAt: c.fetchedAt, Err: err}
	out.Stale = err != nil && len(c.models) > 0
	return out
}
