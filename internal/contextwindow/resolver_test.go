package contextwindow

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/provider"
)

// fakeProvider implements the subset of provider.Provider the resolver uses.
// Only Models is exercised; ChatStream panics to catch accidental calls.
// An optional gate blocks Models until released, for single-flight tests.
type fakeProvider struct {
	mu     sync.Mutex
	models []provider.ModelInfo
	err    error
	calls  int
	gate   chan struct{}
}

func (f *fakeProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	f.mu.Lock()
	f.calls++
	models, err, gate := f.models, f.err, f.gate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if err != nil {
		return nil, err
	}
	return models, nil
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) ChatStream(context.Context, provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	panic("ChatStream not expected")
}

func (f *fakeProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeProvider) set(models []provider.ModelInfo, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.models = models
	f.err = err
}

type fakeProviderSource map[string]provider.Provider

func (s fakeProviderSource) Get(name string) (provider.Provider, bool) {
	p, ok := s[name]
	return p, ok
}

type fakeConfigSource []provider.ProviderConfig

func (s fakeConfigSource) Providers() []provider.ProviderConfig { return s }

type fakeCatalog map[string]int

func (c fakeCatalog) ModelContextLimit(_ context.Context, _, modelID string) (int, bool) {
	w, ok := c[modelID]
	return w, ok
}

// newTestResolver builds a resolver with a controllable clock. Tests must
// call r.wg.Wait() (settle) before advancing the clock so background fetches
// never read it concurrently.
func newTestResolver(providers ProviderSource, configs ConfigSource, catalog Catalog) (*Resolver, *time.Time) {
	r := NewResolver(providers, configs, catalog)
	now := time.Unix(1000000, 0)
	r.now = func() time.Time { return now }
	return r, &now
}

// settledWindow calls Window, waits for any background fetches it kicked to
// land, and calls it again — the converged answer.
func settledWindow(r *Resolver, providerName, modelID string) int {
	r.Window(providerName, modelID)
	r.wg.Wait()
	return r.Window(providerName, modelID)
}

func TestWindow_Precedence(t *testing.T) {
	t.Parallel()

	// Every tier resolves to a distinct value so a silently skipped tier
	// changes the observed result.
	cfg := fakeConfigSource{{ID: "lmstudio", Name: "LMStudio", ContextWindow: 4096}}
	catalog := fakeCatalog{"gemma-4-26b": 999999}

	t.Run("provider-reported loaded length wins over everything", func(t *testing.T) {
		prov := &fakeProvider{models: []provider.ModelInfo{
			{ID: "gemma-4-26b", MaxContextLength: 131072, LoadedContextLength: 8192},
		}}
		r, _ := newTestResolver(fakeProviderSource{"lmstudio": prov}, cfg, catalog)
		if got := settledWindow(r, "lmstudio", "gemma-4-26b"); got != 8192 {
			t.Errorf("Window = %d, want 8192 (loaded length)", got)
		}
	})

	t.Run("provider-reported max used when loaded absent", func(t *testing.T) {
		prov := &fakeProvider{models: []provider.ModelInfo{
			{ID: "gemma-4-26b", MaxContextLength: 131072},
		}}
		r, _ := newTestResolver(fakeProviderSource{"lmstudio": prov}, cfg, catalog)
		if got := settledWindow(r, "lmstudio", "gemma-4-26b"); got != 131072 {
			t.Errorf("Window = %d, want 131072 (max length)", got)
		}
	})

	t.Run("config override used when provider reports nothing", func(t *testing.T) {
		// Model listed, but with no context info (standard /v1/models).
		prov := &fakeProvider{models: []provider.ModelInfo{{ID: "gemma-4-26b"}}}
		r, _ := newTestResolver(fakeProviderSource{"lmstudio": prov}, cfg, catalog)
		if got := settledWindow(r, "lmstudio", "gemma-4-26b"); got != 4096 {
			t.Errorf("Window = %d, want 4096 (config override)", got)
		}
	})

	t.Run("catalog used when no override configured", func(t *testing.T) {
		prov := &fakeProvider{models: []provider.ModelInfo{{ID: "gemma-4-26b"}}}
		noOverride := fakeConfigSource{{ID: "lmstudio", Name: "LMStudio"}}
		r, _ := newTestResolver(fakeProviderSource{"lmstudio": prov}, noOverride, catalog)
		if got := settledWindow(r, "lmstudio", "gemma-4-26b"); got != 999999 {
			t.Errorf("Window = %d, want 999999 (catalog)", got)
		}
	})

	t.Run("zero when nothing knows the model", func(t *testing.T) {
		prov := &fakeProvider{}
		noOverride := fakeConfigSource{{ID: "lmstudio", Name: "LMStudio"}}
		r, _ := newTestResolver(fakeProviderSource{"lmstudio": prov}, noOverride, fakeCatalog{})
		if got := settledWindow(r, "lmstudio", "mystery-model"); got != 0 {
			t.Errorf("Window = %d, want 0", got)
		}
	})
}

func TestWindow_NeverBlocksOnColdMiss(t *testing.T) {
	t.Parallel()

	// A provider whose Models call hangs until released. Window must return
	// immediately anyway — the read path may never be coupled to a slow
	// provider endpoint.
	gate := make(chan struct{})
	prov := &fakeProvider{
		models: []provider.ModelInfo{{ID: "m1", LoadedContextLength: 8192}},
		gate:   gate,
	}
	r, _ := newTestResolver(fakeProviderSource{"p1": prov}, nil, nil)

	done := make(chan int)
	go func() { done <- r.Window("p1", "m1") }()
	select {
	case got := <-done:
		if got != 0 {
			t.Errorf("cold Window = %d, want 0 (fetch pending)", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Window blocked on a hung provider fetch")
	}

	close(gate)
	r.wg.Wait()
	if got := r.Window("p1", "m1"); got != 8192 {
		t.Errorf("Window after fetch = %d, want 8192", got)
	}
}

func TestWindow_SingleFlightPerProvider(t *testing.T) {
	t.Parallel()

	gate := make(chan struct{})
	prov := &fakeProvider{
		models: []provider.ModelInfo{{ID: "m1", LoadedContextLength: 8192}},
		gate:   gate,
	}
	r, _ := newTestResolver(fakeProviderSource{"p1": prov}, nil, nil)

	// Many concurrent cold lookups must coalesce into one provider fetch.
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Window("p1", "m1")
		}()
	}
	wg.Wait()
	close(gate)
	r.wg.Wait()

	if got := prov.callCount(); got != 1 {
		t.Errorf("Models called %d times for concurrent cold lookups, want 1", got)
	}
}

func TestWindow_NormalizesDisplayName(t *testing.T) {
	t.Parallel()

	// Runtime snapshots carry the provider display name ("LMStudio"), not the
	// registry key ("lmstudio"). Both must resolve identically.
	prov := &fakeProvider{models: []provider.ModelInfo{
		{ID: "gemma-4-26b", LoadedContextLength: 8192},
	}}
	r, _ := newTestResolver(
		fakeProviderSource{"lmstudio": prov},
		fakeConfigSource{{ID: "lmstudio", Name: "LMStudio"}},
		nil,
	)
	byKey := settledWindow(r, "lmstudio", "gemma-4-26b")
	byName := r.Window("LMStudio", "gemma-4-26b")
	if byKey != 8192 || byName != 8192 {
		t.Errorf("byKey = %d, byName = %d, want both 8192", byKey, byName)
	}
	if got := prov.callCount(); got != 1 {
		t.Errorf("Models called %d times, want 1 (name and key share one cache entry)", got)
	}
}

func TestWindow_NilTolerance(t *testing.T) {
	t.Parallel()

	var nilResolver *Resolver
	if got := nilResolver.Window("p", "m"); got != 0 {
		t.Errorf("nil resolver Window = %d, want 0", got)
	}
	nilResolver.ObserveModels("p", nil) // must not panic

	r := NewResolver(nil, nil, nil)
	if got := r.Window("p", "m"); got != 0 {
		t.Errorf("all-nil-deps Window = %d, want 0", got)
	}
	if got := r.Window("p", ""); got != 0 {
		t.Errorf("empty model Window = %d, want 0", got)
	}
}

func TestWindow_CachesModelList(t *testing.T) {
	t.Parallel()

	prov := &fakeProvider{models: []provider.ModelInfo{
		{ID: "m1", LoadedContextLength: 8192},
	}}
	r, now := newTestResolver(fakeProviderSource{"p1": prov}, nil, nil)

	for range 5 {
		r.Window("p1", "m1")
		r.wg.Wait()
	}
	if got := prov.callCount(); got != 1 {
		t.Fatalf("Models called %d times within TTL, want 1", got)
	}

	// Past the TTL the list is refetched, picking up a changed loaded length
	// (e.g. the user reloaded the model at a different context size).
	prov.set([]provider.ModelInfo{{ID: "m1", LoadedContextLength: 16384}}, nil)
	*now = now.Add(modelListTTL + time.Second)
	if got := settledWindow(r, "p1", "m1"); got != 16384 {
		t.Errorf("Window after TTL = %d, want 16384", got)
	}
	if got := prov.callCount(); got != 2 {
		t.Errorf("Models called %d times after TTL expiry, want 2", got)
	}
}

func TestWindow_ServesStaleWhileRefreshing(t *testing.T) {
	t.Parallel()

	prov := &fakeProvider{models: []provider.ModelInfo{
		{ID: "m1", LoadedContextLength: 8192},
	}}
	r, now := newTestResolver(fakeProviderSource{"p1": prov}, nil, nil)
	if got := settledWindow(r, "p1", "m1"); got != 8192 {
		t.Fatalf("initial Window = %d, want 8192", got)
	}

	// Past the TTL, the stale value serves immediately while the background
	// refresh is still in flight.
	gate := make(chan struct{})
	prov.mu.Lock()
	prov.gate = gate
	prov.mu.Unlock()
	*now = now.Add(modelListTTL + time.Second)
	if got := r.Window("p1", "m1"); got != 8192 {
		t.Errorf("Window during refresh = %d, want stale 8192", got)
	}
	close(gate)
	r.wg.Wait()
}

func TestWindow_NegativeCachesFetchErrors(t *testing.T) {
	t.Parallel()

	prov := &fakeProvider{err: errors.New("connection refused")}
	r, now := newTestResolver(fakeProviderSource{"p1": prov}, nil, nil)

	for range 5 {
		if got := settledWindow(r, "p1", "m1"); got != 0 {
			t.Fatalf("Window = %d, want 0 while provider down", got)
		}
	}
	if got := prov.callCount(); got != 1 {
		t.Fatalf("Models called %d times within error TTL, want 1", got)
	}

	// After the error TTL, a recovered provider is picked up.
	prov.set([]provider.ModelInfo{{ID: "m1", LoadedContextLength: 8192}}, nil)
	*now = now.Add(errorTTL + time.Second)
	if got := settledWindow(r, "p1", "m1"); got != 8192 {
		t.Errorf("Window after recovery = %d, want 8192", got)
	}
}

func TestWindow_ServesStaleOnFetchError(t *testing.T) {
	t.Parallel()

	prov := &fakeProvider{models: []provider.ModelInfo{
		{ID: "m1", LoadedContextLength: 8192},
	}}
	r, now := newTestResolver(fakeProviderSource{"p1": prov}, nil, nil)

	if got := settledWindow(r, "p1", "m1"); got != 8192 {
		t.Fatalf("initial Window = %d, want 8192", got)
	}

	// Provider goes down; past TTL the refetch fails but the stale value
	// still serves.
	prov.set(nil, errors.New("connection refused"))
	*now = now.Add(modelListTTL + time.Second)
	if got := settledWindow(r, "p1", "m1"); got != 8192 {
		t.Errorf("Window with stale cache = %d, want 8192", got)
	}
}

func TestWindow_CatalogMissRetriesAfterTTL(t *testing.T) {
	t.Parallel()

	// Catalog knows nothing at first (e.g. unreachable), then learns.
	catalog := fakeCatalog{}
	prov := &fakeProvider{}
	r, now := newTestResolver(fakeProviderSource{"p1": prov}, nil, catalog)

	if got := settledWindow(r, "p1", "m1"); got != 0 {
		t.Fatalf("Window = %d, want 0 on catalog miss", got)
	}
	catalog["m1"] = 200000
	// Within the miss TTL the cached miss still serves.
	if got := settledWindow(r, "p1", "m1"); got != 0 {
		t.Errorf("Window within miss TTL = %d, want 0", got)
	}
	*now = now.Add(catalogMissTTL + time.Second)
	if got := settledWindow(r, "p1", "m1"); got != 200000 {
		t.Errorf("Window after miss TTL = %d, want 200000", got)
	}
}

func TestObserveModels_SeedsCache(t *testing.T) {
	t.Parallel()

	// No provider source at all: only observed data can answer.
	r, _ := newTestResolver(nil, nil, nil)
	r.ObserveModels("p1", []provider.ModelInfo{
		{ID: "m1", MaxContextLength: 32768},
	})
	if got := r.Window("p1", "m1"); got != 32768 {
		t.Errorf("Window = %d, want 32768 from observed list", got)
	}
}

func TestObserveModels_ReplacesCachedList(t *testing.T) {
	t.Parallel()

	prov := &fakeProvider{models: []provider.ModelInfo{
		{ID: "m1", LoadedContextLength: 8192},
	}}
	r, _ := newTestResolver(fakeProviderSource{"p1": prov}, nil, nil)
	if got := settledWindow(r, "p1", "m1"); got != 8192 {
		t.Fatalf("initial Window = %d, want 8192", got)
	}

	// A user-triggered model listing observed fresher data.
	r.ObserveModels("p1", []provider.ModelInfo{{ID: "m1", LoadedContextLength: 16384}})
	if got := r.Window("p1", "m1"); got != 16384 {
		t.Errorf("Window after observe = %d, want 16384", got)
	}
	if got := prov.callCount(); got != 1 {
		t.Errorf("Models called %d times, want 1 (observe must not trigger fetch)", got)
	}
}

func TestResolver_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	prov := &fakeProvider{models: []provider.ModelInfo{
		{ID: "m1", LoadedContextLength: 8192},
	}}
	r := NewResolver(
		fakeProviderSource{"lmstudio": prov},
		fakeConfigSource{{ID: "lmstudio", Name: "LMStudio", ContextWindow: 4096}},
		fakeCatalog{"m2": 131072},
	)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 100 {
				r.Window("lmstudio", "m1")
				r.Window("LMStudio", "m2")
			}
		}()
		go func() {
			defer wg.Done()
			for range 100 {
				r.ObserveModels("lmstudio", []provider.ModelInfo{
					{ID: "m1", LoadedContextLength: 8192},
				})
			}
		}()
	}
	wg.Wait()
	r.wg.Wait()
}
