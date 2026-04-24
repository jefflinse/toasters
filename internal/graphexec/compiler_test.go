package graphexec

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jefflinse/rhizome"

	"github.com/jefflinse/toasters/internal/provider"
)

// compilerTemplate is a convenience wrapper around templateConfig.
func compilerTemplate(t testing.TB, responses [][]provider.StreamEvent) TemplateConfig {
	t.Helper()
	cfg, _ := templateConfig(t, responses)
	return cfg
}

// bugFixDef is the declarative analogue of the bug-fix graph in
// defaults/user/graphs/bug-fix.yaml. If this definition compiles and
// behaves identically under the same provider responses, we have
// confidence the compiler is correct against the runtime surface.
func bugFixDef() *Definition {
	return &Definition{
		ID:    "bug-fix",
		Entry: "investigate",
		Exit:  "review",
		Nodes: []Node{
			{ID: "investigate", Role: "investigator"},
			{ID: "plan", Role: "planner"},
			{ID: "implement", Role: "implementer"},
			{ID: "test", Role: "tester"},
			{ID: "review", Role: "reviewer"},
		},
		Edges: []Edge{
			{From: "investigate", To: "plan"},
			{From: "plan", To: "implement"},
			{From: "implement", To: "test"},
			{From: "test", Router: &Router{
				On: "$test.output.passed",
				Branches: []Branch{
					{When: true, To: "review"},
					{When: false, To: "implement"},
				},
			}},
			{From: "review", Router: &Router{
				On: "$review.output.approved",
				Branches: []Branch{
					{When: true, To: EndNode},
					{When: false, To: "implement"},
				},
			}},
		},
		MaxIterations: 3,
	}
}

func TestCompile_LinearAndConditional_HappyPath(t *testing.T) {
	cfg := compilerTemplate(t, [][]provider.StreamEvent{
		summaryResp("found bug"),
		summaryResp("fix it"),
		summaryResp("done"),
		testResultResp(true, "ok"),
		reviewResp(true, "lgtm"),
	})

	compiled, err := Compile(bugFixDef(), cfg, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	state := NewTaskState("j", "t", "/w", "mock", "test-model")
	state.SetArtifact("task.description", "fix it")

	result, err := compiled.Run(context.Background(), state)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, key := range []string{"investigate.summary", "plan.summary", "implement.summary", "test.summary", "review.feedback"} {
		if got := result.GetArtifactString(key); got == "" {
			t.Errorf("expected artifact %q to be set", key)
		}
	}
}

func TestCompile_RouterRetriesOnFailure(t *testing.T) {
	cfg := compilerTemplate(t, [][]provider.StreamEvent{
		summaryResp("f"),
		summaryResp("p"),
		summaryResp("i1"),
		testResultResp(false, "fail"),
		summaryResp("i2"),
		testResultResp(true, "ok"),
		reviewResp(true, "lgtm"),
	})

	compiled, err := Compile(bugFixDef(), cfg, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	state := NewTaskState("j", "t", "/w", "mock", "test-model")
	state.SetArtifact("task.description", "fix it")

	result, err := compiled.Run(context.Background(), state)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := result.GetArtifactString("implement.summary"); got != "i2" {
		t.Errorf("implement.summary = %q, want i2 (router should have retried)", got)
	}
}

func TestCompile_MaxIterationsCap(t *testing.T) {
	def := &Definition{
		ID:    "proto",
		Entry: "implement",
		Nodes: []Node{
			{ID: "implement", Role: "implementer"},
			{ID: "test", Role: "tester"},
		},
		Edges: []Edge{
			{From: "implement", To: "test"},
			{From: "test", Router: &Router{
				On: "$test.output.passed",
				Branches: []Branch{
					{When: true, To: EndNode},
					{When: false, To: "implement"},
				},
			}},
		},
		MaxIterations: 3,
	}

	cfg := compilerTemplate(t, [][]provider.StreamEvent{
		summaryResp("1"),
		testResultResp(false, "fail"),
		summaryResp("2"),
		testResultResp(false, "fail"),
		summaryResp("3"),
		testResultResp(false, "fail"),
	})

	compiled, err := Compile(def, cfg, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	state := NewTaskState("j", "t", "/w", "mock", "test-model")

	_, err = compiled.Run(context.Background(), state)
	if !errors.Is(err, rhizome.ErrCycleLimit) {
		t.Errorf("err = %v, want ErrCycleLimit", err)
	}
}

func TestCompile_RouterDefault(t *testing.T) {
	def := &Definition{
		ID:    "default-only",
		Entry: "investigate",
		Nodes: []Node{
			{ID: "investigate", Role: "investigator"},
			{ID: "plan", Role: "planner"},
		},
		Edges: []Edge{
			{From: "investigate", Router: &Router{
				On: "$investigate.output.summary",
				Branches: []Branch{
					{When: "never-matches", To: EndNode},
				},
				Default: "plan",
			}},
			{From: "plan", To: EndNode},
		},
	}

	cfg := compilerTemplate(t, [][]provider.StreamEvent{
		summaryResp("anything"),
		summaryResp("plan"),
	})
	compiled, err := Compile(def, cfg, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	state := NewTaskState("j", "t", "/w", "mock", "test-model")
	if _, err := compiled.Run(context.Background(), state); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := state.GetArtifactString("plan.summary"); got != "plan" {
		t.Errorf("plan.summary = %q, want %q (default branch should have fired)", got, "plan")
	}
}

func TestCompile_RouterErrorsWhenNoBranchMatchesAndNoDefault(t *testing.T) {
	def := &Definition{
		ID:    "no-default",
		Entry: "investigate",
		Nodes: []Node{
			{ID: "investigate", Role: "investigator"},
			{ID: "plan", Role: "planner"},
		},
		Edges: []Edge{
			{From: "investigate", Router: &Router{
				On: "$investigate.output.summary",
				Branches: []Branch{
					{When: "never-matches", To: "plan"},
				},
			}},
			{From: "plan", To: EndNode},
		},
	}

	cfg := compilerTemplate(t, [][]provider.StreamEvent{
		summaryResp("something-else"),
	})
	compiled, err := Compile(def, cfg, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	state := NewTaskState("j", "t", "/w", "mock", "test-model")
	_, err = compiled.Run(context.Background(), state)
	if err == nil {
		t.Fatal("Run: expected error for unmatched router value")
	}
	if !strings.Contains(err.Error(), "no branch matched") {
		t.Errorf("err = %v, want to contain %q", err, "no branch matched")
	}
}

func TestCompile_RejectsInvalidDefinition(t *testing.T) {
	_, err := Compile(&Definition{ID: ""}, TemplateConfig{}, nil)
	if err == nil {
		t.Fatal("Compile: expected validation error")
	}
}

func TestCompile_RejectsUnknownRole(t *testing.T) {
	def := &Definition{
		ID:    "g",
		Entry: "a",
		Nodes: []Node{{ID: "a", Role: "does-not-exist"}},
		Edges: []Edge{{From: "a", To: EndNode}},
	}
	cfg := compilerTemplate(t, nil)
	_, err := Compile(def, cfg, nil)
	if err == nil {
		t.Fatal("Compile: expected unknown-role error")
	}
	if !strings.Contains(err.Error(), "unknown role") {
		t.Errorf("err = %v, want unknown-role message", err)
	}
}

func TestCompile_RejectsSubgraphForNow(t *testing.T) {
	def := &Definition{
		ID:    "g",
		Entry: "a",
		Nodes: []Node{{ID: "a", Graph: "other"}},
		Edges: []Edge{{From: "a", To: EndNode}},
	}
	_, err := Compile(def, TemplateConfig{}, nil)
	if err == nil {
		t.Fatal("Compile: expected subgraph-not-supported error")
	}
	if !strings.Contains(err.Error(), "not yet supported") {
		t.Errorf("err = %v, want not-yet-supported message", err)
	}
}

func TestCompile_RouterRejectsUnknownField(t *testing.T) {
	// "$test.output.bogus" is a field not on the test-result schema.
	def := &Definition{
		ID:    "bad-router",
		Entry: "implement",
		Nodes: []Node{
			{ID: "implement", Role: "implementer"},
			{ID: "test", Role: "tester"},
		},
		Edges: []Edge{
			{From: "implement", To: "test"},
			{From: "test", Router: &Router{
				On: "$test.output.bogus",
				Branches: []Branch{
					{When: true, To: EndNode},
					{When: false, To: "implement"},
				},
			}},
		},
	}
	cfg := compilerTemplate(t, nil)
	_, err := Compile(def, cfg, nil)
	if err == nil {
		t.Fatal("Compile: expected router-schema error")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("err = %v, want to mention unknown field", err)
	}
}
