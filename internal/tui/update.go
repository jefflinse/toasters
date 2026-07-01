// Update sub-handlers: key handling and message processing for the command
// popup and worker-session kill. (Nodes-screen keys live in nodes.go.)
package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
)

// killWorkerSession returns a command that cancels a running worker session
// through the service and reports the outcome as a toast. The network call
// runs inside the command (off the update loop) — in remote-client mode it's
// an HTTP round-trip that would otherwise freeze the UI for up to 2s.
// Cancellation is cooperative — the worker stops at its next tool-call
// boundary — so the toast says so. The resulting session.done(cancelled)
// event repaints the node on its own.
func (m *Model) killWorkerSession(sessionID string) tea.Cmd {
	if sessionID == "" {
		return nil
	}
	svc := m.svc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := svc.Sessions().Cancel(ctx, sessionID); err != nil {
			return asyncToastMsg{message: "⚠ Kill failed: " + err.Error(), level: toastWarning}
		}
		return asyncToastMsg{message: "🔪 Worker killed (stops at next tool boundary)", level: toastInfo}
	}
}

// updateCmdPopup handles key events when the slash command popup is visible.
// Returns (true, cmd) if the key was consumed, (false, nil) if it should fall through.
func (m *Model) updateCmdPopup(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case "up":
		if len(m.cmdPopup.filteredCmds) > 0 {
			m.cmdPopup.selectedIdx = (m.cmdPopup.selectedIdx - 1 + len(m.cmdPopup.filteredCmds)) % len(m.cmdPopup.filteredCmds)
		}
		return true, nil
	case "down":
		if len(m.cmdPopup.filteredCmds) > 0 {
			m.cmdPopup.selectedIdx = (m.cmdPopup.selectedIdx + 1) % len(m.cmdPopup.filteredCmds)
		}
		return true, nil
	case "tab", "enter":
		if len(m.cmdPopup.filteredCmds) > 0 {
			m.input.SetValue(m.cmdPopup.filteredCmds[m.cmdPopup.selectedIdx].Name + " ")
		}
		m.cmdPopup.show = false
		return true, nil
	case "esc":
		m.cmdPopup.show = false
		return true, nil
	}
	return false, nil
}
