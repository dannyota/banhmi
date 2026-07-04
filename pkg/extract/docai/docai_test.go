package docai

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"cloud.google.com/go/documentai/apiv1/documentaipb"
)

func TestInputPath(t *testing.T) {
	got := inputPath("abc123def456")
	want := "input/abc123def456.pdf"
	if got != want {
		t.Errorf("inputPath = %q, want %q", got, want)
	}
}

func TestOutputPrefixPath(t *testing.T) {
	got := outputPrefixPath("abc123def456")
	want := "output/abc123def456/"
	if got != want {
		t.Errorf("outputPrefixPath = %q, want %q", got, want)
	}
}

func TestRegionFromProcessor(t *testing.T) {
	tests := []struct {
		name      string
		processor string
		want      string
	}{
		{
			name:      "asia-southeast1",
			processor: "projects/272817505016/locations/asia-southeast1/processors/1394aeaa71309925",
			want:      "asia-southeast1",
		},
		{
			name:      "us",
			processor: "projects/123/locations/us/processors/456",
			want:      "us",
		},
		{
			name:      "eu",
			processor: "projects/123/locations/eu/processors/456",
			want:      "eu",
		},
		{
			name:      "malformed fallback",
			processor: "invalid",
			want:      "us",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := regionFromProcessor(tt.processor)
			if got != tt.want {
				t.Errorf("regionFromProcessor(%q) = %q, want %q", tt.processor, got, tt.want)
			}
		})
	}
}

func TestParseDocumentJSON(t *testing.T) {
	// Minimal Document AI output JSON that mirrors the real structure.
	fixture := &documentaipb.Document{
		Text: "PERATURAN BANK INDONESIA\nNOMOR 10 TAHUN 2025\nTENTANG PENILAIAN TINGKAT KESEHATAN BANK UMUM\n",
		Pages: []*documentaipb.Document_Page{
			{
				PageNumber: 1,
				DetectedLanguages: []*documentaipb.Document_Page_DetectedLanguage{
					{LanguageCode: "id", Confidence: 0.95},
				},
			},
		},
	}

	data, err := protojson.Marshal(fixture)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}

	var got documentaipb.Document
	if err := protojson.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.GetText() != fixture.GetText() {
		t.Errorf("text = %q, want %q", got.GetText(), fixture.GetText())
	}
	if len(got.GetPages()) != 1 {
		t.Errorf("pages = %d, want 1", len(got.GetPages()))
	}
}

func TestIntegrationOCR(t *testing.T) {
	// Skip unless ADC and processor are available — this calls the real API.
	processor := os.Getenv("BANHMI_DOCAI_PROCESSOR")
	bucket := os.Getenv("BANHMI_DOCAI_BUCKET")
	testPDF := os.Getenv("BANHMI_DOCAI_TEST_PDF") // absolute path to a small scanned PDF
	if processor == "" || bucket == "" || testPDF == "" {
		t.Skip("skipping: set BANHMI_DOCAI_PROCESSOR, BANHMI_DOCAI_BUCKET, BANHMI_DOCAI_TEST_PDF for integration test")
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c, err := New(processor, bucket, log)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Use a fixed test sha256 so repeated runs hit the cache.
	text, err := c.OCR(context.Background(), "integration_test_fixture", testPDF)
	if err != nil {
		t.Fatalf("OCR: %v", err)
	}
	if len(text) < 10 {
		t.Errorf("OCR text too short (%d chars): %q", len(text), text)
	}
	t.Logf("OCR result: %d chars, first 200: %.200s", len(text), text)
}
