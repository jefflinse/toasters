// Package contextwindow resolves the effective context window (in tokens)
// for a provider/model pair. The resolved window is what compaction
// thresholds and TUI context bars are measured against.
//
// Resolution precedence:
//
//  1. provider-reported model info (LM Studio reports the *loaded* context
//     length, which is ground truth — a 128k model loaded at 8k overflows
//     at 8k)
//  2. the provider definition's context_window override (providers/*.yaml)
//  3. the models.dev catalog
//  4. 0 — unknown; callers treat this as "feature unavailable"
//
// Window never blocks: it reads caches and returns the best currently-known
// value, kicking background fetches (bounded by their own timeout, detached
// from any caller context) to fill misses and refresh stale entries. A cold
// provider therefore resolves to 0 for the first call and converges once
// the fetch lands — callers on hot paths (the 500ms progress broadcast, and
// later the per-turn compaction checks) must never be coupled to a slow or
// down provider endpoint.
//
// The package sits below runtime/operator/service in the dependency graph
// (it imports only internal/provider) so all three can share one resolver
// without import cycles.
package contextwindow

import (
	"context"
	"sync"
	"time"

	"github.com/jefflinse/toasters/internal/provider"
)

const (
	// modelListTTL is how long a provider-reported model list stays fresh.
	modelListTTL = time.Minute

	// errorTTL is how long a failed model-list fetch suppresses retries, so
	// an unreachable provider doesn't spawn a fetch per lookup.
	errorTTL = 15 * time.Second

	// catalogTTL is how long a successful catalog answer stays fresh. The
	// catalog client caches the whole dataset for an hour; match it.
	catalogTTL = time.Hour

	// catalogMissTTL is how long a catalog miss (model unknown, or catalog
	// unreachable) suppresses retries. Short: once the catalog client has
	// its data cached, a retry is a cheap in-memory lookup.
	catalogMissTTL = time.Minute

	// fetchTimeout bounds each background fetch. Deliberately independent
	// of any caller's context so a caller's tight deadline can't be
	// misread as a provider failure (poisoning the negative cache).
	fetchTimeout = 15 * time.Second
)

// ConfigSource supplies live provider definitions. *loader.Loader satisfies
// it, so definition reloads are picked up without explicit invalidation.
type ConfigSource interface {
	Providers() []provider.ProviderConfig
}

// Catalog looks up a model's context limit in an external catalog.
// *modelsdev.Client satisfies it.
type Catalog interface {
	ModelContextLimit(ctx context.Context, providerID, modelID string) (int, bool)
}

// ProviderSource resolves a provider instance by registry key.
// *provider.Registry satisfies it.
type ProviderSource interface {
	Get(name string) (provider.Provider, bool)
}

// providerCache holds one provider's reported context lengths by model ID.
type providerCache struct {
	windows   map[string]int
	fetchedAt time.Time
	failedAt  time.Time
}

// catalogEntry caches one catalog lookup result.
type catalogEntry struct {
	window    int
	found     bool
	checkedAt time.Time
}

// Resolver resolves and caches context windows. All dependencies are
// optional (nil-tolerant); a zero-dependency Resolver resolves everything to
// 0. Safe for concurrent use.
type Resolver struct {
	providers ProviderSource
	configs   ConfigSource
	catalog   Catalog
	now       func() time.Time

	mu          sync.Mutex
	reported    map[string]*providerCache // provider-reported windows by registry key
	catalogHits map[string]catalogEntry   // catalog answers by key+"\x00"+model
	inflight    map[string]bool           // background fetch dedup
	wg          sync.WaitGroup            // tracks background fetches (tests wait on it)
}

// NewResolver creates a Resolver. Any argument may be nil, disabling that
// resolution tier.
func NewResolver(providers ProviderSource, configs ConfigSource, catalog Catalog) *Resolver {
	return &Resolver{
		providers:   providers,
		configs:     configs,
		catalog:     catalog,
		now:         time.Now,
		reported:    make(map[string]*providerCache),
		catalogHits: make(map[string]catalogEntry),
		inflight:    make(map[string]bool),
	}
}

// Window returns the effective context window for a model served by the
// named provider, or 0 if not (yet) known. providerName may be a registry
// key ("lmstudio") or a display name ("LMStudio"); display names are
// normalized against the config source. Window never blocks — cache misses
// return immediately and converge on later calls once the background fetch
// completes.
func (r *Resolver) Window(providerName, modelID string) int {
	if r == nil || modelID == "" {
		return 0
	}
	key, cfg := r.lookupConfig(providerName)
	now := r.clock()

	r.mu.Lock()
	var reportedW int
	entry := r.reported[key]
	if entry != nil {
		reportedW = entry.windows[modelID]
	}
	stale := entry == nil ||
		(now.Sub(entry.fetchedAt) >= modelListTTL && now.Sub(entry.failedAt) >= errorTTL)
	if stale && r.providers != nil {
		r.spawnLocked("models:"+key, func(ctx context.Context) { r.fetchModels(ctx, key) })
	}

	var catalogW int
	var catalogFound bool
	if r.catalog != nil {
		ck := key + "\x00" + modelID
		ce, ok := r.catalogHits[ck]
		lookup := func(ctx context.Context) { r.lookupCatalog(ctx, ck, key, modelID) }
		switch {
		case ok && ce.found:
			catalogW, catalogFound = ce.window, true
			if now.Sub(ce.checkedAt) >= catalogTTL {
				r.spawnLocked("catalog:"+ck, lookup)
			}
		case ok && now.Sub(ce.checkedAt) < catalogMissTTL:
			// Recent miss; don't retry yet.
		default:
			r.spawnLocked("catalog:"+ck, lookup)
		}
	}
	r.mu.Unlock()

	if reportedW > 0 {
		return reportedW
	}
	if cfg != nil && cfg.ContextWindow > 0 {
		return cfg.ContextWindow
	}
	if catalogFound {
		return catalogW
	}
	return 0
}

// ObserveModels records a freshly fetched model list for a provider,
// replacing any cached list. Call it after any successful Models() fetch so
// user-triggered listings keep the resolver warm for free.
func (r *Resolver) ObserveModels(providerKey string, models []provider.ModelInfo) {
	if r == nil || providerKey == "" {
		return
	}
	windows := make(map[string]int, len(models))
	for _, m := range models {
		windows[m.ID] = m.ContextLength()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reported == nil {
		r.reported = make(map[string]*providerCache)
	}
	r.reported[providerKey] = &providerCache{windows: windows, fetchedAt: r.clock()}
}

// spawnLocked starts fn on a background goroutine with a bounded, detached
// context, unless a fetch with the same dedup key is already in flight.
// Callers must hold r.mu.
func (r *Resolver) spawnLocked(key string, fn func(context.Context)) {
	if r.inflight == nil {
		r.inflight = make(map[string]bool)
	}
	if r.inflight[key] {
		return
	}
	r.inflight[key] = true
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		fn(ctx)
		r.mu.Lock()
		delete(r.inflight, key)
		r.mu.Unlock()
	}()
}

// fetchModels fetches a provider's model list and updates the reported
// cache. Runs on a background goroutine; never called with r.mu held.
func (r *Resolver) fetchModels(ctx context.Context, key string) {
	p, ok := r.providers.Get(key)
	var (
		models []provider.ModelInfo
		err    error
	)
	if ok {
		models, err = p.Models(ctx)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if !ok || err != nil {
		// Negative-cache the failure (or the unknown key) so lookups don't
		// spawn a fetch per call; stale windows keep serving meanwhile.
		entry := r.reported[key]
		if entry == nil {
			entry = &providerCache{}
			r.reported[key] = entry
		}
		entry.failedAt = r.clock()
		return
	}
	windows := make(map[string]int, len(models))
	for _, m := range models {
		windows[m.ID] = m.ContextLength()
	}
	r.reported[key] = &providerCache{windows: windows, fetchedAt: r.clock()}
}

// lookupCatalog resolves one catalog answer and caches it. Runs on a
// background goroutine; never called with r.mu held.
func (r *Resolver) lookupCatalog(ctx context.Context, ck, key, modelID string) {
	w, found := r.catalog.ModelContextLimit(ctx, key, modelID)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.catalogHits[ck] = catalogEntry{window: w, found: found, checkedAt: r.clock()}
}

// lookupConfig normalizes a provider display name or key to the registry key
// and returns the matching config, if any. Runtime session snapshots carry
// the display name ("LMStudio") while the registry and DB key by ID
// ("lmstudio"); without this normalization those lookups silently miss.
func (r *Resolver) lookupConfig(providerName string) (string, *provider.ProviderConfig) {
	if r.configs == nil {
		return providerName, nil
	}
	var byName *provider.ProviderConfig
	for _, cfg := range r.configs.Providers() {
		if cfg.Key() == providerName {
			cfg := cfg
			return providerName, &cfg
		}
		if cfg.Name == providerName && byName == nil {
			cfg := cfg
			byName = &cfg
		}
	}
	if byName != nil {
		return byName.Key(), byName
	}
	return providerName, nil
}

// clock returns the current time, honoring a test override.
func (r *Resolver) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}
