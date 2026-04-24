package operator

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jefflinse/toasters/internal/db"
)

// formatJobContext formats job and task information as readable context.
// Consumed by operator tools that surface a job snapshot to the LLM.
func formatJobContext(ctx context.Context, store db.Store, jobID string) (string, error) {
	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		return "", fmt.Errorf("getting job: %w", err)
	}

	tasks, err := store.ListTasksForJob(ctx, jobID)
	if err != nil {
		return "", fmt.Errorf("listing tasks: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Job: %s\n", job.Title)
	fmt.Fprintf(&b, "Status: %s\n", job.Status)
	if job.Description != "" {
		fmt.Fprintf(&b, "Description: %s\n", job.Description)
	}
	if job.WorkspaceDir != "" {
		fmt.Fprintf(&b, "Workspace: %s\n", contractHome(job.WorkspaceDir))
	}

	if len(tasks) == 0 {
		b.WriteString("\nNo tasks.")
	} else {
		fmt.Fprintf(&b, "\nTasks (%d):\n", len(tasks))
		for _, task := range tasks {
			fmt.Fprintf(&b, "  - [%s] %s", task.Status, task.Title)
			if task.GraphID != "" {
				fmt.Fprintf(&b, " (graph: %s)", task.GraphID)
			}
			if task.Summary != "" {
				fmt.Fprintf(&b, " — %s", task.Summary)
			}
			b.WriteString("\n")
		}
	}

	return b.String(), nil
}

// contractHome replaces the user's home directory prefix with "~/" for
// shorter, more readable paths in tool output. If the home directory
// cannot be determined or the path is not under it, the path is returned
// unchanged.
func contractHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if strings.HasPrefix(path, home+"/") {
		return "~/" + path[len(home)+1:]
	}
	if path == home {
		return "~"
	}
	return path
}
