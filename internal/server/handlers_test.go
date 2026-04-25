package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/service"
)

// ---------------------------------------------------------------------------
// mockService — minimal service.Service implementation for handler tests.
// ---------------------------------------------------------------------------

type mockService struct {
	operator   *mockOperatorService
	definition *mockDefinitionService
	jobs       *mockJobService
	sessions   *mockSessionService
	events     *mockEventService
	system     *mockSystemService
}

func newMockService() *mockService {
	return &mockService{
		operator:   &mockOperatorService{},
		definition: &mockDefinitionService{},
		jobs:       &mockJobService{},
		sessions:   &mockSessionService{},
		events:     &mockEventService{},
		system:     &mockSystemService{},
	}
}

func (m *mockService) Operator() service.OperatorService { return m.operator }
func (m *mockService) Definitions() service.DefinitionService {
	return m.definition
}
func (m *mockService) Jobs() service.JobService         { return m.jobs }
func (m *mockService) Sessions() service.SessionService { return m.sessions }
func (m *mockService) Events() service.EventService     { return m.events }
func (m *mockService) System() service.SystemService    { return m.system }

type mockOperatorService struct {
	respondToPromptErr   error
	respondToPromptCalls []struct {
		requestID string
		response  string
	}
	respondToBlockerErr   error
	respondToBlockerCalls []struct {
		jobID   string
		taskID  string
		answers []string
	}
}

func (m *mockOperatorService) SendMessage(_ context.Context, _ string) (string, error) {
	return "turn-123", nil
}

func (m *mockOperatorService) RespondToPrompt(_ context.Context, requestID, response string) error {
	m.respondToPromptCalls = append(m.respondToPromptCalls, struct {
		requestID string
		response  string
	}{requestID, response})
	return m.respondToPromptErr
}

func (m *mockOperatorService) Status(_ context.Context) (service.OperatorStatus, error) {
	return service.OperatorStatus{State: service.OperatorStateIdle}, nil
}

func (m *mockOperatorService) History(_ context.Context) ([]service.ChatEntry, error) {
	return nil, nil
}

func (m *mockOperatorService) RespondToBlocker(_ context.Context, jobID, taskID string, answers []string) error {
	m.respondToBlockerCalls = append(m.respondToBlockerCalls, struct {
		jobID   string
		taskID  string
		answers []string
	}{jobID, taskID, answers})
	return m.respondToBlockerErr
}

type mockDefinitionService struct{}
type mockJobService struct{}
type mockSessionService struct{}
type mockEventService struct{}
type mockSystemService struct{}

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
func (m *mockDefinitionService) ListWorkers(_ context.Context) ([]service.Worker, error) {
	return nil, nil
}
func (m *mockDefinitionService) GetWorker(_ context.Context, _ string) (service.Worker, error) {
	return service.Worker{}, nil
}
func (m *mockDefinitionService) ListGraphs(_ context.Context) ([]service.GraphDefinition, error) {
	return nil, nil
}
func (m *mockDefinitionService) GetGraph(_ context.Context, _ string) (service.GraphDefinition, error) {
	return service.GraphDefinition{}, nil
}

func (m *mockJobService) List(_ context.Context, _ *service.JobListFilter) ([]service.Job, error) {
	return nil, nil
}
func (m *mockJobService) ListAll(_ context.Context) ([]service.Job, error) {
	return nil, nil
}
func (m *mockJobService) Get(_ context.Context, _ string) (service.JobDetail, error) {
	return service.JobDetail{}, nil
}
func (m *mockJobService) Cancel(_ context.Context, _ string) error { return nil }

func (m *mockSessionService) List(_ context.Context) ([]service.SessionSnapshot, error) {
	return nil, nil
}
func (m *mockSessionService) Get(_ context.Context, _ string) (service.SessionDetail, error) {
	return service.SessionDetail{}, nil
}
func (m *mockSessionService) Cancel(_ context.Context, _ string) error { return nil }

func (m *mockEventService) Subscribe(_ context.Context) <-chan service.Event {
	return nil
}

func (m *mockSystemService) Health(_ context.Context) (service.HealthStatus, error) {
	return service.HealthStatus{Status: "ok"}, nil
}
func (m *mockSystemService) ListModels(_ context.Context) ([]service.ModelInfo, error) {
	return nil, nil
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

// ---------------------------------------------------------------------------
// Handler tests
// ---------------------------------------------------------------------------

func TestRespondToPrompt_ResponseTooLarge(t *testing.T) {
	t.Parallel()

	mockSvc := newMockService()
	srv := New(mockSvc)

	// Create a response that exceeds maxResponseBytes (50,000 bytes).
	largeResponse := strings.Repeat("x", maxResponseBytes+1)
	body := fmt.Sprintf(`{"response": "%s"}`, largeResponse)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/operator/prompts/req-123/respond", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("requestId", "req-123")
	rec := httptest.NewRecorder()

	srv.respondToPrompt(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var errResp ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("unmarshaling response: %v", err)
	}
	if errResp.Error.Code != "bad_request" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "bad_request")
	}
	if !strings.Contains(errResp.Error.Message, "response too long") {
		t.Errorf("error message = %q, want to contain 'response too long'", errResp.Error.Message)
	}

	// Service should not have been called.
	if len(mockSvc.operator.respondToPromptCalls) != 0 {
		t.Error("service should not have been called for oversized response")
	}
}

func TestRespondToPrompt_ResponseAtLimit(t *testing.T) {
	t.Parallel()

	mockSvc := newMockService()
	srv := New(mockSvc)

	// Response exactly at the limit should pass server validation.
	response := strings.Repeat("x", maxResponseBytes)
	body := fmt.Sprintf(`{"response": "%s"}`, response)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/operator/prompts/req-123/respond", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("requestId", "req-123")
	rec := httptest.NewRecorder()

	srv.respondToPrompt(rec, req)

	// Should succeed (204 No Content).
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body.String())
	}

	// Service should have been called.
	if len(mockSvc.operator.respondToPromptCalls) != 1 {
		t.Fatal("service should have been called exactly once")
	}
	if mockSvc.operator.respondToPromptCalls[0].requestID != "req-123" {
		t.Errorf("requestID = %q, want %q", mockSvc.operator.respondToPromptCalls[0].requestID, "req-123")
	}
}

func TestRespondToPrompt_EmptyResponse(t *testing.T) {
	t.Parallel()

	mockSvc := newMockService()
	srv := New(mockSvc)

	body := `{"response": ""}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/operator/prompts/req-123/respond", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("requestId", "req-123")
	rec := httptest.NewRecorder()

	srv.respondToPrompt(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var errResp ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("unmarshaling response: %v", err)
	}
	if !strings.Contains(errResp.Error.Message, "response is required") {
		t.Errorf("error message = %q, want to contain 'response is required'", errResp.Error.Message)
	}
}
