package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// keyPress constructs a tea.KeyPressMsg for a regular printable character.
// For characters like 'p', 'q', 'k', 'j', 'o', 'y', 'n', '[', ']', 'd', etc.
func keyPress(ch rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: ch, Text: string(ch)}
}

// specialKey constructs a tea.KeyPressMsg for a special key (up, down, left, right, esc, enter, tab).
func specialKey(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code}
}

// ctrlKey constructs a tea.KeyPressMsg for a ctrl+<key> combination.
func ctrlKey(ch rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: ch, Mod: tea.ModCtrl}
}

// --------------------------------------------------------------------------
// updateNodes tests
// --------------------------------------------------------------------------

// nodesModel returns a model with the nodes screen open and n active sessions.
func nodesModel(t *testing.T, n int) *Model {
	t.Helper()
	m := newMinimalModel(t)
	m.width = 120
	m.height = 40
	ids := []string{"alpha", "beta", "gamma", "delta"}
	for i := 0; i < n && i < len(ids); i++ {
		id := ids[i]
		m.runtimeSessions[id] = &runtimeSlot{sessionID: id, jobID: id, workerName: "coder", status: "active"}
	}
	m.openNodes()
	return &m
}

func TestUpdateNodes_ListNavigation(t *testing.T) {
	t.Parallel()

	m := nodesModel(t, 3)
	// sortedRuntimeSessions is deterministic (active, then startTime, then id):
	// alpha, beta, gamma.
	if got := m.selectedNodeSessionID(); got != "alpha" {
		t.Fatalf("initial selection = %q, want alpha", got)
	}

	res, _ := m.updateNodes(keyPress('j'))
	m = res.(*Model)
	if got := m.selectedNodeSessionID(); got != "beta" {
		t.Errorf("after down, selection = %q, want beta", got)
	}

	res, _ = m.updateNodes(keyPress('k'))
	m = res.(*Model)
	if got := m.selectedNodeSessionID(); got != "alpha" {
		t.Errorf("after up, selection = %q, want alpha", got)
	}

	// Up at the top clamps.
	res, _ = m.updateNodes(keyPress('k'))
	if got := res.(*Model).selectedNodeSessionID(); got != "alpha" {
		t.Errorf("up at top should stay on alpha, got %q", got)
	}
}

// TestUpdateNodes_SelectionSurvivesReorder verifies the selection stays pinned
// to the same node when the list reorders (the watched node finishes and moves
// from the active group to the finished group).
func TestUpdateNodes_SelectionSurvivesReorder(t *testing.T) {
	t.Parallel()

	m := nodesModel(t, 3) // alpha, beta, gamma — all active
	// Select beta.
	res, _ := m.updateNodes(keyPress('j'))
	m = res.(*Model)
	if m.selectedNodeSessionID() != "beta" {
		t.Fatalf("precondition: expected beta selected")
	}
	// alpha finishes → it sorts after the still-active beta/gamma, so the list
	// reorders. Selection must still resolve to beta, not to whatever slid into
	// index 1.
	m.runtimeSessions["alpha"].status = "completed"
	if got := m.selectedNodeSessionID(); got != "beta" {
		t.Errorf("after reorder, selection = %q, want beta (pinned by id)", got)
	}
}

func TestUpdateNodes_FocusAndTabs(t *testing.T) {
	t.Parallel()

	m := nodesModel(t, 2)
	if m.nodes.focusDetail {
		t.Fatal("nodes should open with the list focused")
	}

	// Tab focuses the detail pane.
	res, _ := m.updateNodes(specialKey(tea.KeyTab))
	m = res.(*Model)
	if !m.nodes.focusDetail {
		t.Fatal("Tab should focus the detail pane")
	}

	// Right/left cycle tabs while the detail is focused.
	res, _ = m.updateNodes(keyPress('l'))
	m = res.(*Model)
	if m.nodes.tab != cockpitTabPrompt {
		t.Errorf("after right, tab = %d, want Prompt", m.nodes.tab)
	}
	res, _ = m.updateNodes(keyPress('h'))
	m = res.(*Model)
	if m.nodes.tab != cockpitTabOutput {
		t.Errorf("after left, tab = %d, want Output", m.nodes.tab)
	}

	// Plain Tab in the detail pane must NOT switch tabs (only ←→ do).
	res, _ = m.updateNodes(specialKey(tea.KeyTab))
	m = res.(*Model)
	if m.nodes.tab != cockpitTabOutput {
		t.Errorf("Tab in detail should not switch tabs, tab = %d", m.nodes.tab)
	}

	// Shift+Tab returns focus to the list without closing the screen.
	res, _ = m.updateNodes(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	m = res.(*Model)
	if m.nodes.focusDetail {
		t.Error("Shift+Tab in detail should return focus to the list")
	}
	if !m.nodes.show {
		t.Error("Shift+Tab in detail should not close the nodes screen")
	}

	// Esc also returns focus to the list.
	m.nodes.focusDetail = true
	res, _ = m.updateNodes(specialKey(tea.KeyEscape))
	m = res.(*Model)
	if m.nodes.focusDetail {
		t.Error("Esc in detail should return focus to the list")
	}
	if !m.nodes.show {
		t.Error("Esc in detail should not close the nodes screen")
	}
}

func TestUpdateNodes_EscFromListCloses(t *testing.T) {
	t.Parallel()

	m := nodesModel(t, 2)
	res, _ := m.updateNodes(specialKey(tea.KeyEscape))
	if res.(*Model).nodes.show {
		t.Error("Esc from the list should close the nodes screen")
	}
}

func TestUpdateNodes_Filter(t *testing.T) {
	t.Parallel()

	m := nodesModel(t, 3) // alpha, beta, gamma
	res, _ := m.updateNodes(keyPress('/'))
	m = res.(*Model)
	if !m.nodes.filterActive {
		t.Fatal("'/' should start filter capture")
	}
	for _, ch := range "beta" {
		res, _ = m.updateNodes(keyPress(ch))
		m = res.(*Model)
	}
	if got := len(m.filteredNodeSessions()); got != 1 {
		t.Errorf("filter 'beta' matched %d sessions, want 1", got)
	}
	// Enter applies (keeps query, exits capture).
	res, _ = m.updateNodes(specialKey(tea.KeyEnter))
	m = res.(*Model)
	if m.nodes.filterActive {
		t.Error("Enter should exit filter capture")
	}
	if m.nodes.filterQuery != "beta" {
		t.Errorf("filterQuery = %q, want beta", m.nodes.filterQuery)
	}
}

func TestUpdateNodes_KillConfirmation(t *testing.T) {
	t.Parallel()

	t.Run("x arms confirm for a live worker", func(t *testing.T) {
		t.Parallel()
		m := nodesModel(t, 1)
		res, cmd := m.updateNodes(keyPress('x'))
		got := res.(*Model)
		if !got.nodes.confirmKill {
			t.Error("expected confirmKill armed")
		}
		if cmd != nil {
			t.Error("arming should not issue a command")
		}
	})

	t.Run("enter confirms and issues the kill", func(t *testing.T) {
		t.Parallel()
		m := nodesModel(t, 1)
		m.nodes.confirmKill = true
		res, cmd := m.updateNodes(specialKey(tea.KeyEnter))
		if res.(*Model).nodes.confirmKill {
			t.Error("confirmKill should clear after confirming")
		}
		if cmd == nil {
			t.Error("expected a kill command after confirming")
		}
	})

	t.Run("x is a no-op for a graph pseudo-session", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.width, m.height = 120, 40
		m.runtimeSessions["graph:t:n"] = &runtimeSlot{sessionID: "graph:t:n", status: "active"}
		m.openNodes()
		res, _ := m.updateNodes(keyPress('x'))
		if res.(*Model).nodes.confirmKill {
			t.Error("graph node should not arm a kill confirmation")
		}
	})
}

func TestToggleNodes(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.width, m.height = 120, 40
	m.toggleNodes()
	if !m.nodes.show {
		t.Fatal("toggle should open the nodes screen")
	}
	m.toggleNodes()
	if m.nodes.show {
		t.Error("toggle should close the nodes screen")
	}
}

func TestMainScreenTabOrder(t *testing.T) {
	t.Parallel()

	mm := newMinimalModel(t)
	mm.width, mm.height = 120, 40
	show := true
	mm.leftPanelOverride = &show // force the left panel visible so no target is skipped
	mm.focused = focusJobs
	m := &mm

	// Forward: Jobs → Fleet → Blockers → Chat → Jobs.
	for i, want := range []focusedPanel{focusFleet, focusBlockers, focusChat, focusJobs} {
		res, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		m = res.(*Model)
		if m.focused != want {
			t.Fatalf("forward step %d: focused = %v, want %v", i, m.focused, want)
		}
	}

	// Reverse from Jobs: Chat → Blockers → Fleet → Jobs.
	for i, want := range []focusedPanel{focusChat, focusBlockers, focusFleet, focusJobs} {
		res, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
		m = res.(*Model)
		if m.focused != want {
			t.Fatalf("reverse step %d: focused = %v, want %v", i, m.focused, want)
		}
	}
}

// --------------------------------------------------------------------------
// updateCmdPopup tests
// --------------------------------------------------------------------------

func TestUpdateCmdPopup_Dismiss(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.cmdPopup.show = true
	m.cmdPopup.filteredCmds = allCommands
	m.cmdPopup.selectedIdx = 2

	handled, cmd := m.updateCmdPopup(specialKey(tea.KeyEscape))

	if !handled {
		t.Error("esc should be handled (consumed)")
	}
	if m.cmdPopup.show {
		t.Error("showCmdPopup should be false after esc")
	}
	if cmd != nil {
		t.Error("expected nil cmd on dismiss")
	}
}

func TestUpdateCmdPopup_Navigation(t *testing.T) {
	t.Parallel()

	cmds := []SlashCommand{
		{Name: "/help"},
		{Name: "/exit"},
		{Name: "/new"},
	}

	tests := []struct {
		name     string
		key      tea.KeyPressMsg
		startIdx int
		wantIdx  int
	}{
		{"down from 0 of 3", specialKey(tea.KeyDown), 0, 1},
		{"down from 2 of 3 wraps to 0", specialKey(tea.KeyDown), 2, 0},
		{"up from 1 of 3", specialKey(tea.KeyUp), 1, 0},
		{"up from 0 of 3 wraps to 2", specialKey(tea.KeyUp), 0, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.cmdPopup.show = true
			m.cmdPopup.filteredCmds = cmds
			m.cmdPopup.selectedIdx = tt.startIdx

			handled, _ := m.updateCmdPopup(tt.key)

			if !handled {
				t.Error("navigation key should be handled")
			}
			if m.cmdPopup.selectedIdx != tt.wantIdx {
				t.Errorf("selectedCmdIdx = %d, want %d", m.cmdPopup.selectedIdx, tt.wantIdx)
			}
		})
	}
}

func TestUpdateCmdPopup_NavigationEmptyCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"up with empty commands", specialKey(tea.KeyUp)},
		{"down with empty commands", specialKey(tea.KeyDown)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.cmdPopup.show = true
			m.cmdPopup.filteredCmds = nil
			m.cmdPopup.selectedIdx = 0

			handled, _ := m.updateCmdPopup(tt.key)

			if !handled {
				t.Error("navigation key should be handled even with empty commands")
			}
			if m.cmdPopup.selectedIdx != 0 {
				t.Errorf("selectedCmdIdx = %d, want 0 (unchanged for empty commands)", m.cmdPopup.selectedIdx)
			}
		})
	}
}

func TestUpdateCmdPopup_SelectionEnter(t *testing.T) {
	t.Parallel()

	cmds := []SlashCommand{
		{Name: "/help"},
		{Name: "/exit"},
		{Name: "/new"},
	}

	m := newMinimalModel(t)
	m.cmdPopup.show = true
	m.cmdPopup.filteredCmds = cmds
	m.cmdPopup.selectedIdx = 1

	handled, _ := m.updateCmdPopup(specialKey(tea.KeyEnter))

	if !handled {
		t.Error("enter should be handled")
	}
	if m.cmdPopup.show {
		t.Error("showCmdPopup should be false after selection")
	}
}

func TestUpdateCmdPopup_SelectionTab(t *testing.T) {
	t.Parallel()

	cmds := []SlashCommand{
		{Name: "/help"},
		{Name: "/exit"},
	}

	m := newMinimalModel(t)
	m.cmdPopup.show = true
	m.cmdPopup.filteredCmds = cmds
	m.cmdPopup.selectedIdx = 0

	handled, _ := m.updateCmdPopup(specialKey(tea.KeyTab))

	if !handled {
		t.Error("tab should be handled")
	}
	if m.cmdPopup.show {
		t.Error("showCmdPopup should be false after tab selection")
	}
	if m.input.Value() != "/help " {
		t.Errorf("input value = %q, want %q", m.input.Value(), "/help ")
	}
}

func TestUpdateCmdPopup_SelectionWithEmptyCommands(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.cmdPopup.show = true
	m.cmdPopup.filteredCmds = nil
	m.cmdPopup.selectedIdx = 0

	handled, _ := m.updateCmdPopup(specialKey(tea.KeyEnter))

	if !handled {
		t.Error("enter should be handled even with empty commands")
	}
	if m.cmdPopup.show {
		t.Error("showCmdPopup should be false after enter with empty commands")
	}
	// Input should remain empty since there are no commands to select.
	if m.input.Value() != "" {
		t.Errorf("input value = %q, want empty string", m.input.Value())
	}
}

func TestUpdateCmdPopup_UnhandledKeyFallsThrough(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.cmdPopup.show = true
	m.cmdPopup.filteredCmds = allCommands
	m.cmdPopup.selectedIdx = 0

	handled, cmd := m.updateCmdPopup(keyPress('a'))

	if handled {
		t.Error("regular character should not be handled (should fall through)")
	}
	if m.cmdPopup.show != true {
		t.Error("showCmdPopup should remain true for unhandled key")
	}
	if cmd != nil {
		t.Error("expected nil cmd for unhandled key")
	}
}

func TestSessionMetaMsg_UpdatesSlot(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.runtimeSessions["graph:t1:plan"] = &runtimeSlot{sessionID: "graph:t1:plan", status: "active"}

	res, _ := m.Update(SessionMetaMsg{
		SessionID:   "graph:t1:plan",
		Model:       "qwen3",
		Provider:    "lmstudio",
		Temperature: 0.7,
		Thinking:    true,
	})
	slot := res.(*Model).runtimeSessions["graph:t1:plan"]
	if slot.model != "qwen3" || slot.provider != "lmstudio" {
		t.Errorf("model/provider = %q/%q, want qwen3/lmstudio", slot.model, slot.provider)
	}
	if !slot.hasTemp || slot.temperature != 0.7 || !slot.thinking {
		t.Errorf("temp/thinking = %v/%v/%v, want set/0.7/true", slot.hasTemp, slot.temperature, slot.thinking)
	}

	// Unknown session is a no-op (no panic, no slot created).
	res2, _ := m.Update(SessionMetaMsg{SessionID: "nope", Model: "x"})
	if _, ok := res2.(*Model).runtimeSessions["nope"]; ok {
		t.Error("meta for unknown session should not create a slot")
	}
}
