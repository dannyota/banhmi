-- +goose Up
-- pgvector is required: gold.chunk_embedding uses vector(1024). The ParadeDB
-- image bundles it. On a plain PostgreSQL image, install pgvector first.
CREATE EXTENSION IF NOT EXISTS vector;

-- pg_search (ParadeDB BM25) is optional. The DO block swallows only the
-- "extension unavailable" cases so a non-ParadeDB image still applies cleanly.
-- +goose StatementBegin
DO $$
BEGIN
    CREATE EXTENSION IF NOT EXISTS pg_search;
EXCEPTION
    WHEN undefined_file OR feature_not_supported OR insufficient_privilege THEN
        RAISE NOTICE 'pg_search unavailable, skipping (BM25 lexical search disabled): %', SQLERRM;
END;
$$;
-- +goose StatementEnd

-- +goose Down
DROP EXTENSION IF EXISTS pg_search;
DROP EXTENSION IF EXISTS vector;
