// Package kb ties an embedding model to the durable vector store
// (internal/db's VectorStore), giving callers a simple Remember/Recall API
// over user- and system-scope facts. This is Part B3a: only the operator
// consumes it so far — workers and system-scope population are later
// increments.
package kb

import (
	"context"
	"fmt"
	"time"

	"github.com/jefflinse/toasters/internal/db"
)

// Embedder is the subset of the embedding provider this service needs.
// *provider.Scheduler satisfies it. Defined here (consumer side) so kb does
// not import internal/provider.
type Embedder interface {
	Embed(ctx context.Context, model string, inputs []string) ([][]float32, error)
}

// Hit is a single recalled fact.
type Hit struct {
	ID      string
	Content string
	Source  string
	Score   float32
}

// Service ties an embedding model to the vector store: it embeds text on the
// way in (Remember) and on the way out (Recall), applying the configured
// nomic-style task prefixes so query and document embeddings live in the
// asymmetric-retrieval space the model was trained for.
type Service struct {
	store       *db.VectorStore
	embedder    Embedder
	model       string
	docPrefix   string
	queryPrefix string
	topK        int
}

// New creates a Service backed by store, using embedder/model to produce
// vectors. docPrefix/queryPrefix are prepended to content/query text before
// embedding (empty means no prefix). topK <= 0 defaults to 5.
func New(store *db.VectorStore, embedder Embedder, model, docPrefix, queryPrefix string, topK int) *Service {
	if topK <= 0 {
		topK = 5
	}
	return &Service{
		store:       store,
		embedder:    embedder,
		model:       model,
		docPrefix:   docPrefix,
		queryPrefix: queryPrefix,
		topK:        topK,
	}
}

// Remember embeds content (with the document prefix) and stores it. Returns
// the stored entry's id.
func (s *Service) Remember(ctx context.Context, scope, source, content string) (string, error) {
	vecs, err := s.embedder.Embed(ctx, s.model, []string{s.docPrefix + content})
	if err != nil {
		return "", fmt.Errorf("embedding content: %w", err)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return "", fmt.Errorf("embedding returned no vector")
	}
	return s.store.Insert(ctx, db.VectorEntry{
		Scope:     scope,
		Source:    source,
		Model:     s.model,
		Content:   content,
		Embedding: vecs[0],
		CreatedAt: time.Now().UTC(),
	})
}

// Recall embeds query (with the query prefix) and returns the top-k nearest
// stored facts in scope. k<=0 uses the configured default.
func (s *Service) Recall(ctx context.Context, scope, query string, k int) ([]Hit, error) {
	if k <= 0 {
		k = s.topK
	}
	vecs, err := s.embedder.Embed(ctx, s.model, []string{s.queryPrefix + query})
	if err != nil {
		return nil, fmt.Errorf("embedding query: %w", err)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return nil, fmt.Errorf("embedding returned no vector")
	}
	scored, err := s.store.Search(ctx, scope, s.model, vecs[0], k)
	if err != nil {
		return nil, fmt.Errorf("searching memory: %w", err)
	}
	hits := make([]Hit, 0, len(scored))
	for _, sc := range scored {
		hits = append(hits, Hit{ID: sc.Entry.ID, Content: sc.Entry.Content, Source: sc.Entry.Source, Score: sc.Score})
	}
	return hits, nil
}
