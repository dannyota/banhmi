-- +goose Up
-- Add new schema named "silver"
CREATE SCHEMA IF NOT EXISTS "silver";
-- Create "document" table
CREATE TABLE "silver"."document" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "doc_key" text NOT NULL, "doc_number" text NULL, "doc_number_norm" text NOT NULL DEFAULT '', "title" text NULL, "doc_type" text NULL, "doc_type_code" text NOT NULL DEFAULT '', "issuer" text NULL, "issuer_code" text NOT NULL DEFAULT '', "issued_at" timestamptz NULL, "signer" text NULL, "is_consolidated" boolean NOT NULL DEFAULT false, "markdown" text NULL, "source_document_id" bigint NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "uq_silver_document_key" UNIQUE ("doc_key"));
-- Create index "idx_silver_document_issued" to table: "document"
CREATE INDEX "idx_silver_document_issued" ON "silver"."document" ("issued_at");
-- Create index "idx_silver_document_issuer_type" to table: "document"
CREATE INDEX "idx_silver_document_issuer_type" ON "silver"."document" ("issuer", "doc_type");
-- Create index "idx_silver_document_number" to table: "document"
CREATE INDEX "idx_silver_document_number" ON "silver"."document" ("doc_number");
-- Create "doc_ref" table
CREATE TABLE "silver"."doc_ref" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "ref_key" text NOT NULL, "document_id" bigint NULL, "label" text NULL, "src_ref" jsonb NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "uq_silver_doc_ref_key" UNIQUE ("ref_key"));
-- Create index "idx_silver_doc_ref_document" to table: "doc_ref"
CREATE INDEX "idx_silver_doc_ref_document" ON "silver"."doc_ref" ("document_id");
-- Create index "idx_silver_doc_ref_unresolved" to table: "doc_ref"
CREATE INDEX "idx_silver_doc_ref_unresolved" ON "silver"."doc_ref" ("ref_key") WHERE (document_id IS NULL);
-- Create "amendment_event" table
CREATE TABLE "silver"."amendment_event" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "acting_document_id" bigint NOT NULL, "target_ref_id" bigint NOT NULL, "change_op" text NOT NULL, "effective_date" timestamptz NULL, "source" text NULL, PRIMARY KEY ("id"), CONSTRAINT "fk_silver_amendment_acting" FOREIGN KEY ("acting_document_id") REFERENCES "silver"."document" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "fk_silver_amendment_target" FOREIGN KEY ("target_ref_id") REFERENCES "silver"."doc_ref" ("id") ON UPDATE NO ACTION ON DELETE CASCADE);
-- Create index "idx_silver_amendment_acting" to table: "amendment_event"
CREATE INDEX "idx_silver_amendment_acting" ON "silver"."amendment_event" ("acting_document_id");
-- Create index "idx_silver_amendment_target" to table: "amendment_event"
CREATE INDEX "idx_silver_amendment_target" ON "silver"."amendment_event" ("target_ref_id");
-- Create "document_alias" table
CREATE TABLE "silver"."document_alias" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "source" text NOT NULL, "external_id" text NOT NULL, "document_id" bigint NOT NULL, "match_method" text NOT NULL DEFAULT '', "confidence" double precision NULL, PRIMARY KEY ("id"), CONSTRAINT "uq_silver_document_alias" UNIQUE ("source", "external_id"), CONSTRAINT "fk_silver_alias_document" FOREIGN KEY ("document_id") REFERENCES "silver"."document" ("id") ON UPDATE NO ACTION ON DELETE CASCADE);
-- Create index "idx_silver_alias_document" to table: "document_alias"
CREATE INDEX "idx_silver_alias_document" ON "silver"."document_alias" ("document_id");
-- Create "document_section" table
CREATE TABLE "silver"."document_section" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "document_id" bigint NOT NULL, "parent_id" bigint NULL, "node_key" text NULL, "ptype" smallint NULL, "kind" text NOT NULL, "ordinal" integer NOT NULL, "label" text NULL, "heading" text NULL, "citation_path" text NOT NULL, "content" text NULL, PRIMARY KEY ("id"), CONSTRAINT "uq_silver_section_citation" UNIQUE ("document_id", "citation_path"), CONSTRAINT "uq_silver_section_node" UNIQUE ("document_id", "node_key"), CONSTRAINT "fk_silver_section_document" FOREIGN KEY ("document_id") REFERENCES "silver"."document" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "fk_silver_section_parent" FOREIGN KEY ("parent_id") REFERENCES "silver"."document_section" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "chk_silver_section_kind" CHECK (kind = ANY (ARRAY['phan'::text, 'chuong'::text, 'muc'::text, 'dieu'::text, 'khoan'::text, 'diem'::text])));
-- Create index "idx_silver_section_document" to table: "document_section"
CREATE INDEX "idx_silver_section_document" ON "silver"."document_section" ("document_id");
-- Create index "idx_silver_section_parent" to table: "document_section"
CREATE INDEX "idx_silver_section_parent" ON "silver"."document_section" ("parent_id");
-- Create "document_gazette" table
CREATE TABLE "silver"."document_gazette" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "document_id" bigint NOT NULL, "gazette_number" text NOT NULL, "gazette_date" timestamptz NULL, "source_document_id" bigint NULL, PRIMARY KEY ("id"), CONSTRAINT "uq_silver_document_gazette" UNIQUE ("document_id", "gazette_number"), CONSTRAINT "fk_silver_gazette_document" FOREIGN KEY ("document_id") REFERENCES "silver"."document" ("id") ON UPDATE NO ACTION ON DELETE CASCADE);
-- Create index "idx_silver_gazette_document" to table: "document_gazette"
CREATE INDEX "idx_silver_gazette_document" ON "silver"."document_gazette" ("document_id");
-- Create "document_relation" table
CREATE TABLE "silver"."document_relation" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "from_document_id" bigint NOT NULL, "to_ref_id" bigint NOT NULL, "relation_type" text NOT NULL, "relation_type_raw" integer NULL, "from_section_id" bigint NULL, "to_citation" text NULL, "source" text NULL, PRIMARY KEY ("id"), CONSTRAINT "uq_silver_relation" UNIQUE ("from_document_id", "to_ref_id", "relation_type"), CONSTRAINT "fk_silver_relation_from_doc" FOREIGN KEY ("from_document_id") REFERENCES "silver"."document" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "fk_silver_relation_from_sec" FOREIGN KEY ("from_section_id") REFERENCES "silver"."document_section" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, CONSTRAINT "fk_silver_relation_to_ref" FOREIGN KEY ("to_ref_id") REFERENCES "silver"."doc_ref" ("id") ON UPDATE NO ACTION ON DELETE CASCADE);
-- Create index "idx_silver_relation_from" to table: "document_relation"
CREATE INDEX "idx_silver_relation_from" ON "silver"."document_relation" ("from_document_id");
-- Create index "idx_silver_relation_to" to table: "document_relation"
CREATE INDEX "idx_silver_relation_to" ON "silver"."document_relation" ("to_ref_id");
-- Create "document_text" table
CREATE TABLE "silver"."document_text" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "document_id" bigint NOT NULL, "authority" text NOT NULL, "source" text NOT NULL DEFAULT '', "raw_file_id" bigint NULL, "markdown" text NULL, "source_file_sha256" text NULL, "verbatim_sha256" text NULL, "is_binding" boolean NOT NULL DEFAULT false, "extract_engine" text NULL, "extract_confidence" double precision NULL, "needs_review" boolean NOT NULL DEFAULT false, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "uq_silver_document_text" UNIQUE ("document_id", "authority", "source"), CONSTRAINT "fk_silver_text_document" FOREIGN KEY ("document_id") REFERENCES "silver"."document" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "chk_silver_text_authority" CHECK (authority = ANY (ARRAY['gazette_borndigital'::text, 'transcription_html'::text, 'ocr_extractive'::text, 'ocr_generative'::text, 'human_verified'::text])));
-- Create index "idx_silver_text_document" to table: "document_text"
CREATE INDEX "idx_silver_text_document" ON "silver"."document_text" ("document_id");
-- Create "document_topic" table
CREATE TABLE "silver"."document_topic" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "document_id" bigint NOT NULL, "topic" text NOT NULL, "topic_source" text NOT NULL, "matched_keyword" text NULL, "confidence" double precision NULL, PRIMARY KEY ("id"), CONSTRAINT "uq_silver_document_topic" UNIQUE ("document_id", "topic", "topic_source"), CONSTRAINT "fk_silver_topic_document" FOREIGN KEY ("document_id") REFERENCES "silver"."document" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "chk_silver_topic_source" CHECK (topic_source = ANY (ARRAY['linhvuc_source'::text, 'classifier'::text, 'keyword_match'::text])));
-- Create index "idx_silver_topic_document" to table: "document_topic"
CREATE INDEX "idx_silver_topic_document" ON "silver"."document_topic" ("document_id");
-- Create index "idx_silver_topic_topic" to table: "document_topic"
CREATE INDEX "idx_silver_topic_topic" ON "silver"."document_topic" ("topic");
-- Create "validity_period" table
CREATE TABLE "silver"."validity_period" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "document_id" bigint NOT NULL, "section_id" bigint NULL, "version_id" bigint NULL, "status_code" text NOT NULL, "status_class" text NOT NULL, "eff_from" timestamptz NULL, "eff_to" timestamptz NULL, "reason" text NULL, "caused_by_ref_id" bigint NULL, "source" text NULL, "observed_at" timestamptz NOT NULL, "superseded_at" timestamptz NULL, PRIMARY KEY ("id"), CONSTRAINT "fk_silver_validity_document" FOREIGN KEY ("document_id") REFERENCES "silver"."document" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "fk_silver_validity_section" FOREIGN KEY ("section_id") REFERENCES "silver"."document_section" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "chk_silver_validity_class" CHECK (status_class = ANY (ARRAY['in_force'::text, 'expired'::text, 'partial'::text, 'not_yet'::text, 'suspended'::text, 'inappropriate'::text])));
-- Create index "idx_silver_validity_current" to table: "validity_period"
CREATE INDEX "idx_silver_validity_current" ON "silver"."validity_period" ("document_id", "superseded_at");
-- Create index "idx_silver_validity_document" to table: "validity_period"
CREATE INDEX "idx_silver_validity_document" ON "silver"."validity_period" ("document_id");
-- Create index "idx_silver_validity_section" to table: "validity_period"
CREATE INDEX "idx_silver_validity_section" ON "silver"."validity_period" ("section_id");
