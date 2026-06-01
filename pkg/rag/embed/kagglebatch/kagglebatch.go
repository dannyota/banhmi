// Package kagglebatch embeds many texts in a single Kaggle GPU job and returns
// the vectors. It is a bulk-only embedder, NOT the synchronous embed.Embedder
// (which serves one query at a time): a Kaggle kernel runs for minutes, so this
// path is for offline backfill of the whole corpus, not serve-time embedding.
//
// EmbedAll uploads the input texts as a Kaggle dataset, pushes a GPU kernel that
// runs the BGE-M3 dense recipe (CLS pooling + L2 normalize, 1024-d — matching
// banhmi's local OVMS embedder), waits for it to finish, downloads the output
// vectors, and returns them aligned to the input order.
package kagglebatch

import (
	"bufio"
	"bytes"
	"compress/gzip"
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

// kernelSource is the Python kernel that runs on the Kaggle GPU. It is shipped
// as an embedded asset so the package is self-contained.
//
//go:embed kernel_embed.py
var kernelSource string

const (
	// inputDatasetPrefix + a per-run nanosecond timestamp forms the input dataset
	// slug. Each run uses a FRESH slug because Kaggle retains a deleted slug+title
	// (recreate fails "already in use"), so reusing one after delete is impossible.
	// A unique slug per run sidesteps that and is deleted on done.
	inputDatasetPrefix = "banhmi-embed-"
	// inputFileName is the JSONL file uploaded into the input dataset.
	inputFileName = "input.jsonl"
	// embedKernelPrefix prefixes the per-run embed kernel slug+title. Unique per
	// run (like inputSlug) so concurrent runs and orphaned kernels never share one
	// kernel (push/output/delete would otherwise race on a single slug).
	embedKernelPrefix = "banhmi-embed-run-"
	// outputFileName is the vectors file the kernel writes to /kaggle/working.
	outputFileName = "vectors.jsonl.gz"

	// datasetReadyTimeout bounds waiting for the input dataset to finish
	// processing into a mountable databundle.
	datasetReadyTimeout = 10 * time.Minute
	// datasetPollInterval is the gap between dataset-status polls.
	datasetPollInterval = 5 * time.Second
	// kernelRunTimeout bounds a single kernel session; the full corpus embeds in
	// minutes, but queueing on Kaggle can add slack.
	kernelRunTimeout = 60 * time.Minute
	// kernelPollInterval is the gap between kernel-status polls.
	kernelPollInterval = 15 * time.Second
	// logTailBytes caps how much of a failed kernel's log is folded into the
	// returned error.
	logTailBytes = 4096
)

// Options configures a BatchEmbedder.
type Options struct {
	// Owner is the Kaggle username owning the input dataset + embed kernel.
	// Optional: when empty it is auto-derived from the token (WhoAmI), so callers
	// only need KAGGLE_API_TOKEN — no username to configure.
	Owner string
	// ModelDataset is an optional "owner/slug" mirror of BGE-M3 to mount so the
	// kernel loads the model offline. Empty pulls BAAI/bge-m3 from HuggingFace
	// (which requires the kernel session to have internet enabled).
	ModelDataset string
	// Accelerator is the Kaggle machine shape, e.g. "NvidiaTeslaT4".
	Accelerator string
	// Dims is the expected vector dimension (1024 for BGE-M3); validated on
	// return.
	Dims int
	// KeepArtifacts, when true, leaves the embed kernel in place after a
	// successful run; by default the kernel is deleted so notebooks don't pile up.
	KeepArtifacts bool
	// Token is the Kaggle API token (KGAT). When empty, kaggle.New falls back to
	// the KAGGLE_API_TOKEN environment variable. Callers source it from config.
	Token string
}

// BatchEmbedder embeds texts in a single Kaggle GPU job.
type BatchEmbedder struct {
	opts     Options
	log      *slog.Logger
	client   *kaggle.Client
	datasets *datasets.Client
	kernels  *kernels.Client

	// inputSlug is the per-run input dataset slug, set in EmbedAll; unique so a
	// just-deleted slug is never reused (Kaggle retains deleted slugs).
	inputSlug string
	// kernelSlug is the per-run embed kernel slug+title, set in EmbedAll; unique so
	// concurrent runs and orphaned kernels never collide on a shared kernel.
	kernelSlug string

	// Poll timing; defaults from the package constants. Overridable in tests to
	// avoid real multi-second waits.
	datasetReadyTimeout time.Duration
	datasetPollInterval time.Duration
	kernelRunTimeout    time.Duration
	kernelPollInterval  time.Duration
}

// New returns a BatchEmbedder. The Kaggle token comes from opts.Token (sourced
// from config), falling back to KAGGLE_API_TOKEN in the environment. Dims is
// required; Owner is optional and, when empty, is auto-derived from the token.
func New(opts Options, log *slog.Logger) (*BatchEmbedder, error) {
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

// newWithClient builds a BatchEmbedder over an explicit kaggle.Client. It is the
// shared constructor used by New and by tests that point the client at a fake
// endpoint via kaggle.WithEndpoint.
func newWithClient(opts Options, log *slog.Logger, client *kaggle.Client) (*BatchEmbedder, error) {
	if opts.Dims <= 0 {
		return nil, errors.New("kagglebatch: Dims must be positive")
	}
	if log == nil {
		log = slog.Default()
	}
	return &BatchEmbedder{
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

// InputWriter streams embed input rows to the on-disk JSONL one at a time, so a
// caller never holds all input texts in memory. Write assigns a sequential 0-based
// index in call order; the matching vector comes back under the same index.
type InputWriter struct {
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

// EmbedStream embeds an arbitrary number of texts in a single Kaggle GPU job with
// bounded memory. write fills the input rows (each streamed straight to the input
// JSONL on disk via InputWriter); onVector is invoked once per returned vector,
// keyed by the input index. Neither the input texts nor the output vectors are
// ever all held in memory — the JSONL files on disk are the buffer. It returns the
// number of texts embedded; 0 (write produced no rows) is a no-op that creates no
// dataset or kernel.
func (b *BatchEmbedder) EmbedStream(ctx context.Context, write func(w *InputWriter) error, onVector func(index int, vec []float32) error) (int, error) {
	workDir, err := os.MkdirTemp("", "banhmi-kagglebatch-*")
	if err != nil {
		return 0, fmt.Errorf("create work dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	// Stream the input rows straight to disk; the caller never holds them all.
	inputPath := filepath.Join(workDir, inputFileName)
	f, err := os.Create(inputPath)
	if err != nil {
		return 0, fmt.Errorf("create input file: %w", err)
	}
	iw := &InputWriter{enc: json.NewEncoder(f)}
	if werr := write(iw); werr != nil {
		_ = f.Close()
		return 0, fmt.Errorf("write embed input: %w", werr)
	}
	if cerr := f.Close(); cerr != nil {
		return 0, fmt.Errorf("close input file: %w", cerr)
	}
	n := iw.count
	if n == 0 {
		return 0, nil // nothing to embed; no dataset or kernel is created
	}

	// The input dataset and kernel are created under the token's own account, so
	// Owner defaults to that account (auto-derived; callers need only the token).
	if b.opts.Owner == "" {
		owner, err := b.client.WhoAmI(ctx)
		if err != nil {
			return 0, fmt.Errorf("resolve kaggle owner from token: %w", err)
		}
		b.opts.Owner = owner
		b.log.Info("resolved kaggle owner from token", "owner", owner)
	}

	// Fresh per-run slug: Kaggle retains a deleted slug+title, so a slug is never
	// reused. UnixNano keeps back-to-back runs from colliding.
	b.inputSlug = fmt.Sprintf("%s%d", inputDatasetPrefix, time.Now().UTC().UnixNano())
	b.kernelSlug = fmt.Sprintf("%s%d", embedKernelPrefix, time.Now().UTC().UnixNano())

	if err := b.uploadInput(ctx, inputPath); err != nil {
		return 0, err
	}
	// Delete the dataset + kernel on every exit (success or failure) unless asked to
	// keep them; the slug is unique so nothing is reused next run.
	if !b.opts.KeepArtifacts {
		defer b.cleanup(ctx)
	}
	if err := b.waitDatasetReady(ctx); err != nil {
		return 0, err
	}
	if err := b.pushKernel(ctx); err != nil {
		return 0, err
	}
	if err := b.waitKernel(ctx); err != nil {
		return 0, err
	}

	outDir := filepath.Join(workDir, "output")
	if _, err := b.kernels.Output(ctx, b.opts.Owner, b.kernelSlug, outDir); err != nil {
		return 0, fmt.Errorf("download kernel output: %w", err)
	}
	vectorsPath := filepath.Join(outDir, outputFileName)
	if err := streamParseVectors(vectorsPath, n, b.opts.Dims, onVector); err != nil {
		return 0, err
	}
	return n, nil
}

// EmbedAll embeds every text in a single Kaggle GPU job and returns the vectors in
// input order. It is a convenience wrapper over EmbedStream that materializes all
// input and output in memory; prefer EmbedStream for large corpora (it streams and
// stays memory-bounded).
func (b *BatchEmbedder) EmbedAll(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, len(texts))
	n, err := b.EmbedStream(ctx,
		func(w *InputWriter) error {
			for _, t := range texts {
				if err := w.Write(t); err != nil {
					return err
				}
			}
			return nil
		},
		func(index int, vec []float32) error {
			out[index] = vec
			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	if n != len(texts) {
		return nil, fmt.Errorf("kaggle returned %d vectors for %d texts", n, len(texts))
	}
	return out, nil
}

// cleanup deletes the run's input dataset + kernel (best-effort). It runs on every
// EmbedAll exit (success or failure) so a unique-slug dataset never lingers.
// Detached from ctx + bounded so it still runs when ctx is cancelled.
func (b *BatchEmbedder) cleanup(parent context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 2*time.Minute)
	defer cancel()
	if err := b.kernels.DeleteKernel(ctx, b.opts.Owner, b.kernelSlug); err != nil {
		b.log.Warn("could not delete embed kernel; left for manual cleanup", "slug", b.kernelSlug, "err", err)
	} else {
		b.log.Info("deleted embed kernel", "slug", b.kernelSlug)
	}
	if err := b.datasets.DeleteDataset(ctx, b.opts.Owner, b.inputSlug); err != nil {
		b.log.Warn("could not delete embed input dataset; left for manual cleanup", "slug", b.inputSlug, "err", err)
	} else {
		b.log.Info("deleted embed input dataset", "slug", b.inputSlug)
	}
}

// uploadInput creates the fresh, unique input dataset from the input JSONL. The
// slug is new each run, so this is always a create (title == slug, also unique).
func (b *BatchEmbedder) uploadInput(ctx context.Context, inputPath string) error {
	notes := "banhmi embed input " + time.Now().UTC().Format(time.RFC3339)
	if err := b.datasets.CreateOrVersion(ctx, b.opts.Owner, b.inputSlug, b.inputSlug, []string{inputPath}, true, notes); err != nil {
		return fmt.Errorf("create embed input dataset %s: %w", b.inputSlug, err)
	}
	b.log.Info("created embed input dataset", "owner", b.opts.Owner, "slug", b.inputSlug)
	return nil
}

// waitDatasetReady polls the input dataset status until it is READY, the
// context is cancelled, or the timeout elapses.
func (b *BatchEmbedder) waitDatasetReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, b.datasetReadyTimeout)
	defer cancel()

	const statusGrace = 60 * time.Second
	var firstErr time.Time
	for {
		status, err := b.datasets.Status(ctx, b.opts.Owner, b.inputSlug)
		if err != nil {
			// Right after create, and with access-token auth, GetDatasetStatus can
			// transiently 403/404 before the dataset is queryable. Tolerate it for a
			// grace period, then proceed — pushKernel validates the mount via
			// InvalidDatasetSources, so a genuinely-bad dataset still fails fast.
			if !isNotFound(err) {
				return fmt.Errorf("input dataset status: %w", err)
			}
			if firstErr.IsZero() {
				firstErr = time.Now()
			} else if time.Since(firstErr) >= statusGrace {
				b.log.Warn("dataset status unavailable; proceeding (kernel push validates the mount)",
					"slug", b.inputSlug, "err", err)
				return nil
			}
			b.log.Debug("dataset status not yet available; retrying", "slug", b.inputSlug)
		} else {
			firstErr = time.Time{}
			switch strings.ToUpper(status) {
			case string(datasets.DatabundleVersionStatusReady):
				b.log.Info("input dataset ready", "slug", b.inputSlug)
				return nil
			case string(datasets.DatabundleVersionStatusFailed), string(datasets.DatabundleVersionStatusDeleted):
				return fmt.Errorf("input dataset processing %s", status)
			}
		}
		if err := sleep(ctx, b.datasetPollInterval); err != nil {
			return err
		}
	}
}

// pushKernel pushes the embed kernel and checks the push was accepted. The
// kernel mounts the input dataset, plus the model mirror when configured;
// internet is enabled only when no mirror is set (to pull from HuggingFace).
func (b *BatchEmbedder) pushKernel(ctx context.Context) error {
	dataSources := []string{fmt.Sprintf("%s/%s", b.opts.Owner, b.inputSlug)}
	if b.opts.ModelDataset != "" {
		dataSources = append(dataSources, b.opts.ModelDataset)
	}

	resp, err := b.kernels.Push(ctx, &kernels.ApiSaveKernelRequest{
		Slug:               fmt.Sprintf("%s/%s", b.opts.Owner, b.kernelSlug),
		NewTitle:           b.kernelSlug,
		Text:               kernelSource,
		Language:           "python",
		KernelType:         "script",
		IsPrivate:          true,
		EnableGpu:          true,
		EnableInternet:     b.opts.ModelDataset == "",
		MachineShape:       b.opts.Accelerator,
		DatasetDataSources: dataSources,
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
	b.log.Info("pushed embed kernel", "slug", b.kernelSlug, "version", resp.VersionNumber)
	return nil
}

// waitKernel polls the kernel session status until it completes, errors, is
// cancelled, the context is cancelled, or the timeout elapses. On an ERROR /
// CANCEL terminal status it folds a tail of the kernel log into the error.
func (b *BatchEmbedder) waitKernel(ctx context.Context) error {
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
// returns a short, newline-prefixed tail for inclusion in an error. A download
// failure yields an empty string — best effort only.
func (b *BatchEmbedder) logTail(ctx context.Context) string {
	dir, err := os.MkdirTemp("", "banhmi-kagglelog-*")
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

// inputRow is one line of the input JSONL the kernel reads.
type inputRow struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}

// vectorRow is one line of the output JSONL the kernel writes.
type vectorRow struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

// writeInputJSONL writes one {"index": i, "text": ...} line per text.
func writeInputJSONL(path string, texts []string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create input file: %w", err)
	}
	defer func() { _ = f.Close() }()

	enc := json.NewEncoder(f)
	for i, text := range texts {
		if err := enc.Encode(inputRow{Index: i, Text: text}); err != nil {
			return fmt.Errorf("encode input row %d: %w", i, err)
		}
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close input file: %w", err)
	}
	return nil
}

// parseVectorsJSONL reads the kernel output into a slice in input order. It is a
// convenience wrapper over streamParseVectors; prefer streamParseVectors for large
// outputs (it never materializes all vectors).
func parseVectorsJSONL(path string, n, dims int) ([][]float32, error) {
	out := make([][]float32, n)
	if err := streamParseVectors(path, n, dims, func(index int, vec []float32) error {
		out[index] = vec
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// streamParseVectors parses the kernel output line by line, validating that every
// index in [0,n) appears exactly once and each vector has exactly dims components,
// invoking onVector for each in arrival order. It holds at most one line + one
// vector at a time, so the output size does not drive heap usage.
func streamParseVectors(path string, n, dims int, onVector func(index int, vec []float32) error) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open vectors file %s: %w", outputFileName, err)
	}
	defer func() { _ = f.Close() }()

	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, gerr := gzip.NewReader(f)
		if gerr != nil {
			return fmt.Errorf("gzip reader for %s: %w", outputFileName, gerr)
		}
		defer func() { _ = gz.Close() }()
		r = gz
	}

	sc := bufio.NewScanner(r)
	// One BGE-M3 vector line (1024 floats as JSON) is ~12 KB; allow a generous max
	// so a long line is never truncated.
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
		return fmt.Errorf("scan vectors file %s: %w", outputFileName, err)
	}
	if count != n {
		for i, ok := range seen {
			if !ok {
				return fmt.Errorf("missing vector for index %d (%d of %d returned)", i, count, n)
			}
		}
		return fmt.Errorf("kaggle returned %d vectors for %d inputs", count, n)
	}
	return nil
}

// isNotFound reports whether err means the dataset does not yet exist and so
// must be created rather than versioned. Kaggle returns 404 in the obvious case,
// but also 403 PERMISSION_DENIED ("datasets.update denied") when versioning a
// dataset that does not exist under the account — treat both as "create it".
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
