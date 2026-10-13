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
    IF (SELECT count(*) FROM pg_roles r WHERE r.rolname IN ('banhmi_corpus_release_reader', 'banhmi_corpus_release_loader')
        AND NOT (r.rolcanlogin OR r.rolinherit OR r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls)
        AND NOT EXISTS (SELECT 1 FROM pg_auth_members m WHERE m.member = r.oid)) <> 2 THEN
        RAISE EXCEPTION 'invalid release capability roles';
    END IF;
END
$base$;
-- +goose StatementEnd
CREATE SCHEMA silver;
CREATE SCHEMA gold;
CREATE SCHEMA config;
CREATE SCHEMA release;
REVOKE ALL ON SCHEMA silver, gold, config, release FROM PUBLIC;
GRANT USAGE ON SCHEMA silver, gold, config, release TO banhmi_corpus_release_reader, banhmi_corpus_release_loader;

-- +goose Down
-- +goose StatementBegin
DO $down$
BEGIN
    RAISE EXCEPTION 'corpus migrations cannot be rolled back';
END
$down$;
-- +goose StatementEnd
