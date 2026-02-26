package operator

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/compose"
	"github.com/jefflinse/toasters/internal/db"
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

// newTestOperatorTools creates an operatorTools with a real store and composer.
// The store is seeded with the given agents.
func newTestOperatorTools(t *testing.T, agents []*db.Agent) *operatorTools {
	t.Helper()
	store := newOperatorTestStore(t)
	ctx := context.Background()

	for _, a := range agents {
		if err := store.UpsertAgent(ctx, a); err != nil {
			t.Fatalf("upserting agent: %v", err)
		}
	}

	composer := compose.New(store, "test-provider", "test-model")
	eventCh := make(chan Event, 64)
	systemTools := NewSystemTools(store, composer, eventCh, nil)

	return newOperatorTools(nil, composer, store, systemTools, t.TempDir())
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

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})

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

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})

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

func TestOperatorConsultAgent(t *testing.T) {
	// The operator calls consult_agent("planner", "..."), which spawns a
	// fresh session via runtime.SpawnAndWait. We need the planner agent
	// in the DB so the composer can look it up.
	//
	// Since both the operator and the planner use the same provider registry,
	// we use a single mock provider that handles multiple ChatStream calls:
	//   1. Operator's first response: tool call to consult_agent
	//   2. Planner agent's response: "Here is the plan: ..."
	//   3. Operator's second response (after seeing tool result): final text
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			// 1. Operator calls consult_agent.
			{events: []provider.StreamEvent{
				{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID:        "call-1",
					Name:      "consult_agent",
					Arguments: json.RawMessage(`{"agent_name": "planner", "message": "Plan a web app"}`),
				}},
				{Type: provider.EventDone},
			}},
			// 2. Planner agent responds.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Plan: 1) Setup 2) Build 3) Deploy"},
				{Type: provider.EventDone},
			}},
			// 3. Operator responds after seeing planner's result.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "The planner suggests: Setup, Build, Deploy"},
				{Type: provider.EventDone},
			}},
		},
	}

	store := newOperatorTestStore(t)
	ctx := context.Background()

	// Seed the planner agent in the DB (must be source=system for consult_agent).
	if err := store.UpsertAgent(ctx, &db.Agent{
		ID:           "planner",
		Name:         "Planner",
		Source:       "system",
		SystemPrompt: "You are a planning agent. Analyze requests and create plans.",
	}); err != nil {
		t.Fatalf("upserting planner agent: %v", err)
	}

	composer := compose.New(store, "test", "test-model")
	rt := runtime.New(store, newTestRegistry(mp))

	var textBuf strings.Builder
	var mu sync.Mutex

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
		Store:    store,
		Composer: composer,
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})

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
		return strings.Contains(textBuf.String(), "planner suggests")
	}, 5*time.Second)

	mu.Lock()
	got := textBuf.String()
	mu.Unlock()

	assertContains(t, got, "planner suggests")

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

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
		OnEvent: func(ev Event) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		},
	})

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

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
		OnEvent: func(ev Event) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		},
	})

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
			AgentID: "agent-2",
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

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
		Store:    store,
		OnEvent: func(ev Event) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		},
	})

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

	// Seed a team with a lead agent.
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

	// The mock provider needs to handle the spawned agent's ChatStream call.
	mp := &mockProvider{
		name: "test-provider",
		responses: []mockResponse{
			// Spawned team lead agent responds.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Working on second task"},
				{Type: provider.EventDone},
			}},
		},
	}

	reg := newTestRegistry(mp)
	composer := compose.New(store, "test-provider", "test-model")
	rt := runtime.New(store, reg)
	spawner := &mockSpawner{}

	// Create operator with SystemTools that have a spawner.
	eventCh := make(chan Event, eventChSize)
	systemTools := NewSystemTools(store, composer, eventCh, spawner)
	tools := newOperatorTools(rt, composer, store, systemTools, t.TempDir())
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
		systemPrompt: defaultSystemPrompt,
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

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
		Store:    store,
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})

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

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
		Store:    store,
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})

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

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
		Store:    store,
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})

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

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
		Store:    store,
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	_ = op.Send(ctx, Event{
		Type: EventBlockerReported,
		Payload: BlockerReportedPayload{
			TaskID:      "task-1",
			TeamID:      "backend",
			AgentID:     "worker-1",
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

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
		Store:    store,
		OnEvent: func(ev Event) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	_ = op.Send(ctx, Event{
		Type: EventProgressUpdate,
		Payload: ProgressUpdatePayload{
			TaskID:  "task-1",
			AgentID: "agent-1",
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

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
		Store:    store,
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})

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

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
		Store:    store,
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})

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

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
	})

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

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})

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

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})

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

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})

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

func TestConsultAgent_ComposedAgent(t *testing.T) {
	// Verify that consult_agent uses the composer to look up and compose the
	// agent from the DB, including the agent's system prompt and resolved
	// provider/model.
	store := newOperatorTestStore(t)
	ctx := context.Background()

	// Seed a planner agent with a specific system prompt (must be source=system).
	if err := store.UpsertAgent(ctx, &db.Agent{
		ID:           "planner",
		Name:         "Planner",
		Source:       "system",
		SystemPrompt: "You are a planning agent. Create detailed plans.",
		Provider:     "custom-provider",
		Model:        "custom-model",
	}); err != nil {
		t.Fatalf("upserting agent: %v", err)
	}

	mp := &mockProvider{
		name: "custom-provider",
		responses: []mockResponse{
			// 1. Operator calls consult_agent.
			{events: []provider.StreamEvent{
				{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID:        "call-1",
					Name:      "consult_agent",
					Arguments: json.RawMessage(`{"agent_name": "planner", "message": "Plan a migration"}`),
				}},
				{Type: provider.EventDone},
			}},
			// 2. Planner agent responds (uses custom-provider).
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Migration plan: step 1, step 2"},
				{Type: provider.EventDone},
			}},
			// 3. Operator responds after seeing tool result.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "The planner created a migration plan."},
				{Type: provider.EventDone},
			}},
		},
	}

	reg := provider.NewRegistry()
	reg.Register("custom-provider", mp)

	composer := compose.New(store, "custom-provider", "default-model")
	rt := runtime.New(store, reg)

	var textBuf strings.Builder
	var mu sync.Mutex

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "default-model",
		WorkDir:  t.TempDir(),
		Store:    store,
		Composer: composer,
		OnText: func(text string) {
			mu.Lock()
			textBuf.WriteString(text)
			mu.Unlock()
		},
	})

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	op.Start(ctx)

	_ = op.Send(ctx, Event{
		Type:    EventUserMessage,
		Payload: UserMessagePayload{Text: "Plan a migration"},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(textBuf.String(), "migration plan")
	}, 5*time.Second)

	mu.Lock()
	got := textBuf.String()
	mu.Unlock()

	assertContains(t, got, "migration plan")

	// Verify the planner's ChatStream call used the agent's custom model.
	reqs := mp.getRequests()
	if len(reqs) < 2 {
		t.Fatalf("want at least 2 ChatStream calls, got %d", len(reqs))
	}

	// The second call is the planner agent's session.
	plannerReq := reqs[1]
	assertEqual(t, "custom-model", plannerReq.Model)

	// Verify the planner's system prompt came from the DB.
	assertContains(t, plannerReq.System, "You are a planning agent")
}

func TestConsultAgent_UnknownAgent(t *testing.T) {
	// Verify that consult_agent returns an error when the agent is not found
	// in the DB.
	tools := newTestOperatorTools(t, nil) // no agents seeded

	_, err := tools.Execute(context.Background(), "consult_agent",
		json.RawMessage(`{"agent_name": "nonexistent", "message": "hello"}`))
	assertError(t, err)
	assertContains(t, err.Error(), "unknown agent")
	assertContains(t, err.Error(), "nonexistent")
}

func TestConsultAgentMissingParams(t *testing.T) {
	tools := newTestOperatorTools(t, nil)

	// Missing agent_name.
	_, err := tools.Execute(context.Background(), "consult_agent",
		json.RawMessage(`{"message": "hello"}`))
	assertError(t, err)
	assertContains(t, err.Error(), "agent_name is required")

	// Missing message.
	_, err = tools.Execute(context.Background(), "consult_agent",
		json.RawMessage(`{"agent_name": "planner"}`))
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
	composer := compose.New(store, "test-provider", "test-model")
	eventCh := make(chan Event, 64)
	systemTools := NewSystemTools(store, composer, eventCh, nil)
	tools := newOperatorTools(nil, composer, store, systemTools, t.TempDir())

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

func TestSurfaceToUserWithoutStore(t *testing.T) {
	// surface_to_user should still work without a store (graceful degradation).
	tools := newOperatorTools(nil, nil, nil, nil, t.TempDir())

	result, err := tools.Execute(context.Background(), "surface_to_user",
		json.RawMessage(`{"text": "No store available"}`))
	assertNoError(t, err)
	assertContains(t, result, "Surfaced to user")
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

	if len(defs) != 4 {
		t.Fatalf("want 4 tool definitions, got %d", len(defs))
	}

	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true
	}

	for _, expected := range []string{"consult_agent", "surface_to_user", "query_job", "query_teams"} {
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

	composer := compose.New(store, "test-provider", "test-model")
	eventCh := make(chan Event, 64)
	systemTools := NewSystemTools(store, composer, eventCh, nil)
	tools := newOperatorTools(nil, composer, store, systemTools, t.TempDir())

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

	composer := compose.New(store, "test-provider", "test-model")
	eventCh := make(chan Event, 64)
	systemTools := NewSystemTools(store, composer, eventCh, nil)
	tools := newOperatorTools(nil, composer, store, systemTools, t.TempDir())

	result, err := tools.Execute(ctx, "query_teams", json.RawMessage(`{}`))
	assertNoError(t, err)
	assertContains(t, result, "Alpha Team")
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
