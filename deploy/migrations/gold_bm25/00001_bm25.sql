-- +goose Up
-- ParadeDB pg_search BM25 index for lexical retrieval over gold.chunk.
-- This is the lexical arm of hybrid search (pkg/rag/retrieve): `content @@@ $query`
-- with paradedb.score(id) ranks chunks by BM25 (real IDF + length normalization,
-- which Postgres ts_rank lacks). key_field='id' makes gold.chunk.id the BM25 key,
-- so paradedb.score(id) and the @@@ predicate join back to the chunk row.
--
-- Hand-written (not Atlas-generated): Atlas cannot diff the custom `bm25` access
-- method, so this lives in its own migration dir applied after gold. The DO block
-- swallows only the "pg_search unavailable" cases so a non-ParadeDB image still
-- migrates cleanly (lexical search degrades to vector-only; see pkg/rag/retrieve).
-- +goose StatementBegin
DO $$
BEGIN
    CREATE INDEX IF NOT EXISTS idx_gold_chunk_bm25
        ON gold.chunk
        USING bm25 (id, content)
        WITH (key_field='id');
EXCEPTION
    WHEN undefined_object OR feature_not_supported OR undefined_file THEN
        RAISE NOTICE 'bm25 access method unavailable, skipping idx_gold_chunk_bm25 (BM25 lexical search disabled): %', SQLERRM;
END;
$$;
-- +goose StatementEnd

-- +goose Down
DROP INDEX IF EXISTS gold.idx_gold_chunk_bm25;
