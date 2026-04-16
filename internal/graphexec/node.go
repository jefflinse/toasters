package graphexec

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jefflinse/rhizome"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

const (
	// DefaultMaxTurns is the default maximum number of LLM round-trips
	// (send messages, get response, execute tools) per node.
	DefaultMaxTurns = 20

	// DefaultMaxToolResultBytes caps individual tool results to prevent
	// context window overflow. Matches session.go:219.
	DefaultMaxToolResultBytes = 8 * 1024
)

// NodeConfig configures an LLM node for use in a rhizome graph.
type NodeConfig struct {
	// Provider is the LLM provider to use for chat completions.
	Provider provider.Provider

	// ToolExecutor dispatches tool calls. May be nil if the node needs
	// no tools (pure-reasoning node).
	ToolExecutor runtime.ToolExecutor

	// SystemPrompt is the system prompt template for this node.
	// It may reference artifacts from TaskState via prompt construction.
	SystemPrompt string

	// InitialMessage is the user message that kicks off the conversation.
	// If empty, the node builds one from TaskState artifacts.
	InitialMessage string

	// Model overrides TaskState.Model for this node. If empty, uses
	// the model from TaskState.
	Model string

	// MaxTurns caps the number of LLM round-trips. 0 = DefaultMaxTurns.
	MaxTurns int

	// MaxToolResultBytes caps individual tool result size.
	// 0 = DefaultMaxToolResultBytes.
	MaxToolResultBytes int

	// ArtifactKey is the key under which this node stores its output
	// in TaskState.Artifacts. If empty, the node's final text is stored
	// in TaskState.FinalText but no artifact is written.
	ArtifactKey string

	// TerminalTools, when non-empty, lists tool names that cause the
	// conversation loop to return immediately after executing. Used with
	// decision tools so a node ends as soon as the LLM signals its outcome,
	// saving the round-trip that would otherwise be spent on a filler
	// "decision made" text response.
	TerminalTools []string
}

// LLMNode returns a rhizome.NodeFunc that runs a bounded LLM conversation.
//
// This is the core building block for graph-based execution. It extracts
// the Session.Run() conversation loop (session.go:119-240) into a stateless
// transformer: fresh prompt from state in, structured artifacts out, no
// accumulated history across nodes.
//
// The conversation loop:
//  1. Build messages from TaskState (fresh context, not accumulated history)
//  2. Send to LLM via provider.ChatStream()
//  3. Collect response (text + tool calls)
//  4. If tool calls: execute via ToolExecutor, append results, loop to step 2
//  5. If no tool calls: extract output into TaskState, return
//  6. If max turns exceeded: return with partial results
func LLMNode(cfg NodeConfig) rhizome.NodeFunc[*TaskState] {
	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = DefaultMaxTurns
	}
	maxResultBytes := cfg.MaxToolResultBytes
	if maxResultBytes <= 0 {
		maxResultBytes = DefaultMaxToolResultBytes
	}

	return func(ctx context.Context, state *TaskState) (*TaskState, error) {
		model := cfg.Model
		if model == "" {
			model = state.Model
		}

		// Build tool definitions for the provider.
		var tools []provider.Tool
		if cfg.ToolExecutor != nil {
			for _, td := range cfg.ToolExecutor.Definitions() {
				tools = append(tools, provider.Tool{
					Name:        td.Name,
					Description: td.Description,
					Parameters:  td.Parameters,
				})
			}
		}

		// Seed the conversation with the initial user message.
		initialMsg := cfg.InitialMessage
		if initialMsg == "" {
			initialMsg = buildInitialMessage(state)
		}
		messages := []provider.Message{
			{Role: "user", Content: initialMsg},
		}

		// Conversation loop — bounded by maxTurns.
		for turn := 0; turn < maxTurns; turn++ {
			if ctx.Err() != nil {
				return state, ctx.Err()
			}

			// Send messages to LLM.
			eventCh, err := cfg.Provider.ChatStream(ctx, provider.ChatRequest{
				Model:    model,
				Messages: messages,
				Tools:    tools,
				System:   cfg.SystemPrompt,
			})
			if err != nil {
				return state, fmt.Errorf("starting stream: %w", err)
			}

			// Collect response.
			assistantMsg, toolCalls, err := collectResponse(ctx, eventCh)
			if err != nil {
				return state, fmt.Errorf("collecting response: %w", err)
			}
			messages = append(messages, assistantMsg)

			// No tool calls — conversation complete.
			if len(toolCalls) == 0 {
				state.FinalText = assistantMsg.Content
				if cfg.ArtifactKey != "" {
					state.SetArtifact(cfg.ArtifactKey, assistantMsg.Content)
				}
				return state, nil
			}

			// Execute tool calls.
			if cfg.ToolExecutor == nil {
				return state, fmt.Errorf("LLM requested tool calls but no ToolExecutor configured")
			}

			terminated := false
			for _, tc := range toolCalls {
				result, execErr := cfg.ToolExecutor.Execute(ctx, tc.Name, tc.Arguments)
				if execErr != nil {
					result = fmt.Sprintf("error: %s", execErr.Error())
				}

				// Cap tool results to prevent context overflow.
				if len(result) > maxResultBytes {
					result = result[:maxResultBytes] + "\n[... truncated: result exceeded limit ...]"
				}

				messages = append(messages, provider.Message{
					Role:       "tool",
					Content:    result,
					ToolCallID: tc.ID,
				})

				if slices.Contains(cfg.TerminalTools, tc.Name) {
					terminated = true
				}
			}

			// A terminal tool (e.g. a decision tool) was called — the node's
			// outcome is recorded in state and routing will pick the next edge.
			// No further LLM round-trips are needed.
			if terminated {
				return state, nil
			}

			// Loop — send tool results back to LLM.
		}

		// Max turns exceeded — return partial state.
		state.Err = fmt.Errorf("max turns (%d) exceeded", maxTurns)
		return state, nil
	}
}

// collectResponse reads from the event channel and accumulates text and
// tool calls. This mirrors session.go:collectResponse but without the
// subscriber pattern or token tracking.
func collectResponse(ctx context.Context, eventCh <-chan provider.StreamEvent) (provider.Message, []provider.ToolCall, error) {
	var textBuf strings.Builder
	var toolCalls []provider.ToolCall

	for {
		select {
		case <-ctx.Done():
			return provider.Message{}, nil, ctx.Err()

		case ev, ok := <-eventCh:
			if !ok {
				msg := provider.Message{
					Role:      "assistant",
					Content:   textBuf.String(),
					ToolCalls: toolCalls,
				}
				return msg, toolCalls, nil
			}

			switch ev.Type {
			case provider.EventText:
				textBuf.WriteString(ev.Text)
			case provider.EventToolCall:
				if ev.ToolCall != nil {
					toolCalls = append(toolCalls, *ev.ToolCall)
				}
			case provider.EventError:
				return provider.Message{}, nil, ev.Error
			case provider.EventDone:
				// Continue reading until channel closes.
			}
		}
	}
}

// buildInitialMessage constructs a user message from TaskState for nodes
// that don't have a hardcoded InitialMessage. This is the default
// entry point — specialized nodes will override this via NodeConfig.
func buildInitialMessage(state *TaskState) string {
	var parts []string

	if desc := state.GetArtifactString("task.description"); desc != "" {
		parts = append(parts, fmt.Sprintf("Task: %s", desc))
	}

	if state.WorkspaceDir != "" {
		parts = append(parts, fmt.Sprintf("Workspace: %s", state.WorkspaceDir))
	}

	// Include any relevant artifacts as context.
	for key, val := range state.Artifacts {
		if key == "task.description" {
			continue // already included above
		}
		if s, ok := val.(string); ok && s != "" {
			parts = append(parts, fmt.Sprintf("## %s\n%s", key, s))
		}
	}

	if len(parts) == 0 {
		return "Please complete the assigned task."
	}

	return strings.Join(parts, "\n\n")
}

// Ensure LLMNode returns the right type at compile time.
var _ rhizome.NodeFunc[*TaskState] = LLMNode(NodeConfig{})
