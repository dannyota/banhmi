-- +goose Up
-- Modify "relation_type" table
ALTER TABLE "config"."relation_type" ADD COLUMN "is_superseding" boolean NOT NULL DEFAULT false;
