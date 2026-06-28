package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jefflinse/toasters/internal/db"
)

// List returns jobs matching the given filter.
func (s *localJobService) List(ctx context.Context, filter *JobListFilter) ([]Job, error) {
	if s.svc.cfg.Store == nil {
		return nil, Unavailablef("store not configured")
	}
	dbFilter := db.JobFilter{}
	if filter != nil {
		if filter.Status != nil {
			status := db.JobStatus(*filter.Status)
			dbFilter.Status = &status
		}
		if filter.Type != nil {
			dbFilter.Type = filter.Type
		}
		dbFilter.Limit = filter.Limit
		dbFilter.Offset = filter.Offset
	}
	dbJobs, err := s.svc.cfg.Store.ListJobs(ctx, dbFilter)
	if err != nil {
		return nil, fmt.Errorf("listing jobs: %w", err)
	}
	jobs := make([]Job, 0, len(dbJobs))
	for _, j := range dbJobs {
		jobs = append(jobs, dbJobToService(j))
	}
	return jobs, nil
}

// ListAll returns all jobs regardless of status.
func (s *localJobService) ListAll(ctx context.Context) ([]Job, error) {
	if s.svc.cfg.Store == nil {
		return nil, Unavailablef("store not configured")
	}
	dbJobs, err := s.svc.cfg.Store.ListAllJobs(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing all jobs: %w", err)
	}
	jobs := make([]Job, 0, len(dbJobs))
	for _, j := range dbJobs {
		jobs = append(jobs, dbJobToService(j))
	}
	return jobs, nil
}

// Get returns a JobDetail for the given job ID.
func (s *localJobService) Get(ctx context.Context, id string) (JobDetail, error) {
	if s.svc.cfg.Store == nil {
		return JobDetail{}, Unavailablef("store not configured")
	}
	dbJob, err := s.svc.cfg.Store.GetJob(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return JobDetail{}, fmt.Errorf("getting job %s: %w", id, ErrNotFound)
		}
		return JobDetail{}, fmt.Errorf("getting job %s: %w", id, err)
	}

	dbTasks, err := s.svc.cfg.Store.ListTasksForJob(ctx, id)
	if err != nil {
		slog.Warn("failed to list tasks for job detail", "job", id, "error", err)
	}

	dbProgress, err := s.svc.cfg.Store.GetRecentProgress(ctx, id, 5)
	if err != nil {
		slog.Warn("failed to get progress for job detail", "job", id, "error", err)
	}

	tasks := make([]Task, 0, len(dbTasks))
	for _, t := range dbTasks {
		tasks = append(tasks, dbTaskToService(t))
	}

	reports := make([]ProgressReport, 0, len(dbProgress))
	for _, p := range dbProgress {
		reports = append(reports, dbProgressToService(p))
	}

	return JobDetail{
		Job:      dbJobToService(dbJob),
		Tasks:    tasks,
		Progress: reports,
	}, nil
}

// Cancel cancels the job with the given ID.
func (s *localJobService) Cancel(ctx context.Context, id string) error {
	if s.svc.cfg.Store == nil {
		return Unavailablef("store not configured")
	}
	dbJob, err := s.svc.cfg.Store.GetJob(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return fmt.Errorf("getting job %s: %w", id, ErrNotFound)
		}
		return fmt.Errorf("getting job %s: %w", id, err)
	}

	switch dbJob.Status {
	case db.JobStatusActive, db.JobStatusPending, db.JobStatusSettingUp:
		// cancellable
	default:
		return Conflictf("job %s cannot be cancelled (status: %s)", id, dbJob.Status)
	}

	// Flip the status first so anything reacting to in-flight work seeing
	// its context cancelled observes a cancelled job.
	if err := s.svc.cfg.Store.UpdateJobStatus(ctx, id, db.JobStatusCancelled); err != nil {
		return fmt.Errorf("cancelling job %s: %w", id, err)
	}

	// Actually stop the work: cancel in-flight graph executions (each
	// persists its task as cancelled on the way out) and any worker
	// sessions still running for this job.
	if c, ok := s.svc.currentGraphExecutor().(interface{ CancelJob(string) int }); ok {
		if n := c.CancelJob(id); n > 0 {
			slog.Info("cancelled in-flight graph executions", "job_id", id, "count", n)
		}
	}
	if s.svc.cfg.Runtime != nil {
		if n := s.svc.cfg.Runtime.CancelJobSessions(id); n > 0 {
			slog.Info("cancelled worker sessions", "job_id", id, "count", n)
		}
	}

	// Sweep every non-terminal task so nothing re-dispatches them later
	// (GetReadyTasks only returns pending tasks; RetryTask requires failed).
	// in_progress tasks are included: a live run writes the same cancelled
	// status when its context unwinds, and an orphaned row (no live run)
	// would otherwise wedge the job's serial gate forever.
	tasks, err := s.svc.cfg.Store.ListTasksForJob(ctx, id)
	if err != nil {
		return fmt.Errorf("listing tasks for cancelled job %s: %w", id, err)
	}
	for _, t := range tasks {
		switch t.Status {
		case db.TaskStatusPending, db.TaskStatusBlocked, db.TaskStatusInProgress:
			if err := s.svc.cfg.Store.UpdateTaskStatus(ctx, t.ID, db.TaskStatusCancelled, "Cancelled with job"); err != nil {
				slog.Warn("failed to cancel task", "task_id", t.ID, "error", err)
			}
		}
	}
	return nil
}

// RetryTask re-dispatches a failed, graph-bound task. It resets the task to
// in_progress (clearing stale result fields) and re-runs its bound graph on the
// executor, deterministically and without involving the operator LLM.
func (s *localJobService) RetryTask(ctx context.Context, taskID string) error {
	if s.svc.cfg.Store == nil {
		return Unavailablef("store not configured")
	}
	if s.svc.currentGraphExecutor() == nil {
		return fmt.Errorf("no graph executor configured")
	}
	task, err := s.svc.cfg.Store.GetTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return fmt.Errorf("getting task %s: %w", taskID, ErrNotFound)
		}
		return fmt.Errorf("getting task %s: %w", taskID, err)
	}
	if task.Status != db.TaskStatusFailed {
		return fmt.Errorf("task %s cannot be retried (status: %s)", taskID, task.Status)
	}
	if task.GraphID == "" {
		return fmt.Errorf("task %s has no bound graph to retry", taskID)
	}
	job, err := s.svc.cfg.Store.GetJob(ctx, task.JobID)
	if err != nil {
		return fmt.Errorf("getting job for task %s: %w", taskID, err)
	}
	if err := s.svc.cfg.Store.RetryTask(ctx, taskID, task.GraphID); err != nil {
		return fmt.Errorf("resetting task %s for retry: %w", taskID, err)
	}
	s.svc.redispatchTaskGraph(ctx, task, job, task.GraphID)
	return nil
}
