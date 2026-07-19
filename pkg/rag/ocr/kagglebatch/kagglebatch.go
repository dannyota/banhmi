// Package kagglebatch OCRs many scanned PDFs in a single Kaggle GPU job and
// returns the extracted text. It is the bulk OCR engine (the twin of
// pkg/rag/embed/kagglebatch): a Kaggle kernel runs for minutes, so this path is
// for offline backfill of scanned/gate-failed PDFs, not inline extraction.
//
// OCRAll uploads the input PDFs as a Kaggle dataset (each named <sha256>.pdf),
// pushes a GPU kernel that runs EasyOCR (the same recipe as tools/easyocr_ocr.py),
// waits for it to finish, downloads the output text, and returns it keyed back to
// each input by sha256.
package kagglebatch

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	kaggle "danny.vn/kaggle"
	"danny.vn/kaggle/datasets"
	"danny.vn/kaggle/kernels"
)

// kernelSource is the Python kernel that runs EasyOCR on the Kaggle GPU. Shipped
// as an embedded asset so the package is self-contained.
//
//go:embed kernel_ocr.py
var kernelSource string

const (
	// inputDatasetPrefix + a per-run nanosecond timestamp forms the input dataset
	// slug. Each run uses a FRESH slug because Kaggle retains a deleted slug+title
	// (recreate fails "already in use"), so reusing one after delete is impossible.
	// A unique slug per run sidesteps that and is deleted on done (the token can
	// delete a dataset, just not recreate a retained slug).
	inputDatasetPrefix = "banhmi-ocr-"
	paramsFileName     = "params.json"
	// ocrKernelPrefix prefixes the per-run OCR kernel slug+title; unique per run
	// (like inputSlug) so concurrent runs / orphaned kernels never share a kernel.
	ocrKernelPrefix = "banhmi-ocr-run-"
	outputFileName  = "ocr.jsonl"

	datasetReadyTimeout = 10 * time.Minute
	datasetPollInterval = 5 * time.Second
	// kernelRunTimeout bounds a single kernel session. OCR is slower per page than
	// embedding (a few seconds/page on GPU), so allow a wide window for a backfill.
	kernelRunTimeout   = 120 * time.Minute
	kernelPollInterval = 15 * time.Second
	logTailBytes       = 4096
)

// Options configures a BatchOCR.
type Options struct {
	// Owner is the Kaggle username owning the input dataset + OCR kernel. Optional:
	// when empty it is auto-derived from the token (WhoAmI), so callers need only
	// KAGGLE_API_TOKEN.
	Owner string
	// Accelerator is the Kaggle machine shape, e.g. "NvidiaTeslaT4".
	Accelerator string
	// Languages is the EasyOCR language list (e.g. "vi"); passed to the kernel.
	Languages string
	// DPI is the PDF render DPI (default 300).
	DPI int
	// BatchSize is the EasyOCR recognition batch size (default 32).
	BatchSize int
	// KeepArtifacts, when true, leaves the kernel + input dataset after a
	// successful run; by default both are deleted so notebooks don't pile up.
	KeepArtifacts bool
	// Token is the Kaggle API token (KGAT). When empty, kaggle.New falls back to
	// the KAGGLE_API_TOKEN environment variable. Callers source it from config.
	Token string
}

// Input is one scanned PDF to OCR: its content hash (used as the result key and
// the staged filename) and a local path to the file.
type Input struct {
	Sha256 string
	Path   string
}

// Result is the OCR output for one Input, aligned by sha256. Err is non-empty
// when that single document failed in the kernel (the batch still returns the
// rest); callers decide whether to skip or flag it.
type Result struct {
	Sha256     string
	Text       string
	Confidence float64
	Pages      int
	Err        string
}

// BatchOCR OCRs scanned PDFs in a single Kaggle GPU job.
type BatchOCR struct {
	opts     Options
	log      *slog.Logger
	client   *kaggle.Client
	datasets *datasets.Client
	kernels  *kernels.Client

	// inputSlug is the per-run input dataset slug, set in OCRAll; unique so a
	// just-deleted slug is never reused (Kaggle retains deleted slugs).
	inputSlug string
	// kernelSlug is the per-run OCR kernel slug+title, set in OCRAll; unique so
	// concurrent runs and orphaned kernels never collide on a shared kernel.
	kernelSlug string

	datasetReadyTimeout time.Duration
	datasetPollInterval time.Duration
	kernelRunTimeout    time.Duration
	kernelPollInterval  time.Duration
}

// New returns a BatchOCR. The Kaggle token comes from opts.Token (sourced from
// config), falling back to KAGGLE_API_TOKEN in the environment. Owner is optional
// and, when empty, is auto-derived from the token.
func New(opts Options, log *slog.Logger) (*BatchOCR, error) {
	var copts []kaggle.Option
	if opts.Token != "" {
		copts = append(copts, kaggle.WithToken(opts.Token))
	}
	client, err := kaggle.New(copts...)
	if err != nil {
		return nil, fmt.Errorf("new kaggle client: %w", err)
	}
	return newWithClient(opts, log, client)
}

// newWithClient builds a BatchOCR over an explicit kaggle.Client (used by New and
// by tests pointing the client at a fake endpoint).
func newWithClient(opts Options, log *slog.Logger, client *kaggle.Client) (*BatchOCR, error) {
	if log == nil {
		log = slog.Default()
	}
	if opts.DPI <= 0 {
		opts.DPI = 300
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 32
	}
	if strings.TrimSpace(opts.Languages) == "" {
		opts.Languages = "vi"
	}
	return &BatchOCR{
		opts:                opts,
		log:                 log,
		client:              client,
		datasets:            datasets.New(client),
		kernels:             kernels.New(client),
		datasetReadyTimeout: datasetReadyTimeout,
		datasetPollInterval: datasetPollInterval,
		kernelRunTimeout:    kernelRunTimeout,
		kernelPollInterval:  kernelPollInterval,
	}, nil
}

// OCRStream OCRs every input in a single Kaggle GPU job and invokes onResult once
// per output row (keyed by sha256), streamed from the downloaded file so the full
// OCR text is never all held in memory. Inputs that produced no output row are NOT
// reported — the caller detects them by which sha256 it did not see. It returns an
// error only on a whole-job failure; a single document that failed in the kernel
// arrives as a Result with Err set.
func (b *BatchOCR) OCRStream(ctx context.Context, inputs []Input, onResult func(Result) error) error {
	if len(inputs) == 0 {
		return nil
	}

	if b.opts.Owner == "" {
		owner, err := b.client.WhoAmI(ctx)
		if err != nil {
			return fmt.Errorf("resolve kaggle owner from token: %w", err)
		}
		b.opts.Owner = owner
		b.log.Info("resolved kaggle owner from token", "owner", owner)
	}

	// Fresh per-run slug: Kaggle retains a deleted slug+title, so a slug is never
	// reused. UnixNano keeps back-to-back runs from colliding.
	b.inputSlug = fmt.Sprintf("%s%d", inputDatasetPrefix, time.Now().UTC().UnixNano())
	b.kernelSlug = fmt.Sprintf("%s%d", ocrKernelPrefix, time.Now().UTC().UnixNano())

	workDir, err := os.MkdirTemp("", "banhmi-ocrbatch-*")
	if err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	inputDir := filepath.Join(workDir, "input")
	files, err := stageInputs(inputDir, inputs, b.opts)
	if err != nil {
		return err
	}

	if err := b.uploadInput(ctx, files); err != nil {
		return err
	}
	// Delete the dataset + kernel on every exit (success or failure) unless asked to
	// keep them; the slug is unique so nothing is reused next run.
	if !b.opts.KeepArtifacts {
		defer b.cleanup(ctx)
	}
	if err := b.waitDatasetReady(ctx); err != nil {
		return err
	}
	if err := b.pushKernel(ctx); err != nil {
		return err
	}
	if err := b.waitKernel(ctx); err != nil {
		return err
	}

	outDir := filepath.Join(workDir, "output")
	if _, err := b.kernels.Output(ctx, b.opts.Owner, b.kernelSlug, outDir); err != nil {
		return fmt.Errorf("download kernel output: %w", err)
	}
	return streamParseOCR(filepath.Join(outDir, outputFileName), onResult)
}

// OCRAll OCRs every input in a single Kaggle GPU job. result[i] corresponds to
// inputs[i] (matched by sha256); inputs with no output row get a Result with Err
// set. It is a convenience wrapper over OCRStream that materializes all results in
// memory; prefer OCRStream for large batches (it streams and stays bounded).
func (b *BatchOCR) OCRAll(ctx context.Context, inputs []Input) ([]Result, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	byID := make(map[string]Result, len(inputs))
	if err := b.OCRStream(ctx, inputs, func(r Result) error {
		byID[r.Sha256] = r
		return nil
	}); err != nil {
		return nil, err
	}
	results := make([]Result, len(inputs))
	for i, in := range inputs {
		if r, ok := byID[in.Sha256]; ok {
			results[i] = r
		} else {
			results[i] = Result{Sha256: in.Sha256, Err: "no OCR output returned"}
		}
	}
	return results, nil
}

// cleanup deletes the run's input dataset + kernel (best-effort). It runs on every
// OCRAll exit (success or failure) so a unique-slug dataset never lingers. Detached
// from ctx + bounded so it still runs when ctx is cancelled (e.g. activity timeout).
func (b *BatchOCR) cleanup(parent context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 2*time.Minute)
	defer cancel()
	if err := b.kernels.DeleteKernel(ctx, b.opts.Owner, b.kernelSlug); err != nil {
		b.log.Warn("could not delete ocr kernel; left for manual cleanup", "slug", b.kernelSlug, "err", err)
	} else {
		b.log.Info("deleted ocr kernel", "slug", b.kernelSlug)
	}
	if err := b.datasets.DeleteDataset(ctx, b.opts.Owner, b.inputSlug); err != nil {
		b.log.Warn("could not delete ocr input dataset; left for manual cleanup", "slug", b.inputSlug, "err", err)
	} else {
		b.log.Info("deleted ocr input dataset", "slug", b.inputSlug)
	}
}

// stageInputs copies each input PDF into inputDir as <sha256>.pdf and writes
// params.json, returning the list of files to upload as the dataset.
func stageInputs(inputDir string, inputs []Input, opts Options) ([]string, error) {
	if err := os.MkdirAll(inputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create input dir: %w", err)
	}
	files := make([]string, 0, len(inputs)+1)
	staged := make(map[string]bool, len(inputs))
	for _, in := range inputs {
		sha := strings.TrimSpace(in.Sha256)
		if sha == "" {
			return nil, fmt.Errorf("ocr input has empty sha256 (path %s)", in.Path)
		}
		// Distinct documents can share an identical PDF (same sha256). Stage and
		// upload each file once — a duplicate dataset path fails Kaggle create
		// with "Path must be unique". Output is keyed by sha256, so every input
		// sharing the sha still resolves to the one OCR result.
		if staged[sha] {
			continue
		}
		staged[sha] = true
		dst := filepath.Join(inputDir, sha+".pdf")
		if err := copyFile(in.Path, dst); err != nil {
			return nil, fmt.Errorf("stage input %s: %w", sha, err)
		}
		files = append(files, dst)
	}

	params := map[string]any{"languages": opts.Languages, "dpi": opts.DPI, "batch_size": opts.BatchSize}
	paramsPath := filepath.Join(inputDir, paramsFileName)
	pb, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}
	if err := os.WriteFile(paramsPath, pb, 0o644); err != nil {
		return nil, fmt.Errorf("write params: %w", err)
	}
	files = append(files, paramsPath)
	return files, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// uploadInput creates the fresh, unique input dataset from the staged files.
func (b *BatchOCR) uploadInput(ctx context.Context, files []string) error {
	// The slug is fresh each run, so this is always a create (no version/idempotency
	// dance). Title == slug so the title is unique too (Kaggle enforces both).
	notes := "banhmi ocr input " + time.Now().UTC().Format(time.RFC3339)
	if err := b.datasets.CreateOrVersion(ctx, b.opts.Owner, b.inputSlug, b.inputSlug, files, true, notes); err != nil {
		return fmt.Errorf("create ocr input dataset %s: %w", b.inputSlug, err)
	}
	b.log.Info("created ocr input dataset", "owner", b.opts.Owner, "slug", b.inputSlug, "files", len(files))
	return nil
}

// waitDatasetReady polls the input dataset status until READY, the context is
// cancelled, or the timeout elapses.
func (b *BatchOCR) waitDatasetReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, b.datasetReadyTimeout)
	defer cancel()

	const statusGrace = 60 * time.Second
	var firstErr time.Time
	for {
		status, err := b.datasets.Status(ctx, b.opts.Owner, b.inputSlug)
		if err != nil {
			if !isNotFound(err) {
				return fmt.Errorf("ocr input dataset status: %w", err)
			}
			if firstErr.IsZero() {
				firstErr = time.Now()
			} else if time.Since(firstErr) >= statusGrace {
				b.log.Warn("dataset status unavailable; proceeding (kernel push validates the mount)",
					"slug", b.inputSlug, "err", err)
				return nil
			}
		} else {
			firstErr = time.Time{}
			switch strings.ToUpper(status) {
			case string(datasets.DatabundleVersionStatusReady):
				b.log.Info("ocr input dataset ready", "slug", b.inputSlug)
				return nil
			case string(datasets.DatabundleVersionStatusFailed), string(datasets.DatabundleVersionStatusDeleted):
				return fmt.Errorf("ocr input dataset processing %s", status)
			}
		}
		if err := sleep(ctx, b.datasetPollInterval); err != nil {
			return err
		}
	}
}

// pushKernel pushes the EasyOCR kernel mounting the input dataset. Internet stays
// enabled so the kernel can pip-install EasyOCR + download its (small) models.
func (b *BatchOCR) pushKernel(ctx context.Context) error {
	resp, err := b.kernels.Push(ctx, &kernels.ApiSaveKernelRequest{
		Slug:               fmt.Sprintf("%s/%s", b.opts.Owner, b.kernelSlug),
		NewTitle:           b.kernelSlug,
		Text:               kernelSource,
		Language:           "python",
		KernelType:         "script",
		IsPrivate:          true,
		EnableGpu:          true,
		EnableInternet:     true,
		MachineShape:       b.opts.Accelerator,
		DatasetDataSources: []string{fmt.Sprintf("%s/%s", b.opts.Owner, b.inputSlug)},
	})
	if err != nil {
		return fmt.Errorf("push kernel: %w", err)
	}
	if resp.Error != "" {
		return fmt.Errorf("push kernel rejected: %s", resp.Error)
	}
	if len(resp.InvalidDatasetSources) > 0 {
		return fmt.Errorf("push kernel: invalid dataset sources %v", resp.InvalidDatasetSources)
	}
	b.log.Info("pushed ocr kernel", "slug", b.kernelSlug, "version", resp.VersionNumber)
	return nil
}

// waitKernel polls the kernel session until it completes, errors, is cancelled,
// the context is cancelled, or the timeout elapses.
func (b *BatchOCR) waitKernel(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, b.kernelRunTimeout)
	defer cancel()

	for {
		resp, err := b.kernels.Status(ctx, b.opts.Owner, b.kernelSlug)
		if err != nil {
			return fmt.Errorf("kernel status: %w", err)
		}
		status := strings.ToUpper(string(resp.Status))
		switch {
		case strings.Contains(status, "COMPLETE"):
			b.log.Info("kernel complete", "slug", b.kernelSlug)
			return nil
		case strings.Contains(status, "ERROR"):
			return fmt.Errorf("kernel failed (status %s): %s%s", resp.Status, resp.FailureMessage, b.logTail(ctx))
		case strings.Contains(status, "CANCEL"):
			return fmt.Errorf("kernel cancelled (status %s): %s", resp.Status, resp.FailureMessage)
		}
		b.log.Debug("kernel running", "slug", b.kernelSlug, "status", resp.Status)
		if err := sleep(ctx, b.kernelPollInterval); err != nil {
			return err
		}
	}
}

// logTail downloads the kernel output (which includes the captured log) and
// returns a short newline-prefixed tail for an error. Best effort only.
func (b *BatchOCR) logTail(ctx context.Context) string {
	dir, err := os.MkdirTemp("", "banhmi-ocrlog-*")
	if err != nil {
		return ""
	}
	defer func() { _ = os.RemoveAll(dir) }()

	files, err := b.kernels.Output(ctx, b.opts.Owner, b.kernelSlug, dir)
	if err != nil {
		return ""
	}
	for _, f := range files {
		if !strings.HasSuffix(f, ".log") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if len(data) > logTailBytes {
			data = data[len(data)-logTailBytes:]
		}
		return "\nkernel log tail:\n" + string(data)
	}
	return ""
}

// streamParseOCR parses the OCR output JSONL line by line, invoking onRow once per
// output row (keyed by sha256). It holds at most one line + one result at a time,
// so the total OCR text size does not drive heap usage.
func streamParseOCR(path string, onRow func(Result) error) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open ocr output %s: %w", outputFileName, err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	// A single document's OCR text can be large (many pages); allow a generous max
	// so a long line is never truncated.
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	type row struct {
		Sha256     string  `json:"sha256"`
		Text       string  `json:"text"`
		Confidence float64 `json:"confidence"`
		Pages      int     `json:"pages"`
		Error      string  `json:"error"`
	}
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r row
		if err := json.Unmarshal(line, &r); err != nil {
			return fmt.Errorf("parse ocr output line %d: %w", lineNo, err)
		}
		if err := onRow(Result{Sha256: r.Sha256, Text: r.Text, Confidence: r.Confidence, Pages: r.Pages, Err: r.Error}); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("scan ocr output %s: %w", outputFileName, err)
	}
	return nil
}

// isNotFound reports whether err means the dataset does not yet exist and so must
// be created rather than versioned (Kaggle returns 404, or 403 PERMISSION_DENIED
// when versioning a dataset that does not exist under the account).
func isNotFound(err error) bool {
	var apiErr *kaggle.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == 404 || apiErr.Code == 404 ||
			apiErr.Status == 403 || apiErr.Code == 403
	}
	return false
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
