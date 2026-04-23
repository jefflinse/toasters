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
