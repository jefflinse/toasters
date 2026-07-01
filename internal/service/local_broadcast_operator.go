package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/operator"
)

// BroadcastOperatorText broadcasts an operator.text event. Called from the
// operator's OnText callback. turnID is the user turn the text belongs to —
// threaded from the operator rather than read from currentTurnID so a
// system-initiated turn streaming while a user message is still queued can't
// be mislabeled with the pending user turn's ID.
func (s *LocalService) BroadcastOperatorText(turnID, text, reasoning string) {
	s.turnMu.Lock()
	s.pendingResponse.WriteString(text)
	s.pendingReasoning.WriteString(reasoning)
	s.turnMu.Unlock()

	s.broadcast(Event{
		Type:   EventTypeOperatorText,
		TurnID: turnID,
		Payload: OperatorTextPayload{
			Text:      text,
			Reasoning: reasoning,
		},
	})
}

// BroadcastOperatorToolCall broadcasts an operator.tool_call event. Wired as
// the operator's OnToolCall callback from both construction paths so the TUI
// can show what the operator is doing between text segments. The result is
// truncated to keep the indicator compact.
func (s *LocalService) BroadcastOperatorToolCall(name string, args json.RawMessage, result string, isError bool) {
	const maxResult = 500
	if len(result) > maxResult {
		result = result[:maxResult] + "…"
	}
	s.broadcast(Event{
		Type: EventTypeOperatorToolCall,
		Payload: OperatorToolCallPayload{
			Name:    name,
			Args:    args,
			Result:  result,
			IsError: isError,
		},
	})
}

// BroadcastOperatorEvent broadcasts a service event derived from an operator
// event. Called from the operator's OnEvent callback.
func (s *LocalService) BroadcastOperatorEvent(ev operator.Event) {
	switch ev.Type {
	case operator.EventTaskStarted:
		payload, ok := ev.Payload.(operator.TaskStartedPayload)
		if !ok {
			return
		}
		s.broadcast(Event{
			Type: EventTypeTaskStarted,
			Payload: TaskStartedPayload{
				TaskID:  payload.TaskID,
				JobID:   payload.JobID,
				GraphID: payload.GraphID,
				Title:   payload.Title,
			},
		})

	case operator.EventTaskCompleted:
		payload, ok := ev.Payload.(operator.TaskCompletedPayload)
		if !ok {
			return
		}
		s.broadcast(Event{
			Type: EventTypeTaskCompleted,
			Payload: TaskCompletedPayload{
				TaskID:          payload.TaskID,
				JobID:           payload.JobID,
				GraphID:         payload.GraphID,
				Summary:         payload.Summary,
				Recommendations: payload.Recommendations,
				HasNextTask:     payload.HasNextTask,
			},
		})

	case operator.EventTaskFailed:
		payload, ok := ev.Payload.(operator.TaskFailedPayload)
		if !ok {
			return
		}
		s.broadcast(Event{
			Type: EventTypeTaskFailed,
			Payload: TaskFailedPayload{
				TaskID:  payload.TaskID,
				JobID:   payload.JobID,
				GraphID: payload.GraphID,
				Error:   payload.Error,
			},
		})

	case operator.EventJobComplete:
		payload, ok := ev.Payload.(operator.JobCompletePayload)
		if !ok {
			return
		}
		s.broadcast(Event{
			Type:    EventTypeJobCompleted,
			Payload: s.buildJobCompletedPayload(payload),
		})
	}
}

// buildJobCompletedPayload assembles the rich completion payload by pulling
// together the job row, its tasks, all worker sessions for the job, and a
// listing of files in the workspace whose mtime falls inside the job's
// lifetime. Errors at each stage degrade the payload but never block
// emission — a thin payload still beats no payload for the UI.
func (s *LocalService) buildJobCompletedPayload(payload operator.JobCompletePayload) JobCompletedPayload {
	out := JobCompletedPayload{
		JobID:   payload.JobID,
		Title:   payload.Title,
		Summary: payload.Summary,
		Status:  JobStatusCompleted,
		EndedAt: time.Now(),
	}

	if s.cfg.Store == nil {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if job, err := s.cfg.Store.GetJob(ctx, payload.JobID); err == nil && job != nil {
		out.Workspace = job.WorkspaceDir
		out.StartedAt = job.CreatedAt
		// out.Title comes from the operator payload, which is authoritative
		// for the operator-perceived label; only fall back to the DB when
		// the payload's title is empty.
		if out.Title == "" {
			out.Title = job.Title
		}
	} else if err != nil {
		slog.Warn("job-completed payload: GetJob failed", "job_id", payload.JobID, "error", err)
	}

	if tasks, err := s.cfg.Store.ListTasksForJob(ctx, payload.JobID); err == nil {
		out.TasksTotal = len(tasks)
		for _, t := range tasks {
			switch t.Status {
			case db.TaskStatusCompleted:
				out.TasksCompleted++
			case db.TaskStatusFailed:
				out.TasksFailed++
			}
		}
		// Promote the job's effective status: if any task failed the run
		// wasn't a clean win, even though EventJobComplete fires for any
		// terminal state.
		if out.TasksFailed > 0 {
			out.Status = JobStatusFailed
		}
	} else {
		slog.Warn("job-completed payload: ListTasksForJob failed", "job_id", payload.JobID, "error", err)
	}

	if sessions, err := s.cfg.Store.ListSessionsForJob(ctx, payload.JobID); err == nil {
		for _, sess := range sessions {
			out.TokensIn += sess.TokensIn
			out.TokensOut += sess.TokensOut
			if sess.CostUSD != nil {
				out.CostUSD += *sess.CostUSD
			}
		}
		// Diagnostic: many local-inference servers (LM Studio in
		// particular older builds) ship without `stream_options.include_usage`
		// support, so worker_sessions can land at tokens=0 even when the
		// job clearly produced text. Log the count + aggregate so the
		// user can correlate against `sqlite3 toasters.db "SELECT
		// id,tokens_in,tokens_out FROM worker_sessions WHERE job_id=?"`.
		slog.Debug("job-completed payload: aggregated session tokens",
			"job_id", payload.JobID,
			"sessions", len(sessions),
			"tokens_in", out.TokensIn,
			"tokens_out", out.TokensOut)
	} else {
		slog.Warn("job-completed payload: ListSessionsForJob failed", "job_id", payload.JobID, "error", err)
	}

	if out.Workspace != "" && !out.StartedAt.IsZero() {
		files, extra := walkFilesTouched(out.Workspace, out.StartedAt, out.EndedAt)
		out.FilesTouched = files
		out.FilesTouchedExtra = extra
	}

	return out
}

// walkFilesTouched returns the files inside dir whose mtime falls within
// [startedAt, endedAt+grace]. A small forward grace window covers the
// race between the last file write and the completion event firing. The
// listing is bounded so an over-eager scan in a huge workspace can't
// stall the broadcast loop or blow the SSE event size.
func walkFilesTouched(dir string, startedAt, endedAt time.Time) ([]FileTouch, int) {
	const (
		maxFiles  = 200
		graceWin  = 2 * time.Second
		hardLimit = 5000 // walk-time guard: stop entirely after this many entries
	)
	if dir == "" {
		return nil, 0
	}
	endWindow := endedAt.Add(graceWin)
	var (
		out   []FileTouch
		extra int
		seen  int
	)
	walkErr := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Ignore individual file errors (permission denied, broken
			// symlinks); they shouldn't poison the whole listing.
			return nil
		}
		if d.IsDir() {
			// Skip the workspace-internal cache dirs that almost always
			// pollute file-touch lists with uninteresting churn.
			name := d.Name()
			if path != dir && (name == ".git" || name == "node_modules" || name == ".toasters") {
				return filepath.SkipDir
			}
			return nil
		}
		seen++
		if seen > hardLimit {
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		mtime := info.ModTime()
		if mtime.Before(startedAt) || mtime.After(endWindow) {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			rel = path
		}
		if len(out) >= maxFiles {
			extra++
			return nil
		}
		out = append(out, FileTouch{
			Path: rel,
			Size: info.Size(),
			// New = created during the job window. fileBirthTime returns the
			// real creation timestamp where the platform exposes one (btime
			// on darwin); elsewhere it falls back to mtime, which degrades
			// IsNew to "touched during the window" — an acceptable hint.
			IsNew: !fileBirthTime(info).Before(startedAt),
		})
		return nil
	})
	if walkErr != nil {
		slog.Debug("walkFilesTouched: WalkDir returned error", "dir", dir, "error", walkErr)
	}
	// Stable ordering: alphabetical by relative path so the displayed
	// list doesn't reshuffle every render.
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, extra
}

// BroadcastOperatorDone broadcasts an operator.done event. Called from the
// operator's OnTurnDone callback. The SendMessage gate (currentTurnID) is
// released only when the finishing turn is the gated one — a system-initiated
// turn (empty turnID) completing must not open the gate for a user turn that
// is still queued behind it.
func (s *LocalService) BroadcastOperatorDone(turnID, modelName string, tokensIn, tokensOut, reasoningTokens, contextTokens int) {
	s.turnMu.Lock()
	if turnID != "" && s.currentTurnID == turnID {
		s.currentTurnID = ""
	}
	responseText := s.pendingResponse.String()
	s.pendingResponse.Reset()
	reasoningText := s.pendingReasoning.String()
	s.pendingReasoning.Reset()
	s.turnMu.Unlock()

	if responseText != "" || reasoningText != "" {
		s.appendHistory(ChatEntry{
			Message:    ChatMessage{Role: MessageRoleAssistant, Content: responseText},
			Reasoning:  reasoningText,
			Timestamp:  time.Now(),
			ClaudeMeta: fmt.Sprintf("operator · %s", modelName),
		})
	}

	s.broadcast(Event{
		Type:   EventTypeOperatorDone,
		TurnID: turnID,
		Payload: OperatorDonePayload{
			ModelName:       modelName,
			TokensIn:        tokensIn,
			TokensOut:       tokensOut,
			ReasoningTokens: reasoningTokens,
			ContextTokens:   contextTokens,
		},
	})
}
