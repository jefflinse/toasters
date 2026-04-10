package client_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/client"
	"github.com/jefflinse/toasters/internal/server"
	"github.com/jefflinse/toasters/internal/service"
)

// ---------------------------------------------------------------------------
// Test timestamp
// ---------------------------------------------------------------------------

var testTime = time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)

// ---------------------------------------------------------------------------
// Mock service implementation
// ---------------------------------------------------------------------------

type mockService struct {
	// Operator
	sendMessageFn      func(ctx context.Context, msg string) (string, error)
	respondToPromptFn  func(ctx context.Context, reqID, resp string) error
	statusFn           func(ctx context.Context) (service.OperatorStatus, error)
	historyFn          func(ctx context.Context) ([]service.ChatEntry, error)
	respondToBlockerFn func(ctx context.Context, jobID, taskID string, answers []string) error

	// Definitions
	listSkillsFn        func(ctx context.Context) ([]service.Skill, error)
	getSkillFn          func(ctx context.Context, id string) (service.Skill, error)
	createSkillFn       func(ctx context.Context, name string) (service.Skill, error)
	deleteSkillFn       func(ctx context.Context, id string) error
	generateSkillFn     func(ctx context.Context, prompt string) (string, error)
	listAgentsFn        func(ctx context.Context) ([]service.Agent, error)
	getAgentFn          func(ctx context.Context, id string) (service.Agent, error)
	createAgentFn       func(ctx context.Context, name string) (service.Agent, error)
	deleteAgentFn       func(ctx context.Context, id string) error
	addSkillToAgentFn   func(ctx context.Context, agentID, skillName string) error
	generateAgentFn     func(ctx context.Context, prompt string) (string, error)
	listTeamsFn         func(ctx context.Context) ([]service.TeamView, error)
	getTeamFn           func(ctx context.Context, id string) (service.TeamView, error)
	createTeamFn        func(ctx context.Context, name string) (service.TeamView, error)
	deleteTeamFn        func(ctx context.Context, id string) error
	addAgentToTeamFn    func(ctx context.Context, teamID, agentID string) error
	setCoordinatorFn    func(ctx context.Context, teamID, agentName string) error
	promoteTeamFn       func(ctx context.Context, teamID string) (string, error)
	generateTeamFn      func(ctx context.Context, prompt string) (string, error)
	detectCoordinatorFn func(ctx context.Context, teamID string) (string, error)

	// Jobs
	listJobsFn    func(ctx context.Context, filter *service.JobListFilter) ([]service.Job, error)
	listAllJobsFn func(ctx context.Context) ([]service.Job, error)
	getJobFn      func(ctx context.Context, id string) (service.JobDetail, error)
	cancelJobFn   func(ctx context.Context, id string) error

	// Sessions
	listSessionsFn  func(ctx context.Context) ([]service.SessionSnapshot, error)
	getSessionFn    func(ctx context.Context, id string) (service.SessionDetail, error)
	cancelSessionFn func(ctx context.Context, id string) error

	// Events
	subscribeFn func(ctx context.Context) <-chan service.Event

	// System
	healthFn           func(ctx context.Context) (service.HealthStatus, error)
	listModelsFn       func(ctx context.Context) ([]service.ModelInfo, error)
	listMCPServersFn   func(ctx context.Context) ([]service.MCPServerStatus, error)
	getProgressStateFn func(ctx context.Context) (service.ProgressState, error)
	getLogsFn          func(ctx context.Context) (string, error)
}

func (m *mockService) Operator() service.OperatorService      { return &mockOperator{m} }
func (m *mockService) Definitions() service.DefinitionService { return &mockDefinitions{m} }
func (m *mockService) Jobs() service.JobService               { return &mockJobs{m} }
func (m *mockService) Sessions() service.SessionService       { return &mockSessions{m} }
func (m *mockService) Events() service.EventService           { return &mockEvents{m} }
func (m *mockService) System() service.SystemService          { return &mockSystem{m} }

// --- Operator ---

type mockOperator struct{ s *mockService }

func (o *mockOperator) SendMessage(ctx context.Context, msg string) (string, error) {
	if o.s.sendMessageFn != nil {
		return o.s.sendMessageFn(ctx, msg)
	}
	return "", nil
}

func (o *mockOperator) RespondToPrompt(ctx context.Context, reqID, resp string) error {
	if o.s.respondToPromptFn != nil {
		return o.s.respondToPromptFn(ctx, reqID, resp)
	}
	return nil
}

func (o *mockOperator) Status(ctx context.Context) (service.OperatorStatus, error) {
	if o.s.statusFn != nil {
		return o.s.statusFn(ctx)
	}
	return service.OperatorStatus{}, nil
}

func (o *mockOperator) History(ctx context.Context) ([]service.ChatEntry, error) {
	if o.s.historyFn != nil {
		return o.s.historyFn(ctx)
	}
	return nil, nil
}

func (o *mockOperator) RespondToBlocker(ctx context.Context, jobID, taskID string, answers []string) error {
	if o.s.respondToBlockerFn != nil {
		return o.s.respondToBlockerFn(ctx, jobID, taskID, answers)
	}
	return nil
}

// --- Definitions ---

type mockDefinitions struct{ s *mockService }

func (d *mockDefinitions) ListSkills(ctx context.Context) ([]service.Skill, error) {
	if d.s.listSkillsFn != nil {
		return d.s.listSkillsFn(ctx)
	}
	return nil, nil
}

func (d *mockDefinitions) GetSkill(ctx context.Context, id string) (service.Skill, error) {
	if d.s.getSkillFn != nil {
		return d.s.getSkillFn(ctx, id)
	}
	return service.Skill{}, nil
}

func (d *mockDefinitions) CreateSkill(ctx context.Context, name string) (service.Skill, error) {
	if d.s.createSkillFn != nil {
		return d.s.createSkillFn(ctx, name)
	}
	return service.Skill{}, nil
}

func (d *mockDefinitions) DeleteSkill(ctx context.Context, id string) error {
	if d.s.deleteSkillFn != nil {
		return d.s.deleteSkillFn(ctx, id)
	}
	return nil
}

func (d *mockDefinitions) GenerateSkill(ctx context.Context, prompt string) (string, error) {
	if d.s.generateSkillFn != nil {
		return d.s.generateSkillFn(ctx, prompt)
	}
	return "", nil
}

func (d *mockDefinitions) ListAgents(ctx context.Context) ([]service.Agent, error) {
	if d.s.listAgentsFn != nil {
		return d.s.listAgentsFn(ctx)
	}
	return nil, nil
}

func (d *mockDefinitions) GetAgent(ctx context.Context, id string) (service.Agent, error) {
	if d.s.getAgentFn != nil {
		return d.s.getAgentFn(ctx, id)
	}
	return service.Agent{}, nil
}

func (d *mockDefinitions) CreateAgent(ctx context.Context, name string) (service.Agent, error) {
	if d.s.createAgentFn != nil {
		return d.s.createAgentFn(ctx, name)
	}
	return service.Agent{}, nil
}

func (d *mockDefinitions) DeleteAgent(ctx context.Context, id string) error {
	if d.s.deleteAgentFn != nil {
		return d.s.deleteAgentFn(ctx, id)
	}
	return nil
}

func (d *mockDefinitions) AddSkillToAgent(ctx context.Context, agentID, skillName string) error {
	if d.s.addSkillToAgentFn != nil {
		return d.s.addSkillToAgentFn(ctx, agentID, skillName)
	}
	return nil
}

func (d *mockDefinitions) GenerateAgent(ctx context.Context, prompt string) (string, error) {
	if d.s.generateAgentFn != nil {
		return d.s.generateAgentFn(ctx, prompt)
	}
	return "", nil
}

func (d *mockDefinitions) ListTeams(ctx context.Context) ([]service.TeamView, error) {
	if d.s.listTeamsFn != nil {
		return d.s.listTeamsFn(ctx)
	}
	return nil, nil
}

func (d *mockDefinitions) GetTeam(ctx context.Context, id string) (service.TeamView, error) {
	if d.s.getTeamFn != nil {
		return d.s.getTeamFn(ctx, id)
	}
	return service.TeamView{}, nil
}

func (d *mockDefinitions) CreateTeam(ctx context.Context, name string) (service.TeamView, error) {
	if d.s.createTeamFn != nil {
		return d.s.createTeamFn(ctx, name)
	}
	return service.TeamView{}, nil
}

func (d *mockDefinitions) DeleteTeam(ctx context.Context, id string) error {
	if d.s.deleteTeamFn != nil {
		return d.s.deleteTeamFn(ctx, id)
	}
	return nil
}

func (d *mockDefinitions) AddAgentToTeam(ctx context.Context, teamID, agentID string) error {
	if d.s.addAgentToTeamFn != nil {
		return d.s.addAgentToTeamFn(ctx, teamID, agentID)
	}
	return nil
}

func (d *mockDefinitions) SetCoordinator(ctx context.Context, teamID, agentName string) error {
	if d.s.setCoordinatorFn != nil {
		return d.s.setCoordinatorFn(ctx, teamID, agentName)
	}
	return nil
}

func (d *mockDefinitions) PromoteTeam(ctx context.Context, teamID string) (string, error) {
	if d.s.promoteTeamFn != nil {
		return d.s.promoteTeamFn(ctx, teamID)
	}
	return "", nil
}

func (d *mockDefinitions) GenerateTeam(ctx context.Context, prompt string) (string, error) {
	if d.s.generateTeamFn != nil {
		return d.s.generateTeamFn(ctx, prompt)
	}
	return "", nil
}

func (d *mockDefinitions) DetectCoordinator(ctx context.Context, teamID string) (string, error) {
	if d.s.detectCoordinatorFn != nil {
		return d.s.detectCoordinatorFn(ctx, teamID)
	}
	return "", nil
}

// --- Jobs ---

type mockJobs struct{ s *mockService }

func (j *mockJobs) List(ctx context.Context, filter *service.JobListFilter) ([]service.Job, error) {
	if j.s.listJobsFn != nil {
		return j.s.listJobsFn(ctx, filter)
	}
	return nil, nil
}

func (j *mockJobs) ListAll(ctx context.Context) ([]service.Job, error) {
	if j.s.listAllJobsFn != nil {
		return j.s.listAllJobsFn(ctx)
	}
	return nil, nil
}

func (j *mockJobs) Get(ctx context.Context, id string) (service.JobDetail, error) {
	if j.s.getJobFn != nil {
		return j.s.getJobFn(ctx, id)
	}
	return service.JobDetail{}, nil
}

func (j *mockJobs) Cancel(ctx context.Context, id string) error {
	if j.s.cancelJobFn != nil {
		return j.s.cancelJobFn(ctx, id)
	}
	return nil
}

// --- Sessions ---

type mockSessions struct{ s *mockService }

func (ss *mockSessions) List(ctx context.Context) ([]service.SessionSnapshot, error) {
	if ss.s.listSessionsFn != nil {
		return ss.s.listSessionsFn(ctx)
	}
	return nil, nil
}

func (ss *mockSessions) Get(ctx context.Context, id string) (service.SessionDetail, error) {
	if ss.s.getSessionFn != nil {
		return ss.s.getSessionFn(ctx, id)
	}
	return service.SessionDetail{}, nil
}

func (ss *mockSessions) Cancel(ctx context.Context, id string) error {
	if ss.s.cancelSessionFn != nil {
		return ss.s.cancelSessionFn(ctx, id)
	}
	return nil
}

// --- Events ---

type mockEvents struct{ s *mockService }

func (e *mockEvents) Subscribe(ctx context.Context) <-chan service.Event {
	if e.s.subscribeFn != nil {
		return e.s.subscribeFn(ctx)
	}
	ch := make(chan service.Event)
	close(ch)
	return ch
}

// --- System ---

type mockSystem struct{ s *mockService }

func (sys *mockSystem) Health(ctx context.Context) (service.HealthStatus, error) {
	if sys.s.healthFn != nil {
		return sys.s.healthFn(ctx)
	}
	return service.HealthStatus{}, nil
}

func (sys *mockSystem) ListModels(ctx context.Context) ([]service.ModelInfo, error) {
	if sys.s.listModelsFn != nil {
		return sys.s.listModelsFn(ctx)
	}
	return nil, nil
}

func (sys *mockSystem) ListMCPServers(ctx context.Context) ([]service.MCPServerStatus, error) {
	if sys.s.listMCPServersFn != nil {
		return sys.s.listMCPServersFn(ctx)
	}
	return nil, nil
}

func (sys *mockSystem) GetProgressState(ctx context.Context) (service.ProgressState, error) {
	if sys.s.getProgressStateFn != nil {
		return sys.s.getProgressStateFn(ctx)
	}
	return service.ProgressState{}, nil
}

func (sys *mockSystem) GetLogs(ctx context.Context) (string, error) {
	if sys.s.getLogsFn != nil {
		return sys.s.getLogsFn(ctx)
	}
	return "", nil
}
func (sys *mockSystem) ListCatalogProviders(_ context.Context) ([]service.CatalogProvider, error) {
	return nil, nil
}
func (sys *mockSystem) AddProvider(_ context.Context, _ service.AddProviderRequest) error {
	return nil
}

// ---------------------------------------------------------------------------
// Test helper: start a real server with a mock service, return a client
// ---------------------------------------------------------------------------

// setupTestServer starts a real server.Server wrapping the given mock service,
// creates a client.RemoteClient pointing at it, and registers cleanup.
func setupTestServer(t *testing.T, svc service.Service) *client.RemoteClient {
	t.Helper()

	// Find a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	// Suppress server logging in tests.
	srv := server.New(svc, server.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err := srv.Start(addr); err != nil {
		t.Fatalf("starting server: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	rc, err := client.New(fmt.Sprintf("http://%s", addr))
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}
	t.Cleanup(func() { rc.Close() })

	return rc
}

// ---------------------------------------------------------------------------
// Integration tests
// ---------------------------------------------------------------------------

func TestOperator_SendMessage(t *testing.T) {
	t.Parallel()

	mock := &mockService{
		sendMessageFn: func(_ context.Context, msg string) (string, error) {
			if msg != "hello operator" {
				t.Errorf("unexpected message: %q", msg)
			}
			return "turn-123", nil
		},
	}

	rc := setupTestServer(t, mock)
	ctx := context.Background()

	turnID, err := rc.Operator().SendMessage(ctx, "hello operator")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if turnID != "turn-123" {
		t.Errorf("got turnID %q, want %q", turnID, "turn-123")
	}
}

func TestOperator_Status(t *testing.T) {
	t.Parallel()

	mock := &mockService{
		statusFn: func(_ context.Context) (service.OperatorStatus, error) {
			return service.OperatorStatus{
				State:         service.OperatorStateIdle,
				CurrentTurnID: "",
				ModelName:     "test-model",
			}, nil
		},
	}

	rc := setupTestServer(t, mock)
	ctx := context.Background()

	st, err := rc.Operator().Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.State != service.OperatorStateIdle {
		t.Errorf("got state %q, want %q", st.State, service.OperatorStateIdle)
	}
	if st.ModelName != "test-model" {
		t.Errorf("got model %q, want %q", st.ModelName, "test-model")
	}
}

func TestOperator_History(t *testing.T) {
	t.Parallel()

	mock := &mockService{
		historyFn: func(_ context.Context) ([]service.ChatEntry, error) {
			return []service.ChatEntry{
				{
					Message:   service.ChatMessage{Role: service.MessageRoleUser, Content: "hi"},
					Timestamp: testTime,
				},
				{
					Message:    service.ChatMessage{Role: service.MessageRoleAssistant, Content: "hello"},
					Timestamp:  testTime.Add(time.Second),
					Reasoning:  "thinking...",
					ClaudeMeta: "operator · test-model",
				},
			}, nil
		},
	}

	rc := setupTestServer(t, mock)
	ctx := context.Background()

	entries, err := rc.Operator().History(ctx)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}

	if entries[0].Message.Role != service.MessageRoleUser {
		t.Errorf("entry[0] role = %q, want %q", entries[0].Message.Role, service.MessageRoleUser)
	}
	if entries[0].Message.Content != "hi" {
		t.Errorf("entry[0] content = %q, want %q", entries[0].Message.Content, "hi")
	}

	if entries[1].Message.Role != service.MessageRoleAssistant {
		t.Errorf("entry[1] role = %q, want %q", entries[1].Message.Role, service.MessageRoleAssistant)
	}
	if entries[1].Reasoning != "thinking..." {
		t.Errorf("entry[1] reasoning = %q, want %q", entries[1].Reasoning, "thinking...")
	}
	if entries[1].ClaudeMeta != "operator · test-model" {
		t.Errorf("entry[1] claude_meta = %q, want %q", entries[1].ClaudeMeta, "operator · test-model")
	}
}

func TestJobs_List(t *testing.T) {
	t.Parallel()

	mock := &mockService{
		listJobsFn: func(_ context.Context, _ *service.JobListFilter) ([]service.Job, error) {
			return []service.Job{
				{
					ID:          "job-1",
					Title:       "Fix bug",
					Description: "Fix the login bug",
					Type:        "bug_fix",
					Status:      service.JobStatusActive,
					CreatedAt:   testTime,
					UpdatedAt:   testTime,
				},
				{
					ID:          "job-2",
					Title:       "Add feature",
					Description: "Add dark mode",
					Type:        "new_feature",
					Status:      service.JobStatusPending,
					CreatedAt:   testTime,
					UpdatedAt:   testTime,
				},
			}, nil
		},
	}

	rc := setupTestServer(t, mock)
	ctx := context.Background()

	jobs, err := rc.Jobs().List(ctx, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(jobs))
	}

	if jobs[0].ID != "job-1" {
		t.Errorf("jobs[0].ID = %q, want %q", jobs[0].ID, "job-1")
	}
	if jobs[0].Title != "Fix bug" {
		t.Errorf("jobs[0].Title = %q, want %q", jobs[0].Title, "Fix bug")
	}
	if jobs[0].Status != service.JobStatusActive {
		t.Errorf("jobs[0].Status = %q, want %q", jobs[0].Status, service.JobStatusActive)
	}

	if jobs[1].ID != "job-2" {
		t.Errorf("jobs[1].ID = %q, want %q", jobs[1].ID, "job-2")
	}
	if jobs[1].Type != "new_feature" {
		t.Errorf("jobs[1].Type = %q, want %q", jobs[1].Type, "new_feature")
	}
}

func TestJobs_Get(t *testing.T) {
	t.Parallel()

	mock := &mockService{
		getJobFn: func(_ context.Context, id string) (service.JobDetail, error) {
			if id != "job-42" {
				return service.JobDetail{}, fmt.Errorf("%w: job %s", service.ErrNotFound, id)
			}
			return service.JobDetail{
				Job: service.Job{
					ID:     "job-42",
					Title:  "Test job",
					Status: service.JobStatusActive,
				},
				Tasks: []service.Task{
					{
						ID:     "task-1",
						JobID:  "job-42",
						Title:  "Subtask 1",
						Status: service.TaskStatusInProgress,
					},
				},
				Progress: []service.ProgressReport{
					{
						ID:      1,
						JobID:   "job-42",
						TaskID:  "task-1",
						Status:  "in_progress",
						Message: "Working on it",
					},
				},
			}, nil
		},
	}

	rc := setupTestServer(t, mock)
	ctx := context.Background()

	jd, err := rc.Jobs().Get(ctx, "job-42")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if jd.Job.ID != "job-42" {
		t.Errorf("job.ID = %q, want %q", jd.Job.ID, "job-42")
	}
	if jd.Job.Title != "Test job" {
		t.Errorf("job.Title = %q, want %q", jd.Job.Title, "Test job")
	}
	if len(jd.Tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(jd.Tasks))
	}
	if jd.Tasks[0].ID != "task-1" {
		t.Errorf("tasks[0].ID = %q, want %q", jd.Tasks[0].ID, "task-1")
	}
	if jd.Tasks[0].Status != service.TaskStatusInProgress {
		t.Errorf("tasks[0].Status = %q, want %q", jd.Tasks[0].Status, service.TaskStatusInProgress)
	}
	if len(jd.Progress) != 1 {
		t.Fatalf("got %d progress reports, want 1", len(jd.Progress))
	}
	if jd.Progress[0].Message != "Working on it" {
		t.Errorf("progress[0].Message = %q, want %q", jd.Progress[0].Message, "Working on it")
	}
}

func TestJobs_Cancel_Success(t *testing.T) {
	t.Parallel()

	mock := &mockService{
		cancelJobFn: func(_ context.Context, id string) error {
			if id != "job-99" {
				return fmt.Errorf("%w: job %s", service.ErrNotFound, id)
			}
			return nil
		},
	}

	rc := setupTestServer(t, mock)
	ctx := context.Background()

	if err := rc.Jobs().Cancel(ctx, "job-99"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
}

func TestJobs_Cancel_NotFound(t *testing.T) {
	t.Parallel()

	mock := &mockService{
		cancelJobFn: func(_ context.Context, _ string) error {
			return fmt.Errorf("%w: job not found", service.ErrNotFound)
		},
	}

	rc := setupTestServer(t, mock)
	ctx := context.Background()

	err := rc.Jobs().Cancel(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, service.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestSessions_List(t *testing.T) {
	t.Parallel()

	mock := &mockService{
		listSessionsFn: func(_ context.Context) ([]service.SessionSnapshot, error) {
			return []service.SessionSnapshot{
				{
					ID:        "sess-1",
					AgentID:   "agent-a",
					TeamName:  "team-alpha",
					Status:    "active",
					Model:     "gpt-4",
					Provider:  "openai",
					StartTime: testTime,
					TokensIn:  100,
					TokensOut: 50,
				},
				{
					ID:        "sess-2",
					AgentID:   "agent-b",
					Status:    "active",
					StartTime: testTime.Add(time.Minute),
					TokensIn:  200,
					TokensOut: 75,
				},
			}, nil
		},
	}

	rc := setupTestServer(t, mock)
	ctx := context.Background()

	snaps, err := rc.Sessions().List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("got %d snapshots, want 2", len(snaps))
	}

	if snaps[0].ID != "sess-1" {
		t.Errorf("snaps[0].ID = %q, want %q", snaps[0].ID, "sess-1")
	}
	if snaps[0].TeamName != "team-alpha" {
		t.Errorf("snaps[0].TeamName = %q, want %q", snaps[0].TeamName, "team-alpha")
	}
	if snaps[0].TokensIn != 100 {
		t.Errorf("snaps[0].TokensIn = %d, want %d", snaps[0].TokensIn, 100)
	}
	if snaps[0].TokensOut != 50 {
		t.Errorf("snaps[0].TokensOut = %d, want %d", snaps[0].TokensOut, 50)
	}

	if snaps[1].ID != "sess-2" {
		t.Errorf("snaps[1].ID = %q, want %q", snaps[1].ID, "sess-2")
	}
}

func TestDefinitions_ListSkills(t *testing.T) {
	t.Parallel()

	mock := &mockService{
		listSkillsFn: func(_ context.Context) ([]service.Skill, error) {
			return []service.Skill{
				{
					ID:          "skill-1",
					Name:        "code-review",
					Description: "Reviews code",
					Tools:       []string{"read_file", "grep"},
					Source:      "user",
					CreatedAt:   testTime,
					UpdatedAt:   testTime,
				},
				{
					ID:          "skill-2",
					Name:        "testing",
					Description: "Writes tests",
					Tools:       []string{"run_tests"},
					Prompt:      "You are a test writer.",
					Source:      "system",
					CreatedAt:   testTime,
					UpdatedAt:   testTime,
				},
			}, nil
		},
	}

	rc := setupTestServer(t, mock)
	ctx := context.Background()

	skills, err := rc.Definitions().ListSkills(ctx)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("got %d skills, want 2", len(skills))
	}

	if skills[0].ID != "skill-1" {
		t.Errorf("skills[0].ID = %q, want %q", skills[0].ID, "skill-1")
	}
	if skills[0].Name != "code-review" {
		t.Errorf("skills[0].Name = %q, want %q", skills[0].Name, "code-review")
	}
	if len(skills[0].Tools) != 2 {
		t.Errorf("skills[0].Tools = %v, want 2 tools", skills[0].Tools)
	}

	if skills[1].Prompt != "You are a test writer." {
		t.Errorf("skills[1].Prompt = %q, want %q", skills[1].Prompt, "You are a test writer.")
	}
}

func TestDefinitions_CreateAgent(t *testing.T) {
	t.Parallel()

	mock := &mockService{
		createAgentFn: func(_ context.Context, name string) (service.Agent, error) {
			return service.Agent{
				ID:        "agent-new",
				Name:      name,
				Source:    "user",
				Tools:     []string{},
				Skills:    []string{},
				CreatedAt: testTime,
				UpdatedAt: testTime,
			}, nil
		},
	}

	rc := setupTestServer(t, mock)
	ctx := context.Background()

	agent, err := rc.Definitions().CreateAgent(ctx, "my-agent")
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if agent.ID != "agent-new" {
		t.Errorf("agent.ID = %q, want %q", agent.ID, "agent-new")
	}
	if agent.Name != "my-agent" {
		t.Errorf("agent.Name = %q, want %q", agent.Name, "my-agent")
	}
	if agent.Source != "user" {
		t.Errorf("agent.Source = %q, want %q", agent.Source, "user")
	}
}

func TestDefinitions_GenerateSkill(t *testing.T) {
	t.Parallel()

	mock := &mockService{
		generateSkillFn: func(_ context.Context, prompt string) (string, error) {
			if prompt != "create a debugging skill" {
				t.Errorf("unexpected prompt: %q", prompt)
			}
			return "op-gen-123", nil
		},
	}

	rc := setupTestServer(t, mock)
	ctx := context.Background()

	opID, err := rc.Definitions().GenerateSkill(ctx, "create a debugging skill")
	if err != nil {
		t.Fatalf("GenerateSkill: %v", err)
	}
	if opID != "op-gen-123" {
		t.Errorf("operationID = %q, want %q", opID, "op-gen-123")
	}
}

func TestSystem_Health(t *testing.T) {
	t.Parallel()

	mock := &mockService{
		healthFn: func(_ context.Context) (service.HealthStatus, error) {
			return service.HealthStatus{
				Status:  "ok",
				Version: "1.0.0",
				Uptime:  5 * time.Minute,
			}, nil
		},
	}

	rc := setupTestServer(t, mock)
	ctx := context.Background()

	h, err := rc.System().Health(ctx)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.Status != "ok" {
		t.Errorf("status = %q, want %q", h.Status, "ok")
	}
	if h.Version != "1.0.0" {
		t.Errorf("version = %q, want %q", h.Version, "1.0.0")
	}
	// Uptime goes through float64 seconds conversion, so allow small tolerance.
	if h.Uptime < 4*time.Minute+59*time.Second || h.Uptime > 5*time.Minute+time.Second {
		t.Errorf("uptime = %v, want ~5m", h.Uptime)
	}
}

func TestSystem_GetProgressState(t *testing.T) {
	t.Parallel()

	mock := &mockService{
		getProgressStateFn: func(_ context.Context) (service.ProgressState, error) {
			return service.ProgressState{
				Jobs: []service.Job{
					{ID: "job-1", Title: "Test job", Status: service.JobStatusActive},
				},
				Tasks: map[string][]service.Task{
					"job-1": {
						{ID: "task-1", JobID: "job-1", Title: "Task 1", Status: service.TaskStatusInProgress},
					},
				},
				Reports: map[string][]service.ProgressReport{
					"job-1": {
						{ID: 1, JobID: "job-1", Message: "Working"},
					},
				},
				ActiveSessions: []service.AgentSession{
					{ID: "sess-1", AgentID: "agent-a", Status: service.SessionStatusActive},
				},
				LiveSnapshots: []service.SessionSnapshot{
					{ID: "sess-1", AgentID: "agent-a", Status: "active", TokensIn: 42},
				},
				FeedEntries: []service.FeedEntry{
					{ID: 1, EntryType: service.FeedEntryTypeTaskStarted, Content: "Task started"},
				},
			}, nil
		},
	}

	rc := setupTestServer(t, mock)
	ctx := context.Background()

	ps, err := rc.System().GetProgressState(ctx)
	if err != nil {
		t.Fatalf("GetProgressState: %v", err)
	}

	if len(ps.Jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(ps.Jobs))
	}
	if ps.Jobs[0].ID != "job-1" {
		t.Errorf("jobs[0].ID = %q, want %q", ps.Jobs[0].ID, "job-1")
	}

	tasks, ok := ps.Tasks["job-1"]
	if !ok {
		t.Fatal("missing tasks for job-1")
	}
	if len(tasks) != 1 || tasks[0].ID != "task-1" {
		t.Errorf("tasks[job-1] = %v, want [{ID: task-1}]", tasks)
	}

	reports, ok := ps.Reports["job-1"]
	if !ok {
		t.Fatal("missing reports for job-1")
	}
	if len(reports) != 1 || reports[0].Message != "Working" {
		t.Errorf("reports[job-1] = %v, want [{Message: Working}]", reports)
	}

	if len(ps.ActiveSessions) != 1 || ps.ActiveSessions[0].ID != "sess-1" {
		t.Errorf("active_sessions = %v, want [{ID: sess-1}]", ps.ActiveSessions)
	}

	if len(ps.LiveSnapshots) != 1 || ps.LiveSnapshots[0].TokensIn != 42 {
		t.Errorf("live_snapshots = %v, want [{TokensIn: 42}]", ps.LiveSnapshots)
	}

	if len(ps.FeedEntries) != 1 || ps.FeedEntries[0].EntryType != service.FeedEntryTypeTaskStarted {
		t.Errorf("feed_entries = %v, want [{EntryType: task_started}]", ps.FeedEntries)
	}
}

func TestSystem_GetLogs(t *testing.T) {
	t.Parallel()

	mock := &mockService{
		getLogsFn: func(_ context.Context) (string, error) {
			return "line 1: something happened\nline 2: another thing\n", nil
		},
	}

	rc := setupTestServer(t, mock)
	ctx := context.Background()

	content, err := rc.System().GetLogs(ctx)
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if content != "line 1: something happened\nline 2: another thing\n" {
		t.Errorf("GetLogs() = %q, want %q", content, "line 1: something happened\nline 2: another thing\n")
	}
}

func TestSystem_GetLogs_Empty(t *testing.T) {
	t.Parallel()

	mock := &mockService{
		getLogsFn: func(_ context.Context) (string, error) {
			return "", nil
		},
	}

	rc := setupTestServer(t, mock)
	ctx := context.Background()

	content, err := rc.System().GetLogs(ctx)
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if content != "" {
		t.Errorf("GetLogs() = %q, want empty string", content)
	}
}

func TestSystem_GetLogs_Error(t *testing.T) {
	t.Parallel()

	mock := &mockService{
		getLogsFn: func(_ context.Context) (string, error) {
			return "", fmt.Errorf("log file unreadable")
		},
	}

	rc := setupTestServer(t, mock)
	ctx := context.Background()

	_, err := rc.System().GetLogs(ctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "log file unreadable") && !strings.Contains(err.Error(), "get logs") {
		t.Errorf("error = %v, want error containing 'log file unreadable' or 'get logs'", err)
	}
}

func TestErrorPropagation_NotFound(t *testing.T) {
	t.Parallel()

	mock := &mockService{
		getJobFn: func(_ context.Context, _ string) (service.JobDetail, error) {
			return service.JobDetail{}, fmt.Errorf("looking up job: %w", service.ErrNotFound)
		},
	}

	rc := setupTestServer(t, mock)
	ctx := context.Background()

	_, err := rc.Jobs().Get(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, service.ErrNotFound) {
		t.Errorf("expected errors.Is(err, ErrNotFound), got: %v", err)
	}
}

func TestSSE_EventDelivery(t *testing.T) {
	t.Parallel()

	// The mock Subscribe returns a channel that we control.
	eventCh := make(chan service.Event, 10)

	mock := &mockService{
		subscribeFn: func(_ context.Context) <-chan service.Event {
			return eventCh
		},
	}

	rc := setupTestServer(t, mock)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ch := rc.Events().Subscribe(ctx)

	// Emit 3 events from the mock.
	eventCh <- service.Event{
		Seq:       1,
		Type:      service.EventTypeOperatorText,
		Timestamp: testTime,
		TurnID:    "turn-1",
		Payload:   service.OperatorTextPayload{Text: "hello world"},
	}
	eventCh <- service.Event{
		Seq:       2,
		Type:      service.EventTypeTaskCompleted,
		Timestamp: testTime,
		Payload: service.TaskCompletedPayload{
			TaskID:  "task-1",
			JobID:   "job-1",
			TeamID:  "team-1",
			Summary: "done",
		},
	}
	eventCh <- service.Event{
		Seq:       3,
		Type:      service.EventTypeHeartbeat,
		Timestamp: testTime,
		Payload:   service.HeartbeatPayload{ServerTime: testTime},
	}

	// Receive and verify events. Note: the server assigns its own seq numbers
	// per-connection, so we don't check seq values. Also, the server may emit
	// heartbeats independently, so we collect events by type.
	received := make(map[service.EventType]service.Event)
	timeout := time.After(5 * time.Second)

	for len(received) < 3 {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("event channel closed unexpectedly")
			}
			// Skip server-generated heartbeats that aren't ours.
			if ev.Type == service.EventTypeHeartbeat {
				if p, ok := ev.Payload.(service.HeartbeatPayload); ok {
					if !p.ServerTime.Equal(testTime) {
						continue // server-generated heartbeat, skip
					}
				}
			}
			received[ev.Type] = ev
		case <-timeout:
			t.Fatalf("timed out waiting for events, got %d of 3: %v",
				len(received), keysOf(received))
		}
	}

	// Verify operator.text event.
	if ev, ok := received[service.EventTypeOperatorText]; ok {
		p, ok := ev.Payload.(service.OperatorTextPayload)
		if !ok {
			t.Fatalf("operator.text payload type = %T, want OperatorTextPayload", ev.Payload)
		}
		if p.Text != "hello world" {
			t.Errorf("operator.text text = %q, want %q", p.Text, "hello world")
		}
		if ev.TurnID != "turn-1" {
			t.Errorf("operator.text turnID = %q, want %q", ev.TurnID, "turn-1")
		}
	} else {
		t.Error("missing operator.text event")
	}

	// Verify task.completed event.
	if ev, ok := received[service.EventTypeTaskCompleted]; ok {
		p, ok := ev.Payload.(service.TaskCompletedPayload)
		if !ok {
			t.Fatalf("task.completed payload type = %T, want TaskCompletedPayload", ev.Payload)
		}
		if p.TaskID != "task-1" {
			t.Errorf("task.completed taskID = %q, want %q", p.TaskID, "task-1")
		}
		if p.Summary != "done" {
			t.Errorf("task.completed summary = %q, want %q", p.Summary, "done")
		}
	} else {
		t.Error("missing task.completed event")
	}

	// Verify heartbeat event.
	if ev, ok := received[service.EventTypeHeartbeat]; ok {
		p, ok := ev.Payload.(service.HeartbeatPayload)
		if !ok {
			t.Fatalf("heartbeat payload type = %T, want HeartbeatPayload", ev.Payload)
		}
		if !p.ServerTime.Equal(testTime) {
			t.Errorf("heartbeat server_time = %v, want %v", p.ServerTime, testTime)
		}
	} else {
		t.Error("missing heartbeat event")
	}
}

// keysOf returns the keys of a map as a slice (for error messages).
func keysOf[K comparable, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// ---------------------------------------------------------------------------
// URL validation tests
// ---------------------------------------------------------------------------

func TestNew_ValidURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
	}{
		{"http localhost with port", "http://localhost:8080"},
		{"https example.com", "https://example.com"},
		{"http IP with port", "http://192.168.1.1:3000"},
		{"https subdomain", "https://api.example.com"},
		{"http localhost no port", "http://localhost"},
		{"https domain with port", "https://example.com:443"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rc, err := client.New(tt.baseURL)
			if err != nil {
				t.Fatalf("New(%q) returned error: %v", tt.baseURL, err)
			}
			if rc == nil {
				t.Fatal("New returned nil client")
			}
			rc.Close()
		})
	}
}

func TestNew_InvalidURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		baseURL   string
		wantInErr string // substring expected in error message
	}{
		{"empty string", "", "scheme must be http or https"},
		{"no scheme", "localhost:8080", "scheme must be http or https"},
		{"no host with port only", ":8080", "missing protocol scheme"},
		{"no host http", "http://", "missing host"},
		{"no host https", "https://", "missing host"},
		{"invalid scheme ftp", "ftp://example.com", "scheme must be http or https"},
		{"invalid scheme ws", "ws://localhost:8080", "scheme must be http or https"},
		{"invalid scheme wss", "wss://localhost:8080", "scheme must be http or https"},
		{"scheme only", "http:", "missing host"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rc, err := client.New(tt.baseURL)
			if err == nil {
				if rc != nil {
					rc.Close()
				}
				t.Fatalf("New(%q) expected error, got nil", tt.baseURL)
			}
			if !strings.Contains(err.Error(), tt.wantInErr) {
				t.Errorf("New(%q) error = %q, want error containing %q", tt.baseURL, err.Error(), tt.wantInErr)
			}
		})
	}
}
