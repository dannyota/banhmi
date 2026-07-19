-- +goose Up
-- Add new schema named "ingest"
CREATE SCHEMA IF NOT EXISTS "ingest";
-- Create "discover_cursor" table
CREATE TABLE "ingest"."discover_cursor" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "source" text NOT NULL, "keyword" text NOT NULL, "watermark" text NOT NULL DEFAULT '', "expected_total" bigint NOT NULL DEFAULT 0, "last_seen_total" bigint NOT NULL DEFAULT 0, "last_run_at" timestamptz NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "uq_ingest_discover_cursor" UNIQUE ("source", "keyword"));
-- Create "fetch_doc" table
CREATE TABLE "ingest"."fetch_doc" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "source" text NOT NULL, "external_id" text NOT NULL, "state" text NOT NULL DEFAULT 'discovered', "plan_ready" boolean NOT NULL DEFAULT false, "in_scope" boolean NOT NULL DEFAULT true, "provenance" text NOT NULL DEFAULT '', "artifacts_expected" integer NOT NULL DEFAULT 0, "artifacts_done" integer NOT NULL DEFAULT 0, "artifacts_failed" integer NOT NULL DEFAULT 0, "tree_recheck_after" timestamptz NULL, "tree_recheck_count" integer NOT NULL DEFAULT 0, "content_hash" text NULL, "detail_url" text NULL, "discovered_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "uq_ingest_fetch_doc" UNIQUE ("source", "external_id"), CONSTRAINT "chk_ingest_fetch_doc_state" CHECK (state = ANY (ARRAY['discovered'::text, 'planning'::text, 'fetching'::text, 'partial'::text, 'complete'::text, 'error'::text])));
-- Create index "idx_ingest_fetch_doc_incomplete" to table: "fetch_doc"
CREATE INDEX "idx_ingest_fetch_doc_incomplete" ON "ingest"."fetch_doc" ("plan_ready", "state");
-- Create index "idx_ingest_fetch_doc_recheck" to table: "fetch_doc"
CREATE INDEX "idx_ingest_fetch_doc_recheck" ON "ingest"."fetch_doc" ("tree_recheck_after");
-- Create index "idx_ingest_fetch_doc_state" to table: "fetch_doc"
CREATE INDEX "idx_ingest_fetch_doc_state" ON "ingest"."fetch_doc" ("state", "updated_at");
-- Create "doc_discovery" table
CREATE TABLE "ingest"."doc_discovery" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "fetch_doc_id" bigint NOT NULL, "via" text NOT NULL DEFAULT '', "keyword" text NOT NULL DEFAULT '', "src_fetch_doc_id" bigint NOT NULL DEFAULT 0, "relation_type" text NOT NULL DEFAULT '', "discovered_at" timestamptz NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "uq_ingest_doc_discovery" UNIQUE ("fetch_doc_id", "via", "keyword", "src_fetch_doc_id", "relation_type"), CONSTRAINT "fk_ingest_doc_discovery_doc" FOREIGN KEY ("fetch_doc_id") REFERENCES "ingest"."fetch_doc" ("id") ON UPDATE NO ACTION ON DELETE CASCADE);
-- Create index "idx_ingest_doc_discovery_doc" to table: "doc_discovery"
CREATE INDEX "idx_ingest_doc_discovery_doc" ON "ingest"."doc_discovery" ("fetch_doc_id");
-- Create index "idx_ingest_doc_discovery_keyword" to table: "doc_discovery"
CREATE INDEX "idx_ingest_doc_discovery_keyword" ON "ingest"."doc_discovery" ("keyword");
-- Create "fetch_artifact" table
CREATE TABLE "ingest"."fetch_artifact" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "fetch_doc_id" bigint NOT NULL, "kind" text NOT NULL, "ref_key" text NOT NULL DEFAULT '', "file_kind" text NOT NULL DEFAULT '', "url" text NULL, "url_expires_at" timestamptz NULL, "gateway_url" text NULL, "target_source" text NOT NULL DEFAULT '', "target_ext_id" text NOT NULL DEFAULT '', "is_optional" boolean NOT NULL DEFAULT false, "state" text NOT NULL DEFAULT 'pending', "lease_owner" text NULL, "lease_expires_at" timestamptz NULL, "attempts" integer NOT NULL DEFAULT 0, "max_attempts" integer NOT NULL DEFAULT 5, "next_attempt_at" timestamptz NULL, "content_hash" text NULL, "result_ref" text NULL, "last_error" text NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "uq_ingest_fetch_artifact" UNIQUE ("fetch_doc_id", "kind", "ref_key"), CONSTRAINT "fk_ingest_fetch_artifact_doc" FOREIGN KEY ("fetch_doc_id") REFERENCES "ingest"."fetch_doc" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "chk_ingest_fetch_artifact_kind" CHECK (kind = ANY (ARRAY['body'::text, 'tree'::text, 'file'::text, 'relation'::text, 'appendix'::text])), CONSTRAINT "chk_ingest_fetch_artifact_state" CHECK (state = ANY (ARRAY['pending'::text, 'claimed'::text, 'done'::text, 'error'::text, 'dead'::text, 'skipped'::text, 'superseded'::text])));
-- Create index "idx_ingest_fetch_artifact_claim" to table: "fetch_artifact"
CREATE INDEX "idx_ingest_fetch_artifact_claim" ON "ingest"."fetch_artifact" ("state", "next_attempt_at");
-- Create index "idx_ingest_fetch_artifact_doc" to table: "fetch_artifact"
CREATE INDEX "idx_ingest_fetch_artifact_doc" ON "ingest"."fetch_artifact" ("fetch_doc_id");
-- Create index "idx_ingest_fetch_artifact_expiry" to table: "fetch_artifact"
CREATE INDEX "idx_ingest_fetch_artifact_expiry" ON "ingest"."fetch_artifact" ("url_expires_at");
-- Create index "idx_ingest_fetch_artifact_lease" to table: "fetch_artifact"
CREATE INDEX "idx_ingest_fetch_artifact_lease" ON "ingest"."fetch_artifact" ("lease_expires_at");
