package graphexec

import (
	"context"
	"strings"
	"testing"

	"github.com/jefflinse/rhizome"
)

func TestRoleRegistry_DefaultBuilders(t *testing.T) {
	r := NewRoleRegistry()
	for _, name := range []string{"investigator", "planner", "implementer", "tester", "reviewer"} {
		if _, err := r.Build(name, TemplateConfig{}); err != nil {
			t.Errorf("Build(%q): %v", name, err)
		}
	}
}

func TestRoleRegistry_UnknownRoleListsAvailable(t *testing.T) {
	r := NewRoleRegistry()
	_, err := r.Build("does-not-exist", TemplateConfig{})
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
	fakeBuilder := func(TemplateConfig) rhizome.NodeFunc[*TaskState] {
		called = true
		return func(_ context.Context, s *TaskState) (*TaskState, error) { return s, nil }
	}
	r.Register("investigator", fakeBuilder)

	if _, err := r.Build("investigator", TemplateConfig{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !called {
		t.Error("override was not invoked")
	}
}

func TestRoleRegistry_NamesSorted(t *testing.T) {
	r := NewRoleRegistry()
	names := r.Names()
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Errorf("Names() not sorted: %q > %q", names[i-1], names[i])
		}
	}
}
