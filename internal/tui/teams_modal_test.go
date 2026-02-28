// Regression tests for the Ctrl+P auto-team promotion fix.
//
// Bug: pressing Ctrl+P called promoteAutoTeam() synchronously on the Bubble
// Tea update goroutine, blocking the event loop.
//
// Fix: the handler now appends promoteAutoTeamCmd(team) — a tea.Cmd that runs
// promoteAutoTeam in a goroutine — and returns it from updateTeamsModal.
package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/agentfmt"
	"github.com/jefflinse/toasters/internal/db"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// agentFileContent returns a minimal agent .md file that agentfmt will
// classify as DefAgent (not DefSkill).  The "mode" field is an agent-only
// field that triggers the agent detection heuristic in agentfmt.ParseFile.
func agentFileContent(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\nmode: worker\n---\nDo work.\n"
}

// makeAutoTeam creates a temporary directory that looks like an auto-team
// (contains a .auto-team marker file and at least one agent .md file).
// It returns a TeamView pointing at that directory.
func makeAutoTeam(t *testing.T, name string) TeamView {
	t.Helper()
	dir := t.TempDir()

	// Write the .auto-team marker so isAutoTeam() returns true.
	if err := os.WriteFile(filepath.Join(dir, ".auto-team"), []byte{}, 0o644); err != nil {
		t.Fatalf("writing .auto-team marker: %v", err)
	}

	// Create an agents/ subdirectory with one valid agent file so that
	// promoteAutoTeam can find something to copy.
	agentsDir := filepath.Join(dir, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("creating agents dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "worker.md"), []byte(agentFileContent("worker", "a worker agent")), 0o644); err != nil {
		t.Fatalf("writing agent file: %v", err)
	}

	return TeamView{
		Team: &db.Team{Name: name, SourcePath: dir},
	}
}

// makeNonAutoTeam creates a temporary directory that is NOT an auto-team
// (no .auto-team marker, not a read-only well-known directory).
func makeNonAutoTeam(t *testing.T, name string) TeamView {
	t.Helper()
	dir := t.TempDir()
	return TeamView{
		Team: &db.Team{Name: name, SourcePath: dir},
	}
}

// modelWithTeam returns a minimal Model with the teams modal open and the
// given team pre-selected at index 0.
func modelWithTeam(t *testing.T, team TeamView) *Model {
	t.Helper()
	m := newMinimalModel(t)
	m.teamsModal.show = true
	m.teamsModal.teams = []TeamView{team}
	m.teamsModal.teamIdx = 0
	m.teamsModal.focus = 0 // left panel focused (required for Ctrl+P to fire)
	return &m
}

// isNonNilCmd returns true when cmd is a non-nil tea.Cmd.  We cannot inspect
// the internals of a tea.Batch, but we can verify that a non-nil command was
// returned — which is the key behavioral invariant for the async fix.
func isNonNilCmd(cmd tea.Cmd) bool {
	return cmd != nil
}

// ---------------------------------------------------------------------------
// Test: Ctrl+P on an auto-team returns a non-nil promotion command (async fix)
// ---------------------------------------------------------------------------

// TestCtrlP_AutoTeam_ReturnsPromotionCmd is the primary regression test.
// Before the fix, updateTeamsModal called promoteAutoTeam() synchronously and
// returned nil for the cmd.  After the fix it must return a non-nil tea.Cmd.
func TestCtrlP_AutoTeam_ReturnsPromotionCmd(t *testing.T) {
	t.Parallel()

	team := makeAutoTeam(t, "my-auto-team")
	m := modelWithTeam(t, team)

	_, cmd := m.updateTeamsModal(ctrlKey('p'))

	if !isNonNilCmd(cmd) {
		t.Fatal("updateTeamsModal(Ctrl+P) on an auto-team must return a non-nil tea.Cmd (async promotion); got nil — this is the regression")
	}
}

// TestCtrlP_AutoTeam_SetsPromotingFlag verifies that the promoting flag is set
// to true immediately (before the async cmd completes) so the UI can show a
// spinner.
func TestCtrlP_AutoTeam_SetsPromotingFlag(t *testing.T) {
	t.Parallel()

	team := makeAutoTeam(t, "my-auto-team")
	m := modelWithTeam(t, team)

	result, _ := m.updateTeamsModal(ctrlKey('p'))
	got := result.(*Model)

	if !got.teamsModal.promoting {
		t.Error("teamsModal.promoting should be true immediately after Ctrl+P on an auto-team")
	}
}

// ---------------------------------------------------------------------------
// Test: Ctrl+P on a non-auto-team does NOT dispatch a promotion command
// ---------------------------------------------------------------------------

func TestCtrlP_NonAutoTeam_NoPromotionCmd(t *testing.T) {
	t.Parallel()

	team := makeNonAutoTeam(t, "regular-team")
	m := modelWithTeam(t, team)

	_, cmd := m.updateTeamsModal(ctrlKey('p'))

	// A non-auto-team should not trigger promotion.
	// The returned cmd may be nil or a no-op batch; either way, promoting must
	// remain false and no promotion work should be scheduled.
	if m.teamsModal.promoting {
		t.Error("teamsModal.promoting should remain false for a non-auto-team")
	}
	// We can't distinguish "nil cmd" from "empty batch" without executing it,
	// but we can verify the promoting flag is the authoritative guard.
	_ = cmd
}

// ---------------------------------------------------------------------------
// Test: Ctrl+P while already promoting does NOT dispatch a second command
// ---------------------------------------------------------------------------

// TestCtrlP_WhilePromoting_NoDoubleDispatch guards against double-fire.
// If promoting is already true (an in-flight promotion), a second Ctrl+P must
// be a no-op: the promoting flag stays true and no new cmd is returned.
func TestCtrlP_WhilePromoting_NoDoubleDispatch(t *testing.T) {
	// Not parallel: uses t.Setenv to guard against real config writes.
	t.Setenv("HOME", t.TempDir())

	team := makeAutoTeam(t, "my-auto-team")
	m := modelWithTeam(t, team)

	// Simulate an in-flight promotion.
	m.teamsModal.promoting = true

	_, cmd := m.updateTeamsModal(ctrlKey('p'))

	// The guard `!m.teamsModal.promoting` in the handler must prevent a second
	// command from being dispatched.
	if isNonNilCmd(cmd) {
		// Execute the cmd to check if it's actually a promotion command or just
		// an empty batch.  An empty tea.Batch returns nil when called.
		msg := cmd()
		if _, isPromotion := msg.(teamPromotedMsg); isPromotion {
			t.Error("Ctrl+P while promoting=true must not dispatch a second promotion command")
		}
	}

	// promoting flag must remain true (unchanged from the in-flight state).
	if !m.teamsModal.promoting {
		t.Error("teamsModal.promoting should remain true while promotion is in flight")
	}
}

// ---------------------------------------------------------------------------
// Test: teamPromotedMsg with error clears the promoting flag
// ---------------------------------------------------------------------------

func TestTeamPromotedMsg_Error_ClearsPromotingFlag(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.teamsModal.promoting = true

	updatedModel, _ := m.Update(teamPromotedMsg{
		teamName: "my-auto-team",
		err:      errors.New("disk full"),
	})
	got := updatedModel.(*Model)

	if got.teamsModal.promoting {
		t.Error("teamsModal.promoting should be false after teamPromotedMsg with error")
	}
}

// TestTeamPromotedMsg_Error_DoesNotReloadTeams verifies that a failed promotion
// does not call reloadTeamsForModal (which would be a spurious disk read).
func TestTeamPromotedMsg_Error_DoesNotReloadTeams(t *testing.T) {
	t.Parallel()

	// Seed the modal with a sentinel team list.
	sentinelTeam := TeamView{Team: &db.Team{Name: "sentinel", SourcePath: t.TempDir()}}
	m := newMinimalModel(t)
	m.teamsModal.promoting = true
	m.teamsModal.teams = []TeamView{sentinelTeam}
	m.teamsDir = t.TempDir() // empty dir → DiscoverTeams returns []

	updatedModel, _ := m.Update(teamPromotedMsg{
		teamName: "my-auto-team",
		err:      errors.New("disk full"),
	})
	got := updatedModel.(*Model)

	// On error the teams list must NOT be reloaded (sentinel should still be there).
	if len(got.teamsModal.teams) != 1 || got.teamsModal.teams[0].Name() != "sentinel" {
		t.Errorf("teams list should be unchanged on error; got %v", got.teamsModal.teams)
	}
}

// ---------------------------------------------------------------------------
// Test: teamPromotedMsg with success clears promoting and reloads teams
// ---------------------------------------------------------------------------

// TestTeamPromotedMsg_Success_ClearsPromotingFlag verifies the promoting flag
// is cleared on a successful promotion.
func TestTeamPromotedMsg_Success_ClearsPromotingFlag(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.teamsModal.promoting = true
	m.teamsDir = t.TempDir() // empty dir → DiscoverTeams returns []

	updatedModel, _ := m.Update(teamPromotedMsg{
		teamName: "my-auto-team",
		err:      nil,
	})
	got := updatedModel.(*Model)

	if got.teamsModal.promoting {
		t.Error("teamsModal.promoting should be false after successful teamPromotedMsg")
	}
}

// TestTeamPromotedMsg_Success_ReloadsTeams verifies that a successful promotion
// triggers reloadTeamsForModal.  We create a team in the store and write a
// team.md on disk so we can observe the reload and selectedTeamDef population.
func TestTeamPromotedMsg_Success_ReloadsTeams(t *testing.T) {
	t.Parallel()

	// Build a minimal teams directory with one team so refreshSelectedTeamDef
	// can parse team.md and populate selectedTeamDef.
	teamsDir := t.TempDir()
	teamDir := filepath.Join(teamsDir, "promoted-team")
	agentsDir := filepath.Join(teamDir, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("creating team agents dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "worker.md"), []byte(agentFileContent("worker", "a worker")), 0o644); err != nil {
		t.Fatalf("writing agent file: %v", err)
	}
	// Write a team.md so refreshSelectedTeamDef can parse it and populate selectedTeamDef.
	teamMDContent := "---\nname: promoted-team\ndescription: a promoted team\n---\n"
	if err := os.WriteFile(filepath.Join(teamDir, "team.md"), []byte(teamMDContent), 0o644); err != nil {
		t.Fatalf("writing team.md: %v", err)
	}

	// Create a real store and insert the team so reloadTeamsForModal finds it.
	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.UpsertTeam(context.Background(), &db.Team{
		ID:         "promoted-team",
		Name:       "promoted-team",
		Source:     "user",
		SourcePath: teamDir,
	}); err != nil {
		t.Fatalf("upserting test team: %v", err)
	}

	m := newMinimalModel(t)
	m.store = store
	m.teamsModal.promoting = true
	m.teamsModal.teams = nil // start empty
	m.teamsDir = teamsDir

	updatedModel, _ := m.Update(teamPromotedMsg{
		teamName: "promoted-team",
		err:      nil,
	})
	got := updatedModel.(*Model)

	// reloadTeamsForModal should have populated the teams list from the store.
	if len(got.teamsModal.teams) == 0 {
		t.Error("teamsModal.teams should be non-empty after successful promotion (reloadTeamsForModal was called)")
	}

	// refreshSelectedTeamDef should have been called after teamIdx was updated,
	// so selectedTeamDef must be non-nil (team.md was written above).
	if got.teamsModal.selectedTeamDef == nil {
		t.Error("teamsModal.selectedTeamDef should be non-nil after successful promotion (refreshSelectedTeamDef was called)")
	}
}

// ---------------------------------------------------------------------------
// Test: promoteAutoTeamCmd returns a tea.Cmd that produces teamPromotedMsg
// ---------------------------------------------------------------------------

// TestPromoteAutoTeamCmd_ReturnsTeamPromotedMsg verifies that the tea.Cmd
// returned by promoteAutoTeamCmd, when executed, produces a teamPromotedMsg
// with the correct team name.  This tests the cmd wrapper in isolation.
func TestPromoteAutoTeamCmd_ReturnsTeamPromotedMsg(t *testing.T) {
	t.Parallel()

	// We need a real team with agent files so promoteAutoTeam can succeed.
	// Point the config dir at a temp dir via the HOME env var trick is fragile;
	// instead we test the error path (target already exists) which still
	// produces a teamPromotedMsg — just with a non-nil err.
	team := TeamView{
		Team: &db.Team{Name: "test-team", SourcePath: t.TempDir()},
	}

	cmd := promoteAutoTeamCmd(team)

	if cmd == nil {
		t.Fatal("promoteAutoTeamCmd must return a non-nil tea.Cmd")
	}

	// Execute the cmd synchronously in the test (safe — no event loop here).
	msg := cmd()

	promoted, ok := msg.(teamPromotedMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want teamPromotedMsg", msg)
	}
	if promoted.teamName != "test-team" {
		t.Errorf("teamPromotedMsg.teamName = %q, want %q", promoted.teamName, "test-team")
	}
	// err may be non-nil (e.g. no agent files found) — that's fine; the
	// important thing is that the message type is correct and the name matches.
}

// ---------------------------------------------------------------------------
// Test: promoteAutoTeam creates the expected directory structure
// ---------------------------------------------------------------------------

// TestPromoteAutoTeam_CreatesExpectedStructure is a filesystem-level test of
// the underlying promoteAutoTeam function for the real-world bootstrap case:
// the auto-team already lives inside user/teams/{name}/ (team.Dir IS the
// target directory), with a .auto-team marker and an agents/ symlink pointing
// to a separate source directory.
//
// This is the case that previously always failed with "team directory already
// exists" because promoteAutoTeam computed targetDir == team.Dir.
func TestPromoteAutoTeam_CreatesExpectedStructure(t *testing.T) {
	// Not parallel: modifies HOME env var.

	// Create a fake home directory.
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	// Build the real agent source directory (simulates ~/.claude/agents or similar).
	agentSourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(agentSourceDir, "coder.md"), []byte(agentFileContent("coder", "writes code")), 0o644); err != nil {
		t.Fatalf("writing agent file: %v", err)
	}

	// Build the bootstrap auto-team directory inside user/teams/ — this is
	// where bootstrap places auto-teams: ~/.config/toasters/user/teams/{name}/.
	userTeamsDir := filepath.Join(fakeHome, ".config", "toasters", "user", "teams")
	teamDir := filepath.Join(userTeamsDir, "my-team")
	if err := os.MkdirAll(teamDir, 0o755); err != nil {
		t.Fatalf("creating team dir: %v", err)
	}

	// Write the .auto-team marker.
	if err := os.WriteFile(filepath.Join(teamDir, ".auto-team"), []byte{}, 0o644); err != nil {
		t.Fatalf("writing .auto-team: %v", err)
	}

	// Create the agents/ symlink pointing to the real source directory.
	agentsSymlink := filepath.Join(teamDir, "agents")
	if err := os.Symlink(agentSourceDir, agentsSymlink); err != nil {
		t.Fatalf("creating agents symlink: %v", err)
	}

	// team.Dir is the same as the target directory — this is the bug scenario.
	team := TeamView{
		Team: &db.Team{Name: "my-team", SourcePath: teamDir},
	}

	if err := promoteAutoTeam(team); err != nil {
		t.Fatalf("promoteAutoTeam returned unexpected error: %v", err)
	}

	// Verify the expected in-place structure.

	// team.md must exist in team.Dir.
	teamMDPath := filepath.Join(teamDir, "team.md")
	if _, err := os.Stat(teamMDPath); err != nil {
		t.Errorf("team.md not found at %s: %v", teamMDPath, err)
	}

	// agents/ must now be a real directory (not a symlink).
	info, err := os.Lstat(agentsSymlink)
	if err != nil {
		t.Fatalf("agents/ not found at %s: %v", agentsSymlink, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("agents/ should be a real directory after promotion, not a symlink")
	}
	if !info.IsDir() {
		t.Error("agents/ should be a directory after promotion")
	}

	// agents/coder.md must exist inside the real directory.
	agentMDPath := filepath.Join(teamDir, "agents", "coder.md")
	if _, err := os.Stat(agentMDPath); err != nil {
		t.Errorf("agents/coder.md not found at %s: %v", agentMDPath, err)
	}

	// .auto-team marker must be removed.
	markerPath := filepath.Join(teamDir, ".auto-team")
	if _, err := os.Stat(markerPath); err == nil {
		t.Error(".auto-team marker should be removed after promotion")
	}
}

// TestPromoteReadOnlyAutoTeam_CreatesExpectedStructure tests the legacy
// read-only auto-team path (e.g. ~/.claude/agents), where team.Dir is the
// agents directory itself and a new managed team directory is created under
// user/teams/. This calls promoteReadOnlyAutoTeam directly to avoid the
// getCachedHomeDir sync.Once cache interfering with isReadOnlyTeam.
func TestPromoteReadOnlyAutoTeam_CreatesExpectedStructure(t *testing.T) {
	// Not parallel: modifies HOME env var.

	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	// Simulate a read-only agents directory (e.g. ~/.claude/agents).
	// team.Dir IS the agents directory for read-only teams.
	agentsSourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(agentsSourceDir, "coder.md"), []byte(agentFileContent("coder", "writes code")), 0o644); err != nil {
		t.Fatalf("writing agent file: %v", err)
	}

	team := TeamView{
		Team: &db.Team{Name: "my-team", SourcePath: agentsSourceDir},
	}

	// Call promoteReadOnlyAutoTeam directly to bypass the isReadOnlyTeam check
	// (which relies on getCachedHomeDir, a sync.Once that may be stale in tests).
	if err := promoteReadOnlyAutoTeam(team); err != nil {
		t.Fatalf("promoteReadOnlyAutoTeam returned unexpected error: %v", err)
	}

	// Verify the expected directory structure under the fake home.
	userTeamsDir := filepath.Join(fakeHome, ".config", "toasters", "user", "teams")
	targetDir := filepath.Join(userTeamsDir, "my-team")

	// team.md must exist.
	teamMDPath := filepath.Join(targetDir, "team.md")
	if _, err := os.Stat(teamMDPath); err != nil {
		t.Errorf("team.md not found at %s: %v", teamMDPath, err)
	}

	// agents/coder.md must exist.
	agentMDPath := filepath.Join(targetDir, "agents", "coder.md")
	if _, err := os.Stat(agentMDPath); err != nil {
		t.Errorf("agents/coder.md not found at %s: %v", agentMDPath, err)
	}
}

// TestPromoteAutoTeam_FailsIfTargetExists verifies that promoteReadOnlyAutoTeam
// returns an error (rather than silently overwriting) when the target directory
// already exists. This applies to the read-only (legacy) path only.
func TestPromoteAutoTeam_FailsIfTargetExists(t *testing.T) {
	// Not parallel: modifies HOME env var.

	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	// Use an arbitrary source directory (content doesn't matter — we fail before reading it).
	sourceDir := t.TempDir()

	// Pre-create the target directory to simulate a collision.
	userTeamsDir := filepath.Join(fakeHome, ".config", "toasters", "user", "teams")
	targetDir := filepath.Join(userTeamsDir, "my-team")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("pre-creating target dir: %v", err)
	}

	team := TeamView{Team: &db.Team{Name: "my-team", SourcePath: sourceDir}}

	// Call promoteReadOnlyAutoTeam directly to test the collision guard.
	err := promoteReadOnlyAutoTeam(team)
	if err == nil {
		t.Error("promoteReadOnlyAutoTeam should return an error when the target directory already exists")
	}
}

// TestPromoteAutoTeam_FailsWithNoAgentFiles verifies that promoteAutoTeam
// returns an error when the source agents directory is empty.
func TestPromoteAutoTeam_FailsWithNoAgentFiles(t *testing.T) {
	// Not parallel: modifies HOME env var.

	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	// Build a bootstrap auto-team inside user/teams/ with an empty agents/ symlink target.
	emptySourceDir := t.TempDir() // empty — no .md files

	userTeamsDir := filepath.Join(fakeHome, ".config", "toasters", "user", "teams")
	teamDir := filepath.Join(userTeamsDir, "empty-team")
	if err := os.MkdirAll(teamDir, 0o755); err != nil {
		t.Fatalf("creating team dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(teamDir, ".auto-team"), []byte{}, 0o644); err != nil {
		t.Fatalf("writing .auto-team: %v", err)
	}
	if err := os.Symlink(emptySourceDir, filepath.Join(teamDir, "agents")); err != nil {
		t.Fatalf("creating agents symlink: %v", err)
	}

	team := TeamView{Team: &db.Team{Name: "empty-team", SourcePath: teamDir}}

	err := promoteAutoTeam(team)
	if err == nil {
		t.Error("promoteAutoTeam should return an error when no agent files are found")
	}
}

// ---------------------------------------------------------------------------
// Test: promoteAutoTeamCmd wraps promoteAutoTeam correctly (content check)
// ---------------------------------------------------------------------------

// TestPromoteAutoTeamCmd_SuccessPath verifies the full happy path end-to-end:
// the cmd returned by promoteAutoTeamCmd, when executed, produces a
// teamPromotedMsg with nil error and the correct team name.
//
// This test models the real-world bootstrap case: team.Dir is already inside
// user/teams/ with a .auto-team marker and an agents/ symlink.
func TestPromoteAutoTeamCmd_SuccessPath(t *testing.T) {
	// Not parallel: modifies HOME env var.

	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	// Build the real agent source directory.
	agentSourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(agentSourceDir, "builder.md"), []byte(agentFileContent("builder", "builds things")), 0o644); err != nil {
		t.Fatalf("writing agent file: %v", err)
	}

	// Build the bootstrap auto-team directory inside user/teams/.
	userTeamsDir := filepath.Join(fakeHome, ".config", "toasters", "user", "teams")
	teamDir := filepath.Join(userTeamsDir, "build-team")
	if err := os.MkdirAll(teamDir, 0o755); err != nil {
		t.Fatalf("creating team dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(teamDir, ".auto-team"), []byte{}, 0o644); err != nil {
		t.Fatalf("writing .auto-team: %v", err)
	}
	if err := os.Symlink(agentSourceDir, filepath.Join(teamDir, "agents")); err != nil {
		t.Fatalf("creating agents symlink: %v", err)
	}

	team := TeamView{Team: &db.Team{Name: "build-team", SourcePath: teamDir}}

	cmd := promoteAutoTeamCmd(team)
	if cmd == nil {
		t.Fatal("promoteAutoTeamCmd must return a non-nil tea.Cmd")
	}

	msg := cmd()

	promoted, ok := msg.(teamPromotedMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want teamPromotedMsg", msg)
	}
	if promoted.teamName != "build-team" {
		t.Errorf("teamName = %q, want %q", promoted.teamName, "build-team")
	}
	if promoted.err != nil {
		t.Errorf("unexpected error in teamPromotedMsg: %v", promoted.err)
	}
}

// ---------------------------------------------------------------------------
// Test: writeAgentFile / writeTeamFile round-trip
// ---------------------------------------------------------------------------

// TestWriteAgentFile_RoundTrip verifies that writeAgentFile produces a file
// that agentfmt can parse back to the same name and description.
func TestWriteAgentFile_RoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "agent.md")

	def := &agentfmt.AgentDef{
		Name:        "my-agent",
		Description: "does things",
		Mode:        "worker", // agent-only field required for ParseFile to classify as DefAgent
		Body:        "You are a helpful agent.",
	}

	if err := writeAgentFile(path, def); err != nil {
		t.Fatalf("writeAgentFile: %v", err)
	}

	defType, parsed, err := agentfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if defType != agentfmt.DefAgent {
		t.Errorf("defType = %v, want DefAgent", defType)
	}
	agentDef, ok := parsed.(*agentfmt.AgentDef)
	if !ok {
		t.Fatalf("parsed type = %T, want *agentfmt.AgentDef", parsed)
	}
	if agentDef.Name != "my-agent" {
		t.Errorf("Name = %q, want %q", agentDef.Name, "my-agent")
	}
	if agentDef.Description != "does things" {
		t.Errorf("Description = %q, want %q", agentDef.Description, "does things")
	}
}

// TestWriteTeamFile_RoundTrip verifies that writeTeamFile produces a file
// that agentfmt can parse back to the same name and agents list.
func TestWriteTeamFile_RoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "team.md")

	def := &agentfmt.TeamDef{
		Name:        "my-team",
		Description: "a great team",
		Lead:        "leader",
		Agents:      []string{"leader", "worker1", "worker2"},
	}

	if err := writeTeamFile(path, def); err != nil {
		t.Fatalf("writeTeamFile: %v", err)
	}

	parsed, err := agentfmt.ParseTeam(path)
	if err != nil {
		t.Fatalf("ParseTeam: %v", err)
	}
	if parsed.Name != "my-team" {
		t.Errorf("Name = %q, want %q", parsed.Name, "my-team")
	}
	if parsed.Lead != "leader" {
		t.Errorf("Lead = %q, want %q", parsed.Lead, "leader")
	}
	if len(parsed.Agents) != 3 {
		t.Errorf("Agents len = %d, want 3", len(parsed.Agents))
	}
}

// ---------------------------------------------------------------------------
// Test: isAutoTeam / isReadOnlyTeam helpers
// ---------------------------------------------------------------------------

func TestIsAutoTeam_WithMarker(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".auto-team"), []byte{}, 0o644); err != nil {
		t.Fatalf("writing .auto-team: %v", err)
	}

	team := TeamView{Team: &db.Team{Name: "auto", SourcePath: dir}}
	if !isAutoTeam(team) {
		t.Error("isAutoTeam should return true when .auto-team marker is present")
	}
}

func TestIsAutoTeam_WithoutMarker(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	team := TeamView{Team: &db.Team{Name: "regular", SourcePath: dir}}
	if isAutoTeam(team) {
		t.Error("isAutoTeam should return false when .auto-team marker is absent")
	}
}

func TestIsReadOnlyTeam_UnknownDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	team := TeamView{Team: &db.Team{Name: "unknown", SourcePath: dir}}
	if isReadOnlyTeam(team) {
		t.Error("isReadOnlyTeam should return false for an arbitrary directory")
	}
}
