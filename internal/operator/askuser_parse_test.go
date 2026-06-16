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
		// Truncated array (missing closing ']') — recover the complete elements
		// rather than dumping the raw JSON as one question.
		{"truncated array", `[{"question":"Q1","options":["a"]},{"question":"Q2"}`, []PromptQuestion{{Question: "Q1", Options: []string{"a"}}, {Question: "Q2"}}},
		// Double-encoded AND truncated — the exact qwen failure from the
		// screenshot: the whole array packed into a string, missing its ']'.
		{"double-encoded truncated array", `"[{\"question\":\"Q1\",\"options\":[\"a\"]},{\"question\":\"Q2\"}"`, []PromptQuestion{{Question: "Q1", Options: []string{"a"}}, {Question: "Q2"}}},
		// Array whose final element is cut off mid-object — keep the complete ones.
		{"array with truncated tail element", `[{"question":"Q1"},{"question":"Q2"},{"ques`, []PromptQuestion{{Question: "Q1"}, {Question: "Q2"}}},
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

// TestAskUser_DoubleEncodedTruncatedArray is the regression for the workout
// blocker: qwen packed five {question,options} objects into the questions
// STRING field and the array was truncated (no closing ']'). The old strict
// path dumped the entire raw JSON as a single question; ask_user must now
// recover all five distinct questions with their options.
func TestAskUser_DoubleEncodedTruncatedArray(t *testing.T) {
	t.Parallel()

	ot := newTestOperatorTools(t)
	var captured []PromptQuestion
	ot.promptUser = func(_ context.Context, _ string, qs []PromptQuestion) (string, error) {
		captured = qs
		return "ok", nil
	}

	// Verbatim from the persisted operator session: questions is a JSON
	// string holding the array, missing its trailing ']'.
	const inner = `[{"question": "What equipment do you have access to?", "options": ["Full gym membership", "Minimal/no equipment (bodyweight only)"]}, ` +
		`{"question": "How many days per week can you realistically commit to working out?", "options": ["3 days", "4 days", "5 days"]}, ` +
		`{"question": "How long can each session be?", "options": ["30 minutes", "45 minutes", "60 minutes", "Flexible"]}, ` +
		`{"question": "Do you have any injuries, health conditions, or physical limitations I should know about?", "options": ["No known issues", "Back problems"]}, ` +
		`{"question": "What's your primary goal right now?", "options": ["Lose weight / fat loss", "All of the above"]}`
	wrapped, err := json.Marshal(map[string]string{"questions": inner})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ot.askUser(context.Background(), json.RawMessage(wrapped)); err != nil {
		t.Fatalf("askUser rejected double-encoded truncated array: %v", err)
	}
	if len(captured) != 5 {
		t.Fatalf("recovered %d questions, want 5: %+v", len(captured), captured)
	}
	if captured[0].Question != "What equipment do you have access to?" {
		t.Errorf("Q1 = %q, want the equipment question (not raw JSON)", captured[0].Question)
	}
	if len(captured[2].Options) != 4 {
		t.Errorf("Q3 options = %v, want 4", captured[2].Options)
	}
	for _, q := range captured {
		if len(q.Question) > 0 && q.Question[0] == '[' {
			t.Errorf("question carries raw JSON instead of text: %q", q.Question)
		}
	}
}
