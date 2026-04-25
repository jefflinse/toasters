package graphexec

import (
	"context"
	"testing"

	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/provider"
)

// recordingProvider captures the last ChatRequest it received without
// actually issuing a network call. Sufficient for verifying that the
// tunedProvider wrapper layered the right values on top.
type recordingProvider struct {
	last provider.ChatRequest
}

func (r *recordingProvider) Name() string { return "recording" }

func (r *recordingProvider) Models(_ context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (r *recordingProvider) ChatStream(_ context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	r.last = req
	ch := make(chan provider.StreamEvent)
	close(ch)
	return ch, nil
}

func TestTunedProvider_InjectsTemperatureAndThinkOff(t *testing.T) {
	rec := &recordingProvider{}
	tp := newTunedProvider(rec, false, 0.25)
	if _, err := tp.ChatStream(context.Background(), provider.ChatRequest{System: "Be concise."}); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if rec.last.Temperature == nil || *rec.last.Temperature != 0.25 {
		t.Errorf("temperature = %v, want 0.25", rec.last.Temperature)
	}
	wantSystem := "/no_think\n\nBe concise."
	if rec.last.System != wantSystem {
		t.Errorf("system = %q, want %q", rec.last.System, wantSystem)
	}
}

func TestTunedProvider_ThinkOnReplacesNoThink(t *testing.T) {
	rec := &recordingProvider{}
	tp := newTunedProvider(rec, true, 0.1)
	if _, err := tp.ChatStream(context.Background(), provider.ChatRequest{System: "/no_think\n\nDo work."}); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	wantSystem := "/think\n\nDo work."
	if rec.last.System != wantSystem {
		t.Errorf("system = %q, want %q", rec.last.System, wantSystem)
	}
}

func TestTunedProvider_LeavesCallerTemperatureAlone(t *testing.T) {
	rec := &recordingProvider{}
	tp := newTunedProvider(rec, false, 0.1)
	pinned := 0.9
	req := provider.ChatRequest{Temperature: &pinned}
	if _, err := tp.ChatStream(context.Background(), req); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if rec.last.Temperature == nil || *rec.last.Temperature != 0.9 {
		t.Errorf("temperature = %v, want 0.9 (unchanged)", rec.last.Temperature)
	}
}

func TestEffectiveWorkerDefaults_RoleOverridesGlobal(t *testing.T) {
	cfg := TemplateConfig{WorkerThinkingEnabled: false, WorkerTemperature: 0.1}
	yes := true
	temp := 0.7
	role := &prompt.Role{Thinking: &yes, Temperature: &temp}
	thinking, temperature := effectiveWorkerDefaults(cfg, role)
	if !thinking {
		t.Error("thinking should follow the role override (true)")
	}
	if temperature != 0.7 {
		t.Errorf("temperature = %v, want 0.7", temperature)
	}
}

func TestEffectiveWorkerDefaults_FallsBackToGlobal(t *testing.T) {
	cfg := TemplateConfig{WorkerThinkingEnabled: true, WorkerTemperature: 0.4}
	thinking, temperature := effectiveWorkerDefaults(cfg, &prompt.Role{})
	if !thinking {
		t.Error("thinking should follow the global default (true)")
	}
	if temperature != 0.4 {
		t.Errorf("temperature = %v, want 0.4", temperature)
	}
}
