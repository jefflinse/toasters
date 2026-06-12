package graphexec

import (
	"context"
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/provider"
)

// mapGraphSource is a test GraphSource backed by an in-memory map.
type mapGraphSource map[string]*Definition

func (m mapGraphSource) GraphByID(id string) *Definition {
	if d, ok := m[id]; ok {
		return d
	}
	return nil
}

func (m mapGraphSource) Graphs() []*Definition {
	defs := make([]*Definition, 0, len(m))
	for _, d := range m {
		defs = append(defs, d)
	}
	return defs
}

func openTestStoreForDispatch(t *testing.T) db.Store {
	t.Helper()
	path := t.TempDir() + "/test.db"
	store, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// buildDispatchExecutor assembles an Executor for dispatch tests, backed by
// a mock provider registry and an in-memory GraphSource.
func buildDispatchExecutor(t *testing.T, responses [][]provider.StreamEvent, graphs mapGraphSource) (*Executor, *mockProvider, db.Store) {
	t.Helper()

	mock := &mockProvider{responses: responses}
	reg := provider.NewRegistry()
	reg.Register("mock", mock)

	store := openTestStoreForDispatch(t)
	return NewExecutor(ExecutorConfig{
		Registry:     reg,
		Store:        store,
		Graphs:       graphs,
		PromptEngine: testEngine(t),
		DefaultModel: "test-model",
	}), mock, store
}

func TestExecuteTask_DispatchesViaGraphID(t *testing.T) {
	def := bugFixDef() // from compiler_test.go

	executor, mock, store := buildDispatchExecutor(t, [][]provider.StreamEvent{
		summaryResp("finding"),
		summaryResp("plan"),
		summaryResp("impl"),
		testResultResp(true, "ok"),
		reviewResp(true, "lgtm"),
	}, mapGraphSource{"bug-fix": def})

	workspace := t.TempDir()
	_ = store.CreateJob(context.Background(), &db.Job{
		ID:           "j1",
		Title:        "jt",
		Description:  "jd",
		Status:       db.JobStatusPending,
		WorkspaceDir: workspace,
	})
	_ = store.CreateTask(context.Background(), &db.Task{
		ID:     "t1",
		JobID:  "j1",
		Title:  "fix a bug",
		Status: db.TaskStatusPending,
	})

	err := executor.ExecuteTask(context.Background(), TaskRequest{
		JobID:        "j1",
		JobTitle:     "jt",
		TaskID:       "t1",
		TaskTitle:    "fix a bug",
		GraphID:      "bug-fix",
		WorkspaceDir: workspace,
		ProviderName: "mock",
		Model:        "test-model",
	})
	if err != nil {
		t.Fatalf("ExecuteTask: %v", err)
	}
	if mock.calls != 5 {
		t.Errorf("provider called %d times, want 5 (full bug-fix graph)", mock.calls)
	}
}

func TestExecuteTask_GraphIDNotFound(t *testing.T) {
	executor, _, _ := buildDispatchExecutor(t,
		[][]provider.StreamEvent{},
		mapGraphSource{},
	)
	err := executor.ExecuteTask(context.Background(), TaskRequest{
		JobID: "j1", TaskID: "t1", GraphID: "missing",
		ProviderName: "mock", Model: "test-model",
		WorkspaceDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error for missing graph id")
	}
	if !strings.Contains(err.Error(), `"missing"`) {
		t.Errorf("err = %v, want to name the missing graph id", err)
	}
}

func TestExecuteTask_GraphIDWithoutSourceFails(t *testing.T) {
	mock := &mockProvider{}
	reg := provider.NewRegistry()
	reg.Register("mock", mock)
	executor := NewExecutor(ExecutorConfig{
		Registry:     reg,
		Store:        openTestStoreForDispatch(t),
		DefaultModel: "test-model",
		// Graphs deliberately nil.
	})

	err := executor.ExecuteTask(context.Background(), TaskRequest{
		JobID: "j1", TaskID: "t1", GraphID: "bug-fix",
		ProviderName: "mock", Model: "test-model",
		WorkspaceDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "no GraphSource") {
		t.Errorf("err = %v, want to mention missing GraphSource", err)
	}
}

func TestExecuteTask_RequiresGraphID(t *testing.T) {
	executor, _, _ := buildDispatchExecutor(t, nil, mapGraphSource{})

	err := executor.ExecuteTask(context.Background(), TaskRequest{
		JobID:        "j1",
		TaskID:       "t1",
		TaskTitle:    "proto",
		WorkspaceDir: t.TempDir(),
		ProviderName: "mock",
		Model:        "test-model",
	})
	if err == nil || !strings.Contains(err.Error(), "graph_id is required") {
		t.Errorf("err = %v, want graph_id-required error", err)
	}
}

// The task description must reach graph nodes as the task.description
// artifact — pre-fix it was seeded from the TITLE, so workers saw none of
// the detail coarse-decompose produced and asked the user for it instead.
func TestPrepareTask_SeedsDescriptionArtifacts(t *testing.T) {
	def := bugFixDef()
	executor, _, _ := buildDispatchExecutor(t, nil, mapGraphSource{"bug-fix": def})

	req := TaskRequest{
		JobID:           "j1",
		JobTitle:        "jt",
		TaskID:          "t1",
		TaskTitle:       "Create Docker multi-stage build and docker-compose",
		TaskDescription: "Multi-stage Dockerfile for module github.com/x/todo, binary name todod, exposing port 8080.",
		GraphID:         "bug-fix",
		WorkspaceDir:    t.TempDir(),
		ProviderName:    "mock",
		Model:           "test-model",
	}
	_, state, err := executor.prepareTask(req)
	if err != nil {
		t.Fatalf("prepareTask: %v", err)
	}
	if got := state.GetArtifactString("task.description"); got != req.TaskDescription {
		t.Errorf("task.description = %q, want the description (not the title)", got)
	}
	if got := state.GetArtifactString("task.title"); got != req.TaskTitle {
		t.Errorf("task.title = %q, want %q", got, req.TaskTitle)
	}
	// The node's initial message is built from these artifacts — the
	// description must reach the worker's prompt.
	if msg := buildInitialMessage(state); !strings.Contains(msg, "binary name todod") {
		t.Errorf("initial message missing task description: %q", msg)
	}

	// Without a description, fall back to the title so prompts never go blank.
	req.TaskDescription = ""
	_, state, err = executor.prepareTask(req)
	if err != nil {
		t.Fatalf("prepareTask (no description): %v", err)
	}
	if got := state.GetArtifactString("task.description"); got != req.TaskTitle {
		t.Errorf("task.description fallback = %q, want title", got)
	}
}
