-- +goose Up
-- Modify "document" table
ALTER TABLE "silver"."document" ADD COLUMN "index_class" text NOT NULL DEFAULT 'primary';
-- Modify "document_section" table
ALTER TABLE "silver"."document_section" DROP CONSTRAINT "chk_silver_section_kind", ADD CONSTRAINT "chk_silver_section_kind" CHECK (kind = ANY (ARRAY['phan'::text, 'chuong'::text, 'muc'::text, 'dieu'::text, 'khoan'::text, 'diem'::text, 'phuluc'::text]));
