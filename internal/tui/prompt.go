// Prompt and tool handling: prompt mode UI, tool call dispatch, and ask-user response processing.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/llm"
)

// updatePromptMode handles key presses when the model is in prompt mode
// (i.e. the operator has asked the user a question with numbered options).
func (m *Model) updatePromptMode(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	allOptions := append(m.promptOptions, "Custom response...")
	switch msg.String() {
	case "up", "k":
		if m.promptSelected > 0 {
			m.promptSelected--
		}
	case "down", "j":
		if m.promptSelected < len(allOptions)-1 {
			m.promptSelected++
		}
	case "enter":
		if !m.promptCustom {
			if m.promptSelected == len(allOptions)-1 {
				// Selected "Custom response..."
				m.promptCustom = true
				m.input.Reset()
				cmds = append(cmds, m.input.Focus())
			} else {
				// Selected a pre-defined option.
				result := allOptions[m.promptSelected]
				call := m.promptPendingCall
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
			call := m.promptPendingCall
			cmds = append(cmds, func() tea.Msg {
				return AskUserResponseMsg{Call: call, Result: result}
			})
		}
	case "esc":
		if m.promptCustom {
			// Go back to option selection.
			m.promptCustom = false
			m.input.Reset()
		} else {
			// Cancel entirely.
			call := m.promptPendingCall
			cmds = append(cmds, func() tea.Msg {
				return AskUserResponseMsg{Call: call, Result: "User cancelled."}
			})
		}
	default:
		if m.promptCustom {
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
	m.streaming = false

	// Check for kill_slot, assign_team, ask_user, or escalate_to_user — intercept before ExecuteTool.
	for _, call := range msg.Calls {
		if call.Function.Name == "kill_slot" {
			var args struct {
				SlotID int `json:"slot_id"`
			}
			_ = json.Unmarshal([]byte(call.Function.Arguments), &args)

			// Look up slot info for the confirmation message.
			question := fmt.Sprintf("Kill slot %d?", args.SlotID)
			snapshots := m.gateway.Slots()
			if args.SlotID >= 0 && args.SlotID < len(snapshots) {
				snap := snapshots[args.SlotID]
				if snap.AgentName != "" {
					question = fmt.Sprintf("Kill slot %d (%s on %s)?", args.SlotID, snap.AgentName, snap.JobID)
				}
			}

			m.appendEntry(ChatEntry{
				Message:    llm.Message{Role: "assistant", Content: question},
				Timestamp:  time.Now(),
				ClaudeMeta: "kill-confirm",
			})
			m.streaming = false
			m.promptMode = true
			m.confirmKill = true
			m.confirmDispatch = false
			m.pendingKillSlot = args.SlotID
			m.promptPendingCall = call
			m.promptQuestion = question
			m.promptOptions = []string{"Yes, kill", "Cancel"}
			m.promptSelected = 0
			m.promptCustom = false
			m.updateViewportContent()
			if !m.userScrolled {
				m.chatViewport.GotoBottom()
			}
			cmds = append(cmds, m.input.Focus())
			return m, tea.Batch(cmds...)
		}
		if call.Function.Name == "assign_team" {
			var args struct {
				TeamName string `json:"team_name"`
				JobID    string `json:"job_id"`
			}
			_ = json.Unmarshal([]byte(call.Function.Arguments), &args)

			question := fmt.Sprintf("Assign job '%s' to team '%s'?", args.JobID, args.TeamName)
			m.appendEntry(ChatEntry{
				Message:    llm.Message{Role: "assistant", Content: question},
				Timestamp:  time.Now(),
				ClaudeMeta: "dispatch-confirm",
			})
			m.streaming = false
			m.promptMode = true
			m.confirmDispatch = true
			m.changingTeam = false
			m.pendingDispatch = call
			m.promptQuestion = question
			m.promptOptions = []string{"Yes, dispatch", "Change team", "Cancel"}
			m.promptSelected = 0
			m.promptCustom = false
			m.promptPendingCall = call
			m.updateViewportContent()
			if !m.userScrolled {
				m.chatViewport.GotoBottom()
			}
			cmds = append(cmds, m.input.Focus())
			return m, tea.Batch(cmds...)
		}
		if call.Function.Name == "escalate_to_user" {
			var args struct {
				Question string `json:"question"`
				Context  string `json:"context"`
			}
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				args.Question = "A team has encountered a blocker."
				args.Context = ""
			}
			fullQuestion := args.Question
			if args.Context != "" {
				fullQuestion = args.Question + "\n\n" + args.Context
			}
			m.appendEntry(ChatEntry{
				Message:    llm.Message{Role: "assistant", Content: fullQuestion},
				Timestamp:  time.Now(),
				ClaudeMeta: "escalate-prompt",
			})
			m.streaming = false
			m.promptMode = true
			m.promptQuestion = fullQuestion
			m.promptOptions = []string{"Provide answer"}
			m.promptSelected = 0
			m.promptCustom = false
			m.promptPendingCall = call
			m.updateViewportContent()
			if !m.userScrolled {
				m.chatViewport.GotoBottom()
			}
			cmds = append(cmds, m.input.Focus())
			return m, tea.Batch(cmds...)
		}
		if call.Function.Name == "ask_user" {
			// Parse arguments.
			var args struct {
				Question string   `json:"question"`
				Options  []string `json:"options"`
			}
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				args.Question = "What would you like to do?"
				args.Options = []string{}
			}
			// Render question into chat history as an assistant message.
			m.appendEntry(ChatEntry{
				Message:    llm.Message{Role: "assistant", Content: args.Question},
				Timestamp:  time.Now(),
				ClaudeMeta: "ask-user-prompt",
			})
			// Enter prompt mode.
			m.streaming = false
			m.promptMode = true
			m.promptQuestion = args.Question
			m.promptOptions = args.Options
			m.promptSelected = 0
			m.promptCustom = false
			m.promptPendingCall = call
			m.updateViewportContent()
			if !m.userScrolled {
				m.chatViewport.GotoBottom()
			}
			cmds = append(cmds, m.input.Focus())
			return m, tea.Batch(cmds...)
		}
	}

	// Append the assistant "tool call" turn to the conversation.
	assistantMsg := llm.Message{
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
		indicator := fmt.Sprintf("⚙ calling `%s`…", call.Function.Name)
		m.appendEntry(ChatEntry{
			Message:    llm.Message{Role: "assistant", Content: indicator},
			Timestamp:  time.Now(),
			ClaudeMeta: "tool-call-indicator",
		})
	}

	// Update the viewport so the user sees the tool call indicators immediately.
	m.updateViewportContent()
	if !m.userScrolled {
		m.chatViewport.GotoBottom()
	}

	// Dispatch tool execution asynchronously.
	m.toolsInFlight = true
	ctx, cancel := context.WithCancel(context.Background())
	m.toolCancelFunc = cancel
	return m, executeToolsCmd(ctx, msg.Calls, m.toolExec)
}

// handleAskUserResponse processes an AskUserResponseMsg — the user has submitted
// a response in prompt mode (or a confirmation dialog like kill/dispatch/timeout).
func (m *Model) handleAskUserResponse(msg AskUserResponseMsg) (tea.Model, tea.Cmd) {
	// Handle slot-timeout confirmation flow.
	if m.confirmTimeout {
		m.confirmTimeout = false
		m.promptMode = false
		m.promptOptions = nil
		m.promptSelected = 0
		m.promptPendingCall = llm.ToolCall{}
		switch msg.Result {
		case "Continue (+15m)":
			_ = m.gateway.ExtendSlot(m.pendingTimeoutSlot)
			m.appendEntry(ChatEntry{
				Message:    llm.Message{Role: "assistant", Content: fmt.Sprintf("Slot %d extended by 15m.", m.pendingTimeoutSlot)},
				Timestamp:  time.Now(),
				ClaudeMeta: "tool-call-indicator",
			})
		default: // "Kill"
			_ = m.gateway.Kill(m.pendingTimeoutSlot)
			m.appendEntry(ChatEntry{
				Message:    llm.Message{Role: "assistant", Content: fmt.Sprintf("Slot %d killed.", m.pendingTimeoutSlot)},
				Timestamp:  time.Now(),
				ClaudeMeta: "tool-call-indicator",
			})
		}
		m.updateViewportContent()
		if !m.userScrolled {
			m.chatViewport.GotoBottom()
		}
		return m, m.input.Focus()
	}

	// Handle kill confirmation flow.
	if m.confirmKill {
		m.confirmKill = false
		m.promptMode = false
		m.promptCustom = false
		m.promptOptions = nil
		m.promptSelected = 0

		var result string
		if msg.Result == "Yes, kill" {
			_ = m.gateway.Kill(m.pendingKillSlot)
			result = fmt.Sprintf("killed slot %d", m.pendingKillSlot)
		} else {
			result = "User cancelled the kill."
		}
		m.appendEntry(ChatEntry{
			Message:    llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{m.promptPendingCall}},
			Timestamp:  time.Now(),
			ClaudeMeta: "tool-call-indicator",
		})
		m.appendEntry(ChatEntry{
			Message:   llm.Message{Role: "tool", Content: result, ToolCallID: m.promptPendingCall.ID},
			Timestamp: time.Now(),
		})
		m.updateViewportContent()
		return m, m.startStream(m.messagesFromEntries())
	}

	// Handle dispatch confirmation flow.
	if m.confirmDispatch {
		m.promptMode = false
		m.promptCustom = false
		m.promptOptions = nil
		m.promptSelected = 0

		if m.changingTeam {
			// Second prompt: user selected a new team name.
			m.changingTeam = false
			m.confirmDispatch = false

			// Rewrite the team_name in the pending dispatch args.
			var args map[string]any
			_ = json.Unmarshal([]byte(m.pendingDispatch.Function.Arguments), &args)
			args["team_name"] = msg.Result
			newArgs, _ := json.Marshal(args)
			m.pendingDispatch.Function.Arguments = string(newArgs)

			m.toolsInFlight = true
			ctx, cancel := context.WithCancel(context.Background())
			m.toolCancelFunc = cancel
			m.appendEntry(ChatEntry{
				Message:    llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{m.pendingDispatch}},
				Timestamp:  time.Now(),
				ClaudeMeta: "tool-call-indicator",
			})
			m.updateViewportContent()
			return m, executeToolsCmd(ctx, []llm.ToolCall{m.pendingDispatch}, m.toolExec)
		}

		switch msg.Result {
		case "Yes, dispatch":
			m.confirmDispatch = false
			m.toolsInFlight = true
			ctx, cancel := context.WithCancel(context.Background())
			m.toolCancelFunc = cancel
			m.appendEntry(ChatEntry{
				Message:    llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{m.pendingDispatch}},
				Timestamp:  time.Now(),
				ClaudeMeta: "tool-call-indicator",
			})
			m.updateViewportContent()
			return m, executeToolsCmd(ctx, []llm.ToolCall{m.pendingDispatch}, m.toolExec)

		case "Change team":
			// Show second prompt with available team names.
			teamNames := make([]string, len(m.teams))
			for i, t := range m.teams {
				teamNames[i] = t.Name
			}
			m.promptMode = true
			m.confirmDispatch = true
			m.changingTeam = true
			m.promptQuestion = "Select a team:"
			m.promptOptions = teamNames
			m.promptSelected = 0
			m.promptPendingCall = m.pendingDispatch
			m.updateViewportContent()
			return m, m.input.Focus()

		default: // "Cancel" or anything else
			m.confirmDispatch = false
			m.appendEntry(ChatEntry{
				Message:    llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{m.pendingDispatch}},
				Timestamp:  time.Now(),
				ClaudeMeta: "tool-call-indicator",
			})
			m.appendEntry(ChatEntry{
				Message:   llm.Message{Role: "tool", Content: "User cancelled the dispatch.", ToolCallID: m.pendingDispatch.ID},
				Timestamp: time.Now(),
			})
			m.updateViewportContent()
			return m, m.startStream(m.messagesFromEntries())
		}
	}

	// Clear prompt mode.
	m.promptMode = false
	m.promptCustom = false
	m.promptQuestion = ""
	m.promptOptions = nil
	m.promptSelected = 0
	m.input.Reset()

	// Inject the tool call + result into message history.
	// First: the assistant turn with the tool call.
	m.appendEntry(ChatEntry{
		Message:    llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{msg.Call}},
		Timestamp:  time.Now(),
		ClaudeMeta: "tool-call-indicator",
	})
	// Then: the tool result.
	m.appendEntry(ChatEntry{
		Message:   llm.Message{Role: "tool", Content: msg.Result, ToolCallID: msg.Call.ID},
		Timestamp: time.Now(),
	})
	m.updateViewportContent()
	if !m.userScrolled {
		m.chatViewport.GotoBottom()
	}
	// Resume the stream.
	return m, m.startStream(m.messagesFromEntries())
}
