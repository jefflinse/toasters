package operator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/kb"
)

// fakeKB is a test double implementing operator.KnowledgeBase.
type fakeKB struct {
	recallHits  []kb.Hit
	recallErr   error
	rememberID  string
	rememberErr error

	lastRecallQuery     string
	lastRecallK         int
	lastRememberContent string
	lastRememberSource  string
}

func (f *fakeKB) Recall(_ context.Context, _ string, query string, k int) ([]kb.Hit, error) {
	f.lastRecallQuery = query
	f.lastRecallK = k
	if f.recallErr != nil {
		return nil, f.recallErr
	}
	return f.recallHits, nil
}

func (f *fakeKB) Remember(_ context.Context, _ string, source string, content string) (string, error) {
	f.lastRememberContent = content
	f.lastRememberSource = source
	if f.rememberErr != nil {
		return "", f.rememberErr
	}
	return f.rememberID, nil
}

func newKBTestOperatorTools(t *testing.T, kbSvc KnowledgeBase) *operatorTools {
	t.Helper()
	ot := newTestOperatorTools(t)
	ot.kb = kbSvc
	return ot
}

func TestKBSearchFormatsHitsWithScores(t *testing.T) {
	fake := &fakeKB{
		recallHits: []kb.Hit{
			{ID: "1", Content: "Always run the linter and gofmt before committing.", Source: "user", Score: 0.71},
			{ID: "2", Content: "Prefer small, single-concern pull requests.", Source: "user", Score: 0.58},
		},
	}
	ot := newKBTestOperatorTools(t, fake)

	args, err := json.Marshal(map[string]any{"query": "run tests"})
	assertNoError(t, err)

	result, err := ot.kbSearch(context.Background(), args)
	assertNoError(t, err)

	if fake.lastRecallQuery != "run tests" {
		t.Fatalf("expected query %q to be forwarded, got %q", "run tests", fake.lastRecallQuery)
	}
	if !strings.Contains(result, "run tests") {
		t.Fatalf("expected result to mention the query, got: %s", result)
	}
	if !strings.Contains(result, "[score 0.71]") {
		t.Fatalf("expected result to surface the score, got: %s", result)
	}
	if !strings.Contains(result, "Always run the linter") {
		t.Fatalf("expected result to contain hit content, got: %s", result)
	}
	if !strings.Contains(strings.ToLower(result), "judge each") {
		t.Fatalf("expected result to repeat the skepticism reminder, got: %s", result)
	}
}

func TestKBSearchZeroHits(t *testing.T) {
	fake := &fakeKB{recallHits: nil}
	ot := newKBTestOperatorTools(t, fake)

	args, err := json.Marshal(map[string]any{"query": "something obscure"})
	assertNoError(t, err)

	result, err := ot.kbSearch(context.Background(), args)
	assertNoError(t, err)
	if !strings.Contains(strings.ToLower(result), "no relevant facts") {
		t.Fatalf("expected 'no relevant facts' message, got: %s", result)
	}
	if !strings.Contains(result, "something obscure") {
		t.Fatalf("expected result to echo the query, got: %s", result)
	}
}

func TestKBSearchDegradesOnError(t *testing.T) {
	fake := &fakeKB{recallErr: errors.New("connection refused")}
	ot := newKBTestOperatorTools(t, fake)

	args, err := json.Marshal(map[string]any{"query": "run tests"})
	assertNoError(t, err)

	result, err := ot.kbSearch(context.Background(), args)
	// Key degrade behavior: nil error even though the backend failed.
	assertNoError(t, err)
	if !strings.Contains(strings.ToLower(result), "memory is currently unavailable") {
		t.Fatalf("expected degrade message, got: %s", result)
	}
}

func TestKBWriteUserSuccess(t *testing.T) {
	fake := &fakeKB{rememberID: "abc-123"}
	ot := newKBTestOperatorTools(t, fake)

	args, err := json.Marshal(map[string]any{"content": "The user prefers tabs over spaces."})
	assertNoError(t, err)

	result, err := ot.kbWriteUser(context.Background(), args)
	assertNoError(t, err)
	if !strings.Contains(result, "Remembered") {
		t.Fatalf("expected success message, got: %s", result)
	}
	if !strings.Contains(result, "abc-123") {
		t.Fatalf("expected id in result, got: %s", result)
	}
	if fake.lastRememberSource != "operator" {
		t.Fatalf("expected default source 'operator', got %q", fake.lastRememberSource)
	}
}

func TestKBWriteUserRejectsEmptyContent(t *testing.T) {
	fake := &fakeKB{}
	ot := newKBTestOperatorTools(t, fake)

	args, err := json.Marshal(map[string]any{"content": "   "})
	assertNoError(t, err)

	result, err := ot.kbWriteUser(context.Background(), args)
	assertNoError(t, err)
	if fake.lastRememberContent != "" {
		t.Fatalf("expected Remember not to be called with empty content, but it was called with %q", fake.lastRememberContent)
	}
	if strings.Contains(result, "Remembered") {
		t.Fatalf("did not expect success message for empty content, got: %s", result)
	}
}

func TestKBWriteUserDegradesOnError(t *testing.T) {
	fake := &fakeKB{rememberErr: errors.New("connection refused")}
	ot := newKBTestOperatorTools(t, fake)

	args, err := json.Marshal(map[string]any{"content": "Remember this."})
	assertNoError(t, err)

	result, err := ot.kbWriteUser(context.Background(), args)
	// Key degrade behavior: nil error even though the backend failed.
	assertNoError(t, err)
	if !strings.Contains(strings.ToLower(result), "could not save") {
		t.Fatalf("expected degrade message, got: %s", result)
	}
}

func TestKBSearchRejectsEmptyQueryWithoutError(t *testing.T) {
	fake := &fakeKB{}
	ot := newKBTestOperatorTools(t, fake)

	args, err := json.Marshal(map[string]any{"query": "   "})
	assertNoError(t, err)

	result, err := ot.kbSearch(context.Background(), args)
	// Empty query is exactly the kind of malformed call a small local model
	// can make — degrade to a message, don't propagate an error (which would
	// count against the operator's consecutive-failed-round circuit breaker).
	assertNoError(t, err)
	if fake.lastRecallQuery != "" {
		t.Fatalf("expected Recall not to be called for an empty query, but it was called with %q", fake.lastRecallQuery)
	}
	if strings.Contains(strings.ToLower(result), "result(s) for") {
		t.Fatalf("did not expect a formatted hits result for an empty query, got: %s", result)
	}
}

func TestOperatorDefinitionsOmitKBToolsWhenNil(t *testing.T) {
	ot := newTestOperatorTools(t)
	ot.kb = nil

	for _, td := range ot.Definitions() {
		if td.Name == "kb_search" || td.Name == "kb_write_user" {
			t.Fatalf("expected kb tools to be omitted when kb is nil, found %s", td.Name)
		}
	}
}

func TestOperatorDefinitionsIncludeKBToolsWhenSet(t *testing.T) {
	ot := newKBTestOperatorTools(t, &fakeKB{})

	var foundSearch, foundWrite bool
	for _, td := range ot.Definitions() {
		if td.Name == "kb_search" {
			foundSearch = true
		}
		if td.Name == "kb_write_user" {
			foundWrite = true
		}
	}
	if !foundSearch || !foundWrite {
		t.Fatalf("expected both kb tools when kb is set, foundSearch=%v foundWrite=%v", foundSearch, foundWrite)
	}
}
