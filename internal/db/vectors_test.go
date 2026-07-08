package db

import (
	"context"
	"math"
	"sync"
	"testing"
)

// --- encodeVec / decodeVec ---

func TestEncodeDecodeVec_RoundTrip(t *testing.T) {
	vec := []float32{1.5, -2.25, 0, 3.125}
	got, err := decodeVec(encodeVec(vec))
	if err != nil {
		t.Fatalf("decodeVec: %v", err)
	}
	if len(got) != len(vec) {
		t.Fatalf("decodeVec length = %d, want %d", len(got), len(vec))
	}
	for i := range vec {
		if got[i] != vec[i] {
			t.Errorf("decodeVec[%d] = %v, want %v", i, got[i], vec[i])
		}
	}
}

func TestEncodeDecodeVec_Empty(t *testing.T) {
	got, err := decodeVec(encodeVec(nil))
	if err != nil {
		t.Fatalf("decodeVec(empty): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("decodeVec(empty) = %v, want empty", got)
	}
}

func TestDecodeVec_InvalidLength(t *testing.T) {
	if _, err := decodeVec([]byte{1, 2, 3}); err == nil {
		t.Fatal("decodeVec with non-multiple-of-4 length: expected error, got nil")
	}
}

// --- cosine ---

func TestCosine_Identical(t *testing.T) {
	vec := []float32{1, 2, 3}
	got := cosine(vec, vec)
	if math.Abs(float64(got)-1.0) > 1e-6 {
		t.Errorf("cosine(identical) = %v, want ~1.0", got)
	}
}

func TestCosine_Orthogonal(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{0, 1}
	got := cosine(a, b)
	if math.Abs(float64(got)) > 1e-6 {
		t.Errorf("cosine(orthogonal) = %v, want ~0", got)
	}
}

func TestCosine_ZeroVector(t *testing.T) {
	a := []float32{0, 0, 0}
	b := []float32{1, 2, 3}
	got := cosine(a, b)
	if math.IsNaN(float64(got)) {
		t.Fatal("cosine(zero vector) = NaN, want 0 (NaN would corrupt sort ordering)")
	}
	if got != 0 {
		t.Errorf("cosine(zero vector) = %v, want 0", got)
	}
}

func TestCosine_DifferingLengths(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{1, 2}
	if got := cosine(a, b); got != 0 {
		t.Errorf("cosine(differing lengths) = %v, want 0", got)
	}
}

// --- Insert ---

func TestVectorStore_Insert_GeneratesID(t *testing.T) {
	store := openTestStore(t)
	vs := store.VectorStore()
	ctx := context.Background()

	id, err := vs.Insert(ctx, VectorEntry{
		Scope:     "user",
		Model:     "m",
		Content:   "fact one",
		Embedding: []float32{1, 2, 3},
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if id == "" {
		t.Fatal("Insert with empty ID: expected a generated id, got empty string")
	}
}

func TestVectorStore_Insert_PreservesProvidedID(t *testing.T) {
	store := openTestStore(t)
	vs := store.VectorStore()
	ctx := context.Background()

	id, err := vs.Insert(ctx, VectorEntry{
		ID:        "my-fixed-id",
		Scope:     "system",
		Model:     "m",
		Content:   "fact",
		Embedding: []float32{1, 2},
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if id != "my-fixed-id" {
		t.Errorf("Insert returned id = %q, want %q", id, "my-fixed-id")
	}
}

func TestVectorStore_Insert_RejectsEmptyEmbedding(t *testing.T) {
	store := openTestStore(t)
	vs := store.VectorStore()
	ctx := context.Background()

	if _, err := vs.Insert(ctx, VectorEntry{Scope: "user", Model: "m", Embedding: nil}); err == nil {
		t.Fatal("Insert with empty embedding: expected error, got nil")
	}
}

func TestVectorStore_Insert_RejectsEmptyScope(t *testing.T) {
	store := openTestStore(t)
	vs := store.VectorStore()
	ctx := context.Background()

	if _, err := vs.Insert(ctx, VectorEntry{Model: "m", Embedding: []float32{1}}); err == nil {
		t.Fatal("Insert with empty scope: expected error, got nil")
	}
}

func TestVectorStore_Insert_RejectsEmptyModel(t *testing.T) {
	store := openTestStore(t)
	vs := store.VectorStore()
	ctx := context.Background()

	if _, err := vs.Insert(ctx, VectorEntry{Scope: "user", Embedding: []float32{1}}); err == nil {
		t.Fatal("Insert with empty model: expected error, got nil")
	}
}

// --- Search ---

func TestVectorStore_Search_RanksClosestFirst(t *testing.T) {
	store := openTestStore(t)
	vs := store.VectorStore()
	ctx := context.Background()

	entries := map[string][]float32{
		"a": {1, 0, 0},
		"b": {0, 1, 0},
		"c": {0, 0, 1},
	}
	for id, emb := range entries {
		if _, err := vs.Insert(ctx, VectorEntry{
			ID: id, Scope: "user", Model: "m", Content: id, Embedding: emb,
		}); err != nil {
			t.Fatalf("Insert(%s): %v", id, err)
		}
	}

	// Query closest to "b".
	got, err := vs.Search(ctx, "user", "m", []float32{0.1, 0.9, 0}, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Search returned %d entries, want 3", len(got))
	}
	if got[0].Entry.ID != "b" {
		t.Errorf("top result = %q, want %q", got[0].Entry.ID, "b")
	}

	// Top-k respected.
	top1, err := vs.Search(ctx, "user", "m", []float32{0.1, 0.9, 0}, 1)
	if err != nil {
		t.Fatalf("Search(k=1): %v", err)
	}
	if len(top1) != 1 {
		t.Fatalf("Search(k=1) returned %d entries, want 1", len(top1))
	}
	if top1[0].Entry.ID != "b" {
		t.Errorf("Search(k=1) top result = %q, want %q", top1[0].Entry.ID, "b")
	}
}

func TestVectorStore_Search_ModelFilter(t *testing.T) {
	store := openTestStore(t)
	vs := store.VectorStore()
	ctx := context.Background()

	if _, err := vs.Insert(ctx, VectorEntry{
		ID: "e-m1", Scope: "user", Model: "m1", Content: "one", Embedding: []float32{1, 0},
	}); err != nil {
		t.Fatalf("Insert(m1): %v", err)
	}
	if _, err := vs.Insert(ctx, VectorEntry{
		ID: "e-m2", Scope: "user", Model: "m2", Content: "two", Embedding: []float32{1, 0},
	}); err != nil {
		t.Fatalf("Insert(m2): %v", err)
	}

	got, err := vs.Search(ctx, "user", "m1", []float32{1, 0}, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Search(model=m1) returned %d entries, want 1", len(got))
	}
	if got[0].Entry.ID != "e-m1" {
		t.Errorf("Search(model=m1) result = %q, want %q", got[0].Entry.ID, "e-m1")
	}
}

func TestVectorStore_Search_DimGuard(t *testing.T) {
	store := openTestStore(t)
	vs := store.VectorStore()
	ctx := context.Background()

	if _, err := vs.Insert(ctx, VectorEntry{
		ID: "dim4", Scope: "user", Model: "m", Content: "four", Embedding: []float32{1, 0, 0, 0},
	}); err != nil {
		t.Fatalf("Insert(dim4): %v", err)
	}
	if _, err := vs.Insert(ctx, VectorEntry{
		ID: "dim3", Scope: "user", Model: "m", Content: "three", Embedding: []float32{1, 0, 0},
	}); err != nil {
		t.Fatalf("Insert(dim3): %v", err)
	}

	got, err := vs.Search(ctx, "user", "m", []float32{1, 0, 0, 0}, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Search with mismatched dims returned %d entries, want 1 (dim3 row skipped)", len(got))
	}
	if got[0].Entry.ID != "dim4" {
		t.Errorf("Search result = %q, want %q", got[0].Entry.ID, "dim4")
	}
}

func TestVectorStore_Search_EmptyStore(t *testing.T) {
	store := openTestStore(t)
	vs := store.VectorStore()
	ctx := context.Background()

	got, err := vs.Search(ctx, "user", "m", []float32{1, 2, 3}, 5)
	if err != nil {
		t.Fatalf("Search on empty store: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Search on empty store returned %d entries, want 0", len(got))
	}
}

// --- Concurrency smoke test ---

func TestVectorStore_ConcurrentInsertSearch(t *testing.T) {
	store := openTestStore(t)
	vs := store.VectorStore()
	ctx := context.Background()

	const inserters = 8
	const searchers = 8
	const iterations = 20

	var wg sync.WaitGroup
	errs := make(chan error, (inserters+searchers)*iterations)

	for i := range inserters {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range iterations {
				_, err := vs.Insert(ctx, VectorEntry{
					Scope:     "user",
					Model:     "m",
					Content:   "concurrent fact",
					Embedding: []float32{float32(i), float32(j), 1},
				})
				if err != nil {
					errs <- err
				}
			}
		}(i)
	}
	for range searchers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				if _, err := vs.Search(ctx, "user", "m", []float32{1, 1, 1}, 5); err != nil {
					errs <- err
				}
			}
		}()
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Insert/Search: %v", err)
		}
	}
}
