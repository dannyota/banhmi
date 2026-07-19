// Package fitz wraps go-fitz (MuPDF via CGO) for document text extraction.
// It handles PDF, DOCX, and EPUB input. HTML is not supported as input by MuPDF;
// use extract.HTML() for inline HTML bodies.
//
// Legacy DOC files are converted to DOCX via LibreOffice (soffice --headless)
// before extraction.
//
// MuPDF has global shared state (FreeType, glyph cache) protected by
// client-provided locks. go-fitz passes nil (no locks), so concurrent
// fz_context calls are NOT safe. All MuPDF calls serialize on mu.
package fitz

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	gofitz "github.com/gen2brain/go-fitz"
)

const libreOfficeTimeout = 120 * time.Second

var mu sync.Mutex

// ExtractText opens a file (PDF, DOCX, EPUB) with MuPDF and returns the
// concatenated text of all pages. Validates magic bytes before opening to
// avoid MuPDF crashes on non-document files.
func ExtractText(path string) (string, error) {
	if err := validateMagic(path); err != nil {
		return "", fmt.Errorf("fitz %s: %w", filepath.Base(path), err)
	}

	mu.Lock()
	defer mu.Unlock()

	doc, err := gofitz.New(path)
	if err != nil {
		return "", fmt.Errorf("fitz open %s: %w", filepath.Base(path), err)
	}
	defer func() { _ = doc.Close() }()

	var b strings.Builder
	for i := range doc.NumPage() {
		text, err := doc.Text(i)
		if err != nil {
			return "", fmt.Errorf("fitz page %d: %w", i, err)
		}
		if b.Len() > 0 && text != "" {
			b.WriteByte('\n')
		}
		b.WriteString(text)
	}
	return b.String(), nil
}

// ScanStats probes up to maxProbe pages and reports how many carry a raster
// image. A scanned document (e.g. a gazette scan with an embedded OCR text
// layer) renders every page as one full-page image, so imagePages ≈ probed;
// born-digital PDFs have images only on logo/letterhead pages. Callers use the
// ratio to distrust an embedded OCR layer even when text extraction "works" —
// such layers carry misrecognitions that no text-level gate reliably catches
// in diacritic-poor languages.
func ScanStats(path string, maxProbe int) (probed, imagePages int, err error) {
	if err := validateMagic(path); err != nil {
		return 0, 0, fmt.Errorf("fitz %s: %w", filepath.Base(path), err)
	}

	mu.Lock()
	defer mu.Unlock()

	doc, err := gofitz.New(path)
	if err != nil {
		return 0, 0, fmt.Errorf("fitz open %s: %w", filepath.Base(path), err)
	}
	defer func() { _ = doc.Close() }()

	n := doc.NumPage()
	if maxProbe <= 0 || maxProbe > n {
		maxProbe = n
	}
	for i := 0; i < maxProbe; i++ {
		h, err := doc.HTML(i, false)
		if err != nil {
			continue
		}
		probed++
		if strings.Contains(h, "<img") {
			imagePages++
		}
	}
	return probed, imagePages, nil
}

// ExtractTextFromBytes writes data to a temp file with the given extension,
// extracts text via MuPDF, and cleans up the temp file.
func ExtractTextFromBytes(data []byte, ext string) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("fitz: empty input")
	}
	ext = strings.TrimSpace(ext)
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	tmp, err := os.CreateTemp("", "banhmi-fitz-*"+ext)
	if err != nil {
		return "", fmt.Errorf("create fitz temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write fitz temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close fitz temp file: %w", err)
	}
	return ExtractText(tmpPath)
}

// ConvertDOCToDocx converts a legacy .doc file to .docx using LibreOffice
// (soffice --headless --convert-to docx). Returns the path to the .docx output.
// The caller is responsible for cleaning up the output file.
func ConvertDOCToDocx(docPath, outDir string) (string, error) {
	cmd := exec.Command(
		"soffice",
		"--headless",
		"--nologo",
		"--nodefault",
		"--nofirststartwizard",
		"--nolockcheck",
		"--convert-to", "docx",
		"--outdir", outDir,
		docPath,
	)
	cmd.Stdout = nil
	cmd.Stderr = nil

	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()

	select {
	case err := <-done:
		if err != nil {
			return "", fmt.Errorf("soffice convert doc to docx: %w", err)
		}
	case <-time.After(libreOfficeTimeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return "", fmt.Errorf("soffice convert doc to docx: timed out after %s", libreOfficeTimeout)
	}

	base := strings.TrimSuffix(filepath.Base(docPath), filepath.Ext(docPath)) + ".docx"
	outPath := filepath.Join(outDir, base)
	if _, err := os.Stat(outPath); err != nil {
		return "", fmt.Errorf("soffice produced no output: %w", err)
	}
	return outPath, nil
}

// validateMagic checks file header bytes to ensure it's a format MuPDF can handle.
func validateMagic(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var header [8]byte
	n, err := f.Read(header[:])
	if err != nil || n < 4 {
		return fmt.Errorf("cannot read header (%d bytes): %w", n, err)
	}

	switch {
	case string(header[:5]) == "%PDF-":
		return nil
	case header[0] == 'P' && header[1] == 'K' && header[2] == 0x03 && header[3] == 0x04:
		return nil // ZIP (DOCX/EPUB)
	case header[0] == 0xD0 && header[1] == 0xCF && header[2] == 0x11 && header[3] == 0xE0:
		return nil // OLE2 (legacy DOC)
	default:
		return fmt.Errorf("not a PDF/DOCX/EPUB (magic: %x)", header[:n])
	}
}
