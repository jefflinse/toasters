package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/service"
)

// --------------------------------------------------------------------------
// Mock service for reloadTeamsCmd / DefinitionsReloadedMsg tests
// --------------------------------------------------------------------------

// mockDefService implements service.DefinitionService for tests.
// Only ListTeams is functional; all other methods will panic if called.
type mockDefService struct {
	service.DefinitionService // embed to satisfy interface; nil is fine since only ListTeams is used
	listTeamsFn               func(ctx context.Context) ([]service.TeamView, error)
}

func (m *mockDefService) ListTeams(ctx context.Context) ([]service.TeamView, error) {
	return m.listTeamsFn(ctx)
}

// mockSvcForTUI implements service.Service for TUI tests.
// Only Definitions() is functional; other sub-interfaces will panic if called.
type mockSvcForTUI struct {
	defs *mockDefService
}

func (m *mockSvcForTUI) Operator() service.OperatorService      { panic("not implemented") }
func (m *mockSvcForTUI) Definitions() service.DefinitionService { return m.defs }
func (m *mockSvcForTUI) Jobs() service.JobService               { panic("not implemented") }
func (m *mockSvcForTUI) Sessions() service.SessionService       { panic("not implemented") }
func (m *mockSvcForTUI) Events() service.EventService           { panic("not implemented") }
func (m *mockSvcForTUI) System() service.SystemService          { panic("not implemented") }

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

func TestUpdateGrid_EnterWithNoSession(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.grid.showGrid = true
	m.grid.gridFocusCell = 0

	result, cmd := m.updateGrid(specialKey(tea.KeyEnter))
	got := result.(*Model)

	// Should not panic with no session, and should not open output modal.
	if got.outputModal.show {
		t.Error("showOutputModal should be false when no session is focused")
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

	// Should not panic with no session, and should not open prompt modal.
	if got.promptModal.show {
		t.Error("showPromptModal should be false when no session is focused")
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

// --------------------------------------------------------------------------
// reloadTeamsCmd tests
// --------------------------------------------------------------------------

func TestReloadTeamsCmd_Success(t *testing.T) {
	t.Parallel()

	wantTeams := []service.TeamView{
		{Team: service.Team{Name: "alpha"}},
		{Team: service.Team{Name: "beta"}},
	}

	svc := &mockSvcForTUI{
		defs: &mockDefService{
			listTeamsFn: func(_ context.Context) ([]service.TeamView, error) {
				return wantTeams, nil
			},
		},
	}

	cmd := reloadTeamsCmd(svc)
	if cmd == nil {
		t.Fatal("reloadTeamsCmd should return a non-nil tea.Cmd")
	}

	msg := cmd()
	got, ok := msg.(TeamsReloadedMsg)
	if !ok {
		t.Fatalf("expected TeamsReloadedMsg, got %T", msg)
	}
	if len(got.Teams) != len(wantTeams) {
		t.Fatalf("expected %d teams, got %d", len(wantTeams), len(got.Teams))
	}
	for i, want := range wantTeams {
		if got.Teams[i].Team.Name != want.Team.Name {
			t.Errorf("team[%d]: got name %q, want %q", i, got.Teams[i].Team.Name, want.Team.Name)
		}
	}
}

func TestReloadTeamsCmd_Error(t *testing.T) {
	t.Parallel()

	svc := &mockSvcForTUI{
		defs: &mockDefService{
			listTeamsFn: func(_ context.Context) ([]service.TeamView, error) {
				return nil, context.DeadlineExceeded
			},
		},
	}

	cmd := reloadTeamsCmd(svc)
	if cmd == nil {
		t.Fatal("reloadTeamsCmd should return a non-nil tea.Cmd even when ListTeams will fail")
	}

	msg := cmd()
	if msg != nil {
		t.Fatalf("expected nil msg on error, got %T: %v", msg, msg)
	}
}

// --------------------------------------------------------------------------
// DefinitionsReloadedMsg handler tests
// --------------------------------------------------------------------------

func TestDefinitionsReloadedMsg_ReturnsCmd(t *testing.T) {
	t.Parallel()

	wantTeams := []service.TeamView{
		{Team: service.Team{Name: "gamma"}},
	}

	svc := &mockSvcForTUI{
		defs: &mockDefService{
			listTeamsFn: func(_ context.Context) ([]service.TeamView, error) {
				return wantTeams, nil
			},
		},
	}

	m := newMinimalModel(t)
	m.svc = svc

	_, cmd := m.Update(DefinitionsReloadedMsg{})
	if cmd == nil {
		t.Fatal("DefinitionsReloadedMsg should produce a non-nil tea.Cmd (reloadTeamsCmd)")
	}

	// Execute the returned cmd — it should produce a TeamsReloadedMsg.
	msg := cmd()
	got, ok := msg.(TeamsReloadedMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want TeamsReloadedMsg", msg)
	}
	if len(got.Teams) != 1 || got.Teams[0].Team.Name != "gamma" {
		t.Errorf("unexpected teams in TeamsReloadedMsg: %+v", got.Teams)
	}
}
