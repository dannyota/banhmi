// Package docai wraps Google Cloud Vision OCR (images:annotate with
// DOCUMENT_TEXT_DETECTION, model builtin/latest) as a synchronous OCR engine
// for scanned PDFs. Vision replaced Document AI ProcessDocument here: the
// Document AI online page quota is 5 pages/min in asia-southeast1, while
// Vision's global endpoint allows 1,800 document-text requests/min at the
// same per-page price. Each call OCRs a single PDF with client-side
// parallelism:
//
//  1. Cache check: if text for the content hash exists in the Cache, return it
//     (no API call, no cost).
//  2. Count pages (go-fitz/MuPDF) and render every page to JPEG at the
//     configured DPI — Vision has no GCS-free PDF input, so one page = one
//     image = one request = one billed unit.
//  3. Send each page as one images:annotate request, stitch results in
//     page order.
//  4. Cache put: store the final text.
//
// OCRBatch processes multiple PDFs with a bounded worker pool (configurable
// concurrency). A per-request rate limiter prevents quota exhaustion.
//
// Auth uses Application Default Credentials (gcloud auth / service account).
package docai

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"log/slog"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/vision/v2/apiv1/visionpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gofitz "github.com/gen2brain/go-fitz"
	"golang.org/x/time/rate"

	vision "cloud.google.com/go/vision/v2/apiv1"
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

// Client wraps the Vision image annotator for synchronous OCR.
type Client struct {
	vision      *vision.ImageAnnotatorClient
	langHints   []string // OCR language hints (e.g. ["vi"], ["en", "ms"])
	cache       Cache    // nil = no caching
	dpi         int      // PDF page render DPI (default 200)
	concurrency int      // worker pool size (default 8)
	limiter     *rate.Limiter
	// inflight bounds concurrent ProcessDocument RPCs GLOBALLY, across all
	// docs and their page fan-outs. Without it, OCRBatch's doc workers times
	// processPageByPage's page workers put up to concurrency^2 multi-MB
	// uploads in flight at once; admission (the rate limiter) keeps running
	// ahead of what the uplink can drain, the upload queue grows without
	// bound, and every RPC eventually crosses its 300 s deadline. A new
	// upload may start only when a slot frees — closed-loop admission.
	inflight chan struct{}
	log      *slog.Logger
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

// New creates a Vision OCR client on the global endpoint. Cache may be nil to
// disable caching. Auth is ADC (Application Default Credentials). The default
// rate limit (600 req/min) sits well under Vision's 1,800/min document-text
// quota while clearing a ~3,000-page backfill in minutes.
func New(cache Cache, langHints []string, log *slog.Logger, opts ...Option) (*Client, error) {
	ctx := context.Background()

	vc, err := vision.NewImageAnnotatorClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("vision client: %w", err)
	}

	if len(langHints) == 0 {
		langHints = []string{"vi"}
	}

	c := &Client{
		vision:      vc,
		langHints:   langHints,
		cache:       cache,
		dpi:         200,
		concurrency: 8,
		limiter:     rate.NewLimiter(rate.Every(time.Minute/time.Duration(600)), 1),
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
	c.inflight = make(chan struct{}, c.concurrency)
	return c, nil
}

// Close releases the Vision client.
func (c *Client) Close() error {
	return c.vision.Close()
}

// OCR extracts text from a scanned PDF using Vision OCR. If caching is
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

	// Step 2: page count. Vision has no GCS-free PDF input, so every
	// document goes page-by-page regardless of size.
	pageCount, err := pdfPageCount(localPath)
	if err != nil {
		return "", fmt.Errorf("page count %s: %w", sha256, err)
	}

	// Step 3: process per page.
	text, err := c.processPageByPage(ctx, localPath, pageCount)
	if err != nil {
		return "", fmt.Errorf("process pages %s: %w", sha256, err)
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

// processPageByPage renders each page to JPEG and calls processOne per page,
// stitching results in page order.
func (c *Client) processPageByPage(ctx context.Context, localPath string, pageCount int) (string, error) {
	// Render all pages under one mutex hold (MuPDF is not thread-safe).
	jpegs, err := renderAllPages(localPath, pageCount, float64(c.dpi))
	if err != nil {
		return "", err
	}

	pages := make([]string, pageCount)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, c.concurrency)

	var firstErr error

	for i, page := range jpegs {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, data []byte) {
			defer wg.Done()
			defer func() { <-sem }()

			text, err := c.processOne(ctx, data, "image/jpeg")
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
		}(i, page)
	}
	wg.Wait()

	if firstErr != nil {
		return "", firstErr
	}

	return strings.Join(pages, "\n\n"), nil
}

// renderAllPages renders all pages of a PDF to JPEG bytes under the fitz
// mutex. JPEG, not PNG: scanned pages are photographic content, where lossless
// PNG balloons to 10-20 MB per page and eight concurrent uploads starve the
// uplink until every ProcessDocument call hits its 300 s deadline; JPEG at
// quality 85 is 10-20x smaller with no meaningful OCR loss. Opens the document
// once and renders every page in sequence.
func renderAllPages(localPath string, pageCount int, dpi float64) ([][]byte, error) {
	fitzMu.Lock()
	defer fitzMu.Unlock()

	doc, err := gofitz.New(localPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", localPath, err)
	}
	defer func() { _ = doc.Close() }()

	pages := make([][]byte, pageCount)
	var buf bytes.Buffer
	for i := range pageCount {
		img, err := doc.ImageDPI(i, dpi)
		if err != nil {
			return nil, fmt.Errorf("render page %d: %w", i, err)
		}
		buf.Reset()
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
			return nil, fmt.Errorf("encode page %d: %w", i, err)
		}
		pages[i] = append([]byte(nil), buf.Bytes()...)
	}
	return pages, nil
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

// processOne sends one page image through Vision images:annotate
// (DOCUMENT_TEXT_DETECTION, model builtin/latest) with rate limiting and
// transient-error retries.
func (c *Client) processOne(ctx context.Context, content []byte, _ string) (string, error) {
	req := &visionpb.BatchAnnotateImagesRequest{
		Requests: []*visionpb.AnnotateImageRequest{{
			Image: &visionpb.Image{Content: content},
			Features: []*visionpb.Feature{{
				Type:  visionpb.Feature_DOCUMENT_TEXT_DETECTION,
				Model: "builtin/latest",
			}},
			ImageContext: &visionpb.ImageContext{LanguageHints: c.langHints},
		}},
	}

	const maxRetries = 3
	backoffs := [maxRetries]time.Duration{1 * time.Second, 4 * time.Second, 16 * time.Second}

	for attempt := range maxRetries + 1 {
		// Global in-flight slot first (closed-loop: bounded by completions),
		// then the open-loop rate limiter as a quota safety net.
		select {
		case c.inflight <- struct{}{}:
		case <-ctx.Done():
			return "", fmt.Errorf("inflight wait: %w", ctx.Err())
		}
		if err := c.limiter.Wait(ctx); err != nil {
			<-c.inflight
			return "", fmt.Errorf("rate limiter: %w", err)
		}

		resp, err := c.vision.BatchAnnotateImages(ctx, req)
		<-c.inflight
		if err == nil {
			// Vision returns 200 with a per-response error status; surface it.
			r := resp.GetResponses()[0]
			if rerr := r.GetError(); rerr != nil {
				err = status.ErrorProto(rerr)
			} else {
				return r.GetFullTextAnnotation().GetText(), nil
			}
		}

		if attempt == maxRetries || !isTransient(err) {
			return "", fmt.Errorf("annotate image: %w", err)
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
	return "", fmt.Errorf("annotate image: max retries exceeded")
}

// isTransient reports whether an error from Document AI is transient and worth
// retrying: gRPC ResourceExhausted, Unavailable, Internal, DeadlineExceeded,
// or HTTP 429. DeadlineExceeded counts because a request that queued behind a
// congested uplink usually succeeds once the in-flight window has drained.
func isTransient(err error) bool {
	if s, ok := status.FromError(err); ok {
		switch s.Code() {
		case codes.ResourceExhausted, codes.Unavailable, codes.Internal, codes.DeadlineExceeded:
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
