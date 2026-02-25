// Progress polling: SQLite polling command for real-time progress display.
package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/runtime"
)

// progressPollCmd queries SQLite for current job/task/session state and
// collects live session snapshots from the in-process runtime (which carry
// accurate token counts, unlike the DB records that are only written on
// session completion).
// rt may be nil — in that case RuntimeSessions will be empty.
func progressPollCmd(store db.Store, rt *runtime.Runtime) tea.Cmd {
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

		// Get live session snapshots from the in-process runtime (has real token counts).
		var runtimeSessions []runtime.SessionSnapshot
		if rt != nil {
			runtimeSessions = rt.ActiveSessions()
		}

		return progressPollMsg{
			Jobs:            jobs,
			Tasks:           tasks,
			Progress:        progress,
			Sessions:        sessions,
			RuntimeSessions: runtimeSessions,
		}
	}
}

// scheduleProgressPoll returns a command that fires progressPollTickMsg after 500ms.
func scheduleProgressPoll() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
		return progressPollTickMsg{}
	})
}
