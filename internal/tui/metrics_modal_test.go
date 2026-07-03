package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/service"
)

func TestRenderMetricsModal_Empty(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.width = 120
	m.height = 40
	m.metricsModal = metricsModalState{show: true}

	result := m.renderMetricsModal()
	if !strings.Contains(result, "No node executions recorded yet") {
		t.Error("expected empty-state message for node executions")
	}
	if !strings.Contains(result, "No worker sessions recorded yet") {
		t.Error("expected empty-state message for session stats")
	}
}

func TestRenderMetricsModal_Loading(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.width = 120
	m.height = 40
	m.metricsModal = metricsModalState{show: true, loading: true}

	result := m.renderMetricsModal()
	if !strings.Contains(result, "Loading...") {
		t.Error("expected 'Loading...' while fetch is in flight")
	}
}

func TestRenderMetricsModal_WithData(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.width = 120
	m.height = 40
	m.metricsModal = metricsModalState{
		show: true,
		report: service.MetricsReport{
			Nodes: []service.NodeMetric{
				{Node: "implement", Runs: 10, Failures: 2, FailureRate: 0.2, AvgElapsedMS: 1234, MinElapsedMS: 500, MaxElapsedMS: 3000},
			},
			Sessions: []service.SessionMetric{
				{WorkerID: "coder", Sessions: 5, Failures: 1, FailureRate: 0.2, AvgDurationSeconds: 42, AvgTokensIn: 1500, AvgTokensOut: 400, UsageUnavailable: 2, AvgContextPercent: 0.35},
			},
		},
	}

	result := m.renderMetricsModal()
	if !strings.Contains(result, "implement") {
		t.Error("expected node name 'implement' in modal")
	}
	if !strings.Contains(result, "coder") {
		t.Error("expected worker id 'coder' in modal")
	}
	if !strings.Contains(result, "35%") {
		t.Error("expected context percentage '35%' in modal")
	}
}

func TestUpdateMetricsModal_EscCloses(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.metricsModal = metricsModalState{show: true}

	result, cmd := m.updateMetricsModal(specialKey(tea.KeyEscape))
	model := result.(*Model)
	if model.metricsModal.show {
		t.Error("expected modal to be closed after Esc")
	}
	if cmd != nil {
		t.Error("expected nil cmd after Esc")
	}
}

func TestUpdateMetricsModal_ScrollDownIncrementsAndUpClamps(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.metricsModal = metricsModalState{show: true}

	result, _ := m.updateMetricsModal(specialKey(tea.KeyDown))
	model := result.(*Model)
	if model.metricsModal.scroll != 1 {
		t.Errorf("scroll = %d, want 1 after Down", model.metricsModal.scroll)
	}

	result, _ = model.updateMetricsModal(specialKey(tea.KeyUp))
	model = result.(*Model)
	if model.metricsModal.scroll != 0 {
		t.Errorf("scroll = %d, want 0 after Up", model.metricsModal.scroll)
	}

	// Up at zero must not go negative.
	result, _ = model.updateMetricsModal(specialKey(tea.KeyUp))
	model = result.(*Model)
	if model.metricsModal.scroll != 0 {
		t.Errorf("scroll = %d, want 0 (clamped)", model.metricsModal.scroll)
	}
}

func TestUpdate_DispatchesToMetricsModalWhenOpen(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.metricsModal = metricsModalState{show: true}

	result, _ := m.Update(specialKey(tea.KeyEscape))
	model := result.(*Model)
	if model.metricsModal.show {
		t.Error("expected Update to dispatch Esc to updateMetricsModal and close modal")
	}
}

func TestMetricsLoadedMsg_PopulatesReport(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.metricsModal = metricsModalState{show: true, loading: true}

	report := service.MetricsReport{
		Nodes: []service.NodeMetric{{Node: "plan", Runs: 3}},
	}
	result, _ := m.Update(MetricsLoadedMsg{Report: report})
	model := result.(*Model)
	if model.metricsModal.loading {
		t.Error("expected loading=false after MetricsLoadedMsg")
	}
	if len(model.metricsModal.report.Nodes) != 1 || model.metricsModal.report.Nodes[0].Node != "plan" {
		t.Errorf("report = %+v, want one node 'plan'", model.metricsModal.report)
	}
}

func TestMetricsLoadedMsg_Error(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.metricsModal = metricsModalState{show: true, loading: true}

	result, _ := m.Update(MetricsLoadedMsg{Err: errors.New("boom")})
	model := result.(*Model)
	if model.metricsModal.loading {
		t.Error("expected loading=false after MetricsLoadedMsg with error")
	}
	if model.metricsModal.err == nil {
		t.Error("expected err to be set")
	}
}

func TestAllCommands_IncludesMetrics(t *testing.T) {
	t.Parallel()
	found := false
	for _, cmd := range allCommands {
		if cmd.Name == "/metrics" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected /metrics in allCommands")
	}
}
