package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func bootstrapArgs(f *chainFixture, runtime string, restoring bool) []string {
	name := f.chain.name
	args := []string{"--no-psqlrc", "--dbname", f.adminURL, "--set", "ON_ERROR_STOP=1",
		"--set", "migration_login_role=banhmi_corpus_" + name + "_migrator",
		"--set", "runtime_login_role=" + runtime}
	if name == "writer" {
		args = append(args, "--set", "export_login_role=banhmi_corpus_writer_exporter")
	}
	if name == "release" {
		binding := "loader_login_role=banhmi_corpus_release_load"
		if restoring {
			binding = "restore_login_role=banhmi_corpus_release_restore"
		}
		args = append(args, "--set", binding)
	}
	return append(args, "--file", filepath.Join("..", "..", "deploy", "corpusmigrations", "bootstrap", name+".sql"))
}

func bootstrapAccessSnapshot(t *testing.T, f *chainFixture) string {
	t.Helper()
	var snapshot string
	if err := f.admin.QueryRow(`SELECT json_build_array(
        (SELECT datacl::text FROM pg_database WHERE datname=current_database()),
        (SELECT nspacl::text FROM pg_namespace WHERE nspname='public'),
        (SELECT COALESCE(json_agg(m ORDER BY roleid,member)::text,'[]') FROM pg_auth_members m))::text`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestBootstrapConnectBoundary(t *testing.T) {
	for _, mode := range []struct {
		name      string
		restoring bool
	}{{"writer", false}, {"control", false}, {"release", false}, {"release", true}} {
		t.Run(fmt.Sprintf("%s-restore-%t", mode.name, mode.restoring), func(t *testing.T) {
			f := newFixtureMode(t, mode.name, mode.restoring)
			runtime := "banhmi_corpus_" + mode.name + "_app"
			args := bootstrapArgs(f, runtime, mode.restoring)
			before := bootstrapAccessSnapshot(t, f)
			tool(t, "psql", args...)
			if got := bootstrapAccessSnapshot(t, f); got != before {
				t.Fatal("identical bootstrap changed access or membership")
			}
			// Only the explicitly bound release construction login may connect.
			if mode.name == "release" {
				var loader, restore bool
				if err := f.admin.QueryRow(`SELECT
                    has_database_privilege('banhmi_corpus_release_load',current_database(),'CONNECT'),
                    has_database_privilege('banhmi_corpus_release_restore',current_database(),'CONNECT')`).Scan(&loader, &restore); err != nil {
					t.Fatal(err)
				}
				if loader != !mode.restoring || restore != mode.restoring {
					t.Fatal("release CONNECT does not match the selected loader or restore mode")
				}
			}
			unexpected := fmt.Sprintf("banhmi_bootstrap_unexpected_%d", fixtureSequence.Add(1))
			execSQL(t, f.admin, "CREATE ROLE "+unexpected+" LOGIN")
			t.Cleanup(func() {
				execSQL(t, f.admin, "REVOKE ALL ON DATABASE "+f.database+" FROM "+unexpected+"; DROP ROLE "+unexpected)
			})
			assertRejected := func(args []string) {
				t.Helper()
				before := bootstrapAccessSnapshot(t, f)
				output, err := exec.Command("psql", args...).CombinedOutput()
				if err == nil || !strings.Contains(string(output), "unexpected CONNECT for login "+unexpected) {
					t.Fatalf("bootstrap must name the unexpected login: %v\n%s", err, output)
				}
				if got := bootstrapAccessSnapshot(t, f); got != before {
					t.Fatal("rejected bootstrap changed access or membership")
				}
			}
			execSQL(t, f.admin, "GRANT CONNECT ON DATABASE "+f.database+" TO "+unexpected)
			assertRejected(args)
			execSQL(t, f.admin, "REVOKE CONNECT ON DATABASE "+f.database+" FROM "+unexpected)
			// Replacing runtime must fail on the old login and roll back the new grant.
			before = bootstrapAccessSnapshot(t, f)
			output, err := exec.Command("psql", bootstrapArgs(f, unexpected, mode.restoring)...).CombinedOutput()
			if err == nil || !strings.Contains(string(output), "unexpected CONNECT for login "+runtime) {
				t.Fatalf("replacement must name the stale runtime: %v\n%s", err, output)
			}
			if got := bootstrapAccessSnapshot(t, f); got != before {
				t.Fatal("rejected replacement changed access or membership")
			}
		})
	}
}
