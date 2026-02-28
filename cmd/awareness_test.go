package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/tui"
)

// mockProvider implements provider.Provider for testing awareness functions.
// It returns a configurable text response (or error) from ChatStream.
type mockProvider struct {
	response string
	err      error
}

func (m *mockProvider) ChatStream(_ context.Context, _ provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	if m.err != nil {
		return nil, m.err
	}
	ch := make(chan provider.StreamEvent, 2)
	ch <- provider.StreamEvent{Type: provider.EventText, Text: m.response}
	ch <- provider.StreamEvent{Type: provider.EventDone}
	close(ch)
	return ch, nil
}

func (m *mockProvider) Models(_ context.Context) ([]provider.ModelInfo, error) {
	return nil, errors.New("not implemented")
}

func (m *mockProvider) Name() string { return "mock" }

// helper to build a TeamView with the given name, coordinator, and workers.
func makeTeamView(name string, coordinator *db.Agent, workers []*db.Agent) tui.TeamView {
	return tui.TeamView{
		Team:        &db.Team{Name: name},
		Coordinator: coordinator,
		Workers:     workers,
	}
}

func TestSummarizeTeam_WithCoordinator(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{response: "Use this team when you need frontend work."}
	team := makeTeamView("frontend", &db.Agent{
		SystemPrompt: "You are a frontend specialist.",
	}, nil)

	got := summarizeTeam(context.Background(), mp, team)
	if got != "Use this team when you need frontend work." {
		t.Errorf("summarizeTeam() = %q, want %q", got, "Use this team when you need frontend work.")
	}
}

func TestSummarizeTeam_WithCoordinatorWhitespacePrompt(t *testing.T) {
	t.Parallel()

	// A coordinator with a whitespace-only system prompt should fall through
	// to the worker-names branch.
	mp := &mockProvider{response: "Use this team when you need backend work."}
	team := makeTeamView("backend", &db.Agent{
		SystemPrompt: "   \n\t  ",
	}, []*db.Agent{
		{Name: "api-dev"},
		{Name: "db-dev"},
	})

	got := summarizeTeam(context.Background(), mp, team)
	if got != "Use this team when you need backend work." {
		t.Errorf("summarizeTeam() = %q, want %q", got, "Use this team when you need backend work.")
	}
}

func TestSummarizeTeam_NoCoordinator(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{response: "Use this team when you need infrastructure work."}
	team := makeTeamView("infra", nil, []*db.Agent{
		{Name: "terraform-agent"},
		{Name: "docker-agent"},
	})

	got := summarizeTeam(context.Background(), mp, team)
	if got != "Use this team when you need infrastructure work." {
		t.Errorf("summarizeTeam() = %q, want %q", got, "Use this team when you need infrastructure work.")
	}
}

func TestSummarizeTeam_LLMError(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{err: errors.New("connection refused")}
	team := makeTeamView("broken", &db.Agent{
		SystemPrompt: "You handle broken things.",
	}, nil)

	got := summarizeTeam(context.Background(), mp, team)
	want := "Use this team when you need help from the broken team."
	if got != want {
		t.Errorf("summarizeTeam() = %q, want %q", got, want)
	}
}

func TestSummarizeTeam_LLMErrorNoCoordinator(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{err: errors.New("timeout")}
	team := makeTeamView("qa", nil, []*db.Agent{
		{Name: "test-runner"},
	})

	got := summarizeTeam(context.Background(), mp, team)
	want := "Use this team when you need help from the qa team."
	if got != want {
		t.Errorf("summarizeTeam() = %q, want %q", got, want)
	}
}

func TestSummarizeTeam_ResponseTrimmed(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{response: "  Use this team for testing.  \n"}
	team := makeTeamView("test", &db.Agent{
		SystemPrompt: "You run tests.",
	}, nil)

	got := summarizeTeam(context.Background(), mp, team)
	if got != "Use this team for testing." {
		t.Errorf("summarizeTeam() = %q, want trimmed response", got)
	}
}

func TestGenerateTeamAwareness_EmptyTeams(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{response: "should not be called"}
	got := generateTeamAwareness(context.Background(), mp, nil, t.TempDir())
	if got != "" {
		t.Errorf("generateTeamAwareness(nil teams) = %q, want empty string", got)
	}
}

func TestGenerateTeamAwareness_EmptySlice(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{response: "should not be called"}
	got := generateTeamAwareness(context.Background(), mp, []tui.TeamView{}, t.TempDir())
	if got != "" {
		t.Errorf("generateTeamAwareness(empty slice) = %q, want empty string", got)
	}
}

func TestGenerateTeamAwareness_OneTeam(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{response: "Use this team when you need frontend work."}
	teams := []tui.TeamView{
		makeTeamView("frontend", &db.Agent{SystemPrompt: "Frontend specialist."}, nil),
	}
	configDir := t.TempDir()

	got := generateTeamAwareness(context.Background(), mp, teams, configDir)

	if !strings.Contains(got, "# Teams") {
		t.Error("output should contain '# Teams' header")
	}
	if !strings.Contains(got, "## frontend") {
		t.Error("output should contain '## frontend' header")
	}
	if !strings.Contains(got, "Use this team when you need frontend work.") {
		t.Error("output should contain the team summary")
	}
	if !strings.HasSuffix(got, "\n") {
		t.Error("output should end with a newline")
	}

	// Verify file was written.
	outPath := filepath.Join(configDir, "team-awareness.md")
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}
	if string(data) != got {
		t.Errorf("file content does not match returned content:\nfile: %q\nreturned: %q", string(data), got)
	}
}

func TestGenerateTeamAwareness_MultipleTeams(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{response: "Use this team for general work."}
	teams := []tui.TeamView{
		makeTeamView("frontend", &db.Agent{SystemPrompt: "Frontend."}, nil),
		makeTeamView("backend", &db.Agent{SystemPrompt: "Backend."}, nil),
		makeTeamView("devops", nil, []*db.Agent{{Name: "deployer"}}),
	}
	configDir := t.TempDir()

	got := generateTeamAwareness(context.Background(), mp, teams, configDir)

	for _, name := range []string{"frontend", "backend", "devops"} {
		if !strings.Contains(got, "## "+name) {
			t.Errorf("output should contain '## %s' header", name)
		}
	}

	// Each team should have its summary.
	count := strings.Count(got, "Use this team for general work.")
	if count != 3 {
		t.Errorf("expected 3 summaries, got %d", count)
	}
}

func TestGenerateTeamAwareness_WritesFile(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{response: "Use this team for testing."}
	teams := []tui.TeamView{
		makeTeamView("test-team", &db.Agent{SystemPrompt: "Tester."}, nil),
	}
	configDir := t.TempDir()

	content := generateTeamAwareness(context.Background(), mp, teams, configDir)

	outPath := filepath.Join(configDir, "team-awareness.md")
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("expected file at %s: %v", outPath, err)
	}
	if string(data) != content {
		t.Error("file content should match returned string")
	}
}

func TestGenerateTeamAwareness_InvalidConfigDir(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{response: "Use this team for work."}
	teams := []tui.TeamView{
		makeTeamView("team-a", &db.Agent{SystemPrompt: "Agent."}, nil),
	}

	// Use a non-existent directory — file write should fail gracefully.
	badDir := filepath.Join(t.TempDir(), "nonexistent", "subdir")
	got := generateTeamAwareness(context.Background(), mp, teams, badDir)

	// Function should still return content even if file write fails.
	if !strings.Contains(got, "## team-a") {
		t.Error("should return content even when file write fails")
	}
}

func TestGenerateTeamAwareness_LLMErrorFallback(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{err: errors.New("LLM unavailable")}
	teams := []tui.TeamView{
		makeTeamView("broken-team", nil, []*db.Agent{{Name: "worker-1"}}),
	}
	configDir := t.TempDir()

	got := generateTeamAwareness(context.Background(), mp, teams, configDir)

	want := "Use this team when you need help from the broken-team team."
	if !strings.Contains(got, want) {
		t.Errorf("output should contain fallback sentence %q, got:\n%s", want, got)
	}
}
