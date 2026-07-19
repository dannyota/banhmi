-- +goose Up
-- Modify "validity_period" table
ALTER TABLE "silver"."validity_period" DROP CONSTRAINT "chk_silver_validity_class", ADD CONSTRAINT "chk_silver_validity_class" CHECK (status_class = ANY (ARRAY['in_force'::text, 'expired'::text, 'partial'::text, 'not_yet'::text, 'suspended'::text, 'inappropriate'::text, 'unknown'::text]));
