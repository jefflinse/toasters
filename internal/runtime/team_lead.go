package runtime

import (
	"context"
	"log/slog"
	"time"
)

// ComposedWorker holds the fully resolved worker definition needed to spawn a
// team lead session.
type ComposedWorker struct {
	WorkerID        string
	Name            string
	SystemPrompt    string
	Tools           []string
	DisallowedTools []string
	Provider        string
	Model           string
	TeamID          string
	MaxTurns        *int
}

// TeamLeadSpawner is the interface for spawning team lead sessions.
// It is defined here (in runtime) so that *Runtime can implement it without
// creating an import cycle: operator → runtime is fine; runtime → operator
// would be a cycle.
type TeamLeadSpawner interface {
	SpawnTeamLead(ctx context.Context, composed *ComposedWorker, taskID, jobID, workDir, taskDescription string, extraTools ToolExecutor) error
}

// TeamLeadCompletionTracker is an optional interface that a team_lead's
// extraTools may satisfy. SpawnTeamLead's safety-net watcher uses it to
// detect team_lead sessions that ended without taking any terminal action
// (complete_task or report_blocker). When no terminal action was taken —
// for example because the model wrote a final text message instead of
// invoking a tool — the watcher force-fails the task so the operator can
// decide next steps.
//
// *operator.TeamLeadTools satisfies this interface; the runtime side checks
// for it via a type assertion to keep the dependency direction one-way.
type TeamLeadCompletionTracker interface {
	// TerminalActionTaken reports whether any terminal tool (complete_task or
	// report_blocker) was invoked at least once.
	TerminalActionTaken() bool
	// ForceFail marks the task as failed with the given reason and emits a
	// TaskFailed event so the operator can decide how to proceed.
	ForceFail(ctx context.Context, reason string) error
}

// teamLeadCompletionFallbackTimeout bounds the synthetic completion call so a
// stuck operator event channel can't permanently leak the watcher goroutine.
const teamLeadCompletionFallbackTimeout = 5 * time.Second

// watchTeamLeadForCompletion blocks until sess terminates and, if the team
// lead never invoked a terminal tool (complete_task or report_blocker),
// force-fails the task so the operator can decide next steps. A session ending
// without any terminal action typically means the model wrote pushback text or
// hit a turn limit rather than completing the work.
func watchTeamLeadForCompletion(sess *Session, taskID string, tracker TeamLeadCompletionTracker) {
	<-sess.Done()
	if tracker.TerminalActionTaken() {
		return
	}
	slog.Warn("team_lead session ended without terminal action; force-failing task",
		"task_id", taskID,
		"session_id", sess.ID(),
	)
	ctx, cancel := context.WithTimeout(context.Background(), teamLeadCompletionFallbackTimeout)
	defer cancel()
	if err := tracker.ForceFail(ctx, "Team lead session ended without completing or blocking the task"); err != nil {
		slog.Error("team_lead force-fail failed",
			"task_id", taskID,
			"session_id", sess.ID(),
			"error", err,
		)
	}
}

// SpawnTeamLead implements TeamLeadSpawner. It spawns a team lead worker session
// from a fully composed worker definition. The session runs fire-and-forget at
// depth 0 (team leads may spawn workers; workers may not spawn further).
// taskDescription is sent as the initial user message to kick off the conversation.
// extraTools, if non-nil, are layered on top of CoreTools with dispatch priority.
func (r *Runtime) SpawnTeamLead(ctx context.Context, composed *ComposedWorker, taskID, jobID, workDir, taskDescription string, extraTools ToolExecutor) error {
	// Resolve tool definitions from the composed tool name list. Team leads
	// receive the full default CoreTools set filtered to the composed tool names.
	// The actual ToolDef schemas are provided by CoreTools.Definitions() at
	// session start; here we pass nil Tools so the session builds its own
	// CoreTools stack (which will include spawn_worker at depth 0).
	opts := SpawnOpts{
		WorkerID:        composed.WorkerID,
		ProviderName:    composed.Provider,
		Model:           composed.Model,
		SystemPrompt:    composed.SystemPrompt,
		InitialMessage:  taskDescription,
		DisallowedTools: composed.DisallowedTools,
		ExtraTools:      extraTools,
		JobID:           jobID,
		TaskID:          taskID,
		TeamName:        composed.TeamID,
		WorkDir:         workDir,
		Depth:           0,
		MaxDepth:        defaultMaxDepth,
	}

	// Apply tool filter from composition if specified.
	// Use nil check (not len > 0) so that an explicitly empty slice still
	// triggers the filter path, bypassing the denylist only when Tools is
	// truly unset (nil).
	if composed.Tools != nil {
		// Build a temporary CoreTools to resolve tool names to ToolDef values.
		tmp := NewCoreTools(
			workDir,
			WithShell(true),
			WithSpawner(r, 0, defaultMaxDepth),
			WithStore(r.store),
		)
		byName := tmp.DefinitionsByName()
		if extraTools != nil {
			for _, td := range extraTools.Definitions() {
				byName[td.Name] = td
			}
		}
		var toolDefs []ToolDef
		for _, name := range composed.Tools {
			if td, ok := byName[name]; ok {
				toolDefs = append(toolDefs, td)
			}
		}
		if len(toolDefs) > 0 {
			opts.Tools = toolDefs
		}
	}

	if composed.MaxTurns != nil {
		opts.MaxTurns = *composed.MaxTurns
	}

	sess, err := r.SpawnWorker(ctx, opts)
	if err != nil {
		return err
	}

	// Safety net: if extraTools knows how to track and force completion
	// (e.g. *operator.TeamLeadTools), spawn a watcher that auto-completes
	// the task if the session ends without complete_task ever being called.
	// Without this, a team_lead model that writes a final text message
	// instead of invoking the tool would strand the task at "in_progress"
	// forever and the operator would never advance the job.
	if tracker, ok := extraTools.(TeamLeadCompletionTracker); ok {
		go watchTeamLeadForCompletion(sess, taskID, tracker)
	}

	return nil
}
