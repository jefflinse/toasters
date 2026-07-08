-- 018_kb_vectors.sql: Durable embeddings for the system/user knowledge base.
--
-- Job-scope KB facts stay as files (Part A). This table is only for
-- system/user scope durable facts, stored as embeddings for semantic search.
-- The default (and only, for now) implementation is a pure-Go brute-force
-- cosine scan over the float32 BLOBs — no CGo, no sqlite-vec, since
-- modernc.org/sqlite is a hard constraint.
--
-- model is the embedding model id: different embedding models produce
-- incomparable vector spaces, so searches always filter on (scope, model)
-- together to avoid comparing vectors across spaces.

CREATE TABLE IF NOT EXISTS kb_vectors (
    id         TEXT PRIMARY KEY,
    scope      TEXT NOT NULL,
    source     TEXT NOT NULL DEFAULT '',
    model      TEXT NOT NULL,
    content    TEXT NOT NULL,
    dim        INTEGER NOT NULL,
    embedding  BLOB NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_kb_vectors_scope_model ON kb_vectors (scope, model);
