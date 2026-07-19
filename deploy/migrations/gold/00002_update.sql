-- +goose Up
-- Modify "chunk" table
ALTER TABLE "gold"."chunk" ADD COLUMN "content_sparse" sparsevec(1048576) NULL;
