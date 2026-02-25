// Tool execution: async tool call dispatch via goroutine with cancellation support.
package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/jefflinse/toasters/internal/llm/tools"
	"github.com/jefflinse/toasters/internal/provider"
)

// executeToolsCmd returns a tea.Cmd that executes tool calls in a goroutine.
// Results are delivered back to the Bubble Tea event loop as a ToolResultMsg.
// The goroutine does NOT access any Model fields — it only communicates via the message.
func executeToolsCmd(ctx context.Context, calls []provider.ToolCall, executor *tools.ToolExecutor) tea.Cmd {
	return func() tea.Msg {
		results := make([]ToolResult, 0, len(calls))
		for _, call := range calls {
			// Check for cancellation before each tool call.
			if ctx.Err() != nil {
				results = append(results, ToolResult{
					CallID: call.ID,
					Name:   call.Name,
					Err:    ctx.Err(),
				})
				break
			}

			result, err := executor.ExecuteTool(ctx, call)
			results = append(results, ToolResult{
				CallID: call.ID,
				Name:   call.Name,
				Result: result,
				Err:    err,
			})
		}
		return ToolResultMsg{Results: results}
	}
}
