package graphexec

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jefflinse/rhizome"

	"github.com/jefflinse/toasters/internal/provider"
)

// tempCaptureProvider records the temperature on each ChatStream request and
// replies with a fixed response.
type tempCaptureProvider struct {
	mu    sync.Mutex
	temps []float64
	resp  []provider.StreamEvent
}

func (c *tempCaptureProvider) ChatStream(_ context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	c.mu.Lock()
	if req.Temperature != nil {
		c.temps = append(c.temps, *req.Temperature)
	}
	c.mu.Unlock()
	ch := make(chan provider.StreamEvent, len(c.resp)+1)
	for _, e := range c.resp {
		ch <- e
	}
	ch <- provider.StreamEvent{Type: provider.EventDone}
	close(ch)
	return ch, nil
}

func (c *tempCaptureProvider) Models(_ context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (c *tempCaptureProvider) Name() string { return "mock" }

func (c *tempCaptureProvider) recorded() []float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]float64(nil), c.temps...)
}

// br is a successful BranchResult carrying the given JSON object.
func br(index int, obj map[string]any) rhizome.BranchResult[json.RawMessage] {
	raw, _ := json.Marshal(obj)
	return rhizome.BranchResult[json.RawMessage]{Index: index, Value: raw}
}

// brErr is a failed BranchResult.
func brErr(index int, err error) rhizome.BranchResult[json.RawMessage] {
	return rhizome.BranchResult[json.RawMessage]{Index: index, Err: err}
}

// --- Mechanical reducer unit tests ---

func TestReduceFirstSuccess(t *testing.T) {
	out, winner, err := reduceFirstSuccess([]rhizome.BranchResult[json.RawMessage]{
		brErr(0, context.Canceled),
		br(1, map[string]any{"summary": "second"}),
		br(2, map[string]any{"summary": "third"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if winner != 1 {
		t.Fatalf("winner = %d, want 1", winner)
	}
	var o map[string]any
	_ = json.Unmarshal(out, &o)
	if o["summary"] != "second" {
		t.Fatalf("output = %v, want second", o)
	}
}

func TestReduceFirstSuccess_AllFail(t *testing.T) {
	_, _, err := reduceFirstSuccess([]rhizome.BranchResult[json.RawMessage]{brErr(0, context.Canceled)})
	if err == nil {
		t.Fatal("expected error when all branches fail")
	}
}

func TestReduceMajority(t *testing.T) {
	results := []rhizome.BranchResult[json.RawMessage]{
		br(0, map[string]any{"approved": true, "feedback": "a"}),
		br(1, map[string]any{"approved": false, "feedback": "b"}),
		br(2, map[string]any{"approved": true, "feedback": "c"}),
	}
	out, winner, err := reduceMajority(results, "approved", 0)
	if err != nil {
		t.Fatal(err)
	}
	if winner != 0 { // first branch carrying the winning value (true)
		t.Fatalf("winner = %d, want 0", winner)
	}
	var o map[string]any
	_ = json.Unmarshal(out, &o)
	if o["approved"] != true {
		t.Fatalf("approved = %v, want true", o["approved"])
	}
}

func TestReduceMajority_QuorumNotMet(t *testing.T) {
	results := []rhizome.BranchResult[json.RawMessage]{
		br(0, map[string]any{"approved": true}),
		br(1, map[string]any{"approved": false}),
	}
	if _, _, err := reduceMajority(results, "approved", 2); err == nil {
		t.Fatal("expected quorum error (best value had 1 vote, quorum 2)")
	}
}

func TestReduceMajority_IgnoresFailedBranches(t *testing.T) {
	results := []rhizome.BranchResult[json.RawMessage]{
		brErr(0, context.Canceled),
		br(1, map[string]any{"severity": "high"}),
		br(2, map[string]any{"severity": "high"}),
	}
	_, winner, err := reduceMajority(results, "severity", 2)
	if err != nil {
		t.Fatal(err)
	}
	if winner != 1 {
		t.Fatalf("winner = %d, want 1", winner)
	}
}

func TestReduceCollect(t *testing.T) {
	out, winner, err := reduceCollect([]rhizome.BranchResult[json.RawMessage]{
		br(0, map[string]any{"n": 1}),
		brErr(1, context.Canceled),
		br(2, map[string]any{"n": 3}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if winner != -1 {
		t.Fatalf("winner = %d, want -1 (collect has no single winner)", winner)
	}
	var o struct {
		Branches []map[string]any `json:"branches"`
	}
	_ = json.Unmarshal(out, &o)
	if len(o.Branches) != 2 { // only successful branches collected
		t.Fatalf("collected %d branches, want 2", len(o.Branches))
	}
}

// --- Integration: read-only majority via the real reviewer role ---

func TestFanout_ReadOnlyMajority_RealReviewer(t *testing.T) {
	cfg, _ := templateConfig(t, [][]provider.StreamEvent{
		reviewResp(true, "looks good"),
		reviewResp(false, "nope"),
		reviewResp(true, "fine"),
	})

	node := Node{ID: "review", Fanout: &Fanout{
		Count:  3,
		Branch: &FanoutBranch{Role: "code-reviewer", Slots: map[string]string{"toolchain": "go"}},
		Reduce: &Reduce{Strategy: ReduceMajority, Key: "approved"},
	}}

	fn, schemaRole, err := buildFanoutNode("g", node, cfg, NewRoleRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if schemaRole == nil || schemaRole.Output != "review-decision" {
		t.Fatalf("schema role = %+v, want code-reviewer (review-decision)", schemaRole)
	}

	state := NewTaskState("j", "t", t.TempDir(), "mock", "test-model")
	state.SetArtifact("task.description", "review the change")

	out, err := fn(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	var dec map[string]any
	_ = json.Unmarshal(out.GetNodeOutput("review"), &dec)
	if dec["approved"] != true { // 2 of 3 approved
		t.Fatalf("approved = %v, want true (majority)", dec["approved"])
	}
}

// --- Integration: write-role first_success isolates and promotes ---

// stubWriter returns a RoleBuilder that simulates a write-role coder: it writes
// a file into its (isolated) workspace and emits a structured output.
func stubWriter(t *testing.T) RoleBuilder {
	return func(_ TemplateConfig, nodeID string, _ map[string]string) rhizome.NodeFunc[*TaskState] {
		return func(ctx context.Context, s *TaskState) (*TaskState, error) {
			id := effectiveNodeID(ctx, nodeID)
			if err := os.WriteFile(filepath.Join(s.WorkspaceDir, "result.txt"), []byte("by "+id), 0o644); err != nil {
				return s, err
			}
			if err := s.SetNodeOutput(id, map[string]any{"summary": "done by " + id, "label": id}); err != nil {
				return s, err
			}
			return s, nil
		}
	}
}

func TestFanout_WriteFirstSuccess_PromotesWinner(t *testing.T) {
	cfg := TemplateConfig{PromptEngine: testEngine(t)} // engine resolves coder => write access
	reg := NewRoleRegistry()
	reg.Register("coder", stubWriter(t)) // stub behavior; access still from engine

	node := Node{ID: "impl", Fanout: &Fanout{
		Count:  3,
		Branch: &FanoutBranch{Role: "coder"},
		Reduce: &Reduce{Strategy: ReduceFirstSuccess},
	}}

	fn, _, err := buildFanoutNode("g", node, cfg, reg)
	if err != nil {
		t.Fatal(err)
	}

	base := t.TempDir()
	state := NewTaskState("j", "t", base, "mock", "test-model")

	out, err := fn(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}

	// first_success winner is branch 0 ("impl#0"); its file must be promoted
	// back to the base workspace, and the node output must be its output.
	got, err := os.ReadFile(filepath.Join(base, "result.txt"))
	if err != nil {
		t.Fatalf("winner's file not promoted to base: %v", err)
	}
	if string(got) != "by impl#0" {
		t.Fatalf("promoted file = %q, want %q", got, "by impl#0")
	}
	var o map[string]any
	_ = json.Unmarshal(out.GetNodeOutput("impl"), &o)
	if o["label"] != "impl#0" {
		t.Fatalf("node output label = %v, want impl#0", o["label"])
	}
}

// --- Integration: reduce.role judge selects a winner (write) ---

// stubJudge picks the candidate whose output has the largest "rank" and returns
// its index as the winner.
func stubJudge() RoleBuilder {
	return func(_ TemplateConfig, nodeID string, _ map[string]string) rhizome.NodeFunc[*TaskState] {
		return func(ctx context.Context, s *TaskState) (*TaskState, error) {
			id := effectiveNodeID(ctx, nodeID)
			var cands []fanoutCandidate
			_ = json.Unmarshal([]byte(s.GetArtifactString(candidatesArtifact)), &cands)
			bestIdx, bestRank := -1, -1.0
			for _, c := range cands {
				var o map[string]any
				_ = json.Unmarshal(c.Output, &o)
				if r, ok := o["rank"].(float64); ok && r > bestRank {
					bestRank, bestIdx = r, c.Index
				}
			}
			if err := s.SetNodeOutput(id, map[string]any{"winner": bestIdx}); err != nil {
				return s, err
			}
			return s, nil
		}
	}
}

func TestFanout_JudgeSelection_PromotesChosen(t *testing.T) {
	cfg := TemplateConfig{PromptEngine: testEngine(t)}
	reg := NewRoleRegistry()
	// Branch writes a file and ranks itself by its branch index (so branch 2 wins).
	reg.Register("coder", func(_ TemplateConfig, nodeID string, _ map[string]string) rhizome.NodeFunc[*TaskState] {
		return func(ctx context.Context, s *TaskState) (*TaskState, error) {
			id := effectiveNodeID(ctx, nodeID)
			if err := os.WriteFile(filepath.Join(s.WorkspaceDir, "result.txt"), []byte("by "+id), 0o644); err != nil {
				return s, err
			}
			// rank = the trailing branch index, so #2 ranks highest.
			rank := float64(id[len(id)-1] - '0')
			return s, s.SetNodeOutput(id, map[string]any{"summary": id, "rank": rank})
		}
	})
	reg.Register("judge", stubJudge())

	node := Node{ID: "impl", Fanout: &Fanout{
		Count:  3,
		Branch: &FanoutBranch{Role: "coder"},
		Reduce: &Reduce{Role: "judge"},
	}}

	fn, _, err := buildFanoutNode("g", node, cfg, reg)
	if err != nil {
		t.Fatal(err)
	}

	base := t.TempDir()
	state := NewTaskState("j", "t", base, "mock", "test-model")

	if _, err := fn(context.Background(), state); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(base, "result.txt"))
	if err != nil {
		t.Fatalf("chosen winner's file not promoted: %v", err)
	}
	if string(got) != "by impl#2" { // judge picks highest rank => branch 2
		t.Fatalf("promoted file = %q, want %q", got, "by impl#2")
	}
}

// --- Integration: reduce.role aggregator merges (read-only) ---

func TestFanout_JudgeMerge_ReadOnly(t *testing.T) {
	cfg := TemplateConfig{PromptEngine: testEngine(t)}
	reg := NewRoleRegistry()
	// Read-only branch role (investigator has readonly access in defaults).
	reg.Register("investigator", func(_ TemplateConfig, nodeID string, _ map[string]string) rhizome.NodeFunc[*TaskState] {
		return func(ctx context.Context, s *TaskState) (*TaskState, error) {
			id := effectiveNodeID(ctx, nodeID)
			return s, s.SetNodeOutput(id, map[string]any{"finding": id})
		}
	})
	// Aggregator merges candidates into a single synthesized output.
	reg.Register("aggregator", func(_ TemplateConfig, nodeID string, _ map[string]string) rhizome.NodeFunc[*TaskState] {
		return func(ctx context.Context, s *TaskState) (*TaskState, error) {
			id := effectiveNodeID(ctx, nodeID)
			var cands []fanoutCandidate
			_ = json.Unmarshal([]byte(s.GetArtifactString(candidatesArtifact)), &cands)
			return s, s.SetNodeOutput(id, map[string]any{"merged_count": len(cands), "approved": true})
		}
	})

	node := Node{ID: "review", Fanout: &Fanout{
		Count:  3,
		Branch: &FanoutBranch{Role: "investigator"},
		Reduce: &Reduce{Role: "aggregator"},
	}}

	fn, _, err := buildFanoutNode("g", node, cfg, reg)
	if err != nil {
		t.Fatal(err)
	}

	state := NewTaskState("j", "t", t.TempDir(), "mock", "test-model")
	out, err := fn(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	var o map[string]any
	_ = json.Unmarshal(out.GetNodeOutput("review"), &o)
	if o["merged_count"] != float64(3) { // node output is the judge's merged output
		t.Fatalf("merged_count = %v, want 3", o["merged_count"])
	}
}

// --- Per-branch overrides: diverse-temperature consensus ---

func TestFanout_Branches_PerBranchTemperature(t *testing.T) {
	cap := &tempCaptureProvider{resp: reviewResp(true, "ok")}
	cfg := TemplateConfig{
		Provider:     cap,
		ToolExecutor: &mockToolExecutor{},
		Model:        "test-model",
		PromptEngine: testEngine(t),
	}

	t02, t08 := 0.2, 0.8
	node := Node{ID: "review", Fanout: &Fanout{
		Branches: []FanoutBranch{
			{Role: "code-reviewer", Slots: map[string]string{"toolchain": "go"}, Temperature: &t02},
			{Role: "code-reviewer", Slots: map[string]string{"toolchain": "go"}, Temperature: &t08},
		},
		Reduce: &Reduce{Strategy: ReduceMajority, Key: "approved"},
	}}

	fn, _, err := buildFanoutNode("g", node, cfg, NewRoleRegistry())
	if err != nil {
		t.Fatal(err)
	}

	state := NewTaskState("j", "t", t.TempDir(), "mock", "test-model")
	state.SetArtifact("task.description", "review the change")

	if _, err := fn(context.Background(), state); err != nil {
		t.Fatal(err)
	}

	got := cap.recorded()
	if len(got) != 2 {
		t.Fatalf("recorded %d temperatures, want 2: %v", len(got), got)
	}
	want := map[float64]bool{0.2: true, 0.8: true}
	for _, temp := range got {
		if !want[temp] {
			t.Errorf("branch ran at unexpected temperature %v (want 0.2 or 0.8)", temp)
		}
		delete(want, temp)
	}
	if len(want) != 0 {
		t.Errorf("missing per-branch temperatures: %v", want)
	}
}

func TestValidateFanout_Forms(t *testing.T) {
	good := 0.5
	tooHot := 3.0
	cases := []struct {
		name    string
		f       *Fanout
		wantErr string
	}{
		{"both forms set", &Fanout{Count: 2, Branch: &FanoutBranch{Role: "coder"}, Branches: []FanoutBranch{{Role: "coder"}}, Reduce: &Reduce{Strategy: ReduceFirstSuccess}}, "pick one"},
		{"neither form set", &Fanout{Reduce: &Reduce{Strategy: ReduceFirstSuccess}}, "must set count+branch or branches"},
		{"empty branch role", &Fanout{Branches: []FanoutBranch{{Role: ""}}, Reduce: &Reduce{Strategy: ReduceFirstSuccess}}, "role is required"},
		{"temperature out of range", &Fanout{Branches: []FanoutBranch{{Role: "coder", Temperature: &tooHot}}, Reduce: &Reduce{Strategy: ReduceFirstSuccess}}, "temperature must be in [0, 2]"},
		{"valid branches form", &Fanout{Branches: []FanoutBranch{{Role: "coder", Temperature: &good}}, Reduce: &Reduce{Strategy: ReduceFirstSuccess}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFanout("g", "n", tc.f)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

// --- Resilient reduce: a failing judge falls back to first_success ---

func TestFanout_JudgeFailure_FallsBackToFirstSuccess(t *testing.T) {
	cfg := TemplateConfig{PromptEngine: testEngine(t)}
	reg := NewRoleRegistry()
	reg.Register("coder", stubWriter(t))
	// A judge that always errors: after judgeMaxAttempts retries, reduce must
	// fall back to first_success rather than failing the (successful) branches.
	var judgeCalls int
	reg.Register("flaky-judge", func(_ TemplateConfig, _ string, _ map[string]string) rhizome.NodeFunc[*TaskState] {
		return func(_ context.Context, s *TaskState) (*TaskState, error) {
			judgeCalls++
			return s, errors.New("judge boom")
		}
	})

	node := Node{ID: "impl", Fanout: &Fanout{
		Count:  2,
		Branch: &FanoutBranch{Role: "coder"},
		Reduce: &Reduce{Role: "flaky-judge"},
	}}

	fn, _, err := buildFanoutNode("g", node, cfg, reg)
	if err != nil {
		t.Fatal(err)
	}

	base := t.TempDir()
	state := NewTaskState("j", "t", base, "mock", "test-model")

	if _, err := fn(context.Background(), state); err != nil {
		t.Fatalf("node should succeed via fallback, got: %v", err)
	}
	if judgeCalls != judgeMaxAttempts {
		t.Errorf("judge called %d times, want %d (retries before fallback)", judgeCalls, judgeMaxAttempts)
	}
	// first_success fallback promotes branch 0's workspace.
	got, err := os.ReadFile(filepath.Join(base, "result.txt"))
	if err != nil {
		t.Fatalf("fallback winner not promoted: %v", err)
	}
	if string(got) != "by impl#0" {
		t.Fatalf("promoted file = %q, want %q", got, "by impl#0")
	}
}

// --- Compile-time rejection: collect cannot drive a write-role fan-out ---

func TestBuildFanout_WriteCollectRejected(t *testing.T) {
	cfg := TemplateConfig{PromptEngine: testEngine(t)}
	reg := NewRoleRegistry()
	reg.Register("coder", stubWriter(t))

	node := Node{ID: "impl", Fanout: &Fanout{
		Count:  2,
		Branch: &FanoutBranch{Role: "coder"},
		Reduce: &Reduce{Strategy: ReduceCollect},
	}}

	if _, _, err := buildFanoutNode("g", node, cfg, reg); err == nil {
		t.Fatal("expected error: collect cannot select a winner for a write role")
	}
}
