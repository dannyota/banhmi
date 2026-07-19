-- +goose Up
-- Create "abbreviation_expand" table
CREATE TABLE "config"."abbreviation_expand" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "jurisdiction" text NOT NULL DEFAULT 'id', "abbreviation" text NOT NULL, "expansion" text NOT NULL, "origin" text NOT NULL DEFAULT 'seed', "created_at" timestamptz NOT NULL DEFAULT now(), "updated_at" timestamptz NOT NULL DEFAULT now(), PRIMARY KEY ("id"), CONSTRAINT "uq_config_abbreviation_expand" UNIQUE ("jurisdiction", "abbreviation"), CONSTRAINT "chk_config_abbreviation_expand_origin" CHECK (origin = ANY (ARRAY['seed'::text, 'user'::text])));
