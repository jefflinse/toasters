package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/gofrs/uuid/v5"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/graphexec"
)

// Decomposition graph IDs — re-exposed from graphexec so this file can
// keep using its short local names. The service dispatches them
// automatically: coarse-decompose on new jobs, fine-decompose on tasks
// without a graph_id.
const (
	graphCoarseDecompose = graphexec.GraphCoarseDecompose
	graphFineDecompose   = graphexec.GraphFineDecompose
)

// maxDecomposeDepth bounds fine-decompose recursion. A task whose
// DecomposeDepth has reached this cap is surfaced to the user instead of
// being re-decomposed — prevents runaway splits when the decomposer
// can't find a graph match.
const maxDecomposeDepth = 3

// decomposeMetadata is the JSON shape stored on a bootstrap task's
// metadata column. It records which job or parent task the bootstrap is
// decomposing, so the completion handler can route mutations to the
// right target.
type decomposeMetadata struct {
	DecomposesJob      string `json:"decomposes_job,omitempty"`
	DecomposesParentID string `json:"decomposes_parent_id,omitempty"`
}

// decompositionResult is the parsed form of the decomposition-result
// schema shared by coarse-decompose and fine-decompose.
type decompositionResult struct {
	Tasks     []decomposedTask `json:"tasks,omitempty"`
	GraphID   string           `json:"graph_id,omitempty"`
	Toolchain string           `json:"toolchain,omitempty"`
	Rejected  bool             `json:"rejected,omitempty"`
	NoGraph   bool             `json:"no_graph,omitempty"`
	Reason    string           `json:"reason,omitempty"`
}

// decomposedTask is one entry produced by a decomposition graph.
type decomposedTask struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	DependsOn   []int  `json:"depends_on,omitempty"`
}

// dispatchCoarseDecompose creates a bootstrap task to run the
// coarse-decompose graph on a new job, then kicks it off. No-op when
// the graph executor is not wired (test environments).
func (s *LocalService) dispatchCoarseDecompose(jobID, jobTitle, jobDescription string) {
	if s.currentGraphExecutor() == nil || s.cfg.Store == nil {
		return
	}
	meta, _ := json.Marshal(decomposeMetadata{DecomposesJob: jobID})
	bootstrap := &db.Task{
		ID:       newTaskID(),
		JobID:    jobID,
		Title:    fmt.Sprintf("Decompose: %s", jobTitle),
		Status:   db.TaskStatusInProgress,
		GraphID:  graphCoarseDecompose,
		Metadata: json.RawMessage(meta),
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	if err := s.cfg.Store.CreateTask(ctx, bootstrap); err != nil {
		slog.Error("failed to create coarse-decompose bootstrap task",
			"job_id", jobID, "error", err)
		return
	}

	job, err := s.cfg.Store.GetJob(ctx, jobID)
	if err != nil {
		slog.Error("failed to fetch job for coarse-decompose dispatch",
			"job_id", jobID, "error", err)
		return
	}

	s.dispatchBootstrap(bootstrap, job, jobTitle, jobDescription, "")
}

// dispatchFineDecompose creates a bootstrap task to run fine-decompose
// against the parent task, then kicks it off. Called when a task is
// created without a graph_id.
func (s *LocalService) dispatchFineDecompose(parent *db.Task, job *db.Job) {
	if s.currentGraphExecutor() == nil || s.cfg.Store == nil {
		return
	}
	if parent.DecomposeDepth >= maxDecomposeDepth {
		slog.Warn("fine-decompose cap reached; leaving task pending for user",
			"task_id", parent.ID, "depth", parent.DecomposeDepth)
		return
	}
	meta, _ := json.Marshal(decomposeMetadata{DecomposesParentID: parent.ID})
	bootstrap := &db.Task{
		ID:             newTaskID(),
		JobID:          parent.JobID,
		Title:          fmt.Sprintf("Pick graph: %s", parent.Title),
		Status:         db.TaskStatusInProgress,
		GraphID:        graphFineDecompose,
		Metadata:       json.RawMessage(meta),
		DecomposeDepth: parent.DecomposeDepth,
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	if err := s.cfg.Store.CreateTask(ctx, bootstrap); err != nil {
		slog.Error("failed to create fine-decompose bootstrap task",
			"task_id", parent.ID, "error", err)
		return
	}

	s.dispatchBootstrap(bootstrap, job, parent.Title, parent.Description, parent.ID)
}

// dispatchBootstrap kicks off the graph executor for a decomposition
// bootstrap task. Runs in a goroutine — ExecuteTask itself is async
// inside dispatchGraphTask, but we're already inside a broadcaster
// callback that must not block.
//
// subjectTitle/subjectDescription identify the work being decomposed: the
// job's title and description for coarse-decompose, the parent task's for
// fine-decompose. subjectTaskID is the real task whose siblings should be
// exposed to the bootstrap graph (parent.ID for fine-decompose); pass empty
// for coarse-decompose, which operates on the job and has no siblings.
func (s *LocalService) dispatchBootstrap(bootstrap *db.Task, job *db.Job, subjectTitle, subjectDescription, subjectTaskID string) {
	siblings := ""
	if subjectTaskID != "" {
		if jobTasks, err := s.cfg.Store.ListTasksForJob(s.ctx, bootstrap.JobID); err == nil {
			siblings = graphexec.FormatSiblingTitles(graphexec.SiblingTitles(jobTasks, subjectTaskID))
		}
	}
	providerID, model := s.currentDefaults()
	exec := s.currentGraphExecutor()
	req := graphexec.TaskRequest{
		JobID:           bootstrap.JobID,
		JobTitle:        job.Title,
		JobDescription:  job.Description,
		TaskID:          bootstrap.ID,
		TaskTitle:       subjectTitle,
		TaskDescription: subjectDescription,
		GraphID:         bootstrap.GraphID,
		Siblings:        siblings,
		WorkspaceDir:    job.WorkspaceDir,
		ProviderName:    providerID,
		Model:           model,
	}
	go func() {
		if err := exec.ExecuteTask(s.ctx, req); err != nil {
			slog.Error("decomposition bootstrap dispatch failed",
				"bootstrap_task_id", bootstrap.ID,
				"graph_id", bootstrap.GraphID,
				"error", err)
		}
	}()
}

// consumeDecompositionOutput processes the output of a completed
// decomposition bootstrap task. Returns true when fully handled (caller
// should not forward to the operator). The method always returns true
// for the two decomposition graph ids because either the output is
// well-formed and we acted, or it was malformed and we logged — nothing
// downstream needs to see decomposition task completions.
func (s *LocalService) consumeDecompositionOutput(graphID, bootstrapTaskID string, output json.RawMessage) {
	if s.cfg.Store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()

	bootstrap, err := s.cfg.Store.GetTask(ctx, bootstrapTaskID)
	if err != nil {
		slog.Warn("decomposition bootstrap task missing on completion",
			"task_id", bootstrapTaskID, "error", err)
		return
	}

	var meta decomposeMetadata
	if len(bootstrap.Metadata) > 0 {
		if err := json.Unmarshal(bootstrap.Metadata, &meta); err != nil {
			slog.Warn("malformed decomposition bootstrap metadata",
				"task_id", bootstrapTaskID, "error", err)
			return
		}
	}

	var result decompositionResult
	if len(output) > 0 {
		if err := json.Unmarshal(output, &result); err != nil {
			slog.Warn("malformed decomposition-result output",
				"graph_id", graphID, "task_id", bootstrapTaskID, "error", err)
			return
		}
	}

	switch graphID {
	case graphCoarseDecompose:
		s.applyCoarseResult(ctx, meta.DecomposesJob, result)
	case graphFineDecompose:
		s.applyFineResult(ctx, meta.DecomposesParentID, result)
	}
}

// applyCoarseResult creates one pending Task per entry in result.Tasks.
// Each new task is created without a graph_id, which triggers
// fine-decompose automatically via BroadcastTaskCreated.
func (s *LocalService) applyCoarseResult(ctx context.Context, jobID string, result decompositionResult) {
	if jobID == "" {
		slog.Warn("coarse-decompose completed without a job reference")
		return
	}
	if len(result.Tasks) == 0 {
		slog.Warn("coarse-decompose produced no tasks", "job_id", jobID, "reason", result.Reason)
		return
	}
	// Create tasks in order; sort_order mirrors the array index. Defer
	// broadcasts until after dependencies are wired so fine-decompose
	// dispatch only fires for tasks with no unmet predecessors.
	ids := make([]string, len(result.Tasks))
	for i, t := range result.Tasks {
		task := &db.Task{
			ID:    newTaskID(),
			JobID: jobID,
			Title: t.Title,
			// Description is the task's immutable contract; Summary starts
			// as a display copy of it but is overwritten by status updates.
			Description: t.Description,
			Status:      db.TaskStatusPending,
			Summary:     t.Description,
			SortOrder:   i,
		}
		if err := s.cfg.Store.CreateTask(ctx, task); err != nil {
			slog.Error("failed to create task from coarse-decompose output",
				"job_id", jobID, "index", i, "error", err)
			continue
		}
		ids[i] = task.ID
	}
	for i, t := range result.Tasks {
		if ids[i] == "" {
			continue
		}
		for _, depIdx := range t.DependsOn {
			if depIdx < 0 || depIdx >= len(ids) || ids[depIdx] == "" {
				slog.Warn("ignoring invalid dependency index from coarse-decompose",
					"task_id", ids[i], "index", depIdx, "job_id", jobID)
				continue
			}
			if err := s.cfg.Store.AddTaskDependency(ctx, ids[i], ids[depIdx]); err != nil {
				slog.Error("failed to persist task dependency",
					"task_id", ids[i], "depends_on", ids[depIdx], "error", err)
			}
		}
	}
	for i, t := range result.Tasks {
		if ids[i] == "" {
			continue
		}
		s.BroadcastTaskCreated(ids[i], jobID, t.Title, "")
	}
	slog.Info("coarse-decompose applied",
		"job_id", jobID, "task_count", len(result.Tasks), "reason", result.Reason)
}

// applyFineResult assigns a graph to the parent task (on graph_id output)
// or replaces the parent with subtasks (on rejection). A parent with no
// output is left pending for the user to inspect.
func (s *LocalService) applyFineResult(ctx context.Context, parentID string, result decompositionResult) {
	if parentID == "" {
		slog.Warn("fine-decompose completed without a parent reference")
		return
	}
	parent, err := s.cfg.Store.GetTask(ctx, parentID)
	if err != nil {
		slog.Warn("fine-decompose parent task missing on completion",
			"parent_id", parentID, "error", err)
		return
	}

	switch {
	case result.GraphID != "":
		s.assignGraphToParent(ctx, parent, result.GraphID, result.Toolchain, result.Reason)
	case result.NoGraph:
		s.handleNoGraphFit(ctx, parent, result.Reason)
	case result.Rejected && len(result.Tasks) > 0:
		s.replaceParentWithSubtasks(ctx, parent, result)
	default:
		slog.Warn("fine-decompose produced neither graph_id nor subtasks",
			"parent_id", parentID, "reason", result.Reason)
	}
}

// handleNoGraphFit terminates a task that no installed graph can run. The
// decomposer signals this (no_graph) when splitting would not help because
// every subtask would be the same unsupported kind of work — the runaway
// breadth-explosion we'd otherwise get by recursively rejecting out-of-domain
// tasks until the depth cap. Mark the task failed (terminal, so the job can
// still complete) with an actionable reason and surface it in the feed,
// without routing through the operator's task-failed retry path — retrying
// fine-decompose would just reproduce the same no-graph result.
func (s *LocalService) handleNoGraphFit(ctx context.Context, parent *db.Task, reason string) {
	if reason == "" {
		reason = "no installed graph fits this kind of work"
	}
	summary := fmt.Sprintf("No installed graph fits this task: %s. Install or define a graph for this kind of work, or rescope the job.", reason)
	if err := s.cfg.Store.UpdateTaskStatus(ctx, parent.ID, db.TaskStatusFailed, summary); err != nil {
		slog.Error("failed to mark no-graph task failed", "task_id", parent.ID, "error", err)
		return
	}
	entry := &db.FeedEntry{
		EntryType: db.FeedEntrySystemEvent,
		Content:   fmt.Sprintf("⚠️ No graph fits task %q — %s", parent.Title, reason),
		JobID:     parent.JobID,
	}
	if err := s.cfg.Store.CreateFeedEntry(ctx, entry); err != nil {
		slog.Warn("failed to create no-graph feed entry", "task_id", parent.ID, "error", err)
	}
	// Deliberately NOT routed through BroadcastTaskFailed: the operator's
	// task-failed path nudges retry_task, which re-runs fine-decompose and
	// reproduces the same no-graph verdict — an infinite loop. The feed entry
	// is the surface; the failed status lets the job reach a terminal state.
	slog.Info("fine-decompose found no fitting graph; task surfaced to user",
		"task_id", parent.ID, "job_id", parent.JobID, "reason", reason)
}

// assignGraphToParent wires the selected graph to the parent task and
// kicks off normal execution via the executor. toolchain is the toolchain
// id chosen by fine-decompose to bind slot-bearing roles inside the graph;
// it may be empty when the graph has no slot-bearing roles.
func (s *LocalService) assignGraphToParent(ctx context.Context, parent *db.Task, graphID, toolchain, reason string) {
	job, err := s.cfg.Store.GetJob(ctx, parent.JobID)
	if err != nil {
		slog.Error("failed to fetch job for graph assignment",
			"task_id", parent.ID, "error", err)
		return
	}

	// If a sibling is already in progress, defer execution via
	// PreAssignTaskGraph — same serial-execution semantics the operator
	// uses for manually-assigned tasks.
	jobTasks, err := s.cfg.Store.ListTasksForJob(ctx, parent.JobID)
	if err != nil {
		slog.Error("failed to list sibling tasks", "task_id", parent.ID, "error", err)
		return
	}
	for _, t := range jobTasks {
		if t.ID != parent.ID && t.Status == db.TaskStatusInProgress && !isDecompositionGraph(t.GraphID) {
			if err := s.cfg.Store.PreAssignTaskGraph(ctx, parent.ID, graphID); err != nil {
				slog.Error("pre-assign failed", "task_id", parent.ID, "error", err)
				return
			}
			s.persistTaskToolchain(ctx, parent.ID, toolchain)
			slog.Info("fine-decompose pre-assigned graph; sibling in progress",
				"task_id", parent.ID, "graph_id", graphID, "sibling", t.ID, "reason", reason)
			s.BroadcastTaskAssigned(parent.ID, parent.JobID, graphID, parent.Title)
			return
		}
	}

	if err := s.cfg.Store.AssignTaskToGraph(ctx, parent.ID, graphID); err != nil {
		slog.Error("assign-to-graph failed", "task_id", parent.ID, "error", err)
		return
	}
	s.persistTaskToolchain(ctx, parent.ID, toolchain)

	providerID, model := s.currentDefaults()
	exec := s.currentGraphExecutor()
	req := graphexec.TaskRequest{
		JobID:           parent.JobID,
		JobTitle:        job.Title,
		JobDescription:  job.Description,
		TaskID:          parent.ID,
		TaskTitle:       parent.Title,
		TaskDescription: parent.Description,
		GraphID:         graphID,
		Toolchain:       toolchain,
		Siblings:        graphexec.FormatSiblingTitles(graphexec.SiblingTitles(jobTasks, parent.ID)),
		WorkspaceDir:    job.WorkspaceDir,
		ProviderName:    providerID,
		Model:           model,
	}
	go func() {
		if err := exec.ExecuteTask(s.ctx, req); err != nil {
			slog.Error("fine-decompose dispatch failed",
				"task_id", req.TaskID, "graph_id", req.GraphID, "error", err)
		}
	}()
	s.BroadcastTaskAssigned(parent.ID, parent.JobID, graphID, parent.Title)
	slog.Info("fine-decompose assigned graph",
		"task_id", parent.ID, "graph_id", graphID, "reason", reason)
}

// persistTaskToolchain writes fine-decompose's toolchain choice onto the
// task's metadata column so it survives past the initial dispatch. Without
// this, a task's `task.toolchain` artifact is only ever populated from the
// graphexec.TaskRequest that built it — any later dispatch that rebuilds
// the request from the task row (retry, serial-gate advance) loses it and
// slot resolution hard-errors for roles that bind a toolchain slot. No-op
// when toolchain is empty (graphs with no slot-bearing roles).
func (s *LocalService) persistTaskToolchain(ctx context.Context, taskID, toolchain string) {
	if toolchain == "" {
		return
	}
	meta, err := db.MarshalTaskMetadata(db.TaskMetadata{Toolchain: toolchain})
	if err != nil {
		slog.Error("failed to encode toolchain metadata", "task_id", taskID, "error", err)
		return
	}
	if err := s.cfg.Store.SetTaskMetadata(ctx, taskID, meta); err != nil {
		slog.Error("failed to persist toolchain metadata", "task_id", taskID, "error", err)
	}
}

// redispatchTaskGraph builds a TaskRequest for an already-graph-bound task and
// runs it on the graph executor in a detached, service-lifetime-scoped
// goroutine. Unlike assignGraphToParent it does not touch task status — the
// caller transitions the task into a runnable state first (job retry resets
// failed→in_progress via Store.RetryTask). Serial-execution gating is
// intentionally skipped: a retry is an explicit user override of a terminal
// task and should start immediately.
func (s *LocalService) redispatchTaskGraph(ctx context.Context, task *db.Task, job *db.Job, graphID string) {
	jobTasks, err := s.cfg.Store.ListTasksForJob(ctx, task.JobID)
	if err != nil {
		// Non-fatal: dispatch with no sibling context rather than abort.
		slog.Warn("retry: failed to list sibling tasks", "task_id", task.ID, "error", err)
	}
	providerID, model := s.currentDefaults()
	exec := s.currentGraphExecutor()
	req := graphexec.TaskRequest{
		JobID:           task.JobID,
		JobTitle:        job.Title,
		JobDescription:  job.Description,
		TaskID:          task.ID,
		TaskTitle:       task.Title,
		TaskDescription: task.Description,
		GraphID:         graphID,
		Toolchain:       db.ParseTaskMetadata(task.Metadata).Toolchain,
		Siblings:        graphexec.FormatSiblingTitles(graphexec.SiblingTitles(jobTasks, task.ID)),
		WorkspaceDir:    job.WorkspaceDir,
		ProviderName:    providerID,
		Model:           model,
	}
	go func() {
		if err := exec.ExecuteTask(s.ctx, req); err != nil {
			slog.Error("retry dispatch failed",
				"task_id", req.TaskID, "graph_id", req.GraphID, "error", err)
		}
	}()
	s.BroadcastTaskAssigned(task.ID, task.JobID, graphID, task.Title)
	slog.Info("task retry dispatched", "task_id", task.ID, "job_id", task.JobID, "graph_id", graphID)
}

// replaceParentWithSubtasks marks the parent task as completed (split)
// and creates N child tasks, each inheriting the parent's incremented
// decompose_depth so runaway loops are capped.
func (s *LocalService) replaceParentWithSubtasks(ctx context.Context, parent *db.Task, result decompositionResult) {
	summary := fmt.Sprintf("Split into %d subtasks: %s", len(result.Tasks), result.Reason)
	if err := s.cfg.Store.UpdateTaskStatus(ctx, parent.ID, db.TaskStatusCompleted, summary); err != nil {
		slog.Error("failed to mark parent as split", "task_id", parent.ID, "error", err)
		return
	}
	childDepth := parent.DecomposeDepth + 1
	ids := make([]string, len(result.Tasks))
	for i, t := range result.Tasks {
		task := &db.Task{
			ID:             newTaskID(),
			JobID:          parent.JobID,
			Title:          t.Title,
			Description:    t.Description,
			Status:         db.TaskStatusPending,
			Summary:        t.Description,
			ParentID:       parent.ID,
			SortOrder:      parent.SortOrder*100 + i,
			DecomposeDepth: childDepth,
		}
		if err := s.cfg.Store.CreateTask(ctx, task); err != nil {
			slog.Error("failed to create subtask from fine-decompose rejection",
				"parent_id", parent.ID, "index", i, "error", err)
			continue
		}
		ids[i] = task.ID
	}
	for i, t := range result.Tasks {
		if ids[i] == "" {
			continue
		}
		for _, depIdx := range t.DependsOn {
			if depIdx < 0 || depIdx >= len(ids) || ids[depIdx] == "" {
				slog.Warn("ignoring invalid dependency index from fine-decompose rejection",
					"task_id", ids[i], "index", depIdx, "parent_id", parent.ID)
				continue
			}
			if err := s.cfg.Store.AddTaskDependency(ctx, ids[i], ids[depIdx]); err != nil {
				slog.Error("failed to persist subtask dependency",
					"task_id", ids[i], "depends_on", ids[depIdx], "error", err)
			}
		}
	}
	for i, t := range result.Tasks {
		if ids[i] == "" {
			continue
		}
		s.BroadcastTaskCreated(ids[i], parent.JobID, t.Title, "")
	}
	slog.Info("fine-decompose rejected parent; replaced with subtasks",
		"parent_id", parent.ID, "children", len(result.Tasks), "depth", childDepth, "reason", result.Reason)
}

// isDecompositionGraph is a short local alias for the shared predicate.
func isDecompositionGraph(id string) bool {
	return graphexec.IsDecompositionGraph(id)
}

// newTaskID returns a fresh task UUID string. Mirrors the format used by
// operator.SystemTools.createTask for parity.
func newTaskID() string {
	id, err := uuid.NewV4()
	if err != nil {
		// Extremely unlikely; fall back to a time-derived id rather than
		// panicking.
		return fmt.Sprintf("task-%d", time.Now().UnixNano())
	}
	return id.String()
}
