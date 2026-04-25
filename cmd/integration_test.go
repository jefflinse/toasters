package cmd

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/auth"
	"github.com/jefflinse/toasters/internal/server"
	"github.com/jefflinse/toasters/internal/service"
)

// TestAuthTokenFlow verifies the auth token generation and loading flow
func TestAuthTokenFlow(t *testing.T) {
	// Create temp config dir
	tmpDir := t.TempDir()

	// EnsureToken creates a token
	token, err := auth.EnsureToken(tmpDir)
	if err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}
	if len(token) != 64 {
		t.Errorf("token length = %d, want 64", len(token))
	}

	// Verify token file exists with correct permissions
	info, err := os.Stat(filepath.Join(tmpDir, "server.token"))
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("token file permissions = %04o, want 0600", perm)
	}

	// LoadToken should return the same token
	loaded, err := auth.LoadToken(tmpDir)
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if loaded != token {
		t.Errorf("LoadToken = %q, want %q", loaded, token)
	}
}

// TestServerWithAuth verifies server auth middleware works correctly
func TestServerWithAuth(t *testing.T) {
	// Create temp config dir and token
	tmpDir := t.TempDir()
	token, err := auth.EnsureToken(tmpDir)
	if err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}

	// Create a mock service and server with the token
	mockSvc := newMockService()
	srv := server.New(mockSvc, server.WithToken(token))

	// Start server on random port
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Start(":0"); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer func() { _ = srv.Shutdown(ctx) }()

	addr := srv.Addr()
	if addr == "" {
		t.Fatal("server addr is empty")
	}

	// Health endpoint should work without auth
	t.Run("health_no_auth", func(t *testing.T) {
		resp, err := http.Get("http://" + addr + "/api/v1/health")
		if err != nil {
			t.Fatalf("health request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("health status = %d, want 200", resp.StatusCode)
		}
	})

	// Protected endpoint should fail without auth
	t.Run("protected_no_auth", func(t *testing.T) {
		resp, err := http.Get("http://" + addr + "/api/v1/operator/status")
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("unauth status = %d, want 401", resp.StatusCode)
		}
	})

	// Protected endpoint should work with valid auth
	t.Run("protected_with_auth", func(t *testing.T) {
		req, err := http.NewRequest("GET", "http://"+addr+"/api/v1/operator/status", nil)
		if err != nil {
			t.Fatalf("create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("auth status = %d, want 200", resp.StatusCode)
		}
	})

	// Protected endpoint should fail with wrong token
	t.Run("protected_wrong_token", func(t *testing.T) {
		req, err := http.NewRequest("GET", "http://"+addr+"/api/v1/operator/status", nil)
		if err != nil {
			t.Fatalf("create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer wrongtoken123")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("wrong token status = %d, want 401", resp.StatusCode)
		}
	})
}

// TestServerNoAuthMode verifies server works without auth when no token is provided
func TestServerNoAuthMode(t *testing.T) {
	mockSvc := newMockService()
	srv := server.New(mockSvc) // No token = no auth

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Start(":0"); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer srv.Shutdown(ctx)

	addr := srv.Addr()
	if addr == "" {
		t.Fatal("server addr is empty")
	}

	// Protected endpoint should work without auth
	resp, err := http.Get("http://" + addr + "/api/v1/operator/status")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200 (no auth required)", resp.StatusCode)
	}
}

// TestServerAddrReturnsActualPort verifies Addr() returns the actual listening address
func TestServerAddrReturnsActualPort(t *testing.T) {
	mockSvc := newMockService()
	srv := server.New(mockSvc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start on :0 to get a random port
	if err := srv.Start(":0"); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer func() { _ = srv.Shutdown(ctx) }()

	addr := srv.Addr()
	if addr == "" {
		t.Fatal("Addr() returned empty string")
	}
	// Should not be :0 - should be the actual port
	if addr == ":0" {
		t.Error("Addr() returned :0, expected actual port")
	}
}

// ---------------------------------------------------------------------------
// Mock Service Implementation
// ---------------------------------------------------------------------------

// mockService implements service.Service for testing
type mockService struct {
	operator    *mockOperatorService
	definitions *mockDefinitionService
	jobs        *mockJobService
	sessions    *mockSessionService
	events      *mockEventService
	system      *mockSystemService
}

func newMockService() *mockService {
	return &mockService{
		operator:    &mockOperatorService{},
		definitions: &mockDefinitionService{},
		jobs:        &mockJobService{},
		sessions:    &mockSessionService{},
		events:      &mockEventService{},
		system:      &mockSystemService{},
	}
}

func (m *mockService) Operator() service.OperatorService      { return m.operator }
func (m *mockService) Definitions() service.DefinitionService { return m.definitions }
func (m *mockService) Jobs() service.JobService               { return m.jobs }
func (m *mockService) Sessions() service.SessionService       { return m.sessions }
func (m *mockService) Events() service.EventService           { return m.events }
func (m *mockService) System() service.SystemService          { return m.system }
func (m *mockService) Shutdown(ctx context.Context) error     { return nil }

// mockOperatorService implements service.OperatorService
type mockOperatorService struct{}

func (m *mockOperatorService) SendMessage(ctx context.Context, message string) (string, error) {
	return "turn-123", nil
}
func (m *mockOperatorService) RespondToPrompt(ctx context.Context, requestID, response string) error {
	return nil
}
func (m *mockOperatorService) Status(ctx context.Context) (service.OperatorStatus, error) {
	return service.OperatorStatus{State: service.OperatorStateIdle, ModelName: "test-model"}, nil
}
func (m *mockOperatorService) History(ctx context.Context) ([]service.ChatEntry, error) {
	return nil, nil
}

// mockDefinitionService implements service.DefinitionService
type mockDefinitionService struct{}

func (m *mockDefinitionService) ListSkills(ctx context.Context) ([]service.Skill, error) {
	return nil, nil
}
func (m *mockDefinitionService) GetSkill(ctx context.Context, id string) (service.Skill, error) {
	return service.Skill{}, service.ErrNotFound
}
func (m *mockDefinitionService) CreateSkill(ctx context.Context, name string) (service.Skill, error) {
	return service.Skill{}, nil
}
func (m *mockDefinitionService) DeleteSkill(ctx context.Context, id string) error {
	return nil
}
func (m *mockDefinitionService) GenerateSkill(ctx context.Context, prompt string) (string, error) {
	return "op-123", nil
}
func (m *mockDefinitionService) ListWorkers(ctx context.Context) ([]service.Worker, error) {
	return nil, nil
}
func (m *mockDefinitionService) GetWorker(ctx context.Context, id string) (service.Worker, error) {
	return service.Worker{}, service.ErrNotFound
}
func (m *mockDefinitionService) ListGraphs(ctx context.Context) ([]service.GraphDefinition, error) {
	return nil, nil
}
func (m *mockDefinitionService) GetGraph(ctx context.Context, id string) (service.GraphDefinition, error) {
	return service.GraphDefinition{}, service.ErrNotFound
}

// mockJobService implements service.JobService
type mockJobService struct{}

func (m *mockJobService) List(ctx context.Context, filter *service.JobListFilter) ([]service.Job, error) {
	return nil, nil
}
func (m *mockJobService) ListAll(ctx context.Context) ([]service.Job, error) {
	return nil, nil
}
func (m *mockJobService) Get(ctx context.Context, id string) (service.JobDetail, error) {
	return service.JobDetail{}, service.ErrNotFound
}
func (m *mockJobService) Cancel(ctx context.Context, id string) error {
	return nil
}

// mockSessionService implements service.SessionService
type mockSessionService struct{}

func (m *mockSessionService) List(ctx context.Context) ([]service.SessionSnapshot, error) {
	return nil, nil
}
func (m *mockSessionService) Get(ctx context.Context, id string) (service.SessionDetail, error) {
	return service.SessionDetail{}, service.ErrNotFound
}
func (m *mockSessionService) Cancel(ctx context.Context, id string) error {
	return nil
}

// mockEventService implements service.EventService
type mockEventService struct{}

func (m *mockEventService) Subscribe(ctx context.Context) <-chan service.Event {
	ch := make(chan service.Event)
	close(ch)
	return ch
}

// mockSystemService implements service.SystemService
type mockSystemService struct{}

func (m *mockSystemService) Health(ctx context.Context) (service.HealthStatus, error) {
	return service.HealthStatus{Status: "ok", Version: "test", Uptime: 0}, nil
}
func (m *mockSystemService) ListModels(ctx context.Context) ([]service.ModelInfo, error) {
	return nil, nil
}
func (m *mockSystemService) ListMCPServers(ctx context.Context) ([]service.MCPServerStatus, error) {
	return nil, nil
}
func (m *mockSystemService) GetProgressState(ctx context.Context) (service.ProgressState, error) {
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
