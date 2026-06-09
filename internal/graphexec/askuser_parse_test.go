package graphexec

import (
	"encoding/json"
	"testing"
)

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
