package extract

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

const (
	defaultOCRTesseractCommand = "tesseract"
	defaultOCRMyPDFCommand     = "ocrmypdf"
	defaultOCRDPI              = 300
	defaultOCRLanguage         = "vie+eng"
	defaultOCRTimeout          = 10 * time.Minute
)

// OCRClient runs local PDF OCR via OCRmyPDF and returns extracted text.
type OCRClient struct {
	tesseract string
	ocrmypdf  string
	dpi       int
	language  string
	timeout   time.Duration
}

// NewOCRClient returns a local OCR client from explicit binary names and options.
func NewOCRClient(tesseractCommand, pdfToImageCommand string, dpi int, language string) *OCRClient {
	tesseractCommand = strings.TrimSpace(tesseractCommand)
	if tesseractCommand == "" {
		tesseractCommand = defaultOCRTesseractCommand
	}
	pdfToImageCommand = strings.TrimSpace(pdfToImageCommand)
	if pdfToImageCommand == "" {
		pdfToImageCommand = defaultOCRMyPDFCommand
	}
	language = strings.TrimSpace(language)
	if language == "" {
		language = defaultOCRLanguage
	}
	if dpi <= 0 {
		dpi = defaultOCRDPI
	}
	return &OCRClient{
		tesseract: tesseractCommand,
		ocrmypdf:  pdfToImageCommand,
		dpi:       dpi,
		language:  language,
		timeout:   defaultOCRTimeout,
	}
}

// OCRResponse is the structured output used by the extraction activity.
type OCRResponse struct {
	Text       string    `json:"text"`
	Confidence float64   `json:"confidence"`
	Engine     string    `json:"engine"`
	Complete   *bool     `json:"complete,omitempty"`
	Pages      []OCRPage `json:"pages,omitempty"`
}

// OCRPage is one page-level OCR status item.
type OCRPage struct {
	Page       int     `json:"page"`
	Chars      int     `json:"chars"`
	Confidence float64 `json:"confidence"`
	Seconds    float64 `json:"seconds"`
	Error      string  `json:"error"`
	Words      int     `json:"words,omitempty"`
	DPI        int     `json:"dpi,omitempty"`
}

// IsComplete reports whether every rendered PDF page was successfully OCRed.
func (r OCRResponse) IsComplete() bool {
	return r.Complete == nil || *r.Complete
}

// OCR runs local OCR on the PDF path and returns plain text + metadata.
func (c *OCRClient) OCR(ctx context.Context, path string) (*OCRResponse, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("ocr pdf path is required")
	}

	ctx, cancel := ensureTimeout(ctx, c.timeout)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "banhmi-ocr-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	start := time.Now()

	outputPDF := filepath.Join(tmpDir, "ocr-output.pdf")
	outputTextPath := filepath.Join(tmpDir, "ocr.txt")
	args := []string{
		"--language", c.language,
		"--tesseract-oem", "3",
		"--tesseract-pagesegmode", "6",
		"--output-type", "pdf",
		"--force-ocr",
		"--invalidate-digital-signatures",
		"--sidecar", outputTextPath,
		path,
		outputPDF,
	}
	if _, err := runCommand(ctx, c.ocrmypdf, args...); err != nil {
		return nil, fmt.Errorf("ocrmypdf %s: %w", path, err)
	}

	rawText, err := os.ReadFile(outputTextPath)
	if err != nil {
		return nil, fmt.Errorf("ocr output text %s: %w", path, err)
	}

	text := strings.TrimSpace(string(rawText))
	if text == "" {
		return nil, fmt.Errorf("ocr %s: no readable OCR output", path)
	}

	pageCount := strings.Count(text, "\f") + 1
	pages := make([]OCRPage, pageCount)
	pages[0] = OCRPage{
		Page:       1,
		Chars:      len([]rune(text)),
		Confidence: estimateTextConfidence(text),
		Seconds:    time.Since(start).Seconds(),
		DPI:        c.dpi,
	}
	for i := 1; i < pageCount; i++ {
		pages[i] = OCRPage{
			Page:       i + 1,
			Confidence: 0,
			DPI:        c.dpi,
		}
	}

	return &OCRResponse{
		Text:       text,
		Confidence: estimateTextConfidence(text),
		Engine:     "ocrmypdf/" + filepath.Base(c.ocrmypdf),
		Complete:   boolPtr(true),
		Pages:      pages,
	}, nil
}

func runCommand(ctx context.Context, command string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("%s: %w: %s", command, err, msg)
		}
		return "", err
	}
	return string(out), nil
}

func ensureTimeout(ctx context.Context, d time.Duration) (context.Context, func()) {
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, func() {}
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	return ctx, cancel
}

func estimateTextConfidence(text string) float64 {
	meaningful := 0
	alphaNumeric := 0
	for _, r := range text {
		if unicode.IsSpace(r) || r == '\f' || r == '\r' {
			continue
		}
		meaningful++
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			alphaNumeric++
		}
	}
	if meaningful == 0 {
		return 0
	}
	return float64(alphaNumeric) / float64(meaningful)
}

func boolPtr(v bool) *bool {
	return &v
}
