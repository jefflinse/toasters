package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// --------------------------------------------------------------------------
// updatePromptMode tests
// --------------------------------------------------------------------------

func TestUpdatePromptMode_NavigateDown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		key           tea.KeyPressMsg
		options       []string
		startSelected int
		wantSelected  int
	}{
		{"down from 0 with 2 options", specialKey(tea.KeyDown), []string{"A", "B"}, 0, 1},
		{"j from 0 with 2 options", keyPress('j'), []string{"A", "B"}, 0, 1},
		{"down from 1 with 2 options moves to Custom", specialKey(tea.KeyDown), []string{"A", "B"}, 1, 2},
		{"down at last option (Custom) stays", specialKey(tea.KeyDown), []string{"A", "B"}, 2, 2},
		{"down from 0 with 0 options moves to Custom", specialKey(tea.KeyDown), []string{}, 0, 0},
		{"j at last option stays", keyPress('j'), []string{"A"}, 1, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.prompt.promptMode = true
			m.prompt.promptOptions = tt.options
			m.prompt.promptSelected = tt.startSelected

			result, _ := m.updatePromptMode(tt.key)
			got := result.(*Model)

			if got.prompt.promptSelected != tt.wantSelected {
				t.Errorf("promptSelected = %d, want %d", got.prompt.promptSelected, tt.wantSelected)
			}
		})
	}
}

func TestUpdatePromptMode_NavigateUp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		key           tea.KeyPressMsg
		options       []string
		startSelected int
		wantSelected  int
	}{
		{"up from 1 with 2 options", specialKey(tea.KeyUp), []string{"A", "B"}, 1, 0},
		{"k from 2 with 2 options", keyPress('k'), []string{"A", "B"}, 2, 1},
		{"up from 0 stays at 0", specialKey(tea.KeyUp), []string{"A", "B"}, 0, 0},
		{"k from 0 stays at 0", keyPress('k'), []string{"A"}, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.prompt.promptMode = true
			m.prompt.promptOptions = tt.options
			m.prompt.promptSelected = tt.startSelected

			result, _ := m.updatePromptMode(tt.key)
			got := result.(*Model)

			if got.prompt.promptSelected != tt.wantSelected {
				t.Errorf("promptSelected = %d, want %d", got.prompt.promptSelected, tt.wantSelected)
			}
		})
	}
}

func TestUpdatePromptMode_SelectPredefinedOption(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.prompt.promptMode = true
	m.prompt.promptOptions = []string{"Option A", "Option B", "Option C"}
	m.prompt.promptSelected = 1 // "Option B"

	result, _ := m.updatePromptMode(specialKey(tea.KeyEnter))
	got := result.(*Model)

	// promptMode should be cleared after selecting an option.
	if got.prompt.promptMode {
		t.Error("promptMode should be false after selecting a predefined option")
	}
	if got.prompt.promptCustom {
		t.Error("promptCustom should be false after selecting a predefined option")
	}
}

func TestUpdatePromptMode_SelectCustomResponseOption(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.prompt.promptMode = true
	m.prompt.promptOptions = []string{"Option A", "Option B"}
	m.prompt.promptSelected = 2 // "Custom response..." (appended automatically)

	result, cmd := m.updatePromptMode(specialKey(tea.KeyEnter))
	got := result.(*Model)

	if !got.prompt.promptCustom {
		t.Error("promptCustom should be true after selecting 'Custom response...'")
	}
	// The cmd should be a focus command for the input, not nil.
	if cmd == nil {
		t.Error("expected non-nil cmd (input focus) after selecting custom response")
	}
}

func TestUpdatePromptMode_SubmitCustomText(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.prompt.promptMode = true
	m.prompt.promptCustom = true
	m.prompt.promptOptions = []string{"Option A"}
	m.input.SetValue("My custom answer")

	result, _ := m.updatePromptMode(specialKey(tea.KeyEnter))
	got := result.(*Model)

	// promptMode should be cleared after submitting custom text.
	if got.prompt.promptMode {
		t.Error("promptMode should be false after submitting custom text")
	}
	if got.prompt.promptCustom {
		t.Error("promptCustom should be false after submitting custom text")
	}
}

func TestUpdatePromptMode_SubmitEmptyCustomText(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.prompt.promptMode = true
	m.prompt.promptCustom = true
	m.prompt.promptOptions = []string{"Option A"}
	m.input.SetValue("   ") // whitespace only

	result, _ := m.updatePromptMode(specialKey(tea.KeyEnter))
	got := result.(*Model)

	// promptMode should be cleared.
	if got.prompt.promptMode {
		t.Error("promptMode should be false after submitting empty custom text")
	}
}

func TestUpdatePromptMode_EscCancelFromOptionSelection(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.prompt.promptMode = true
	m.prompt.promptCustom = false
	m.prompt.promptOptions = []string{"Option A", "Option B"}

	result, _ := m.updatePromptMode(specialKey(tea.KeyEscape))
	got := result.(*Model)

	// promptMode should be cleared after esc cancellation.
	if got.prompt.promptMode {
		t.Error("promptMode should be false after esc cancellation")
	}
}

func TestUpdatePromptMode_EscFromCustomGoesBackToOptions(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.prompt.promptMode = true
	m.prompt.promptCustom = true
	m.prompt.promptOptions = []string{"Option A"}
	m.input.SetValue("some text")

	result, cmd := m.updatePromptMode(specialKey(tea.KeyEscape))
	got := result.(*Model)

	if got.prompt.promptCustom {
		t.Error("promptCustom should be false after esc from custom mode")
	}
	// Input should be reset.
	if got.input.Value() != "" {
		t.Errorf("input value = %q, want empty after esc from custom", got.input.Value())
	}
	// No cmd should be produced — just go back to option selection.
	if cmd != nil {
		t.Error("expected nil cmd when going back from custom to option selection")
	}
}

func TestUpdatePromptMode_CustomModeDelegatesKeyToTextarea(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.prompt.promptMode = true
	m.prompt.promptCustom = true
	m.prompt.promptOptions = []string{"Option A"}
	m.input.Focus()

	// Typing a character in custom mode should delegate to the textarea.
	result, _ := m.updatePromptMode(keyPress('x'))
	got := result.(*Model)

	// The model should still be in custom mode.
	if !got.prompt.promptCustom {
		t.Error("promptCustom should remain true when typing in custom mode")
	}
}
