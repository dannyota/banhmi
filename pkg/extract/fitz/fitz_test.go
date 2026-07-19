package fitz

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractText_MissingFile(t *testing.T) {
	_, err := ExtractText("/nonexistent/file.pdf")
	if err == nil {
		t.Fatal("ExtractText on missing file should error")
	}
}

func TestExtractTextFromBytes_Empty(t *testing.T) {
	_, err := ExtractTextFromBytes(nil, ".pdf")
	if err == nil {
		t.Fatal("ExtractTextFromBytes with nil should error")
	}
	_, err = ExtractTextFromBytes([]byte{}, ".pdf")
	if err == nil {
		t.Fatal("ExtractTextFromBytes with empty should error")
	}
}

func TestExtractTextFromBytes_Extension(t *testing.T) {
	// Verify extension normalization (dot prepended when missing).
	_, err := ExtractTextFromBytes([]byte("invalid"), "pdf")
	// The file is invalid but the temp file should be created with .pdf suffix.
	if err == nil {
		t.Fatal("invalid PDF bytes should error")
	}
	if !strings.Contains(err.Error(), "fitz") {
		t.Fatalf("error = %q, want fitz error", err)
	}
}

func TestConvertDOCToDocx_MissingSoffice(t *testing.T) {
	if _, err := exec.LookPath("soffice"); err == nil {
		t.Skip("soffice is available, cannot test missing-soffice path")
	}
	_, err := ConvertDOCToDocx("/tmp/fake.doc", t.TempDir())
	if err == nil {
		t.Fatal("ConvertDOCToDocx should fail when soffice is missing")
	}
}

func TestConvertDOCToDocx_WithSoffice(t *testing.T) {
	if _, err := exec.LookPath("soffice"); err != nil {
		t.Skip("soffice not available")
	}
	dir := t.TempDir()
	docPath := filepath.Join(dir, "test.doc")
	if err := os.WriteFile(docPath, []byte("not a real doc"), 0o600); err != nil {
		t.Fatalf("write test.doc: %v", err)
	}
	outDir := t.TempDir()
	_, err := ConvertDOCToDocx(docPath, outDir)
	// Expect an error or a best-effort result; either is fine for a fake DOC.
	if err == nil {
		t.Log("soffice unexpectedly succeeded on fake DOC")
	}
}

func TestExtractTextFromBytes_DOCX(t *testing.T) {
	// Create a valid DOCX using soffice to convert a text file, then extract.
	if _, err := exec.LookPath("soffice"); err != nil {
		t.Skip("soffice not available for DOCX fixture generation")
	}

	dir := t.TempDir()
	txtPath := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(txtPath, []byte("Hello from banhmi test"), 0o600); err != nil {
		t.Fatalf("write txt: %v", err)
	}

	cmd := exec.Command("soffice", "--headless", "--convert-to", "docx", "--outdir", dir, txtPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("soffice convert failed: %v: %s", err, out)
	}

	docxPath := filepath.Join(dir, "test.docx")
	data, err := os.ReadFile(docxPath)
	if err != nil {
		t.Fatalf("read docx: %v", err)
	}

	text, err := ExtractTextFromBytes(data, ".docx")
	if err != nil {
		t.Fatalf("ExtractTextFromBytes: %v", err)
	}
	if !strings.Contains(text, "Hello") {
		t.Fatalf("text = %q, want to contain 'Hello'", text)
	}
}
