-- +goose Up
-- Modify "fetch_doc" table
ALTER TABLE "ingest"."fetch_doc" ADD COLUMN "discovered_files" jsonb NULL;
