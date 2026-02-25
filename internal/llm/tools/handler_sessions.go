package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jefflinse/toasters/internal/provider"
)

func handleListSessions(_ context.Context, te *ToolExecutor, _ provider.ToolCall) (string, error) {
	if te.Runtime == nil {
		return "runtime not initialized", nil
	}
	sessions := te.Runtime.ActiveSessions()
	if len(sessions) == 0 {
		return "no active runtime sessions", nil
	}
	var lines []string
	for _, s := range sessions {
		elapsed := time.Since(s.StartTime).Truncate(time.Second)
		lines = append(lines, fmt.Sprintf("session %s: agent=%s model=%s provider=%s status=%s tokens_in=%d tokens_out=%d elapsed=%s",
			shortID(s.ID), s.AgentID, s.Model, s.Provider, s.Status, s.TokensIn, s.TokensOut, elapsed))
	}
	return strings.Join(lines, "\n"), nil
}

func handleCancelSession(_ context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
	if te.Runtime == nil {
		return "runtime not initialized", nil
	}
	var args struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing cancel_session args: %w", err)
	}
	// Support prefix matching — find the session whose ID starts with the given prefix.
	sessions := te.Runtime.ActiveSessions()
	var matchID string
	for _, s := range sessions {
		if strings.HasPrefix(s.ID, args.SessionID) {
			if matchID != "" {
				return fmt.Sprintf("ambiguous session prefix %q — matches multiple sessions", args.SessionID), nil
			}
			matchID = s.ID
		}
	}
	if matchID == "" {
		// Try exact match on all sessions (including non-active).
		if err := te.Runtime.CancelSession(args.SessionID); err != nil {
			return fmt.Sprintf("session %q not found: %v", args.SessionID, err), nil
		}
		return fmt.Sprintf("cancelled session %s", args.SessionID), nil
	}
	if err := te.Runtime.CancelSession(matchID); err != nil {
		return fmt.Sprintf("error cancelling session: %v", err), nil
	}
	return fmt.Sprintf("cancelled session %s", shortID(matchID)), nil
}
