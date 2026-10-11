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

-- name: InsertChunkSet :one
INSERT INTO gold.chunk_set (
    build_id, chunk_set_key, source_manifest_digest, citation_recipe_digest,
    chunking_recipe_digest, tokenizer_digest, preprocessing_digest,
    writer_owner, fence_token, created_at
) VALUES (
    @build_id, @chunk_set_key, @source_manifest_digest, @citation_recipe_digest,
    @chunking_recipe_digest, @tokenizer_digest, @preprocessing_digest,
    @writer_owner, @fence_token, @created_at
)
ON CONFLICT (chunk_set_key) DO NOTHING
RETURNING *;

-- name: ChunkSetByKey :one
SELECT * FROM gold.chunk_set WHERE chunk_set_key = @chunk_set_key;

-- name: InsertCorpusChunk :one
INSERT INTO gold.corpus_chunk (
    chunk_set_id, chunk_local_id, document_revision_id, provision_revision_id,
    native_citation_bytes, native_citation_text, source_anchor, context_prefix,
    content, content_sha256, embedding_input_sha256, ordinal, token_count, created_at
) VALUES (
    @chunk_set_id, @chunk_local_id, @document_revision_id, @provision_revision_id,
    @native_citation_bytes, @native_citation_text, @source_anchor, @context_prefix,
    @content, @content_sha256, @embedding_input_sha256, @ordinal, @token_count, @created_at
)
ON CONFLICT (chunk_set_id, chunk_local_id) DO NOTHING
RETURNING *;

-- name: CorpusChunkByLocalID :one
SELECT * FROM gold.corpus_chunk
WHERE chunk_set_id = @chunk_set_id AND chunk_local_id = @chunk_local_id;

-- name: ListCorpusChunksAfter :many
SELECT * FROM gold.corpus_chunk
WHERE chunk_set_id = @chunk_set_id AND chunk_local_id > @after_chunk_local_id
ORDER BY chunk_local_id
LIMIT @row_limit;

-- name: InsertChunkVector :one
INSERT INTO gold.chunk_vector (
    corpus_chunk_id, embedding_input_sha256, model_digest, tokenizer_digest,
    preprocessing_digest, dimensions, embedding, created_at
) VALUES (
    @corpus_chunk_id, @embedding_input_sha256, @model_digest, @tokenizer_digest,
    @preprocessing_digest, @dimensions, @embedding, @created_at
)
ON CONFLICT (
    corpus_chunk_id, embedding_input_sha256, model_digest,
    tokenizer_digest, preprocessing_digest, dimensions
) DO NOTHING
RETURNING *;

-- name: ChunkVectorByExactRecipe :one
SELECT * FROM gold.chunk_vector
WHERE corpus_chunk_id = @corpus_chunk_id
  AND embedding_input_sha256 = @embedding_input_sha256
  AND model_digest = @model_digest
  AND tokenizer_digest = @tokenizer_digest
  AND preprocessing_digest = @preprocessing_digest
  AND dimensions = @dimensions;

-- name: ListChunkVectors :many
SELECT * FROM gold.chunk_vector
WHERE corpus_chunk_id = @corpus_chunk_id
ORDER BY model_digest, tokenizer_digest, preprocessing_digest, dimensions;

-- name: InsertLexicalRevision :one
INSERT INTO gold.lexical_revision (
    chunk_set_id, lexical_revision_key, lexical_model_digest,
    tokenizer_digest, preprocessing_digest, dimensions, created_at
) VALUES (
    @chunk_set_id, @lexical_revision_key, @lexical_model_digest,
    @tokenizer_digest, @preprocessing_digest, @dimensions, @created_at
)
ON CONFLICT (chunk_set_id) DO NOTHING
RETURNING *;

-- name: LexicalRevisionByChunkSet :one
SELECT * FROM gold.lexical_revision WHERE chunk_set_id = @chunk_set_id;

-- name: InsertChunkLexicalVector :one
INSERT INTO gold.chunk_lexical_vector (
    chunk_set_id, lexical_revision_id, corpus_chunk_id, content_sparse, created_at
) VALUES (
    @chunk_set_id, @lexical_revision_id, @corpus_chunk_id, @content_sparse, @created_at
)
ON CONFLICT (lexical_revision_id, corpus_chunk_id) DO NOTHING
RETURNING *;

-- name: ListChunkLexicalVectors :many
SELECT * FROM gold.chunk_lexical_vector
WHERE lexical_revision_id = @lexical_revision_id
  AND corpus_chunk_id > @after_corpus_chunk_id
ORDER BY corpus_chunk_id
LIMIT @row_limit;

-- name: InsertPublishedEvidence :one
INSERT INTO gold.published_evidence (
    build_id, chunk_set_id, corpus_chunk_id, document_uuid, document_revision_id,
    provision_revision_id, native_citation_bytes, native_citation_text,
    source_anchor, source_url, validity_status, legal_effective_from,
    legal_effective_to, provenance, known_gaps, created_at
) VALUES (
    @build_id, @chunk_set_id, @corpus_chunk_id, @document_uuid, @document_revision_id,
    @provision_revision_id, @native_citation_bytes, @native_citation_text,
    @source_anchor, @source_url, @validity_status, @legal_effective_from,
    @legal_effective_to, @provenance, @known_gaps, @created_at
)
ON CONFLICT (build_id, corpus_chunk_id) DO NOTHING
RETURNING *;

-- name: PublishedEvidenceByChunk :one
SELECT * FROM gold.published_evidence
WHERE build_id = @build_id AND corpus_chunk_id = @corpus_chunk_id;

-- name: InsertPublishedEvidenceSubject :one
INSERT INTO gold.published_evidence_subject (
    published_evidence_id, taxonomy_release_id, subject_key,
    context_role, coverage_state
) VALUES (
    @published_evidence_id, @taxonomy_release_id, @subject_key,
    @context_role, @coverage_state
)
ON CONFLICT (
    published_evidence_id, taxonomy_release_id, subject_key, context_role
) DO NOTHING
RETURNING *;

-- name: ListPublishedEvidenceSubjects :many
SELECT * FROM gold.published_evidence_subject
WHERE published_evidence_id = @published_evidence_id
ORDER BY taxonomy_release_id, subject_key, context_role;

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
