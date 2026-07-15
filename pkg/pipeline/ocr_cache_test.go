package pipeline

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestOCRCacheLocalRoundtrip(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	c, err := BuildOCRCache(ctx, "", dir, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("BuildOCRCache: %v", err)
	}

	const sha = "ab12cd34"
	if _, ok, err := c.Get(ctx, sha); err != nil || ok {
		t.Fatalf("Get on empty cache = ok=%v err=%v, want miss", ok, err)
	}

	const text = "Điều 1. Phạm vi điều chỉnh"
	if err := c.Put(ctx, sha, text); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok, err := c.Get(ctx, sha)
	if err != nil || !ok || got != text {
		t.Fatalf("Get = %q ok=%v err=%v, want %q", got, ok, err, text)
	}

	// The primary copy must be a plain local file, backup-friendly.
	data, err := os.ReadFile(filepath.Join(dir, "ocr", sha+".txt"))
	if err != nil {
		t.Fatalf("local file: %v", err)
	}
	if string(data) != text {
		t.Fatalf("local file content = %q, want %q", data, text)
	}
}

func TestBuildOCRCacheDisabled(t *testing.T) {
	c, err := BuildOCRCache(context.Background(), "", "", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("BuildOCRCache: %v", err)
	}
	if c != nil {
		t.Fatalf("expected nil cache when no storage dir and no bucket")
	}
}
