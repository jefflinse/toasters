package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jefflinse/toasters/internal/provider"
)

func handleListSlots(_ context.Context, te *ToolExecutor, _ provider.ToolCall) (string, error) {
	if te.Gateway == nil {
		return "gateway not initialized", nil
	}
	slots := te.Gateway.SlotSummaries()
	if len(slots) == 0 {
		return "no active slots", nil
	}
	var lines []string
	for _, s := range slots {
		lines = append(lines, fmt.Sprintf("slot %d: %s on %s — %s (%s)", s.Index, s.Team, s.JobID, s.Status, s.Elapsed))
	}
	return strings.Join(lines, "\n"), nil
}

func handleKillSlot(_ context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
	if te.Gateway == nil {
		return "gateway not initialized", nil
	}
	var args struct {
		SlotID int `json:"slot_id"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing kill_slot args: %w", err)
	}
	if err := te.Gateway.Kill(args.SlotID); err != nil {
		return fmt.Sprintf("error killing slot %d: %v", args.SlotID, err), nil
	}
	return fmt.Sprintf("killed slot %d", args.SlotID), nil
}
