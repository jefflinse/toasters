package modelsdev

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// testCatalogJSON mimics the models.dev api.json shape: providers keyed by
// ID, models keyed by ID. "shared-model" exists under two providers with
// different limits to exercise provider-scoped preference.
const testCatalogJSON = `{
	"anthropic": {
		"name": "Anthropic",
		"models": {
			"claude-opus-4-6": {"name": "Claude Opus 4.6", "limit": {"context": 200000, "output": 32000}},
			"shared-model": {"name": "Shared", "limit": {"context": 100000}}
		}
	},
	"zeta": {
		"name": "Zeta",
		"models": {
			"shared-model": {"name": "Shared", "limit": {"context": 50000}},
			"no-limit-model": {"name": "No Limit"}
		}
	}
}`

func newTestClient(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testCatalogJSON))
	}))
	t.Cleanup(srv.Close)
	return NewClient(WithURL(srv.URL))
}

func TestModelContextLimit(t *testing.T) {
	t.Parallel()
	c := newTestClient(t)
	ctx := context.Background()

	t.Run("exact provider and model match", func(t *testing.T) {
		got, ok := c.ModelContextLimit(ctx, "anthropic", "claude-opus-4-6")
		if !ok || got != 200000 {
			t.Errorf("= (%d, %v), want (200000, true)", got, ok)
		}
	})

	t.Run("provider-scoped match preferred on model ID collision", func(t *testing.T) {
		got, ok := c.ModelContextLimit(ctx, "zeta", "shared-model")
		if !ok || got != 50000 {
			t.Errorf("= (%d, %v), want (50000, true) from zeta, not anthropic", got, ok)
		}
	})

	t.Run("cross-provider fallback for unknown provider IDs", func(t *testing.T) {
		// Local provider IDs like "lmstudio" don't exist in the catalog, but
		// the model IDs they serve often do.
		got, ok := c.ModelContextLimit(ctx, "lmstudio", "claude-opus-4-6")
		if !ok || got != 200000 {
			t.Errorf("= (%d, %v), want (200000, true) via cross-provider scan", got, ok)
		}
	})

	t.Run("collision without provider match resolves deterministically", func(t *testing.T) {
		// Sorted-ID order: "anthropic" wins over "zeta".
		got, ok := c.ModelContextLimit(ctx, "lmstudio", "shared-model")
		if !ok || got != 100000 {
			t.Errorf("= (%d, %v), want (100000, true) from anthropic (first sorted ID)", got, ok)
		}
	})

	t.Run("unknown model", func(t *testing.T) {
		if got, ok := c.ModelContextLimit(ctx, "anthropic", "mystery"); ok || got != 0 {
			t.Errorf("= (%d, %v), want (0, false)", got, ok)
		}
	})

	t.Run("model without a context limit", func(t *testing.T) {
		if got, ok := c.ModelContextLimit(ctx, "zeta", "no-limit-model"); ok || got != 0 {
			t.Errorf("= (%d, %v), want (0, false)", got, ok)
		}
	})
}

func TestModelContextLimit_CatalogUnavailable(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(WithURL(srv.URL))

	if got, ok := c.ModelContextLimit(context.Background(), "anthropic", "claude-opus-4-6"); ok || got != 0 {
		t.Errorf("= (%d, %v), want (0, false) when catalog unreachable", got, ok)
	}
}
