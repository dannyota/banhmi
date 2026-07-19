CREATE EXTENSION IF NOT EXISTS vector;

CREATE SCHEMA IF NOT EXISTS gold;

-- gold.chunk is a RAG-ready, structure-aware chunk (one per Điều, long Điều split
-- by Khoản). citation is the human-facing "Điều 7, Khoản 2"; context_prefix is the
-- deterministic contextual-retrieval header (số ký hiệu + title + Chương/Mục +
-- effective date) prepended before embedding. document_id / section_id are business
-- keys into silver; document_version_id is nullable until dated versions exist.
CREATE TABLE gold.chunk (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    document_id         BIGINT NOT NULL,
    document_version_id BIGINT,
    section_id          BIGINT,
    citation            TEXT NOT NULL,
    context_prefix      TEXT,
    content             TEXT NOT NULL,
    ordinal             INTEGER NOT NULL,
    token_count         INTEGER,
    -- BM25 sparse vector (pgvector) for the RDS-portable lexical retrieval arm.
    -- Populated by cmd/lexindex (Go-computed BM25 weights), queried via raw pgx
    -- (pkg/rag/retrieve); NULL until the lexical index is built. Dimension matches
    -- lexical.Dim (2^20, the hashing-trick term space).
    content_sparse      sparsevec(1048576),
    CONSTRAINT uq_gold_chunk UNIQUE (document_id, citation, ordinal)
);

CREATE INDEX idx_gold_chunk_document ON gold.chunk (document_id);
CREATE INDEX idx_gold_chunk_section ON gold.chunk (section_id);

-- gold.chunk_embedding holds one embedding per (chunk, model, dims) so multiple
-- embedders coexist and re-embedding is non-destructive. The vector is fixed at
-- 1024 dims (BGE-M3 default); the HNSW cosine index lives in the migration.
CREATE TABLE gold.chunk_embedding (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chunk_id    BIGINT NOT NULL,
    model       TEXT NOT NULL,
    dims        INTEGER NOT NULL,
    embedding   vector(1024) NOT NULL,
    CONSTRAINT fk_gold_embedding_chunk FOREIGN KEY (chunk_id)
        REFERENCES gold.chunk (id) ON DELETE CASCADE,
    CONSTRAINT uq_gold_chunk_embedding UNIQUE (chunk_id, model, dims)
);

CREATE INDEX idx_gold_embedding_chunk ON gold.chunk_embedding (chunk_id);

CREATE INDEX idx_gold_embedding_hnsw ON gold.chunk_embedding
    USING hnsw (embedding vector_cosine_ops);

-- gold.document_summary is a generated document-level summary (one per document).
CREATE TABLE gold.document_summary (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    document_id  BIGINT NOT NULL,
    summary      TEXT,
    CONSTRAINT uq_gold_document_summary UNIQUE (document_id)
);
