package operator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jefflinse/toasters/internal/graphexec"
)

// The lenient/recovery parsing of ask_user "questions" now lives in
// graphexec.ParsePromptQuestions (shared with graph nodes); its shape coverage
// is tested in internal/graphexec/askuser_parse_test.go. The tests below cover
// the operator's askUser handler that consumes it.

// TestAskUser_AcceptsStringQuestions is the regression for the observed bug:
// qwen sent questions as a bare string and the strict unmarshal failed with
// "cannot unmarshal string into Go struct field .questions". ask_user must now
// accept it and forward a single question to the prompt handler.
func TestAskUser_AcceptsStringQuestions(t *testing.T) {
	t.Parallel()

	ot := newTestOperatorTools(t)
	var captured []graphexec.PromptQuestion
	ot.promptUser = func(_ context.Context, _ string, qs []graphexec.PromptQuestion) (string, error) {
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
	var captured []graphexec.PromptQuestion
	ot.promptUser = func(_ context.Context, _ string, qs []graphexec.PromptQuestion) (string, error) {
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
