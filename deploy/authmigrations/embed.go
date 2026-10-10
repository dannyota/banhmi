// Package authmigrations embeds only the dedicated authentication migrations.
package authmigrations

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"fmt"
	"io/fs"
	"strings"
)

// FS contains the append-only Goose migration and Atlas checksum files.
// The initial SQL and checksum are generated on GitHub from sql/auth/schema.sql.
//
//go:embed *.sql atlas.sum
var FS embed.FS

// Verify checks every migration against atlas.sum before database access.
func Verify() error {
	return verifyFS(FS)
}

func verifyFS(source fs.FS) error {
	sumBytes, err := fs.ReadFile(source, "atlas.sum")
	if err != nil {
		return fmt.Errorf("read atlas.sum: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(sumBytes)), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "h1:") {
		return fmt.Errorf("atlas.sum is invalid")
	}

	type entry struct {
		name string
		hash string
	}
	entries := make([]entry, 0, len(lines)-1)
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[1], "h1:") {
			return fmt.Errorf("atlas.sum entry is invalid")
		}
		entries = append(entries, entry{name: fields[0], hash: fields[1]})
	}

	migrationFiles, err := fs.Glob(source, "*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	if len(migrationFiles) != len(entries) {
		return fmt.Errorf("atlas.sum lists %d migrations, found %d", len(entries), len(migrationFiles))
	}
	listedMigrations := make(map[string]bool, len(entries))
	for _, item := range entries {
		if listedMigrations[item.name] {
			return fmt.Errorf("atlas.sum lists migration %s more than once", item.name)
		}
		listedMigrations[item.name] = true
	}
	for _, name := range migrationFiles {
		if !listedMigrations[name] {
			return fmt.Errorf("migration %s is not listed in atlas.sum", name)
		}
	}

	fileHash := sha256.New()
	for _, item := range entries {
		migration, err := fs.ReadFile(source, item.name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", item.name, err)
		}
		_, _ = fileHash.Write([]byte(item.name))
		_, _ = fileHash.Write(migration)
		actual := "h1:" + base64.StdEncoding.EncodeToString(fileHash.Sum(nil))
		if actual != item.hash {
			return fmt.Errorf("migration checksum mismatch for %s", item.name)
		}
	}

	directoryHash := sha256.New()
	for _, item := range entries {
		_, _ = directoryHash.Write([]byte(item.name))
		_, _ = directoryHash.Write([]byte(strings.TrimPrefix(item.hash, "h1:")))
	}
	actualDirectoryHash := "h1:" + base64.StdEncoding.EncodeToString(directoryHash.Sum(nil))
	if actualDirectoryHash != strings.TrimSpace(lines[0]) {
		return fmt.Errorf("migration directory checksum mismatch")
	}
	return nil
}
