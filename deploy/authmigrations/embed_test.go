package authmigrations

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"testing"
	"testing/fstest"
)

func TestVerifyEmbeddedMigrations(t *testing.T) {
	if err := Verify(); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
}

func TestVerifyRejectsTamperedMigration(t *testing.T) {
	const name = "20261011000000_init.sql"
	original := []byte("-- +goose Up\nSELECT 1;\n")
	filesystem := fstest.MapFS{
		name:        {Data: []byte("-- +goose Up\nSELECT 2;\n")},
		"atlas.sum": {Data: []byte(testAtlasSum(name, original))},
	}
	if err := verifyFS(filesystem); err == nil {
		t.Fatal("verifyFS() error = nil, want checksum mismatch")
	}
}

func TestVerifyRejectsUnlistedMigration(t *testing.T) {
	const name = "20261011000000_init.sql"
	migration := []byte("-- +goose Up\nSELECT 1;\n")
	filesystem := fstest.MapFS{
		name:                              {Data: migration},
		"20261012000000_unlisted.sql":     {Data: migration},
		"atlas.sum":                       {Data: []byte(testAtlasSum(name, migration))},
	}
	if err := verifyFS(filesystem); err == nil {
		t.Fatal("verifyFS() error = nil, want unlisted-migration error")
	}
}

func testAtlasSum(name string, migration []byte) string {
	fileHash := sha256.New()
	_, _ = fileHash.Write([]byte(name))
	_, _ = fileHash.Write(migration)
	encodedFileHash := base64.StdEncoding.EncodeToString(fileHash.Sum(nil))

	directoryHash := sha256.New()
	_, _ = directoryHash.Write([]byte(name))
	_, _ = directoryHash.Write([]byte(encodedFileHash))
	encodedDirectoryHash := base64.StdEncoding.EncodeToString(directoryHash.Sum(nil))
	return fmt.Sprintf("h1:%s\n%s h1:%s\n", encodedDirectoryHash, name, encodedFileHash)
}

