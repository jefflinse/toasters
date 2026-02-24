package tui

import "testing"

func TestFilterCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		prefix string
		check  func(t *testing.T, result []SlashCommand)
	}{
		{
			name:   "slash alone returns all commands",
			prefix: "/",
			check: func(t *testing.T, result []SlashCommand) {
				if len(result) != len(allCommands) {
					t.Errorf("expected %d commands, got %d", len(allCommands), len(result))
				}
			},
		},
		{
			name:   "prefix matches subset",
			prefix: "/he",
			check: func(t *testing.T, result []SlashCommand) {
				if len(result) != 1 {
					t.Fatalf("expected 1 command, got %d", len(result))
				}
				if result[0].Name != "/help" {
					t.Errorf("expected /help, got %q", result[0].Name)
				}
			},
		},
		{
			name:   "prefix matches multiple",
			prefix: "/e",
			check: func(t *testing.T, result []SlashCommand) {
				// /exit matches
				if len(result) < 1 {
					t.Error("expected at least 1 match for /e")
				}
				for _, c := range result {
					if c.Name[:2] != "/e" {
						t.Errorf("command %q does not start with /e", c.Name)
					}
				}
			},
		},
		{
			name:   "no matches returns empty",
			prefix: "/zzz",
			check: func(t *testing.T, result []SlashCommand) {
				if len(result) != 0 {
					t.Errorf("expected 0 commands, got %d", len(result))
				}
			},
		},
		{
			name:   "exact match",
			prefix: "/help",
			check: func(t *testing.T, result []SlashCommand) {
				if len(result) != 1 {
					t.Fatalf("expected 1 command, got %d", len(result))
				}
				if result[0].Name != "/help" {
					t.Errorf("expected /help, got %q", result[0].Name)
				}
			},
		},
		{
			name:   "quit and exit both match /qu and /ex",
			prefix: "/qu",
			check: func(t *testing.T, result []SlashCommand) {
				if len(result) != 1 {
					t.Fatalf("expected 1 command for /qu, got %d", len(result))
				}
				if result[0].Name != "/quit" {
					t.Errorf("expected /quit, got %q", result[0].Name)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := filterCommands(tt.prefix)
			tt.check(t, result)
		})
	}
}
