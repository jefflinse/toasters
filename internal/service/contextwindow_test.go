package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/operator"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

// fakeWindowSource implements ContextWindowSource with canned answers and
// call accounting.
type fakeWindowSource struct {
	mu          sync.Mutex
	windows     map[string]int // keyed by provider + "/" + model
	windowCalls int
	observed    map[string][]provider.ModelInfo
}

func (f *fakeWindowSource) Window(_ context.Context, providerName, modelID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.windowCalls++
	return f.windows[providerName+"/"+modelID]
}

func (f *fakeWindowSource) ObserveModels(providerKey string, models []provider.ModelInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed == nil {
		f.observed = make(map[string][]provider.ModelInfo)
	}
	f.observed[providerKey] = models
}

func TestSessionSnapshotsToService_FillsContextWindow(t *testing.T) {
	t.Parallel()

	fake := &fakeWindowSource{windows: map[string]int{"LMStudio/gemma": 8192}}
	svc := NewLocal(LocalConfig{ConfigDir: t.TempDir(), ContextWindows: fake})

	snaps := []runtime.SessionSnapshot{
		{ID: "s1", Provider: "LMStudio", Model: "gemma", CurrentContextTokens: 1000},
		{ID: "s2", Provider: "LMStudio", Model: "gemma", CurrentContextTokens: 2000},
		{ID: "s3", Provider: "LMStudio", Model: "other", CurrentContextTokens: 500},
	}
	out := svc.sessionSnapshotsToService(context.Background(), snaps)

	if len(out) != 3 {
		t.Fatalf("mapped %d snapshots, want 3", len(out))
	}
	if out[0].ContextWindow != 8192 || out[1].ContextWindow != 8192 {
		t.Errorf("gemma windows = %d, %d, want 8192 both", out[0].ContextWindow, out[1].ContextWindow)
	}
	if out[2].ContextWindow != 0 {
		t.Errorf("unknown model window = %d, want 0", out[2].ContextWindow)
	}
	// Two distinct provider/model pairs → two resolver calls, not three.
	if fake.windowCalls != 2 {
		t.Errorf("resolver called %d times, want 2 (memoized per provider/model)", fake.windowCalls)
	}
}

func TestSessionSnapshotsToService_NilResolver(t *testing.T) {
	t.Parallel()

	svc := NewLocal(LocalConfig{ConfigDir: t.TempDir()})
	out := svc.sessionSnapshotsToService(context.Background(), []runtime.SessionSnapshot{
		{ID: "s1", Provider: "LMStudio", Model: "gemma"},
	})
	if len(out) != 1 || out[0].ContextWindow != 0 {
		t.Errorf("nil-resolver mapping = %+v, want ContextWindow 0", out)
	}
}

func TestOperatorStatus_CarriesContextWindow(t *testing.T) {
	t.Parallel()

	op, err := operator.New(operator.Config{Model: "gemma", SystemPrompt: "test operator"})
	if err != nil {
		t.Fatalf("operator.New: %v", err)
	}
	fake := &fakeWindowSource{windows: map[string]int{"lmstudio/gemma": 8192}}
	svc := NewLocal(LocalConfig{
		ConfigDir:          t.TempDir(),
		Operator:           op,
		OperatorModel:      "gemma",
		OperatorProviderID: "lmstudio",
		ContextWindows:     fake,
		StartTime:          time.Now(),
	})

	st, err := svc.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.ContextWindow != 8192 {
		t.Errorf("Status.ContextWindow = %d, want 8192", st.ContextWindow)
	}
}

func TestListProviderModels_ObservesModels(t *testing.T) {
	t.Parallel()

	registry := provider.NewRegistry()
	registry.Register("lmstudio", &mockProvider{modelsResult: []provider.ModelInfo{
		{ID: "gemma", LoadedContextLength: 8192},
	}})
	fake := &fakeWindowSource{}
	svc := NewLocal(LocalConfig{
		ConfigDir:      t.TempDir(),
		Registry:       registry,
		ContextWindows: fake,
	})

	if _, err := svc.ListProviderModels(context.Background(), "lmstudio"); err != nil {
		t.Fatalf("ListProviderModels: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	got, ok := fake.observed["lmstudio"]
	if !ok || len(got) != 1 || got[0].ID != "gemma" {
		t.Errorf("observed = %+v, want the fetched gemma model list keyed by provider ID", fake.observed)
	}
}

func TestListModels_ObservesUnderOperatorProviderID(t *testing.T) {
	t.Parallel()

	fake := &fakeWindowSource{}
	svc := NewLocal(LocalConfig{
		ConfigDir: t.TempDir(),
		Provider: &mockProvider{modelsResult: []provider.ModelInfo{
			{ID: "gemma", LoadedContextLength: 8192},
		}},
		OperatorProviderID: "lmstudio",
		ContextWindows:     fake,
	})

	if _, err := svc.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if _, ok := fake.observed["lmstudio"]; !ok {
		t.Errorf("observed keys = %v, want operator provider ID %q", fake.observed, "lmstudio")
	}
}
