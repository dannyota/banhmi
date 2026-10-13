package main

import (
	"io/fs"
	"math"
	"testing"

	"github.com/pressly/goose/v3"

	"danny.vn/banhmi/deploy/migrations"
)

func TestEmbeddedMigrationDiscovery(t *testing.T) {
	t.Cleanup(func() { goose.SetBaseFS(nil) })
	pendingVersions := map[string]int64{
		"extensions": 1,
		"bronze":     2,
		"silver":     9,
		"gold":       3,
		"ingest":     6,
		"config":     8,
	}
	for _, entry := range dirOrder {
		t.Run(entry.dir, func(t *testing.T) {
			sub, err := fs.Sub(migrations.FS, entry.dir)
			if err != nil {
				t.Fatalf("open embedded layer: %v", err)
			}
			goose.SetBaseFS(sub)
			chain, err := goose.CollectMigrations(".", 0, math.MaxInt64)
			if err != nil {
				t.Fatalf("collect embedded migrations: %v", err)
			}
			if len(chain) == 0 || chain[len(chain)-1].Version != pendingVersions[entry.dir] {
				t.Fatalf("embedded chain = %v, missing reviewed pending version", chain)
			}
			for _, migration := range chain {
				data, err := fs.ReadFile(sub, migration.Source)
				if err != nil {
					t.Fatalf("read discovered SQL: %v", err)
				}
				if len(data) == 0 {
					t.Fatalf("discovered SQL %s is empty", migration.Source)
				}
			}
		})
	}
}
