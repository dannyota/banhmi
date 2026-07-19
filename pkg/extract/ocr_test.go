package extract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExecutableFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("write executable file: %v", err)
	}
}

func writeTestPDF(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(p, []byte("%PDF-1.7"), 0o600); err != nil {
		t.Fatalf("write test pdf: %v", err)
	}
	return p
}

func TestOCRClient_OCRSuccess(t *testing.T) {
	dir := t.TempDir()
	pdfPath := writeTestPDF(t, dir)

	writeExecutableFile(t, filepath.Join(dir, "ocrmypdf"), `#!/usr/bin/env python3
import pathlib, sys

args = sys.argv[1:]
if "--sidecar" not in args:
    raise SystemExit("missing --sidecar")
if "--language" not in args or "vie+eng" not in args:
    raise SystemExit("missing language")

text_file = args[args.index("--sidecar") + 1]
pathlib.Path(text_file).write_text("Điều 1.\n\nNội dung", encoding="utf-8")
`)

	client := NewOCRClient(filepath.Join(dir, "tesseract"), filepath.Join(dir, "ocrmypdf"), 260, "vie+eng")
	res, err := client.OCR(context.Background(), pdfPath)
	if err != nil {
		t.Fatalf("OCR: %v", err)
	}
	if !res.IsComplete() {
		t.Fatal("expected complete OCR response")
	}
	if res.Engine != "ocrmypdf/ocrmypdf" {
		t.Fatalf("engine=%q, want ocrmypdf/ocrmypdf", res.Engine)
	}
	if len(res.Pages) != 1 {
		t.Fatalf("pages=%d, want 1", len(res.Pages))
	}
	if res.Pages[0].DPI != 260 {
		t.Fatalf("dpi=%d, want 260", res.Pages[0].DPI)
	}
	if !strings.Contains(res.Text, "Điều 1.") || !strings.Contains(res.Text, "Nội dung") {
		t.Fatalf("text=%q, want OCR output", res.Text)
	}
}

func TestOCRClient_OCRFailure(t *testing.T) {
	dir := t.TempDir()
	pdfPath := writeTestPDF(t, dir)

	writeExecutableFile(t, filepath.Join(dir, "ocrmypdf"), `#!/usr/bin/env python3
import pathlib, sys

raise SystemExit(2)
`)
	client := NewOCRClient(filepath.Join(dir, "tesseract"), filepath.Join(dir, "ocrmypdf"), 300, "vie+eng")
	if _, err := client.OCR(context.Background(), pdfPath); err == nil {
		t.Fatal("expected OCR failure")
	}
}

func TestOCRClient_Defaults(t *testing.T) {
	client := NewOCRClient("", "", 0, "")
	if client.tesseract != "tesseract" {
		t.Fatalf("tesseract=%q, want default", client.tesseract)
	}
	if client.ocrmypdf != "ocrmypdf" {
		t.Fatalf("ocrmypdf=%q, want default", client.ocrmypdf)
	}
	if client.dpi != 300 {
		t.Fatalf("dpi=%d, want 300", client.dpi)
	}
	if client.language != "vie+eng" {
		t.Fatalf("language=%q, want vie+eng", client.language)
	}
}
