package tui

import (
	"context"
	"errors"
	"testing"

	"github.com/jefflinse/toasters/internal/provider"
)

// mockProvider implements provider.Provider for testing fetchModels.
type mockProvider struct {
	models []provider.ModelInfo
	err    error
}

func (m *mockProvider) ChatStream(_ context.Context, _ provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent)
	close(ch)
	return ch, nil
}

func (m *mockProvider) Models(_ context.Context) ([]provider.ModelInfo, error) {
	return m.models, m.err
}

func (m *mockProvider) Name() string {
	return "mock"
}

func TestFetchModels_ReturnsNonNilCmd(t *testing.T) {
	t.Parallel()

	m := Model{
		llmClient: &mockProvider{},
	}

	cmd := m.fetchModels()
	if cmd == nil {
		t.Fatal("expected non-nil cmd from fetchModels")
	}
}

func TestFetchModels_SuccessReturnsModelsMsg(t *testing.T) {
	t.Parallel()

	models := []provider.ModelInfo{
		{ID: "model-1", State: "loaded", MaxContextLength: 8192},
		{ID: "model-2", State: "not-loaded", MaxContextLength: 4096},
	}
	m := Model{
		llmClient: &mockProvider{models: models},
	}

	cmd := m.fetchModels()
	msg := cmd()

	modelsMsg, ok := msg.(ModelsMsg)
	if !ok {
		t.Fatalf("expected ModelsMsg, got %T", msg)
	}
	if modelsMsg.Err != nil {
		t.Fatalf("unexpected error: %v", modelsMsg.Err)
	}
	if len(modelsMsg.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(modelsMsg.Models))
	}
	if modelsMsg.Models[0].ID != "model-1" {
		t.Errorf("first model ID = %q, want %q", modelsMsg.Models[0].ID, "model-1")
	}
	if modelsMsg.Models[1].ID != "model-2" {
		t.Errorf("second model ID = %q, want %q", modelsMsg.Models[1].ID, "model-2")
	}
}

func TestFetchModels_ErrorReturnsModelsMsg(t *testing.T) {
	t.Parallel()

	testErr := errors.New("connection refused")
	m := Model{
		llmClient: &mockProvider{err: testErr},
	}

	cmd := m.fetchModels()
	msg := cmd()

	modelsMsg, ok := msg.(ModelsMsg)
	if !ok {
		t.Fatalf("expected ModelsMsg, got %T", msg)
	}
	if !errors.Is(modelsMsg.Err, testErr) {
		t.Errorf("Err = %v, want %v", modelsMsg.Err, testErr)
	}
	if modelsMsg.Models != nil {
		t.Errorf("Models = %v, want nil", modelsMsg.Models)
	}
}

func TestFetchModels_EmptyModelsReturnsEmptySlice(t *testing.T) {
	t.Parallel()

	m := Model{
		llmClient: &mockProvider{models: []provider.ModelInfo{}},
	}

	cmd := m.fetchModels()
	msg := cmd()

	modelsMsg, ok := msg.(ModelsMsg)
	if !ok {
		t.Fatalf("expected ModelsMsg, got %T", msg)
	}
	if modelsMsg.Err != nil {
		t.Fatalf("unexpected error: %v", modelsMsg.Err)
	}
	if len(modelsMsg.Models) != 0 {
		t.Errorf("expected 0 models, got %d", len(modelsMsg.Models))
	}
}
