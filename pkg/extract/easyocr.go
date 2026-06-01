package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultEasyOCRCommand   = "python3"
	defaultEasyOCRLanguages = "vi"
	defaultEasyOCRDPI       = 300
	defaultEasyOCRBatchSize = 32
	defaultEasyOCRTimeout   = 15 * time.Minute
)

// EasyOCRClient runs the local EasyOCR Python tool (tools/easyocr_ocr.py) on a PDF
// and returns extracted text + confidence. It runs on the local CPU; the Kaggle
// batch engine runs the same recipe on a GPU. Classic detect+recognize: it
// transcribes every region and never hallucinates.
type EasyOCRClient struct {
	command   string
	script    string
	languages string
	dpi       int
	batchSize int
	gpu       bool
	timeout   time.Duration
}

// NewEasyOCRClient builds the client. An empty command defaults to python3; an
// empty script resolves the compiled-in default locations (env, container, repo).
func NewEasyOCRClient(command, script, languages string, dpi, batchSize int, gpu bool) *EasyOCRClient {
	command = strings.TrimSpace(command)
	if command == "" {
		command = defaultEasyOCRCommand
	}
	languages = strings.TrimSpace(languages)
	if languages == "" {
		languages = defaultEasyOCRLanguages
	}
	if dpi <= 0 {
		dpi = defaultEasyOCRDPI
	}
	if batchSize <= 0 {
		batchSize = defaultEasyOCRBatchSize
	}
	return &EasyOCRClient{
		command:   command,
		script:    strings.TrimSpace(script),
		languages: languages,
		dpi:       dpi,
		batchSize: batchSize,
		gpu:       gpu,
		timeout:   defaultEasyOCRTimeout,
	}
}

// easyOCRResult is the JSON tools/easyocr_ocr.py prints to stdout.
type easyOCRResult struct {
	Engine     string  `json:"engine"`
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence"`
	Pages      []struct {
		Page       int     `json:"page"`
		Text       string  `json:"text"`
		Confidence float64 `json:"confidence"`
		Boxes      int     `json:"boxes"`
	} `json:"pages"`
}

// OCR runs EasyOCR on the PDF path and returns text + metadata as an OCRResponse
// (the same type the OCRmyPDF client returned), so callers stay engine-agnostic.
func (c *EasyOCRClient) OCR(ctx context.Context, path string) (*OCRResponse, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("ocr pdf path is required")
	}
	ctx, cancel := ensureTimeout(ctx, c.timeout)
	defer cancel()

	script, err := c.resolveScript()
	if err != nil {
		return nil, err
	}
	args := []string{
		script, path,
		"--languages", c.languages,
		"--dpi", strconv.Itoa(c.dpi),
		"--batch-size", strconv.Itoa(c.batchSize),
	}
	if c.gpu {
		args = append(args, "--gpu")
	}
	out, err := runCommand(ctx, c.command, args...)
	if err != nil {
		return nil, fmt.Errorf("easyocr %s: %w", path, err)
	}

	var r easyOCRResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		return nil, fmt.Errorf("parse easyocr output %s: %w", path, err)
	}
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return nil, fmt.Errorf("easyocr %s: no readable OCR output", path)
	}

	pages := make([]OCRPage, len(r.Pages))
	for i, p := range r.Pages {
		pages[i] = OCRPage{
			Page:       p.Page + 1,
			Chars:      len([]rune(p.Text)),
			Confidence: p.Confidence,
			Words:      p.Boxes,
			DPI:        c.dpi,
		}
	}
	engine := strings.TrimSpace(r.Engine)
	if engine == "" {
		engine = "easyocr/?"
	}
	return &OCRResponse{
		Text:       text,
		Confidence: r.Confidence,
		Engine:     engine,
		Complete:   boolPtr(true),
		Pages:      pages,
	}, nil
}

// resolveScript finds tools/easyocr_ocr.py: explicit config, env override, the
// container path, then the repo-local default — mirroring MarkItDown.
func (c *EasyOCRClient) resolveScript() (string, error) {
	for _, p := range []string{
		c.script,
		os.Getenv("BANHMI_EASYOCR_SCRIPT"),
		"/opt/banhmi/easyocr_ocr.py",
		"tools/easyocr_ocr.py",
	} {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("easyocr script not found (set extract.ocr.script or BANHMI_EASYOCR_SCRIPT)")
}
