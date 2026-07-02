package tui

import (
	"testing"

	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/service"
)

// TestThresholdFormatParseRoundTrip verifies every preset the settings UI
// cycles through survives the option-string round trip, including the "off"
// sentinel for 0.
func TestThresholdFormatParseRoundTrip(t *testing.T) {
	t.Parallel()

	for _, v := range config.CompactionThresholdOptions() {
		if got := parseThreshold(formatThreshold(v)); got != v {
			t.Errorf("parseThreshold(formatThreshold(%d)) = %d, want %d", v, got, v)
		}
	}
	if got := formatThreshold(0); got != "off" {
		t.Errorf("formatThreshold(0) = %q, want %q", got, "off")
	}
	if got := parseThreshold("garbage"); got != 0 {
		t.Errorf("parseThreshold(garbage) = %d, want 0", got)
	}
}

// TestCompactionThresholdRows verifies the two threshold rows' get/set
// closures read and write their own Settings field — a swap between the two
// rows is exactly the bug a flat row list invites.
func TestCompactionThresholdRows(t *testing.T) {
	t.Parallel()

	rowByLabel := func(label string) settingsRow {
		for _, r := range settingsRows {
			if r.label == label {
				return r
			}
		}
		t.Fatalf("settings row %q not found", label)
		return settingsRow{}
	}

	opRow := rowByLabel("Operator Compaction Threshold")
	workerRow := rowByLabel("Worker Compaction Threshold")

	s := service.Settings{OperatorCompactionThreshold: 40, WorkerCompactionThreshold: 80}
	if got := opRow.get(&s); got != "40%" {
		t.Errorf("operator row get = %q, want %q", got, "40%")
	}
	if got := workerRow.get(&s); got != "80%" {
		t.Errorf("worker row get = %q, want %q", got, "80%")
	}

	opRow.set(&s, "60%")
	workerRow.set(&s, "off")
	if s.OperatorCompactionThreshold != 60 {
		t.Errorf("after set: OperatorCompactionThreshold = %d, want 60", s.OperatorCompactionThreshold)
	}
	if s.WorkerCompactionThreshold != 0 {
		t.Errorf("after set: WorkerCompactionThreshold = %d, want 0 (off)", s.WorkerCompactionThreshold)
	}
}

// TestSettingsSavedMsg_AppliesThresholds drives the real Update dispatch (not
// applySettings directly) and proves a saved 0 reaches the model as 0.
func TestSettingsSavedMsg_AppliesThresholds(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	res, _ := m.Update(SettingsSavedMsg{Settings: service.Settings{
		OperatorCompactionThreshold: 0,
		WorkerCompactionThreshold:   0,
	}})
	got := res.(*Model)
	if got.opCompactionThreshold != 0 || got.workerCompactionThreshold != 0 {
		t.Errorf("thresholds after SettingsSavedMsg = %d/%d, want 0/0",
			got.opCompactionThreshold, got.workerCompactionThreshold)
	}
}
