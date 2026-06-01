-- +goose Up
-- Modify "fetch_artifact" table
ALTER TABLE "ingest"."fetch_artifact" ADD COLUMN "file_name" text NOT NULL DEFAULT '';
