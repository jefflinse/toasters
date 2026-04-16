package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"os"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

// --- Test helpers ---

func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func assertError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func assertEqual(t *testing.T, want, got string) {
	t.Helper()
	if want != got {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Fatalf("expected %q to contain %q", s, substr)
	}
}

// --- Mock provider ---

// mockProvider implements provider.Provider for testing. It returns
// pre-configured responses in order, one per ChatStream call.
type mockProvider struct {
	name      string
	responses []mockResponse
	mu        sync.Mutex
	callIdx   int
	requests  []provider.ChatRequest // captured for inspection
}

type mockResponse struct {
	events []provider.StreamEvent
	err    error
	block  bool // if true, channel stays open until context is cancelled
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) ChatStream(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.requests = append(m.requests, req)

	if m.callIdx >= len(m.responses) {
		return nil, errors.New("no more mock responses")
	}

	resp := m.responses[m.callIdx]
	m.callIdx++

	if resp.err != nil {
		return nil, resp.err
	}

	ch := make(chan provider.StreamEvent, len(resp.events)+1)
	for _, ev := range resp.events {
		ch <- ev
	}

	if resp.block {
		go func() {
			<-ctx.Done()
			close(ch)
		}()
	} else {
		close(ch)
	}
	return ch, nil
}

func (m *mockProvider) Models(_ context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (m *mockProvider) getRequests() []provider.ChatRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]provider.ChatRequest, len(m.requests))
	copy(cp, m.requests)
	return cp
}

// --- Operator test helpers ---

// newOperatorTestStore opens a real SQLite store in a temp directory.
func newOperatorTestStore(t *testing.T) db.Store {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// testPromptEngine creates a prompt engine with system roles in a temp directory.
// Roles are specified as name→body pairs.
func testPromptEngine(t *testing.T, roles map[string]string) *prompt.Engine {
	t.Helper()
	dir := t.TempDir()
	rolesDir := filepath.Join(dir, "roles")
	if err := os.MkdirAll(rolesDir, 0o755); err != nil {
		t.Fatalf("creating roles dir: %v", err)
	}
	for name, body := range roles {
		path := filepath.Join(rolesDir, name+".md")
		content := fmt.Sprintf("---\nname: %s\nmode: worker\ntools:\n  - query_teams\n  - create_job\n  - create_task\n  - assign_task\n  - query_job_context\n---\n%s", name, body)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("writing role %s: %v", name, err)
		}
	}
	engine := prompt.NewEngine()
	if err := engine.LoadDir(dir, "system"); err != nil {
		t.Fatalf("loading prompt engine: %v", err)
	}
	return engine
}

// newTestOperatorTools creates an operatorTools with a real store and prompt engine.
func newTestOperatorTools(t *testing.T, workers []*db.Worker) *operatorTools {
	t.Helper()
	store := newOperatorTestStore(t)
	ctx := context.Background()

	for _, w := range workers {
		if err := store.UpsertWorker(ctx, w); err != nil {
			t.Fatalf("upserting worker: %v", err)
		}
	}

	engine := testPromptEngine(t, map[string]string{
		"scheduler":  "You are a scheduling agent.",
		"decomposer": "You are the decomposer.",
	})

	eventCh := make(chan Event, 64)
	systemTools := NewSystemTools(store, engine, "test-provider", "test-model", eventCh, nil, t.TempDir(), nil, nil)

	return newOperatorTools(nil, engine, "test-provider", "test-model", store, systemTools, t.TempDir())
}

// --- Tests ---

func TestOperatorProcessesUserMessage(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Hello from operator"},
				{Type: provider.EventDone},
			}},
		},
	}

	rt := runtime.New(nil, newTestRegistry(mp))

	var textBuf strings.Builder
	var mu sync.Mutex

	op, err := New(Config{
		Runtime:      rt,
		Provider:     mp,
		Model:        "test-model",
		WorkDir:      t.TempDir(),
		SystemPrompt: "You are the operator.",
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("creating operator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	_ = op.Send(ctx, Event{
		Type:    EventUserMessage,
		Payload: UserMessagePayload{Text: "Hello"},
	})

	// Wait for processing.
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return textBuf.Len() > 0
	}, 2*time.Second)

	mu.Lock()
	got := textBuf.String()
	mu.Unlock()

	assertEqual(t, "Hello from operator", got)
}

func TestOperatorLongLivedSession(t *testing.T) {
	// The operator should maintain conversation context across multiple user
	// messages. We verify by checking that the second ChatStream call receives
	// the full message history (user + assistant + user).
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			// Response to first user message.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "First response"},
				{Type: provider.EventDone},
			}},
			// Response to second user message.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Second response"},
				{Type: provider.EventDone},
			}},
		},
	}

	rt := runtime.New(nil, newTestRegistry(mp))

	var textBuf strings.Builder
	var mu sync.Mutex

	op, err := New(Config{
		Runtime:      rt,
		Provider:     mp,
		Model:        "test-model",
		WorkDir:      t.TempDir(),
		SystemPrompt: "You are the operator.",
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("creating operator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	// Send first message.
	_ = op.Send(ctx, Event{
		Type:    EventUserMessage,
		Payload: UserMessagePayload{Text: "First message"},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(textBuf.String(), "First response")
	}, 2*time.Second)

	// Send second message.
	_ = op.Send(ctx, Event{
		Type:    EventUserMessage,
		Payload: UserMessagePayload{Text: "Second message"},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(textBuf.String(), "Second response")
	}, 2*time.Second)

	// Verify the second ChatStream call received the full history.
	reqs := mp.getRequests()
	if len(reqs) != 2 {
		t.Fatalf("want 2 ChatStream calls, got %d", len(reqs))
	}

	// First call: 1 user message.
	if len(reqs[0].Messages) != 1 {
		t.Fatalf("first call: want 1 message, got %d", len(reqs[0].Messages))
	}
	assertEqual(t, "user", reqs[0].Messages[0].Role)
	assertEqual(t, "First message", reqs[0].Messages[0].Content)

	// Second call: user + assistant + user = 3 messages.
	if len(reqs[1].Messages) != 3 {
		t.Fatalf("second call: want 3 messages, got %d", len(reqs[1].Messages))
	}
	assertEqual(t, "user", reqs[1].Messages[0].Role)
	assertEqual(t, "assistant", reqs[1].Messages[1].Role)
	assertEqual(t, "user", reqs[1].Messages[2].Role)
	assertEqual(t, "Second message", reqs[1].Messages[2].Content)

	// Verify message count reflects the full history.
	if op.MessageCount() != 4 { // user, assistant, user, assistant
		t.Fatalf("want 4 messages in history, got %d", op.MessageCount())
	}
}

func TestOperatorConsultWorker(t *testing.T) {
	// The operator calls consult_worker("scheduler", "..."), which spawns a
	// fresh session via runtime.SpawnAndWait. We need the scheduler worker
	// in the DB so the composer can look it up.
	//
	// Since both the operator and the scheduler use the same provider registry,
	// we use a single mock provider that handles multiple ChatStream calls:
	//   1. Operator's first response: tool call to consult_worker
	//   2. Scheduler worker's response: "Here is the schedule: ..."
	//   3. Operator's second response (after seeing tool result): final text
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			// 1. Operator calls consult_worker.
			{events: []provider.StreamEvent{
				{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID:        "call-1",
					Name:      "consult_worker",
					Arguments: json.RawMessage(`{"worker_name": "scheduler", "message": "Schedule the tasks"}`),
				}},
				{Type: provider.EventDone},
			}},
			// 2. Scheduler worker responds.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Schedule: 1) Setup 2) Build 3) Deploy"},
				{Type: provider.EventDone},
			}},
			// 3. Operator responds after seeing scheduler's result.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "The scheduler suggests: Setup, Build, Deploy"},
				{Type: provider.EventDone},
			}},
		},
	}

	store := newOperatorTestStore(t)
	ctx := context.Background()

	engine := testPromptEngine(t, map[string]string{
		"scheduler": "You are a scheduling agent. Analyze requests and schedule tasks.",
	})

	rt := runtime.New(store, newTestRegistry(mp))

	var textBuf strings.Builder
	var mu sync.Mutex

	op, err := New(Config{
		Runtime:         rt,
		Provider:        mp,
		Model:           "test-model",
		WorkDir:         t.TempDir(),
		Store:           store,
		PromptEngine:    engine,
		DefaultProvider: "test",
		DefaultModel:    "test-model",
		SystemPrompt:    "You are the operator.",
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("creating operator: %v", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	op.Start(ctx)

	_ = op.Send(ctx, Event{
		Type:    EventUserMessage,
		Payload: UserMessagePayload{Text: "Build me a web app"},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(textBuf.String(), "scheduler suggests")
	}, 5*time.Second)

	mu.Lock()
	got := textBuf.String()
	mu.Unlock()

	assertContains(t, got, "scheduler suggests")

	// Verify the operator's message history includes the tool call round-trip.
	// Expected: user, assistant (tool call), tool (result), assistant (final).
	if op.MessageCount() != 4 {
		t.Fatalf("want 4 messages in history, got %d", op.MessageCount())
	}
}

func TestOperatorEventCallbackFires(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "ok"},
				{Type: provider.EventDone},
			}},
		},
	}

	rt := runtime.New(nil, newTestRegistry(mp))

	var events []Event
	var mu sync.Mutex

	op, err := New(Config{
		Runtime:      rt,
		Provider:     mp,
		Model:        "test-model",
		WorkDir:      t.TempDir(),
		SystemPrompt: "You are the operator.",
		OnEvent: func(ev Event) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("creating operator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	_ = op.Send(ctx, Event{
		Type:    EventUserMessage,
		Payload: UserMessagePayload{Text: "Hello"},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) > 0
	}, 2*time.Second)

	mu.Lock()
	defer mu.Unlock()

	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	if events[0].Type != EventUserMessage {
		t.Fatalf("want EventUserMessage, got %s", events[0].Type)
	}
}

func TestOperatorMechanicalEvents(t *testing.T) {
	// Purely mechanical events (task_started, progress_update) should be
	// handled without calling the LLM. We verify by checking that no
	// ChatStream calls are made.
	mp := &mockProvider{
		name:      "test",
		responses: []mockResponse{}, // no responses — LLM should not be called
	}

	rt := runtime.New(nil, newTestRegistry(mp))

	var events []Event
	var mu sync.Mutex

	op, err := New(Config{
		Runtime:      rt,
		Provider:     mp,
		Model:        "test-model",
		WorkDir:      t.TempDir(),
		SystemPrompt: "You are the operator.",
		OnEvent: func(ev Event) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("creating operator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	// Send purely mechanical events.
	_ = op.Send(ctx, Event{
		Type: EventTaskStarted,
		Payload: TaskStartedPayload{
			TaskID: "task-0",
			JobID:  "job-1",
			TeamID: "team-1",
			Title:  "Setup",
		},
	})
	_ = op.Send(ctx, Event{
		Type: EventProgressUpdate,
		Payload: ProgressUpdatePayload{
			TaskID:  "task-1",
			WorkerID: "agent-2",
			Message: "50% complete",
		},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) >= 2
	}, 2*time.Second)

	mu.Lock()
	defer mu.Unlock()

	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d", len(events))
	}

	// Verify no ChatStream calls were made.
	reqs := mp.getRequests()
	if len(reqs) != 0 {
		t.Fatalf("want 0 ChatStream calls for mechanical events, got %d", len(reqs))
	}
}

func TestEventLoop_TaskStarted_CreatesFeedEntry(t *testing.T) {
	store := newOperatorTestStore(t)
	mp := &mockProvider{
		name:      "test",
		responses: []mockResponse{},
	}
	rt := runtime.New(nil, newTestRegistry(mp))

	var events []Event
	var mu sync.Mutex

	op, err := New(Config{
		Runtime:      rt,
		Provider:     mp,
		Model:        "test-model",
		WorkDir:      t.TempDir(),
		Store:        store,
		SystemPrompt: "You are the operator.",
		OnEvent: func(ev Event) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("creating operator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	_ = op.Send(ctx, Event{
		Type: EventTaskStarted,
		Payload: TaskStartedPayload{
			TaskID: "task-1",
			JobID:  "job-1",
			TeamID: "backend",
			Title:  "Build API",
		},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) >= 1
	}, 2*time.Second)

	// Verify feed entry was created.
	entries, err := store.ListRecentFeedEntries(context.Background(), 10)
	assertNoError(t, err)
	if len(entries) != 1 {
		t.Fatalf("want 1 feed entry, got %d", len(entries))
	}
	assertEqual(t, string(db.FeedEntryTaskStarted), string(entries[0].EntryType))
	assertContains(t, entries[0].Content, "backend")
	assertContains(t, entries[0].Content, "Build API")
}

func TestEventLoop_TaskCompleted_AssignsNextTask(t *testing.T) {
	store := newOperatorTestStore(t)
	ctx := context.Background()

	// Seed a team with a lead worker.
	seedTeam(t, ctx, store, "backend", "Backend Team", "lead-agent")

	// Create a job.
	job := &db.Job{
		ID:          "job-1",
		Title:       "Test Job",
		Description: "A test job",
		Status:      db.JobStatusActive,
	}
	assertNoError(t, store.CreateJob(ctx, job))

	// Create a completed task and a pending next task with pre-assigned team.
	task1 := &db.Task{
		ID:     "task-1",
		JobID:  "job-1",
		Title:  "First task",
		Status: db.TaskStatusCompleted,
		TeamID: "backend",
	}
	assertNoError(t, store.CreateTask(ctx, task1))

	task2 := &db.Task{
		ID:     "task-2",
		JobID:  "job-1",
		Title:  "Second task",
		Status: db.TaskStatusPending,
		TeamID: "backend",
	}
	assertNoError(t, store.CreateTask(ctx, task2))

	// The mock provider needs to handle the spawned worker's ChatStream call.
	mp := &mockProvider{
		name: "test-provider",
		responses: []mockResponse{
			// Spawned team lead worker responds.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Working on second task"},
				{Type: provider.EventDone},
			}},
		},
	}

	reg := newTestRegistry(mp)
	rt := runtime.New(store, reg)
	spawner := &mockSpawner{}

	// Create operator with SystemTools that have a spawner and prompt engine.
	engine := testPromptEngine(t, map[string]string{
		"lead-agent": "You are a test lead.",
	})
	eventCh := make(chan Event, eventChSize)
	systemTools := NewSystemTools(store, engine, "test-provider", "test-model", eventCh, spawner, t.TempDir(), nil, nil)
	tools := newOperatorTools(rt, engine, "test-provider", "test-model", store, systemTools, t.TempDir())
	provTools := operatorToolsToProviderTools(tools.Definitions())

	var events []Event
	var mu sync.Mutex

	op := &Operator{
		rt:           rt,
		prov:         mp,
		model:        "test-model",
		tools:        tools,
		store:        store,
		eventCh:      eventCh,
		workDir:      t.TempDir(),
		systemPrompt: "You are the operator.",
		provTools:    provTools,
		onEvent: func(ev Event) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		},
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	op.Start(ctx)

	// Send TaskCompleted with HasNextTask=true.
	_ = op.Send(ctx, Event{
		Type: EventTaskCompleted,
		Payload: TaskCompletedPayload{
			TaskID:      "task-1",
			JobID:       "job-1",
			TeamID:      "backend",
			Summary:     "First task done",
			HasNextTask: true,
		},
	})

	// Wait for the spawner to be called (assign_task spawns the team lead).
	waitFor(t, func() bool {
		return len(spawner.getCalls()) > 0
	}, 3*time.Second)

	// Verify the next task was assigned.
	calls := spawner.getCalls()
	if len(calls) != 1 {
		t.Fatalf("want 1 spawn call, got %d", len(calls))
	}
	assertEqual(t, "task-2", calls[0].TaskID)
	assertEqual(t, "job-1", calls[0].JobID)

	// Verify task-2 status changed to in_progress.
	updatedTask, err := store.GetTask(ctx, "task-2")
	assertNoError(t, err)
	assertEqual(t, string(db.TaskStatusInProgress), string(updatedTask.Status))
}

func TestEventLoop_TaskCompleted_ChecksJobComplete(t *testing.T) {
	store := newOperatorTestStore(t)
	ctx := context.Background()

	// Create a job with a single task (already completed).
	job := &db.Job{
		ID:          "job-1",
		Title:       "Test Job",
		Description: "A test job",
		Status:      db.JobStatusActive,
	}
	assertNoError(t, store.CreateJob(ctx, job))

	task := &db.Task{
		ID:     "task-1",
		JobID:  "job-1",
		Title:  "Only task",
		Status: db.TaskStatusCompleted,
	}
	assertNoError(t, store.CreateTask(ctx, task))

	mp := &mockProvider{
		name:      "test",
		responses: []mockResponse{},
	}
	rt := runtime.New(nil, newTestRegistry(mp))

	var textBuf strings.Builder
	var mu sync.Mutex

	op, err := New(Config{
		Runtime:      rt,
		Provider:     mp,
		Model:        "test-model",
		WorkDir:      t.TempDir(),
		Store:        store,
		SystemPrompt: "You are the operator.",
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("creating operator: %v", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	op.Start(ctx)

	// Send TaskCompleted with no next task and no recommendations.
	// This should trigger checkJobComplete → EventJobComplete.
	_ = op.Send(ctx, Event{
		Type: EventTaskCompleted,
		Payload: TaskCompletedPayload{
			TaskID:      "task-1",
			JobID:       "job-1",
			TeamID:      "backend",
			Summary:     "All done",
			HasNextTask: false,
		},
	})

	// Wait for the job to be marked complete.
	waitFor(t, func() bool {
		j, err := store.GetJob(context.Background(), "job-1")
		if err != nil {
			return false
		}
		return j.Status == db.JobStatusCompleted
	}, 3*time.Second)

	// Verify job status.
	updatedJob, err := store.GetJob(ctx, "job-1")
	assertNoError(t, err)
	assertEqual(t, string(db.JobStatusCompleted), string(updatedJob.Status))

	// Verify feed entries: one for task_completed, one for job_complete.
	entries, err := store.ListRecentFeedEntries(ctx, 10)
	assertNoError(t, err)
	if len(entries) < 2 {
		t.Fatalf("want at least 2 feed entries, got %d", len(entries))
	}

	// Check that a job_complete feed entry exists.
	var foundJobComplete bool
	for _, e := range entries {
		if e.EntryType == db.FeedEntryJobComplete {
			foundJobComplete = true
			assertContains(t, e.Content, "Test Job")
		}
	}
	if !foundJobComplete {
		t.Fatal("expected job_complete feed entry")
	}
}

func TestEventLoop_TaskCompleted_WithRecommendations(t *testing.T) {
	store := newOperatorTestStore(t)
	ctx := context.Background()

	// Create a job with a single completed task.
	job := &db.Job{
		ID:          "job-1",
		Title:       "Test Job",
		Description: "A test job",
		Status:      db.JobStatusActive,
	}
	assertNoError(t, store.CreateJob(ctx, job))

	task := &db.Task{
		ID:     "task-1",
		JobID:  "job-1",
		Title:  "Only task",
		Status: db.TaskStatusCompleted,
	}
	assertNoError(t, store.CreateTask(ctx, task))

	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			// LLM responds to the recommendations consultation.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "I'll create a follow-up task for caching."},
				{Type: provider.EventDone},
			}},
		},
	}
	rt := runtime.New(nil, newTestRegistry(mp))

	var textBuf strings.Builder
	var mu sync.Mutex

	op, err := New(Config{
		Runtime:      rt,
		Provider:     mp,
		Model:        "test-model",
		WorkDir:      t.TempDir(),
		Store:        store,
		SystemPrompt: "You are the operator.",
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("creating operator: %v", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	op.Start(ctx)

	// Send TaskCompleted with recommendations but no next task.
	_ = op.Send(ctx, Event{
		Type: EventTaskCompleted,
		Payload: TaskCompletedPayload{
			TaskID:          "task-1",
			JobID:           "job-1",
			TeamID:          "backend",
			Summary:         "API built",
			Recommendations: "Add caching layer for performance",
			HasNextTask:     false,
		},
	})

	// Wait for LLM to be consulted.
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(textBuf.String(), "follow-up task")
	}, 3*time.Second)

	// Verify ChatStream was called (LLM was consulted).
	reqs := mp.getRequests()
	if len(reqs) == 0 {
		t.Fatal("expected ChatStream to be called for recommendations")
	}

	// Verify the message sent to LLM contains the recommendations.
	lastReq := reqs[0]
	if len(lastReq.Messages) == 0 {
		t.Fatal("expected messages in ChatStream request")
	}
	lastMsg := lastReq.Messages[len(lastReq.Messages)-1]
	assertContains(t, lastMsg.Content, "Add caching layer for performance")
}

func TestEventLoop_TaskFailed_RoutesToLLM(t *testing.T) {
	store := newOperatorTestStore(t)

	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			// LLM responds to the failure notification.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "I'll retry the task with a different approach."},
				{Type: provider.EventDone},
			}},
		},
	}
	rt := runtime.New(nil, newTestRegistry(mp))

	var textBuf strings.Builder
	var mu sync.Mutex

	op, err := New(Config{
		Runtime:      rt,
		Provider:     mp,
		Model:        "test-model",
		WorkDir:      t.TempDir(),
		Store:        store,
		SystemPrompt: "You are the operator.",
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("creating operator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	_ = op.Send(ctx, Event{
		Type: EventTaskFailed,
		Payload: TaskFailedPayload{
			TaskID: "task-1",
			JobID:  "job-1",
			TeamID: "backend",
			Error:  "compilation error in main.go",
		},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(textBuf.String(), "retry")
	}, 3*time.Second)

	// Verify ChatStream was called.
	reqs := mp.getRequests()
	if len(reqs) == 0 {
		t.Fatal("expected ChatStream to be called for task failure")
	}

	// Verify the message contains the error.
	lastMsg := reqs[0].Messages[len(reqs[0].Messages)-1]
	assertContains(t, lastMsg.Content, "compilation error in main.go")

	// Verify feed entry was created.
	entries, err := store.ListRecentFeedEntries(context.Background(), 10)
	assertNoError(t, err)
	var foundFailed bool
	for _, e := range entries {
		if e.EntryType == db.FeedEntryTaskFailed {
			foundFailed = true
			assertContains(t, e.Content, "compilation error")
		}
	}
	if !foundFailed {
		t.Fatal("expected task_failed feed entry")
	}
}

func TestEventLoop_BlockerReported_RoutesToLLM(t *testing.T) {
	store := newOperatorTestStore(t)

	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			// LLM responds to the blocker notification.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "I'll consult the blocker-handler to resolve this."},
				{Type: provider.EventDone},
			}},
		},
	}
	rt := runtime.New(nil, newTestRegistry(mp))

	var textBuf strings.Builder
	var mu sync.Mutex

	op, err := New(Config{
		Runtime:      rt,
		Provider:     mp,
		Model:        "test-model",
		WorkDir:      t.TempDir(),
		Store:        store,
		SystemPrompt: "You are the operator.",
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("creating operator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	_ = op.Send(ctx, Event{
		Type: EventBlockerReported,
		Payload: BlockerReportedPayload{
			TaskID:      "task-1",
			TeamID:      "backend",
			WorkerID:    "worker-1",
			Description: "Cannot access production database",
		},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(textBuf.String(), "blocker-handler")
	}, 3*time.Second)

	// Verify ChatStream was called.
	reqs := mp.getRequests()
	if len(reqs) == 0 {
		t.Fatal("expected ChatStream to be called for blocker")
	}

	// Verify the message contains the blocker description.
	lastMsg := reqs[0].Messages[len(reqs[0].Messages)-1]
	assertContains(t, lastMsg.Content, "Cannot access production database")

	// Verify feed entry was created.
	entries, err := store.ListRecentFeedEntries(context.Background(), 10)
	assertNoError(t, err)
	var foundBlocker bool
	for _, e := range entries {
		if e.EntryType == db.FeedEntryBlockerReported {
			foundBlocker = true
			assertContains(t, e.Content, "Cannot access production database")
		}
	}
	if !foundBlocker {
		t.Fatal("expected blocker_reported feed entry")
	}
}

func TestEventLoop_ProgressUpdate_NoFeedEntry(t *testing.T) {
	store := newOperatorTestStore(t)
	mp := &mockProvider{
		name:      "test",
		responses: []mockResponse{},
	}
	rt := runtime.New(nil, newTestRegistry(mp))

	var events []Event
	var mu sync.Mutex

	op, err := New(Config{
		Runtime:      rt,
		Provider:     mp,
		Model:        "test-model",
		WorkDir:      t.TempDir(),
		Store:        store,
		SystemPrompt: "You are the operator.",
		OnEvent: func(ev Event) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("creating operator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	_ = op.Send(ctx, Event{
		Type: EventProgressUpdate,
		Payload: ProgressUpdatePayload{
			TaskID:  "task-1",
			WorkerID: "agent-1",
			Message: "50% complete",
		},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) >= 1
	}, 2*time.Second)

	// Verify NO feed entry was created.
	entries, err := store.ListRecentFeedEntries(context.Background(), 10)
	assertNoError(t, err)
	if len(entries) != 0 {
		t.Fatalf("want 0 feed entries for progress update, got %d", len(entries))
	}

	// Verify no ChatStream calls.
	reqs := mp.getRequests()
	if len(reqs) != 0 {
		t.Fatalf("want 0 ChatStream calls for progress update, got %d", len(reqs))
	}
}

func TestEventLoop_JobComplete_MarksDone(t *testing.T) {
	store := newOperatorTestStore(t)
	ctx := context.Background()

	// Create a job.
	job := &db.Job{
		ID:          "job-1",
		Title:       "Build web app",
		Description: "A test job",
		Status:      db.JobStatusActive,
	}
	assertNoError(t, store.CreateJob(ctx, job))

	mp := &mockProvider{
		name:      "test",
		responses: []mockResponse{},
	}
	rt := runtime.New(nil, newTestRegistry(mp))

	var textBuf strings.Builder
	var mu sync.Mutex

	op, err := New(Config{
		Runtime:      rt,
		Provider:     mp,
		Model:        "test-model",
		WorkDir:      t.TempDir(),
		Store:        store,
		SystemPrompt: "You are the operator.",
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("creating operator: %v", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	op.Start(ctx)

	_ = op.Send(ctx, Event{
		Type: EventJobComplete,
		Payload: JobCompletePayload{
			JobID:   "job-1",
			Title:   "Build web app",
			Summary: "All tasks completed successfully",
		},
	})

	// Wait for the job status to be updated.
	waitFor(t, func() bool {
		j, err := store.GetJob(context.Background(), "job-1")
		if err != nil {
			return false
		}
		return j.Status == db.JobStatusCompleted
	}, 2*time.Second)

	// Verify job status.
	updatedJob, err := store.GetJob(ctx, "job-1")
	assertNoError(t, err)
	assertEqual(t, string(db.JobStatusCompleted), string(updatedJob.Status))

	// Verify feed entry.
	entries, err := store.ListRecentFeedEntries(ctx, 10)
	assertNoError(t, err)
	if len(entries) != 1 {
		t.Fatalf("want 1 feed entry, got %d", len(entries))
	}
	assertEqual(t, string(db.FeedEntryJobComplete), string(entries[0].EntryType))
	assertContains(t, entries[0].Content, "Build web app")

	// Verify OnText was called with the completion message.
	mu.Lock()
	got := textBuf.String()
	mu.Unlock()
	assertContains(t, got, "Job complete")

	// Verify no ChatStream calls.
	reqs := mp.getRequests()
	if len(reqs) != 0 {
		t.Fatalf("want 0 ChatStream calls for job complete, got %d", len(reqs))
	}
}

func TestEventLoop_UserResponse_RoutesToLLM(t *testing.T) {
	store := newOperatorTestStore(t)

	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			// LLM responds to the user's response.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Great, proceeding with the plan."},
				{Type: provider.EventDone},
			}},
		},
	}
	rt := runtime.New(nil, newTestRegistry(mp))

	var textBuf strings.Builder
	var mu sync.Mutex

	op, err := New(Config{
		Runtime:      rt,
		Provider:     mp,
		Model:        "test-model",
		WorkDir:      t.TempDir(),
		Store:        store,
		SystemPrompt: "You are the operator.",
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("creating operator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	_ = op.Send(ctx, Event{
		Type: EventUserResponse,
		Payload: UserResponsePayload{
			Text:      "Yes, proceed with option 2",
			RequestID: "req-42",
		},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(textBuf.String(), "proceeding")
	}, 3*time.Second)

	// Verify ChatStream was called.
	reqs := mp.getRequests()
	if len(reqs) == 0 {
		t.Fatal("expected ChatStream to be called for user response")
	}

	// Verify the message contains the user's response and request ID.
	lastMsg := reqs[0].Messages[len(reqs[0].Messages)-1]
	assertContains(t, lastMsg.Content, "Yes, proceed with option 2")
	assertContains(t, lastMsg.Content, "req-42")

	// Verify feed entry was created.
	entries, err := store.ListRecentFeedEntries(context.Background(), 10)
	assertNoError(t, err)
	var foundUserMsg bool
	for _, e := range entries {
		if e.EntryType == db.FeedEntryUserMessage {
			foundUserMsg = true
			assertContains(t, e.Content, "Yes, proceed with option 2")
		}
	}
	if !foundUserMsg {
		t.Fatal("expected user_message feed entry for user response")
	}
}

func TestOperatorCleanShutdown(t *testing.T) {
	mp := &mockProvider{
		name:      "test",
		responses: []mockResponse{},
	}

	rt := runtime.New(nil, newTestRegistry(mp))

	op, err := New(Config{
		Runtime:      rt,
		Provider:     mp,
		Model:        "test-model",
		WorkDir:      t.TempDir(),
		SystemPrompt: "You are the operator.",
	})
	if err != nil {
		t.Fatalf("creating operator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		op.run(ctx)
		close(done)
	}()

	// Cancel should cause the event loop to exit cleanly.
	cancel()

	select {
	case <-done:
		// Event loop exited cleanly.
	case <-time.After(2 * time.Second):
		t.Fatal("event loop did not exit within timeout")
	}
}

func TestOperatorChatStreamError(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{err: errors.New("connection refused")},
		},
	}

	rt := runtime.New(nil, newTestRegistry(mp))

	var textBuf strings.Builder
	var mu sync.Mutex

	op, err := New(Config{
		Runtime:      rt,
		Provider:     mp,
		Model:        "test-model",
		WorkDir:      t.TempDir(),
		SystemPrompt: "You are the operator.",
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("creating operator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	_ = op.Send(ctx, Event{
		Type:    EventUserMessage,
		Payload: UserMessagePayload{Text: "Hello"},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return textBuf.Len() > 0
	}, 2*time.Second)

	mu.Lock()
	got := textBuf.String()
	mu.Unlock()

	assertContains(t, got, "operator error")
	assertContains(t, got, "connection refused")
}

func TestOperatorStreamError(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "partial"},
				{Type: provider.EventError, Error: errors.New("stream broke")},
			}},
		},
	}

	rt := runtime.New(nil, newTestRegistry(mp))

	var textBuf strings.Builder
	var mu sync.Mutex

	op, err := New(Config{
		Runtime:      rt,
		Provider:     mp,
		Model:        "test-model",
		WorkDir:      t.TempDir(),
		SystemPrompt: "You are the operator.",
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("creating operator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	_ = op.Send(ctx, Event{
		Type:    EventUserMessage,
		Payload: UserMessagePayload{Text: "Hello"},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(textBuf.String(), "operator error")
	}, 2*time.Second)

	mu.Lock()
	got := textBuf.String()
	mu.Unlock()

	// Should have received partial text before the error.
	assertContains(t, got, "partial")
	assertContains(t, got, "stream broke")
}

func TestOperatorSurfaceToUser(t *testing.T) {
	// Operator calls surface_to_user, gets the result back, then responds.
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			// Operator calls surface_to_user.
			{events: []provider.StreamEvent{
				{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID:        "call-1",
					Name:      "surface_to_user",
					Arguments: json.RawMessage(`{"text": "Important update"}`),
				}},
				{Type: provider.EventDone},
			}},
			// Operator responds after seeing tool result.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "I've surfaced the update."},
				{Type: provider.EventDone},
			}},
		},
	}

	rt := runtime.New(nil, newTestRegistry(mp))

	var textBuf strings.Builder
	var mu sync.Mutex

	op, err := New(Config{
		Runtime:      rt,
		Provider:     mp,
		Model:        "test-model",
		WorkDir:      t.TempDir(),
		SystemPrompt: "You are the operator.",
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("creating operator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	_ = op.Send(ctx, Event{
		Type:    EventUserMessage,
		Payload: UserMessagePayload{Text: "Give me an update"},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(textBuf.String(), "surfaced the update")
	}, 2*time.Second)
}

func TestConsultWorker_ComposedWorker(t *testing.T) {
	// Verify that consult_worker uses the prompt engine to look up and compose the
	// worker, including the worker's system prompt and resolved provider/model.
	store := newOperatorTestStore(t)

	engine := testPromptEngine(t, map[string]string{
		"scheduler": "You are a scheduling agent. Create detailed schedules.",
	})

	mp := &mockProvider{
		name: "custom-provider",
		responses: []mockResponse{
			// 1. Operator calls consult_worker.
			{events: []provider.StreamEvent{
				{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID:        "call-1",
					Name:      "consult_worker",
					Arguments: json.RawMessage(`{"worker_name": "scheduler", "message": "Schedule a migration"}`),
				}},
				{Type: provider.EventDone},
			}},
			// 2. Scheduler worker responds (uses custom-provider).
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Migration schedule: step 1, step 2"},
				{Type: provider.EventDone},
			}},
			// 3. Operator responds after seeing tool result.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "The scheduler created a migration schedule."},
				{Type: provider.EventDone},
			}},
		},
	}

	reg := provider.NewRegistry()
	reg.Register("custom-provider", mp)

	rt := runtime.New(store, reg)

	var textBuf strings.Builder
	var mu sync.Mutex

	op, err := New(Config{
		Runtime:         rt,
		Provider:        mp,
		Model:           "default-model",
		WorkDir:         t.TempDir(),
		Store:           store,
		PromptEngine:    engine,
		DefaultProvider: "custom-provider",
		DefaultModel:    "default-model",
		SystemPrompt:    "You are the operator.",
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("creating operator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	_ = op.Send(ctx, Event{
		Type:    EventUserMessage,
		Payload: UserMessagePayload{Text: "Schedule a migration"},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(textBuf.String(), "migration schedule")
	}, 5*time.Second)

	mu.Lock()
	got := textBuf.String()
	mu.Unlock()

	assertContains(t, got, "migration schedule")

	// Verify the scheduler's ChatStream call used the default provider/model.
	reqs := mp.getRequests()
	if len(reqs) < 2 {
		t.Fatalf("want at least 2 ChatStream calls, got %d", len(reqs))
	}

	// The second call is the scheduler worker's session.
	schedulerReq := reqs[1]
	assertEqual(t, "default-model", schedulerReq.Model)

	// Verify the scheduler's system prompt came from the prompt engine.
	assertContains(t, schedulerReq.System, "You are a scheduling agent")
}

func TestConsultWorker_UnknownWorker(t *testing.T) {
	// Verify that consult_worker returns an error when the worker is not found
	// in the prompt engine.
	tools := newTestOperatorTools(t, nil) // no workers seeded

	_, err := tools.Execute(context.Background(), "consult_worker",
		json.RawMessage(`{"worker_name": "nonexistent", "message": "hello"}`))
	assertError(t, err)
	assertContains(t, err.Error(), "unknown system worker")
	assertContains(t, err.Error(), "nonexistent")
}

func TestConsultWorkerMissingParams(t *testing.T) {
	tools := newTestOperatorTools(t, nil)

	// Missing worker_name.
	_, err := tools.Execute(context.Background(), "consult_worker",
		json.RawMessage(`{"message": "hello"}`))
	assertError(t, err)
	assertContains(t, err.Error(), "worker_name is required")

	// Missing message.
	_, err = tools.Execute(context.Background(), "consult_worker",
		json.RawMessage(`{"worker_name": "scheduler"}`))
	assertError(t, err)
	assertContains(t, err.Error(), "message is required")
}

func TestSurfaceToUserMissingText(t *testing.T) {
	tools := newTestOperatorTools(t, nil)

	_, err := tools.Execute(context.Background(), "surface_to_user",
		json.RawMessage(`{}`))
	assertError(t, err)
	assertContains(t, err.Error(), "text is required")
}

func TestSurfaceToUserCreatesFeedEntry(t *testing.T) {
	store := newOperatorTestStore(t)
	eventCh := make(chan Event, 64)
	systemTools := NewSystemTools(store, nil, "test-provider", "test-model", eventCh, nil, t.TempDir(), nil, nil)
	tools := newOperatorTools(nil, nil, "test-provider", "test-model", store, systemTools, t.TempDir())

	result, err := tools.Execute(context.Background(), "surface_to_user",
		json.RawMessage(`{"text": "Important update"}`))
	assertNoError(t, err)
	assertContains(t, result, "Surfaced to user")

	// Verify feed entry was created in the DB.
	entries, err := store.ListRecentFeedEntries(context.Background(), 10)
	assertNoError(t, err)
	if len(entries) != 1 {
		t.Fatalf("want 1 feed entry, got %d", len(entries))
	}
	assertEqual(t, "Important update", entries[0].Content)
	assertEqual(t, string(db.FeedEntrySystemEvent), string(entries[0].EntryType))
}

func TestSurfaceToUserWithoutSystemTools(t *testing.T) {
	// surface_to_user should return an error when no system tools are configured.
	tools := newOperatorTools(nil, nil, "test-provider", "test-model", nil, nil, t.TempDir())

	_, err := tools.Execute(context.Background(), "surface_to_user",
		json.RawMessage(`{"text": "No store available"}`))
	assertError(t, err)
	assertContains(t, err.Error(), "surface_to_user unavailable")
}

func TestOperatorToolsUnknownTool(t *testing.T) {
	tools := newTestOperatorTools(t, nil)

	_, err := tools.Execute(context.Background(), "nonexistent", json.RawMessage(`{}`))
	assertError(t, err)
	if !errors.Is(err, runtime.ErrUnknownTool) {
		t.Fatalf("want ErrUnknownTool, got %v", err)
	}
}

func TestOperatorToolDefinitions(t *testing.T) {
	tools := newTestOperatorTools(t, nil)
	defs := tools.Definitions()

	if len(defs) != 12 {
		t.Fatalf("want 12 tool definitions, got %d", len(defs))
	}

	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true
	}

	for _, expected := range []string{"consult_worker", "surface_to_user", "list_jobs", "query_job", "query_teams", "setup_workspace", "create_job", "create_task", "assign_task", "save_work_request", "start_job", "ask_user"} {
		if !names[expected] {
			t.Errorf("expected %s in definitions", expected)
		}
	}
}

func TestQueryJobDelegatesToSystemTools(t *testing.T) {
	store := newOperatorTestStore(t)
	ctx := context.Background()

	// Create a job in the DB.
	if err := store.CreateJob(ctx, &db.Job{
		ID:          "job-1",
		Title:       "Test Job",
		Description: "A test job",
		Status:      db.JobStatusPending,
	}); err != nil {
		t.Fatalf("creating job: %v", err)
	}

	eventCh := make(chan Event, 64)
	systemTools := NewSystemTools(store, nil, "test-provider", "test-model", eventCh, nil, t.TempDir(), nil, nil)
	tools := newOperatorTools(nil, nil, "test-provider", "test-model", store, systemTools, t.TempDir())

	result, err := tools.Execute(ctx, "query_job",
		json.RawMessage(`{"job_id": "job-1"}`))
	assertNoError(t, err)
	assertContains(t, result, "Test Job")
	assertContains(t, result, "job-1")
}

func TestQueryTeamsDelegatesToSystemTools(t *testing.T) {
	store := newOperatorTestStore(t)
	ctx := context.Background()

	// Seed a team.
	seedTeam(t, ctx, store, "team-1", "Alpha Team", "lead-1")

	eventCh := make(chan Event, 64)
	systemTools := NewSystemTools(store, nil, "test-provider", "test-model", eventCh, nil, t.TempDir(), nil, nil)
	tools := newOperatorTools(nil, nil, "test-provider", "test-model", store, systemTools, t.TempDir())

	result, err := tools.Execute(ctx, "query_teams", json.RawMessage(`{}`))
	assertNoError(t, err)
	assertContains(t, result, "Alpha Team")
}

// --- Regression tests for Bug 1: consultWorker tool filtering ---

// TestConsultWorker_ToolFiltering is a regression test for the bug where
// consultWorker built SpawnOpts without passing composed.Tools, so the spawned
// system worker always saw ALL system tools instead of only its declared tools.
//
// The fix converts composed.Tools ([]string) to []runtime.ToolDef by looking
// up names in the tool executor's Definitions(), then passes them as SpawnOpts.Tools.
//
// Without the fix, the scheduler's ChatStream request would contain all system
// tools instead of only the 2 declared ones.
func TestConsultWorker_ToolFiltering(t *testing.T) {
	store := newOperatorTestStore(t)

	// Create a prompt engine role with only two tools declared.
	dir := t.TempDir()
	rolesDir := filepath.Join(dir, "roles")
	if err := os.MkdirAll(rolesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	schedulerRole := "---\nname: Scheduler\nmode: worker\ntools:\n  - create_job\n  - create_task\n---\nYou are a scheduling agent."
	if err := os.WriteFile(filepath.Join(rolesDir, "scheduler.md"), []byte(schedulerRole), 0o644); err != nil {
		t.Fatal(err)
	}
	engine := prompt.NewEngine()
	if err := engine.LoadDir(dir, "system"); err != nil {
		t.Fatal(err)
	}

	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			// 1. Operator calls consult_worker.
			{events: []provider.StreamEvent{
				{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID:        "call-1",
					Name:      "consult_worker",
					Arguments: json.RawMessage(`{"worker_name": "scheduler", "message": "Schedule something"}`),
				}},
				{Type: provider.EventDone},
			}},
			// 2. Scheduler worker responds (only sees its declared tools).
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Here is the schedule."},
				{Type: provider.EventDone},
			}},
			// 3. Operator responds after seeing scheduler's result.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Schedule received."},
				{Type: provider.EventDone},
			}},
		},
	}

	reg := provider.NewRegistry()
	reg.Register("test", mp)

	rt := runtime.New(store, reg)

	var textBuf strings.Builder
	var mu sync.Mutex

	op, err := New(Config{
		Runtime:         rt,
		Provider:        mp,
		Model:           "test-model",
		WorkDir:         t.TempDir(),
		Store:           store,
		PromptEngine:    engine,
		DefaultProvider: "test",
		DefaultModel:    "test-model",
		SystemPrompt:    "You are the operator.",
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("creating operator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	_ = op.Send(ctx, Event{
		Type:    EventUserMessage,
		Payload: UserMessagePayload{Text: "Schedule something"},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(textBuf.String(), "Schedule received")
	}, 5*time.Second)

	// The second ChatStream call is the scheduler worker's session.
	// Its Tools list must contain ONLY the two declared tools, not all system tools.
	reqs := mp.getRequests()
	if len(reqs) < 2 {
		t.Fatalf("want at least 2 ChatStream calls, got %d", len(reqs))
	}

	schedulerReq := reqs[1]

	// Build a set of tool names from the scheduler's request.
	schedulerToolNames := make(map[string]bool, len(schedulerReq.Tools))
	for _, tool := range schedulerReq.Tools {
		schedulerToolNames[tool.Name] = true
	}

	// The scheduler must see its declared tools (create_job, create_task).
	for _, expected := range []string{"create_job", "create_task"} {
		if !schedulerToolNames[expected] {
			t.Errorf("scheduler session missing declared tool %q — regression: tool filtering not applied", expected)
		}
	}

	// The scheduler must NOT see tools it did not declare.
	undeclaredTools := []string{"assign_task", "query_teams", "query_job", "query_job_context", "surface_to_user"}
	for _, undeclared := range undeclaredTools {
		if schedulerToolNames[undeclared] {
			t.Errorf("scheduler session has undeclared tool %q — regression: consultWorker not filtering tools to worker's declared set", undeclared)
		}
	}

	if len(schedulerReq.Tools) != 2 {
		t.Errorf("scheduler session has %d tools, want 2; got: %v",
			len(schedulerReq.Tools), schedulerReq.Tools)
	}
}

// --- truncateMessages tests ---

func TestTruncateMessages_NoTruncationNeeded(t *testing.T) {
	msgs := []provider.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	got := truncateMessages(msgs, 10)
	if len(got) != 2 {
		t.Fatalf("want 2 messages, got %d", len(got))
	}
}

func TestTruncateMessages_ExactLimit(t *testing.T) {
	msgs := []provider.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	got := truncateMessages(msgs, 2)
	if len(got) != 2 {
		t.Fatalf("want 2 messages, got %d", len(got))
	}
}

func TestTruncateMessages_SafeBoundaryOnUserMessage(t *testing.T) {
	// Build a conversation where naive truncation with max=4 would start
	// on a tool result, splitting the tool-call/result pair.
	msgs := []provider.Message{
		{Role: "user", Content: "msg-1"},       // 0
		{Role: "assistant", Content: "resp-1"}, // 1
		{Role: "user", Content: "msg-2"},       // 2
		{Role: "assistant", Content: "", ToolCalls: []provider.ToolCall{{ID: "tc-1", Name: "do_thing"}}}, // 3
		{Role: "tool", Content: "result-1", ToolCallID: "tc-1"},                                          // 4 ← naive start
		{Role: "assistant", Content: "after tool"},                                                       // 5
		{Role: "user", Content: "msg-3"},                                                                 // 6
		{Role: "assistant", Content: "resp-3"},                                                           // 7
	}

	// Naive truncation with max=4 would give msgs[4:] = [tool, assistant, user, assistant]
	// which starts with an orphaned tool result.
	got := truncateMessages(msgs, 4)

	// Should skip the orphaned tool result, then find assistant("after tool")
	// which has no tool calls — that's a safe start boundary.
	if len(got) != 3 {
		t.Fatalf("want 3 messages, got %d", len(got))
	}
	assertEqual(t, "assistant", got[0].Role)
	assertEqual(t, "after tool", got[0].Content)
	assertEqual(t, "user", got[1].Role)
	assertEqual(t, "msg-3", got[1].Content)
	assertEqual(t, "assistant", got[2].Role)
	assertEqual(t, "resp-3", got[2].Content)
}

func TestTruncateMessages_SafeBoundaryOnAssistantNoToolCalls(t *testing.T) {
	// Tail starts with an assistant message that has no tool calls — that's safe.
	msgs := []provider.Message{
		{Role: "user", Content: "old-1"},
		{Role: "assistant", Content: "old-2"},
		{Role: "user", Content: "old-3"},
		{Role: "assistant", Content: "safe-start"}, // no tool calls
		{Role: "user", Content: "msg-4"},
		{Role: "assistant", Content: "resp-4"},
	}

	got := truncateMessages(msgs, 3)
	// Tail is [assistant("safe-start"), user("msg-4"), assistant("resp-4")]
	// assistant with no tool calls is a safe start.
	if len(got) != 3 {
		t.Fatalf("want 3 messages, got %d", len(got))
	}
	assertEqual(t, "assistant", got[0].Role)
	assertEqual(t, "safe-start", got[0].Content)
}

func TestTruncateMessages_SkipsAssistantWithToolCalls(t *testing.T) {
	// Tail starts with an assistant message that HAS tool calls — its tool
	// results are before the window, so it's not safe to start here.
	msgs := []provider.Message{
		{Role: "user", Content: "old-1"},
		{Role: "assistant", Content: "old-2"},
		{Role: "assistant", Content: "", ToolCalls: []provider.ToolCall{{ID: "tc-1", Name: "x"}}}, // unsafe start
		{Role: "tool", Content: "result", ToolCallID: "tc-1"},
		{Role: "user", Content: "msg-3"},
		{Role: "assistant", Content: "resp-3"},
	}

	got := truncateMessages(msgs, 4)
	// Tail is [assistant(tool calls), tool, user, assistant]
	// Must skip assistant-with-tool-calls and tool result, start at user.
	if len(got) != 2 {
		t.Fatalf("want 2 messages, got %d", len(got))
	}
	assertEqual(t, "user", got[0].Role)
	assertEqual(t, "msg-3", got[0].Content)
}

func TestTruncateMessages_MultipleToolCallsBeforeWindow(t *testing.T) {
	// Simulate a scenario where multiple tool results are orphaned at the
	// start of the tail window.
	msgs := []provider.Message{
		{Role: "user", Content: "old"},
		{Role: "assistant", Content: "", ToolCalls: []provider.ToolCall{
			{ID: "tc-1", Name: "tool_a"},
			{ID: "tc-2", Name: "tool_b"},
		}},
		{Role: "tool", Content: "result-a", ToolCallID: "tc-1"}, // orphaned
		{Role: "tool", Content: "result-b", ToolCallID: "tc-2"}, // orphaned
		{Role: "assistant", Content: "after tools"},
		{Role: "user", Content: "next-msg"},
		{Role: "assistant", Content: "next-resp"},
	}

	got := truncateMessages(msgs, 5)
	// Tail is [tool("result-a"), tool("result-b"), assistant("after tools"), user, assistant]
	// Must skip both tool results, start at assistant("after tools") which has no tool calls.
	if len(got) != 3 {
		t.Fatalf("want 3 messages, got %d", len(got))
	}
	assertEqual(t, "assistant", got[0].Role)
	assertEqual(t, "after tools", got[0].Content)
	assertEqual(t, "user", got[1].Role)
	assertEqual(t, "next-msg", got[1].Content)
}

func TestTruncateMessages_AllToolResults(t *testing.T) {
	// Edge case: the entire tail is tool results. The function should return
	// an empty slice (or the tail from startIdx=0 if no safe boundary found).
	// In practice this shouldn't happen, but the function should not panic.
	msgs := make([]provider.Message, 10)
	for i := range msgs {
		msgs[i] = provider.Message{Role: "tool", Content: "result", ToolCallID: "tc"}
	}

	got := truncateMessages(msgs, 5)
	// No safe boundary found — startIdx stays at 0, returns the full tail.
	// This is the best we can do; the conversation is already corrupted.
	if len(got) != 5 {
		t.Fatalf("want 5 messages (fallback), got %d", len(got))
	}
}

func TestTruncateMessages_PreservesToolCallResultPair(t *testing.T) {
	// Verify that a complete tool-call → tool-result pair within the window
	// is preserved intact.
	msgs := []provider.Message{
		{Role: "user", Content: "old-1"},
		{Role: "assistant", Content: "old-2"},
		{Role: "user", Content: "do something"},
		{Role: "assistant", Content: "", ToolCalls: []provider.ToolCall{{ID: "tc-1", Name: "run"}}},
		{Role: "tool", Content: "done", ToolCallID: "tc-1"},
		{Role: "assistant", Content: "completed"},
	}

	got := truncateMessages(msgs, 4)
	// Tail is [user("do something"), assistant(tool call), tool, assistant]
	// Starts at user — the entire tool-call/result pair is within the window.
	if len(got) != 4 {
		t.Fatalf("want 4 messages, got %d", len(got))
	}
	assertEqual(t, "user", got[0].Role)
	assertEqual(t, "do something", got[0].Content)
	assertEqual(t, "assistant", got[1].Role)
	if len(got[1].ToolCalls) != 1 {
		t.Fatal("expected assistant message to have tool calls")
	}
	assertEqual(t, "tool", got[2].Role)
	assertEqual(t, "assistant", got[3].Role)
}

func TestTruncateMessages_LargeConversation(t *testing.T) {
	// Simulate a realistic large conversation that exceeds maxMessages.
	// Pattern: user → assistant(tool call) → tool result → assistant → repeat
	var msgs []provider.Message
	for i := 0; i < 60; i++ {
		msgs = append(msgs,
			provider.Message{Role: "user", Content: fmt.Sprintf("user-%d", i)},
			provider.Message{Role: "assistant", Content: "", ToolCalls: []provider.ToolCall{
				{ID: fmt.Sprintf("tc-%d", i), Name: "some_tool"},
			}},
			provider.Message{Role: "tool", Content: fmt.Sprintf("result-%d", i), ToolCallID: fmt.Sprintf("tc-%d", i)},
			provider.Message{Role: "assistant", Content: fmt.Sprintf("resp-%d", i)},
		)
	}
	// 240 messages total

	got := truncateMessages(msgs, 200)

	// The first message in the truncated window must be safe.
	if got[0].Role == "tool" {
		t.Fatal("truncated window starts with orphaned tool result")
	}
	if got[0].Role == "assistant" && len(got[0].ToolCalls) > 0 {
		t.Fatal("truncated window starts with assistant that has tool calls (results may be missing)")
	}

	// Verify no tool result appears without its preceding tool call in the window.
	for i, msg := range got {
		if msg.Role == "tool" && i > 0 {
			prev := got[i-1]
			// The preceding message should be either another tool result (multi-tool)
			// or an assistant with tool calls.
			if prev.Role != "tool" && (prev.Role != "assistant" || len(prev.ToolCalls) == 0) {
				t.Fatalf("tool result at index %d has no preceding tool call", i)
			}
		}
	}
}

// --- Helpers ---

func newTestRegistry(mp *mockProvider) *provider.Registry {
	reg := provider.NewRegistry()
	reg.Register(mp.Name(), mp)
	return reg
}

// waitFor polls a condition until it returns true or the timeout expires.
func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}
