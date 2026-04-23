package graphexec

import (
	"context"
	"errors"
	"strings"
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

	cfg, mock := templateConfig(t, responses)
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

// --- bug-fix.yaml ---

func TestBundled_BugFix_HappyPath(t *testing.T) {
	state, calls := runBundled(t, "bug-fix", [][]provider.StreamEvent{
		summaryResp("found bug"),
		summaryResp("fix it"),
		summaryResp("patch"),
		testResultResp(true, "pass"),
		reviewResp(true, "lgtm"),
	})
	if calls != 5 {
		t.Errorf("provider called %d times, want 5", calls)
	}
	for _, key := range []string{"investigate.summary", "plan.summary", "implement.summary", "test.summary", "review.feedback"} {
		if got := state.GetArtifactString(key); got == "" {
			t.Errorf("artifact %q unset", key)
		}
	}
}

func TestBundled_BugFix_TestFailureRetry(t *testing.T) {
	_, calls := runBundled(t, "bug-fix", [][]provider.StreamEvent{
		summaryResp("f"),
		summaryResp("p"),
		summaryResp("i1"),
		testResultResp(false, "fail"),
		summaryResp("i2"),
		testResultResp(true, "ok"),
		reviewResp(true, "ok"),
	})
	if calls != 7 {
		t.Errorf("provider called %d times, want 7", calls)
	}
}

func TestBundled_BugFix_ReviewRejectionRetry(t *testing.T) {
	_, calls := runBundled(t, "bug-fix", [][]provider.StreamEvent{
		summaryResp("f"),
		summaryResp("p"),
		summaryResp("i1"),
		testResultResp(true, "pass"),
		reviewResp(false, "fix more"),
		summaryResp("i2"),
		testResultResp(true, "still pass"),
		reviewResp(true, "ok"),
	})
	if calls != 8 {
		t.Errorf("provider called %d times, want 8", calls)
	}
}

// --- new-feature.yaml ---

func TestBundled_NewFeature_HappyPath(t *testing.T) {
	_, calls := runBundled(t, "new-feature", [][]provider.StreamEvent{
		summaryResp("plan"),
		summaryResp("impl"),
		testResultResp(true, "ok"),
		reviewResp(true, "lgtm"),
	})
	if calls != 4 {
		t.Errorf("provider called %d times, want 4", calls)
	}
}

// --- prototype.yaml ---

func TestBundled_Prototype_HitsCycleCap(t *testing.T) {
	def := loadBundled(t, "prototype")
	cfg, _ := templateConfig(t, [][]provider.StreamEvent{
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

	cfg, _ := templateConfig(t, nil)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			name := strings.TrimSuffix(e.Name(), ".yaml")
			def := loadBundled(t, name)
			if _, err := Compile(def, cfg, nil); err != nil {
				t.Errorf("Compile %s: %v", name, err)
			}
		})
	}
}
