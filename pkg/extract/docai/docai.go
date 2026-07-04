// Package docai wraps GCP Document AI Enterprise OCR as a batch OCR engine for
// scanned PDFs. Each call OCRs a single PDF identified by its content hash:
//
//  1. GCS cache check: if output/{sha256}/*.json exists, download and parse it
//     (no API call, no cost).
//  2. Upload: if input/{sha256}.pdf does not exist in the bucket, upload from
//     the local storage path.
//  3. batchProcess: submit the GCS document to the Document AI processor.
//  4. Poll the long-running operation until done (caller heartbeats around this).
//  5. Download the output JSON, unmarshal the Document proto, return .Text.
//
// The GCS cache (keyed by content hash) is the idempotency guard: re-runs,
// Temporal retries, and full DB rebuilds skip the API call when output exists.
// Auth uses Application Default Credentials (gcloud auth / service account).
package docai

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"cloud.google.com/go/documentai/apiv1/documentaipb"
	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/encoding/protojson"

	documentai "cloud.google.com/go/documentai/apiv1"
)

// Client wraps the Document AI processor and GCS bucket needed for OCR.
type Client struct {
	docai     *documentai.DocumentProcessorClient
	gcs       *storage.Client
	processor string // full resource name: projects/.../processors/...
	bucket    string // GCS bucket name (no gs:// prefix)
	log       *slog.Logger
}

// New creates a Document AI + GCS client pair. The processor must be the full
// resource name (projects/{project}/locations/{location}/processors/{id}). The
// bucket is the bare GCS bucket name. Auth is ADC (Application Default
// Credentials); no explicit credentials needed when gcloud auth or a service
// account is configured.
func New(processor, bucket string, log *slog.Logger) (*Client, error) {
	ctx := context.Background()

	// The Document AI API endpoint must match the processor's region.
	region := regionFromProcessor(processor)
	endpoint := region + "-documentai.googleapis.com:443"

	dc, err := documentai.NewDocumentProcessorClient(ctx,
		option.WithEndpoint(endpoint))
	if err != nil {
		return nil, fmt.Errorf("documentai client: %w", err)
	}

	gc, err := storage.NewClient(ctx)
	if err != nil {
		_ = dc.Close()
		return nil, fmt.Errorf("gcs client: %w", err)
	}

	return &Client{
		docai:     dc,
		gcs:       gc,
		processor: processor,
		bucket:    bucket,
		log:       log,
	}, nil
}

// Close releases both GCP clients.
func (c *Client) Close() error {
	var errs []error
	if err := c.docai.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close documentai: %w", err))
	}
	if err := c.gcs.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close gcs: %w", err))
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// OCR extracts text from a scanned PDF using Document AI, with a GCS cache
// keyed by content hash. If output already exists in GCS, no API call is made
// (free). localPath is the absolute path to the PDF on the local filesystem.
func (c *Client) OCR(ctx context.Context, sha256, localPath string) (string, error) {
	// Step 1: GCS cache check — if output exists, return it without an API call.
	if text, ok, err := c.cachedOutput(ctx, sha256); err != nil {
		return "", fmt.Errorf("cache check %s: %w", sha256, err)
	} else if ok {
		c.log.Info("docai: cache hit", "sha256", sha256)
		return text, nil
	}

	// Step 2: Upload PDF to GCS if not already present.
	inputURI := c.inputURI(sha256)
	if err := c.ensureUploaded(ctx, sha256, localPath); err != nil {
		return "", fmt.Errorf("upload %s: %w", sha256, err)
	}

	// Step 3: batchProcess — submit to Document AI.
	outputPrefix := c.outputPrefix(sha256)
	op, err := c.docai.BatchProcessDocuments(ctx, &documentaipb.BatchProcessRequest{
		Name: c.processor,
		InputDocuments: &documentaipb.BatchDocumentsInputConfig{
			Source: &documentaipb.BatchDocumentsInputConfig_GcsDocuments{
				GcsDocuments: &documentaipb.GcsDocuments{
					Documents: []*documentaipb.GcsDocument{{
						GcsUri:   inputURI,
						MimeType: "application/pdf",
					}},
				},
			},
		},
		DocumentOutputConfig: &documentaipb.DocumentOutputConfig{
			Destination: &documentaipb.DocumentOutputConfig_GcsOutputConfig_{
				GcsOutputConfig: &documentaipb.DocumentOutputConfig_GcsOutputConfig{
					GcsUri: "gs://" + c.bucket + "/" + outputPrefix,
				},
			},
		},
		ProcessOptions: &documentaipb.ProcessOptions{
			OcrConfig: &documentaipb.OcrConfig{
				EnableNativePdfParsing: true,
				Hints: &documentaipb.OcrConfig_Hints{
					LanguageHints: []string{"id", "ms", "en", "vi"},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("batch process %s: %w", sha256, err)
	}
	c.log.Info("docai: batch submitted", "sha256", sha256, "operation", op.Name())

	// Step 4: Poll LRO until done. The Go SDK's Wait() blocks, which is fine —
	// the caller (runOCRDocumentAI) heartbeats from the outer loop.
	if _, err := op.Wait(ctx); err != nil {
		return "", fmt.Errorf("batch wait %s: %w", sha256, err)
	}

	// Step 5: Download the output JSON from GCS and extract text.
	text, _, err := c.cachedOutput(ctx, sha256)
	if err != nil {
		return "", fmt.Errorf("read output %s: %w", sha256, err)
	}
	if text == "" {
		return "", fmt.Errorf("no output produced for %s", sha256)
	}

	c.log.Info("docai: OCR complete", "sha256", sha256, "chars", len([]rune(text)))
	return text, nil
}

// inputPath returns the GCS object key for a PDF input.
func inputPath(sha256 string) string {
	return "input/" + sha256 + ".pdf"
}

// outputPrefix returns the GCS prefix where Document AI writes output JSON.
func outputPrefixPath(sha256 string) string {
	return "output/" + sha256 + "/"
}

// inputURI returns the full gs:// URI for a PDF input.
func (c *Client) inputURI(sha256 string) string {
	return "gs://" + c.bucket + "/" + inputPath(sha256)
}

// outputPrefix returns the GCS prefix for output objects.
func (c *Client) outputPrefix(sha256 string) string {
	return outputPrefixPath(sha256)
}

// cachedOutput checks whether output JSON already exists in GCS for the given
// sha256 and returns the extracted text. Returns ("", false, nil) on a cache
// miss.
func (c *Client) cachedOutput(ctx context.Context, sha256 string) (string, bool, error) {
	prefix := outputPrefixPath(sha256)
	it := c.gcs.Bucket(c.bucket).Objects(ctx, &storage.Query{Prefix: prefix})

	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			return "", false, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("list output objects: %w", err)
		}
		if !strings.HasSuffix(attrs.Name, ".json") {
			continue
		}

		// Found a JSON output — download and parse.
		text, err := c.readDocumentText(ctx, attrs.Name)
		if err != nil {
			return "", false, err
		}
		return text, true, nil
	}
}

// ensureUploaded uploads localPath to the GCS input location if it does not
// already exist.
func (c *Client) ensureUploaded(ctx context.Context, sha256, localPath string) error {
	key := inputPath(sha256)
	_, err := c.gcs.Bucket(c.bucket).Object(key).Attrs(ctx)
	if err == nil {
		// Already uploaded.
		return nil
	}
	if err != storage.ErrObjectNotExist {
		return fmt.Errorf("check input object: %w", err)
	}

	// Upload from local file.
	f, err := openFile(localPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	w := c.gcs.Bucket(c.bucket).Object(key).NewWriter(ctx)
	w.ContentType = "application/pdf"
	if _, err := io.Copy(w, f); err != nil {
		_ = w.Close()
		return fmt.Errorf("upload to gs://%s/%s: %w", c.bucket, key, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close upload gs://%s/%s: %w", c.bucket, key, err)
	}
	c.log.Info("docai: uploaded input", "sha256", sha256, "bucket", c.bucket, "key", key)
	return nil
}

// readDocumentText downloads one GCS object (a Document AI JSON output),
// unmarshals the Document proto, and returns its .Text field.
func (c *Client) readDocumentText(ctx context.Context, objectName string) (string, error) {
	r, err := c.gcs.Bucket(c.bucket).Object(objectName).NewReader(ctx)
	if err != nil {
		return "", fmt.Errorf("open gs://%s/%s: %w", c.bucket, objectName, err)
	}
	defer func() { _ = r.Close() }()

	data, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("read gs://%s/%s: %w", c.bucket, objectName, err)
	}

	var doc documentaipb.Document
	if err := protojson.Unmarshal(data, &doc); err != nil {
		return "", fmt.Errorf("unmarshal document JSON: %w", err)
	}
	return doc.GetText(), nil
}

// openFile opens a local file for reading.
func openFile(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open local file %s: %w", path, err)
	}
	return f, nil
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
