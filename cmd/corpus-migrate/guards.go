package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"danny.vn/banhmi/deploy/corpusmigrations"
)

type inventoryEntry struct {
	Schema        string `json:"schema"`
	Kind          string `json:"kind"`
	Identity      string `json:"identity"`
	CreateVersion int64  `json:"create_version"`
	RemoveVersion int64  `json:"remove_version,omitempty"`
}

func (e inventoryEntry) key() string {
	return e.Schema + "/" + e.Kind + "/" + e.Identity
}

func loadInventory(c chainSpec) ([]inventoryEntry, error) {
	data, err := corpusmigrations.FS.ReadFile("inventory/" + c.name + ".json")
	if err != nil {
		return nil, fmt.Errorf("read inventory: %w", err)
	}
	var entries []inventoryEntry
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode inventory: %w", err)
	}
	if entries == nil {
		return nil, fmt.Errorf("inventory must be a JSON array")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("inventory has trailing data")
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if !contains(c.schemas, e.Schema) || e.Identity == "" || e.CreateVersion < 1 || e.CreateVersion > int64(len(c.versions)) ||
			(e.RemoveVersion != 0 && (e.RemoveVersion <= e.CreateVersion || e.RemoveVersion > int64(len(c.versions)))) || seen[e.key()] {
			return nil, fmt.Errorf("invalid inventory entry %s", e.key())
		}
		switch e.Kind {
		case "relation", "function", "type", "trigger":
		default:
			return nil, fmt.Errorf("invalid inventory kind")
		}
		seen[e.key()] = true
	}
	return entries, nil
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

// catalogObjects deliberately reads pg_catalog without privilege filtering.
// PostgreSQL 17 reserves OIDs below FirstNormalObjectId (16384) for system
// objects. Normal OIDs are inspected in every namespace. OIDs also prove
// generated storage dependencies, but never serve as inventory identities.
const catalogObjects = `
WITH approved_extensions AS (
    SELECT e.oid FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace
    WHERE n.nspname='public' AND ((e.extname='vector' AND e.extversion='0.8.2')
       OR (e.extname='btree_gist' AND e.extversion='1.7'))
), extension_objects AS (
    SELECT d.classid, d.objid FROM pg_depend d JOIN approved_extensions e ON e.oid=d.refobjid
    WHERE d.refclassid='pg_extension'::regclass AND d.deptype='e'
), version_relation AS (
    SELECT c.oid FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname='public' AND c.relname=$1 AND c.relkind='r'
), version_relations AS (
    SELECT oid FROM version_relation
    UNION SELECT i.indexrelid FROM pg_index i JOIN version_relation v ON v.oid=i.indrelid
    UNION SELECT d.objid FROM pg_depend d JOIN version_relation v ON v.oid=d.refobjid
          JOIN pg_class c ON c.oid=d.objid AND c.relkind='S'
          WHERE d.classid='pg_class'::regclass AND d.refclassid='pg_class'::regclass AND d.deptype IN ('a','i')
), generated_toast AS (
    -- Prove server-created TOAST storage and its primary index through the
    -- catalogs. Extra user relations in this namespace must remain visible.
    SELECT toast.oid FROM pg_class toast JOIN pg_namespace n ON n.oid=toast.relnamespace
    JOIN pg_class parent ON parent.reltoastrelid=toast.oid
    WHERE n.nspname='pg_toast' AND n.oid < 16384 AND toast.relkind='t'
      AND toast.relowner=parent.relowner
      AND EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid='pg_class'::regclass AND d.objid=toast.oid
          AND d.refclassid='pg_class'::regclass AND d.refobjid=parent.oid AND d.deptype='i')
    UNION SELECT i.indexrelid FROM pg_index i JOIN pg_class toast ON toast.oid=i.indrelid
    JOIN pg_class storage_index ON storage_index.oid=i.indexrelid
    JOIN pg_namespace n ON n.oid=toast.relnamespace
    JOIN pg_class parent ON parent.reltoastrelid=toast.oid
    WHERE n.nspname='pg_toast' AND n.oid < 16384 AND toast.relkind='t'
      AND toast.relowner=parent.relowner AND storage_index.relowner=parent.relowner
      AND storage_index.relnamespace=n.oid AND i.indisprimary AND i.indisunique
      AND EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid='pg_class'::regclass AND d.objid=toast.oid
          AND d.refclassid='pg_class'::regclass AND d.refobjid=parent.oid AND d.deptype='i')
), objects AS (
    SELECT n.nspname AS schema, 'relation' AS kind, c.relname AS identity, c.relowner AS owner,
           EXISTS (SELECT 1 FROM extension_objects e WHERE e.classid='pg_class'::regclass AND e.objid=c.oid) AS extension,
           EXISTS (SELECT 1 FROM version_relations v WHERE v.oid=c.oid) AS version_object,
           ''::text AS parent, false AS internal, ''::text AS constraint_schema, ''::text AS constraint_relation
    FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE c.oid >= 16384 AND NOT EXISTS (SELECT 1 FROM generated_toast g WHERE g.oid=c.oid)
    UNION ALL
    SELECT n.nspname, 'function', p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')', p.proowner,
           EXISTS (SELECT 1 FROM extension_objects e WHERE e.classid='pg_proc'::regclass AND e.objid=p.oid),
           false, '', false, '', ''
    FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE p.oid >= 16384
    UNION ALL
    SELECT n.nspname, 'type', t.typname, t.typowner,
           EXISTS (SELECT 1 FROM extension_objects e WHERE e.classid='pg_type'::regclass AND e.objid IN (t.oid,t.typelem)),
           EXISTS (SELECT 1 FROM version_relations v WHERE v.oid=COALESCE(NULLIF(t.typrelid,0),element.typrelid)),
           COALESCE(row_relation.relname, ''), false, '', ''
    FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace
    LEFT JOIN pg_type element ON element.oid=t.typelem AND element.typarray=t.oid
    LEFT JOIN pg_class row_relation ON row_relation.oid=COALESCE(NULLIF(t.typrelid,0),element.typrelid)
    WHERE t.oid >= 16384
    UNION ALL
    SELECT n.nspname, 'trigger', c.relname || '.' || t.tgname, c.relowner, false, false, c.relname,
           t.tgisinternal, COALESCE(cn.nspname,''), COALESCE(cc.relname,'')
    FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
    LEFT JOIN pg_constraint constraint_row ON constraint_row.oid=t.tgconstraint
    LEFT JOIN pg_class cc ON cc.oid=constraint_row.conrelid
    LEFT JOIN pg_namespace cn ON cn.oid=cc.relnamespace WHERE t.oid >= 16384
    UNION ALL
    SELECT n.nspname, extra.kind, extra.name, extra.owner,
           EXISTS(SELECT 1 FROM extension_objects e WHERE e.classid=extra.classid AND e.objid=extra.oid),
           false, '', false, '', ''
    FROM (
        SELECT 'operator' AS kind, 'pg_operator'::regclass AS classid, oid, oprnamespace AS namespace, oprname AS name, oprowner AS owner FROM pg_operator
        UNION ALL SELECT 'operator class', 'pg_opclass'::regclass, oid, opcnamespace, opcname, opcowner FROM pg_opclass
        UNION ALL SELECT 'operator family', 'pg_opfamily'::regclass, oid, opfnamespace, opfname, opfowner FROM pg_opfamily
        UNION ALL SELECT 'collation', 'pg_collation'::regclass, oid, collnamespace, collname, collowner FROM pg_collation
        UNION ALL SELECT 'conversion', 'pg_conversion'::regclass, oid, connamespace, conname, conowner FROM pg_conversion
        UNION ALL SELECT 'text search configuration', 'pg_ts_config'::regclass, oid, cfgnamespace, cfgname, cfgowner FROM pg_ts_config
        UNION ALL SELECT 'text search dictionary', 'pg_ts_dict'::regclass, oid, dictnamespace, dictname, dictowner FROM pg_ts_dict
        UNION ALL SELECT 'text search parser', 'pg_ts_parser'::regclass, oid, prsnamespace, prsname, 0::oid FROM pg_ts_parser
        UNION ALL SELECT 'text search template', 'pg_ts_template'::regclass, oid, tmplnamespace, tmplname, 0::oid FROM pg_ts_template
    ) extra JOIN pg_namespace n ON n.oid=extra.namespace WHERE extra.oid >= 16384
)
SELECT schema, kind, identity, pg_get_userbyid(owner), extension, version_object, parent, internal,
       constraint_schema, constraint_relation
FROM objects
ORDER BY schema, CASE WHEN kind='trigger' THEN 0 ELSE 1 END, kind, identity`

func preflight(ctx context.Context, tx *sql.Tx, c chainSpec, status bool) (int64, error) {
	// Catalog names and built-in functions must not resolve through a caller's
	// search_path. SET LOCAL changes no persistent object in this read-only tx.
	if _, err := tx.ExecContext(ctx, "SET LOCAL search_path = pg_catalog"); err != nil {
		return 0, err
	}
	var foreignGlobalObject bool
	if err := tx.QueryRowContext(ctx, `SELECT
        EXISTS(SELECT 1 FROM pg_default_acl)
        OR EXISTS(SELECT 1 FROM pg_event_trigger) OR EXISTS(SELECT 1 FROM pg_foreign_server)
        OR EXISTS(SELECT 1 FROM pg_foreign_data_wrapper) OR EXISTS(SELECT 1 FROM pg_largeobject_metadata)
        OR EXISTS(SELECT 1 FROM pg_publication)
        OR EXISTS(SELECT 1 FROM pg_language WHERE lanname NOT IN ('internal','c','sql','plpgsql'))
        OR EXISTS(SELECT 1 FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace
            WHERE NOT ((e.extname='plpgsql' AND n.nspname='pg_catalog')
                OR (n.nspname='public' AND ((e.extname='vector' AND e.extversion='0.8.2')
                    OR (e.extname='btree_gist' AND e.extversion='1.7')))))`).Scan(&foreignGlobalObject); err != nil {
		return 0, err
	}
	if foreignGlobalObject {
		return 0, fmt.Errorf("unexpected database-level object, extension, or default privileges")
	}
	// Rules can suppress Goose history writes without causing an SQL error.
	// Reject them before reading history or making any Goose call.
	var rewriteRule bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_rewrite r
        JOIN pg_class c ON c.oid=r.ev_class WHERE c.oid >= 16384)`).Scan(&rewriteRule); err != nil {
		return 0, err
	}
	if rewriteRule {
		return 0, fmt.Errorf("unexpected relation rewrite rule")
	}
	var tableExists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
        WHERE n.nspname='public' AND c.relname=$1 AND c.relkind='r')`, c.table).Scan(&tableExists); err != nil { return 0, err }
	var foreignHistory bool
	if err := tx.QueryRowContext(ctx, `WITH selected AS (
            SELECT c.oid FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
            WHERE n.nspname='public' AND c.relname=$1 AND c.relkind='r'
        ), permitted AS (
            SELECT oid FROM selected
            UNION SELECT i.indexrelid FROM pg_index i JOIN selected s ON s.oid=i.indrelid
            UNION SELECT d.objid FROM pg_depend d JOIN selected s ON s.oid=d.refobjid
                JOIN pg_class c ON c.oid=d.objid AND c.relkind='S'
                WHERE d.classid='pg_class'::regclass AND d.refclassid='pg_class'::regclass AND d.deptype IN ('a','i')
        ) SELECT EXISTS(SELECT 1 FROM pg_class c WHERE c.relname ~ '^goose_db_version'
            AND NOT EXISTS(SELECT 1 FROM permitted p WHERE p.oid=c.oid))`, c.table).Scan(&foreignHistory); err != nil {
		return 0, err
	}
	if foreignHistory {
		return 0, fmt.Errorf("foreign Goose version relation")
	}
	version, owner, err := readHistory(ctx, tx, c, tableExists)
	if err != nil {
		return 0, err
	}
	if err := checkRoles(ctx, tx, c, owner, status); err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT nspname, pg_get_userbyid(nspowner) FROM pg_namespace
        WHERE oid >= 16384 AND nspname <> 'public'
          AND oid <> pg_my_temp_schema() AND NOT pg_is_other_temp_schema(oid)`)
	if err != nil {
		return 0, err
	}
	seenSchemas := map[string]bool{}
	for rows.Next() {
		var name, schemaOwner string
		if err := rows.Scan(&name, &schemaOwner); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if version == 0 || !contains(c.schemas, name) || schemaOwner != owner {
			_ = rows.Close(); return 0, fmt.Errorf("unexpected schema or owner: %s", name)
		}
		seenSchemas[name] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if version > 0 && len(seenSchemas) != len(c.schemas) {
		return 0, fmt.Errorf("missing chain schema")
	}
	expected := map[string]bool{}
	for _, entry := range c.inventory {
		if entry.CreateVersion <= version && (entry.RemoveVersion == 0 || entry.RemoveVersion > version) {
			expected[entry.key()] = true
		}
	}
	rows, err = tx.QueryContext(ctx, catalogObjects, c.table)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	seen := map[string]bool{}
	for rows.Next() {
		var schema, kind, identity, objectOwner, parent, constraintSchema, constraintRelation string
		var extension, versionObject, internal bool
		if err := rows.Scan(&schema, &kind, &identity, &objectOwner, &extension, &versionObject, &parent, &internal, &constraintSchema, &constraintRelation); err != nil {
			return 0, err
		}
		key := schema + "/" + kind + "/" + identity
		if kind == "relation" && strings.HasPrefix(identity, "goose_db_version") && !versionObject {
			return 0, fmt.Errorf("foreign version relation: %s", key)
		}
		if schema == "public" {
			if versionObject && objectOwner == owner {
				continue
			}
			if extension {
				continue
			}
			return 0, fmt.Errorf("unexpected public object: %s", key)
		}
		if !seenSchemas[schema] || objectOwner != owner {
			return 0, fmt.Errorf("unexpected object or owner: %s", key)
		}
		if kind == "type" && parent != "" && expected[schema+"/relation/"+parent] {
			continue
		}
		if kind == "trigger" && internal {
			if expected[schema+"/relation/"+parent] && expected[constraintSchema+"/relation/"+constraintRelation] {
				continue
			}
			return 0, fmt.Errorf("unowned internal trigger: %s", key)
		}
		if !expected[key] {
			return 0, fmt.Errorf("undeclared object: %s", key)
		}
		seen[key] = true
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for key := range expected {
		if !seen[key] {
			return 0, fmt.Errorf("missing object: %s", key)
		}
	}
	return version, nil
}

func readHistory(ctx context.Context, tx *sql.Tx, c chainSpec, exists bool) (int64, string, error) {
	if !exists {
		return 0, "", nil
	}
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT pg_get_userbyid(c.relowner) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
        WHERE n.nspname='public' AND c.relname=$1`, c.table).Scan(&owner); err != nil { return 0, "", err }
	rows, err := tx.QueryContext(ctx, `SELECT a.attname, format_type(a.atttypid,a.atttypmod), a.attnotnull
        FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace
        WHERE n.nspname='public' AND c.relname=$1 AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, c.table)
	if err != nil {
		return 0, "", err
	}
	wantNames := []string{"id", "version_id", "is_applied", "tstamp"}
	wantTypes := []string{"integer", "bigint", "boolean", "timestamp without time zone"}
	count := 0
	for rows.Next() {
		var name, typ string
		var notNull bool
		if err := rows.Scan(&name, &typ, &notNull); err != nil {
			_ = rows.Close()
			return 0, "", err
		}
		if count >= len(wantNames) || name != wantNames[count] || typ != wantTypes[count] || !notNull {
			_ = rows.Close(); return 0, "", fmt.Errorf("invalid Goose version columns")
		}
		count++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, "", err
	}
	if err := rows.Close(); err != nil {
		return 0, "", err
	}
	if count != 4 {
		return 0, "", fmt.Errorf("invalid Goose version columns")
	}
	var validShape bool
	if err := tx.QueryRowContext(ctx, `SELECT c.relpersistence='p' AND NOT c.relrowsecurity AND NOT c.relforcerowsecurity
        AND EXISTS(SELECT 1 FROM pg_attribute a WHERE a.attrelid=c.oid AND a.attname='id' AND a.attidentity='d')
        AND EXISTS(SELECT 1 FROM pg_constraint k WHERE k.conrelid=c.oid AND k.contype='p' AND k.conkey=ARRAY[1]::smallint[])
        FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname=$1`, c.table).Scan(&validShape); err != nil { return 0, "", err }
	if !validShape {
		return 0, "", fmt.Errorf("invalid Goose version identity or primary key")
	}
	// The identifier is a constant derived from the validated chain, never user SQL.
	rows, err = tx.QueryContext(ctx, `SELECT id, version_id, is_applied FROM public.` + c.table + ` ORDER BY id`)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = rows.Close() }()
	var version, lastID int64
	initialized := false
	for rows.Next() {
		var id, v int64
		var applied bool
		if err := rows.Scan(&id, &v, &applied); err != nil {
			return 0, "", err
		}
		if id <= lastID || !applied || (!initialized && v != 0) || (initialized && v != version+1) || v > int64(len(c.versions)) {
			return 0, "", fmt.Errorf("unknown or noncontiguous Goose history")
		}
		initialized = true
		lastID, version = id, v
	}
	if err := rows.Err(); err != nil {
		return 0, "", err
	}
	return version, owner, nil
}

func checkRoles(ctx context.Context, tx *sql.Tx, c chainSpec, owner string, status bool) error {
	var principal string
	if err := tx.QueryRowContext(ctx, "SELECT current_user").Scan(&principal); err != nil {
		return err
	}
	absent := owner == ""
	if absent {
		owner = principal
	}
	if !status && principal != owner {
		return fmt.Errorf("migration login must own the chain")
	}
	// Read-only callers may inherit their one capability. Owners never inherit roles.
	var valid bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles r WHERE r.rolname=$1 AND r.rolcanlogin
        AND NOT (r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls)
        AND NOT EXISTS(SELECT 1 FROM pg_auth_members m WHERE m.member=r.oid))`, owner).Scan(&valid); err != nil { return err }
	if !(status && absent) && !valid {
		return fmt.Errorf("invalid migration owner role")
	}
	caps := []string{"banhmi_corpus_" + c.name + "_runtime"}
	if c.name == "writer" {
		caps = append(caps, "banhmi_corpus_writer_export")
	}
	if c.name == "release" { caps = []string{"banhmi_corpus_release_reader", "banhmi_corpus_release_loader"} }
	var runtimeCreate bool
	if err := tx.QueryRowContext(ctx, `WITH runtime_roles AS (
        SELECT r.oid FROM pg_roles r WHERE r.rolname IN ('banhmi_corpus_writer_runtime',
            'banhmi_corpus_writer_export','banhmi_corpus_release_reader',
            'banhmi_corpus_release_loader','banhmi_corpus_control_runtime')
        UNION SELECT m.member FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.roleid
            WHERE r.rolname IN ('banhmi_corpus_writer_runtime','banhmi_corpus_writer_export',
                'banhmi_corpus_release_reader','banhmi_corpus_release_loader','banhmi_corpus_control_runtime')
    ) SELECT EXISTS(SELECT 1 FROM runtime_roles r
        WHERE has_database_privilege(r.oid,current_database(),'CREATE')
           OR EXISTS(SELECT 1 FROM pg_namespace n WHERE (n.nspname='public' OR n.nspname=ANY($1::text[]))
               AND has_schema_privilege(r.oid,n.oid,'CREATE')))`, c.schemas).Scan(&runtimeCreate); err != nil {
		return err
	}
	if runtimeCreate {
		return fmt.Errorf("capability role or member login holds CREATE privileges")
	}
	for _, cap := range caps {
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles r WHERE r.rolname=$1
            AND NOT (r.rolcanlogin OR r.rolinherit OR r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls)
            AND NOT EXISTS(SELECT 1 FROM pg_auth_members m WHERE m.member=r.oid)
            AND NOT EXISTS(SELECT 1 FROM pg_auth_members m JOIN pg_roles login ON login.oid=m.member
                WHERE m.roleid=r.oid AND (NOT login.rolcanlogin OR login.rolsuper OR login.rolcreatedb OR login.rolcreaterole
                    OR login.rolreplication OR login.rolbypassrls OR m.admin_option OR NOT m.inherit_option OR m.set_option
                    OR (SELECT count(*) FROM pg_auth_members other WHERE other.member=login.oid) <> 1)))`, cap).Scan(&valid); err != nil { return err }
		if !valid {
			return fmt.Errorf("invalid capability or login membership: %s", cap)
		}
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles r WHERE r.rolname=$1 AND r.rolcanlogin
        AND NOT (r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls)
        AND NOT EXISTS(SELECT 1 FROM pg_auth_members m JOIN pg_roles parent ON parent.oid=m.roleid
            WHERE m.member=r.oid AND (parent.rolname <> ALL($2::text[]) OR m.admin_option OR NOT m.inherit_option OR m.set_option)))`, principal, caps).Scan(&valid); err != nil { return err }
	if !valid {
		return fmt.Errorf("invalid status or migration principal")
	}
	if !status {
		if err := tx.QueryRowContext(ctx, `SELECT has_database_privilege(current_user,current_database(),'CREATE')
            AND has_schema_privilege(current_user,'public','CREATE')`).Scan(&valid); err != nil { return err }
		if !valid {
			return fmt.Errorf("missing migration CREATE privileges")
		}
	}
	return nil
}

