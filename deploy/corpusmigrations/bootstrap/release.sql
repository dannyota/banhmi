\set ON_ERROR_STOP on
\if :{?migration_login_role}
\else
DO $missing$ BEGIN RAISE EXCEPTION 'migration_login_role is required'; END $missing$;
\endif
\if :{?runtime_login_role}
\else
DO $missing$ BEGIN RAISE EXCEPTION 'runtime_login_role is required'; END $missing$;
\endif
\if :{?loader_login_role}
\if :{?restore_login_role}
DO $missing$ BEGIN RAISE EXCEPTION 'provide exactly one of loader_login_role or restore_login_role'; END $missing$;
\endif
\set load_login_role :loader_login_role
\set restoring false
\else
\if :{?restore_login_role}
\set load_login_role :restore_login_role
\set restoring true
\else
DO $missing$ BEGIN RAISE EXCEPTION 'provide exactly one of loader_login_role or restore_login_role'; END $missing$;
\endif
\endif
BEGIN;
SET LOCAL search_path = pg_catalog;
SELECT set_config('bootstrap.migration_login_role', :'migration_login_role', true);
SELECT set_config('bootstrap.runtime_login_role', :'runtime_login_role', true);
SELECT set_config('bootstrap.load_login_role', :'load_login_role', true);
SELECT set_config('bootstrap.restoring', :'restoring', true);
DO $bootstrap$
DECLARE
    migration_name text := current_setting('bootstrap.migration_login_role');
    login_names text[] := ARRAY[migration_name, current_setting('bootstrap.runtime_login_role'), current_setting('bootstrap.load_login_role')];
    capability_names text[] := ARRAY['banhmi_corpus_release_reader', 'banhmi_corpus_release_loader'];
    login_name text;
    capability_name text;
    expected_name text;
    inherit_membership boolean;
    set_membership boolean;
    role_record record;
    position integer;
BEGIN
    IF (SELECT count(DISTINCT name) FROM unnest(login_names) AS names(name)) <> cardinality(login_names)
       OR EXISTS (SELECT 1 FROM unnest(login_names) AS names(name) WHERE name = '' OR name = ANY(capability_names)) THEN
        RAISE EXCEPTION 'bootstrap logins must be distinct and nonempty';
    END IF;
    FOREACH capability_name IN ARRAY capability_names LOOP
        IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = capability_name) THEN
            EXECUTE format('CREATE ROLE %I NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS', capability_name);
        END IF;
        SELECT * INTO STRICT role_record FROM pg_roles WHERE rolname = capability_name;
        IF role_record.rolcanlogin OR role_record.rolinherit OR role_record.rolsuper OR role_record.rolcreatedb
           OR role_record.rolcreaterole OR role_record.rolreplication OR role_record.rolbypassrls
           OR EXISTS (SELECT 1 FROM pg_auth_members WHERE member = role_record.oid) THEN
            RAISE EXCEPTION 'invalid capability role %', capability_name;
        END IF;
        EXECUTE format('REVOKE ALL ON DATABASE %I FROM %I', current_database(), capability_name);
        EXECUTE format('REVOKE ALL ON SCHEMA public FROM %I', capability_name);
    END LOOP;
    FOR position IN 1..cardinality(login_names) LOOP
        login_name := login_names[position];
        SELECT * INTO STRICT role_record FROM pg_roles WHERE rolname = login_name;
        IF NOT role_record.rolcanlogin OR role_record.rolsuper OR role_record.rolcreatedb OR role_record.rolcreaterole
           OR role_record.rolreplication OR role_record.rolbypassrls THEN
            RAISE EXCEPTION 'invalid bootstrap login %', login_name;
        END IF;
        IF position = 1 THEN
            IF EXISTS (SELECT 1 FROM pg_auth_members WHERE member = role_record.oid) THEN
                RAISE EXCEPTION 'migration login must have no membership';
            END IF;
        ELSE
            expected_name := capability_names[position - 1];
            inherit_membership := true;
            set_membership := false;
            IF position = 3 AND current_setting('bootstrap.restoring')::boolean THEN
                expected_name := migration_name;
                inherit_membership := false;
                set_membership := true;
            END IF;
            IF EXISTS (SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid = m.roleid
                       WHERE m.member = role_record.oid AND (r.rolname <> expected_name OR m.admin_option
                             OR m.inherit_option <> inherit_membership OR m.set_option <> set_membership))
               OR (SELECT count(*) FROM pg_auth_members WHERE member = role_record.oid) > 1 THEN
                RAISE EXCEPTION 'unexpected bootstrap membership for %', login_name;
            END IF;
            EXECUTE format('GRANT %I TO %I WITH INHERIT %s, SET %s, ADMIN FALSE',
                           expected_name, login_name, CASE WHEN inherit_membership THEN 'TRUE' ELSE 'FALSE' END,
                           CASE WHEN set_membership THEN 'TRUE' ELSE 'FALSE' END);
        END IF;
        EXECUTE format('REVOKE ALL ON DATABASE %I FROM %I', current_database(), login_name);
        EXECUTE format('REVOKE ALL ON SCHEMA public FROM %I', login_name);
        EXECUTE format('GRANT CONNECT ON DATABASE %I TO %I', current_database(), login_name);
        EXECUTE format('GRANT USAGE ON SCHEMA public TO %I', login_name);
    END LOOP;
    EXECUTE format('REVOKE CONNECT, CREATE, TEMPORARY ON DATABASE %I FROM PUBLIC', current_database());
    REVOKE ALL ON SCHEMA public FROM PUBLIC;
    EXECUTE format('GRANT CREATE ON DATABASE %I TO %I', current_database(), migration_name);
    EXECUTE format('GRANT USAGE, CREATE ON SCHEMA public TO %I', migration_name);
    IF EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolcanlogin AND NOT r.rolsuper AND r.rolname <> migration_name
               AND (has_database_privilege(r.oid, current_database(), 'CREATE')
                    OR has_schema_privilege(r.oid, 'public', 'CREATE'))) THEN
        RAISE EXCEPTION 'only the migration login may hold CREATE privileges';
    END IF;
END
$bootstrap$;
COMMIT;
