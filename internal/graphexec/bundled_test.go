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
	state.SetArtifact("task.toolchain", "go")

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
	state.SetArtifact("task.toolchain", "go")
	if _, err := compiled.Run(context.Background(), state); !errors.Is(err, rhizome.ErrCycleLimit) {
		t.Errorf("err = %v, want ErrCycleLimit", err)
	}
}

func TestBundled_AllGraphsCompile(t *testing.T) {
	cfg, _ := templateConfig(t, nil)

	type catalog struct {
		root     string
		fs       func(path string) ([]byte, error)
		readDir  func(path string) ([]string, error)
		loadFunc func(*testing.T, string) *Definition
	}

	userCat := catalog{
		root: "user/graphs",
		readDir: func(p string) ([]string, error) {
			entries, err := defaults.UserFiles.ReadDir(p)
			if err != nil {
				return nil, err
			}
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
					names = append(names, e.Name())
				}
			}
			return names, nil
		},
		loadFunc: loadBundled,
	}
	sysCat := catalog{
		root: "system/graphs",
		readDir: func(p string) ([]string, error) {
			entries, err := defaults.SystemFiles.ReadDir(p)
			if err != nil {
				return nil, err
			}
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
					names = append(names, e.Name())
				}
			}
			return names, nil
		},
		loadFunc: loadBundledSystem,
	}

	for _, c := range []catalog{userCat, sysCat} {
		names, err := c.readDir(c.root)
		if err != nil {
			t.Fatalf("ReadDir %s: %v", c.root, err)
		}
		if len(names) == 0 {
			t.Fatalf("no bundled graphs found under %s", c.root)
		}
		for _, name := range names {
			file := name
			t.Run(c.root+"/"+file, func(t *testing.T) {
				id := strings.TrimSuffix(file, ".yaml")
				def := c.loadFunc(t, id)
				if _, err := Compile(def, cfg, nil); err != nil {
					t.Errorf("Compile %s/%s: %v", c.root, id, err)
				}
			})
		}
	}
}

// --- system/graphs/coarse-decompose & fine-decompose run-through ---

func TestBundled_CoarseDecompose_EmitsTasks(t *testing.T) {
	def := loadBundledSystem(t, "coarse-decompose")
	cfg, _ := templateConfig(t, [][]provider.StreamEvent{
		completeJSON(`{
		  "tasks": [
		    {"title": "t1", "description": "do first thing", "depends_on": []},
		    {"title": "t2", "description": "do second thing", "depends_on": [0]}
		  ],
		  "reason": "split by layer"
		}`),
	})
	compiled, err := Compile(def, cfg, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	state := NewTaskState("j", "t", "/w", "mock", "test-model")
	state.SetArtifact("job.description", "build X")
	state.ExitNode = def.Exit

	result, err := compiled.Run(context.Background(), state)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	out := exitNodeOutput(result)
	if len(out) == 0 {
		t.Fatal("no exit-node output recorded")
	}
	if !strings.Contains(string(out), `"t1"`) || !strings.Contains(string(out), `"t2"`) {
		t.Errorf("output missing task titles: %s", out)
	}
}

func TestBundled_FineDecompose_EmitsGraphID(t *testing.T) {
	def := loadBundledSystem(t, "fine-decompose")
	cfg, _ := templateConfig(t, [][]provider.StreamEvent{
		completeJSON(`{"graph_id":"go-feature","reason":"Go implementation task"}`),
	})
	compiled, err := Compile(def, cfg, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	state := NewTaskState("j", "t", "/w", "mock", "test-model")
	state.SetArtifact("task.description", "implement thing in Go")
	state.ExitNode = def.Exit

	result, err := compiled.Run(context.Background(), state)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	out := exitNodeOutput(result)
	if !strings.Contains(string(out), `"go-feature"`) {
		t.Errorf("output missing graph_id: %s", out)
	}
}

// loadBundledSystem reads a bundled graph from defaults/system/graphs/.
func loadBundledSystem(t *testing.T, name string) *Definition {
	t.Helper()
	path := "system/graphs/" + name + ".yaml"
	f, err := defaults.SystemFiles.Open(path)
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
