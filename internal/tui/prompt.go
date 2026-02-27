// Prompt and tool handling: prompt mode UI, tool call dispatch, and ask-user response processing.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/provider"
)

// updatePromptMode handles key presses when the model is in prompt mode
// (i.e. the operator has asked the user a question with numbered options).
func (m *Model) updatePromptMode(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	allOptions := append(m.prompt.promptOptions, "Custom response...")
	switch msg.String() {
	case "up", "k":
		if m.prompt.promptSelected > 0 {
			m.prompt.promptSelected--
		}
	case "down", "j":
		if m.prompt.promptSelected < len(allOptions)-1 {
			m.prompt.promptSelected++
		}
	case "enter":
		if !m.prompt.promptCustom {
			if m.prompt.promptSelected == len(allOptions)-1 {
				// Selected "Custom response..."
				m.prompt.promptCustom = true
				m.input.Reset()
				cmds = append(cmds, m.input.Focus())
			} else {
				// Selected a pre-defined option.
				result := allOptions[m.prompt.promptSelected]
				call := m.prompt.promptPendingCall
				cmds = append(cmds, func() tea.Msg {
					return AskUserResponseMsg{Call: call, Result: result}
				})
			}
		} else {
			// Custom text submitted.
			result := strings.TrimSpace(m.input.Value())
			if result == "" {
				result = "User provided no response."
			}
			call := m.prompt.promptPendingCall
			cmds = append(cmds, func() tea.Msg {
				return AskUserResponseMsg{Call: call, Result: result}
			})
		}
	case "esc":
		if m.prompt.promptCustom {
			// Go back to option selection.
			m.prompt.promptCustom = false
			m.input.Reset()
		} else {
			// Cancel entirely.
			call := m.prompt.promptPendingCall
			cmds = append(cmds, func() tea.Msg {
				return AskUserResponseMsg{Call: call, Result: "User cancelled."}
			})
		}
	default:
		if m.prompt.promptCustom {
			// Delegate to textarea.
			var inputCmd tea.Cmd
			m.input, inputCmd = m.input.Update(msg)
			cmds = append(cmds, inputCmd)
		}
	}
	return m, tea.Batch(cmds...)
}

// handleToolCalls processes a ToolCallMsg — the LLM wants to call tools.
// It intercepts special tools (kill_slot, assign_team, ask_user, escalate_to_user)
// for confirmation prompts, then executes remaining tools and re-invokes the stream.
func (m *Model) handleToolCalls(msg ToolCallMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	// The LLM wants to call tools. Dispatch them asynchronously; results arrive
	// via ToolResultMsg, which re-invokes the stream for the final answer.
	m.stream.streaming = false

	// Check for kill_slot, assign_team, ask_user, or escalate_to_user — intercept before ExecuteTool.
	for _, call := range msg.Calls {

		if call.Name == "assign_team" {
			var args struct {
				TeamName string `json:"team_name"`
				JobID    string `json:"job_id"`
			}
			_ = json.Unmarshal(call.Arguments, &args)

			question := fmt.Sprintf("Assign job '%s' to team '%s'?", args.JobID, args.TeamName)
			m.appendEntry(ChatEntry{
				Message:    provider.Message{Role: "assistant", Content: question},
				Timestamp:  time.Now(),
				ClaudeMeta: "dispatch-confirm",
			})
			m.stream.streaming = false
			m.prompt.promptMode = true
			m.prompt.confirmDispatch = true
			m.prompt.changingTeam = false
			m.prompt.pendingDispatch = call
			m.prompt.promptQuestion = question
			m.prompt.promptOptions = []string{"Yes, dispatch", "Change team", "Cancel"}
			m.prompt.promptSelected = 0
			m.prompt.promptCustom = false
			m.prompt.promptPendingCall = call
			m.updateViewportContent()
			if !m.scroll.userScrolled {
				m.chatViewport.GotoBottom()
			}
			cmds = append(cmds, m.input.Focus())
			return m, tea.Batch(cmds...)
		}
		if call.Name == "escalate_to_user" {
			var args struct {
				Question string `json:"question"`
				Context  string `json:"context"`
			}
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				args.Question = "A team has encountered a blocker."
				args.Context = ""
			}
			fullQuestion := args.Question
			if args.Context != "" {
				fullQuestion = args.Question + "\n\n" + args.Context
			}
			m.appendEntry(ChatEntry{
				Message:    provider.Message{Role: "assistant", Content: fullQuestion},
				Timestamp:  time.Now(),
				ClaudeMeta: "escalate-prompt",
			})
			m.stream.streaming = false
			m.prompt.promptMode = true
			m.prompt.promptQuestion = fullQuestion
			m.prompt.promptOptions = []string{"Provide answer"}
			m.prompt.promptSelected = 0
			m.prompt.promptCustom = false
			m.prompt.promptPendingCall = call
			m.updateViewportContent()
			if !m.scroll.userScrolled {
				m.chatViewport.GotoBottom()
			}
			cmds = append(cmds, m.input.Focus())
			return m, tea.Batch(cmds...)
		}
		if call.Name == "ask_user" {
			// Parse arguments.
			var args struct {
				Question string   `json:"question"`
				Options  []string `json:"options"`
			}
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				args.Question = "What would you like to do?"
				args.Options = []string{}
			}
			// Render question into chat history as an assistant message.
			m.appendEntry(ChatEntry{
				Message:    provider.Message{Role: "assistant", Content: args.Question},
				Timestamp:  time.Now(),
				ClaudeMeta: "ask-user-prompt",
			})
			// Enter prompt mode.
			m.stream.streaming = false
			m.prompt.promptMode = true
			m.prompt.promptQuestion = args.Question
			m.prompt.promptOptions = args.Options
			m.prompt.promptSelected = 0
			m.prompt.promptCustom = false
			m.prompt.promptPendingCall = call
			m.updateViewportContent()
			if !m.scroll.userScrolled {
				m.chatViewport.GotoBottom()
			}
			cmds = append(cmds, m.input.Focus())
			return m, tea.Batch(cmds...)
		}
	}

	// Append the assistant "tool call" turn to the conversation.
	assistantMsg := provider.Message{
		Role:      "assistant",
		Content:   "",
		ToolCalls: msg.Calls,
	}
	m.appendEntry(ChatEntry{
		Message:   assistantMsg,
		Timestamp: time.Now(),
	})

	// Show visual indicators for each tool being called.
	for _, call := range msg.Calls {
		indicator := fmt.Sprintf("⚙ calling `%s`…", call.Name)
		m.appendEntry(ChatEntry{
			Message:    provider.Message{Role: "assistant", Content: indicator},
			Timestamp:  time.Now(),
			ClaudeMeta: "tool-call-indicator",
		})
	}

	// Update the viewport so the user sees the tool call indicators immediately.
	m.updateViewportContent()
	if !m.scroll.userScrolled {
		m.chatViewport.GotoBottom()
	}

	// Dispatch tool execution asynchronously.
	m.toolsInFlight = true
	ctx, cancel := context.WithCancel(context.Background())
	m.toolCancelFunc = cancel
	return m, executeToolsCmd(ctx, msg.Calls, m.toolExec)
}

// handleAskUserResponse processes an AskUserResponseMsg — the user has submitted
// a response in prompt mode (or a confirmation dialog like dispatch).
func (m *Model) handleAskUserResponse(msg AskUserResponseMsg) (tea.Model, tea.Cmd) {
	// Handle dispatch confirmation flow.
	if m.prompt.confirmDispatch {
		m.prompt.promptMode = false
		m.prompt.promptCustom = false
		m.prompt.promptOptions = nil
		m.prompt.promptSelected = 0

		if m.prompt.changingTeam {
			// Second prompt: user selected a new team name.
			m.prompt.changingTeam = false
			m.prompt.confirmDispatch = false

			// Rewrite the team_name in the pending dispatch args.
			var args map[string]any
			_ = json.Unmarshal(m.prompt.pendingDispatch.Arguments, &args)
			args["team_name"] = msg.Result
			newArgs, _ := json.Marshal(args)
			m.prompt.pendingDispatch.Arguments = newArgs

			m.toolsInFlight = true
			ctx, cancel := context.WithCancel(context.Background())
			m.toolCancelFunc = cancel
			m.appendEntry(ChatEntry{
				Message:    provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{m.prompt.pendingDispatch}},
				Timestamp:  time.Now(),
				ClaudeMeta: "tool-call-indicator",
			})
			m.updateViewportContent()
			return m, executeToolsCmd(ctx, []provider.ToolCall{m.prompt.pendingDispatch}, m.toolExec)
		}

		switch msg.Result {
		case "Yes, dispatch":
			m.prompt.confirmDispatch = false
			m.toolsInFlight = true
			ctx, cancel := context.WithCancel(context.Background())
			m.toolCancelFunc = cancel
			m.appendEntry(ChatEntry{
				Message:    provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{m.prompt.pendingDispatch}},
				Timestamp:  time.Now(),
				ClaudeMeta: "tool-call-indicator",
			})
			m.updateViewportContent()
			return m, executeToolsCmd(ctx, []provider.ToolCall{m.prompt.pendingDispatch}, m.toolExec)

		case "Change team":
			// Show second prompt with available team names.
			teamNames := make([]string, len(m.teams))
			for i, t := range m.teams {
				teamNames[i] = t.Name
			}
			m.prompt.promptMode = true
			m.prompt.confirmDispatch = true
			m.prompt.changingTeam = true
			m.prompt.promptQuestion = "Select a team:"
			m.prompt.promptOptions = teamNames
			m.prompt.promptSelected = 0
			m.prompt.promptPendingCall = m.prompt.pendingDispatch
			m.updateViewportContent()
			return m, m.input.Focus()

		default: // "Cancel" or anything else
			m.prompt.confirmDispatch = false
			m.appendEntry(ChatEntry{
				Message:    provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{m.prompt.pendingDispatch}},
				Timestamp:  time.Now(),
				ClaudeMeta: "tool-call-indicator",
			})
			m.appendEntry(ChatEntry{
				Message:   provider.Message{Role: "tool", Content: "User cancelled the dispatch.", ToolCallID: m.prompt.pendingDispatch.ID},
				Timestamp: time.Now(),
			})
			m.updateViewportContent()
			return m, m.startStream(m.messagesFromEntries())
		}
	}

	// Clear prompt mode.
	m.prompt.promptMode = false
	m.prompt.promptCustom = false
	m.prompt.promptQuestion = ""
	m.prompt.promptOptions = nil
	m.prompt.promptSelected = 0
	m.input.Reset()

	// Inject the tool call + result into message history.
	// First: the assistant turn with the tool call.
	m.appendEntry(ChatEntry{
		Message:    provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{msg.Call}},
		Timestamp:  time.Now(),
		ClaudeMeta: "tool-call-indicator",
	})
	// Then: the tool result.
	m.appendEntry(ChatEntry{
		Message:   provider.Message{Role: "tool", Content: msg.Result, ToolCallID: msg.Call.ID},
		Timestamp: time.Now(),
	})
	m.updateViewportContent()
	if !m.scroll.userScrolled {
		m.chatViewport.GotoBottom()
	}
	// Resume the stream.
	return m, m.startStream(m.messagesFromEntries())
}
