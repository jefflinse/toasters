package graphexec

import (
	"strings"

	"github.com/jefflinse/toasters/internal/db"
)

// Decomposition graph ids — these match the system graph YAML files at
// defaults/system/graphs/. Centralized here so sibling computation in
// both service and operator can identify and exclude bootstrap tasks
// without each layer redefining the constants.
const (
	GraphCoarseDecompose = "coarse-decompose"
	GraphFineDecompose   = "fine-decompose"
)

// IsDecompositionGraph reports whether a graph id is one of the
// internal decomposition graphs.
func IsDecompositionGraph(id string) bool {
	return id == GraphCoarseDecompose || id == GraphFineDecompose
}

// SiblingTitles extracts titles of other real tasks in a job —
// excluding the given task ID and any decomposition bootstrap tasks.
// Pair with FormatSiblingTitles to build the value passed in
// TaskRequest.Siblings.
func SiblingTitles(tasks []*db.Task, excludeTaskID string) []string {
	out := make([]string, 0, len(tasks))
	for _, t := range tasks {
		if t == nil || t.ID == excludeTaskID {
			continue
		}
		if IsDecompositionGraph(t.GraphID) {
			continue
		}
		out = append(out, t.Title)
	}
	return out
}

// noSiblingsPlaceholder is what `task.siblings` resolves to when there are
// no other tasks in the job. Templates render it verbatim, so phrasing
// matters: it tells the worker that this is the only task — i.e. there
// are no sibling tasks they could mistakenly drift into.
const noSiblingsPlaceholder = "(none — this is the only task in this job)"

// FormatSiblingTitles renders other task titles in a job as a markdown
// bullet list, suitable for setting as the `task.siblings` artifact via
// TaskRequest.Siblings. Callers pass titles already filtered (the current
// task and any decomposition bootstrap tasks excluded). An empty input
// produces the empty string; the executor substitutes the no-siblings
// placeholder at seed time.
func FormatSiblingTitles(titles []string) string {
	if len(titles) == 0 {
		return ""
	}
	lines := make([]string, 0, len(titles))
	for _, t := range titles {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		lines = append(lines, "- "+t)
	}
	return strings.Join(lines, "\n")
}

// siblingsArtifact returns the value to store under `task.siblings`,
// substituting the no-siblings placeholder when the caller-provided
// string is empty so templates always render meaningful text.
func siblingsArtifact(formatted string) string {
	if strings.TrimSpace(formatted) == "" {
		return noSiblingsPlaceholder
	}
	return formatted
}
