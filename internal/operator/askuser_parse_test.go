package operator

import (
	"context"
	"encoding/json"
	"testing"
)

func TestParsePromptQuestions_LenientShapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want []PromptQuestion
	}{
		{"empty", ``, nil},
		{"null", `null`, nil},
		{"bare string", `"What DB driver?"`, []PromptQuestion{{Question: "What DB driver?"}}},
		{"blank string", `"   "`, nil},
		{"single object", `{"question":"Which?","options":["a","b"]}`, []PromptQuestion{{Question: "Which?", Options: []string{"a", "b"}}}},
		{"array of strings", `["Q1","Q2"]`, []PromptQuestion{{Question: "Q1"}, {Question: "Q2"}}},
		{"array of objects", `[{"question":"Q1"},{"question":"Q2","options":["x"]}]`, []PromptQuestion{{Question: "Q1"}, {Question: "Q2", Options: []string{"x"}}}},
		{"mixed array", `["Q1",{"question":"Q2"}]`, []PromptQuestion{{Question: "Q1"}, {Question: "Q2"}}},
		// Double-encoded: the whole array packed into a JSON string (the qwen bug).
		{"double-encoded array", `"[{\"question\":\"Q1\",\"options\":[\"a\"]},{\"question\":\"Q2\"}]"`, []PromptQuestion{{Question: "Q1", Options: []string{"a"}}, {Question: "Q2"}}},
		{"double-encoded object", `"{\"question\":\"Q1\"}"`, []PromptQuestion{{Question: "Q1"}}},
		// A genuine free-form question that merely starts with text stays one Q.
		{"plain text question", `"What database driver?"`, []PromptQuestion{{Question: "What database driver?"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parsePromptQuestions(json.RawMessage(c.in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("len = %d, want %d (%+v)", len(got), len(c.want), got)
			}
			for i := range c.want {
				if got[i].Question != c.want[i].Question {
					t.Errorf("[%d] question = %q, want %q", i, got[i].Question, c.want[i].Question)
				}
				if len(got[i].Options) != len(c.want[i].Options) {
					t.Errorf("[%d] options = %v, want %v", i, got[i].Options, c.want[i].Options)
				}
			}
		})
	}
}

// TestAskUser_AcceptsStringQuestions is the regression for the observed bug:
// qwen sent questions as a bare string and the strict unmarshal failed with
// "cannot unmarshal string into Go struct field .questions". ask_user must now
// accept it and forward a single question to the prompt handler.
func TestAskUser_AcceptsStringQuestions(t *testing.T) {
	t.Parallel()

	ot := newTestOperatorTools(t)
	var captured []PromptQuestion
	ot.promptUser = func(_ context.Context, _ string, qs []PromptQuestion) (string, error) {
		captured = qs
		return "ok", nil
	}

	_, err := ot.askUser(context.Background(), json.RawMessage(`{"questions":"What database driver should I use?"}`))
	if err != nil {
		t.Fatalf("askUser rejected string-shaped questions: %v", err)
	}
	if len(captured) != 1 || captured[0].Question != "What database driver should I use?" {
		t.Errorf("captured = %+v, want one question carrying the string", captured)
	}
}
