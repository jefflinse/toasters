package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/service"
)

// TestServerGracefulShutdownWithSSEClients verifies that the server shuts down
// cleanly within 5 seconds even when SSE clients are connected.
//
// This test would fail without the CloseAllSSEConnections() call because:
// - SSE connections disable write deadlines for long-lived streams
// - Blocked writes on SSE connections would prevent http.Server.Shutdown() from completing
// - The test would timeout waiting for shutdown to finish
//
// To verify this test would fail without the fix:
// 1. Comment out the call to srv.CloseAllSSEConnections() before srv.Shutdown()
// 2. Run this test - it will timeout after 5 seconds
func TestServerGracefulShutdownWithSSEClients(t *testing.T) {
	t.Parallel()

	// Track active SSE connections.
	var activeConns atomic.Int32
	var connClosed atomic.Int32

	// Create a configurable mock service for shutdown tests.
	mock := newShutdownTestMock()
	mock.subscribeFn = func(ctx context.Context) <-chan service.Event {
		activeConns.Add(1)
		out := make(chan service.Event, 10)
		go func() {
			defer func() {
				connClosed.Add(1)
				close(out)
			}()
			// Keep the channel open until context is cancelled.
			<-ctx.Done()
		}()
		return out
	}

	// Find a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	// Create and start server with suppressed logging.
	srv := New(mock, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err := srv.Start(addr); err != nil {
		t.Fatalf("starting server: %v", err)
	}

	// Connect 3 SSE clients.
	const numClients = 3
	var wg sync.WaitGroup
	clientErrCh := make(chan error, numClients)

	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(clientNum int) {
			defer wg.Done()

			resp, err := http.Get("http://" + addr + "/api/v1/events")
			if err != nil {
				clientErrCh <- err
				return
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusOK {
				clientErrCh <- err
				return
			}

			// Read from the SSE stream until it closes.
			buf := make([]byte, 1024)
			for {
				_, err := resp.Body.Read(buf)
				if err != nil {
					// Connection closed - this is expected during shutdown.
					return
				}
			}
		}(i)
	}

	// Wait for all clients to connect.
	waitForClients := func() bool {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if activeConns.Load() >= int32(numClients) {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}

	if !waitForClients() {
		// Check for client connection errors.
		select {
		case err := <-clientErrCh:
			t.Fatalf("client failed to connect: %v", err)
		default:
			t.Fatalf("timed out waiting for clients to connect (got %d, want %d)",
				activeConns.Load(), numClients)
		}
	}

	// Now shutdown the server.
	// This should complete quickly because CloseAllSSEConnections() unblocks the SSE handlers.
	shutdownStart := time.Now()

	// First close all SSE connections (this is what makes shutdown fast).
	srv.CloseAllSSEConnections()

	// Then call Shutdown with a reasonable timeout.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	shutdownErr := srv.Shutdown(shutdownCtx)
	shutdownDuration := time.Since(shutdownStart)

	if shutdownErr != nil {
		t.Fatalf("server shutdown failed: %v", shutdownErr)
	}

	// Shutdown should complete well within 5 seconds.
	// Without CloseAllSSEConnections(), it would timeout.
	if shutdownDuration > 5*time.Second {
		t.Errorf("shutdown took %v, expected < 5s", shutdownDuration)
	}

	t.Logf("shutdown completed in %v", shutdownDuration)

	// Verify all SSE connections were closed.
	wg.Wait()

	// All clients should have detected the closure.
	closed := connClosed.Load()
	if closed != int32(numClients) {
		t.Errorf("closed connections = %d, want %d", closed, numClients)
	}
}

// TestServerShutdown_WithoutClosingSSEConnections documents the behavior
// when CloseAllSSEConnections() is NOT called before Shutdown().
//
// This test is skipped by default but can be enabled to verify that the fix works.
// Without calling CloseAllSSEConnections(), the shutdown would hang until the
// context deadline is reached.
func TestServerShutdown_WithoutClosingSSEConnections(t *testing.T) {
	t.Parallel()

	// Skip this test by default - it's for documentation/manual verification.
	// To run: go test -run TestServerShutdown_WithoutClosingSSEConnections
	t.Skip("Skipping test that demonstrates the bug - enable manually to verify fix")

	mock := newShutdownTestMock()
	mock.subscribeFn = func(ctx context.Context) <-chan service.Event {
		out := make(chan service.Event, 10)
		go func() {
			defer close(out)
			// Keep channel open until context cancelled.
			<-ctx.Done()
		}()
		return out
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	srv := New(mock, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err := srv.Start(addr); err != nil {
		t.Fatalf("starting server: %v", err)
	}

	// Connect one SSE client.
	var clientDone atomic.Bool
	go func() {
		resp, err := http.Get("http://" + addr + "/api/v1/events")
		if err != nil {
			return
		}
		defer func() { _ = resp.Body.Close() }()

		buf := make([]byte, 1024)
		for {
			_, err := resp.Body.Read(buf)
			if err != nil {
				clientDone.Store(true)
				return
			}
		}
	}()

	// Wait for client to connect.
	time.Sleep(100 * time.Millisecond)

	// Shutdown WITHOUT calling CloseAllSSEConnections().
	// This will hang until the context deadline because the SSE handler
	// is blocked waiting for events.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	err = srv.Shutdown(shutdownCtx)
	duration := time.Since(start)

	// Without the fix, this would timeout and return context.DeadlineExceeded.
	// With the fix (if we called CloseAllSSEConnections), it would succeed quickly.
	t.Logf("shutdown completed in %v with error: %v", duration, err)
}

// shutdownTestMock is a configurable mock service for shutdown tests.
type shutdownTestMock struct {
	subscribeFn        func(ctx context.Context) <-chan service.Event
	getProgressStateFn func(ctx context.Context) (service.ProgressState, error)
}

func newShutdownTestMock() *shutdownTestMock {
	return &shutdownTestMock{}
}

func (m *shutdownTestMock) Operator() service.OperatorService {
	return &shutdownTestOperator{}
}

func (m *shutdownTestMock) Definitions() service.DefinitionService {
	return &shutdownTestDefinitions{}
}

func (m *shutdownTestMock) Jobs() service.JobService {
	return &shutdownTestJobs{}
}

func (m *shutdownTestMock) Sessions() service.SessionService {
	return &shutdownTestSessions{}
}

func (m *shutdownTestMock) Events() service.EventService {
	return &shutdownTestEvents{m}
}

func (m *shutdownTestMock) System() service.SystemService {
	return &shutdownTestSystem{m}
}

// shutdownTestOperator implements service.OperatorService.
type shutdownTestOperator struct{}

func (m *shutdownTestOperator) SendMessage(ctx context.Context, message string) (string, error) {
	return "", nil
}
func (m *shutdownTestOperator) RespondToPrompt(ctx context.Context, requestID, response string) error {
	return nil
}
func (m *shutdownTestOperator) Status(ctx context.Context) (service.OperatorStatus, error) {
	return service.OperatorStatus{}, nil
}
func (m *shutdownTestOperator) History(ctx context.Context) ([]service.ChatEntry, error) {
	return nil, nil
}
func (m *shutdownTestOperator) RespondToBlocker(ctx context.Context, jobID, taskID string, answers []string) error {
	return nil
}

// shutdownTestDefinitions implements service.DefinitionService.
type shutdownTestDefinitions struct{}

func (m *shutdownTestDefinitions) ListSkills(ctx context.Context) ([]service.Skill, error) {
	return nil, nil
}
func (m *shutdownTestDefinitions) GetSkill(ctx context.Context, id string) (service.Skill, error) {
	return service.Skill{}, nil
}
func (m *shutdownTestDefinitions) CreateSkill(ctx context.Context, name string) (service.Skill, error) {
	return service.Skill{}, nil
}
func (m *shutdownTestDefinitions) DeleteSkill(ctx context.Context, id string) error {
	return nil
}
func (m *shutdownTestDefinitions) GenerateSkill(ctx context.Context, prompt string) (string, error) {
	return "", nil
}
func (m *shutdownTestDefinitions) ListAgents(ctx context.Context) ([]service.Agent, error) {
	return nil, nil
}
func (m *shutdownTestDefinitions) GetAgent(ctx context.Context, id string) (service.Agent, error) {
	return service.Agent{}, nil
}
func (m *shutdownTestDefinitions) CreateAgent(ctx context.Context, name string) (service.Agent, error) {
	return service.Agent{}, nil
}
func (m *shutdownTestDefinitions) DeleteAgent(ctx context.Context, id string) error {
	return nil
}
func (m *shutdownTestDefinitions) AddSkillToAgent(ctx context.Context, agentID, skillName string) error {
	return nil
}
func (m *shutdownTestDefinitions) GenerateAgent(ctx context.Context, prompt string) (string, error) {
	return "", nil
}
func (m *shutdownTestDefinitions) ListTeams(ctx context.Context) ([]service.TeamView, error) {
	return nil, nil
}
func (m *shutdownTestDefinitions) GetTeam(ctx context.Context, id string) (service.TeamView, error) {
	return service.TeamView{}, nil
}
func (m *shutdownTestDefinitions) CreateTeam(ctx context.Context, name string) (service.TeamView, error) {
	return service.TeamView{}, nil
}
func (m *shutdownTestDefinitions) DeleteTeam(ctx context.Context, id string) error {
	return nil
}
func (m *shutdownTestDefinitions) AddAgentToTeam(ctx context.Context, teamID, agentID string) error {
	return nil
}
func (m *shutdownTestDefinitions) SetCoordinator(ctx context.Context, teamID, agentName string) error {
	return nil
}
func (m *shutdownTestDefinitions) PromoteTeam(ctx context.Context, teamID string) (string, error) {
	return "", nil
}
func (m *shutdownTestDefinitions) GenerateTeam(ctx context.Context, prompt string) (string, error) {
	return "", nil
}
func (m *shutdownTestDefinitions) DetectCoordinator(ctx context.Context, teamID string) (string, error) {
	return "", nil
}

// shutdownTestJobs implements service.JobService.
type shutdownTestJobs struct{}

func (m *shutdownTestJobs) List(ctx context.Context, filter *service.JobListFilter) ([]service.Job, error) {
	return nil, nil
}
func (m *shutdownTestJobs) ListAll(ctx context.Context) ([]service.Job, error) {
	return nil, nil
}
func (m *shutdownTestJobs) Get(ctx context.Context, id string) (service.JobDetail, error) {
	return service.JobDetail{}, nil
}
func (m *shutdownTestJobs) Cancel(ctx context.Context, id string) error {
	return nil
}

// shutdownTestSessions implements service.SessionService.
type shutdownTestSessions struct{}

func (m *shutdownTestSessions) List(ctx context.Context) ([]service.SessionSnapshot, error) {
	return nil, nil
}
func (m *shutdownTestSessions) Get(ctx context.Context, id string) (service.SessionDetail, error) {
	return service.SessionDetail{}, nil
}
func (m *shutdownTestSessions) Cancel(ctx context.Context, id string) error {
	return nil
}

// shutdownTestEvents implements service.EventService.
type shutdownTestEvents struct {
	mock *shutdownTestMock
}

func (m *shutdownTestEvents) Subscribe(ctx context.Context) <-chan service.Event {
	if m.mock.subscribeFn != nil {
		return m.mock.subscribeFn(ctx)
	}
	ch := make(chan service.Event)
	close(ch)
	return ch
}

// shutdownTestSystem implements service.SystemService.
type shutdownTestSystem struct {
	mock *shutdownTestMock
}

func (m *shutdownTestSystem) Health(ctx context.Context) (service.HealthStatus, error) {
	return service.HealthStatus{}, nil
}
func (m *shutdownTestSystem) ListModels(ctx context.Context) ([]service.ModelInfo, error) {
	return nil, nil
}
func (m *shutdownTestSystem) ListMCPServers(ctx context.Context) ([]service.MCPServerStatus, error) {
	return nil, nil
}
func (m *shutdownTestSystem) GetProgressState(ctx context.Context) (service.ProgressState, error) {
	if m.mock.getProgressStateFn != nil {
		return m.mock.getProgressStateFn(ctx)
	}
	return service.ProgressState{}, nil
}

func (m *shutdownTestSystem) GetLogs(_ context.Context) (string, error) {
	return "", nil
}
func (m *shutdownTestSystem) ListCatalogProviders(_ context.Context) ([]service.CatalogProvider, error) {
	return nil, nil
}
func (m *shutdownTestSystem) AddProvider(_ context.Context, _ service.AddProviderRequest) error {
	return nil
}
func (m *shutdownTestSystem) UpdateProvider(_ context.Context, _ service.AddProviderRequest) error {
	return nil
}
func (m *shutdownTestSystem) ListConfiguredProviderIDs(_ context.Context) ([]string, error) {
	return nil, nil
}
func (m *shutdownTestSystem) SetOperatorProvider(_ context.Context, _ string) error { return nil }
