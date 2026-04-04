package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/service"
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
func makeTeamView(name string, coordinator *service.Agent, workers []service.Agent) service.TeamView {
	return service.TeamView{
		Team:        service.Team{Name: name},
		Coordinator: coordinator,
		Workers:     workers,
	}
}

func TestSummarizeTeam_WithCoordinator(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{response: "Use this team when you need frontend work."}
	coord := service.Agent{SystemPrompt: "You are a frontend specialist."}
	team := makeTeamView("frontend", &coord, nil)

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
	coord := service.Agent{SystemPrompt: "   \n\t  "}
	team := makeTeamView("backend", &coord, []service.Agent{
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
	team := makeTeamView("infra", nil, []service.Agent{
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
	coord := service.Agent{SystemPrompt: "You handle broken things."}
	team := makeTeamView("broken", &coord, nil)

	got := summarizeTeam(context.Background(), mp, team)
	want := "Use this team when you need help from the broken team."
	if got != want {
		t.Errorf("summarizeTeam() = %q, want %q", got, want)
	}
}

func TestSummarizeTeam_LLMErrorNoCoordinator(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{err: errors.New("timeout")}
	team := makeTeamView("qa", nil, []service.Agent{
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
	coord := service.Agent{SystemPrompt: "You run tests."}
	team := makeTeamView("test", &coord, nil)

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
	got := generateTeamAwareness(context.Background(), mp, []service.TeamView{}, t.TempDir())
	if got != "" {
		t.Errorf("generateTeamAwareness(empty slice) = %q, want empty string", got)
	}
}

func TestGenerateTeamAwareness_OneTeam(t *testing.T) {
	t.Parallel()

	mp := &mockProvider{response: "Use this team when you need frontend work."}
	coord := service.Agent{SystemPrompt: "Frontend specialist."}
	teams := []service.TeamView{
		makeTeamView("frontend", &coord, nil),
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
	coord1 := service.Agent{SystemPrompt: "Frontend."}
	coord2 := service.Agent{SystemPrompt: "Backend."}
	teams := []service.TeamView{
		makeTeamView("frontend", &coord1, nil),
		makeTeamView("backend", &coord2, nil),
		makeTeamView("devops", nil, []service.Agent{{Name: "deployer"}}),
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
	coord := service.Agent{SystemPrompt: "Tester."}
	teams := []service.TeamView{
		makeTeamView("test-team", &coord, nil),
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
	coord := service.Agent{SystemPrompt: "Agent."}
	teams := []service.TeamView{
		makeTeamView("team-a", &coord, nil),
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
	teams := []service.TeamView{
		makeTeamView("broken-team", nil, []service.Agent{{Name: "worker-1"}}),
	}
	configDir := t.TempDir()

	got := generateTeamAwareness(context.Background(), mp, teams, configDir)

	want := "Use this team when you need help from the broken-team team."
	if !strings.Contains(got, want) {
		t.Errorf("output should contain fallback sentence %q, got:\n%s", want, got)
	}
}

func TestGenerateTeamAwareness_NilProviderFallback(t *testing.T) {
	t.Parallel()

	teams := []service.TeamView{
		makeTeamView("frontend", nil, []service.Agent{{Name: "react-dev"}}),
		makeTeamView("backend", &service.Agent{SystemPrompt: "API specialist"}, nil),
	}
	configDir := t.TempDir()

	// This is the critical code path - nil provider with actual teams
	got := generateTeamAwareness(context.Background(), nil, teams, configDir)

	if !strings.Contains(got, "# Teams") {
		t.Error("output should contain '# Teams' header")
	}
	if !strings.Contains(got, "## frontend") {
		t.Error("output should contain '## frontend' header")
	}
	if !strings.Contains(got, "## backend") {
		t.Error("output should contain '## backend' header")
	}
	if !strings.Contains(got, "Use this team when you need help from the frontend team.") {
		t.Error("output should contain fallback text for frontend")
	}
	if !strings.Contains(got, "Use this team when you need help from the backend team.") {
		t.Error("output should contain fallback text for backend")
	}

	// Verify file was written
	outPath := filepath.Join(configDir, "team-awareness.md")
	if _, err := os.Stat(outPath); os.IsNotExist(err) {
		t.Error("team-awareness.md should have been written")
	}
}
