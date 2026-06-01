package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

func TestRedactURLQuery(t *testing.T) {
	err := errors.New(`download 0.docx: Get "https://example.invalid/object.docx?X-Amz-Credential=secret&X-Amz-Signature=sig": status 403`)
	got := redactURLQuery(err)

	if got == err.Error() {
		t.Fatalf("redactURLQuery did not change signed URL: %q", got)
	}
	if containsAny(got, "secret", "sig", "X-Amz-Credential", "X-Amz-Signature") {
		t.Fatalf("redactURLQuery leaked signed query data: %q", got)
	}
	if !containsAny(got, "https://example.invalid/object.docx?<redacted>") {
		t.Fatalf("redactURLQuery = %q, want redacted URL", got)
	}
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if n != "" && strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func TestStoreFileCreatesOCRReadableFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod temp dir: %v", err)
	}
	a := &Activities{storageDir: dir}
	src := fakeDownloadSource{data: []byte("%PDF test")}

	name, sha, n, err := a.storeFile(context.Background(), src, ingest.FileRef{Ext: "pdf"})
	if err != nil {
		t.Fatalf("storeFile: %v", err)
	}
	if n != int64(len(src.data)) {
		t.Fatalf("size = %d, want %d", n, len(src.data))
	}
	if name != sha+".pdf" {
		t.Fatalf("name = %q, want hash filename", name)
	}
	info, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("stat stored file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("mode = %o, want 0644 for OCR-readability", got)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat storage dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o755 {
		t.Fatalf("dir mode = %o, want 0755 for OCR-readable storage", got)
	}
}

func TestUsableHTMLPayloadRejectsEmptyVBPLShell(t *testing.T) {
	body := `<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
</head>
<body>
</body>
</html>`

	if usableHTMLPayload(body) {
		t.Fatal("empty VBPL HTML shell must not be treated as usable content")
	}
}

func TestUsableHTMLPayloadAcceptsLegalText(t *testing.T) {
	body := `<html><body><p>Điều 1. Phạm vi điều chỉnh</p></body></html>`

	if !usableHTMLPayload(body) {
		t.Fatal("HTML with body text must be usable")
	}
}

func TestNormalizeDocNumberForStorage(t *testing.T) {
	cases := map[string]string{
		"2345/QĐ-NHNN":      "2345QDNHNN",
		" 27 / 2022 / TT ":  "272022TT",
		"43/VBHN-NHNN":      "43VBHNNHNN",
		"14/2022/NĐ-CP,":    "142022NDCP",
		"01/2024/QĐ-TTg":    "012024QDTTG",
		"01/2024/QD-TTg":    "012024QDTTG",
		"01/2024/QĐ–NHNN":   "012024QDNHNN",
		"01/2024/QĐ NHNN":   "012024QDNHNN",
		"01/2024/QĐ.\tNHNN": "012024QDNHNN",
		"01/2024/QĐ\nNHNN":  "012024QDNHNN",
	}
	for in, want := range cases {
		if got := normalizeDocNumberForStorage(in); got != want {
			t.Fatalf("normalizeDocNumberForStorage(%q) = %q, want %q", in, got, want)
		}
	}
}

type fakeDownloadSource struct {
	data []byte
}

func (s fakeDownloadSource) ID() string { return "fake" }

func (s fakeDownloadSource) Discover(context.Context, time.Time, string) ([]ingest.DiscoveredDoc, error) {
	panic("unused")
}

func (s fakeDownloadSource) FetchDetail(context.Context, ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	panic("unused")
}

func (s fakeDownloadSource) Download(_ context.Context, _ ingest.FileRef, w io.Writer) (int64, string, error) {
	sum := sha256.Sum256(s.data)
	n, err := w.Write(s.data)
	return int64(n), hex.EncodeToString(sum[:]), err
}
