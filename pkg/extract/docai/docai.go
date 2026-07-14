// Package docai wraps GCP Document AI Enterprise OCR as a synchronous OCR
// engine for scanned PDFs. Each call OCRs a single PDF via ProcessDocument
// with client-side parallelism:
//
//  1. Cache check: if text for the content hash exists in the Cache, return it
//     (no API call, no cost).
//  2. Read the PDF, count pages (go-fitz/MuPDF).
//  3. Small PDF (<=15 pages AND <=20 MB): send the whole file as one
//     ProcessDocument call with RawDocument.
//     Large PDF: render each page to PNG at the configured DPI, send each page
//     as a separate ProcessDocument call, stitch results in order.
//  4. Cache put: store the final text.
//
// OCRBatch processes multiple PDFs with a bounded worker pool (configurable
// concurrency). A per-request rate limiter prevents quota exhaustion.
//
// Auth uses Application Default Credentials (gcloud auth / service account).
package docai

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/documentai/apiv1/documentaipb"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gofitz "github.com/gen2brain/go-fitz"
	"golang.org/x/time/rate"

	documentai "cloud.google.com/go/documentai/apiv1"
)

// fitzMu serializes all MuPDF (go-fitz) calls in this package. MuPDF has
// global shared state (FreeType, glyph cache) and go-fitz passes nil locks,
// so concurrent fz_context calls are not safe. This is independent of the
// mutex in pkg/extract/fitz to avoid a circular dependency.
var fitzMu sync.Mutex

// Cache stores and retrieves OCR text keyed by document content hash.
type Cache interface {
	Get(ctx context.Context, sha256 string) (text string, ok bool, err error)
	Put(ctx context.Context, sha256, text string) error
}

// Client wraps the Document AI processor for synchronous OCR.
type Client struct {
	docai       *documentai.DocumentProcessorClient
	processor   string   // full resource name: projects/.../processors/...
	langHints   []string // OCR language hints (e.g. ["vi"], ["en", "ms"])
	cache       Cache    // nil = no caching
	dpi         int      // PDF page render DPI (default 200)
	concurrency int      // worker pool size (default 8)
	limiter     *rate.Limiter
	log         *slog.Logger
}

// Option configures optional Client parameters.
type Option func(*Client)

// WithDPI sets the DPI used when rendering PDF pages to PNG for large PDFs.
// Default is 200.
func WithDPI(dpi int) Option {
	return func(c *Client) { c.dpi = dpi }
}

// WithConcurrency sets the maximum number of parallel ProcessDocument calls.
// Default is 8.
func WithConcurrency(n int) Option {
	return func(c *Client) { c.concurrency = n }
}

// WithRequestsPerMinute sets the rate limit for Document AI API calls.
// Default is 100 requests per minute.
func WithRequestsPerMinute(rpm int) Option {
	return func(c *Client) {
		if rpm > 0 {
			c.limiter = rate.NewLimiter(rate.Every(time.Minute/time.Duration(rpm)), 1)
		}
	}
}

// New creates a Document AI client. The processor must be the full resource
// name (projects/{project}/locations/{location}/processors/{id}). Cache may be
// nil to disable caching. Auth is ADC (Application Default Credentials).
func New(processor string, cache Cache, langHints []string, log *slog.Logger, opts ...Option) (*Client, error) {
	ctx := context.Background()

	// The Document AI API endpoint must match the processor's region.
	region := regionFromProcessor(processor)
	endpoint := region + "-documentai.googleapis.com:443"

	dc, err := documentai.NewDocumentProcessorClient(ctx,
		option.WithEndpoint(endpoint))
	if err != nil {
		return nil, fmt.Errorf("documentai client: %w", err)
	}

	if len(langHints) == 0 {
		langHints = []string{"vi"}
	}

	c := &Client{
		docai:       dc,
		processor:   processor,
		langHints:   langHints,
		cache:       cache,
		dpi:         200,
		concurrency: 8,
		limiter:     rate.NewLimiter(rate.Every(time.Minute/time.Duration(100)), 1),
		log:         log,
	}
	for _, o := range opts {
		o(c)
	}
	if c.dpi <= 0 {
		c.dpi = 200
	}
	if c.concurrency <= 0 {
		c.concurrency = 8
	}
	return c, nil
}

// Close releases the Document AI client.
func (c *Client) Close() error {
	return c.docai.Close()
}

// maxInlinePages is the Document AI ProcessDocument limit for PDF input.
const maxInlinePages = 15

// maxInlineSize is the maximum PDF size (bytes) for a single ProcessDocument call.
const maxInlineSize = 20 * 1024 * 1024 // 20 MB

// OCR extracts text from a scanned PDF using Document AI. If caching is
// configured and the hash exists, no API call is made. localPath is the
// absolute path to the PDF on the local filesystem.
func (c *Client) OCR(ctx context.Context, sha256, localPath string) (string, error) {
	// Step 1: cache check.
	if c.cache != nil {
		text, ok, err := c.cache.Get(ctx, sha256)
		if err != nil {
			return "", fmt.Errorf("cache check %s: %w", sha256, err)
		}
		if ok {
			c.log.Info("docai: cache hit", "sha256", sha256)
			return text, nil
		}
	}

	// Step 2: read PDF, get size and page count.
	pdfBytes, err := os.ReadFile(localPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", localPath, err)
	}
	fileSize := len(pdfBytes)

	pageCount, err := pdfPageCount(localPath)
	if err != nil {
		return "", fmt.Errorf("page count %s: %w", sha256, err)
	}

	// Step 3: process.
	var text string
	if pageCount <= maxInlinePages && fileSize <= maxInlineSize {
		// Small PDF — send whole file as one call.
		text, err = c.processOne(ctx, pdfBytes, "application/pdf")
		if err != nil {
			return "", fmt.Errorf("process %s: %w", sha256, err)
		}
	} else {
		// Large PDF — render each page to PNG and process individually.
		text, err = c.processPageByPage(ctx, localPath, pageCount)
		if err != nil {
			return "", fmt.Errorf("process pages %s: %w", sha256, err)
		}
	}

	// Step 4: cache put.
	if c.cache != nil {
		if err := c.cache.Put(ctx, sha256, text); err != nil {
			c.log.Warn("docai: cache put failed", "sha256", sha256, "err", err)
		}
	}

	c.log.Info("docai: OCR complete", "sha256", sha256, "pages", pageCount, "chars", len([]rune(text)))
	return text, nil
}

// processPageByPage renders each page to PNG and calls processOne per page,
// stitching results in page order.
func (c *Client) processPageByPage(ctx context.Context, localPath string, pageCount int) (string, error) {
	// Render all pages to PNG under one mutex hold (MuPDF is not thread-safe).
	pngs, err := renderAllPages(localPath, pageCount, float64(c.dpi))
	if err != nil {
		return "", err
	}

	pages := make([]string, pageCount)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, c.concurrency)

	var firstErr error

	for i, png := range pngs {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, data []byte) {
			defer wg.Done()
			defer func() { <-sem }()

			text, err := c.processOne(ctx, data, "image/png")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("page %d: %w", idx, err)
				}
				c.log.Warn("docai: page OCR failed", "page", idx, "err", err)
				return
			}
			pages[idx] = text
		}(i, png)
	}
	wg.Wait()

	if firstErr != nil {
		return "", firstErr
	}

	return strings.Join(pages, "\n\n"), nil
}

// renderAllPages renders all pages of a PDF to PNG bytes under the fitz mutex.
// Opens the document once and extracts every page in sequence.
func renderAllPages(localPath string, pageCount int, dpi float64) ([][]byte, error) {
	fitzMu.Lock()
	defer fitzMu.Unlock()

	doc, err := gofitz.New(localPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", localPath, err)
	}
	defer func() { _ = doc.Close() }()

	pngs := make([][]byte, pageCount)
	for i := range pageCount {
		png, err := doc.ImagePNG(i, dpi)
		if err != nil {
			return nil, fmt.Errorf("render page %d: %w", i, err)
		}
		pngs[i] = png
	}
	return pngs, nil
}

// pdfPageCount returns the number of pages in a PDF using go-fitz.
func pdfPageCount(localPath string) (int, error) {
	fitzMu.Lock()
	defer fitzMu.Unlock()

	doc, err := gofitz.New(localPath)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", localPath, err)
	}
	defer func() { _ = doc.Close() }()

	return doc.NumPage(), nil
}

// processOne sends one synchronous ProcessDocument request with rate limiting
// and transient-error retries.
func (c *Client) processOne(ctx context.Context, content []byte, mimeType string) (string, error) {
	req := &documentaipb.ProcessRequest{
		Name: c.processor,
		Source: &documentaipb.ProcessRequest_RawDocument{
			RawDocument: &documentaipb.RawDocument{
				Content:  content,
				MimeType: mimeType,
			},
		},
		ProcessOptions: &documentaipb.ProcessOptions{
			OcrConfig: &documentaipb.OcrConfig{
				EnableNativePdfParsing: true,
				Hints: &documentaipb.OcrConfig_Hints{
					LanguageHints: c.langHints,
				},
			},
		},
	}

	const maxRetries = 3
	backoffs := [maxRetries]time.Duration{1 * time.Second, 4 * time.Second, 16 * time.Second}

	for attempt := range maxRetries + 1 {
		if err := c.limiter.Wait(ctx); err != nil {
			return "", fmt.Errorf("rate limiter: %w", err)
		}

		resp, err := c.docai.ProcessDocument(ctx, req)
		if err == nil {
			return resp.GetDocument().GetText(), nil
		}

		if attempt == maxRetries || !isTransient(err) {
			return "", fmt.Errorf("process document: %w", err)
		}

		wait := backoffs[attempt]
		c.log.Warn("docai: transient error, retrying",
			"attempt", attempt+1, "backoff", wait, "err", err)

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(wait):
		}
	}

	// Unreachable, but the compiler needs it.
	return "", fmt.Errorf("process document: max retries exceeded")
}

// isTransient reports whether an error from Document AI is transient and worth
// retrying: gRPC ResourceExhausted, Unavailable, Internal, or HTTP 429.
func isTransient(err error) bool {
	if s, ok := status.FromError(err); ok {
		switch s.Code() {
		case codes.ResourceExhausted, codes.Unavailable, codes.Internal:
			return true
		}
	}
	// Fallback: some client wrappers surface 429 outside gRPC status.
	if strings.Contains(err.Error(), "429") {
		return true
	}
	return false
}

// OCRInput is one PDF to OCR in a batch.
type OCRInput struct {
	Sha256    string
	LocalPath string
}

// OCRBatch OCRs multiple PDFs with client-side parallelism. Cached results are
// returned without an API call. Per-document failures are logged and skipped;
// the batch itself only fails on context cancellation.
func (c *Client) OCRBatch(ctx context.Context, inputs []OCRInput) (map[string]string, error) {
	results := make(map[string]string, len(inputs))

	// Deduplicate by sha256 and check cache.
	type todo struct {
		sha256    string
		localPath string
	}
	seen := make(map[string]bool, len(inputs))
	var uncached []todo

	for _, in := range inputs {
		if seen[in.Sha256] {
			continue
		}
		seen[in.Sha256] = true

		if c.cache != nil {
			text, ok, err := c.cache.Get(ctx, in.Sha256)
			if err != nil {
				c.log.Warn("docai: cache check failed", "sha256", in.Sha256, "err", err)
				uncached = append(uncached, todo{in.Sha256, in.LocalPath})
				continue
			}
			if ok {
				c.log.Info("docai: cache hit", "sha256", in.Sha256)
				results[in.Sha256] = text
				continue
			}
		}
		uncached = append(uncached, todo{in.Sha256, in.LocalPath})
	}

	if len(uncached) == 0 {
		return results, nil
	}

	// Process uncached documents in parallel.
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, c.concurrency)

	for _, item := range uncached {
		wg.Add(1)
		sem <- struct{}{}
		go func(sha256, localPath string) {
			defer wg.Done()
			defer func() { <-sem }()

			text, err := c.OCR(ctx, sha256, localPath)
			if err != nil {
				c.log.Warn("docai: OCR failed, skipping", "sha256", sha256, "err", err)
				return
			}
			mu.Lock()
			results[sha256] = text
			mu.Unlock()
		}(item.sha256, item.localPath)
	}
	wg.Wait()

	return results, nil
}

// regionFromProcessor extracts the region from a full processor resource name.
// e.g. "projects/123/locations/us/processors/456" -> "us".
// Falls back to "us" if the format is unexpected.
func regionFromProcessor(processor string) string {
	parts := strings.Split(processor, "/")
	for i, p := range parts {
		if p == "locations" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return "us"
}
