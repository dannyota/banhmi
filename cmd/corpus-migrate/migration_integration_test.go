package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"

	"danny.vn/banhmi/deploy/corpusmigrations"
)

var fixtureSequence atomic.Uint64

type chainFixture struct {
	chain        chainSpec
	db           *sql.DB
	admin        *sql.DB
	adminURL     string
	migrationURL string
	database     string
}

func testURL(t *testing.T, chain string) *url.URL {
	t.Helper()
	name := "BANHMI_CORPUS_" + strings.ToUpper(chain) + "_TEST_DATABASE_URL"
	value := os.Getenv(name)
	if value == "" {
		if testing.Short() {
			t.Skip(name + " is absent")
		}
		t.Fatal(name + " is required outside short mode")
	}
	u, err := url.Parse(value)
	if err != nil {
		t.Fatal("invalid synthetic test URL")
	}
	// Fixtures must never create/drop databases on an arbitrary service.
	if (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") || u.Path != "/banhmi_corpus_"+chain+"_ci" ||
		u.User == nil || u.User.Username() != "banhmi_corpus_"+chain+"_migrator" {
		t.Fatal("chain tests require the isolated trust-authenticated CI service")
	}
	if _, set := u.User.Password(); set {
		t.Fatal("chain tests require password-free synthetic logins")
	}
	if (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Port() != "5432" || u.Fragment != "" ||
		len(u.Query()) != 1 || u.Query().Get("sslmode") != "disable" {
		t.Fatal("chain test URL must use the fixed disposable service without connection overrides")
	}
	return u
}

func openTestDB(t *testing.T, value string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", value)
	if err != nil {
		t.Fatal("open fixture database failed")
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal("connect fixture database failed")
	}
	return db
}

func execSQL(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), statement); err != nil {
		t.Fatalf("fixture SQL: %v", err)
	}
}

func tool(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(name, args...)
	// All command URLs are validated, synthetic and password-free. No credential
	// files or production environment are consulted by the service authentication.
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture %s: %v\n%s", name, err, output)
	}
	return output
}

func newFixture(t *testing.T, name string) *chainFixture {
	t.Helper()
	return newFixtureMode(t, name, false)
}

func newFixtureMode(t *testing.T, name string, restoring bool) *chainFixture {
	t.Helper()
	u := testURL(t, name)
	adminURL := *u
	adminURL.User = url.User("postgres")
	adminURL.Path = "/postgres"
	admin := openTestDB(t, adminURL.String())
	database := fmt.Sprintf("banhmi_corpus_fixture_%d_%d", time.Now().UnixNano(), fixtureSequence.Add(1))
	execSQL(t, admin, "CREATE DATABASE "+database)
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP DATABASE "+database+" WITH (FORCE)"); err != nil {
			t.Errorf("drop disposable fixture: %v", err)
		}
	})
	u.Path = "/" + database
	adminURL.Path = u.Path
	localAdmin := openTestDB(t, adminURL.String())
	if name != "control" {
		execSQL(t, localAdmin, "CREATE EXTENSION vector WITH SCHEMA public VERSION '0.8.2'; CREATE EXTENSION btree_gist WITH SCHEMA public VERSION '1.7'")
	}
	args := []string{"--no-psqlrc", "--dbname", adminURL.String(), "--set", "ON_ERROR_STOP=1", "--set", "migration_login_role=banhmi_corpus_" + name + "_migrator", "--set", "runtime_login_role=banhmi_corpus_" + name + "_app"}
	if name == "writer" {
		args = append(args, "--set", "export_login_role=banhmi_corpus_writer_exporter")
	}
	if name == "release" {
		if restoring {
			args = append(args, "--set", "restore_login_role=banhmi_corpus_release_restore")
		} else {
			args = append(args, "--set", "loader_login_role=banhmi_corpus_release_load")
		}
	}
	args = append(args, "--file", filepath.Join("..", "..", "deploy", "corpusmigrations", "bootstrap", name+".sql"))
	tool(t, "psql", args...)
	c, err := loadChain(name)
	if err != nil {
		t.Fatal(err)
	}
	f := &chainFixture{chain: c, db: openTestDB(t, u.String()), admin: localAdmin, adminURL: adminURL.String(), migrationURL: u.String(), database: database}
	if restoring {
		t.Cleanup(func() {
			execSQL(t, localAdmin, "REVOKE banhmi_corpus_release_migrator FROM banhmi_corpus_release_restore; REVOKE CONNECT ON DATABASE "+database+" FROM banhmi_corpus_release_restore")
		})
	}
	return f
}

func applyFixture(t *testing.T, f *chainFixture, opt options) {
	t.Helper()
	if err := migrate(context.Background(), f.db, f.chain, opt, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func assertInventory(t *testing.T, f *chainFixture, version int64) {
	t.Helper()
	tx, err := f.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	got, err := preflight(context.Background(), tx, f.chain, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != version {
		t.Fatalf("version %d, want %d", got, version)
	}
}

// loadChain reads the embedded directory and its JSON inventory.
func finalVersion(c chainSpec) int64 {
	return c.versions[len(c.versions)-1]
}

func inventoryAt(c chainSpec, version int64) []inventoryEntry {
	var entries []inventoryEntry
	for _, entry := range c.inventory {
		if entry.CreateVersion <= version && (entry.RemoveVersion == 0 || entry.RemoveVersion > version) {
			entries = append(entries, entry)
		}
	}
	return entries
}

func appliedHistory(c chainSpec, through int64) string {
	values := []string{"(0,true)"}
	for _, version := range c.versions {
		if version <= through {
			values = append(values, fmt.Sprintf("(%d,true)", version))
		}
	}
	return strings.Join(values, ",")
}

func inventoriedTable(t *testing.T, f *chainFixture, toast bool) string {
	t.Helper()
	for _, entry := range inventoryAt(f.chain, finalVersion(f.chain)) {
		if entry.Kind != "relation" {
			continue
		}
		var eligible bool
		if err := f.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_class c
            JOIN pg_namespace n ON n.oid=c.relnamespace
            WHERE n.nspname=$1 AND c.relname=$2 AND c.relkind='r'
              AND (NOT $3::boolean OR c.reltoastrelid <> 0))`, entry.Schema, entry.Identity, toast).Scan(&eligible); err != nil {
			t.Fatal(err)
		}
		if eligible {
			return `"` + strings.ReplaceAll(entry.Schema, `"`, `""`) + `"."` + strings.ReplaceAll(entry.Identity, `"`, `""`) + `"`
		}
	}
	t.Fatalf("inventory has no table with required TOAST storage=%t", toast)
	return ""
}

func TestWriterChainReplay(t *testing.T) {
	testReplay(t, "writer")
}
func TestReleaseChainReplay(t *testing.T) {
	testReplay(t, "release")
}
func TestControlChainReplay(t *testing.T) {
	testReplay(t, "control")
}

func TestRecreatedPlpgsqlReplay(t *testing.T) {
	for _, name := range []string{"writer", "release", "control"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, name)
			execSQL(t, f.admin, "DROP EXTENSION plpgsql; CREATE EXTENSION plpgsql WITH SCHEMA pg_catalog")
			var handlers int
			var normalOIDs bool
			if err := f.admin.QueryRow(`SELECT count(*), COALESCE(bool_and(p.oid >= 16384),false)
                FROM pg_proc p JOIN pg_depend d ON d.classid='pg_proc'::regclass AND d.objid=p.oid
                JOIN pg_extension e ON d.refclassid='pg_extension'::regclass AND d.refobjid=e.oid
                JOIN pg_namespace n ON n.oid=p.pronamespace
                WHERE e.extname='plpgsql' AND d.deptype='e' AND n.nspname='pg_catalog'
                  AND p.proname IN ('plpgsql_call_handler','plpgsql_inline_handler','plpgsql_validator')`).Scan(&handlers, &normalOIDs); err != nil {
				t.Fatal(err)
			}
			if handlers != 3 || !normalOIDs {
				t.Fatal("recreated plpgsql must have three normal-OID catalog handlers")
			}
			assertInventory(t, f, 0)
			assertReadOnlyStatus(t, f, false)
			for _, version := range f.chain.versions {
				applyFixture(t, f, options{hasTo: true, to: version})
				assertInventory(t, f, version)
				before := catalogSnapshot(t, f)
				applyFixture(t, f, options{hasTo: true, to: version})
				if got := catalogSnapshot(t, f); got != before {
					t.Fatal("recreated plpgsql prefix no-op changed catalog or history")
				}
			}
			before := catalogSnapshot(t, f)
			applyFixture(t, f, options{})
			if got := catalogSnapshot(t, f); got != before {
				t.Fatal("recreated plpgsql no-op changed catalog or history")
			}
			assertReadOnlyStatus(t, f, true)
		})
	}
}

func assertReadOnlyStatus(t *testing.T, f *chainFixture, applied bool) {
	t.Helper()
	if applied {
		execSQL(t, f.db, "GRANT SELECT ON public."+f.chain.table+" TO banhmi_corpus_"+f.chain.name+"_app")
	}
	u, err := url.Parse(f.migrationURL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.User("banhmi_corpus_" + f.chain.name + "_app")
	query := u.Query()
	query.Set("options", "-c default_transaction_read_only=on")
	u.RawQuery = query.Encode()
	readonly := openTestDB(t, u.String())
	before := catalogSnapshot(t, f)
	var out bytes.Buffer
	if err := migrate(context.Background(), readonly, f.chain, options{status: true}, &out); err != nil {
		t.Fatal(err)
	}
	state := "pending"
	if applied {
		state = "applied"
	}
	for _, file := range f.chain.files {
		if !strings.Contains(out.String(), file+" "+state) {
			t.Fatal("read-only status omitted file or state")
		}
	}
	if got := catalogSnapshot(t, f); got != before {
		t.Fatal("read-only status changed catalog or history")
	}
}

func testReplay(t *testing.T, name string) {
	f := newFixture(t, name)
	assertInventory(t, f, 0)
	for _, version := range f.chain.versions {
		applyFixture(t, f, options{hasTo: true, to: version})
		assertInventory(t, f, version)
		before := catalogSnapshot(t, f)
		applyFixture(t, f, options{hasTo: true, to: version})
		if got := catalogSnapshot(t, f); got != before {
			t.Fatal("second prefix replay changed catalog or history")
		}
	}
	t.Setenv(migrationDatabaseURLEnv, f.migrationURL)
	if err := run(context.Background(), []string{"-chain", name, "-to", fmt.Sprint(finalVersion(f.chain))}, io.Discard); err != nil {
		t.Fatal(err)
	}
	before := catalogSnapshot(t, f)
	applyFixture(t, f, options{})
	if got := catalogSnapshot(t, f); got != before {
		t.Fatal("default no-op replay changed catalog or history")
	}
	for _, args := range [][]string{
		{"-chain", name, "-to", "0"}, {"-chain", name, "-to", "-1"},
		{"-chain", name, "-to", fmt.Sprint(finalVersion(f.chain) + 1)}, {"-chain", name, "-status", "-to", fmt.Sprint(f.chain.versions[0])},
		{"-chain", name, "extra"}, {"-chain", name, "-unknown"}, {}, {"-chain", "legacy"},
	} {
		t.Setenv(migrationDatabaseURLEnv, f.migrationURL)
		if err := run(context.Background(), args, io.Discard); err == nil {
			t.Fatalf("accepted invalid flags %v", args)
		}
		if got := catalogSnapshot(t, f); got != before {
			t.Fatal("invalid flags changed database")
		}
	}
	// An explicit future-version fixture proves -to cannot move backwards even
	// when a target is a real file in the currently embedded prefix.
	opt := options{hasTo: true, to: f.chain.versions[0]}
	if len(f.chain.versions) > 1 {
		if err := migrate(context.Background(), f.db, f.chain, opt, io.Discard); err == nil {
			t.Fatal("accepted target below current version")
		}
	}
	gooseMu.Lock()
	if err := goose.SetDialect("postgres"); err != nil {
		gooseMu.Unlock()
		t.Fatal(err)
	}
	sub, err := fs.Sub(corpusmigrations.FS, name)
	if err != nil {
		gooseMu.Unlock()
		t.Fatal(err)
	}
	goose.SetBaseFS(sub)
	goose.SetTableName("public." + f.chain.table)
	err = goose.DownContext(context.Background(), f.db, ".")
	gooseMu.Unlock()
	if err == nil {
		t.Fatal("Down succeeded")
	}
	if got := catalogSnapshot(t, f); got != before {
		t.Fatal("failed Down changed catalog or history")
	}
	assertInventory(t, f, finalVersion(f.chain))
	var maximum int64
	if err := f.db.QueryRow("SELECT max(version_id) FROM public." + f.chain.table + " WHERE is_applied").Scan(&maximum); err != nil {
		t.Fatal(err)
	}
	if maximum != finalVersion(f.chain) {
		t.Fatalf("version table maximum %d, want %d", maximum, finalVersion(f.chain))
	}
	t.Run("restored-portable-inventory", func(t *testing.T) {
		restored := newFixtureMode(t, name, name == "release")
		dump := postgresTool(t, nil, "pg_dump", "--dbname", f.adminURL, "--format=custom")
		list := postgresTool(t, dump, "pg_restore", "--list")
		var keep []string
		for _, line := range strings.Split(string(list), "\n") {
			if !strings.Contains(line, " EXTENSION ") && !strings.Contains(line, " SCHEMA - public ") {
				keep = append(keep, line)
			}
		}
		// The service contains the matching PostgreSQL 17 client. The runner's
		// pg_dump may be older and cannot dump a PostgreSQL 17 server.
		toc := "/tmp/" + restored.database + ".list"
		postgresTool(t, []byte(strings.Join(keep, "\n")), "sh", "-c", `cat > "$1"`, "sh", toc)
		t.Cleanup(func() { postgresTool(t, nil, "rm", "--", toc) })
		restoreURL := restored.adminURL
		if name == "release" {
			u, err := url.Parse(restoreURL)
			if err != nil {
				t.Fatal(err)
			}
			u.User = url.User("banhmi_corpus_release_restore")
			restoreURL = u.String()
		}
		postgresTool(t, dump, "pg_restore", "--dbname", restoreURL, "--no-owner", "--no-acl", "--no-comments", "--exit-on-error",
			"--role=banhmi_corpus_"+name+"_migrator", "--use-list", toc)
		assertInventory(t, restored, finalVersion(restored.chain))
		var originalOID, restoredOID int64
		query := "SELECT oid::bigint FROM pg_namespace WHERE nspname=$1"
		if err := f.db.QueryRow(query, f.chain.schemas[0]).Scan(&originalOID); err != nil {
			t.Fatal(err)
		}
		if err := restored.db.QueryRow(query, f.chain.schemas[0]).Scan(&restoredOID); err != nil {
			t.Fatal(err)
		}
		if originalOID == restoredOID {
			t.Fatal("restored fixture did not change OIDs")
		}
		applyFixture(t, restored, options{})
	})
	t.Run("existing-empty-history-replay", func(t *testing.T) {
		empty := newFixture(t, name)
		makeVersionTable(t, empty, "(0,true)")
		execSQL(t, empty.db, "DELETE FROM public."+empty.chain.table)
		assertInventory(t, empty, 0)
		applyFixture(t, empty, options{})
		assertInventory(t, empty, finalVersion(empty.chain))
	})
	if name != "control" {
		for _, mutation := range []string{"DROP EXTENSION vector CASCADE", "DROP EXTENSION btree_gist CASCADE",
			"UPDATE pg_extension SET extversion='0.0' WHERE extname='vector'", "UPDATE pg_extension SET extversion='0.0' WHERE extname='btree_gist'"} {
			t.Run(mutation, func(t *testing.T) {
				broken := newFixture(t, name)
				execSQL(t, broken.admin, mutation)
				before := catalogSnapshot(t, broken)
				// Exercise the extension checks in 00001 itself, not only the
				// catalog's rejection of objects from a wrong-version extension.
				up, err := corpusmigrationsSQL(name)
				if err != nil {
					t.Fatal(err)
				}
				tx, err := broken.db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				_, applyErr := tx.Exec(up)
				_ = tx.Rollback()
				if applyErr == nil {
					t.Fatal("00001 accepted missing or wrong-version extension")
				}
				if got := catalogSnapshot(t, broken); got != before {
					t.Fatal("failed base extension check created objects")
				}
				if err := migrate(context.Background(), broken.db, broken.chain, options{}, io.Discard); err == nil {
					t.Fatal("migration accepted invalid extension provisioning")
				}
				var schemas int
				if err := broken.admin.QueryRow(`SELECT count(*) FROM pg_namespace WHERE nspname IN ('ingest','bronze','silver','gold','config','release')`).Scan(&schemas); err != nil {
					t.Fatal(err)
				}
				if schemas != 0 {
					t.Fatal("failed migration left chain schemas")
				}
			})
		}
	}
}

func catalogSnapshot(t *testing.T, f *chainFixture) string {
	t.Helper()
	var schemas string
	if err := f.admin.QueryRow(`SELECT COALESCE(json_agg(row(nspname,nspowner) ORDER BY nspname)::text,'[]') FROM pg_namespace
        WHERE oid >= 16384 OR nspname='public'`).Scan(&schemas); err != nil {
		t.Fatal(err)
	}
	rows, err := f.admin.Query(catalogObjects, f.chain.table)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out bytes.Buffer
	out.WriteString(schemas)
	for rows.Next() {
		var schema, kind, identity, owner, parent, cs, cr string
		var extension, versionObject, internal bool
		if err := rows.Scan(&schema, &kind, &identity, &owner, &extension, &versionObject, &parent, &internal, &cs, &cr); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&out, "%s/%s/%s/%s/%t/%t/%s/%t/%s/%s\n", schema, kind, identity, owner, extension, versionObject, parent, internal, cs, cr)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := f.admin.QueryRow("SELECT to_regclass($1) IS NOT NULL", "public."+f.chain.table).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		var history string
		if err := f.admin.QueryRow("SELECT COALESCE(json_agg(v ORDER BY id)::text,'[]') FROM public." + f.chain.table + " v").Scan(&history); err != nil {
			t.Fatal(err)
		}
		out.WriteString(history)
	}
	return out.String()
}

func TestWriterSchemaBoundaries(t *testing.T) {
	f := newFixture(t, "writer")
	for _, version := range f.chain.versions {
		applyFixture(t, f, options{hasTo: true, to: version})
		assertInventory(t, f, version)
		var expected int
		for _, entry := range inventoryAt(f.chain, version) {
			if entry.Kind == "relation" {
				expected++
			}
		}
		var relations int
		if err := f.db.QueryRow(`SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
            WHERE n.nspname IN ('ingest','bronze','silver','gold','config')`).Scan(&relations); err != nil {
			t.Fatal(err)
		}
		if relations != expected {
			t.Fatalf("version %d contains %d relations, want inventory count %d", version, relations, expected)
		}
		assertWriterTableBoundaries(t, f)
	}
	for _, schema := range f.chain.schemas {
		tx, err := f.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec("CREATE TABLE " + schema + ".immutable_fixture (id bigint PRIMARY KEY); CREATE TRIGGER immutable_fixture BEFORE UPDATE OR DELETE ON " + schema + ".immutable_fixture FOR EACH ROW EXECUTE FUNCTION " + schema + ".reject_immutable_change(); INSERT INTO " + schema + ".immutable_fixture VALUES (1)"); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		_, mutationErr := tx.Exec("UPDATE " + schema + ".immutable_fixture SET id=2")
		assertImmutableError(t, mutationErr)
		_ = tx.Rollback()
		tx, err = f.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec("CREATE TABLE " + schema + ".immutable_fixture (id bigint PRIMARY KEY); CREATE TRIGGER immutable_fixture BEFORE UPDATE OR DELETE ON " + schema + ".immutable_fixture FOR EACH ROW EXECUTE FUNCTION " + schema + ".reject_immutable_change(); INSERT INTO " + schema + ".immutable_fixture VALUES (1)"); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		_, mutationErr = tx.Exec("DELETE FROM " + schema + ".immutable_fixture")
		assertImmutableError(t, mutationErr)
		_ = tx.Rollback()
	}
	assertInventory(t, f, finalVersion(f.chain))
}

func assertWriterTableBoundaries(t *testing.T, f *chainFixture) {
	t.Helper()
	rows, err := f.db.Query(`WITH tables AS (
        SELECT c.oid, n.nspname, c.relname FROM pg_class c
        JOIN pg_namespace n ON n.oid=c.relnamespace
        WHERE n.nspname IN ('ingest','bronze','silver','gold','config') AND c.relkind IN ('r','p')
    ), required_paths(table_name, columns, predicate) AS (VALUES
        ('execution_lease', ARRAY['expires_at','id'], NULL::text),
        ('execution_lease', ARRAY['next_eligible_at','id'], NULL::text),
        ('execution_result', ARRAY['execution_id','stage','substep','id'], NULL::text),
        ('revision_acceptance', ARRAY['state','watermark','id'], NULL::text),
        ('revision_acceptance', ARRAY['revision_key','processing_attempt_key','id'], NULL::text),
        ('revision_acceptance_state', ARRAY['state','watermark','id'], NULL::text),
        ('revision_acceptance_state', ARRAY['revision_key','processing_attempt_key'], NULL::text),
        ('delivery_record', ARRAY['due_at','id'], '(acknowledged_at IS NULL)'),
        ('batch_request', ARRAY['state','next_eligible_at','id'], NULL::text),
        ('batch_execution', ARRAY['state','next_poll_at','id'], NULL::text),
        ('batch_item', ARRAY['batch_execution_id','item_key'], NULL::text),
        ('batch_waiter', ARRAY['batch_request_id','workflow_id'], NULL::text),
        ('batch_waiter', ARRAY['due_at','id'], '(delivered_at IS NULL)'),
        ('quarantine_decision', ARRAY['document_version_key','id'], NULL::text),
        ('evidence_gap', ARRAY['scope_kind','scope_key','id'], NULL::text)
    )
    SELECT t.nspname || '.' || t.relname || ': missing single-column primary key'
    FROM tables t WHERE NOT EXISTS (
        SELECT 1 FROM pg_constraint k WHERE k.conrelid=t.oid AND k.contype='p' AND cardinality(k.conkey)=1)
    UNION ALL
    SELECT t.nspname || '.' || t.relname || '.' || k.conname || ': cross-schema foreign key'
    FROM tables t JOIN pg_constraint k ON k.conrelid=t.oid AND k.contype='f'
    JOIN pg_class parent ON parent.oid=k.confrelid JOIN pg_namespace pn ON pn.oid=parent.relnamespace
    WHERE pn.nspname <> t.nspname
    UNION ALL
    SELECT t.nspname || '.' || t.relname || '.' || a.attname || ': country discriminator'
    FROM tables t JOIN pg_attribute a ON a.attrelid=t.oid
    WHERE a.attnum > 0 AND NOT a.attisdropped AND (a.attname='country' OR a.attname LIKE 'country\_%' ESCAPE '\')
      AND NOT (t.nspname='ingest' AND t.relname='country_admission' AND a.attname='country_code')
    UNION ALL
    SELECT t.nspname || '.' || t.relname || '.' || k.conname || ': missing parent-leading index'
    FROM tables t JOIN pg_constraint k ON k.conrelid=t.oid AND k.contype='f'
    WHERE NOT EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid=t.oid
        AND i.indisvalid AND i.indisready AND i.indpred IS NULL AND i.indnkeyatts >= cardinality(k.conkey)
        AND NOT EXISTS (SELECT 1 FROM unnest(k.conkey) WITH ORDINALITY AS fkcol(attnum, ordinal)
            WHERE i.indkey[(fkcol.ordinal-1)::integer] <> fkcol.attnum))
    UNION ALL
    SELECT t.nspname || '.' || t.relname || ': missing required index on (' || array_to_string(r.columns, ',') || ')'
    FROM tables t JOIN required_paths r ON t.nspname='ingest' AND t.relname=r.table_name
    WHERE NOT EXISTS (SELECT 1 FROM pg_index i JOIN pg_class idx ON idx.oid=i.indexrelid
        JOIN pg_am am ON am.oid=idx.relam
        WHERE i.indrelid=t.oid AND i.indisvalid AND i.indisready AND am.amname='btree'
          AND pg_get_expr(i.indpred, i.indrelid) IS NOT DISTINCT FROM r.predicate
          AND ARRAY(SELECT a.attname::text FROM unnest(i.indkey) WITH ORDINALITY AS keycol(attnum, ordinal)
              JOIN pg_attribute a ON a.attrelid=t.oid AND a.attnum=keycol.attnum
              WHERE keycol.ordinal <= cardinality(r.columns) AND keycol.ordinal <= i.indnkeyatts
              ORDER BY keycol.ordinal) = r.columns)
    UNION ALL
    SELECT n.nspname || '.' || c.relname || ': invalid or unready index'
    FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname IN ('ingest','bronze','silver','gold','config') AND (NOT i.indisvalid OR NOT i.indisready)`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var violation string
		if err := rows.Scan(&violation); err != nil {
			t.Fatal(err)
		}
		t.Error(violation)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func assertImmutableError(t *testing.T, err error) {
	t.Helper()
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "55000" {
		t.Fatalf("mutation must fail with PostgreSQL state 55000: %v", err)
	}
}

// The container identifier is supplied only by the disposable generation job.
func postgresTool(t *testing.T, input []byte, name string, args ...string) []byte {
	t.Helper()
	container := os.Getenv("BANHMI_CORPUS_TEST_POSTGRES_CONTAINER")
	if container == "" {
		t.Fatal("disposable PostgreSQL container is required for restore fixtures")
	}
	command := append([]string{"exec", "-i", "--user", "postgres", container, name}, args...)
	cmd := exec.Command("docker", command...)
	cmd.Stdin = bytes.NewReader(input)
	var output, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("fixture PostgreSQL %s: %v\n%s", name, err, stderr.String())
	}
	return output.Bytes()
}
