package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"danny.vn/banhmi/pkg/extract"
	ocrbatch "danny.vn/banhmi/pkg/rag/ocr/kagglebatch"
	dbsilver "danny.vn/banhmi/pkg/store/silver"
)

// ocrHeartbeat is how often the OcrAll activity heartbeats while it waits on the
// (minutes-long) Kaggle GPU job or grinds local CPU OCR.
const ocrHeartbeat = 30 * time.Second

// OcrAllParams configures a whole-corpus OCR pass over the scanned PDFs the gate
// flagged (born-digital extraction failed or was non-binding). Engine is "kaggle"
// or "local" (resolved by the caller from config.OcrEngine()); the Kaggle fields
// and EasyOCR knobs come from config.Extract.OCR. Force re-OCRs docs that already
// have ocr_extractive text; Limit caps the count (0 = all). The KGAT token comes
// from KAGGLE_API_TOKEN in the worker's env.
type OcrAllParams struct {
	Engine      string
	Owner       string
	Accelerator string
	Command     string
	Script      string
	Languages   string
	DPI         int
	BatchSize   int
	Force       bool
	Limit       int
}

// OcrAllResult reports how many scanned docs got OCR text written and how many
// the engine could not read.
type OcrAllResult struct {
	Processed int
	Failed    int
}

// ocrScan is one scanned PDF that needs OCR.
type ocrScan struct {
	documentID  int64
	source      string
	rawFileID   int64
	sha256      string
	storagePath string
}

// ocrOut is the engine-agnostic OCR result for one scan.
type ocrOut struct {
	Text       string
	Confidence float64
	Engine     string
	Err        string
}

// OcrAllWorkflow OCRs every gate-flagged scan in one batch — the twin of
// EmbedAllWorkflow. It runs the single OcrAll activity on the EXTERNAL queue: the
// activity is I/O-bound (it waits minutes on a Kaggle GPU, or grinds CPU OCR), so
// it must not occupy a local CPU slot. Extract defers OCR to this pass; once the
// ocr_extractive text lands, Normalize/Index continue as usual.
func OcrAllWorkflow(ctx workflow.Context, p OcrAllParams) (OcrAllResult, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:           ExternalActivityTaskQueue(workflow.GetInfo(ctx).TaskQueueName),
		StartToCloseTimeout: 3 * time.Hour,
		HeartbeatTimeout:    3 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    10 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    2,
		},
	})

	var a *Activities
	var res OcrAllResult
	if err := workflow.ExecuteActivity(ctx, a.OcrAll, p).Get(ctx, &res); err != nil {
		return OcrAllResult{}, err
	}
	workflow.GetLogger(ctx).Info("ocr-all workflow complete",
		"processed", res.Processed, "failed", res.Failed, "engine", p.Engine)
	return res, nil
}

// OcrAll loads every scan that needs OCR, OCRs them in one batch (a Kaggle GPU job
// or a local CPU pass per OcrAllParams.Engine), and upserts the text as
// authority='ocr_extractive', is_binding=FALSE. OCR text is never the sole source
// of binding legal text. It heartbeats while the engine runs.
func (a *Activities) OcrAll(ctx context.Context, p OcrAllParams) (OcrAllResult, error) {
	log := activity.GetLogger(ctx)

	scans, err := a.loadScansForOCR(ctx, p.Force, p.Limit)
	if err != nil {
		return OcrAllResult{}, err
	}
	if len(scans) == 0 {
		log.Info("ocr-all: no scans need OCR")
		return OcrAllResult{}, nil
	}

	gate, err := a.loadGate(ctx)
	if err != nil {
		log.Warn("ocr-all: failed to load gate config, using defaults", "err", err)
		gate = extract.DefaultGate()
	}

	log.Info("ocr-all: OCR starting", "scans", len(scans), "engine", p.Engine, "force", p.Force)
	now := time.Now().UTC()
	var res OcrAllResult

	// Distinct documents can share an identical scanned PDF (same sha256), so one
	// OCR result fans out to every scan that uses it.
	scansBySha := make(map[string][]ocrScan, len(scans))
	for _, s := range scans {
		scansBySha[s.sha256] = append(scansBySha[s.sha256], s)
	}
	seen := make(map[string]bool, len(scans))

	// persist writes one scan's OCR result to silver.document_text, updating res.
	persist := func(s ocrScan, out ocrOut) error {
		if out.Err != "" || strings.TrimSpace(out.Text) == "" {
			res.Failed++
			reason := "no OCR text"
			if out.Err != "" {
				reason = out.Err
			}
			log.Warn("ocr-all: doc not OCR'd", "document_id", s.documentID, "sha256", s.sha256, "reason", reason)
			return nil
		}

		text := extract.Normalize(out.Text)
		gr := gate.Assess(text)
		sourceUnavailable := extract.OfficialPlaceholder(text)
		needsReview := !gr.OK || sourceUnavailable

		md := &text
		verbatim := sha256Hex(text)
		rawFileID := s.rawFileID
		sha := s.sha256
		engine := out.Engine
		if engine == "" {
			engine = "easyocr"
		}
		if _, err := a.silver.UpsertDocumentText(ctx, dbsilver.UpsertDocumentTextParams{
			DocumentID:        s.documentID,
			Authority:         "ocr_extractive",
			Source:            s.source,
			RawFileID:         &rawFileID,
			Markdown:          md,
			SourceFileSha256:  &sha,
			VerbatimSha256:    &verbatim,
			IsBinding:         false,
			ExtractEngine:     &engine,
			ExtractConfidence: pgtype.Float8{Float64: out.Confidence, Valid: out.Confidence > 0},
			NeedsReview:       needsReview,
			CreatedAt:         now,
		}); err != nil {
			return fmt.Errorf("upsert ocr text doc=%d: %w", s.documentID, err)
		}
		res.Processed++
		log.Info("ocr-all: wrote ocr_extractive", "document_id", s.documentID,
			"chars", len([]rune(text)), "confidence", out.Confidence, "needs_review", needsReview)
		return nil
	}

	// onResult fans one OCR output (keyed by sha256) out to every scan sharing it,
	// persisting each as it streams in so the full OCR text is never all in memory.
	onResult := func(sha string, out ocrOut) error {
		seen[sha] = true
		for _, s := range scansBySha[sha] {
			if err := persist(s, out); err != nil {
				return err
			}
		}
		return nil
	}

	if err := a.runOCR(ctx, p, scans, onResult); err != nil {
		return res, err
	}

	// A scan whose sha produced no output row is a failure.
	for _, s := range scans {
		if !seen[s.sha256] {
			res.Failed++
			log.Warn("ocr-all: doc not OCR'd", "document_id", s.documentID, "sha256", s.sha256, "reason", "no OCR output")
		}
	}

	log.Info("ocr-all: complete", "processed", res.Processed, "failed", res.Failed)
	return res, nil
}

// runOCR dispatches to the Kaggle GPU batch or the local CPU engine, invoking
// onResult once per output (keyed by sha256) as each result becomes available so
// the caller persists incrementally rather than holding the whole batch.
func (a *Activities) runOCR(ctx context.Context, p OcrAllParams, scans []ocrScan, onResult func(sha string, out ocrOut) error) error {
	if p.Engine == "kaggle" {
		return a.runOCRKaggle(ctx, p, scans, onResult)
	}
	return a.runOCRLocal(ctx, p, scans, onResult)
}

// runOCRLocal OCRs each distinct scan (deduped by sha256) on the local CPU via the
// EasyOCR helper, invoking onResult per result and heartbeating every few docs.
func (a *Activities) runOCRLocal(ctx context.Context, p OcrAllParams, scans []ocrScan, onResult func(sha string, out ocrOut) error) error {
	client := extract.NewEasyOCRClient(p.Command, p.Script, p.Languages, p.DPI, p.BatchSize, false)
	done := make(map[string]bool, len(scans))
	n := 0
	for _, s := range scans {
		if done[s.sha256] {
			continue
		}
		done[s.sha256] = true
		n++
		absPath := filepath.Join(a.storageDir, s.storagePath)
		var out ocrOut
		if resp, err := client.OCR(ctx, absPath); err != nil {
			out = ocrOut{Err: err.Error()}
			activity.GetLogger(ctx).Warn("ocr-all local: doc failed", "sha256", s.sha256, "err", err)
		} else {
			out = ocrOut{Text: resp.Text, Confidence: resp.Confidence, Engine: resp.Engine}
		}
		if err := onResult(s.sha256, out); err != nil {
			return err
		}
		if n%5 == 0 {
			activity.RecordHeartbeat(ctx, fmt.Sprintf("ocr %d (local CPU)", n))
		}
	}
	return nil
}

// runOCRKaggle OCRs all distinct scans in one Kaggle GPU job, streaming each result
// to onResult as it is parsed (never holding the whole batch) and heartbeating
// while the job runs.
func (a *Activities) runOCRKaggle(ctx context.Context, p OcrAllParams, scans []ocrScan, onResult func(sha string, out ocrOut) error) error {
	inputs := make([]ocrbatch.Input, 0, len(scans))
	added := make(map[string]bool, len(scans))
	for _, s := range scans {
		if added[s.sha256] {
			continue
		}
		added[s.sha256] = true
		inputs = append(inputs, ocrbatch.Input{Sha256: s.sha256, Path: filepath.Join(a.storageDir, s.storagePath)})
	}
	bo, err := ocrbatch.New(ocrbatch.Options{
		Owner:       p.Owner,
		Accelerator: p.Accelerator,
		Languages:   p.Languages,
		DPI:         p.DPI,
		BatchSize:   p.BatchSize,
		Token:       a.kaggleToken,
	}, nil)
	if err != nil {
		return fmt.Errorf("kaggle ocr: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- bo.OCRStream(ctx, inputs, func(r ocrbatch.Result) error {
			return onResult(r.Sha256, ocrOut{Text: r.Text, Confidence: r.Confidence, Engine: "easyocr", Err: r.Err})
		})
	}()

	ticker := time.NewTicker(ocrHeartbeat)
	defer ticker.Stop()
	for waiting := true; waiting; {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			activity.RecordHeartbeat(ctx, fmt.Sprintf("OCR %d scans on Kaggle", len(inputs)))
		case err := <-done:
			if err != nil {
				return fmt.Errorf("kaggle ocr %d scans: %w", len(inputs), err)
			}
			waiting = false
		}
	}
	return nil
}

// loadScansForOCR finds the scans needing OCR: a silver.document with a PDF
// (main/original_scan) bronze.raw_file, no binding text, and (unless force) no
// ocr_extractive row yet. One PDF per doc, preferring an original_scan.
func (a *Activities) loadScansForOCR(ctx context.Context, force bool, limit int) ([]ocrScan, error) {
	if limit <= 0 {
		limit = 1<<31 - 1
	}
	var sb strings.Builder
	sb.WriteString(`
SELECT DISTINCT ON (d.id) d.id, sd.source, rf.id, COALESCE(rf.sha256, ''), rf.storage_path
FROM silver.document d
JOIN bronze.source_document sd ON sd.id = d.source_document_id
JOIN bronze.raw_file rf ON rf.source_document_id = sd.id
  AND rf.file_format = 'pdf' AND rf.file_kind IN ('main', 'original_scan')
  AND rf.storage_path IS NOT NULL
WHERE NOT EXISTS (
        SELECT 1 FROM silver.document_text t WHERE t.document_id = d.id AND t.is_binding = TRUE)
  AND NOT EXISTS (
        SELECT 1 FROM ingest.fetch_doc fd
        WHERE fd.source = sd.source AND fd.external_id = sd.external_id
          AND fd.content_recheck_reason <> '')`)
	if !force {
		sb.WriteString(`
  AND NOT EXISTS (
        SELECT 1 FROM silver.document_text t WHERE t.document_id = d.id AND t.authority = 'ocr_extractive')`)
	}
	sb.WriteString(`
ORDER BY d.id, CASE rf.file_kind WHEN 'original_scan' THEN 0 ELSE 1 END, rf.ordinal
LIMIT $1`)

	rows, err := a.dbpool.Query(ctx, sb.String(), limit)
	if err != nil {
		return nil, fmt.Errorf("load scans for ocr: %w", err)
	}
	defer rows.Close()

	var out []ocrScan
	for rows.Next() {
		var s ocrScan
		if err := rows.Scan(&s.documentID, &s.source, &s.rawFileID, &s.sha256, &s.storagePath); err != nil {
			return nil, fmt.Errorf("scan ocr row: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scans for ocr: %w", err)
	}
	return out, nil
}
