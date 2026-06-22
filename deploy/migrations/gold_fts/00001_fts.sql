-- +goose Up
-- Native Postgres full-text lexical index over gold.chunk — the RDS-portable
-- lexical arm of hybrid search (pkg/rag/retrieve ftsArm). Unlike gold_bm25
-- (ParadeDB pg_search, unavailable on managed RDS), this uses built-in FTS: a
-- functional GIN index on a vi_unaccent tsvector so diacritic-less queries still
-- match (the unaccent dictionary), which BGE-M3 vectors miss. Hand-written: Atlas
-- cannot diff a custom text-search configuration. Applied after gold (the table
-- must exist). The index expression MUST stay byte-identical to ftsTSVector in
-- pkg/rag/retrieve/retrieve.go for the GIN index to be used.

CREATE EXTENSION IF NOT EXISTS unaccent;

-- vi_unaccent = the 'simple' tokenizer (no stemming — correct for Vietnamese,
-- which is space-delimited) plus the unaccent dictionary, so "an toàn" and the
-- diacritic-less "an toan" map to the same lexeme. Created idempotently.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_ts_config WHERE cfgname = 'vi_unaccent') THEN
        CREATE TEXT SEARCH CONFIGURATION vi_unaccent (COPY = simple);
        ALTER TEXT SEARCH CONFIGURATION vi_unaccent
            ALTER MAPPING FOR asciiword, word, hword, hword_part, asciihword, numword, hword_numpart, numhword
            WITH unaccent, simple;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX IF NOT EXISTS idx_gold_chunk_fts
    ON gold.chunk
    USING gin (to_tsvector('vi_unaccent', coalesce(content, '') || ' ' || coalesce(context_prefix, '')));

-- +goose Down
DROP INDEX IF EXISTS gold.idx_gold_chunk_fts;
DROP TEXT SEARCH CONFIGURATION IF EXISTS vi_unaccent;
-- unaccent extension is intentionally left in place (cheap; may be used elsewhere).
