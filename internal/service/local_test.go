package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/operator"
	"github.com/jefflinse/toasters/internal/provider"
)

// ---------------------------------------------------------------------------
// mockStore — minimal db.Store implementation for unit tests.
// Only the methods actually called by the functions under test are implemented;
// all others return a "not implemented" error.
// ---------------------------------------------------------------------------

type mockStore struct {
	// ListSkills
	listSkillsResult []*db.Skill
	listSkillsErr    error

	// GetSkill
	getSkillResult *db.Skill
	getSkillErr    error
}

// Compile-time assertion that mockStore satisfies db.Store.
var _ db.Store = (*mockStore)(nil)

func (m *mockStore) ListSkills(_ context.Context) ([]*db.Skill, error) {
	return m.listSkillsResult, m.listSkillsErr
}

func (m *mockStore) GetSkill(_ context.Context, _ string) (*db.Skill, error) {
	return m.getSkillResult, m.getSkillErr
}

// --- Unimplemented methods ---

func (m *mockStore) CreateJob(_ context.Context, _ *db.Job) error {
	return fmt.Errorf("not implemented")
}
func (m *mockStore) GetJob(_ context.Context, _ string) (*db.Job, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockStore) ListJobs(_ context.Context, _ db.JobFilter) ([]*db.Job, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockStore) ListAllJobs(_ context.Context) ([]*db.Job, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockStore) UpdateJob(_ context.Context, _ string, _ db.JobUpdate) error {
	return fmt.Errorf("not implemented")
}
func (m *mockStore) UpdateJobStatus(_ context.Context, _ string, _ db.JobStatus) error {
	return fmt.Errorf("not implemented")
}
func (m *mockStore) CreateTask(_ context.Context, _ *db.Task) error {
	return fmt.Errorf("not implemented")
}
func (m *mockStore) GetTask(_ context.Context, _ string) (*db.Task, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockStore) ListTasksForJob(_ context.Context, _ string) ([]*db.Task, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockStore) UpdateTaskStatus(_ context.Context, _ string, _ db.TaskStatus, _ string) error {
	return fmt.Errorf("not implemented")
}
func (m *mockStore) UpdateTaskResult(_ context.Context, _ string, _, _ string) error {
	return fmt.Errorf("not implemented")
}
func (m *mockStore) CompleteTask(_ context.Context, _ string, _ db.TaskStatus, _, _ string) error {
	return fmt.Errorf("not implemented")
}
func (m *mockStore) AssignTaskToGraph(_ context.Context, _ string, _ string) error {
	return fmt.Errorf("not implemented")
}
func (m *mockStore) PreAssignTaskGraph(_ context.Context, _ string, _ string) error {
	return fmt.Errorf("not implemented")
}
func (m *mockStore) AddTaskDependency(_ context.Context, _, _ string) error {
	return fmt.Errorf("not implemented")
}
func (m *mockStore) GetReadyTasks(_ context.Context, _ string) ([]*db.Task, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockStore) ReportProgress(_ context.Context, _ *db.ProgressReport) error {
	return fmt.Errorf("not implemented")
}
func (m *mockStore) GetRecentProgress(_ context.Context, _ string, _ int) ([]*db.ProgressReport, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockStore) UpsertSkill(_ context.Context, _ *db.Skill) error {
	return fmt.Errorf("not implemented")
}
func (m *mockStore) DeleteAllSkills(_ context.Context) error {
	return fmt.Errorf("not implemented")
}
func (m *mockStore) CreateFeedEntry(_ context.Context, _ *db.FeedEntry) error {
	return fmt.Errorf("not implemented")
}
func (m *mockStore) ListFeedEntries(_ context.Context, _ string, _ int) ([]*db.FeedEntry, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockStore) ListRecentFeedEntries(_ context.Context, _ int) ([]*db.FeedEntry, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockStore) RebuildDefinitions(_ context.Context, _ []*db.Skill) error {
	return fmt.Errorf("not implemented")
}
func (m *mockStore) CreateSession(_ context.Context, _ *db.WorkerSession) error {
	return fmt.Errorf("not implemented")
}
func (m *mockStore) UpdateSession(_ context.Context, _ string, _ db.SessionUpdate) error {
	return fmt.Errorf("not implemented")
}
func (m *mockStore) GetActiveSessions(_ context.Context) ([]*db.WorkerSession, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockStore) ListSessionsForTask(_ context.Context, _ string) ([]*db.WorkerSession, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockStore) ListSessionsForJob(_ context.Context, _ string) ([]*db.WorkerSession, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockStore) LogArtifact(_ context.Context, _ *db.Artifact) error {
	return fmt.Errorf("not implemented")
}
func (m *mockStore) ListArtifactsForJob(_ context.Context, _ string) ([]*db.Artifact, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockStore) AppendChatEntry(_ context.Context, _ *db.ChatEntry) error {
	return nil
}
func (m *mockStore) ListRecentChatEntries(_ context.Context, _ int) ([]*db.ChatEntry, error) {
	return nil, nil
}
func (m *mockStore) AppendSessionMessage(_ context.Context, _ *db.SessionMessage) error {
	return fmt.Errorf("not implemented")
}
func (m *mockStore) ListSessionMessages(_ context.Context, _ string) ([]*db.SessionMessage, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockStore) Close() error { return nil }

// ---------------------------------------------------------------------------
// mockProvider — minimal provider.Provider for unit tests.
// ---------------------------------------------------------------------------

type mockProvider struct {
	modelsResult []provider.ModelInfo
	modelsErr    error
}

var _ provider.Provider = (*mockProvider)(nil)

func (m *mockProvider) ChatStream(_ context.Context, _ provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockProvider) Models(_ context.Context) ([]provider.ModelInfo, error) {
	return m.modelsResult, m.modelsErr
}

func (m *mockProvider) Name() string { return "mock" }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestService creates a LocalService with a temp dir as ConfigDir and no
// real dependencies. Callers can override cfg fields before use.
func newTestService(t *testing.T) *LocalService {
	t.Helper()
	return NewLocal(LocalConfig{
		ConfigDir: t.TempDir(),
		StartTime: time.Now(),
	})
}

// ---------------------------------------------------------------------------
// Priority 1 — Pure / filesystem methods
// ---------------------------------------------------------------------------

func TestNewLocal_Defaults(t *testing.T) {
	t.Parallel()

	cfg := LocalConfig{ConfigDir: t.TempDir()}
	svc := NewLocal(cfg)

	if svc.ctx == nil {
		t.Error("ctx should be non-nil")
	}
	if svc.cancel == nil {
		t.Error("cancel should be non-nil")
	}
	if svc.subscribers == nil {
		t.Error("subscribers map should be initialized")
	}
	if svc.cfg.StartTime.IsZero() {
		t.Error("StartTime should be set when zero")
	}
}

func TestNewLocal_StartTimePreserved(t *testing.T) {
	t.Parallel()

	fixed := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	cfg := LocalConfig{
		ConfigDir: t.TempDir(),
		StartTime: fixed,
	}
	svc := NewLocal(cfg)

	if !svc.cfg.StartTime.Equal(fixed) {
		t.Errorf("StartTime = %v, want %v", svc.cfg.StartTime, fixed)
	}
}

func TestShutdown_CancelsContext(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)

	// Context should be live before shutdown.
	select {
	case <-svc.ctx.Done():
		t.Fatal("context should not be done before Shutdown()")
	default:
	}

	svc.Shutdown()

	// Context should be cancelled after shutdown.
	select {
	case <-svc.ctx.Done():
		// expected
	case <-time.After(time.Second):
		t.Fatal("context was not cancelled after Shutdown()")
	}
}

func TestHealth_ReturnsOK(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	h, err := svc.Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error = %v", err)
	}
	if h.Status != "ok" {
		t.Errorf("Status = %q, want %q", h.Status, "ok")
	}
	if h.Version == "" {
		t.Error("Version should be non-empty")
	}
	if h.Uptime < 0 {
		t.Errorf("Uptime = %v, want >= 0", h.Uptime)
	}
}

func TestHealth_UptimeIncreases(t *testing.T) {
	t.Parallel()

	start := time.Now().Add(-5 * time.Second)
	svc := NewLocal(LocalConfig{
		ConfigDir: t.TempDir(),
		StartTime: start,
	})

	h, err := svc.Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error = %v", err)
	}
	if h.Uptime < 4*time.Second {
		t.Errorf("Uptime = %v, want >= 4s (started 5s ago)", h.Uptime)
	}
}

func TestConfigDir_ReturnsConfigured(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	svc := NewLocal(LocalConfig{ConfigDir: dir})

	got := svc.ConfigDir()
	if got != dir {
		t.Errorf("ConfigDir() = %q, want %q", got, dir)
	}
}

func TestSlugify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple words", "My Agent", "my-agent"},
		{"empty string", "", ""},
		{"already slug", "my-agent", "my-agent"},
		{"special chars", "Go & Dev!", "go-dev"},
		{"multiple spaces", "a  b  c", "a-b-c"},
		{"leading trailing spaces", "  hello  ", "hello"},
		{"numbers", "Agent 42", "agent-42"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Slugify(tt.input)
			if got != tt.want {
				t.Errorf("Slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestStripCodeFences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no fences",
			input: "plain content",
			want:  "plain content",
		},
		{
			name:  "markdown fence",
			input: "```markdown\ncontent here\n```",
			want:  "content here",
		},
		{
			name:  "plain fence",
			input: "```\ncontent here\n```",
			want:  "content here",
		},
		{
			name:  "trailing fence only",
			input: "content here\n```",
			want:  "content here",
		},
		{
			name:  "whitespace trimming",
			input: "  \n  content  \n  ",
			want:  "content",
		},
		{
			name:  "yaml fence",
			input: "```yaml\nname: test\n```",
			want:  "name: test",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "only fences",
			input: "```\n```",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := stripCodeFences(tt.input)
			if got != tt.want {
				t.Errorf("stripCodeFences(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CreateSkill — filesystem tests
// ---------------------------------------------------------------------------

func TestCreateSkill_WritesFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	svc := NewLocal(LocalConfig{
		ConfigDir: dir,
		Store:     &mockStore{listSkillsErr: fmt.Errorf("no store")},
	})

	// CreateSkill will fail at ListSkills (no real store), but the file should
	// still be written before that point. We verify the file exists.
	_, _ = svc.CreateSkill(context.Background(), "My Skill")

	skillsDir := filepath.Join(dir, "user", "skills")
	path := filepath.Join(skillsDir, "my-skill.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("skill file not written: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "name: My Skill") {
		t.Errorf("skill file missing name field, got:\n%s", content)
	}
	if !strings.Contains(content, "tools: []") {
		t.Errorf("skill file missing tools field, got:\n%s", content)
	}
}

func TestCreateSkill_EmptyName(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	_, err := svc.CreateSkill(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	if !strings.Contains(err.Error(), "invalid skill name") {
		t.Errorf("error = %q, want 'invalid skill name'", err.Error())
	}
}

func TestCreateSkill_DuplicateFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "user", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-create the file.
	if err := os.WriteFile(filepath.Join(skillsDir, "my-skill.md"), []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := NewLocal(LocalConfig{ConfigDir: dir})
	_, err := svc.CreateSkill(context.Background(), "My Skill")
	if err == nil {
		t.Fatal("expected error for duplicate file")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %q, want 'already exists'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// DeleteSkill — filesystem tests
// ---------------------------------------------------------------------------

func TestDeleteSkill_RemovesFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	userDir := filepath.Join(dir, "user", "skills")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(userDir, "my-skill.md")
	if err := os.WriteFile(skillPath, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := NewLocal(LocalConfig{
		ConfigDir: dir,
		Store: &mockStore{
			getSkillResult: &db.Skill{
				ID:         "skill-1",
				Name:       "My Skill",
				Source:     "user",
				SourcePath: skillPath,
			},
		},
	})

	if err := svc.DeleteSkill(context.Background(), "skill-1"); err != nil {
		t.Fatalf("DeleteSkill() error = %v", err)
	}

	if _, err := os.Stat(skillPath); !os.IsNotExist(err) {
		t.Error("skill file should have been removed")
	}
}

func TestDeleteSkill_RejectsSystemSkill(t *testing.T) {
	t.Parallel()

	svc := NewLocal(LocalConfig{
		ConfigDir: t.TempDir(),
		Store: &mockStore{
			getSkillResult: &db.Skill{
				ID:     "sys-skill",
				Name:   "System Skill",
				Source: "system",
			},
		},
	})

	err := svc.DeleteSkill(context.Background(), "sys-skill")
	if err == nil {
		t.Fatal("expected error for system skill deletion")
	}
	if !strings.Contains(err.Error(), "cannot delete system skill") {
		t.Errorf("error = %q, want 'cannot delete system skill'", err.Error())
	}
}

func TestDeleteSkill_RejectsPathTraversal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Create the user/ subdirectory so EvalSymlinks succeeds on the allowed dir.
	userDir := filepath.Join(dir, "user")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a file outside the user directory (at the configDir root).
	outsideFile := filepath.Join(dir, "outside.md")
	if err := os.WriteFile(outsideFile, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := NewLocal(LocalConfig{
		ConfigDir: dir,
		Store: &mockStore{
			getSkillResult: &db.Skill{
				ID:         "skill-1",
				Name:       "Bad Skill",
				Source:     "user",
				SourcePath: outsideFile,
			},
		},
	})

	err := svc.DeleteSkill(context.Background(), "skill-1")
	if err == nil {
		t.Fatal("expected error for path outside user directory")
	}
	if !strings.Contains(err.Error(), "outside user directory") {
		t.Errorf("error = %q, want 'outside user directory'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Priority 2 — Event stream
// ---------------------------------------------------------------------------

func TestSubscribeBroadcast_ReceivesEvent(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := svc.subscribe(ctx)

	svc.broadcast(Event{Type: EventTypeHeartbeat})

	select {
	case ev := <-ch:
		if ev.Seq != 1 {
			t.Errorf("Seq = %d, want 1", ev.Seq)
		}
		if ev.Timestamp.IsZero() {
			t.Error("Timestamp should be set")
		}
		if ev.Type != EventTypeHeartbeat {
			t.Errorf("Type = %q, want %q", ev.Type, EventTypeHeartbeat)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestSubscribeBroadcast_SeqIncrementsPerBroadcast(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := svc.subscribe(ctx)

	svc.broadcast(Event{Type: EventTypeHeartbeat})
	svc.broadcast(Event{Type: EventTypeHeartbeat})

	ev1 := <-ch
	ev2 := <-ch

	if ev1.Seq != 1 {
		t.Errorf("first event Seq = %d, want 1", ev1.Seq)
	}
	if ev2.Seq != 2 {
		t.Errorf("second event Seq = %d, want 2", ev2.Seq)
	}
}

func TestSubscribe_ContextCancellationClosesChannel(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())

	ch := svc.subscribe(ctx)
	cancel()

	// Channel should be closed after context cancellation.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("channel should be closed, not have a value")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel to close")
	}
}

func TestMultipleSubscribers_BothReceiveEvent(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch1 := svc.subscribe(ctx)
	ch2 := svc.subscribe(ctx)

	svc.broadcast(Event{Type: EventTypeDefinitionsReloaded})

	for i, ch := range []<-chan Event{ch1, ch2} {
		select {
		case ev := <-ch:
			if ev.Type != EventTypeDefinitionsReloaded {
				t.Errorf("subscriber %d: Type = %q, want %q", i+1, ev.Type, EventTypeDefinitionsReloaded)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out waiting for event", i+1)
		}
	}
}

func TestBroadcast_OverflowDoesNotPanic(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Subscribe but don't drain — buffer is 256.
	_ = svc.subscribe(ctx)

	// Fill the buffer and then overflow it — should not panic.
	for i := 0; i < 300; i++ {
		svc.broadcast(Event{Type: EventTypeHeartbeat})
	}
}

func TestBroadcastOperatorText_EventPayload(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := svc.subscribe(ctx)

	svc.BroadcastOperatorText("hello world", "some reasoning")

	select {
	case ev := <-ch:
		if ev.Type != EventTypeOperatorText {
			t.Errorf("Type = %q, want %q", ev.Type, EventTypeOperatorText)
		}
		payload, ok := ev.Payload.(OperatorTextPayload)
		if !ok {
			t.Fatalf("Payload type = %T, want OperatorTextPayload", ev.Payload)
		}
		if payload.Text != "hello world" {
			t.Errorf("Text = %q, want %q", payload.Text, "hello world")
		}
		if payload.Reasoning != "some reasoning" {
			t.Errorf("Reasoning = %q, want %q", payload.Reasoning, "some reasoning")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestBroadcastOperatorText_CarriesTurnID(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	svc.turnMu.Lock()
	svc.currentTurnID = "turn-abc"
	svc.turnMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := svc.subscribe(ctx)

	svc.BroadcastOperatorText("text", "")

	select {
	case ev := <-ch:
		if ev.TurnID != "turn-abc" {
			t.Errorf("TurnID = %q, want %q", ev.TurnID, "turn-abc")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
}

func TestBroadcastOperatorDone_ClearsTurnID(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	svc.turnMu.Lock()
	svc.currentTurnID = "turn-xyz"
	svc.turnMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := svc.subscribe(ctx)

	svc.BroadcastOperatorDone("claude-3", 100, 200, 0)

	select {
	case ev := <-ch:
		if ev.Type != EventTypeOperatorDone {
			t.Errorf("Type = %q, want %q", ev.Type, EventTypeOperatorDone)
		}
		if ev.TurnID != "turn-xyz" {
			t.Errorf("TurnID = %q, want %q", ev.TurnID, "turn-xyz")
		}
		payload, ok := ev.Payload.(OperatorDonePayload)
		if !ok {
			t.Fatalf("Payload type = %T, want OperatorDonePayload", ev.Payload)
		}
		if payload.ModelName != "claude-3" {
			t.Errorf("ModelName = %q, want %q", payload.ModelName, "claude-3")
		}
		if payload.TokensIn != 100 {
			t.Errorf("TokensIn = %d, want 100", payload.TokensIn)
		}
		if payload.TokensOut != 200 {
			t.Errorf("TokensOut = %d, want 200", payload.TokensOut)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}

	// currentTurnID should be cleared.
	svc.turnMu.Lock()
	turnID := svc.currentTurnID
	svc.turnMu.Unlock()
	if turnID != "" {
		t.Errorf("currentTurnID = %q, want empty after BroadcastOperatorDone", turnID)
	}
}

func TestBroadcastOperatorEvent_TaskStarted(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := svc.subscribe(ctx)

	svc.BroadcastOperatorEvent(operator.Event{
		Type: operator.EventTaskStarted,
		Payload: operator.TaskStartedPayload{
			TaskID:  "task-1",
			JobID:   "job-1",
			GraphID: "team-1",
			Title:   "Do the thing",
		},
	})

	select {
	case ev := <-ch:
		if ev.Type != EventTypeTaskStarted {
			t.Errorf("Type = %q, want %q", ev.Type, EventTypeTaskStarted)
		}
		payload, ok := ev.Payload.(TaskStartedPayload)
		if !ok {
			t.Fatalf("Payload type = %T, want TaskStartedPayload", ev.Payload)
		}
		if payload.TaskID != "task-1" {
			t.Errorf("TaskID = %q, want %q", payload.TaskID, "task-1")
		}
		if payload.Title != "Do the thing" {
			t.Errorf("Title = %q, want %q", payload.Title, "Do the thing")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
}

func TestBroadcastOperatorEvent_TaskCompleted(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := svc.subscribe(ctx)

	svc.BroadcastOperatorEvent(operator.Event{
		Type: operator.EventTaskCompleted,
		Payload: operator.TaskCompletedPayload{
			TaskID:          "task-2",
			JobID:           "job-2",
			GraphID:         "team-2",
			Summary:         "Done",
			Recommendations: "Next: do X",
			HasNextTask:     true,
		},
	})

	select {
	case ev := <-ch:
		if ev.Type != EventTypeTaskCompleted {
			t.Errorf("Type = %q, want %q", ev.Type, EventTypeTaskCompleted)
		}
		payload, ok := ev.Payload.(TaskCompletedPayload)
		if !ok {
			t.Fatalf("Payload type = %T, want TaskCompletedPayload", ev.Payload)
		}
		if payload.Summary != "Done" {
			t.Errorf("Summary = %q, want %q", payload.Summary, "Done")
		}
		if !payload.HasNextTask {
			t.Error("HasNextTask should be true")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
}

func TestBroadcastOperatorEvent_TaskFailed(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := svc.subscribe(ctx)

	svc.BroadcastOperatorEvent(operator.Event{
		Type: operator.EventTaskFailed,
		Payload: operator.TaskFailedPayload{
			TaskID:  "task-3",
			JobID:   "job-3",
			GraphID: "team-3",
			Error:   "something went wrong",
		},
	})

	select {
	case ev := <-ch:
		if ev.Type != EventTypeTaskFailed {
			t.Errorf("Type = %q, want %q", ev.Type, EventTypeTaskFailed)
		}
		payload, ok := ev.Payload.(TaskFailedPayload)
		if !ok {
			t.Fatalf("Payload type = %T, want TaskFailedPayload", ev.Payload)
		}
		if payload.Error != "something went wrong" {
			t.Errorf("Error = %q, want %q", payload.Error, "something went wrong")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
}

func TestBroadcastOperatorEvent_JobComplete(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := svc.subscribe(ctx)

	svc.BroadcastOperatorEvent(operator.Event{
		Type: operator.EventJobComplete,
		Payload: operator.JobCompletePayload{
			JobID:   "job-5",
			Title:   "My Job",
			Summary: "All done",
		},
	})

	select {
	case ev := <-ch:
		if ev.Type != EventTypeJobCompleted {
			t.Errorf("Type = %q, want %q", ev.Type, EventTypeJobCompleted)
		}
		payload, ok := ev.Payload.(JobCompletedPayload)
		if !ok {
			t.Fatalf("Payload type = %T, want JobCompletedPayload", ev.Payload)
		}
		if payload.JobID != "job-5" {
			t.Errorf("JobID = %q, want %q", payload.JobID, "job-5")
		}
		if payload.Summary != "All done" {
			t.Errorf("Summary = %q, want %q", payload.Summary, "All done")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
}

func TestBroadcastOperatorEvent_UnknownPayloadType_NoEvent(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := svc.subscribe(ctx)

	// Send a known event type but with wrong payload type — should be silently dropped.
	svc.BroadcastOperatorEvent(operator.Event{
		Type:    operator.EventTaskStarted,
		Payload: "wrong type",
	})

	select {
	case ev := <-ch:
		t.Errorf("expected no event, got %+v", ev)
	case <-time.After(100 * time.Millisecond):
		// expected — no event emitted
	}
}

func TestBroadcastDefinitionsReloaded(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := svc.subscribe(ctx)

	svc.BroadcastDefinitionsReloaded()

	select {
	case ev := <-ch:
		if ev.Type != EventTypeDefinitionsReloaded {
			t.Errorf("Type = %q, want %q", ev.Type, EventTypeDefinitionsReloaded)
		}
		if ev.Payload != nil {
			t.Errorf("Payload = %v, want nil", ev.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
}

// ---------------------------------------------------------------------------
// Priority 3 — Store-backed methods
// ---------------------------------------------------------------------------

func TestListSkills_MapsFields(t *testing.T) {
	t.Parallel()

	toolsJSON, _ := json.Marshal([]string{"Read", "Write"})
	store := &mockStore{
		listSkillsResult: []*db.Skill{
			{
				ID:          "skill-1",
				Name:        "Skill One",
				Description: "desc one",
				Tools:       toolsJSON,
				Source:      "user",
				SourcePath:  "/path/to/skill.md",
			},
			{
				ID:     "skill-2",
				Name:   "Skill Two",
				Source: "system",
			},
		},
	}

	svc := NewLocal(LocalConfig{Store: store})
	skills, err := svc.ListSkills(context.Background())
	if err != nil {
		t.Fatalf("ListSkills() error = %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("len(skills) = %d, want 2", len(skills))
	}

	s1 := skills[0]
	if s1.ID != "skill-1" {
		t.Errorf("ID = %q, want %q", s1.ID, "skill-1")
	}
	if s1.Name != "Skill One" {
		t.Errorf("Name = %q, want %q", s1.Name, "Skill One")
	}
	if len(s1.Tools) != 2 || s1.Tools[0] != "Read" || s1.Tools[1] != "Write" {
		t.Errorf("Tools = %v, want [Read Write]", s1.Tools)
	}
}

func TestListSkills_StoreError(t *testing.T) {
	t.Parallel()

	store := &mockStore{listSkillsErr: errors.New("db error")}
	svc := NewLocal(LocalConfig{Store: store})

	_, err := svc.ListSkills(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "listing skills") {
		t.Errorf("error = %q, want 'listing skills'", err.Error())
	}
}

func TestListSkills_NilStore(t *testing.T) {
	t.Parallel()

	svc := NewLocal(LocalConfig{})
	_, err := svc.ListSkills(context.Background())
	if err == nil {
		t.Fatal("expected error for nil store")
	}
}

func TestGetSkill_Found(t *testing.T) {
	t.Parallel()

	store := &mockStore{
		getSkillResult: &db.Skill{
			ID:   "skill-1",
			Name: "My Skill",
		},
	}
	svc := NewLocal(LocalConfig{Store: store})

	sk, err := svc.GetSkill(context.Background(), "skill-1")
	if err != nil {
		t.Fatalf("GetSkill() error = %v", err)
	}
	if sk.ID != "skill-1" {
		t.Errorf("ID = %q, want %q", sk.ID, "skill-1")
	}
}

func TestGetSkill_NotFound(t *testing.T) {
	t.Parallel()

	store := &mockStore{getSkillErr: db.ErrNotFound}
	svc := NewLocal(LocalConfig{Store: store})

	_, err := svc.GetSkill(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error should wrap ErrNotFound, got: %v", err)
	}
}

func TestStatus_DisabledWhenNoOperator(t *testing.T) {
	t.Parallel()

	svc := NewLocal(LocalConfig{OperatorModel: "claude-3-sonnet"})
	status, err := svc.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.State != OperatorStateDisabled {
		t.Errorf("State = %q, want %q", status.State, OperatorStateDisabled)
	}
}

func TestSendMessage_NilOperator(t *testing.T) {
	t.Parallel()

	// operator.Operator is a concrete struct, not an interface, so we cannot
	// mock it. We test only the nil-operator error path here. The happy path
	// (with a real operator goroutine) is covered by integration tests.
	svc := NewLocal(LocalConfig{Operator: nil})
	_, err := svc.SendMessage(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error for nil operator")
	}
	if !strings.Contains(err.Error(), "operator not configured") {
		t.Errorf("error = %q, want 'operator not configured'", err.Error())
	}
}

func TestSendMessage_TurnAlreadyInProgress(t *testing.T) {
	t.Parallel()

	// Simulate a turn already in progress by setting currentTurnID.
	// operator.Operator is concrete, so we can't mock it — but we can test
	// the guard that rejects concurrent turns.
	svc := newTestService(t)
	svc.turnMu.Lock()
	svc.currentTurnID = "existing-turn"
	svc.turnMu.Unlock()

	// Even with a nil operator, the turn-in-progress check fires first only
	// if operator is non-nil. With nil operator, the nil check fires first.
	// So we verify the nil-operator path returns the right error.
	_, err := svc.SendMessage(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestListModels_NilProvider(t *testing.T) {
	t.Parallel()

	svc := NewLocal(LocalConfig{Provider: nil})
	_, err := svc.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error for nil provider")
	}
	if !strings.Contains(err.Error(), "LLM provider not configured") {
		t.Errorf("error = %q, want 'LLM provider not configured'", err.Error())
	}
}

func TestListModels_MapsFields(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{
		modelsResult: []provider.ModelInfo{
			{
				ID:                  "model-1",
				Name:                "Model One",
				Provider:            "mock",
				MaxContextLength:    128000,
				LoadedContextLength: 64000,
			},
		},
	}

	svc := NewLocal(LocalConfig{Provider: mp})
	models, err := svc.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}

	m := models[0]
	if m.ID != "model-1" {
		t.Errorf("ID = %q, want %q", m.ID, "model-1")
	}
	if m.Provider != "mock" {
		t.Errorf("Provider = %q, want %q", m.Provider, "mock")
	}
	if m.MaxContextLength != 128000 {
		t.Errorf("MaxContextLength = %d, want 128000", m.MaxContextLength)
	}
	if m.LoadedContextLength != 64000 {
		t.Errorf("LoadedContextLength = %d, want 64000", m.LoadedContextLength)
	}
}

func TestListModels_ProviderError(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{modelsErr: errors.New("provider unavailable")}
	svc := NewLocal(LocalConfig{Provider: mp})

	_, err := svc.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "listing models") {
		t.Errorf("error = %q, want 'listing models'", err.Error())
	}
}

func TestListMCPServers_NilManager(t *testing.T) {
	t.Parallel()

	svc := NewLocal(LocalConfig{MCPManager: nil})
	servers, err := svc.ListMCPServers(context.Background())
	if err != nil {
		t.Fatalf("ListMCPServers() error = %v", err)
	}
	if servers != nil {
		t.Errorf("servers = %v, want nil", servers)
	}
}

// ---------------------------------------------------------------------------
// Sub-interface accessor tests
// ---------------------------------------------------------------------------

func TestSubInterfaceAccessors(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)

	if svc.Operator() == nil {
		t.Error("Operator() should return non-nil")
	}
	if svc.Definitions() == nil {
		t.Error("Definitions() should return non-nil")
	}
	if svc.Jobs() == nil {
		t.Error("Jobs() should return non-nil")
	}
	if svc.Sessions() == nil {
		t.Error("Sessions() should return non-nil")
	}
	if svc.Events() == nil {
		t.Error("Events() should return non-nil")
	}
	if svc.System() == nil {
		t.Error("System() should return non-nil")
	}
}

// ---------------------------------------------------------------------------
// Subscribe (public EventService method)
// ---------------------------------------------------------------------------

func TestSubscribe_PublicMethod(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := svc.Events().Subscribe(ctx)
	if ch == nil {
		t.Fatal("Subscribe() returned nil channel")
	}

	svc.broadcast(Event{Type: EventTypeHeartbeat})

	select {
	case ev := <-ch:
		if ev.Type != EventTypeHeartbeat {
			t.Errorf("Type = %q, want %q", ev.Type, EventTypeHeartbeat)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

// ---------------------------------------------------------------------------
// Input size limit tests
// ---------------------------------------------------------------------------

func TestRespondToPrompt_ResponseTooLarge(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)

	// Create a response that exceeds maxResponseLen (51200 bytes).
	largeResponse := strings.Repeat("x", maxResponseLen+1)

	err := svc.RespondToPrompt(context.Background(), "request-1", largeResponse)
	if err == nil {
		t.Fatal("expected error for oversized response")
	}
	if !strings.Contains(err.Error(), "response too large") {
		t.Errorf("error = %q, want 'response too large'", err.Error())
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d", maxResponseLen+1)) {
		t.Errorf("error should include actual size %d, got: %q", maxResponseLen+1, err.Error())
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d", maxResponseLen)) {
		t.Errorf("error should include max size %d, got: %q", maxResponseLen, err.Error())
	}
}

func TestRespondToPrompt_ResponseAtLimit(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)

	// Response exactly at the limit should be rejected by the nil operator check,
	// not by size validation.
	response := strings.Repeat("x", maxResponseLen)

	err := svc.RespondToPrompt(context.Background(), "request-1", response)
	if err == nil {
		t.Fatal("expected error (nil operator)")
	}
	// Should NOT be a size error.
	if strings.Contains(err.Error(), "too large") {
		t.Errorf("response at limit should not trigger size error, got: %q", err.Error())
	}
}

func TestRespondToPrompt_NoPendingRequest(t *testing.T) {
	t.Parallel()

	// RespondToPrompt now goes through the shared HITL broker, so a
	// request ID that was never registered produces a broker error
	// rather than "operator not configured". The operator being nil
	// is no longer the check — a dangling response is.
	svc := newTestService(t)

	err := svc.RespondToPrompt(context.Background(), "request-1", "response")
	if err == nil {
		t.Fatal("expected error for unknown request ID")
	}
	if !strings.Contains(err.Error(), "no pending request") {
		t.Errorf("error = %q, want broker 'no pending request' message", err.Error())
	}
}

// ---------------------------------------------------------------------------
// GetLogs tests
// ---------------------------------------------------------------------------

func TestGetLogs_ReturnsFileContent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logContent := "line 1: something happened\nline 2: another thing\n"
	if err := os.WriteFile(filepath.Join(dir, "toasters.log"), []byte(logContent), 0o644); err != nil {
		t.Fatalf("writing log file: %v", err)
	}

	svc := NewLocal(LocalConfig{ConfigDir: dir})

	got, err := svc.GetLogs(context.Background())
	if err != nil {
		t.Fatalf("GetLogs() error = %v", err)
	}
	if got != logContent {
		t.Errorf("GetLogs() = %q, want %q", got, logContent)
	}
}

func TestGetLogs_NoLogFile_ReturnsEmptyString(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// No toasters.log file created.

	svc := NewLocal(LocalConfig{ConfigDir: dir})

	got, err := svc.GetLogs(context.Background())
	if err != nil {
		t.Fatalf("GetLogs() error = %v", err)
	}
	if got != "" {
		t.Errorf("GetLogs() = %q, want empty string when log file does not exist", got)
	}
}

func TestGetLogs_EmptyLogFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "toasters.log"), []byte(""), 0o644); err != nil {
		t.Fatalf("writing log file: %v", err)
	}

	svc := NewLocal(LocalConfig{ConfigDir: dir})

	got, err := svc.GetLogs(context.Background())
	if err != nil {
		t.Fatalf("GetLogs() error = %v", err)
	}
	if got != "" {
		t.Errorf("GetLogs() = %q, want empty string for empty log file", got)
	}
}
