-- +goose Up
-- Add new schema named "gold"
CREATE SCHEMA IF NOT EXISTS "gold";
-- Create "chunk" table
CREATE TABLE "gold"."chunk" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "document_id" bigint NOT NULL, "document_version_id" bigint NULL, "section_id" bigint NULL, "citation" text NOT NULL, "context_prefix" text NULL, "content" text NOT NULL, "ordinal" integer NOT NULL, "token_count" integer NULL, PRIMARY KEY ("id"), CONSTRAINT "uq_gold_chunk" UNIQUE ("document_id", "citation", "ordinal"));
-- Create index "idx_gold_chunk_document" to table: "chunk"
CREATE INDEX "idx_gold_chunk_document" ON "gold"."chunk" ("document_id");
-- Create index "idx_gold_chunk_section" to table: "chunk"
CREATE INDEX "idx_gold_chunk_section" ON "gold"."chunk" ("section_id");
-- Create "document_summary" table
CREATE TABLE "gold"."document_summary" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "document_id" bigint NOT NULL, "summary" text NULL, PRIMARY KEY ("id"), CONSTRAINT "uq_gold_document_summary" UNIQUE ("document_id"));
-- Create "chunk_embedding" table
CREATE TABLE "gold"."chunk_embedding" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "chunk_id" bigint NOT NULL, "model" text NOT NULL, "dims" integer NOT NULL, "embedding" vector(1024) NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "uq_gold_chunk_embedding" UNIQUE ("chunk_id", "model", "dims"), CONSTRAINT "fk_gold_embedding_chunk" FOREIGN KEY ("chunk_id") REFERENCES "gold"."chunk" ("id") ON UPDATE NO ACTION ON DELETE CASCADE);
-- Create index "idx_gold_embedding_chunk" to table: "chunk_embedding"
CREATE INDEX "idx_gold_embedding_chunk" ON "gold"."chunk_embedding" ("chunk_id");
-- Create index "idx_gold_embedding_hnsw" to table: "chunk_embedding"
CREATE INDEX "idx_gold_embedding_hnsw" ON "gold"."chunk_embedding" USING hnsw ("embedding" vector_cosine_ops);
