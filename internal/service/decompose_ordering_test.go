package service

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/jefflinse/toasters/internal/db"
)

// recordingStore wraps mockStore with capture for the methods exercised
// by the decompose-ordering tests (task creation + dependency edges).
type recordingStore struct {
	mockStore
	mu          sync.Mutex
	tasks       []*db.Task
	depEdges    [][2]string // [taskID, dependsOnID]
	feedEntries []*db.FeedEntry
}

func (r *recordingStore) UpdateTaskStatus(_ context.Context, id string, status db.TaskStatus, summary string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.tasks {
		if t.ID == id {
			t.Status = status
			t.Summary = summary
			return nil
		}
	}
	return fmt.Errorf("task not found: %s", id)
}

func (r *recordingStore) CreateFeedEntry(_ context.Context, e *db.FeedEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.feedEntries = append(r.feedEntries, e)
	return nil
}

func (r *recordingStore) CreateTask(_ context.Context, t *db.Task) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks = append(r.tasks, t)
	return nil
}

func (r *recordingStore) AddTaskDependency(_ context.Context, taskID, dependsOn string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.depEdges = append(r.depEdges, [2]string{taskID, dependsOn})
	return nil
}

func (r *recordingStore) GetTask(_ context.Context, id string) (*db.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.tasks {
		if t.ID == id {
			return t, nil
		}
	}
	return nil, fmt.Errorf("task not found: %s", id)
}

func (r *recordingStore) GetReadyTasks(_ context.Context, jobID string) ([]*db.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	completed := make(map[string]bool)
	for _, t := range r.tasks {
		if t.JobID == jobID && t.Status == db.TaskStatusCompleted {
			completed[t.ID] = true
		}
	}
	deps := make(map[string][]string)
	for _, e := range r.depEdges {
		deps[e[0]] = append(deps[e[0]], e[1])
	}
	var ready []*db.Task
	for _, t := range r.tasks {
		if t.JobID != jobID || t.Status != db.TaskStatusPending {
			continue
		}
		ok := true
		for _, dep := range deps[t.ID] {
			if !completed[dep] {
				ok = false
				break
			}
		}
		if ok {
			ready = append(ready, t)
		}
	}
	return ready, nil
}

// markCompleted is a test helper that flips a task's status without going
// through the real UpdateTaskStatus path.
func (r *recordingStore) markCompleted(taskID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.tasks {
		if t.ID == taskID {
			t.Status = db.TaskStatusCompleted
			return
		}
	}
}

func newDecomposeTestService(t *testing.T) (*LocalService, *recordingStore) {
	t.Helper()
	store := &recordingStore{}
	svc := newTestService(t)
	svc.cfg.Store = store
	// Stub GraphExecutor so dispatchFineDecompose's nil guard short-circuits
	// before it actually tries to spawn anything. The bootstrap-task
	// creation is what we're not covering here — only the ready-gating.
	svc.SetGraphExecutor(nil)
	return svc, store
}

func TestApplyCoarseResult_PersistsDependsOnEdges(t *testing.T) {
	svc, store := newDecomposeTestService(t)

	result := decompositionResult{
		Tasks: []decomposedTask{
			{Title: "backend", Description: "build the backend"},
			{Title: "frontend", Description: "build the frontend", DependsOn: []int{0}},
			{Title: "infra", Description: "package and deploy", DependsOn: []int{1}},
		},
		Reason: "linear pipeline",
	}
	svc.applyCoarseResult(context.Background(), "job-1", result)

	if len(store.tasks) != 3 {
		t.Fatalf("created %d tasks, want 3", len(store.tasks))
	}
	// The decomposer's per-task description must land in the dedicated
	// Description field (the task's contract — Summary gets overwritten by
	// status updates and never reached dispatch at all).
	if store.tasks[0].Description != "build the backend" {
		t.Errorf("task Description = %q, want %q", store.tasks[0].Description, "build the backend")
	}
	if len(store.depEdges) != 2 {
		t.Fatalf("persisted %d dep edges, want 2: %v", len(store.depEdges), store.depEdges)
	}
	frontID := store.tasks[1].ID
	infraID := store.tasks[2].ID
	backendID := store.tasks[0].ID
	wantEdges := map[[2]string]bool{
		{frontID, backendID}: true,
		{infraID, frontID}:   true,
	}
	for _, e := range store.depEdges {
		if !wantEdges[e] {
			t.Errorf("unexpected edge %v; want %v", e, wantEdges)
		}
	}
}

func TestApplyCoarseResult_IgnoresInvalidDependsOnIndex(t *testing.T) {
	svc, store := newDecomposeTestService(t)

	result := decompositionResult{
		Tasks: []decomposedTask{
			{Title: "a", Description: "a"},
			{Title: "b", Description: "b", DependsOn: []int{0, 7, -1}}, // 7 and -1 are bogus
		},
	}
	svc.applyCoarseResult(context.Background(), "job-1", result)

	if len(store.depEdges) != 1 {
		t.Errorf("persisted %d edges, want 1 (others should be skipped): %v", len(store.depEdges), store.depEdges)
	}
}

func TestDispatchFineDecomposeForTask_DefersWhenPredecessorsIncomplete(t *testing.T) {
	svc, store := newDecomposeTestService(t)

	result := decompositionResult{
		Tasks: []decomposedTask{
			{Title: "a", Description: "a"},
			{Title: "b", Description: "b", DependsOn: []int{0}},
		},
	}
	svc.applyCoarseResult(context.Background(), "job-1", result)

	// Sanity: with no completed predecessors, only task A is in the
	// ready set; task B should be deferred.
	ready, err := store.GetReadyTasks(context.Background(), "job-1")
	if err != nil {
		t.Fatalf("GetReadyTasks: %v", err)
	}
	if len(ready) != 1 || ready[0].Title != "a" {
		t.Fatalf("ready set = %v, want only [a]", ready)
	}

	// taskIsReady should report false for B.
	if svc.taskIsReady(context.Background(), store.tasks[1]) {
		t.Errorf("taskIsReady(b) = true, want false (a not yet completed)")
	}

	// After A completes, B should be ready.
	store.markCompleted(store.tasks[0].ID)
	if !svc.taskIsReady(context.Background(), store.tasks[1]) {
		t.Errorf("taskIsReady(b) = false after a completed, want true")
	}
}

// applyFineResult with a no_graph verdict must terminate the task (failed,
// terminal so the job can complete) and surface it in the feed — WITHOUT
// splitting it into subtasks. Splitting an out-of-domain task is the runaway
// that produced 77 tasks from 20; no_graph is the bail-out that stops it.
func TestApplyFineResult_NoGraphSurfacesInsteadOfSplitting(t *testing.T) {
	svc, store := newDecomposeTestService(t)
	ctx := context.Background()

	parent := &db.Task{
		ID:     "task-research",
		JobID:  "job-1",
		Title:  "Research company history",
		Status: db.TaskStatusInProgress,
	}
	store.tasks = append(store.tasks, parent)
	before := len(store.tasks)

	svc.applyFineResult(ctx, parent.ID, decompositionResult{
		NoGraph: true,
		Reason:  "no research/report graph for information-gathering tasks",
	})

	// No subtasks created.
	if len(store.tasks) != before {
		t.Errorf("created %d new tasks, want 0 (no_graph must not split)", len(store.tasks)-before)
	}
	// Task driven to a terminal state so the job can complete.
	got, _ := store.GetTask(ctx, parent.ID)
	if got.Status != db.TaskStatusFailed {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if got.Summary == "" {
		t.Error("summary should explain no graph fit")
	}
	// Surfaced in the feed.
	if len(store.feedEntries) != 1 {
		t.Fatalf("feed entries = %d, want 1", len(store.feedEntries))
	}
	if store.feedEntries[0].JobID != "job-1" {
		t.Errorf("feed entry job = %q, want job-1", store.feedEntries[0].JobID)
	}
}

// A rejection (too-broad) still splits — the no_graph path must not have
// regressed the legitimate split case.
func TestApplyFineResult_RejectionStillSplits(t *testing.T) {
	svc, store := newDecomposeTestService(t)
	ctx := context.Background()

	parent := &db.Task{ID: "task-big", JobID: "job-1", Title: "Build everything", Status: db.TaskStatusInProgress}
	store.tasks = append(store.tasks, parent)

	svc.applyFineResult(ctx, parent.ID, decompositionResult{
		Rejected: true,
		Reason:   "spans multiple graphs",
		Tasks: []decomposedTask{
			{Title: "part one", Description: "d1"},
			{Title: "part two", Description: "d2"},
		},
	})

	var created int
	for _, tk := range store.tasks {
		if tk.ParentID == parent.ID {
			created++
		}
	}
	if created != 2 {
		t.Errorf("created %d subtasks, want 2", created)
	}
}
