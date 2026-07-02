package tui

import (
	"testing"

	"github.com/jefflinse/toasters/internal/service"
)

func TestSlotCtxMax_PrefersServerResolvedWindow(t *testing.T) {
	t.Parallel()

	m := Model{modelContext: map[string]int{"gemma": 32768}}

	// Server-resolved window wins over the client-side model lookup.
	if got := m.slotCtxMax(&runtimeSlot{model: "gemma", ctxWindow: 8192}); got != 8192 {
		t.Errorf("slotCtxMax = %d, want 8192 (server-resolved)", got)
	}
	// Without a resolved window, fall back to the model lookup.
	if got := m.slotCtxMax(&runtimeSlot{model: "gemma"}); got != 32768 {
		t.Errorf("slotCtxMax = %d, want 32768 (modelContext fallback)", got)
	}
	// Neither known → 0, bar renders a raw token count.
	if got := m.slotCtxMax(&runtimeSlot{model: "mystery"}); got != 0 {
		t.Errorf("slotCtxMax = %d, want 0", got)
	}
}

func TestHandleModels_DoesNotClobberServerResolvedWindow(t *testing.T) {
	t.Parallel()

	m := &Model{}
	m.stats.ModelName = "gemma"
	m.stats.ContextLength = 8192 // server-resolved via AppReadyMsg

	// A later ListModels response reports a different context length for the
	// same model ID (e.g. the model file's max rather than the loaded value).
	m.handleModels(ModelsMsg{Models: []service.ModelInfo{
		{ID: "gemma", MaxContextLength: 131072},
	}})
	if m.stats.ContextLength != 8192 {
		t.Errorf("ContextLength = %d, want 8192 (server value preserved)", m.stats.ContextLength)
	}

	// But when the server resolved nothing, the model list may fill the gap.
	m.stats.ContextLength = 0
	m.handleModels(ModelsMsg{Models: []service.ModelInfo{
		{ID: "gemma", MaxContextLength: 131072},
	}})
	if m.stats.ContextLength != 131072 {
		t.Errorf("ContextLength = %d, want 131072 (list fills the gap)", m.stats.ContextLength)
	}
}

func TestAppReady_ZeroWindowPreservesExisting(t *testing.T) {
	t.Parallel()

	// AppReadyMsg with no resolved window must not blank a value some other
	// path already filled — 0 means "server doesn't know", not "reset".
	m := newMinimalModel(t)
	m.stats.ContextLength = 8192
	m.handleAppReady(AppReadyMsg{ModelName: "gemma"})
	if m.stats.ContextLength != 8192 {
		t.Errorf("ContextLength = %d, want 8192 preserved", m.stats.ContextLength)
	}
	m.handleAppReady(AppReadyMsg{ModelName: "gemma", ContextWindow: 16384})
	if m.stats.ContextLength != 16384 {
		t.Errorf("ContextLength = %d, want 16384 from AppReadyMsg", m.stats.ContextLength)
	}
}

func TestOperatorStatusRefreshed_ResetsWindowWithModel(t *testing.T) {
	t.Parallel()

	// A status refresh that names a model owns the window outright — even a
	// 0 must land, or a provider switch leaves the old provider's window on
	// the bar.
	m := newMinimalModel(t)
	m.stats.ContextLength = 8192
	res, _ := m.Update(OperatorStatusRefreshedMsg{ModelName: "new-model", ContextWindow: 0})
	got := res.(*Model)
	if got.stats.ContextLength != 0 {
		t.Errorf("ContextLength = %d, want 0 after provider switch with unknown window", got.stats.ContextLength)
	}
	if got.stats.ModelName != "new-model" {
		t.Errorf("ModelName = %q, want %q", got.stats.ModelName, "new-model")
	}
}

func TestProgressPoll_PopulatesSlotContextWindow(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.handleProgressPoll(progressPollMsg{
		LiveSnapshots: []service.SessionSnapshot{
			{ID: "s1", WorkerID: "w1", Status: "active", Model: "gemma", ContextWindow: 8192},
		},
	})
	slot, ok := m.runtimeSessions["s1"]
	if !ok {
		t.Fatal("slot s1 not created from live snapshot")
	}
	if slot.ctxWindow != 8192 {
		t.Errorf("slot.ctxWindow = %d, want 8192", slot.ctxWindow)
	}

	// A refresh without a resolved window must not blank a known one.
	m.handleProgressPoll(progressPollMsg{
		LiveSnapshots: []service.SessionSnapshot{
			{ID: "s1", WorkerID: "w1", Status: "active", Model: "gemma"},
		},
	})
	if slot.ctxWindow != 8192 {
		t.Errorf("slot.ctxWindow after zero-window refresh = %d, want 8192", slot.ctxWindow)
	}
}
