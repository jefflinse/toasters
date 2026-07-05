package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	knowledge  *mockKnowledgeService
}

func newMockService() *mockService {
	return &mockService{
		operator:   &mockOperatorService{},
		definition: &mockDefinitionService{},
		jobs:       &mockJobService{},
		sessions:   &mockSessionService{},
		events:     &mockEventService{},
		system:     &mockSystemService{},
		knowledge:  &mockKnowledgeService{},
	}
}

func (m *mockService) Operator() service.OperatorService { return m.operator }
func (m *mockService) Definitions() service.DefinitionService {
	return m.definition
}
func (m *mockService) Jobs() service.JobService            { return m.jobs }
func (m *mockService) Sessions() service.SessionService    { return m.sessions }
func (m *mockService) Events() service.EventService        { return m.events }
func (m *mockService) System() service.SystemService       { return m.system }
func (m *mockService) Knowledge() service.KnowledgeService { return m.knowledge }

type mockOperatorService struct {
	respondToPromptErr   error
	respondToPromptCalls []struct {
		requestID string
		response  string
	}
	blockers    []service.Blocker
	blockersErr error
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

func (m *mockOperatorService) Blockers(_ context.Context) ([]service.Blocker, error) {
	return m.blockers, m.blockersErr
}

func (m *mockOperatorService) DismissPrompt(_ context.Context, _ string) error { return nil }

func (m *mockOperatorService) BlockerHistory(_ context.Context, _ int) ([]service.BlockerRecord, error) {
	return nil, nil
}

type mockDefinitionService struct{}
type mockJobService struct{}
type mockSessionService struct{}
type mockEventService struct{ ch chan service.Event }
type mockSystemService struct {
	metrics service.MetricsReport
}
type mockKnowledgeService struct {
	notes      []service.NoteMeta
	notesErr   error
	content    string
	contentErr error
}

func (m *mockKnowledgeService) ListJobNotes(_ context.Context, _ string) ([]service.NoteMeta, error) {
	return m.notes, m.notesErr
}
func (m *mockKnowledgeService) ReadJobNote(_ context.Context, _, _ string) (string, error) {
	return m.content, m.contentErr
}

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

func (m *mockJobService) List(_ context.Context, _ *service.JobListFilter) ([]service.Job, error) {
	return nil, nil
}
func (m *mockJobService) ListAll(_ context.Context) ([]service.Job, error) {
	return nil, nil
}
func (m *mockJobService) Get(_ context.Context, _ string) (service.JobDetail, error) {
	return service.JobDetail{}, nil
}
func (m *mockJobService) Cancel(_ context.Context, _ string) error    { return nil }
func (m *mockJobService) RetryTask(_ context.Context, _ string) error { return nil }

func (m *mockSessionService) List(_ context.Context) ([]service.SessionSnapshot, error) {
	return nil, nil
}
func (m *mockSessionService) Get(_ context.Context, _ string) (service.SessionDetail, error) {
	return service.SessionDetail{}, nil
}
func (m *mockSessionService) Cancel(_ context.Context, _ string) error { return nil }

func (m *mockEventService) Subscribe(_ context.Context) <-chan service.Event {
	return m.ch
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
func (m *mockSystemService) Metrics(_ context.Context) (service.MetricsReport, error) {
	return m.metrics, nil
}

// ---------------------------------------------------------------------------
// Handler tests
// ---------------------------------------------------------------------------

func TestOperatorBlockers_ReturnsQueue(t *testing.T) {
	t.Parallel()

	mockSvc := newMockService()
	mockSvc.operator.blockers = []service.Blocker{
		{RequestID: "req-1", Source: "graph:investigate", JobID: "job-1", TaskID: "task-1", Questions: []service.PromptQuestion{{Question: "Which?", Options: []string{"a", "b"}}}},
	}
	srv := New(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/operator/blockers", nil)
	rec := httptest.NewRecorder()
	srv.operatorBlockers(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var resp PaginatedResponse[wireBlockerPayload]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Total != 1 || len(resp.Items) != 1 {
		t.Fatalf("Total/Items = %d/%d, want 1/1", resp.Total, len(resp.Items))
	}
	got := resp.Items[0]
	if got.RequestID != "req-1" || got.Source != "graph:investigate" {
		t.Errorf("item = %+v, want req-1 / graph:investigate", got)
	}
	if got.JobID != "job-1" || got.TaskID != "task-1" {
		t.Errorf("job/task = %q/%q, want job-1/task-1", got.JobID, got.TaskID)
	}
	if len(got.Questions) != 1 || got.Questions[0].Question != "Which?" {
		t.Errorf("questions = %v, want one 'Which?'", got.Questions)
	}
}

func TestGetMetrics_RoundTrip(t *testing.T) {
	t.Parallel()

	mockSvc := newMockService()
	mockSvc.system.metrics = service.MetricsReport{
		Nodes: []service.NodeMetric{
			{Node: "implement", Runs: 4, Failures: 1, FailureRate: 0.25, AvgElapsedMS: 1500, MinElapsedMS: 800, MaxElapsedMS: 3000},
		},
		Sessions: []service.SessionMetric{
			{WorkerID: "coder", Sessions: 3, Failures: 0, AvgTokensIn: 1200, AvgTokensOut: 300, UsageUnavailable: 1, AvgContextPercent: 0.42},
		},
	}
	srv := New(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	rec := httptest.NewRecorder()
	srv.getMetrics(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var resp wireMetricsReport
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Nodes) != 1 || resp.Nodes[0].Node != "implement" || resp.Nodes[0].Runs != 4 {
		t.Fatalf("nodes = %+v, want one implement/4-runs row", resp.Nodes)
	}
	if resp.Nodes[0].Failures != 1 || resp.Nodes[0].FailureRate != 0.25 {
		t.Errorf("failures/rate = %d/%v, want 1/0.25", resp.Nodes[0].Failures, resp.Nodes[0].FailureRate)
	}
	if len(resp.Sessions) != 1 || resp.Sessions[0].WorkerID != "coder" {
		t.Fatalf("sessions = %+v, want one coder row", resp.Sessions)
	}
	if resp.Sessions[0].UsageUnavailable != 1 {
		t.Errorf("usage_unavailable = %d, want 1", resp.Sessions[0].UsageUnavailable)
	}
}

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

// ---------------------------------------------------------------------------
// Knowledge (job notes) handlers
// ---------------------------------------------------------------------------

func TestListJobNotes_ReturnsNotes(t *testing.T) {
	t.Parallel()

	mockSvc := newMockService()
	mockSvc.knowledge.notes = []service.NoteMeta{
		{ID: "20260702-120000.000-worker-hello-abc123", Title: "Hello", Source: "worker", ModTime: time.Now(), Size: 42},
	}
	srv := New(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-1/notes", nil)
	req.SetPathValue("id", "job-1")
	rec := httptest.NewRecorder()
	srv.listJobNotes(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp PaginatedResponse[wireNoteMeta]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Total != 1 || len(resp.Items) != 1 {
		t.Fatalf("Total/Items = %d/%d, want 1/1", resp.Total, len(resp.Items))
	}
	got := resp.Items[0]
	if got.ID != "20260702-120000.000-worker-hello-abc123" || got.Title != "Hello" || got.Source != "worker" {
		t.Errorf("item = %+v, want id/title/source hello-abc123 / Hello / worker", got)
	}
}

func TestListJobNotes_JobNotFound(t *testing.T) {
	t.Parallel()

	mockSvc := newMockService()
	mockSvc.knowledge.notesErr = fmt.Errorf("getting job job-missing: %w", service.ErrNotFound)
	srv := New(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-missing/notes", nil)
	req.SetPathValue("id", "job-missing")
	rec := httptest.NewRecorder()
	srv.listJobNotes(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestGetJobNote_ReturnsContent(t *testing.T) {
	t.Parallel()

	mockSvc := newMockService()
	mockSvc.knowledge.content = "# Hello\n\nBody"
	srv := New(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-1/notes/note-1", nil)
	req.SetPathValue("id", "job-1")
	req.SetPathValue("noteID", "note-1")
	rec := httptest.NewRecorder()
	srv.getJobNote(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp noteContentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Content != "# Hello\n\nBody" {
		t.Errorf("content = %q, want %q", resp.Content, "# Hello\n\nBody")
	}
}

func TestGetJobNote_NotFound(t *testing.T) {
	t.Parallel()

	mockSvc := newMockService()
	mockSvc.knowledge.contentErr = fmt.Errorf("note note-missing: %w", service.ErrNotFound)
	srv := New(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-1/notes/note-missing", nil)
	req.SetPathValue("id", "job-1")
	req.SetPathValue("noteID", "note-missing")
	rec := httptest.NewRecorder()
	srv.getJobNote(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
