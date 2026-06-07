// Prompt mode UI: handles key presses when the operator has asked the user a
// question (or a round of questions via ask_user).
package tui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/service"
)

// updatePromptMode handles key presses while the model is in prompt mode.
// Option mode: ↑↓ move the cursor, ←→ switch between questions in the round,
// Enter selects (and advances, or submits on the last question), Esc cancels.
func (m *Model) updatePromptMode(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	allOptions := append(m.prompt.promptOptions, "Custom response...")

	// When typing a custom response, delegate most keys to the textarea
	// so characters like 'j' and 'k' aren't swallowed by navigation.
	if m.prompt.promptCustom {
		switch msg.String() {
		case "enter":
			result := strings.TrimSpace(m.input.Value())
			if result == "" {
				result = "User provided no response."
			}
			cmds = append(cmds, m.answerCurrentQuestion(result))
		case "esc":
			m.prompt.promptCustom = false
			m.input.Reset()
		default:
			var inputCmd tea.Cmd
			m.input, inputCmd = m.input.Update(msg)
			cmds = append(cmds, inputCmd)
		}
		return m, tea.Batch(cmds...)
	}

	switch msg.String() {
	case "up", "k":
		if m.prompt.promptSelected > 0 {
			m.prompt.promptSelected--
			m.rememberCursor()
		}
	case "down", "j":
		if m.prompt.promptSelected < len(allOptions)-1 {
			m.prompt.promptSelected++
			m.rememberCursor()
		}
	case "left", "h":
		if m.prompt.roundIndex > 0 {
			m.rememberCursor()
			m.loadPromptQuestion(m.prompt.roundIndex - 1)
		}
	case "right", "l":
		if m.prompt.roundIndex < len(m.prompt.round)-1 {
			m.rememberCursor()
			m.loadPromptQuestion(m.prompt.roundIndex + 1)
		}
	case "enter":
		if m.prompt.promptSelected == len(allOptions)-1 {
			// Selected "Custom response..."
			m.prompt.promptCustom = true
			m.input.Reset()
			cmds = append(cmds, m.input.Focus())
		} else {
			cmds = append(cmds, m.answerCurrentQuestion(allOptions[m.prompt.promptSelected]))
		}
	case "esc":
		cmds = append(cmds, m.cancelPrompt())
	}
	return m, tea.Batch(cmds...)
}

// loadPromptQuestion makes round[i] the current question, restoring that
// question's remembered cursor so revisiting shows the prior selection.
func (m *Model) loadPromptQuestion(i int) {
	if i < 0 || i >= len(m.prompt.round) {
		return
	}
	q := m.prompt.round[i]
	m.prompt.roundIndex = i
	m.prompt.promptQuestion = q.Question
	m.prompt.promptOptions = q.Options
	m.prompt.promptCustom = false
	sel := 0
	if i < len(m.prompt.roundCursor) {
		sel = m.prompt.roundCursor[i]
	}
	if max := len(q.Options); sel > max { // index len(options) == "Custom response..."
		sel = max
	}
	if sel < 0 {
		sel = 0
	}
	m.prompt.promptSelected = sel
	m.input.Reset()
}

// rememberCursor stores the current cursor for the current question so ←→
// navigation can restore it.
func (m *Model) rememberCursor() {
	for len(m.prompt.roundCursor) <= m.prompt.roundIndex {
		m.prompt.roundCursor = append(m.prompt.roundCursor, 0)
	}
	m.prompt.roundCursor[m.prompt.roundIndex] = m.prompt.promptSelected
}

// answerCurrentQuestion records the answer to the current question, then either
// advances to the next question or, on the last question, submits the round.
func (m *Model) answerCurrentQuestion(answer string) tea.Cmd {
	for len(m.prompt.roundAnswers) <= m.prompt.roundIndex {
		m.prompt.roundAnswers = append(m.prompt.roundAnswers, "")
	}
	m.prompt.roundAnswers[m.prompt.roundIndex] = answer
	m.rememberCursor()

	if m.prompt.roundIndex >= len(m.prompt.round)-1 {
		return m.finalizePrompt()
	}
	m.loadPromptQuestion(m.prompt.roundIndex + 1)
	return nil
}

// finalizePrompt combines the round's answers (backfilling any question the
// user navigated to but didn't explicitly Enter on from its cursor), echoes
// them into the chat transcript, clears prompt state, and delivers the response
// to whoever is waiting (operator ask_user or a graph-node interrupt).
func (m *Model) finalizePrompt() tea.Cmd {
	round := m.prompt.round
	answers := make([]string, len(round))
	for i := range round {
		if i < len(m.prompt.roundAnswers) && m.prompt.roundAnswers[i] != "" {
			answers[i] = m.prompt.roundAnswers[i]
			continue
		}
		// Backfill from the remembered cursor if it points at a real option.
		if i < len(m.prompt.roundCursor) {
			if c := m.prompt.roundCursor[i]; c >= 0 && c < len(round[i].Options) {
				answers[i] = round[i].Options[c]
			}
		}
	}

	requestID := m.prompt.requestID
	combined := combinePromptAnswers(round, answers)

	m.prompt = promptModeState{}
	m.input.Reset()

	if requestID == "" || m.svc == nil {
		// No correlated request — fall back to a normal chat message, which
		// echoes the user entry itself.
		m.input.SetValue(combined)
		return m.sendMessage()
	}

	// Echo the user's answer(s) into the transcript so the exchange is visible.
	m.appendEntry(service.ChatEntry{
		Message:   service.ChatMessage{Role: service.MessageRoleUser, Content: combined},
		Timestamp: time.Now(),
	})
	m.updateViewportContent()
	m.chatViewport.GotoBottom()

	return func() tea.Msg {
		if err := m.svc.Operator().RespondToPrompt(context.Background(), requestID, combined); err != nil {
			slog.Warn("failed to respond to prompt", "request_id", requestID, "error", err)
		}
		return nil
	}
}

// cancelPrompt abandons the whole round and unblocks the waiter with a
// cancellation marker. No user bubble is echoed for a cancel.
func (m *Model) cancelPrompt() tea.Cmd {
	requestID := m.prompt.requestID
	m.prompt = promptModeState{}
	m.input.Reset()

	if requestID == "" || m.svc == nil {
		return nil
	}
	return func() tea.Msg {
		if err := m.svc.Operator().RespondToPrompt(context.Background(), requestID, "User cancelled."); err != nil {
			slog.Warn("failed to respond to prompt", "request_id", requestID, "error", err)
		}
		return nil
	}
}

// combinePromptAnswers renders the round's answers into a single response
// string. A single-question round returns the bare answer (back-compat); a
// multi-question round returns a numbered question→answer block.
func combinePromptAnswers(round []service.PromptQuestion, answers []string) string {
	if len(round) <= 1 {
		if len(answers) > 0 {
			return answers[0]
		}
		return ""
	}
	var b strings.Builder
	for i, q := range round {
		ans := ""
		if i < len(answers) {
			ans = answers[i]
		}
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%d. %s\n   → %s\n", i+1, q.Question, ans)
	}
	return strings.TrimRight(b.String(), "\n")
}

// promptHistoryContent renders the questions being asked for the chat feed
// record. Retained for callers/tests; the live flow records answers instead.
func promptHistoryContent(round []service.PromptQuestion) string {
	if len(round) <= 1 {
		if len(round) == 1 {
			return round[0].Question
		}
		return ""
	}
	var b strings.Builder
	for i, q := range round {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%d. %s", i+1, q.Question)
	}
	return b.String()
}
