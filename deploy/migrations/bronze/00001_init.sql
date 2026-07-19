-- +goose Up
-- Add new schema named "bronze"
CREATE SCHEMA IF NOT EXISTS "bronze";
-- Create "source_document" table
CREATE TABLE "bronze"."source_document" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "source" text NOT NULL, "external_id" text NOT NULL, "doc_guid" text NOT NULL DEFAULT '', "doc_number" text NULL, "doc_number_norm" text NOT NULL DEFAULT '', "doc_type" text NULL, "doc_type_code" text NOT NULL DEFAULT '', "issuer" text NULL, "issuer_code" text NOT NULL DEFAULT '', "title" text NULL, "issued_at" timestamptz NULL, "effective_at" timestamptz NULL, "expire_at" timestamptz NULL, "status_raw" text NULL, "gazette_number" text NOT NULL DEFAULT '', "gazette_date" timestamptz NULL, "has_content" boolean NOT NULL DEFAULT false, "is_consolidated" boolean NOT NULL DEFAULT false, "detail_url" text NULL, "content_hash" text NULL, "raw_meta" jsonb NULL, "discovered_at" timestamptz NOT NULL, "fetched_at" timestamptz NULL, "collected_at" timestamptz NOT NULL, "first_collected_at" timestamptz NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "uq_bronze_source_document" UNIQUE ("source", "external_id"));
-- Create index "idx_bronze_source_document_dedup" to table: "source_document"
CREATE INDEX "idx_bronze_source_document_dedup" ON "bronze"."source_document" ("doc_number_norm", "issuer_code", "issued_at");
-- Create index "idx_bronze_source_document_guid" to table: "source_document"
CREATE INDEX "idx_bronze_source_document_guid" ON "bronze"."source_document" ("doc_guid");
-- Create index "idx_bronze_source_document_hash" to table: "source_document"
CREATE INDEX "idx_bronze_source_document_hash" ON "bronze"."source_document" ("content_hash");
-- Create index "idx_bronze_source_document_issued" to table: "source_document"
CREATE INDEX "idx_bronze_source_document_issued" ON "bronze"."source_document" ("issued_at");
-- Create index "idx_bronze_source_document_status" to table: "source_document"
CREATE INDEX "idx_bronze_source_document_status" ON "bronze"."source_document" ("source", "status_raw");
-- Create "raw_file" table
CREATE TABLE "bronze"."raw_file" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "source_document_id" bigint NOT NULL, "file_kind" text NOT NULL, "file_format" text NOT NULL, "is_authoritative" boolean NOT NULL DEFAULT false, "ordinal" integer NOT NULL DEFAULT 0, "label" text NOT NULL DEFAULT '', "lang" text NOT NULL DEFAULT '', "url" text NULL, "storage_path" text NULL, "sha256" text NULL, "byte_size" bigint NULL, "content_hash" text NULL, "collected_at" timestamptz NOT NULL, "first_collected_at" timestamptz NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "uq_bronze_raw_file" UNIQUE ("source_document_id", "file_kind", "ordinal", "file_format"), CONSTRAINT "fk_bronze_raw_file_document" FOREIGN KEY ("source_document_id") REFERENCES "bronze"."source_document" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "chk_bronze_raw_file_format" CHECK (file_format = ANY (ARRAY['pdf'::text, 'docx'::text, 'doc'::text, 'html'::text])), CONSTRAINT "chk_bronze_raw_file_kind" CHECK (file_kind = ANY (ARRAY['main'::text, 'appendix'::text, 'version_snapshot'::text, 'original_scan'::text, 'attachment'::text])));
-- Create index "idx_bronze_raw_file_document" to table: "raw_file"
CREATE INDEX "idx_bronze_raw_file_document" ON "bronze"."raw_file" ("source_document_id");
-- Create index "idx_bronze_raw_file_sha256" to table: "raw_file"
CREATE INDEX "idx_bronze_raw_file_sha256" ON "bronze"."raw_file" ("sha256");
-- Create "raw_payload" table
CREATE TABLE "bronze"."raw_payload" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "source_document_id" bigint NOT NULL, "kind" text NOT NULL, "content" text NULL, "content_hash" text NULL, "collected_at" timestamptz NOT NULL, "first_collected_at" timestamptz NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "uq_bronze_raw_payload" UNIQUE ("source_document_id", "kind"), CONSTRAINT "fk_bronze_raw_payload_document" FOREIGN KEY ("source_document_id") REFERENCES "bronze"."source_document" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "chk_bronze_raw_payload_kind" CHECK (kind = ANY (ARRAY['content_html'::text, 'provision_tree_json'::text, 'references_json'::text, 'detail_json'::text])));
-- Create index "idx_bronze_raw_payload_document" to table: "raw_payload"
CREATE INDEX "idx_bronze_raw_payload_document" ON "bronze"."raw_payload" ("source_document_id");
-- Create index "idx_bronze_raw_payload_hash" to table: "raw_payload"
CREATE INDEX "idx_bronze_raw_payload_hash" ON "bronze"."raw_payload" ("content_hash");
