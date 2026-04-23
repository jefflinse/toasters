package graphexec

import (
	"context"
	"errors"
	"testing"

	"github.com/jefflinse/rhizome"

	"github.com/jefflinse/toasters/defaults"
	"github.com/jefflinse/toasters/internal/provider"
)

// loadBundled reads a bundled graph YAML from defaults/user/graphs/<name>.yaml
// and returns a parsed Definition. Fails the test on any error.
func loadBundled(t *testing.T, name string) *Definition {
	t.Helper()
	path := "user/graphs/" + name + ".yaml"
	f, err := defaults.UserFiles.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	def, err := ParseDefinitionReader(f)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return def
}

// runBundled compiles a bundled graph and runs it with the supplied mock
// provider responses, returning the resulting state and the mock's call count.
func runBundled(t *testing.T, name string, responses [][]provider.StreamEvent) (*TaskState, int) {
	t.Helper()

	def := loadBundled(t, name)

	cfg, mock := templateConfig(responses)
	compiled, err := Compile(def, cfg, nil)
	if err != nil {
		t.Fatalf("Compile %s: %v", name, err)
	}

	state := NewTaskState("j", "t", "/w", "mock", "test-model")
	state.SetArtifact("task.description", "do the thing")

	result, err := compiled.Run(context.Background(), state)
	if err != nil {
		t.Fatalf("Run %s: %v", name, err)
	}
	return result, mock.calls
}

// --- bug-fix.yaml matches BugFixGraph from templates.go ---

func TestBundled_BugFix_HappyPath(t *testing.T) {
	state, calls := runBundled(t, "bug-fix", [][]provider.StreamEvent{
		completeResponse(FindingsOutput{Summary: "found bug"}),
		completeResponse(PlanOutput{Summary: "fix it"}),
		completeResponse(ImplementOutput{Summary: "patch"}),
		completeResponse(TestOutput{Passed: true, Summary: "pass"}),
		completeResponse(ReviewOutput{Approved: true, Feedback: "lgtm"}),
	})
	if calls != 5 {
		t.Errorf("provider called %d times, want 5", calls)
	}
	for _, key := range []string{"investigate.findings", "plan.steps", "implement.summary", "test.results", "review.feedback"} {
		if got := state.GetArtifactString(key); got == "" {
			t.Errorf("artifact %q unset", key)
		}
	}
}

func TestBundled_BugFix_TestFailureRetry(t *testing.T) {
	_, calls := runBundled(t, "bug-fix", [][]provider.StreamEvent{
		completeResponse(FindingsOutput{Summary: "f"}),
		completeResponse(PlanOutput{Summary: "p"}),
		completeResponse(ImplementOutput{Summary: "i1"}),
		completeResponse(TestOutput{Passed: false, Summary: "fail"}),
		completeResponse(ImplementOutput{Summary: "i2"}),
		completeResponse(TestOutput{Passed: true, Summary: "ok"}),
		completeResponse(ReviewOutput{Approved: true, Feedback: "ok"}),
	})
	if calls != 7 {
		t.Errorf("provider called %d times, want 7", calls)
	}
}

func TestBundled_BugFix_ReviewRejectionRetry(t *testing.T) {
	_, calls := runBundled(t, "bug-fix", [][]provider.StreamEvent{
		completeResponse(FindingsOutput{Summary: "f"}),
		completeResponse(PlanOutput{Summary: "p"}),
		completeResponse(ImplementOutput{Summary: "i1"}),
		completeResponse(TestOutput{Passed: true, Summary: "pass"}),
		completeResponse(ReviewOutput{Approved: false, Feedback: "fix more"}),
		completeResponse(ImplementOutput{Summary: "i2"}),
		completeResponse(TestOutput{Passed: true, Summary: "still pass"}),
		completeResponse(ReviewOutput{Approved: true, Feedback: "ok"}),
	})
	if calls != 8 {
		t.Errorf("provider called %d times, want 8", calls)
	}
}

// --- new-feature.yaml matches NewFeatureGraph ---

func TestBundled_NewFeature_HappyPath(t *testing.T) {
	_, calls := runBundled(t, "new-feature", [][]provider.StreamEvent{
		completeResponse(PlanOutput{Summary: "plan"}),
		completeResponse(ImplementOutput{Summary: "impl"}),
		completeResponse(TestOutput{Passed: true, Summary: "ok"}),
		completeResponse(ReviewOutput{Approved: true, Feedback: "lgtm"}),
	})
	if calls != 4 {
		t.Errorf("provider called %d times, want 4", calls)
	}
}

// --- prototype.yaml matches PrototypeGraph ---

func TestBundled_Prototype_HitsCycleCap(t *testing.T) {
	def := loadBundled(t, "prototype")
	cfg, _ := templateConfig([][]provider.StreamEvent{
		completeResponse(ImplementOutput{Summary: "1"}),
		completeResponse(TestOutput{Passed: false, Summary: "fail"}),
		completeResponse(ImplementOutput{Summary: "2"}),
		completeResponse(TestOutput{Passed: false, Summary: "fail"}),
		completeResponse(ImplementOutput{Summary: "3"}),
		completeResponse(TestOutput{Passed: false, Summary: "fail"}),
	})
	compiled, err := Compile(def, cfg, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	state := NewTaskState("j", "t", "/w", "mock", "test-model")
	if _, err := compiled.Run(context.Background(), state); !errors.Is(err, rhizome.ErrCycleLimit) {
		t.Errorf("err = %v, want ErrCycleLimit", err)
	}
}

func TestBundled_AllGraphsCompile(t *testing.T) {
	entries, err := defaults.UserFiles.ReadDir("user/graphs")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no bundled graphs found — expected at least one")
	}

	cfg := TemplateConfig{}
	for _, e := range entries {
		if e.IsDir() || !hasSuffix(e.Name(), ".yaml") {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			name := e.Name()[:len(e.Name())-len(".yaml")]
			def := loadBundled(t, name)
			if _, err := Compile(def, cfg, nil); err != nil {
				t.Errorf("Compile %s: %v", name, err)
			}
		})
	}
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
