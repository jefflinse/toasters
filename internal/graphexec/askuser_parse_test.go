package graphexec

import (
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
			got, err := ParsePromptQuestions(json.RawMessage(c.in))
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

func TestParseAskUserPayload_LenientShapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		in        string
		wantQs    []string
		wantQuest string
	}{
		{"string questions", `{"questions":"What driver?"}`, []string{"What driver?"}, ""},
		{"array of strings", `{"questions":["Q1","Q2"]}`, []string{"Q1", "Q2"}, ""},
		{"array of objects", `{"questions":[{"question":"Q1"}]}`, []string{"Q1"}, ""},
		{"single object", `{"questions":{"question":"Q1"}}`, []string{"Q1"}, ""},
		{"single-question shorthand", `{"question":"Just one?"}`, nil, "Just one?"},
		{"double-encoded questions string", `{"questions":"[{\"question\":\"Q1\"},{\"question\":\"Q2\"}]"}`, []string{"Q1", "Q2"}, ""},
		{"array packed into question field", `{"question":"[{\"question\":\"Q1\"}]"}`, []string{"Q1"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseAskUserPayload(json.RawMessage(c.in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Question != c.wantQuest {
				t.Errorf("Question = %q, want %q", got.Question, c.wantQuest)
			}
			if len(got.Questions) != len(c.wantQs) {
				t.Fatalf("Questions len = %d, want %d (%+v)", len(got.Questions), len(c.wantQs), got.Questions)
			}
			for i, want := range c.wantQs {
				if got.Questions[i].Question != want {
					t.Errorf("[%d] = %q, want %q", i, got.Questions[i].Question, want)
				}
			}
		})
	}
}
