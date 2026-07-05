package db

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/gofrs/uuid/v5"
)

// VectorEntry is a single durable knowledge-base fact stored with its
// embedding.
type VectorEntry struct {
	ID        string
	Scope     string // "system" or "user". Job scope stays as files (Part A).
	Source    string
	Model     string // embedding model id that produced Embedding.
	Content   string
	Embedding []float32
	CreatedAt time.Time
}

// ScoredEntry pairs a VectorEntry with its cosine similarity to a query
// vector, as returned by VectorStore.Search.
type ScoredEntry struct {
	Entry VectorEntry
	Score float32
}

// VectorStore is the default brute-force implementation of durable
// system/user knowledge-base storage: a pure-Go cosine scan over float32
// embeddings stored as BLOBs in the shared toasters.db. At the
// thousands-of-vectors scale of durable facts, a naive scan is
// sub-millisecond, so no vector index is needed.
//
// A swappable-backend interface and an in-memory embedding cache are
// deliberately deferred — this is the lean, eval-gated storage substrate
// only.
//
// It is a thin view over the same *sql.DB as the owning SQLiteStore,
// obtained via SQLiteStore.VectorStore(). Safe for concurrent use
// (database/sql handles connection pooling; WAL mode allows concurrent
// readers).
type VectorStore struct {
	db *sql.DB
}

// VectorStore returns a VectorStore backed by this database. Returns a fresh
// lightweight view each call.
func (s *SQLiteStore) VectorStore() *VectorStore {
	return &VectorStore{db: s.db}
}

// Insert stores e, generating an id if e.ID is empty, and returns the final
// id (generated or provided) so a caller can hold a stable handle to the
// entry. Returns an error if e.Embedding is empty or e.Scope / e.Model is
// unset.
func (v *VectorStore) Insert(ctx context.Context, e VectorEntry) (string, error) {
	if len(e.Embedding) == 0 {
		return "", fmt.Errorf("inserting kb vector: embedding must not be empty")
	}
	if e.Scope == "" {
		return "", fmt.Errorf("inserting kb vector: scope must not be empty")
	}
	if e.Model == "" {
		return "", fmt.Errorf("inserting kb vector: model must not be empty")
	}

	id := e.ID
	if id == "" {
		id = uuid.Must(uuid.NewV4()).String()
	}

	createdAt := e.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	_, err := v.db.ExecContext(ctx,
		`INSERT INTO kb_vectors (id, scope, source, model, content, dim, embedding, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, e.Scope, e.Source, e.Model, e.Content, len(e.Embedding), encodeVec(e.Embedding),
		createdAt.Format(time.RFC3339))
	if err != nil {
		return "", fmt.Errorf("inserting kb vector %q: %w", id, err)
	}

	return id, nil
}

// Search returns the top-k entries in scope with the given embedding model,
// ranked by cosine similarity to query, descending. If k <= 0, all matching
// entries are returned, sorted. An empty store or no matches returns an
// empty slice and a nil error — not an error.
//
// scope and model are both required filters: model in particular matters
// because two different embedding models produce incomparable vector
// spaces, so a query with the wrong model string silently returns zero
// results rather than nonsense scores. Callers must query with the exact
// same model string used at insert time.
//
// As a belt-and-suspenders guard alongside the model filter, any stored
// embedding whose length doesn't match len(query) is skipped rather than
// causing an error or panic.
func (v *VectorStore) Search(ctx context.Context, scope, model string, query []float32, k int) ([]ScoredEntry, error) {
	rows, err := v.db.QueryContext(ctx,
		`SELECT id, scope, source, model, content, embedding, created_at
		 FROM kb_vectors WHERE scope = ? AND model = ?`,
		scope, model)
	if err != nil {
		return nil, fmt.Errorf("searching kb vectors: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var results []ScoredEntry
	for rows.Next() {
		var (
			e         VectorEntry
			blob      []byte
			createdAt string
		)
		if err := rows.Scan(&e.ID, &e.Scope, &e.Source, &e.Model, &e.Content, &blob, &createdAt); err != nil {
			return nil, fmt.Errorf("scanning kb vector row: %w", err)
		}

		vec, err := decodeVec(blob)
		if err != nil {
			return nil, fmt.Errorf("decoding embedding for kb vector %q: %w", e.ID, err)
		}
		if len(vec) != len(query) {
			// Dim mismatch: skip rather than panic or error. The (scope,
			// model) filter should already guarantee comparable dimensions;
			// this is a defensive fallback in case of corrupted/mixed data.
			continue
		}
		e.Embedding = vec
		e.CreatedAt = parseTime(createdAt)

		results = append(results, ScoredEntry{Entry: e, Score: cosine(query, vec)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating kb vector rows: %w", err)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if k > 0 && len(results) > k {
		results = results[:k]
	}

	return results, nil
}

// encodeVec packs a []float32 into a little-endian byte slice, 4 bytes per
// value.
func encodeVec(vec []float32) []byte {
	buf := make([]byte, len(vec)*4)
	for i, f := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// decodeVec unpacks a little-endian byte slice into a []float32. Returns an
// error if the byte length is not a multiple of 4.
func decodeVec(buf []byte) ([]float32, error) {
	if len(buf)%4 != 0 {
		return nil, fmt.Errorf("decoding vector: byte length %d is not a multiple of 4", len(buf))
	}
	vec := make([]float32, len(buf)/4)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return vec, nil
}

// cosine returns the cosine similarity between a and b. It returns 0 (rather
// than NaN) if the lengths differ or either vector has zero norm — a NaN in
// the top-k sort would produce nondeterministic ordering.
func cosine(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}
