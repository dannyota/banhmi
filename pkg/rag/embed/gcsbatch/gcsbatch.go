// Package gcsbatch embeds many texts via a Cloud Run Job backed by GCS I/O. It
// replaces the HTTP-based cloudrun engine for bulk embedding: the pipeline writes
// input JSONL to GCS, triggers a Cloud Run Job execution that reads from GCS and
// writes vectors back to GCS, then reads the output vectors. This eliminates
// HTTP body limits and request timeouts for large corpora.
//
// The Cloud Run Job container reads EMBED_INPUT / EMBED_OUTPUT env vars to
// locate its GCS paths. Auth is ADC (Application Default Credentials), the same
// as Document AI.
package gcsbatch

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"
	runv2 "google.golang.org/api/run/v2"
)

const (
	// pollInterval is the gap between execution-status polls.
	pollInterval = 15 * time.Second
	// jobTimeout bounds the total wait for a Cloud Run Job execution.
	jobTimeout = 90 * time.Minute
)

// Options configures a BatchEmbedder.
type Options struct {
	// Bucket is the GCS bucket name (no gs:// prefix) for input/output data.
	Bucket string
	// CloudRunJob is the Cloud Run Job name (e.g. "banhmi-embedder").
	CloudRunJob string
	// Region is the GCP region (e.g. "asia-southeast1").
	Region string
	// Project is the GCP project ID.
	Project string
	// Dims is the expected vector dimension (1024 for Qwen3-Embedding); validated
	// on return.
	Dims int
}

// BatchEmbedder embeds texts via a Cloud Run Job with GCS I/O.
type BatchEmbedder struct {
	opts Options
	log  *slog.Logger
	gcs  *storage.Client
	run  *runv2.Service

	// Overridable in tests.
	pollInterval time.Duration
	jobTimeout   time.Duration
}

// New returns a BatchEmbedder. Auth is ADC (Application Default Credentials).
func New(opts Options, log *slog.Logger) (*BatchEmbedder, error) {
	if err := validateOpts(opts); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}

	ctx := context.Background()
	gcsClient, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("gcsbatch: GCS client: %w", err)
	}

	runService, err := runv2.NewService(ctx, option.WithScopes(runv2.CloudPlatformScope))
	if err != nil {
		_ = gcsClient.Close()
		return nil, fmt.Errorf("gcsbatch: Cloud Run API client: %w", err)
	}

	return &BatchEmbedder{
		opts:         opts,
		log:          log,
		gcs:          gcsClient,
		run:          runService,
		pollInterval: pollInterval,
		jobTimeout:   jobTimeout,
	}, nil
}

// Close releases the GCS and Cloud Run API clients.
func (b *BatchEmbedder) Close() error {
	return b.gcs.Close()
}

func validateOpts(opts Options) error {
	if opts.Bucket == "" {
		return errors.New("gcsbatch: Bucket is required")
	}
	if opts.CloudRunJob == "" {
		return errors.New("gcsbatch: CloudRunJob is required")
	}
	if opts.Region == "" {
		return errors.New("gcsbatch: Region is required")
	}
	if opts.Project == "" {
		return errors.New("gcsbatch: Project is required")
	}
	if opts.Dims <= 0 {
		return errors.New("gcsbatch: Dims must be positive")
	}
	return nil
}

// inputRow is one line of the input JSONL.
type inputRow struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}

// vectorRow is one line of the output JSONL.
type vectorRow struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

// InputWriter streams embed input rows to a GCS object one at a time, so a
// caller never holds all input texts in memory. Write assigns a sequential
// 0-based index in call order; the matching vector comes back under the same
// index.
type InputWriter struct {
	w     *storage.Writer
	enc   *json.Encoder
	count int
}

// Write appends one text as the next input row.
func (w *InputWriter) Write(text string) error {
	if err := w.enc.Encode(inputRow{Index: w.count, Text: text}); err != nil {
		return fmt.Errorf("encode input row %d: %w", w.count, err)
	}
	w.count++
	return nil
}

// EmbedStream embeds an arbitrary number of texts via a Cloud Run Job with
// bounded memory. write fills the input rows (streamed straight to GCS via
// InputWriter); onVector is invoked once per returned vector, keyed by the
// input index. It returns the number of texts embedded; 0 (write produced no
// rows) is a no-op.
func (b *BatchEmbedder) EmbedStream(ctx context.Context, write func(w *InputWriter) error, onVector func(index int, vec []float32) error) (int, error) {
	jobID := fmt.Sprintf("embed-%d", time.Now().UTC().UnixNano())
	inputPath := fmt.Sprintf("embed/input/%s.jsonl", jobID)
	outputPath := fmt.Sprintf("embed/output/%s.jsonl.gz", jobID)

	// Stream input rows directly to GCS.
	n, err := b.uploadInput(ctx, inputPath, write)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}

	inputURI := fmt.Sprintf("gs://%s/%s", b.opts.Bucket, inputPath)
	outputURI := fmt.Sprintf("gs://%s/%s", b.opts.Bucket, outputPath)

	b.log.Info("gcsbatch: triggering Cloud Run Job",
		"job", b.opts.CloudRunJob, "input", inputURI, "output", outputURI, "rows", n)

	// Trigger the Cloud Run Job execution.
	if err := b.executeJob(ctx, inputURI, outputURI); err != nil {
		return 0, err
	}

	// Read output vectors from GCS.
	b.log.Info("gcsbatch: reading output vectors", "path", outputURI)
	if err := b.readOutput(ctx, outputPath, n, onVector); err != nil {
		return 0, err
	}

	b.cleanup(ctx, inputPath, outputPath)
	b.log.Info("gcsbatch: complete", "vectors", n)
	return n, nil
}

// cleanup removes the input and output objects from GCS. Best-effort — errors
// are logged as warnings and never propagated.
func (b *BatchEmbedder) cleanup(ctx context.Context, paths ...string) {
	for _, p := range paths {
		if err := b.gcs.Bucket(b.opts.Bucket).Object(p).Delete(ctx); err != nil {
			b.log.Warn("gcsbatch: cleanup delete failed", "path", p, "err", err)
		}
	}
}

// uploadInput streams input rows to GCS via the write callback and returns the
// count. The GCS writer is closed on success; on error the write is cancelled.
func (b *BatchEmbedder) uploadInput(ctx context.Context, path string, write func(w *InputWriter) error) (int, error) {
	obj := b.gcs.Bucket(b.opts.Bucket).Object(path)
	// Use a cancelable context so we can abort the upload on error
	// instead of committing a partial object.
	uploadCtx, cancelUpload := context.WithCancel(ctx)
	defer cancelUpload()

	gw := obj.NewWriter(uploadCtx)
	gw.ContentType = "application/x-ndjson"

	iw := &InputWriter{
		w:   gw,
		enc: json.NewEncoder(gw),
	}

	if err := write(iw); err != nil {
		cancelUpload()
		_ = gw.Close()
		return 0, fmt.Errorf("write embed input to GCS: %w", err)
	}

	if err := gw.Close(); err != nil {
		return 0, fmt.Errorf("close GCS input writer: %w", err)
	}

	b.log.Info("gcsbatch: uploaded input", "bucket", b.opts.Bucket, "path", path, "rows", iw.count)
	return iw.count, nil
}

// executeJob triggers a Cloud Run Job execution with EMBED_INPUT and
// EMBED_OUTPUT env overrides, then polls until it completes or fails.
func (b *BatchEmbedder) executeJob(ctx context.Context, inputURI, outputURI string) error {
	parent := fmt.Sprintf("projects/%s/locations/%s/jobs/%s",
		b.opts.Project, b.opts.Region, b.opts.CloudRunJob)

	req := &runv2.GoogleCloudRunV2RunJobRequest{
		Overrides: &runv2.GoogleCloudRunV2Overrides{
			ContainerOverrides: []*runv2.GoogleCloudRunV2ContainerOverride{
				{
					Env: []*runv2.GoogleCloudRunV2EnvVar{
						{Name: "EMBED_INPUT", Value: inputURI},
						{Name: "EMBED_OUTPUT", Value: outputURI},
					},
				},
			},
		},
	}

	op, err := b.run.Projects.Locations.Jobs.Run(parent, req).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("trigger Cloud Run Job %s: %w", b.opts.CloudRunJob, err)
	}

	// Poll the long-running operation until done.
	return b.waitOperation(ctx, op.Name)
}

// waitOperation polls a Cloud Run long-running operation until it completes.
func (b *BatchEmbedder) waitOperation(ctx context.Context, opName string) error {
	ctx, cancel := context.WithTimeout(ctx, b.jobTimeout)
	defer cancel()

	for {
		op, err := b.run.Projects.Locations.Operations.Get(opName).Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("poll Cloud Run operation %s: %w", opName, err)
		}

		if op.Done {
			if op.Error != nil {
				return fmt.Errorf("cloud run job failed: code %d: %s", op.Error.Code, op.Error.Message)
			}
			b.log.Info("gcsbatch: Cloud Run Job execution complete", "operation", opName)
			return nil
		}

		b.log.Debug("gcsbatch: job running", "operation", opName)
		if err := sleep(ctx, b.pollInterval); err != nil {
			return err
		}
	}
}

// readOutput downloads and parses the output vectors from GCS, validating
// completeness and invoking onVector for each vector. Memory stays bounded:
// vectors are processed one at a time.
func (b *BatchEmbedder) readOutput(ctx context.Context, path string, n int, onVector func(index int, vec []float32) error) error {
	obj := b.gcs.Bucket(b.opts.Bucket).Object(path)
	r, err := obj.NewReader(ctx)
	if err != nil {
		return fmt.Errorf("open GCS output %s: %w", path, err)
	}
	defer func() { _ = r.Close() }()

	return streamParseVectors(r, n, b.opts.Dims, onVector)
}

// streamParseVectors parses the gzipped output JSONL from an io.Reader,
// validating that every index in [0,n) appears exactly once and each vector has
// exactly dims components. It invokes onVector for each in arrival order.
func streamParseVectors(r io.Reader, n, dims int, onVector func(index int, vec []float32) error) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer func() { _ = gz.Close() }()

	sc := bufio.NewScanner(gz)
	// One vector line (1024 floats as JSON) is ~12 KB; allow a generous max.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	seen := make([]bool, n)
	count := 0
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var row vectorRow
		if err := json.Unmarshal(line, &row); err != nil {
			return fmt.Errorf("parse vectors line %d: %w", lineNo, err)
		}
		if row.Index < 0 || row.Index >= n {
			return fmt.Errorf("vector index %d out of range [0,%d)", row.Index, n)
		}
		if seen[row.Index] {
			return fmt.Errorf("duplicate vector for index %d", row.Index)
		}
		if len(row.Embedding) != dims {
			return fmt.Errorf("vector %d has %d dims, want %d", row.Index, len(row.Embedding), dims)
		}
		seen[row.Index] = true
		count++
		if err := onVector(row.Index, row.Embedding); err != nil {
			return fmt.Errorf("handle vector %d: %w", row.Index, err)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("scan vectors: %w", err)
	}
	if count != n {
		for i, ok := range seen {
			if !ok {
				return fmt.Errorf("missing vector for index %d (%d of %d returned)", i, count, n)
			}
		}
		return fmt.Errorf("gcsbatch returned %d vectors for %d inputs", count, n)
	}
	return nil
}

// sleep waits for d or until ctx is done, returning ctx.Err() if cancelled.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
