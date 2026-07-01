package tui

import (
	"fmt"
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
// updateCockpit tests
// --------------------------------------------------------------------------

func TestUpdateCockpit_Dismiss(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"esc dismisses", specialKey(tea.KeyEscape)},
		{"q dismisses", keyPress('q')},
		{"ctrl+g dismisses", ctrlKey('g')},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.cockpit.show = true
			m.cockpit.sessionID = "sess-1"

			result, cmd := m.updateCockpit(tt.key)
			got := result.(*Model)

			if got.cockpit.show {
				t.Error("cockpit.show should be false after dismiss")
			}
			if got.cockpit.sessionID != "" {
				t.Error("cockpit.sessionID should be cleared on dismiss")
			}
			if cmd != nil {
				t.Error("expected nil cmd on dismiss")
			}
		})
	}
}

func TestUpdateCockpit_TabSwitch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		start cockpitTab
		key   tea.KeyPressMsg
		want  cockpitTab
	}{
		{"tab cycles forward", cockpitTabOutput, specialKey(tea.KeyTab), cockpitTabPrompt},
		{"tab wraps", cockpitTabStats, specialKey(tea.KeyTab), cockpitTabOutput},
		{"right cycles forward", cockpitTabOutput, keyPress('l'), cockpitTabPrompt},
		{"left cycles back", cockpitTabPrompt, keyPress('h'), cockpitTabOutput},
		{"left wraps", cockpitTabOutput, keyPress('h'), cockpitTabStats},
		{"1 selects output", cockpitTabStats, keyPress('1'), cockpitTabOutput},
		{"2 selects prompt", cockpitTabOutput, keyPress('2'), cockpitTabPrompt},
		{"3 selects stats", cockpitTabOutput, keyPress('3'), cockpitTabStats},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.cockpit.show = true
			m.cockpit.tab = tt.start

			result, _ := m.updateCockpit(tt.key)
			got := result.(*Model)

			if got.cockpit.tab != tt.want {
				t.Errorf("tab = %d, want %d", got.cockpit.tab, tt.want)
			}
		})
	}
}

func TestUpdateCockpit_Scroll(t *testing.T) {
	t.Parallel()

	t.Run("down increments active tab scroll", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.cockpit.show = true
		m.cockpit.tab = cockpitTabPrompt
		m.cockpit.scroll[cockpitTabPrompt] = 3

		result, _ := m.updateCockpit(keyPress('j'))
		got := result.(*Model)

		if got.cockpit.scroll[cockpitTabPrompt] != 4 {
			t.Errorf("scroll = %d, want 4", got.cockpit.scroll[cockpitTabPrompt])
		}
	})

	t.Run("up on output sets userScrolled", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.cockpit.show = true
		m.cockpit.tab = cockpitTabOutput
		m.cockpit.scroll[cockpitTabOutput] = 5

		result, _ := m.updateCockpit(keyPress('k'))
		got := result.(*Model)

		if got.cockpit.scroll[cockpitTabOutput] != 4 {
			t.Errorf("scroll = %d, want 4", got.cockpit.scroll[cockpitTabOutput])
		}
		if !got.cockpit.userScrolled {
			t.Error("expected userScrolled to be set after scrolling up on the Output tab")
		}
	})

	t.Run("G re-enables output auto-tail", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.cockpit.show = true
		m.cockpit.tab = cockpitTabOutput
		m.cockpit.userScrolled = true

		result, _ := m.updateCockpit(keyPress('G'))
		got := result.(*Model)

		if got.cockpit.userScrolled {
			t.Error("expected userScrolled to be cleared after jumping to bottom")
		}
		if got.cockpit.scroll[cockpitTabOutput] != scrollBottom {
			t.Errorf("scroll = %d, want scrollBottom", got.cockpit.scroll[cockpitTabOutput])
		}
	})

	t.Run("scroll does not go below zero", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.cockpit.show = true
		m.cockpit.tab = cockpitTabStats
		m.cockpit.scroll[cockpitTabStats] = 0

		result, _ := m.updateCockpit(keyPress('k'))
		got := result.(*Model)

		if got.cockpit.scroll[cockpitTabStats] != 0 {
			t.Errorf("scroll = %d, want 0 (clamped)", got.cockpit.scroll[cockpitTabStats])
		}
	})
}

func TestUpdateCockpit_UnhandledKey(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.cockpit.show = true
	m.cockpit.tab = cockpitTabOutput
	m.cockpit.scroll[cockpitTabOutput] = 5

	result, cmd := m.updateCockpit(keyPress('z'))
	got := result.(*Model)

	if !got.cockpit.show {
		t.Error("cockpit.show should remain true for unhandled key")
	}
	if got.cockpit.scroll[cockpitTabOutput] != 5 {
		t.Errorf("scroll = %d, want 5 (unchanged)", got.cockpit.scroll[cockpitTabOutput])
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
			m.grid.gridCols = 2
			m.grid.gridRows = 2
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
		{"[ from page 0 (first page) is a no-op — cell unchanged", keyPress('['), 0, 3, 0, 3},
		{"[ from page 3 goes to page 2", keyPress('['), 3, 1, 2, 0},
		{"] from page 0 goes to page 1", keyPress(']'), 0, 2, 1, 0},
		{"] from page 3 (last page) is a no-op — cell unchanged", keyPress(']'), 3, 1, 3, 1},
		{"] from page 2 goes to page 3", keyPress(']'), 2, 3, 3, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.grid.showGrid = true
			m.grid.gridCols = 2
			m.grid.gridRows = 2
			// Seed 16 sessions → 4 pages at 4 cells/page, so pages 0..3 exist
			// and page 3 is genuinely the last (the total-pages count is now
			// derived from the live session count, not a fixed ceiling).
			for i := 0; i < 16; i++ {
				id := fmt.Sprintf("sess-%02d", i)
				m.runtimeSessions[id] = &runtimeSlot{sessionID: id, status: "active"}
			}
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

func TestUpdateGrid_Filter(t *testing.T) {
	t.Parallel()

	newGrid := func() *Model {
		m := newMinimalModel(t)
		m.grid.showGrid = true
		m.grid.gridCols = 2
		m.grid.gridRows = 2
		m.runtimeSessions["s1"] = &runtimeSlot{sessionID: "s1", jobID: "alpha", workerName: "coder", status: "active"}
		m.runtimeSessions["s2"] = &runtimeSlot{sessionID: "s2", jobID: "beta", workerName: "tester", status: "active"}
		m.runtimeSessions["s3"] = &runtimeSlot{sessionID: "s3", jobID: "alpha", workerName: "reviewer", status: "completed"}
		return &m
	}

	t.Run("/ enters filter capture", func(t *testing.T) {
		t.Parallel()
		m := newGrid()
		res, _ := m.updateGrid(keyPress('/'))
		if !res.(*Model).grid.filterActive {
			t.Fatal("filterActive should be true after '/'")
		}
	})

	t.Run("typing narrows by job id and resets page", func(t *testing.T) {
		t.Parallel()
		m := newGrid()
		m.grid.gridPage = 1
		m.grid.filterActive = true
		for _, r := range "beta" {
			res, _ := m.updateGrid(keyPress(r))
			m = res.(*Model)
		}
		if got := m.filteredGridSessions(); len(got) != 1 || got[0].jobID != "beta" {
			t.Fatalf("filtered = %d sessions, want 1 (beta)", len(got))
		}
		if m.grid.gridPage != 0 {
			t.Errorf("gridPage = %d, want 0 (reset on query change)", m.grid.gridPage)
		}
	})

	t.Run("esc clears the filter", func(t *testing.T) {
		t.Parallel()
		m := newGrid()
		m.grid.filterActive = true
		m.grid.filterQuery = "beta"
		res, _ := m.updateGrid(specialKey(tea.KeyEscape))
		got := res.(*Model)
		if got.grid.filterActive || got.grid.filterQuery != "" {
			t.Error("esc should clear filter state")
		}
		if len(got.filteredGridSessions()) != 3 {
			t.Errorf("after clear, expected all 3 sessions")
		}
	})

	t.Run("enter applies and exits capture", func(t *testing.T) {
		t.Parallel()
		m := newGrid()
		m.grid.filterActive = true
		m.grid.filterQuery = "alpha"
		res, _ := m.updateGrid(specialKey(tea.KeyEnter))
		got := res.(*Model)
		if got.grid.filterActive {
			t.Error("enter should exit capture")
		}
		if got.grid.filterQuery != "alpha" {
			t.Error("enter should keep the applied query")
		}
		if len(got.filteredGridSessions()) != 2 {
			t.Errorf("expected 2 alpha sessions, got %d", len(got.filteredGridSessions()))
		}
	})
}

func TestUpdateGrid_EnterWithNoSession(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.grid.showGrid = true
	m.grid.gridFocusCell = 0

	result, cmd := m.updateGrid(specialKey(tea.KeyEnter))
	got := result.(*Model)

	// Should not panic with no session, and should not open the cockpit.
	if got.cockpit.show {
		t.Error("cockpit should not open when no session is focused")
	}
	if cmd != nil {
		t.Error("expected nil cmd when no session is focused")
	}
}

func TestUpdateGrid_PromptWithNoSession(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.grid.showGrid = true
	m.grid.gridFocusCell = 0

	result, cmd := m.updateGrid(keyPress('p'))
	got := result.(*Model)

	// Should not panic with no session, and should not open the cockpit.
	if got.cockpit.show {
		t.Error("cockpit should not open when no session is focused")
	}
	if cmd != nil {
		t.Error("expected nil cmd when no session is focused")
	}
}

func TestUpdateGrid_UnhandledKillKey(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.grid.showGrid = true
	m.grid.gridFocusCell = 0

	// 'k' is not a grid navigation key.
	result, cmd := m.updateGrid(keyPress('k'))
	got := result.(*Model)

	// 'k' should be an unhandled key — grid stays open, no cmd.
	if !got.grid.showGrid {
		t.Error("showGrid should remain true for unhandled key")
	}
	if cmd != nil {
		t.Error("expected nil cmd for unhandled key")
	}
}

func TestUpdateGrid_ArrowNavigationDynamic(t *testing.T) {
	t.Parallel()

	// Each sub-group exercises a different grid shape.
	// The cell layout for a cols×rows grid is row-major:
	//   cell index = row*cols + col
	//
	// 3×2 grid (3 cols, 2 rows):
	//   [0][1][2]
	//   [3][4][5]
	//
	// 2×3 grid (2 cols, 3 rows):
	//   [0][1]
	//   [2][3]
	//   [4][5]
	//
	// 4×4 grid (4 cols, 4 rows):
	//   [ 0][ 1][ 2][ 3]
	//   [ 4][ 5][ 6][ 7]
	//   [ 8][ 9][10][11]
	//   [12][13][14][15]

	type navCase struct {
		name      string
		key       tea.KeyPressMsg
		startCell int
		wantCell  int
	}

	gridTests := []struct {
		gridName string
		cols     int
		rows     int
		cases    []navCase
	}{
		{
			gridName: "2x2",
			cols:     2,
			rows:     2,
			// Layout:
			//   [0][1]
			//   [2][3]
			cases: []navCase{
				// left
				{"left from 0 stays at 0 (left edge)", specialKey(tea.KeyLeft), 0, 0},
				{"left from 1 goes to 0", specialKey(tea.KeyLeft), 1, 0},
				{"left from 2 stays at 2 (left edge)", specialKey(tea.KeyLeft), 2, 2},
				{"left from 3 goes to 2", specialKey(tea.KeyLeft), 3, 2},
				// right
				{"right from 0 goes to 1", specialKey(tea.KeyRight), 0, 1},
				{"right from 1 stays at 1 (right edge)", specialKey(tea.KeyRight), 1, 1},
				{"right from 2 goes to 3", specialKey(tea.KeyRight), 2, 3},
				{"right from 3 stays at 3 (right edge)", specialKey(tea.KeyRight), 3, 3},
				// up
				{"up from 0 stays at 0 (top row)", specialKey(tea.KeyUp), 0, 0},
				{"up from 1 stays at 1 (top row)", specialKey(tea.KeyUp), 1, 1},
				{"up from 2 goes to 0", specialKey(tea.KeyUp), 2, 0},
				{"up from 3 goes to 1", specialKey(tea.KeyUp), 3, 1},
				// down
				{"down from 0 goes to 2", specialKey(tea.KeyDown), 0, 2},
				{"down from 1 goes to 3", specialKey(tea.KeyDown), 1, 3},
				{"down from 2 stays at 2 (bottom row)", specialKey(tea.KeyDown), 2, 2},
				{"down from 3 stays at 3 (bottom row)", specialKey(tea.KeyDown), 3, 3},
			},
		},
		{
			gridName: "3x2",
			cols:     3,
			rows:     2,
			// Layout:
			//   [0][1][2]
			//   [3][4][5]
			cases: []navCase{
				// left
				{"left from 0 stays at 0 (left edge)", specialKey(tea.KeyLeft), 0, 0},
				{"left from 1 goes to 0", specialKey(tea.KeyLeft), 1, 0},
				{"left from 3 stays at 3 (left edge)", specialKey(tea.KeyLeft), 3, 3},
				// right
				{"right from 2 stays at 2 (right edge)", specialKey(tea.KeyRight), 2, 2},
				{"right from 1 goes to 2", specialKey(tea.KeyRight), 1, 2},
				{"right from 5 stays at 5 (right edge)", specialKey(tea.KeyRight), 5, 5},
				// up
				{"up from 0 stays at 0 (top row)", specialKey(tea.KeyUp), 0, 0},
				{"up from 3 goes to 0", specialKey(tea.KeyUp), 3, 0},
				{"up from 4 goes to 1", specialKey(tea.KeyUp), 4, 1},
				// down
				{"down from 3 stays at 3 (bottom row)", specialKey(tea.KeyDown), 3, 3},
				{"down from 0 goes to 3", specialKey(tea.KeyDown), 0, 3},
				{"down from 2 goes to 5", specialKey(tea.KeyDown), 2, 5},
			},
		},
		{
			gridName: "2x3",
			cols:     2,
			rows:     3,
			// Layout:
			//   [0][1]
			//   [2][3]
			//   [4][5]
			cases: []navCase{
				// left
				{"left from 0 stays at 0 (left edge)", specialKey(tea.KeyLeft), 0, 0},
				{"left from 1 goes to 0", specialKey(tea.KeyLeft), 1, 0},
				// right
				{"right from 1 stays at 1 (right edge)", specialKey(tea.KeyRight), 1, 1},
				// up
				{"up from 0 stays at 0 (top row)", specialKey(tea.KeyUp), 0, 0},
				{"up from 2 goes to 0", specialKey(tea.KeyUp), 2, 0},
				{"up from 4 goes to 2", specialKey(tea.KeyUp), 4, 2},
				// down
				{"down from 4 stays at 4 (bottom-left corner)", specialKey(tea.KeyDown), 4, 4},
				{"down from 5 stays at 5 (bottom-right corner)", specialKey(tea.KeyDown), 5, 5},
				{"down from 0 goes to 2", specialKey(tea.KeyDown), 0, 2},
				{"down from 2 goes to 4", specialKey(tea.KeyDown), 2, 4},
			},
		},
		{
			gridName: "4x4",
			cols:     4,
			rows:     4,
			// Layout:
			//   [ 0][ 1][ 2][ 3]
			//   [ 4][ 5][ 6][ 7]
			//   [ 8][ 9][10][11]
			//   [12][13][14][15]
			cases: []navCase{
				// left edge
				{"left from 0 stays at 0", specialKey(tea.KeyLeft), 0, 0},
				{"left from 4 stays at 4", specialKey(tea.KeyLeft), 4, 4},
				{"left from 12 stays at 12", specialKey(tea.KeyLeft), 12, 12},
				// right edge
				{"right from 3 stays at 3", specialKey(tea.KeyRight), 3, 3},
				{"right from 7 stays at 7", specialKey(tea.KeyRight), 7, 7},
				{"right from 15 stays at 15", specialKey(tea.KeyRight), 15, 15},
				// top row
				{"up from 0 stays at 0", specialKey(tea.KeyUp), 0, 0},
				{"up from 3 stays at 3", specialKey(tea.KeyUp), 3, 3},
				// bottom row
				{"down from 12 stays at 12", specialKey(tea.KeyDown), 12, 12},
				{"down from 15 stays at 15", specialKey(tea.KeyDown), 15, 15},
				// interior moves
				{"left from 5 goes to 4", specialKey(tea.KeyLeft), 5, 4},
				{"right from 5 goes to 6", specialKey(tea.KeyRight), 5, 6},
				{"up from 5 goes to 1", specialKey(tea.KeyUp), 5, 1},
				{"down from 5 goes to 9", specialKey(tea.KeyDown), 5, 9},
				// corner-to-corner
				{"down from 0 goes to 4", specialKey(tea.KeyDown), 0, 4},
				{"right from 0 goes to 1", specialKey(tea.KeyRight), 0, 1},
				{"up from 15 goes to 11", specialKey(tea.KeyUp), 15, 11},
				{"left from 15 goes to 14", specialKey(tea.KeyLeft), 15, 14},
			},
		},
	}

	for _, gt := range gridTests {
		gt := gt // capture range variable
		t.Run(gt.gridName, func(t *testing.T) {
			t.Parallel()
			for _, tc := range gt.cases {
				tc := tc // capture range variable
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					m := newMinimalModel(t)
					m.grid.showGrid = true
					m.grid.gridCols = gt.cols
					m.grid.gridRows = gt.rows
					m.grid.gridFocusCell = tc.startCell

					result, _ := m.updateGrid(tc.key)
					got := result.(*Model)

					if got.grid.gridFocusCell != tc.wantCell {
						t.Errorf("grid %dx%d: key=%s from cell %d: gridFocusCell = %d, want %d",
							gt.cols, gt.rows, tc.key.String(), tc.startCell,
							got.grid.gridFocusCell, tc.wantCell)
					}
					if !got.grid.showGrid {
						t.Error("showGrid should remain true during navigation")
					}
				})
			}
		})
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

func TestUpdateGrid_KillConfirmation(t *testing.T) {
	t.Parallel()

	setup := func(status, sessionID string) *Model {
		m := newMinimalModel(t)
		m.grid.showGrid = true
		m.grid.gridCols = 1
		m.grid.gridRows = 1
		m.grid.gridFocusCell = 0
		m.runtimeSessions[sessionID] = &runtimeSlot{
			sessionID:  sessionID,
			workerName: "builder",
			status:     status,
		}
		return &m
	}

	t.Run("x arms confirmation for an active worker", func(t *testing.T) {
		t.Parallel()
		m := setup("active", "sess-1")
		res, _ := m.updateGrid(keyPress('x'))
		got := res.(*Model)
		if !got.grid.confirmKill {
			t.Fatal("confirmKill should be set")
		}
		if got.grid.confirmKillSessionID != "sess-1" {
			t.Errorf("confirmKillSessionID = %q, want sess-1", got.grid.confirmKillSessionID)
		}
	})

	t.Run("x is a no-op for a completed worker", func(t *testing.T) {
		t.Parallel()
		m := setup("completed", "sess-2")
		res, _ := m.updateGrid(keyPress('x'))
		if res.(*Model).grid.confirmKill {
			t.Error("confirmKill should not arm for a completed worker")
		}
	})

	t.Run("x is a no-op for a graph pseudo-session", func(t *testing.T) {
		t.Parallel()
		m := setup("active", "graph:task-1:plan")
		res, _ := m.updateGrid(keyPress('x'))
		if res.(*Model).grid.confirmKill {
			t.Error("confirmKill should not arm for a graph node")
		}
	})

	t.Run("esc clears a pending confirmation without closing the grid", func(t *testing.T) {
		t.Parallel()
		m := setup("active", "sess-3")
		m.grid.confirmKill = true
		m.grid.confirmKillSessionID = "sess-3"
		res, _ := m.updateGrid(specialKey(tea.KeyEscape))
		got := res.(*Model)
		if got.grid.confirmKill {
			t.Error("confirmKill should be cleared")
		}
		if !got.grid.showGrid {
			t.Error("grid should stay open when only dismissing the confirmation")
		}
	})
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
