package graphexec

import (
	"context"
	"strings"
	"testing"

	"github.com/jefflinse/rhizome"
)

func TestRoleRegistry_ResolvesViaPromptEngine(t *testing.T) {
	cfg, _ := templateConfig(t, nil)
	r := NewRoleRegistry()
	for _, name := range []string{"investigator", "planner", "implementer", "tester", "reviewer", "go-coder", "py-tester", "tui-coder"} {
		if _, err := r.Build(name, name, cfg); err != nil {
			t.Errorf("Build(%q): %v", name, err)
		}
	}
}

func TestRoleRegistry_UnknownRoleListsAvailable(t *testing.T) {
	cfg, _ := templateConfig(t, nil)
	r := NewRoleRegistry()
	_, err := r.Build("does-not-exist", "node", cfg)
	if err == nil {
		t.Fatal("expected error for unknown role")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unknown role") {
		t.Errorf("err = %q, want to contain %q", msg, "unknown role")
	}
	if !strings.Contains(msg, "investigator") {
		t.Errorf("err = %q, want to list available roles", msg)
	}
}

func TestRoleRegistry_RegisterOverrides(t *testing.T) {
	r := NewRoleRegistry()

	called := false
	fakeBuilder := func(_ TemplateConfig, _ string) rhizome.NodeFunc[*TaskState] {
		called = true
		return func(_ context.Context, s *TaskState) (*TaskState, error) { return s, nil }
	}
	r.Register("investigator", fakeBuilder)

	if _, err := r.Build("investigator", "investigate", TemplateConfig{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !called {
		t.Error("override was not invoked")
	}
}

func TestRoleRegistry_NamesSorted(t *testing.T) {
	r := NewRoleRegistry()
	r.Register("zebra", nil)
	r.Register("alpha", nil)
	r.Register("middle", nil)
	names := r.Names()
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Errorf("Names() not sorted: %q > %q", names[i-1], names[i])
		}
	}
}

func TestResolveSchema_DefaultsToSummary(t *testing.T) {
	engine := testEngine(t)
	role := engine.Role("investigator")
	if role == nil {
		t.Fatal("investigator not loaded")
	}
	// Clear the declared output to exercise the default fallback.
	role.Output = ""
	raw, s, err := ResolveSchema(engine, role)
	if err != nil {
		t.Fatalf("ResolveSchema: %v", err)
	}
	if s.Name != "summary" {
		t.Errorf("Name = %q, want summary", s.Name)
	}
	if !strings.Contains(string(raw), `"summary"`) {
		t.Errorf("schema JSON missing summary field: %s", raw)
	}
}
