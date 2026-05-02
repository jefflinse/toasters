package tui

import (
	"context"
	"errors"
	"testing"

	"github.com/jefflinse/toasters/internal/service"
)

// mockSystemService implements service.SystemService for testing fetchModels.
type mockSystemService struct {
	models []service.ModelInfo
	err    error
}

func (m *mockSystemService) Health(_ context.Context) (service.HealthStatus, error) {
	return service.HealthStatus{}, nil
}
func (m *mockSystemService) ListModels(_ context.Context) ([]service.ModelInfo, error) {
	return m.models, m.err
}
func (m *mockSystemService) ListMCPServers(_ context.Context) ([]service.MCPServerStatus, error) {
	return nil, nil
}
func (m *mockSystemService) GetProgressState(_ context.Context) (service.ProgressState, error) {
	return service.ProgressState{}, nil
}
func (m *mockSystemService) GetLogs(_ context.Context) (string, error) {
	return "", nil
}
func (m *mockSystemService) ListCatalogProviders(_ context.Context) ([]service.CatalogProvider, error) {
	return nil, nil
}
func (m *mockSystemService) AddProvider(_ context.Context, _ service.AddProviderRequest) error {
	return nil
}
func (m *mockSystemService) UpdateProvider(_ context.Context, _ service.AddProviderRequest) error {
	return nil
}
func (m *mockSystemService) ListConfiguredProviderIDs(_ context.Context) ([]string, error) {
	return nil, nil
}
func (m *mockSystemService) SetOperatorProvider(_ context.Context, _ string, _ string) error {
	return nil
}
func (m *mockSystemService) ListProviderModels(_ context.Context, _ string) ([]service.ModelInfo, error) {
	return nil, nil
}
func (m *mockSystemService) GetSettings(_ context.Context) (service.Settings, error) {
	return service.Settings{}, nil
}
func (m *mockSystemService) UpdateSettings(_ context.Context, _ service.Settings) error {
	return nil
}

// mockDefinitionService implements service.DefinitionService with no-op methods.
type mockDefinitionService struct{}

func (m *mockDefinitionService) ListSkills(_ context.Context) ([]service.Skill, error) {
	return nil, nil
}
func (m *mockDefinitionService) GetSkill(_ context.Context, _ string) (service.Skill, error) {
	return service.Skill{}, nil
}
func (m *mockDefinitionService) CreateSkill(_ context.Context, _ string) (service.Skill, error) {
	return service.Skill{}, nil
}
func (m *mockDefinitionService) DeleteSkill(_ context.Context, _ string) error { return nil }
func (m *mockDefinitionService) GenerateSkill(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (m *mockDefinitionService) ListGraphs(_ context.Context) ([]service.GraphDefinition, error) {
	return nil, nil
}
func (m *mockDefinitionService) GetGraph(_ context.Context, _ string) (service.GraphDefinition, error) {
	return service.GraphDefinition{}, nil
}

// mockService implements service.Service for testing.
type mockService struct {
	system      service.SystemService
	definitions service.DefinitionService
}

func (m *mockService) Operator() service.OperatorService { return nil }
func (m *mockService) Definitions() service.DefinitionService {
	if m.definitions != nil {
		return m.definitions
	}
	return &mockDefinitionService{}
}
func (m *mockService) Jobs() service.JobService         { return nil }
func (m *mockService) Sessions() service.SessionService { return nil }
func (m *mockService) Events() service.EventService     { return nil }
func (m *mockService) System() service.SystemService    { return m.system }

func TestFetchModels_ReturnsNonNilCmd(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.svc = &mockService{system: &mockSystemService{}}

	cmd := m.fetchModels()
	if cmd == nil {
		t.Fatal("expected non-nil cmd from fetchModels")
	}
}

func TestFetchModels_SuccessReturnsModelsMsg(t *testing.T) {
	t.Parallel()

	models := []service.ModelInfo{
		{ID: "model-1", State: "loaded", MaxContextLength: 8192},
		{ID: "model-2", State: "not-loaded", MaxContextLength: 4096},
	}
	m := newMinimalModel(t)
	m.svc = &mockService{system: &mockSystemService{models: models}}

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
	m := newMinimalModel(t)
	m.svc = &mockService{system: &mockSystemService{err: testErr}}

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

	m := newMinimalModel(t)
	m.svc = &mockService{system: &mockSystemService{models: []service.ModelInfo{}}}

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
