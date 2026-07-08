package kb

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/db"
)

// fakeEmbedder returns a deterministic vector per input (hashed from the
// input length and byte sum, so different strings reliably produce
// different vectors) and records the inputs it received.
type fakeEmbedder struct {
	inputs []string
	err    error
}

func (f *fakeEmbedder) Embed(_ context.Context, _ string, inputs []string) ([][]float32, error) {
	f.inputs = append(f.inputs, inputs...)
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(inputs))
	for i, in := range inputs {
		out[i] = hashVec(in)
	}
	return out, nil
}

// hashVec deterministically maps a string to a small fixed-dim vector so
// identical strings produce identical (and therefore maximally similar)
// vectors, and different strings produce different ones.
func hashVec(s string) []float32 {
	vec := make([]float32, 8)
	var sum float32
	for i, r := range s {
		sum += float32(r) * float32(i+1)
	}
	for i := range vec {
		vec[i] = sum + float32(i)
	}
	return vec
}

func newTestStore(t *testing.T) *db.VectorStore {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "kb.db"))
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store.VectorStore()
}

func TestRememberRecallRoundTrip(t *testing.T) {
	store := newTestStore(t)
	emb := &fakeEmbedder{}
	svc := New(store, emb, "test-model", "search_document: ", "search_query: ", 5)

	id, err := svc.Remember(context.Background(), "user", "operator", "Always run gofmt before committing.")
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty id")
	}

	hits, err := svc.Recall(context.Background(), "user", "Always run gofmt before committing.", 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hit")
	}
	found := false
	for _, h := range hits {
		if h.Content == "Always run gofmt before committing." {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected recall to return the stored content, got: %+v", hits)
	}
}

func TestPrefixesAppliedToEmbedderInputs(t *testing.T) {
	store := newTestStore(t)
	emb := &fakeEmbedder{}
	svc := New(store, emb, "test-model", "search_document: ", "search_query: ", 5)

	if _, err := svc.Remember(context.Background(), "user", "operator", "some fact"); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if _, err := svc.Recall(context.Background(), "user", "some query", 5); err != nil {
		t.Fatalf("Recall: %v", err)
	}

	if len(emb.inputs) != 2 {
		t.Fatalf("expected 2 embed calls, got %d: %v", len(emb.inputs), emb.inputs)
	}
	if !strings.HasPrefix(emb.inputs[0], "search_document: ") {
		t.Fatalf("expected doc prefix applied, got %q", emb.inputs[0])
	}
	if !strings.HasPrefix(emb.inputs[1], "search_query: ") {
		t.Fatalf("expected query prefix applied, got %q", emb.inputs[1])
	}
}

func TestRecallPropagatesEmbedderError(t *testing.T) {
	store := newTestStore(t)
	emb := &fakeEmbedder{err: errors.New("backend down")}
	svc := New(store, emb, "test-model", "", "", 5)

	_, err := svc.Recall(context.Background(), "user", "some query", 5)
	if err == nil {
		t.Fatal("expected Recall to propagate the embedder error")
	}
}

func TestRecallDefaultTopK(t *testing.T) {
	store := newTestStore(t)
	emb := &fakeEmbedder{}
	svc := New(store, emb, "test-model", "", "", 2)

	for i := 0; i < 5; i++ {
		content := strings.Repeat("x", i+1) + " fact"
		if _, err := svc.Remember(context.Background(), "user", "operator", content); err != nil {
			t.Fatalf("Remember %d: %v", i, err)
		}
	}

	hits, err := svc.Recall(context.Background(), "user", "x fact", 0)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(hits) > 2 {
		t.Fatalf("expected default top_k=2 to cap results, got %d", len(hits))
	}
}
