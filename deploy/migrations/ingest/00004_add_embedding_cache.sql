-- +goose Up
-- Create "embedding_cache" table
CREATE TABLE "ingest"."embedding_cache" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "text_hash" text NOT NULL, "model" text NOT NULL, "dims" integer NOT NULL, "embedding" vector(1024) NOT NULL, "created_at" timestamptz NOT NULL DEFAULT now(), PRIMARY KEY ("id"), CONSTRAINT "uq_ingest_embedding_cache" UNIQUE ("text_hash", "model", "dims"));
