// Progress polling: SQLite polling command for real-time progress display.
package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/db"
)

// progressPollCmd queries SQLite for current job/task/session state.
// Returns a progressPollMsg with the latest data.
func progressPollCmd(store db.Store) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		defer cancel()

		// Query active jobs.
		activeStatus := db.JobStatusActive
		jobs, err := store.ListJobs(ctx, db.JobFilter{Status: &activeStatus})
		if err != nil {
			jobs = nil // graceful degradation
		}

		tasks := make(map[string][]*db.Task)
		progress := make(map[string][]*db.ProgressReport)

		for _, j := range jobs {
			if ctx.Err() != nil {
				break
			}
			jobTasks, err := store.ListTasksForJob(ctx, j.ID)
			if err == nil {
				tasks[j.ID] = jobTasks
			}
			jobProgress, err := store.GetRecentProgress(ctx, j.ID, 5)
			if err == nil {
				progress[j.ID] = jobProgress
			}
		}

		sessions, err := store.GetActiveSessions(ctx)
		if err != nil {
			sessions = nil
		}

		return progressPollMsg{
			Jobs:     jobs,
			Tasks:    tasks,
			Progress: progress,
			Sessions: sessions,
		}
	}
}

// scheduleProgressPoll returns a command that fires progressPollTickMsg after 500ms.
func scheduleProgressPoll() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
		return progressPollTickMsg{}
	})
}
