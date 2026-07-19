-- +goose Up
-- Modify "fetch_doc" table
ALTER TABLE "ingest"."fetch_doc" ADD COLUMN "content_recheck_after" timestamptz NULL, ADD COLUMN "content_recheck_count" integer NOT NULL DEFAULT 0, ADD COLUMN "content_recheck_reason" text NOT NULL DEFAULT '';
-- Create index "idx_ingest_fetch_doc_content_recheck" to table: "fetch_doc"
CREATE INDEX "idx_ingest_fetch_doc_content_recheck" ON "ingest"."fetch_doc" ("content_recheck_after");
