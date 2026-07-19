-- +goose Up
-- Modify "scope_term" table
ALTER TABLE "config"."scope_term" DROP CONSTRAINT "uq_config_scope_term", ADD COLUMN "jurisdiction" text NOT NULL DEFAULT 'vn', ADD CONSTRAINT "uq_config_scope_term" UNIQUE ("jurisdiction", "term_class", "term");
