-- +goose Up
SET LOCAL search_path = pg_catalog;
-- +goose StatementBegin
DO $base$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname = 'banhmi_corpus_control_runtime'
        AND NOT (r.rolcanlogin OR r.rolinherit OR r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls)
        AND NOT EXISTS (SELECT 1 FROM pg_auth_members m WHERE m.member = r.oid)) THEN
        RAISE EXCEPTION 'invalid control capability role';
    END IF;
END
$base$;
-- +goose StatementEnd
CREATE SCHEMA control;
REVOKE ALL ON SCHEMA control FROM PUBLIC;
GRANT USAGE ON SCHEMA control TO banhmi_corpus_control_runtime;

-- +goose Down
-- +goose StatementBegin
DO $down$
BEGIN
    RAISE EXCEPTION 'corpus migrations cannot be rolled back';
END
$down$;
-- +goose StatementEnd
