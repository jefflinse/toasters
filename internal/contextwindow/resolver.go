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
	// an unreachable provider can't stall every snapshot build.
	errorTTL = 15 * time.Second
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

// Resolver resolves and caches context windows. All fields are optional
// (nil-tolerant); a zero Resolver resolves everything to 0. Safe for
// concurrent use.
type Resolver struct {
	providers ProviderSource
	configs   ConfigSource
	catalog   Catalog
	now       func() time.Time

	mu    sync.Mutex
	cache map[string]*providerCache
	// fetching serializes on-demand fetches per provider so concurrent
	// Window calls can't stampede an endpoint. Entries are never removed;
	// the set of providers is small and stable.
	fetching map[string]*sync.Mutex
}

// NewResolver creates a Resolver. Any argument may be nil, disabling that
// resolution tier.
func NewResolver(providers ProviderSource, configs ConfigSource, catalog Catalog) *Resolver {
	return &Resolver{
		providers: providers,
		configs:   configs,
		catalog:   catalog,
		now:       time.Now,
		cache:     make(map[string]*providerCache),
		fetching:  make(map[string]*sync.Mutex),
	}
}

// Window returns the effective context window for a model served by the
// named provider, or 0 if unknown. providerName may be a registry key
// ("lmstudio") or a display name ("LMStudio"); display names are normalized
// against the config source. Window may fetch the provider's model list on a
// cache miss; the fetch is bounded by ctx and failures are negative-cached.
func (r *Resolver) Window(ctx context.Context, providerName, modelID string) int {
	if r == nil || modelID == "" {
		return 0
	}
	key, cfg := r.lookupConfig(providerName)

	if w := r.reportedWindow(ctx, key, modelID); w > 0 {
		return w
	}
	if cfg != nil && cfg.ContextWindow > 0 {
		return cfg.ContextWindow
	}
	if r.catalog != nil {
		if w, ok := r.catalog.ModelContextLimit(ctx, key, modelID); ok {
			return w
		}
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
	if r.cache == nil {
		r.cache = make(map[string]*providerCache)
	}
	r.cache[providerKey] = &providerCache{windows: windows, fetchedAt: r.clock()}
}

// reportedWindow returns the provider-reported context length for the model,
// fetching the provider's model list if the cache is missing or stale.
func (r *Resolver) reportedWindow(ctx context.Context, key, modelID string) int {
	if w, fresh := r.cachedWindow(key, modelID); fresh {
		return w
	}
	if r.providers == nil {
		return 0
	}
	p, ok := r.providers.Get(key)
	if !ok {
		return 0
	}

	// Serialize fetches per provider; whoever wins re-checks freshness so
	// waiters reuse the winner's result instead of re-fetching.
	fetchMu := r.fetchLock(key)
	fetchMu.Lock()
	defer fetchMu.Unlock()
	if w, fresh := r.cachedWindow(key, modelID); fresh {
		return w
	}

	models, err := p.Models(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		entry := r.cache[key]
		if entry == nil {
			entry = &providerCache{}
			r.cache[key] = entry
		}
		entry.failedAt = r.clock()
		// Serve stale data over nothing.
		return entry.windows[modelID]
	}
	windows := make(map[string]int, len(models))
	for _, m := range models {
		windows[m.ID] = m.ContextLength()
	}
	r.cache[key] = &providerCache{windows: windows, fetchedAt: r.clock()}
	return windows[modelID]
}

// cachedWindow returns the cached window for the model and whether the cache
// entry is fresh enough that no fetch is warranted (either recently fetched
// or recently failed).
func (r *Resolver) cachedWindow(key, modelID string) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cache[key]
	if !ok {
		return 0, false
	}
	now := r.clock()
	if now.Sub(entry.fetchedAt) < modelListTTL || now.Sub(entry.failedAt) < errorTTL {
		return entry.windows[modelID], true
	}
	return entry.windows[modelID], false
}

// fetchLock returns the per-provider fetch mutex, creating it if needed.
func (r *Resolver) fetchLock(key string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fetching == nil {
		r.fetching = make(map[string]*sync.Mutex)
	}
	mu, ok := r.fetching[key]
	if !ok {
		mu = &sync.Mutex{}
		r.fetching[key] = mu
	}
	return mu
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
