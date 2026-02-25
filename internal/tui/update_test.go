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
// updatePromptModal tests
// --------------------------------------------------------------------------

func TestUpdatePromptModal_Dismiss(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"esc dismisses", specialKey(tea.KeyEscape)},
		{"p dismisses", keyPress('p')},
		{"q dismisses", keyPress('q')},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.promptModal.show = true
			m.promptModal.content = "some prompt"
			m.promptModal.scroll = 5

			result, cmd := m.updatePromptModal(tt.key)
			got := result.(*Model)

			if got.promptModal.show {
				t.Error("showPromptModal should be false after dismiss")
			}
			if cmd != nil {
				t.Error("expected nil cmd on dismiss")
			}
		})
	}
}

func TestUpdatePromptModal_ScrollDown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"down scrolls down", specialKey(tea.KeyDown)},
		{"j scrolls down", keyPress('j')},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.promptModal.show = true
			m.promptModal.scroll = 3

			result, _ := m.updatePromptModal(tt.key)
			got := result.(*Model)

			if got.promptModal.scroll != 4 {
				t.Errorf("promptModalScroll = %d, want 4", got.promptModal.scroll)
			}
		})
	}
}

func TestUpdatePromptModal_ScrollUp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		key        tea.KeyPressMsg
		startAt    int
		wantScroll int
	}{
		{"up scrolls up from 3", specialKey(tea.KeyUp), 3, 2},
		{"k scrolls up from 3", keyPress('k'), 3, 2},
		{"up at 0 stays at 0", specialKey(tea.KeyUp), 0, 0},
		{"k at 0 stays at 0", keyPress('k'), 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.promptModal.show = true
			m.promptModal.scroll = tt.startAt

			result, _ := m.updatePromptModal(tt.key)
			got := result.(*Model)

			if got.promptModal.scroll != tt.wantScroll {
				t.Errorf("promptModalScroll = %d, want %d", got.promptModal.scroll, tt.wantScroll)
			}
		})
	}
}

func TestUpdatePromptModal_HalfPageScroll(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		key        tea.KeyPressMsg
		startAt    int
		wantScroll int
	}{
		{"ctrl+d scrolls down 10", ctrlKey('d'), 5, 15},
		{"ctrl+u scrolls up 10", ctrlKey('u'), 15, 5},
		{"ctrl+u clamps to 0", ctrlKey('u'), 3, 0},
		{"ctrl+u from 0 stays at 0", ctrlKey('u'), 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.promptModal.show = true
			m.promptModal.scroll = tt.startAt

			result, _ := m.updatePromptModal(tt.key)
			got := result.(*Model)

			if got.promptModal.scroll != tt.wantScroll {
				t.Errorf("promptModalScroll = %d, want %d", got.promptModal.scroll, tt.wantScroll)
			}
		})
	}
}

func TestUpdatePromptModal_UnhandledKey(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.promptModal.show = true
	m.promptModal.scroll = 5

	result, cmd := m.updatePromptModal(keyPress('x'))
	got := result.(*Model)

	// Unhandled key should not change state.
	if got.promptModal.scroll != 5 {
		t.Errorf("promptModalScroll = %d, want 5 (unchanged)", got.promptModal.scroll)
	}
	if !got.promptModal.show {
		t.Error("showPromptModal should remain true for unhandled key")
	}
	if cmd != nil {
		t.Error("expected nil cmd for unhandled key")
	}
}

// --------------------------------------------------------------------------
// updateOutputModal tests
// --------------------------------------------------------------------------

func TestUpdateOutputModal_Dismiss(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"esc dismisses", specialKey(tea.KeyEscape)},
		{"o dismisses", keyPress('o')},
		{"q dismisses", keyPress('q')},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.outputModal.show = true
			m.outputModal.content = "some output"
			m.outputModal.scroll = 5

			result, cmd := m.updateOutputModal(tt.key)
			got := result.(*Model)

			if got.outputModal.show {
				t.Error("showOutputModal should be false after dismiss")
			}
			if cmd != nil {
				t.Error("expected nil cmd on dismiss")
			}
		})
	}
}

func TestUpdateOutputModal_ScrollDown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"down scrolls down", specialKey(tea.KeyDown)},
		{"j scrolls down", keyPress('j')},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.outputModal.show = true
			m.outputModal.scroll = 3

			result, _ := m.updateOutputModal(tt.key)
			got := result.(*Model)

			if got.outputModal.scroll != 4 {
				t.Errorf("outputModalScroll = %d, want 4", got.outputModal.scroll)
			}
		})
	}
}

func TestUpdateOutputModal_ScrollUp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		key        tea.KeyPressMsg
		startAt    int
		wantScroll int
	}{
		{"up scrolls up from 3", specialKey(tea.KeyUp), 3, 2},
		{"k scrolls up from 3", keyPress('k'), 3, 2},
		{"up at 0 stays at 0", specialKey(tea.KeyUp), 0, 0},
		{"k at 0 stays at 0", keyPress('k'), 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.outputModal.show = true
			m.outputModal.scroll = tt.startAt

			result, _ := m.updateOutputModal(tt.key)
			got := result.(*Model)

			if got.outputModal.scroll != tt.wantScroll {
				t.Errorf("outputModalScroll = %d, want %d", got.outputModal.scroll, tt.wantScroll)
			}
		})
	}
}

func TestUpdateOutputModal_HalfPageScroll(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		key        tea.KeyPressMsg
		startAt    int
		wantScroll int
	}{
		{"ctrl+d scrolls down 10", ctrlKey('d'), 5, 15},
		{"ctrl+u scrolls up 10", ctrlKey('u'), 15, 5},
		{"ctrl+u clamps to 0", ctrlKey('u'), 3, 0},
		{"ctrl+u from 0 stays at 0", ctrlKey('u'), 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.outputModal.show = true
			m.outputModal.scroll = tt.startAt

			result, _ := m.updateOutputModal(tt.key)
			got := result.(*Model)

			if got.outputModal.scroll != tt.wantScroll {
				t.Errorf("outputModalScroll = %d, want %d", got.outputModal.scroll, tt.wantScroll)
			}
		})
	}
}

func TestUpdateOutputModal_UnhandledKey(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.outputModal.show = true
	m.outputModal.scroll = 5

	result, cmd := m.updateOutputModal(keyPress('x'))
	got := result.(*Model)

	if got.outputModal.scroll != 5 {
		t.Errorf("outputModalScroll = %d, want 5 (unchanged)", got.outputModal.scroll)
	}
	if !got.outputModal.show {
		t.Error("showOutputModal should remain true for unhandled key")
	}
	if cmd != nil {
		t.Error("expected nil cmd for unhandled key")
	}
}

// --------------------------------------------------------------------------
// updateGrid tests
// --------------------------------------------------------------------------

func TestUpdateGrid_Dismiss(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"ctrl+g dismisses", ctrlKey('g')},
		{"esc dismisses", specialKey(tea.KeyEscape)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.grid.showGrid = true
			m.grid.gridFocusCell = 2
			m.grid.gridPage = 1

			result, cmd := m.updateGrid(tt.key)
			got := result.(*Model)

			if got.grid.showGrid {
				t.Error("showGrid should be false after dismiss")
			}
			if cmd != nil {
				t.Error("expected nil cmd on dismiss")
			}
		})
	}
}

func TestUpdateGrid_ArrowNavigation(t *testing.T) {
	t.Parallel()

	// Grid layout (2x2):
	//   0 | 1
	//   2 | 3
	tests := []struct {
		name      string
		key       tea.KeyPressMsg
		startCell int
		wantCell  int
	}{
		// Left movement
		{"left from cell 1 goes to 0", specialKey(tea.KeyLeft), 1, 0},
		{"left from cell 3 goes to 2", specialKey(tea.KeyLeft), 3, 2},
		{"left from cell 0 stays at 0", specialKey(tea.KeyLeft), 0, 0},
		{"left from cell 2 stays at 2", specialKey(tea.KeyLeft), 2, 2},
		// Right movement
		{"right from cell 0 goes to 1", specialKey(tea.KeyRight), 0, 1},
		{"right from cell 2 goes to 3", specialKey(tea.KeyRight), 2, 3},
		{"right from cell 1 stays at 1", specialKey(tea.KeyRight), 1, 1},
		{"right from cell 3 stays at 3", specialKey(tea.KeyRight), 3, 3},
		// Up movement
		{"up from cell 2 goes to 0", specialKey(tea.KeyUp), 2, 0},
		{"up from cell 3 goes to 1", specialKey(tea.KeyUp), 3, 1},
		{"up from cell 0 stays at 0", specialKey(tea.KeyUp), 0, 0},
		{"up from cell 1 stays at 1", specialKey(tea.KeyUp), 1, 1},
		// Down movement
		{"down from cell 0 goes to 2", specialKey(tea.KeyDown), 0, 2},
		{"down from cell 1 goes to 3", specialKey(tea.KeyDown), 1, 3},
		{"down from cell 2 stays at 2", specialKey(tea.KeyDown), 2, 2},
		{"down from cell 3 stays at 3", specialKey(tea.KeyDown), 3, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.grid.showGrid = true
			m.grid.gridFocusCell = tt.startCell

			result, _ := m.updateGrid(tt.key)
			got := result.(*Model)

			if got.grid.gridFocusCell != tt.wantCell {
				t.Errorf("gridFocusCell = %d, want %d", got.grid.gridFocusCell, tt.wantCell)
			}
			if !got.grid.showGrid {
				t.Error("showGrid should remain true during navigation")
			}
		})
	}
}

func TestUpdateGrid_PageNavigation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		key       tea.KeyPressMsg
		startPage int
		startCell int
		wantPage  int
		wantCell  int
	}{
		{"[ from page 1 goes to page 0", keyPress('['), 1, 2, 0, 0},
		{"[ from page 0 stays at page 0", keyPress('['), 0, 3, 0, 0},
		{"[ from page 3 goes to page 2", keyPress('['), 3, 1, 2, 0},
		{"] from page 0 goes to page 1", keyPress(']'), 0, 2, 1, 0},
		{"] from page 3 stays at page 3", keyPress(']'), 3, 1, 3, 0},
		{"] from page 2 goes to page 3", keyPress(']'), 2, 3, 3, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.grid.showGrid = true
			m.grid.gridPage = tt.startPage
			m.grid.gridFocusCell = tt.startCell

			result, _ := m.updateGrid(tt.key)
			got := result.(*Model)

			if got.grid.gridPage != tt.wantPage {
				t.Errorf("gridPage = %d, want %d", got.grid.gridPage, tt.wantPage)
			}
			if got.grid.gridFocusCell != tt.wantCell {
				t.Errorf("gridFocusCell = %d, want %d (should reset on page change)", got.grid.gridFocusCell, tt.wantCell)
			}
		})
	}
}

func TestUpdateGrid_EnterWithNilGateway(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.grid.showGrid = true
	m.gateway = nil
	m.grid.gridFocusCell = 0

	result, cmd := m.updateGrid(specialKey(tea.KeyEnter))
	got := result.(*Model)

	// Should not panic with nil gateway, and should not open output modal.
	if got.outputModal.show {
		t.Error("showOutputModal should be false when gateway is nil")
	}
	if cmd != nil {
		t.Error("expected nil cmd when gateway is nil")
	}
}

func TestUpdateGrid_PromptWithNilGateway(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.grid.showGrid = true
	m.gateway = nil
	m.grid.gridFocusCell = 0

	result, cmd := m.updateGrid(keyPress('p'))
	got := result.(*Model)

	// Should not panic with nil gateway, and should not open prompt modal.
	if got.promptModal.show {
		t.Error("showPromptModal should be false when gateway is nil")
	}
	if cmd != nil {
		t.Error("expected nil cmd when gateway is nil")
	}
}

func TestUpdateGrid_KillWithNilGateway(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.grid.showGrid = true
	m.gateway = nil
	m.grid.gridFocusCell = 0

	// 'k' and 'ctrl+k' are kill keys in grid mode.
	result, cmd := m.updateGrid(keyPress('k'))
	got := result.(*Model)

	// Should not panic with nil gateway.
	if !got.grid.showGrid {
		t.Error("showGrid should remain true after kill with nil gateway")
	}
	if cmd != nil {
		t.Error("expected nil cmd when gateway is nil")
	}
}

func TestUpdateGrid_UnhandledKey(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.grid.showGrid = true
	m.grid.gridFocusCell = 1
	m.grid.gridPage = 2

	result, cmd := m.updateGrid(keyPress('x'))
	got := result.(*Model)

	if got.grid.gridFocusCell != 1 {
		t.Errorf("gridFocusCell = %d, want 1 (unchanged)", got.grid.gridFocusCell)
	}
	if got.grid.gridPage != 2 {
		t.Errorf("gridPage = %d, want 2 (unchanged)", got.grid.gridPage)
	}
	if !got.grid.showGrid {
		t.Error("showGrid should remain true for unhandled key")
	}
	if cmd != nil {
		t.Error("expected nil cmd for unhandled key")
	}
}

// --------------------------------------------------------------------------
// updateKillModal tests
// --------------------------------------------------------------------------

func TestUpdateKillModal_Dismiss(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.killModal.show = true
	m.killModal.slots = []int{0, 2}
	m.killModal.selectedIdx = 1

	result, cmd := m.updateKillModal(specialKey(tea.KeyEscape))
	got := result.(*Model)

	if got.killModal.show {
		t.Error("showKillModal should be false after esc")
	}
	if cmd != nil {
		t.Error("expected nil cmd on dismiss")
	}
}

func TestUpdateKillModal_Navigation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		key      tea.KeyPressMsg
		slots    []int
		startIdx int
		wantIdx  int
	}{
		// Down wraps around.
		{"down from 0 of 3", specialKey(tea.KeyDown), []int{0, 1, 2}, 0, 1},
		{"down from 2 of 3 wraps to 0", specialKey(tea.KeyDown), []int{0, 1, 2}, 2, 0},
		{"down from 0 of 1 wraps to 0", specialKey(tea.KeyDown), []int{5}, 0, 0},
		// Up wraps around.
		{"up from 1 of 3", specialKey(tea.KeyUp), []int{0, 1, 2}, 1, 0},
		{"up from 0 of 3 wraps to 2", specialKey(tea.KeyUp), []int{0, 1, 2}, 0, 2},
		{"up from 0 of 1 wraps to 0", specialKey(tea.KeyUp), []int{5}, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.killModal.show = true
			m.killModal.slots = tt.slots
			m.killModal.selectedIdx = tt.startIdx

			result, _ := m.updateKillModal(tt.key)
			got := result.(*Model)

			if got.killModal.selectedIdx != tt.wantIdx {
				t.Errorf("selectedKillIdx = %d, want %d", got.killModal.selectedIdx, tt.wantIdx)
			}
			if !got.killModal.show {
				t.Error("showKillModal should remain true during navigation")
			}
		})
	}
}

func TestUpdateKillModal_NavigationEmptySlots(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"up with empty slots", specialKey(tea.KeyUp)},
		{"down with empty slots", specialKey(tea.KeyDown)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.killModal.show = true
			m.killModal.slots = nil
			m.killModal.selectedIdx = 0

			result, _ := m.updateKillModal(tt.key)
			got := result.(*Model)

			if got.killModal.selectedIdx != 0 {
				t.Errorf("selectedKillIdx = %d, want 0 (unchanged for empty slots)", got.killModal.selectedIdx)
			}
		})
	}
}

func TestUpdateKillModal_EnterDismisses(t *testing.T) {
	t.Parallel()

	// With nil gateway, enter should still dismiss the modal.
	m := newMinimalModel(t)
	m.killModal.show = true
	m.killModal.slots = []int{0, 2}
	m.killModal.selectedIdx = 1
	m.gateway = nil

	result, cmd := m.updateKillModal(specialKey(tea.KeyEnter))
	got := result.(*Model)

	if got.killModal.show {
		t.Error("showKillModal should be false after enter")
	}
	if cmd != nil {
		t.Error("expected nil cmd when gateway is nil")
	}
}

func TestUpdateKillModal_EnterWithEmptySlots(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.killModal.show = true
	m.killModal.slots = nil
	m.killModal.selectedIdx = 0
	m.gateway = nil

	result, _ := m.updateKillModal(specialKey(tea.KeyEnter))
	got := result.(*Model)

	// Should dismiss even with empty slots.
	if got.killModal.show {
		t.Error("showKillModal should be false after enter with empty slots")
	}
}

func TestUpdateKillModal_UnhandledKey(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.killModal.show = true
	m.killModal.slots = []int{0, 1}
	m.killModal.selectedIdx = 0

	result, cmd := m.updateKillModal(keyPress('x'))
	got := result.(*Model)

	if !got.killModal.show {
		t.Error("showKillModal should remain true for unhandled key")
	}
	if got.killModal.selectedIdx != 0 {
		t.Errorf("selectedKillIdx = %d, want 0 (unchanged)", got.killModal.selectedIdx)
	}
	if cmd != nil {
		t.Error("expected nil cmd for unhandled key")
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
	// Input should be set to the selected command name + space.
	if m.input.Value() != "/exit " {
		t.Errorf("input value = %q, want %q", m.input.Value(), "/exit ")
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
