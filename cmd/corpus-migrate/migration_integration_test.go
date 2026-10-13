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
	execSQL(t, admin, "CREATE DATABASE " + database)
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP DATABASE " + database + " WITH (FORCE)"); err != nil {
			t.Errorf("drop disposable fixture: %v", err)
		}
	})
	u.Path = "/" + database
	adminURL.Path = u.Path
	localAdmin := openTestDB(t, adminURL.String())
	if name != "control" {
		execSQL(t, localAdmin, "CREATE EXTENSION vector WITH SCHEMA public VERSION '0.8.2'; CREATE EXTENSION btree_gist WITH SCHEMA public VERSION '1.7'")
	}
	args := []string{"--no-psqlrc", "--dbname", adminURL.String(), "--set", "ON_ERROR_STOP=1", "--set", "migration_login_role=banhmi_corpus_"+name+"_migrator", "--set", "runtime_login_role=banhmi_corpus_"+name+"_app"}
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
		t.Cleanup(func() { execSQL(t, localAdmin, "REVOKE banhmi_corpus_release_migrator FROM banhmi_corpus_release_restore; REVOKE CONNECT ON DATABASE "+database+" FROM banhmi_corpus_release_restore") })
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

func TestWriterChainReplay(t *testing.T) {
	testReplay(t, "writer")
}
func TestReleaseChainReplay(t *testing.T) {
	testReplay(t, "release")
}
func TestControlChainReplay(t *testing.T) {
	testReplay(t, "control")
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
	if err := run(context.Background(), []string{"-chain", name, "-to", fmt.Sprint(f.chain.versions[len(f.chain.versions)-1])}, io.Discard); err != nil {
		t.Fatal(err)
	}
	before := catalogSnapshot(t, f)
	applyFixture(t, f, options{})
	if got := catalogSnapshot(t, f); got != before {
		t.Fatal("default no-op replay changed catalog or history")
	}
	for _, args := range [][]string{
		{"-chain", name, "-to", "0"}, {"-chain", name, "-to", "-1"},
		{"-chain", name, "-to", "99999"}, {"-chain", name, "-status", "-to", "1"},
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
	opt := options{hasTo: true, to: 1}
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
	assertInventory(t, f, int64(len(f.chain.versions)))
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
		assertInventory(t, restored, int64(len(f.chain.versions)))
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
		assertInventory(t, empty, int64(len(empty.chain.versions)))
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
        WHERE oid >= 16384 OR nspname='public'`).Scan(&schemas); err != nil { t.Fatal(err) }
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
		if err := f.admin.QueryRow("SELECT COALESCE(json_agg(v ORDER BY id)::text,'[]') FROM public."+f.chain.table+" v").Scan(&history); err != nil {
			t.Fatal(err)
		}
		out.WriteString(history)
	}
	return out.String()
}

func TestWriterSchemaBoundaries(t *testing.T) {
	f := newFixture(t, "writer")
	applyFixture(t, f, options{})
	assertInventory(t, f, 1)
	var relations int
	if err := f.db.QueryRow(`SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
        WHERE n.nspname IN ('ingest','bronze','silver','gold','config')`).Scan(&relations); err != nil { t.Fatal(err) }
	if relations != 0 {
		t.Fatal("version one must contain no tables, sequences, indexes, or views")
	}
	// Version one can prove only function rejection semantics and empty-schema
	// boundaries. Admission, typed references, indexes and PKs arrive with tables.
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
	assertInventory(t, f, 1)
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
