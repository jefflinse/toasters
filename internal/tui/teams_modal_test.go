// Regression tests for the Ctrl+P auto-team promotion fix.
//
// Bug: pressing Ctrl+P called promoteAutoTeam() synchronously on the Bubble
// Tea update goroutine, blocking the event loop.
//
// Fix: the handler now appends promoteAutoTeamCmd(team) — a tea.Cmd that runs
// promotion via the service layer — and returns it from updateTeamsModal.
package tui

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/service"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeAutoTeam creates a temporary directory that looks like an auto-team
// (contains a .auto-team marker file) and returns a service.TeamView pointing
// at that directory.
func makeAutoTeam(t *testing.T, name string) service.TeamView {
	t.Helper()
	dir := t.TempDir()

	// Write the .auto-team marker so isAutoTeam() returns true.
	if err := os.WriteFile(filepath.Join(dir, ".auto-team"), []byte{}, 0o644); err != nil {
		t.Fatalf("writing .auto-team marker: %v", err)
	}

	return service.TeamView{
		Team: service.Team{Name: name, SourcePath: dir, IsAuto: true},
	}
}

// makeNonAutoTeam creates a temporary directory that is NOT an auto-team
// (no .auto-team marker, not a read-only well-known directory).
func makeNonAutoTeam(t *testing.T, name string) service.TeamView {
	t.Helper()
	dir := t.TempDir()
	return service.TeamView{
		Team: service.Team{Name: name, SourcePath: dir},
	}
}

// modelWithTeam returns a minimal Model with the teams modal open and the
// given team pre-selected at index 0.
func modelWithTeam(t *testing.T, team service.TeamView) *Model {
	t.Helper()
	m := newMinimalModel(t)
	m.teamsModal.show = true
	m.teamsModal.teams = []service.TeamView{team}
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
	t.Parallel()

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
// does not call reloadTeamsForModal (which would be a spurious service call).
func TestTeamPromotedMsg_Error_DoesNotReloadTeams(t *testing.T) {
	t.Parallel()

	// Seed the modal with a sentinel team list.
	sentinelTeam := service.TeamView{Team: service.Team{Name: "sentinel", SourcePath: t.TempDir()}}
	m := newMinimalModel(t)
	m.teamsModal.promoting = true
	m.teamsModal.teams = []service.TeamView{sentinelTeam}

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
// Test: teamPromotedMsg with success clears promoting flag
// ---------------------------------------------------------------------------

// TestTeamPromotedMsg_Success_ClearsPromotingFlag verifies the promoting flag
// is cleared on a successful promotion.
func TestTeamPromotedMsg_Success_ClearsPromotingFlag(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.teamsModal.promoting = true
	// Provide a minimal mock service so reloadTeamsForModal doesn't panic.
	m.svc = &mockService{}

	updatedModel, _ := m.Update(teamPromotedMsg{
		teamName: "my-auto-team",
		err:      nil,
	})
	got := updatedModel.(*Model)

	if got.teamsModal.promoting {
		t.Error("teamsModal.promoting should be false after successful teamPromotedMsg")
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

	team := service.TeamView{Team: service.Team{Name: "auto", SourcePath: dir}}
	if !isAutoTeam(team) {
		t.Error("isAutoTeam should return true when .auto-team marker is present")
	}
}

func TestIsAutoTeam_WithoutMarker(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	team := service.TeamView{Team: service.Team{Name: "regular", SourcePath: dir}}
	if isAutoTeam(team) {
		t.Error("isAutoTeam should return false when .auto-team marker is absent")
	}
}

func TestIsReadOnlyTeam_UnknownDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	team := service.TeamView{Team: service.Team{Name: "unknown", SourcePath: dir}}
	if isReadOnlyTeam(team) {
		t.Error("isReadOnlyTeam should return false for an arbitrary directory")
	}
}
