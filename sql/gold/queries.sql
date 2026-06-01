-- name: UpsertChunk :one
-- Idempotent on (document_id, citation, ordinal) so re-chunking converges.
INSERT INTO gold.chunk (
    document_id, document_version_id, section_id, citation,
    context_prefix, content, ordinal, token_count
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
)
ON CONFLICT (document_id, citation, ordinal) DO UPDATE SET
    document_version_id = EXCLUDED.document_version_id,
    section_id = EXCLUDED.section_id,
    context_prefix = EXCLUDED.context_prefix,
    content = EXCLUDED.content,
    token_count = EXCLUDED.token_count
RETURNING id;

-- name: ChunkByID :one
SELECT * FROM gold.chunk WHERE id = $1;

-- name: ListChunksByDocument :many
SELECT * FROM gold.chunk
WHERE document_id = $1
ORDER BY ordinal;

-- name: DeleteChunksByDocument :execrows
DELETE FROM gold.chunk WHERE document_id = $1;

-- name: UpsertChunkEmbedding :one
-- Keyed by (chunk_id, model, dims) so multiple embedders coexist and re-embedding
-- a chunk overwrites only that model's vector.
INSERT INTO gold.chunk_embedding (chunk_id, model, dims, embedding)
VALUES ($1, $2, $3, $4)
ON CONFLICT (chunk_id, model, dims) DO UPDATE SET
    embedding = EXCLUDED.embedding
RETURNING id;

-- name: SearchChunksByEmbedding :many
-- Cosine ANN over the HNSW index for one model, returning the chunk text and its
-- citation. The in-force pre-filter and BM25/RRF fusion are layered on in pkg/rag;
-- this is the vector leg of hybrid retrieval.
SELECT
    c.id,
    c.document_id,
    c.citation,
    c.context_prefix,
    c.content,
    e.embedding <=> @query_embedding AS distance
FROM gold.chunk_embedding e
JOIN gold.chunk c ON c.id = e.chunk_id
WHERE e.model = @model
ORDER BY e.embedding <=> @query_embedding
LIMIT @result_limit;

-- name: UpsertDocumentSummary :one
INSERT INTO gold.document_summary (document_id, summary)
VALUES ($1, $2)
ON CONFLICT (document_id) DO UPDATE SET
    summary = EXCLUDED.summary
RETURNING id;

-- name: DocumentSummaryByDocument :one
SELECT * FROM gold.document_summary WHERE document_id = $1;
