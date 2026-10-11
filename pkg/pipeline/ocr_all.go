package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"danny.vn/banhmi/pkg/extract"
	"danny.vn/banhmi/pkg/extract/docai"
	fitzext "danny.vn/banhmi/pkg/extract/fitz"
	ocrbatch "danny.vn/banhmi/pkg/rag/ocr/kagglebatch"
	dbsilver "danny.vn/banhmi/pkg/store/silver"
)

// ocrHeartbeat is how often the OcrAll activity heartbeats while it waits on the
// (minutes-long) Kaggle GPU job or grinds local CPU OCR.
const ocrHeartbeat = 30 * time.Second

// OcrAllParams configures one bounded OCR shard over scanned PDFs that the gate
// flagged (born-digital extraction failed or was non-binding). Engine is "kaggle"
// or "local" (resolved by the caller from config.OcrEngine()); the Kaggle fields
// and EasyOCR knobs come from config.Extract.OCR. Force re-OCRs docs that already
// have ocr_extractive text. MaxItems sets the shard cap; a positive Limit can
// lower that cap. The KGAT token comes from KAGGLE_API_TOKEN in the worker's
// environment.
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
	AfterID     int64

	MaxItems      int
	MaxInputBytes int64
	MaxPages      int
	MaxElapsed    time.Duration
	MaxConcurrent int

	// Vision OCR fields (used when Engine == "documentai", the Google sync
	// OCR engine, Vision images:annotate under the hood).
	Concurrency       int // parallel annotate calls (default 8)
	RequestsPerMinute int // Vision rate limit (default 600; quota is 1,800)

	// S3Bucket is the S3 bucket for the OCR text cache. Empty disables caching.
	S3Bucket string
}

// OcrAllResult reports how many scanned docs got OCR text written and how many
// the engine could not read.
type OcrAllResult struct {
	Processed  int
	Failed     int
	Rejected   int
	NextCursor int64
	Complete   bool
}

// OcrAllError reports document failures after all selected pages finish.
type OcrAllError struct {
	Result OcrAllResult
	Cause  error
}

func (e *OcrAllError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("ocr-all failed: processed=%d rejected=%d failed=%d next_cursor=%d: %v",
			e.Result.Processed, e.Result.Rejected, e.Result.Failed, e.Result.NextCursor, e.Cause)
	}
	return fmt.Sprintf("ocr-all failed: processed=%d rejected=%d failed=%d next_cursor=%d",
		e.Result.Processed, e.Result.Rejected, e.Result.Failed, e.Result.NextCursor)
}

func (e *OcrAllError) Unwrap() error { return e.Cause }

func validateOCRBudget(p OcrAllParams) error {
	switch {
	case p.MaxItems <= 0:
		return fmt.Errorf("ocr max items must be positive")
	case p.MaxItems == int(^uint(0)>>1):
		return fmt.Errorf("ocr max items is too large")
	case p.MaxInputBytes <= 0:
		return fmt.Errorf("ocr max input bytes must be positive")
	case p.MaxPages <= 0:
		return fmt.Errorf("ocr max pages must be positive")
	case p.MaxElapsed <= 0:
		return fmt.Errorf("ocr max elapsed must be positive")
	case p.MaxConcurrent <= 0:
		return fmt.Errorf("ocr max concurrent must be positive")
	default:
		return nil
	}
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

// OcrAll loads bounded keyset pages of scans, OCRs one page at a time, and
// upserts the text as authority='ocr_extractive', is_binding=FALSE. OCR text is
// never the sole source of binding legal text. It heartbeats while the engine runs.
func (a *Activities) OcrAll(ctx context.Context, p OcrAllParams) (OcrAllResult, error) {
	if err := validateOCRBudget(p); err != nil {
		return OcrAllResult{}, &OcrAllError{Cause: err}
	}
	ctx, cancel := context.WithTimeout(ctx, p.MaxElapsed)
	defer cancel()
	if p.Concurrency <= 0 || p.Concurrency > p.MaxConcurrent {
		p.Concurrency = p.MaxConcurrent
	}
	log := a.log
	gate, err := a.loadGate(ctx)
	if err != nil {
		log.Warn("ocr-all: failed to load gate config, using defaults", "err", err)
		gate = extract.DefaultGate()
	}
	now := time.Now().UTC()
	itemLimit := p.MaxItems
	if p.Limit > 0 && p.Limit < itemLimit {
		itemLimit = p.Limit
	}
	res, err := processOCRShard(ctx, p.AfterID, itemLimit,
		func(ctx context.Context, afterID int64, limit int) ([]ocrScan, error) {
			return a.loadScansForOCRPage(ctx, p.Force, afterID, limit)
		},
		func(ctx context.Context, scans []ocrScan) (OcrAllResult, error) {
			log.Info("ocr-all: OCR page starting", "scans", len(scans), "engine", p.Engine,
				"force", p.Force, "first_document_id", scans[0].documentID)
			return a.processOCRPage(ctx, p, gate, now, p.AfterID, scans)
		})
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return res, &OcrAllError{Result: res, Cause: err}
	}
	if res.Processed == 0 && res.Failed == 0 && res.Rejected == 0 {
		log.Info("ocr-all: no scans need OCR")
		return res, nil
	}
	log.Info("ocr-all: complete", "processed", res.Processed, "failed", res.Failed)
	if res.Failed > 0 || res.Rejected > 0 {
		return res, &OcrAllError{Result: res}
	}
	return res, nil
}

type loadOCRPageFunc func(ctx context.Context, afterID int64, limit int) ([]ocrScan, error)
type processOCRPageFunc func(ctx context.Context, scans []ocrScan) (OcrAllResult, error)

func processOCRShard(ctx context.Context, afterID int64, itemLimit int, load loadOCRPageFunc, process processOCRPageFunc) (OcrAllResult, error) {
	var result OcrAllResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	scans, err := load(ctx, afterID, itemLimit+1)
	if err != nil {
		return result, err
	}
	if len(scans) == 0 {
		result.Complete = true
		return result, nil
	}
	complete := len(scans) <= itemLimit
	if len(scans) > itemLimit {
		scans = scans[:itemLimit]
	}
	lastID := scans[len(scans)-1].documentID
	if lastID <= afterID {
		return result, fmt.Errorf("ocr page cursor did not advance: after=%d last=%d", afterID, lastID)
	}
	result, err = process(ctx, scans)
	if result.NextCursor == 0 {
		result.NextCursor = lastID
	}
	result.Complete = complete && result.Complete
	if err != nil {
		return result, err
	}
	return result, nil
}

func (a *Activities) processOCRPage(ctx context.Context, p OcrAllParams, gate extract.GateConfig, now time.Time, afterID int64, scans []ocrScan) (OcrAllResult, error) {
	return a.processOCRPageWith(ctx, p, gate, now, afterID, scans,
		a.ocrInputSize, a.ocrInputPages, a.runOCR, a.writeOCRResult)
}

type ocrInputSizeFunc func(context.Context, ocrScan) (int64, error)
type ocrInputPagesFunc func(ocrScan) (int, error)
type ocrRunFunc func(context.Context, OcrAllParams, []ocrScan, func(string, ocrOut) error) error
type ocrWriteFunc func(context.Context, extract.GateConfig, time.Time, ocrScan, ocrOut) error

func (a *Activities) processOCRPageWith(ctx context.Context, p OcrAllParams, gate extract.GateConfig, now time.Time, afterID int64, scans []ocrScan, inputSize ocrInputSizeFunc, inputPages ocrInputPagesFunc, run ocrRunFunc, write ocrWriteFunc) (OcrAllResult, error) {
	var res OcrAllResult
	accepted, excluded, admittedCursor, capacityComplete := preflightOCRPageWith(ctx, p, afterID, scans, inputSize, inputPages)
	res.NextCursor = admittedCursor
	res.Complete = capacityComplete
	admittedScans := make([]ocrScan, 0, len(scans))
	for _, scan := range scans {
		if scan.documentID > admittedCursor {
			break
		}
		admittedScans = append(admittedScans, scan)
	}
	scansBySHA := make(map[string][]ocrScan, len(admittedScans))
	for _, scan := range admittedScans {
		if scan.sha256 == "" {
			continue
		}
		scansBySHA[scan.sha256] = append(scansBySHA[scan.sha256], scan)
	}
	completed := make(map[int64]bool, len(admittedScans))
	for _, scan := range admittedScans {
		failure, ok := excluded[scan.documentID]
		if !ok {
			continue
		}
		completed[scan.documentID] = true
		if failure.rejected {
			res.Rejected++
		} else {
			res.Failed++
		}
		a.log.Warn("ocr-all: input excluded", "document_id", scan.documentID,
			"sha256", scan.sha256, "reason", failure.reason, "rejected", failure.rejected)
	}
	persist := func(scan ocrScan, out ocrOut) error {
		if out.Err != "" || strings.TrimSpace(out.Text) == "" {
			res.Failed++
			completed[scan.documentID] = true
			reason := "no OCR text"
			if out.Err != "" {
				reason = out.Err
			}
			a.log.Warn("ocr-all: doc not OCR'd", "document_id", scan.documentID, "sha256", scan.sha256, "reason", reason)
			return nil
		}
		if err := write(ctx, gate, now, scan, out); err != nil {
			return err
		}
		res.Processed++
		completed[scan.documentID] = true
		a.log.Info("ocr-all: wrote ocr_extractive", "document_id", scan.documentID,
			"chars", len([]rune(out.Text)), "confidence", out.Confidence)
		return nil
	}
	onResult := func(sha string, out ocrOut) error {
		for _, scan := range scansBySHA[sha] {
			if err := persist(scan, out); err != nil {
				return err
			}
		}
		return nil
	}
	runErr := ctx.Err()
	if runErr == nil && len(accepted) > 0 {
		runErr = providerCompletionError(ctx, run(ctx, p, accepted, onResult))
	}
	for _, scan := range unfinishedOCRScans(admittedScans, completed) {
		res.Failed++
		a.log.Warn("ocr-all: doc not OCR'd", "document_id", scan.documentID,
			"sha256", scan.sha256, "reason", "processing stopped before an OCR result was persisted")
	}
	return res, runErr
}

func (a *Activities) writeOCRResult(ctx context.Context, gate extract.GateConfig, now time.Time, scan ocrScan, out ocrOut) error {
	text := extract.Normalize(cleanPDFMarkdownNoise(out.Text))
	assessment := gate.Assess(text)
	needsReview := !assessment.OK || extract.OfficialPlaceholder(text)
	md := &text
	verbatim := sha256Hex(text)
	rawFileID := scan.rawFileID
	sha := scan.sha256
	engine := out.Engine
	if engine == "" {
		engine = "easyocr"
	}
	if _, err := a.silver.UpsertDocumentText(ctx, dbsilver.UpsertDocumentTextParams{
		DocumentID: scan.documentID, Authority: "ocr_extractive", Source: scan.source,
		RawFileID: &rawFileID, Markdown: md, SourceFileSha256: &sha, VerbatimSha256: &verbatim,
		IsBinding: false, ExtractEngine: &engine,
		ExtractConfidence: pgtype.Float8{Float64: out.Confidence, Valid: out.Confidence > 0},
		NeedsReview:       needsReview, CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("upsert ocr text doc=%d: %w", scan.documentID, err)
	}
	return nil
}

func unfinishedOCRScans(scans []ocrScan, completed map[int64]bool) []ocrScan {
	unfinished := make([]ocrScan, 0, len(scans))
	for _, scan := range scans {
		if completed[scan.documentID] {
			continue
		}
		completed[scan.documentID] = true
		unfinished = append(unfinished, scan)
	}
	return unfinished
}

type ocrPreflightFailure struct {
	reason   string
	rejected bool
}

func (a *Activities) preflightOCRPage(ctx context.Context, p OcrAllParams, afterID int64, scans []ocrScan) ([]ocrScan, map[int64]ocrPreflightFailure, int64, bool) {
	return preflightOCRPageWith(ctx, p, afterID, scans, a.ocrInputSize, a.ocrInputPages)
}

func (a *Activities) ocrInputSize(ctx context.Context, scan ocrScan) (int64, error) {
	if err := a.ensureLocalFile(ctx, scan.storagePath); err != nil {
		return 0, err
	}
	path := filepath.Join(a.storageDir, scan.storagePath)
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat input: %w", err)
	}
	return info.Size(), nil
}

func (a *Activities) ocrInputPages(scan ocrScan) (int, error) {
	path := filepath.Join(a.storageDir, scan.storagePath)
	pages, err := fitzext.PageCount(path)
	if err != nil {
		return 0, fmt.Errorf("count pages: %w", err)
	}
	return pages, nil
}

func preflightOCRPageWith(ctx context.Context, p OcrAllParams, afterID int64, scans []ocrScan, inputSize ocrInputSizeFunc, inputPages ocrInputPagesFunc) ([]ocrScan, map[int64]ocrPreflightFailure, int64, bool) {
	accepted := make([]ocrScan, 0, len(scans))
	excluded := make(map[int64]ocrPreflightFailure)
	acceptedSHA := make(map[string]bool, len(scans))
	failedSHA := make(map[string]ocrPreflightFailure, len(scans))
	var totalBytes int64
	totalPages := 0
	cursor := afterID
	for _, scan := range scans {
		if scan.sha256 == "" {
			excluded[scan.documentID] = ocrPreflightFailure{reason: "source file sha256 is missing", rejected: true}
			cursor = scan.documentID
			continue
		}
		if failure, ok := failedSHA[scan.sha256]; ok {
			excluded[scan.documentID] = failure
			cursor = scan.documentID
			continue
		}
		if acceptedSHA[scan.sha256] {
			cursor = scan.documentID
			continue
		}
		if err := ctx.Err(); err != nil {
			failure := ocrPreflightFailure{reason: err.Error()}
			excluded[scan.documentID] = failure
			failedSHA[scan.sha256] = failure
			cursor = scan.documentID
			continue
		}
		inputBytes, err := inputSize(ctx, scan)
		if err != nil {
			failure := ocrPreflightFailure{reason: err.Error()}
			excluded[scan.documentID] = failure
			failedSHA[scan.sha256] = failure
			cursor = scan.documentID
			continue
		}
		decision, reason := checkOCRInputBudget(inputBytes, -1, totalBytes, totalPages, p)
		if decision == ocrBudgetFull {
			return accepted, excluded, cursor, false
		}
		if decision == ocrBudgetReject {
			failure := ocrPreflightFailure{reason: reason, rejected: true}
			excluded[scan.documentID] = failure
			failedSHA[scan.sha256] = failure
			cursor = scan.documentID
			continue
		}
		pages, err := inputPages(scan)
		if err != nil {
			failure := ocrPreflightFailure{reason: err.Error()}
			excluded[scan.documentID] = failure
			failedSHA[scan.sha256] = failure
			cursor = scan.documentID
			continue
		}
		decision, reason = checkOCRInputBudget(inputBytes, pages, totalBytes, totalPages, p)
		if decision == ocrBudgetFull {
			return accepted, excluded, cursor, false
		}
		if decision == ocrBudgetReject {
			failure := ocrPreflightFailure{reason: reason, rejected: true}
			excluded[scan.documentID] = failure
			failedSHA[scan.sha256] = failure
			cursor = scan.documentID
			continue
		}
		totalBytes += inputBytes
		totalPages += pages
		accepted = append(accepted, scan)
		acceptedSHA[scan.sha256] = true
		cursor = scan.documentID
	}
	return accepted, excluded, cursor, true
}

type ocrBudgetDecision int

const (
	ocrBudgetAccept ocrBudgetDecision = iota
	ocrBudgetReject
	ocrBudgetFull
)

func checkOCRInputBudget(inputBytes int64, pages int, usedBytes int64, usedPages int, p OcrAllParams) (ocrBudgetDecision, string) {
	if inputBytes <= 0 || inputBytes > p.MaxInputBytes {
		return ocrBudgetReject, fmt.Sprintf("input bytes %d exceed shard budget %d", inputBytes, p.MaxInputBytes)
	}
	if inputBytes > p.MaxInputBytes-usedBytes {
		return ocrBudgetFull, "input byte budget is full"
	}
	if pages >= 0 && (pages == 0 || pages > p.MaxPages) {
		return ocrBudgetReject, fmt.Sprintf("page count %d exceeds shard budget %d", pages, p.MaxPages)
	}
	if pages > p.MaxPages-usedPages {
		return ocrBudgetFull, "page budget is full"
	}
	return ocrBudgetAccept, ""
}

// runOCR dispatches through the legacy synchronous, unfenced, nonresumable path.
// It invokes onResult once per output so the caller persists incrementally.
func (a *Activities) runOCR(ctx context.Context, p OcrAllParams, scans []ocrScan, onResult func(sha string, out ocrOut) error) error {
	switch p.Engine {
	case "kaggle":
		return a.runOCRKaggle(ctx, p, scans, onResult)
	case "documentai":
		return a.runOCRDocumentAI(ctx, p, scans, onResult)
	default:
		return a.runOCRLocal(ctx, p, scans, onResult)
	}
}

// runOCRLocal OCRs each distinct scan (deduped by sha256) on the local CPU via the
// EasyOCR helper, invoking onResult per result and heartbeating every few docs.
func (a *Activities) runOCRLocal(ctx context.Context, p OcrAllParams, scans []ocrScan, onResult func(sha string, out ocrOut) error) error {
	client := extract.NewEasyOCRClient(p.Command, p.Script, p.Languages, p.DPI, p.BatchSize, false)
	n := 0
	for _, s := range uniqueOCRScans(scans) {
		if err := ctx.Err(); err != nil {
			return err
		}
		n++
		if err := a.ensureLocalFile(ctx, s.storagePath); err != nil {
			if oerr := onResult(s.sha256, ocrOut{Err: err.Error()}); oerr != nil {
				return oerr
			}
			a.log.Warn("ocr-all local: ensure file failed", "sha256", s.sha256, "err", err)
			continue
		}
		absPath := filepath.Join(a.storageDir, s.storagePath)
		var out ocrOut
		if resp, err := client.OCR(ctx, absPath); err != nil {
			out = ocrOut{Err: err.Error()}
			a.log.Warn("ocr-all local: doc failed", "sha256", s.sha256, "err", err)
		} else {
			out = ocrOut{Text: resp.Text, Confidence: resp.Confidence, Engine: resp.Engine}
		}
		if err := onResult(s.sha256, out); err != nil {
			return err
		}
		if n%5 == 0 {
			a.log.Info(fmt.Sprintf("ocr %d (local CPU)", n))
		}
	}
	return nil
}

// runOCRKaggle OCRs all distinct scans in one Kaggle GPU job, streaming each result
// to onResult as it is parsed (never holding the whole batch) and heartbeating
// while the job runs.
func (a *Activities) runOCRKaggle(ctx context.Context, p OcrAllParams, scans []ocrScan, onResult func(sha string, out ocrOut) error) error {
	unique := uniqueOCRScans(scans)
	inputs := make([]ocrbatch.Input, 0, len(unique))
	for _, s := range unique {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.ensureLocalFile(ctx, s.storagePath); err != nil {
			a.log.Warn("ocr-all kaggle: ensure file failed, skipping", "sha256", s.sha256, "err", err)
			continue
		}
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
	return consumeOCRKaggleStream(ctx, inputs, bo.OCRStream, onResult, func() {
		a.log.Info(fmt.Sprintf("OCR %d scans on Kaggle", len(inputs)))
	})
}

type ocrKaggleStreamFunc func(context.Context, []ocrbatch.Input, func(ocrbatch.Result) error) error

type ocrKaggleDelivery struct {
	result ocrbatch.Result
	ack    chan error
}

// consumeOCRKaggleStream owns all calls into onResult. The provider goroutine
// only sends values through results, so cancellation cannot leave a callback
// mutating shard state after this function returns.
func consumeOCRKaggleStream(ctx context.Context, inputs []ocrbatch.Input, stream ocrKaggleStreamFunc, onResult func(string, ocrOut) error, heartbeat func()) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan ocrKaggleDelivery)
	done := make(chan error, 1)
	go func() {
		done <- stream(runCtx, inputs, func(r ocrbatch.Result) error {
			delivery := ocrKaggleDelivery{result: r, ack: make(chan error, 1)}
			select {
			case results <- delivery:
			case <-runCtx.Done():
				return runCtx.Err()
			}
			select {
			case err := <-delivery.ack:
				return err
			case <-runCtx.Done():
				// The owner sends the callback result before returning. Prefer that
				// result when cancellation and acknowledgement become ready together.
				select {
				case err := <-delivery.ack:
					return err
				default:
					return runCtx.Err()
				}
			}
		})
	}()

	ticker := time.NewTicker(ocrHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			heartbeat()
		case delivery := <-results:
			resultErr := onResult(delivery.result.Sha256, ocrOut{
				Text: delivery.result.Text, Confidence: delivery.result.Confidence,
				Engine: "easyocr", Err: delivery.result.Err,
			})
			delivery.ack <- resultErr
			if resultErr != nil {
				return resultErr
			}
		case err := <-done:
			if err != nil {
				return fmt.Errorf("kaggle ocr %d scans: %w", len(inputs), err)
			}
			return nil
		}
	}
}

// runOCRDocumentAI OCRs all distinct scans via synchronous Document AI
// ProcessDocument calls with client-side parallelism, heartbeating while the
// worker pool runs. S3 cache means re-runs cost nothing.
func (a *Activities) runOCRDocumentAI(ctx context.Context, p OcrAllParams, scans []ocrScan, onResult func(sha string, out ocrOut) error) error {
	var langHints []string
	for _, l := range strings.Split(p.Languages, ",") {
		if l = strings.TrimSpace(l); l != "" {
			langHints = append(langHints, l)
		}
	}

	// OCR text cache: local files under storageDir/ocr/, S3 mirror when a
	// bucket is configured (same shape as the fetched-file cache).
	cache, err := BuildOCRCache(ctx, p.S3Bucket, a.storageDir, a.log)
	if err != nil {
		a.log.Warn("ocr-all documentai: cache init failed, proceeding without cache", "err", err)
		cache = nil
	}

	var opts []docai.Option
	if p.DPI > 0 {
		opts = append(opts, docai.WithDPI(p.DPI))
	}
	if p.Concurrency > 0 {
		opts = append(opts, docai.WithConcurrency(p.Concurrency))
	}
	if p.RequestsPerMinute > 0 {
		opts = append(opts, docai.WithRequestsPerMinute(p.RequestsPerMinute))
	}

	client, err := docai.New(cache, langHints, a.log, opts...)
	if err != nil {
		return fmt.Errorf("vision ocr client: %w", err)
	}
	defer func() { _ = client.Close() }()

	// Dedup within this bounded page. The OCR cache handles repeated hashes on
	// later pages without retaining a corpus-wide set.
	unique := uniqueOCRScans(scans)
	inputs := make([]docai.OCRInput, 0, len(unique))
	for _, s := range unique {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.ensureLocalFile(ctx, s.storagePath); err != nil {
			a.log.Warn("ocr-all documentai: ensure file failed, skipping", "sha256", s.sha256, "err", err)
			continue
		}
		inputs = append(inputs, docai.OCRInput{
			Sha256:    s.sha256,
			LocalPath: filepath.Join(a.storageDir, s.storagePath),
		})
	}

	// Run the batch in a goroutine with a heartbeat ticker.
	type batchResult struct {
		texts map[string]string
		err   error
	}
	ch := make(chan batchResult, 1)
	go func() {
		texts, err := client.OCRBatch(ctx, inputs)
		ch <- batchResult{texts, err}
	}()

	ticker := time.NewTicker(ocrHeartbeat)
	defer ticker.Stop()
	var texts map[string]string
	var providerErr error
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			a.log.Info(fmt.Sprintf("ocr %d docs (documentai sync)", len(inputs)))
		case res := <-ch:
			if res.err != nil {
				a.log.Warn("ocr-all documentai: batch failed", "err", res.err)
			}
			texts = res.texts
			providerErr = res.err
			goto done
		}
	}
done:
	// Fan out results to onResult per sha256.
	for _, in := range inputs {
		text := texts[in.Sha256]
		var out ocrOut
		if text == "" {
			out = ocrOut{Err: "no OCR output"}
		} else {
			out = ocrOut{Text: text, Confidence: 1.0, Engine: "vision-ocr/builtin-latest"}
		}
		if err := onResult(in.Sha256, out); err != nil {
			return err
		}
	}
	return providerErr
}

func uniqueOCRScans(scans []ocrScan) []ocrScan {
	seen := make(map[string]struct{}, len(scans))
	unique := make([]ocrScan, 0, len(scans))
	for _, scan := range scans {
		if _, ok := seen[scan.sha256]; ok {
			continue
		}
		seen[scan.sha256] = struct{}{}
		unique = append(unique, scan)
	}
	return unique
}

// loadScansForOCRPage finds one keyset page of scans needing OCR: a silver.document with a PDF
// (main/original_scan) bronze.raw_file, no binding text, and (unless force) no
// ocr_extractive row yet. One PDF per doc, preferring an original_scan.
func (a *Activities) loadScansForOCRPage(ctx context.Context, force bool, afterID int64, limit int) ([]ocrScan, error) {
	rows, err := a.dbpool.Query(ctx, ocrScanQuery(force), afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("load scans for ocr after %d: %w", afterID, err)
	}
	defer rows.Close()

	out := make([]ocrScan, 0, limit)
	for rows.Next() {
		var scan ocrScan
		if err := rows.Scan(&scan.documentID, &scan.source, &scan.rawFileID, &scan.sha256, &scan.storagePath); err != nil {
			return nil, fmt.Errorf("scan ocr row: %w", err)
		}
		out = append(out, scan)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scans for ocr: %w", err)
	}
	return out, nil
}

func ocrScanQuery(force bool) string {
	// The PDF is looked up across ALL of the document's source observations
	// (via document_alias), not just source_document_id: cross-source dedup can
	// leave the primary observation without a file while another source (e.g.
	// bpk) holds the gazette scan — UU 4/2023 sat unselected this way.
	var sb strings.Builder
	sb.WriteString(`
SELECT DISTINCT ON (d.id) d.id, sd.source, rf.id, COALESCE(rf.sha256, ''), rf.storage_path
FROM silver.document d
JOIN silver.document_alias da ON da.document_id = d.id
JOIN bronze.source_document sd ON sd.source = da.source AND sd.external_id = da.external_id
JOIN bronze.raw_file rf ON rf.source_document_id = sd.id
  AND rf.file_format = 'pdf' AND rf.file_kind IN ('main', 'original_scan')
  AND rf.storage_path IS NOT NULL
WHERE NOT EXISTS (
        SELECT 1 FROM silver.document_text t WHERE t.document_id = d.id AND t.is_binding = TRUE)
	  AND d.id > $1
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
	LIMIT $2`)
	return sb.String()
}
