-- +goose Up
-- pgvector is required: gold.chunk_embedding uses vector(1024) and gold.chunk
-- uses sparsevec(...) for the BM25 lexical arm. Install it on any plain
-- PostgreSQL image (the deployed RDS and the dev pgvector image both ship it).
CREATE EXTENSION IF NOT EXISTS vector;

-- +goose Down
DROP EXTENSION IF EXISTS vector;
