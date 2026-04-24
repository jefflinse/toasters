package graphexec

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/jefflinse/mycelium/agent"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/provider"
)

// graphSession tracks an in-flight graph-node LLM session so we can write
// both the `worker_sessions` row and the full message history to
// `session_messages` after the node finishes. The SessionID used here is
// new-uuid-per-execution; the `worker_id` column carries "graph:<nodeID>"
// so retries of the same node yield separate rows, findable by task_id.
type graphSession struct {
	id       string
	startedAt time.Time
}

// openGraphSession inserts a `worker_sessions` row with status=active for
// a graph-node LLM call. When store is nil (tests) it is a silent no-op
// and the returned session's id is empty. Failures are logged and
// swallowed — persistence is best-effort; a DB outage must not block
// graph execution.
func openGraphSession(ctx context.Context, store db.Store, state *TaskState, nodeID, sysPrompt string, toolNames []string, cfg TemplateConfig) *graphSession {
	if store == nil {
		return &graphSession{}
	}
	id, err := uuid.NewV4()
	if err != nil {
		slog.Warn("graph session: failed to mint uuid", "error", err)
		return &graphSession{}
	}
	toolsJSON, _ := json.Marshal(toolNames)
	now := time.Now().UTC()
	sess := &db.WorkerSession{
		ID:           id.String(),
		WorkerID:     "graph:" + nodeID,
		JobID:        state.JobID,
		TaskID:       state.TaskID,
		Status:       db.SessionStatusActive,
		Model:        cfg.Model,
		Provider:     state.ProviderName,
		StartedAt:    now,
		SystemPrompt: sysPrompt,
		ToolsJSON:    string(toolsJSON),
	}
	if err := store.CreateSession(ctx, sess); err != nil {
		slog.Warn("graph session: failed to create worker_sessions row",
			"session_id", sess.ID, "task_id", state.TaskID, "node", nodeID, "error", err)
		return &graphSession{}
	}
	return &graphSession{id: id.String(), startedAt: now}
}

// closeGraphSession finalizes a graph-node session: appends every message
// from the agent's history to `session_messages` and updates the parent
// `worker_sessions` row with final status and token counts. The initial
// user message is in history[0]; the system prompt lives on the
// worker_sessions row, not in the transcript.
//
// err is the error returned by agent.Run (nil on success). A non-nil err
// marks the session failed even when a terminal-status Result came back,
// so transcripts for ErrNoTerminalTool / ErrMaxTurnsExceeded are still
// queryable.
//
// reasoning carries the accumulated chain-of-thought captured via the
// agent's OnEvent callback. Mycelium does not fold reasoning into
// Result.History (the model never sees its own reasoning on the next
// turn), so RoleNode collects it separately and hands it in here.
// Non-empty reasoning is persisted as the first session_messages row
// with role="reasoning" so post-hoc debuggers see the pre-answer
// thinking before the assistant/tool turns.
func closeGraphSession(ctx context.Context, store db.Store, sess *graphSession, res agent.Result[json.RawMessage], runErr error, reasoning string) {
	if store == nil || sess == nil || sess.id == "" {
		return
	}
	seq := 0
	if reasoning != "" {
		seq++
		if err := store.AppendSessionMessage(ctx, &db.SessionMessage{
			SessionID: sess.id,
			Seq:       seq,
			Role:      "reasoning",
			Content:   reasoning,
		}); err != nil {
			slog.Warn("graph session: failed to append reasoning",
				"session_id", sess.id, "error", err)
		}
	}
	for _, msg := range res.History {
		seq++
		sm := &db.SessionMessage{
			SessionID:  sess.id,
			Seq:        seq,
			Role:       msg.Role,
			Content:    msg.Content,
			ToolCallID: msg.ToolCallID,
		}
		if len(msg.ToolCalls) > 0 {
			if data, err := json.Marshal(msg.ToolCalls); err == nil {
				sm.ToolCalls = string(data)
			}
		}
		if err := store.AppendSessionMessage(ctx, sm); err != nil {
			slog.Warn("graph session: failed to append message",
				"session_id", sess.id, "seq", sm.Seq, "error", err)
		}
	}
	status := db.SessionStatusCompleted
	if runErr != nil {
		status = db.SessionStatusFailed
	}
	endedAt := time.Now().UTC()
	tokensIn := int64(res.Usage.InputTokens)
	tokensOut := int64(res.Usage.OutputTokens)
	if err := store.UpdateSession(ctx, sess.id, db.SessionUpdate{
		Status:    &status,
		TokensIn:  &tokensIn,
		TokensOut: &tokensOut,
		EndedAt:   &endedAt,
	}); err != nil {
		slog.Warn("graph session: failed to finalize worker_sessions row",
			"session_id", sess.id, "error", err)
	}
}

// toolNamesOf extracts the tool names a node was handed to the agent
// loop, for the worker_sessions.tools_json column. Keeps the set sorted
// implicitly (insertion order mirrors the allowlist) — the column is
// informational, not a source of truth.
func toolNamesOf(tools []agent.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

// lastAssistantText returns the text content of the most recent
// assistant message in a history, or "" if none found. Useful for
// failure diagnostics (e.g. logging the model's text when a terminal
// call was missed).
func lastAssistantText(history []provider.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" && history[i].Content != "" {
			return history[i].Content
		}
	}
	return ""
}
