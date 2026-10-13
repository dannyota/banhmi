package authmigrations

import (
	"io/fs"
	"math"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestEmbeddedAuthMigrationDiscovery(t *testing.T) {
	goose.SetBaseFS(FS)
	t.Cleanup(func() { goose.SetBaseFS(nil) })
	migrations, err := goose.CollectMigrations(".", 0, math.MaxInt64)
	if err != nil {
		t.Fatalf("collect embedded authentication migrations: %v", err)
	}
	if len(migrations) != 1 || migrations[0].Version != 20261011000000 {
		t.Fatalf("authentication migrations = %v, want only the initial auth migration", migrations)
	}
	data, err := fs.ReadFile(FS, migrations[0].Source)
	if err != nil {
		t.Fatalf("read discovered authentication SQL: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("discovered authentication SQL is empty")
	}
}
