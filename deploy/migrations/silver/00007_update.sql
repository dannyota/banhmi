-- +goose Up
-- Modify "document" table
ALTER TABLE "silver"."document" ADD COLUMN "metadata_priority" smallint NOT NULL DEFAULT 0;
