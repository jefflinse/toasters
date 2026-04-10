// Package modelsdev fetches and caches the models.dev provider/model catalog.
//
// The catalog is a single JSON file (https://models.dev/api.json) containing
// all known LLM providers and their models. This package fetches it once on
// demand and caches it in memory with a configurable TTL.
package modelsdev

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

const (
	// DefaultURL is the models.dev catalog endpoint.
	DefaultURL = "https://models.dev/api.json"

	// DefaultTTL is how long the cached catalog stays fresh.
	DefaultTTL = 1 * time.Hour
)

// Provider is a single provider entry from the catalog.
type Provider struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Env    []string          `json:"env"`    // environment variable names for API keys
	NPM    string            `json:"npm"`    // AI SDK npm package (informational)
	API    string            `json:"api"`    // base API URL, if known
	Doc    string            `json:"doc"`    // documentation URL
	Models map[string]*Model `json:"models"` // keyed by model ID
}

// Model is a single model entry from the catalog.
type Model struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Family           string     `json:"family"`
	Attachment       bool       `json:"attachment"`
	Reasoning        bool       `json:"reasoning"`
	ToolCall         bool       `json:"tool_call"`
	StructuredOutput bool       `json:"structured_output"`
	Temperature      bool       `json:"temperature"`
	OpenWeights      bool       `json:"open_weights"`
	Knowledge        string     `json:"knowledge"`
	ReleaseDate      string     `json:"release_date"`
	Modalities       Modalities `json:"modalities"`
	Cost             Cost       `json:"cost"`
	Limit            Limit      `json:"limit"`
}

// Modalities describes input/output modalities.
type Modalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

// Cost holds per-1M-token pricing.
type Cost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

// Limit holds context and output token limits.
type Limit struct {
	Context int `json:"context"`
	Input   int `json:"input"`
	Output  int `json:"output"`
}

// Client fetches and caches the models.dev catalog.
type Client struct {
	url        string
	ttl        time.Duration
	httpClient *http.Client

	mu        sync.RWMutex
	providers map[string]*Provider // keyed by provider ID
	fetchedAt time.Time
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithURL overrides the catalog URL (useful for testing).
func WithURL(url string) ClientOption {
	return func(c *Client) { c.url = url }
}

// WithTTL sets the cache TTL.
func WithTTL(ttl time.Duration) ClientOption {
	return func(c *Client) { c.ttl = ttl }
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(hc *http.Client) ClientOption {
	return func(c *Client) { c.httpClient = hc }
}

// NewClient creates a new catalog client.
func NewClient(opts ...ClientOption) *Client {
	c := &Client{
		url: DefaultURL,
		ttl: DefaultTTL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Providers returns all providers, fetching the catalog if needed.
func (c *Client) Providers(ctx context.Context) (map[string]*Provider, error) {
	c.mu.RLock()
	if c.providers != nil && time.Since(c.fetchedAt) < c.ttl {
		defer c.mu.RUnlock()
		return c.providers, nil
	}
	c.mu.RUnlock()

	return c.refresh(ctx)
}

// ProvidersSorted returns providers sorted by name, fetching if needed.
func (c *Client) ProvidersSorted(ctx context.Context) ([]*Provider, error) {
	provs, err := c.Providers(ctx)
	if err != nil {
		return nil, err
	}
	sorted := make([]*Provider, 0, len(provs))
	for _, p := range provs {
		sorted = append(sorted, p)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})
	return sorted, nil
}

// ModelsSorted returns a provider's models sorted by name.
func (p *Provider) ModelsSorted() []*Model {
	sorted := make([]*Model, 0, len(p.Models))
	for _, m := range p.Models {
		sorted = append(sorted, m)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})
	return sorted
}

// refresh fetches the catalog from the remote URL and updates the cache.
func (c *Client) refresh(ctx context.Context) (map[string]*Provider, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock.
	if c.providers != nil && time.Since(c.fetchedAt) < c.ttl {
		return c.providers, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, fmt.Errorf("modelsdev: creating request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Return stale cache if available.
		if c.providers != nil {
			return c.providers, nil
		}
		return nil, fmt.Errorf("modelsdev: fetching catalog: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if c.providers != nil {
			return c.providers, nil
		}
		return nil, fmt.Errorf("modelsdev: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if c.providers != nil {
			return c.providers, nil
		}
		return nil, fmt.Errorf("modelsdev: reading body: %w", err)
	}

	var providers map[string]*Provider
	if err := json.Unmarshal(body, &providers); err != nil {
		if c.providers != nil {
			return c.providers, nil
		}
		return nil, fmt.Errorf("modelsdev: parsing catalog: %w", err)
	}

	// Backfill IDs from map keys (the JSON keys are the IDs).
	for id, p := range providers {
		p.ID = id
		for mid, m := range p.Models {
			m.ID = mid
		}
	}

	c.providers = providers
	c.fetchedAt = time.Now()
	return c.providers, nil
}
