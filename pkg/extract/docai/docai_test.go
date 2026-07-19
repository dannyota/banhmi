package docai

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestIsTransient(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "generic error",
			err:  fmt.Errorf("something failed"),
			want: false,
		},
		{
			name: "429 in message",
			err:  fmt.Errorf("HTTP 429 Too Many Requests"),
			want: true,
		},
		{
			name: "not found",
			err:  fmt.Errorf("not found"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTransient(tt.err)
			if got != tt.want {
				t.Errorf("isTransient(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// fakeCache is a test Cache implementation backed by a map.
type fakeCache struct {
	mu   sync.Mutex
	data map[string]string
	gets int
	puts int
}

func newFakeCache() *fakeCache {
	return &fakeCache{data: make(map[string]string)}
}

func (c *fakeCache) Get(_ context.Context, sha256 string) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets++
	text, ok := c.data[sha256]
	return text, ok, nil
}

func (c *fakeCache) Put(_ context.Context, sha256, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.puts++
	c.data[sha256] = text
	return nil
}

func TestCacheInterface(t *testing.T) {
	ctx := context.Background()
	cache := newFakeCache()

	// Miss.
	text, ok, err := cache.Get(ctx, "abc")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ok {
		t.Error("expected miss, got hit")
	}
	if text != "" {
		t.Errorf("expected empty text, got %q", text)
	}

	// Put.
	if err := cache.Put(ctx, "abc", "hello world"); err != nil {
		t.Fatalf("put: %v", err)
	}

	// Hit.
	text, ok, err = cache.Get(ctx, "abc")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok {
		t.Error("expected hit, got miss")
	}
	if text != "hello world" {
		t.Errorf("text = %q, want %q", text, "hello world")
	}

	if cache.gets != 2 {
		t.Errorf("gets = %d, want 2", cache.gets)
	}
	if cache.puts != 1 {
		t.Errorf("puts = %d, want 1", cache.puts)
	}
}

func TestStitchOrder(t *testing.T) {
	// Simulate the stitching logic from processPageByPage: page texts joined
	// with "\n\n" in page order.
	pages := []string{"page0 text", "page1 text", "page2 text"}
	got := strings.Join(pages, "\n\n")
	want := "page0 text\n\npage1 text\n\npage2 text"
	if got != want {
		t.Errorf("stitch = %q, want %q", got, want)
	}
}

func TestWithOptions(t *testing.T) {
	// Test that functional options set Client fields correctly.
	// We can't call New (needs real GCP auth), so apply options manually.
	c := &Client{
		dpi:         200,
		concurrency: 8,
	}

	WithDPI(300)(c)
	if c.dpi != 300 {
		t.Errorf("dpi = %d, want 300", c.dpi)
	}

	WithConcurrency(4)(c)
	if c.concurrency != 4 {
		t.Errorf("concurrency = %d, want 4", c.concurrency)
	}

	WithRequestsPerMinute(50)(c)
	if c.limiter == nil {
		t.Fatal("limiter is nil after WithRequestsPerMinute")
	}
}

func TestIntegrationOCR(t *testing.T) {
	// Skip unless ADC and a fixture are available — this calls the real API.
	testPDF := os.Getenv("BANHMI_OCR_TEST_PDF") // absolute path to a small scanned PDF
	if testPDF == "" {
		t.Skip("skipping: set BANHMI_OCR_TEST_PDF (and ADC) for integration test")
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c, err := New(nil, []string{"vi"}, log)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = c.Close() }()

	text, err := c.OCR(context.Background(), "integration_test_fixture", testPDF)
	if err != nil {
		t.Fatalf("OCR: %v", err)
	}
	if len(text) < 10 {
		t.Errorf("OCR text too short (%d chars): %q", len(text), text)
	}
	t.Logf("OCR result: %d chars, first 200: %.200s", len(text), text)
}
