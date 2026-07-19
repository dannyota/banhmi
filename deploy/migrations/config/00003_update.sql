-- +goose Up
-- Create "relation_type" table
CREATE TABLE "config"."relation_type" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "source" text NOT NULL, "code" text NOT NULL, "label" text NOT NULL, "is_amending" boolean NOT NULL DEFAULT false, "origin" text NOT NULL DEFAULT 'seed', "created_at" timestamptz NOT NULL DEFAULT now(), "updated_at" timestamptz NOT NULL DEFAULT now(), PRIMARY KEY ("id"), CONSTRAINT "uq_config_relation_type" UNIQUE ("source", "code"), CONSTRAINT "chk_config_relation_type_origin" CHECK (origin = ANY (ARRAY['seed'::text, 'user'::text])));
