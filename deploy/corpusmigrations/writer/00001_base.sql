-- +goose Up
SET LOCAL search_path = pg_catalog;
-- +goose StatementBegin
DO $base$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace
                   WHERE e.extname = 'vector' AND e.extversion = '0.8.2' AND n.nspname = 'public')
       OR NOT EXISTS (SELECT 1 FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace
                      WHERE e.extname = 'btree_gist' AND e.extversion = '1.7' AND n.nspname = 'public') THEN
        RAISE EXCEPTION 'provision vector 0.8.2 and btree_gist 1.7 in public first';
    END IF;
    IF (SELECT count(*) FROM pg_roles r WHERE r.rolname IN ('banhmi_corpus_writer_runtime', 'banhmi_corpus_writer_export')
        AND NOT (r.rolcanlogin OR r.rolinherit OR r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls)
        AND NOT EXISTS (SELECT 1 FROM pg_auth_members m WHERE m.member = r.oid)) <> 2 THEN
        RAISE EXCEPTION 'invalid writer capability roles';
    END IF;
END
$base$;
-- +goose StatementEnd
CREATE SCHEMA ingest;
CREATE SCHEMA bronze;
CREATE SCHEMA silver;
CREATE SCHEMA gold;
CREATE SCHEMA config;

-- +goose StatementBegin
CREATE FUNCTION ingest.reject_immutable_change() RETURNS trigger LANGUAGE plpgsql AS $immutable$
BEGIN
    RAISE EXCEPTION 'immutable evidence cannot change' USING ERRCODE = '55000';
END
$immutable$;
CREATE FUNCTION bronze.reject_immutable_change() RETURNS trigger LANGUAGE plpgsql AS $immutable$
BEGIN
    RAISE EXCEPTION 'immutable evidence cannot change' USING ERRCODE = '55000';
END
$immutable$;
CREATE FUNCTION silver.reject_immutable_change() RETURNS trigger LANGUAGE plpgsql AS $immutable$
BEGIN
    RAISE EXCEPTION 'immutable evidence cannot change' USING ERRCODE = '55000';
END
$immutable$;
CREATE FUNCTION gold.reject_immutable_change() RETURNS trigger LANGUAGE plpgsql AS $immutable$
BEGIN
    RAISE EXCEPTION 'immutable evidence cannot change' USING ERRCODE = '55000';
END
$immutable$;
CREATE FUNCTION config.reject_immutable_change() RETURNS trigger LANGUAGE plpgsql AS $immutable$
BEGIN
    RAISE EXCEPTION 'immutable evidence cannot change' USING ERRCODE = '55000';
END
$immutable$;
-- +goose StatementEnd
REVOKE ALL ON SCHEMA ingest, bronze, silver, gold, config FROM PUBLIC;
GRANT USAGE ON SCHEMA ingest, bronze, silver, gold, config TO banhmi_corpus_writer_runtime, banhmi_corpus_writer_export;
REVOKE ALL ON FUNCTION ingest.reject_immutable_change(), bronze.reject_immutable_change(), silver.reject_immutable_change(), gold.reject_immutable_change(), config.reject_immutable_change() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION ingest.reject_immutable_change(), bronze.reject_immutable_change(), silver.reject_immutable_change(), gold.reject_immutable_change(), config.reject_immutable_change() TO banhmi_corpus_writer_runtime;

-- +goose Down
-- +goose StatementBegin
DO $down$
BEGIN
    RAISE EXCEPTION 'corpus migrations cannot be rolled back';
END
$down$;
-- +goose StatementEnd
