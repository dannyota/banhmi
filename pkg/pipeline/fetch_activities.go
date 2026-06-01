package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"go.temporal.io/sdk/activity"

	"danny.vn/banhmi/pkg/ingest"
	dbbronze "danny.vn/banhmi/pkg/store/bronze"
	dbingest "danny.vn/banhmi/pkg/store/ingest"
)

// leaseDuration is how long a claimed artifact is reserved for one run before its
// lease expires and another run may re-claim it (crash recovery).
const leaseDuration = 15 * time.Minute

const (
	maxTreeRechecks  = 5
	treeRecheckDelay = 24 * time.Hour
)

// ClaimArtifacts leases up to p.Limit due artifacts (pending, or error past their
// backoff) with FOR UPDATE SKIP LOCKED, returning the workflow-facing view.
func (a *Activities) ClaimArtifacts(ctx context.Context, p ClaimParams) ([]ClaimedArtifact, error) {
	if strings.TrimSpace(p.Source) == "" {
		return nil, fmt.Errorf("claim artifacts: empty source")
	}
	now := time.Now().UTC()
	owner := activity.GetInfo(ctx).WorkflowExecution.RunID
	expires := now.Add(leaseDuration)
	rows, err := a.ledger.ClaimArtifacts(ctx, dbingest.ClaimArtifactsParams{
		LeaseOwner:     &owner,
		LeaseExpiresAt: &expires,
		Now:            now,
		Source:         p.Source,
		ClaimLimit:     int32(p.Limit),
	})
	if err != nil {
		return nil, fmt.Errorf("claim artifacts: %w", err)
	}
	out := make([]ClaimedArtifact, 0, len(rows))
	for _, r := range rows {
		ca := ClaimedArtifact{
			ID:         r.ID,
			FetchDocID: r.FetchDocID,
			Kind:       r.Kind,
			RefKey:     r.RefKey,
			FileKind:   r.FileKind,
			FileName:   r.FileName,
		}
		if r.Url != nil {
			ca.URL = *r.Url
		}
		out = append(out, ca)
	}
	return out, nil
}

// PlanBody processes a `body` artifact: fetch the document's detail page, upsert
// the bronze source_document, enqueue one `file` artifact per downloadable file,
// mark the document plan-ready, and finalize completeness. A transient detail
// failure is recorded in the ledger (backoff/dead-letter) rather than failing the
// activity, so a later run retries. Returns the document's resulting state.
func (a *Activities) PlanBody(ctx context.Context, art ClaimedArtifact) (string, error) {
	log := activity.GetLogger(ctx)
	doc, err := a.ledger.GetFetchDocByID(ctx, art.FetchDocID)
	if err != nil {
		return "", fmt.Errorf("get fetch_doc %d: %w", art.FetchDocID, err)
	}
	src, ok := a.sources[doc.Source]
	if !ok {
		return "", fmt.Errorf("plan: unknown source %q", doc.Source)
	}
	now := time.Now().UTC()

	detailURL := art.URL
	if detailURL == "" && doc.DetailUrl != nil {
		detailURL = *doc.DetailUrl
	}
	detail, err := src.FetchDetail(ctx, ingest.DetailRef{
		ExternalID: doc.ExternalID,
		DetailURL:  detailURL,
	})
	if err != nil {
		a.failArtifact(ctx, art.ID, fmt.Errorf("fetch detail: %w", err), now)
		log.Warn("plan: fetch detail failed", "doc", doc.ExternalID, "err", err)
		return a.finalizeDoc(ctx, doc.ID, now)
	}

	sdID, err := a.bronze.UpsertSourceDocument(ctx, bronzeSourceParams(doc, detail, now))
	if err != nil {
		return "", fmt.Errorf("upsert source_document %s: %w", doc.ExternalID, err)
	}
	if err := a.enrichSourceDocument(ctx, doc.Source, doc.ExternalID, detail, now); err != nil {
		return "", fmt.Errorf("enrich source_document %s: %w", doc.ExternalID, err)
	}

	// Fetch preserves every official source file that the source client selects
	// (DOCX/PDF). Extraction chooses the best text tier later (DOCX -> HTML ->
	// PDF/OCR), but Bronze keeps the raw source evidence.
	expected := int32(1) // the body artifact itself
	htmlBody := usableHTMLPayload(detail.HTML)
	if htmlBody {
		hash := sha256Hex(detail.HTML)
		if _, err := a.bronze.UpsertRawPayload(ctx, dbbronze.UpsertRawPayloadParams{
			SourceDocumentID: sdID,
			Kind:             "content_html",
			Content:          &detail.HTML,
			ContentHash:      &hash,
			CollectedAt:      now,
		}); err != nil {
			return "", fmt.Errorf("upsert content_html %s: %w", doc.ExternalID, err)
		}
	} else {
		if err := a.bronze.DeleteRawPayloadByKind(ctx, dbbronze.DeleteRawPayloadByKindParams{
			SourceDocumentID: sdID,
			Kind:             "content_html",
		}); err != nil {
			return "", fmt.Errorf("delete stale content_html %s: %w", doc.ExternalID, err)
		}
	}
	if doc.Source == "vbpl" || len(detail.Relations) > 0 {
		relations := detail.Relations
		if relations == nil {
			relations = []ingest.Relation{}
		}
		raw, err := json.Marshal(relations)
		if err != nil {
			return "", fmt.Errorf("marshal references %s: %w", doc.ExternalID, err)
		}
		content := string(raw)
		hash := sha256Hex(content)
		if _, err := a.bronze.UpsertRawPayload(ctx, dbbronze.UpsertRawPayloadParams{
			SourceDocumentID: sdID,
			Kind:             "references_json",
			Content:          &content,
			ContentHash:      &hash,
			CollectedAt:      now,
		}); err != nil {
			return "", fmt.Errorf("upsert references_json %s: %w", doc.ExternalID, err)
		}
	}

	// Preserve the verbatim source detail metadata (minus the inline HTML body) so
	// fields not yet mapped to typed columns can be mined later without re-crawling.
	if len(detail.RawMeta) > 0 {
		content := string(detail.RawMeta)
		hash := sha256Hex(content)
		if _, err := a.bronze.UpsertRawPayload(ctx, dbbronze.UpsertRawPayloadParams{
			SourceDocumentID: sdID,
			Kind:             "detail_json",
			Content:          &content,
			ContentHash:      &hash,
			CollectedAt:      now,
		}); err != nil {
			return "", fmt.Errorf("upsert detail_json %s: %w", doc.ExternalID, err)
		}
	}

	if _, ok := src.(ingest.TreeProvider); ok {
		if _, err := a.ledger.EnqueueArtifact(ctx, dbingest.EnqueueArtifactParams{
			FetchDocID:  doc.ID,
			Kind:        "tree",
			RefKey:      "provision_tree",
			MaxAttempts: 5,
			CreatedAt:   now,
		}); err != nil {
			return "", fmt.Errorf("enqueue tree artifact: %w", err)
		}
		expected++
	}

	hasDocx := false
	activeFileRefs := make([]string, 0, len(detail.Files))
	for i, f := range detail.Files {
		if f.Ext == "docx" {
			hasDocx = true
		}
		refKey := fileRefKey(i, f)
		activeFileRefs = append(activeFileRefs, refKey)
		fileKind := fileKindForRef(f)
		ordinal, fileExt := parseFileRefKey(refKey)
		fileName := strings.TrimSpace(f.Name)
		if _, err := a.ledger.EnqueueArtifact(ctx, dbingest.EnqueueArtifactParams{
			FetchDocID:  doc.ID,
			Kind:        "file",
			RefKey:      refKey,
			FileKind:    fileKind,
			FileName:    fileName,
			Url:         strPtr(f.URL),
			MaxAttempts: 5,
			CreatedAt:   now,
		}); err != nil {
			return "", fmt.Errorf("enqueue file artifact: %w", err)
		}
		if fileName != "" {
			if err := a.bronze.UpdateRawFileLabel(ctx, dbbronze.UpdateRawFileLabelParams{
				SourceDocumentID: sdID,
				FileKind:         fileKind,
				Ordinal:          int32(ordinal),
				FileFormat:       fileExt,
				Label:            fileName,
			}); err != nil {
				return "", fmt.Errorf("update raw_file label: %w", err)
			}
		}
	}
	if err := a.ledger.SupersedeMissingFileArtifacts(ctx, dbingest.SupersedeMissingFileArtifactsParams{
		FetchDocID:    doc.ID,
		ActiveRefKeys: activeFileRefs,
		UpdatedAt:     now,
	}); err != nil {
		return "", fmt.Errorf("supersede missing file artifacts: %w", err)
	}
	files := len(detail.Files)
	expected += int32(files)

	if err := a.ledger.SetDocPlanReady(ctx, dbingest.SetDocPlanReadyParams{
		ID:                doc.ID,
		ArtifactsExpected: expected,
		UpdatedAt:         now,
	}); err != nil {
		return "", fmt.Errorf("set plan ready %d: %w", doc.ID, err)
	}
	if err := a.ledger.MarkArtifactDone(ctx, dbingest.MarkArtifactDoneParams{ID: art.ID, UpdatedAt: now}); err != nil {
		return "", fmt.Errorf("mark body done: %w", err)
	}

	state, err := a.finalizeDoc(ctx, doc.ID, now)
	if err != nil {
		return "", err
	}
	log.Info("planned document", "doc", doc.ExternalID, "html_body", htmlBody, "docx", hasDocx, "files", files, "state", state)
	return state, nil
}

// FetchTree processes a `tree` artifact: fetch the source's official provision
// tree and store it as bronze.raw_payload(provision_tree_json). Missing/empty
// trees are terminal skips for completeness, with a bounded delayed re-check for
// eventually-consistent VBPL rows.
func (a *Activities) FetchTree(ctx context.Context, art ClaimedArtifact) (string, error) {
	log := activity.GetLogger(ctx)
	doc, err := a.ledger.GetFetchDocByID(ctx, art.FetchDocID)
	if err != nil {
		return "", fmt.Errorf("get fetch_doc %d: %w", art.FetchDocID, err)
	}
	src, ok := a.sources[doc.Source]
	if !ok {
		return "", fmt.Errorf("fetch tree: unknown source %q", doc.Source)
	}
	provider, ok := src.(ingest.TreeProvider)
	now := time.Now().UTC()
	if !ok {
		if err := a.ledger.MarkArtifactSkipped(ctx, dbingest.MarkArtifactSkippedParams{ID: art.ID, UpdatedAt: now}); err != nil {
			return "", fmt.Errorf("skip unsupported tree artifact: %w", err)
		}
		return a.finalizeDoc(ctx, doc.ID, now)
	}

	sd, err := a.bronze.SourceDocumentByExternalID(ctx, dbbronze.SourceDocumentByExternalIDParams{
		Source: doc.Source, ExternalID: doc.ExternalID,
	})
	if err != nil {
		a.failArtifact(ctx, art.ID, fmt.Errorf("source_document missing: %w", err), now)
		return a.finalizeDoc(ctx, doc.ID, now)
	}

	detailURL := art.URL
	if detailURL == "" && doc.DetailUrl != nil {
		detailURL = *doc.DetailUrl
	}
	content, treeOK, err := provider.FetchTree(ctx, ingest.DetailRef{
		ExternalID: doc.ExternalID,
		DetailURL:  detailURL,
	})
	if err != nil {
		if err := a.ledger.MarkArtifactSkipped(ctx, dbingest.MarkArtifactSkippedParams{ID: art.ID, UpdatedAt: now}); err != nil {
			return "", fmt.Errorf("mark failed tree skipped: %w", err)
		}
		a.scheduleTreeRecheckBestEffort(ctx, doc, now)
		log.Warn("tree fetch failed; using text fallback", "doc", doc.ExternalID, "err", err)
		return a.finalizeDoc(ctx, doc.ID, now)
	}
	if !treeOK {
		if err := a.ledger.MarkArtifactSkipped(ctx, dbingest.MarkArtifactSkippedParams{ID: art.ID, UpdatedAt: now}); err != nil {
			return "", fmt.Errorf("mark empty tree skipped: %w", err)
		}
		a.scheduleTreeRecheckBestEffort(ctx, doc, now)
		state, err := a.finalizeDoc(ctx, doc.ID, now)
		if err != nil {
			return "", err
		}
		log.Info("tree unavailable; using text fallback", "doc", doc.ExternalID, "state", state)
		return state, nil
	}

	hash := sha256Hex(content)
	if _, err := a.bronze.UpsertRawPayload(ctx, dbbronze.UpsertRawPayloadParams{
		SourceDocumentID: sd.ID,
		Kind:             "provision_tree_json",
		Content:          &content,
		ContentHash:      &hash,
		CollectedAt:      now,
	}); err != nil {
		return "", fmt.Errorf("upsert provision_tree_json %s: %w", doc.ExternalID, err)
	}
	resultRef := "raw_payload:provision_tree_json"
	if err := a.ledger.MarkArtifactDone(ctx, dbingest.MarkArtifactDoneParams{
		ID: art.ID, ContentHash: &hash, ResultRef: &resultRef, UpdatedAt: now,
	}); err != nil {
		return "", fmt.Errorf("mark tree done: %w", err)
	}
	if err := a.ledger.ClearTreeRecheck(ctx, dbingest.ClearTreeRecheckParams{ID: doc.ID, UpdatedAt: now}); err != nil {
		return "", fmt.Errorf("clear tree recheck: %w", err)
	}

	state, err := a.finalizeDoc(ctx, doc.ID, now)
	if err != nil {
		return "", err
	}
	log.Info("fetched provision tree", "doc", doc.ExternalID, "sha256", hash, "state", state)
	return state, nil
}

func (a *Activities) scheduleTreeRecheckBestEffort(ctx context.Context, doc dbingest.IngestFetchDoc, now time.Time) {
	if doc.TreeRecheckCount >= maxTreeRechecks {
		if err := a.ledger.ClearTreeRecheck(ctx, dbingest.ClearTreeRecheckParams{ID: doc.ID, UpdatedAt: now}); err != nil {
			activity.GetLogger(ctx).Warn("clear exhausted tree recheck failed", "doc", doc.ExternalID, "err", err)
		}
		return
	}
	next := now.Add(treeRecheckDelay)
	if err := a.ledger.ScheduleTreeRecheck(ctx, dbingest.ScheduleTreeRecheckParams{
		ID: doc.ID, TreeRecheckAfter: &next, UpdatedAt: now,
	}); err != nil {
		activity.GetLogger(ctx).Warn("schedule tree recheck failed", "doc", doc.ExternalID, "err", err)
	}
}

// FetchFile processes a `file` artifact: download it into the raw-file store,
// record it in bronze.raw_file (linked to the source_document by the shared
// natural key), mark the artifact done, and finalize completeness. A transient
// download failure is recorded in the ledger, not failed as an activity. Returns
// the document's resulting state.
func (a *Activities) FetchFile(ctx context.Context, art ClaimedArtifact) (string, error) {
	log := activity.GetLogger(ctx)
	doc, err := a.ledger.GetFetchDocByID(ctx, art.FetchDocID)
	if err != nil {
		return "", fmt.Errorf("get fetch_doc %d: %w", art.FetchDocID, err)
	}
	src, ok := a.sources[doc.Source]
	if !ok {
		return "", fmt.Errorf("fetch: unknown source %q", doc.Source)
	}
	now := time.Now().UTC()

	sd, err := a.bronze.SourceDocumentByExternalID(ctx, dbbronze.SourceDocumentByExternalIDParams{
		Source: doc.Source, ExternalID: doc.ExternalID,
	})
	if err != nil {
		// The body must be planned (bronze row present) before its files; if not
		// yet, record the error and let a later run retry once the body lands.
		a.failArtifact(ctx, art.ID, fmt.Errorf("source_document missing: %w", err), now)
		return a.finalizeDoc(ctx, doc.ID, now)
	}

	ordinal, ext := parseFileRefKey(art.RefKey)
	fileLabel := fileNameForArtifact(art)
	ref := ingest.FileRef{URL: art.URL, Name: fileLabel, Ext: ext}
	fileKind := strings.TrimSpace(art.FileKind)
	if fileKind == "" {
		fileKind = "main"
	}
	path, sha, size, err := a.storeFile(ctx, src, ref)
	if err != nil {
		safeErr := redactURLQuery(err)
		a.failArtifact(ctx, art.ID, fmt.Errorf("download %s %s: %s", fileKind, art.RefKey, safeErr), now)
		log.Warn("file download failed",
			"doc", doc.ExternalID,
			"ref_key", art.RefKey,
			"file_kind", fileKind,
			"format", ext,
			"err", safeErr)
		return a.finalizeDoc(ctx, doc.ID, now)
	}

	if _, err := a.bronze.UpsertRawFile(ctx, dbbronze.UpsertRawFileParams{
		SourceDocumentID: sd.ID,
		FileKind:         fileKind,
		FileFormat:       ext,
		IsAuthoritative:  isAuthoritativeFile(doc.Source),
		Ordinal:          int32(ordinal),
		Label:            fileLabel,
		Url:              strPtr(art.URL),
		StoragePath:      &path,
		Sha256:           &sha,
		ByteSize:         &size,
		ContentHash:      &sha,
		CollectedAt:      now,
	}); err != nil {
		return "", fmt.Errorf("upsert raw_file: %w", err)
	}
	if err := a.ledger.MarkArtifactDone(ctx, dbingest.MarkArtifactDoneParams{
		ID: art.ID, ContentHash: &sha, ResultRef: &path, UpdatedAt: now,
	}); err != nil {
		return "", fmt.Errorf("mark file done: %w", err)
	}

	state, err := a.finalizeDoc(ctx, doc.ID, now)
	if err != nil {
		return "", err
	}
	log.Info("fetched file", "doc", doc.ExternalID, "bytes", size, "sha256", sha, "state", state)
	return state, nil
}

// finalizeDoc recomputes a document's completeness from its child artifacts and
// returns the resulting state. A doc that is not yet plan-ready yields "fetching"
// (the completeness CAS applies only once all artifacts are enumerated).
func (a *Activities) finalizeDoc(ctx context.Context, docID int64, now time.Time) (string, error) {
	state, err := a.ledger.MarkDocCompleteIfDone(ctx, dbingest.MarkDocCompleteIfDoneParams{Now: now, FetchDocID: docID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "fetching", nil
	}
	if err != nil {
		return "", fmt.Errorf("complete-if-done %d: %w", docID, err)
	}
	return state, nil
}

// failArtifact records a transient failure: MarkArtifactError applies backoff and
// dead-letters once attempts reach max_attempts. A ledger write failure here is
// logged, not returned, because the caller is already on a failure path.
func (a *Activities) failArtifact(ctx context.Context, id int64, cause error, now time.Time) {
	msg := cause.Error()
	next := now.Add(5 * time.Minute)
	if err := a.ledger.MarkArtifactError(ctx, dbingest.MarkArtifactErrorParams{
		ID: id, LastError: &msg, NextAttemptAt: &next, UpdatedAt: now,
	}); err != nil {
		activity.GetLogger(ctx).Error("mark artifact error failed", "id", id, "err", err)
	}
}

// storeFile downloads ref into the raw-file store, content-addressing the result
// as {sha256}.{ext}. It heartbeats while downloading so a long transfer is not
// mistaken for a stuck worker. Returns the stored relative name, digest, and size.
func (a *Activities) storeFile(ctx context.Context, src ingest.Source, ref ingest.FileRef) (string, string, int64, error) {
	if err := os.MkdirAll(a.storageDir, 0o755); err != nil {
		return "", "", 0, fmt.Errorf("create storage dir: %w", err)
	}
	if err := os.Chmod(a.storageDir, 0o755); err != nil {
		return "", "", 0, fmt.Errorf("chmod storage dir: %w", err)
	}
	tmp, err := os.CreateTemp(a.storageDir, "dl-*")
	if err != nil {
		return "", "", 0, fmt.Errorf("temp file: %w", err)
	}
	tmpName := tmp.Name()

	stop := heartbeat(ctx)
	n, sha, derr := src.Download(ctx, ref, tmp)
	stop()
	cherr := tmp.Chmod(0o644)
	cerr := tmp.Close()
	if derr != nil {
		_ = os.Remove(tmpName)
		return "", "", 0, derr
	}
	if cherr != nil {
		_ = os.Remove(tmpName)
		return "", "", 0, fmt.Errorf("chmod temp: %w", cherr)
	}
	if cerr != nil {
		_ = os.Remove(tmpName)
		return "", "", 0, fmt.Errorf("close temp: %w", cerr)
	}

	name := sha
	if ref.Ext != "" {
		name += "." + ref.Ext
	}
	if err := os.Rename(tmpName, filepath.Join(a.storageDir, name)); err != nil {
		_ = os.Remove(tmpName)
		return "", "", 0, fmt.Errorf("rename: %w", err)
	}
	return name, sha, n, nil
}

// heartbeat records an activity heartbeat every 5s until the returned stop is called.
func heartbeat(ctx context.Context) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				activity.RecordHeartbeat(ctx)
			}
		}
	}()
	return func() { close(done) }
}

// fileRefKey is the ref_key for the i-th downloadable file: "{ordinal}.{ext}". It
// discriminates sibling files and carries the format Fetch needs to store them.
func fileRefKey(i int, f ingest.FileRef) string {
	ext := f.Ext
	if ext == "" {
		ext = "bin"
	}
	return strconv.Itoa(i) + "." + ext
}

// parseFileRefKey is the inverse of fileRefKey.
func parseFileRefKey(rk string) (ordinal int, ext string) {
	if i := strings.LastIndex(rk, "."); i >= 0 {
		ordinal, _ = strconv.Atoi(rk[:i])
		return ordinal, rk[i+1:]
	}
	ordinal, _ = strconv.Atoi(rk)
	return ordinal, ""
}

func fileNameForArtifact(art ClaimedArtifact) string {
	if name := strings.TrimSpace(art.FileName); name != "" {
		return name
	}
	return art.RefKey
}

func fileKindForRef(ref ingest.FileRef) string {
	if k := strings.TrimSpace(ref.Kind); k != "" {
		return k
	}
	return "main"
}

func isAuthoritativeFile(source string) bool {
	return source == "congbao" || source == "vbpl" || source == "sbv_hanoi"
}

func (a *Activities) enrichSourceDocument(ctx context.Context, source, externalID string, d *ingest.DiscoveredDoc, now time.Time) error {
	return a.bronze.EnrichSourceDocument(ctx, dbbronze.EnrichSourceDocumentParams{
		Source:         source,
		ExternalID:     externalID,
		DocGuid:        strings.TrimSpace(d.DocGUID),
		DocNumberNorm:  normalizeDocNumberForStorage(d.Number),
		DocTypeCode:    strings.TrimSpace(d.DocTypeCode),
		IssuerCode:     strings.TrimSpace(d.IssuerCode),
		ExpireAt:       timePtr(d.ExpireAt),
		GazetteNumber:  strings.TrimSpace(d.GazetteNumber),
		GazetteDate:    timePtr(d.GazetteDate),
		HasContent:     d.HasContent || strings.TrimSpace(d.HTML) != "" || len(d.Files) > 0,
		IsConsolidated: d.IsConsolidated || isConsolidatedDoc(d),
		CollectedAt:    now,
	})
}

func normalizeDocNumberForStorage(number string) string {
	number = strings.ToUpper(strings.TrimSpace(number))
	var b strings.Builder
	for _, r := range number {
		if r == 'Đ' {
			r = 'D'
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isConsolidatedDoc(d *ingest.DiscoveredDoc) bool {
	hay := strings.ToUpper(strings.TrimSpace(d.Number + " " + string(d.DocType) + " " + d.Title))
	return strings.Contains(hay, "VBHN") || strings.Contains(hay, "VĂN BẢN HỢP NHẤT")
}

var urlWithQueryRe = regexp.MustCompile(`https?://[^\s"']+\?[^\s"']+`)

// redactURLQuery removes signed query strings from errors before they are logged
// or written to fetch_artifact.last_error. vbpl file URLs are public-source
// presigned URLs, but their X-Amz-* query values are temporary bearer tokens.
func redactURLQuery(err error) string {
	if err == nil {
		return ""
	}
	return urlWithQueryRe.ReplaceAllStringFunc(err.Error(), func(s string) string {
		if i := strings.IndexByte(s, '?'); i >= 0 {
			return s[:i] + "?<redacted>"
		}
		return s
	})
}

// bronzeSourceParams maps a fetched detail into the bronze source_document upsert,
// keyed by the same natural key the ledger uses. discovered_at carries the
// ledger's discovery time; fetched_at is now.
func bronzeSourceParams(doc dbingest.IngestFetchDoc, d *ingest.DiscoveredDoc, now time.Time) dbbronze.UpsertSourceDocumentParams {
	return dbbronze.UpsertSourceDocumentParams{
		Source:       doc.Source,
		ExternalID:   doc.ExternalID,
		DocNumber:    strPtr(d.Number),
		Title:        strPtr(d.Title),
		DocType:      strPtr(string(d.DocType)),
		Issuer:       strPtr(d.Issuer),
		IssuedAt:     timePtr(d.IssuedAt),
		EffectiveAt:  timePtr(d.EffectiveAt),
		StatusRaw:    strPtr(d.Status),
		DetailUrl:    strPtr(d.DetailURL),
		ContentHash:  strPtr(discoveryHash(*d)),
		DiscoveredAt: doc.DiscoveredAt,
		FetchedAt:    &now,
	}
}

// timePtr returns nil for the zero time so it maps to SQL NULL.
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}
