package provider

import "context"

// EmbeddingProvider is implemented by providers that can turn text into
// vector embeddings, in addition to (or instead of) chat completion. It is a
// separate interface from Provider — not every chat backend supports
// embeddings, and not every embedding backend needs to support chat — so
// callers type-assert for it rather than requiring it on Provider.
//
// This is the seam only: nothing in production calls Embed yet. The
// Knowledge Base vector store (a later change) is the first caller.
type EmbeddingProvider interface {
	// Embed returns one vector per input, in the same order as inputs. model
	// selects the embedding model; an empty model lets the implementation
	// fall back to its configured default.
	Embed(ctx context.Context, model string, inputs []string) ([][]float32, error)
}
