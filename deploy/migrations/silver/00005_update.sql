-- +goose Up
-- Modify "document_section" table
ALTER TABLE "silver"."document_section" DROP CONSTRAINT "chk_silver_section_kind", ADD CONSTRAINT "chk_silver_section_kind" CHECK (kind = ANY (ARRAY['phan'::text, 'chuong'::text, 'muc'::text, 'dieu'::text, 'khoan'::text, 'diem'::text, 'phuluc'::text, 'part'::text, 'chapter'::text, 'section'::text, 'subsection'::text, 'paragraph'::text, 'schedule'::text]));
