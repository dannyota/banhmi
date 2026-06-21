package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/activity"
	"golang.org/x/text/unicode/norm"

	"danny.vn/banhmi/pkg/extract"
	"danny.vn/banhmi/pkg/ingest"
	"danny.vn/banhmi/pkg/rag/embed"
	"danny.vn/banhmi/pkg/scope"
	dbbronze "danny.vn/banhmi/pkg/store/bronze"
	dbconfig "danny.vn/banhmi/pkg/store/config"
	dbgold "danny.vn/banhmi/pkg/store/gold"
	dbingest "danny.vn/banhmi/pkg/store/ingest"
	dbsilver "danny.vn/banhmi/pkg/store/silver"
)

const discoverOverlap = 48 * time.Hour

// Activities holds the dependencies shared by banhmi's pipeline activities: the
// ingest ledger, bronze, silver, and gold stores, the per-source crawlers, and
// the raw-file storage directory. Activities own all I/O and business logic;
// workflows only orchestrate.
type Activities struct {
	dbpool     *pgxpool.Pool
	ledger     *dbingest.Queries
	bronze     *dbbronze.Queries
	silver     *dbsilver.Queries
	gold       *dbgold.Queries
	configQ    *dbconfig.Queries
	sources    map[string]ingest.Source
	storageDir string
	// markitdown runs local MarkItDown (DOCX/HTML/PDF -> Markdown; DOC via
	// LibreOffice PDF bridge). It is required for text extraction.
	markitdown *extract.MarkItDownClient
	// embedder is the optional embedding client. nil means embeddings are
	// disabled for this run; Index still writes chunks and embeddings can be
	// backfilled later.
	embedder embed.Embedder
	// kaggleToken authenticates the bulk embed/OCR Kaggle clients (KGAT). It is a
	// secret sourced from config (KAGGLE_API_TOKEN); it stays on the worker here
	// and is never placed in workflow params (which Temporal persists in history).
	kaggleToken string
	// jurisdiction is the legal jurisdiction this worker serves (config
	// Jurisdiction; "vn"/"my"); it scopes config loads such as the scope matcher.
	jurisdiction string

	// validityClasses maps an upper-cased source effect-status code to a
	// status_class, loaded once from config.validity_status. Missing entries fall
	// back to the built-in statusCodeToClass defaults; a nil map (load failed or no
	// configQ) falls back entirely.
	validityOnce    sync.Once
	validityClasses map[string]string
}

// NewActivities constructs the activity set from its dependencies.
// markitdown is required for text extraction.
// embedder may be nil (disabled); Index still writes gold.chunk rows and
// embeddings can be backfilled later. OCR runs as a separate batch (OcrAll), not
// inline here.
func NewActivities(
	dbpool *pgxpool.Pool,
	ledger *dbingest.Queries,
	bronze *dbbronze.Queries,
	silver *dbsilver.Queries,
	gold *dbgold.Queries,
	configQ *dbconfig.Queries,
	sources map[string]ingest.Source,
	storageDir string,
	markitdown *extract.MarkItDownClient,
	embedder embed.Embedder,
	kaggleToken string,
	jurisdiction string,
) *Activities {
	return &Activities{
		dbpool:       dbpool,
		ledger:       ledger,
		bronze:       bronze,
		silver:       silver,
		gold:         gold,
		configQ:      configQ,
		sources:      sources,
		storageDir:   storageDir,
		markitdown:   markitdown,
		embedder:     embedder,
		kaggleToken:  kaggleToken,
		jurisdiction: jurisdiction,
	}
}

// Discover reads a source's newest-first feed since the stored watermark and
// records each new document in the ingest ledger: a fetch_doc parent, an
// append-only discovery record, and a seed `body` artifact for Fetch to claim. It
// then advances the per-(source, keyword) watermark. It is idempotent — re-running
// converges via the ledger's natural-key upserts, so an at-least-once retry never
// duplicates or loses a document.
func (a *Activities) Discover(ctx context.Context, p DiscoverParams) (DiscoverResult, error) {
	src, ok := a.sources[p.Source]
	if !ok {
		return DiscoverResult{}, fmt.Errorf("discover: unknown source %q", p.Source)
	}
	log := activity.GetLogger(ctx)

	storedWatermark, err := a.watermark(ctx, p)
	if err != nil {
		return DiscoverResult{}, err
	}
	querySince := storedWatermark
	if !querySince.IsZero() {
		// vbpl sorts by issueDate, and several documents can share the same day.
		// Re-query a small overlap so a late-arriving document with the same
		// timestamp as the cursor is not silently missed. Upserts make repeats
		// cheap; newWatermark below never regresses.
		querySince = querySince.Add(-discoverOverlap)
	}

	docs, err := src.Discover(ctx, querySince, p.Keyword)
	if err != nil {
		return DiscoverResult{}, fmt.Errorf("discover %s: %w", p.Source, err)
	}

	// Scope filtering depends on how the source selected these docs. Empty-keyword
	// congbao and vbpl feeds use scope.Match over configured terms. Empty-keyword
	// SBV Hanoi is only a support sweep after VBPL, so it first drops VBPL
	// duplicate numbers and then uses the same configured discovery keywords as
	// VBPL against the local title. A non-empty keyword means the source already
	// filtered server-side, so every doc is in scope and the keyword is its
	// provenance.
	var matcher *scope.Matcher
	var localKeywords []localDiscoveryKeyword
	useLocalKeywordFilter := p.Source == "sbv_hanoi" && p.Keyword == ""
	if p.Keyword == "" {
		if useLocalKeywordFilter {
			localKeywords, err = a.loadVBPLDiscoveryKeywords(ctx)
			if err != nil {
				return DiscoverResult{}, err
			}
		} else {
			matcher, err = a.loadMatcher(ctx)
			if err != nil {
				return DiscoverResult{}, err
			}
		}
	}

	excl, err := a.loadDiscoveryExclusions(ctx)
	if err != nil {
		return DiscoverResult{}, err
	}
	vbplDocNumbers := map[string]struct{}{}
	if p.Source == "sbv_hanoi" {
		vbplDocNumbers, err = a.loadVBPLDocNumbers(ctx)
		if err != nil {
			return DiscoverResult{}, err
		}
	}

	now := time.Now().UTC()
	newWatermark := storedWatermark
	enqueued, skipped := 0, 0
	duplicateVBPL := 0
	localKeywordFiltered := 0
	for _, d := range docs {
		if strings.TrimSpace(d.ExternalID) == "" {
			continue
		}
		if d.PublishedAt.After(newWatermark) {
			newWatermark = d.PublishedAt // advance over every doc seen, in scope or not
		}
		if excl.drop(d) { // config-driven exclusions: doc type (Chỉ thị) / validity (HHL)
			skipped++
			continue
		}
		if p.Source == "sbv_hanoi" && docNumberInSet(d.Number, vbplDocNumbers) {
			skipped++
			duplicateVBPL++
			continue
		}
		matched := []string{p.Keyword}
		if useLocalKeywordFilter {
			matched = matchLocalDiscoveryKeywords(d.Number, d.Title, d.Abstract, localKeywords)
			if len(matched) == 0 {
				skipped++
				localKeywordFiltered++
				continue
			}
		} else if matcher != nil {
			sc := matcher.Match(d.Number, d.Title, d.Abstract)
			if !sc.InScope {
				skipped++
				continue
			}
			matched = sc.Matched
		}
		if err := a.recordDiscovery(ctx, p, d, matched, now); err != nil {
			return DiscoverResult{}, err
		}
		enqueued++
	}

	wm := ""
	if !newWatermark.IsZero() {
		wm = newWatermark.UTC().Format(time.RFC3339)
	}
	if err := a.ledger.UpsertDiscoverCursor(ctx, dbingest.UpsertDiscoverCursorParams{
		Source:        p.Source,
		Keyword:       p.Keyword,
		Watermark:     wm,
		ExpectedTotal: int64(len(docs)),
		LastSeenTotal: int64(len(docs)),
		LastRunAt:     &now,
		CreatedAt:     now,
	}); err != nil {
		return DiscoverResult{}, fmt.Errorf("upsert cursor %s/%s: %w", p.Source, p.Keyword, err)
	}

	log.Info("discover persisted",
		"source", p.Source, "keyword", p.Keyword,
		"discovered", len(docs), "in_scope", enqueued, "skipped", skipped,
		"duplicate_vbpl", duplicateVBPL, "local_keyword_filtered", localKeywordFiltered,
		"local_keywords", len(localKeywords), "watermark", wm)
	return DiscoverResult{Discovered: len(docs), Enqueued: enqueued, Skipped: skipped, Watermark: wm}, nil
}

type localDiscoveryKeyword struct {
	term string
	norm string
}

func (a *Activities) loadVBPLDiscoveryKeywords(ctx context.Context) ([]localDiscoveryKeyword, error) {
	rows, err := a.configQ.ListDiscoveryKeywords(ctx, "vbpl")
	if err != nil {
		return nil, fmt.Errorf("load vbpl discovery keywords for sbv_hanoi filter: %w", err)
	}
	return normalizeLocalDiscoveryKeywords(rows), nil
}

func normalizeLocalDiscoveryKeywords(rows []string) []localDiscoveryKeyword {
	out := make([]localDiscoveryKeyword, 0, len(rows))
	for _, row := range rows {
		term := strings.TrimSpace(row)
		n := normalizeLocalDiscoveryText(term)
		if n == "" {
			continue
		}
		out = append(out, localDiscoveryKeyword{term: term, norm: n})
	}
	return out
}

func matchLocalDiscoveryKeywords(number, title, abstract string, keywords []localDiscoveryKeyword) []string {
	hay := normalizeLocalDiscoveryText(strings.Join([]string{number, title, abstract}, "\n"))
	if hay == "" {
		return nil
	}
	var matched []string
	for _, k := range keywords {
		if strings.Contains(hay, k.norm) {
			matched = append(matched, k.term)
		}
	}
	return matched
}

func normalizeLocalDiscoveryText(s string) string {
	s = strings.ToLower(norm.NFC.String(s))
	var b strings.Builder
	space := true
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			if !space {
				b.WriteByte(' ')
				space = true
			}
			continue
		}
		b.WriteRune(r)
		space = false
	}
	return strings.TrimSpace(b.String())
}

func (a *Activities) loadVBPLDocNumbers(ctx context.Context) (map[string]struct{}, error) {
	const pageSize int32 = 1000
	out := map[string]struct{}{}
	for offset := int32(0); ; offset += pageSize {
		rows, err := a.bronze.ListSourceDocumentsBySource(ctx, dbbronze.ListSourceDocumentsBySourceParams{
			Source: "vbpl",
			Limit:  pageSize,
			Offset: offset,
		})
		if err != nil {
			return nil, fmt.Errorf("list vbpl documents for duplicate filter: %w", err)
		}
		addDocNumbers(out, rows)
		if len(rows) < int(pageSize) {
			break
		}
	}
	return out, nil
}

func addDocNumbers(out map[string]struct{}, rows []dbbronze.BronzeSourceDocument) {
	for _, row := range rows {
		norm := strings.TrimSpace(row.DocNumberNorm)
		if norm == "" && row.DocNumber != nil {
			norm = normalizeDocNumberForStorage(*row.DocNumber)
		}
		if norm != "" {
			out[norm] = struct{}{}
		}
	}
}

func docNumberInSet(number string, numbers map[string]struct{}) bool {
	norm := normalizeDocNumberForStorage(number)
	if norm == "" {
		return false
	}
	_, ok := numbers[norm]
	return ok
}

// loadMatcher builds the scope Matcher from the config schema. It is called once
// per Discover run so operator edits to config scope terms and re-seeds take
// effect on the next tick without restarting the worker.
func (a *Activities) loadMatcher(ctx context.Context) (*scope.Matcher, error) {
	rows, err := a.configQ.ListScopeTerms(ctx, a.jurisdiction)
	if err != nil {
		return nil, fmt.Errorf("load scope terms: %w", err)
	}
	terms := make([]scope.Term, len(rows))
	for i, r := range rows {
		terms[i] = scope.Term{Text: r.Term, Class: r.TermClass}
	}
	return scope.Load(terms), nil
}

// discoveryExclusions drops a discovered doc before it is recorded, by document
// type (name) or validity status (effStatus code) — e.g. Chỉ thị (non-normative
// directives) and HHL (fully expired; HHL1P partial-expiry is kept, still live in
// part). Tunable via config.setting keys discover.exclude_doc_types and
// discover.exclude_eff_status (comma-separated).
type discoveryExclusions struct {
	docTypes map[string]bool
	statuses map[string]bool
}

func (x discoveryExclusions) drop(d ingest.DiscoveredDoc) bool {
	return x.docTypes[strings.TrimSpace(string(d.DocType))] || x.statuses[strings.TrimSpace(d.Status)]
}

// loadDiscoveryExclusions reads the exclusion lists from config.setting so an
// operator can tune them without a redeploy (mirrors loadMatcher / loadGate).
func (a *Activities) loadDiscoveryExclusions(ctx context.Context) (discoveryExclusions, error) {
	rows, err := a.configQ.ListSettings(ctx)
	if err != nil {
		return discoveryExclusions{}, fmt.Errorf("list settings: %w", err)
	}
	x := discoveryExclusions{docTypes: map[string]bool{}, statuses: map[string]bool{}}
	for _, r := range rows {
		switch r.Key {
		case "discover.exclude_doc_types":
			for _, v := range splitCSVSetting(r.Value) {
				x.docTypes[v] = true
			}
		case "discover.exclude_eff_status":
			for _, v := range splitCSVSetting(r.Value) {
				x.statuses[v] = true
			}
		}
	}
	return x, nil
}

// splitCSVSetting splits a comma-separated config.setting value, trimming blanks.
func splitCSVSetting(v string) []string {
	var out []string
	for p := range strings.SplitSeq(v, ",") {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// watermark returns the time to discover after, from the per-(source, keyword)
// cursor. A missing cursor (first run) yields the zero time, taking the whole feed.
func (a *Activities) watermark(ctx context.Context, p DiscoverParams) (time.Time, error) {
	cur, err := a.ledger.GetDiscoverCursor(ctx, dbingest.GetDiscoverCursorParams{Source: p.Source, Keyword: p.Keyword})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return time.Time{}, nil
	case err != nil:
		return time.Time{}, fmt.Errorf("load cursor %s/%s: %w", p.Source, p.Keyword, err)
	}
	if cur.Watermark == "" {
		return time.Time{}, nil
	}
	t, perr := time.Parse(time.RFC3339, cur.Watermark)
	if perr != nil {
		// A malformed watermark must not wedge discovery; re-take the feed.
		return time.Time{}, nil
	}
	return t, nil
}

// recordDiscovery writes one document's rows: the fetch_doc parent (idempotent on
// source+external_id), the append-only discovery provenance, the seed `body`
// artifact Fetch will claim, and the bronze.source_document metadata row (title,
// số ký hiệu, dates, validity + the raw record). The doc is left plan_ready =
// false; Fetch enumerates the file artifacts and marks it ready once known.
func (a *Activities) recordDiscovery(ctx context.Context, p DiscoverParams, d ingest.DiscoveredDoc, matched []string, now time.Time) error {
	return a.recordDiscoveredDoc(ctx, p.Source, "keyword", "keyword", d, matched, 0, "", now)
}

func (a *Activities) recordDiscoveredDoc(
	ctx context.Context,
	source string,
	provenance string,
	via string,
	d ingest.DiscoveredDoc,
	matched []string,
	srcFetchDocID int64,
	relationType string,
	now time.Time,
) error {
	hash := discoveryHash(d)
	doc, err := a.ledger.UpsertFetchDoc(ctx, dbingest.UpsertFetchDocParams{
		Source:       source,
		ExternalID:   d.ExternalID,
		InScope:      true,
		Provenance:   provenance,
		ContentHash:  &hash,
		DetailUrl:    strPtr(d.DetailURL),
		DiscoveredAt: now,
		State:        nil, // COALESCE -> 'discovered'
	})
	if err != nil {
		return fmt.Errorf("upsert fetch_doc %s/%s: %w", source, d.ExternalID, err)
	}

	// Append-only provenance: one row per reason that put the doc in scope.
	for _, kw := range matched {
		if err := a.ledger.RecordDocDiscovery(ctx, dbingest.RecordDocDiscoveryParams{
			FetchDocID:    doc.ID,
			Via:           via,
			Keyword:       kw,
			SrcFetchDocID: srcFetchDocID,
			RelationType:  relationType,
			DiscoveredAt:  now,
		}); err != nil {
			return fmt.Errorf("record discovery doc=%d kw=%q: %w", doc.ID, kw, err)
		}
	}

	if _, err := a.ledger.EnqueueArtifact(ctx, dbingest.EnqueueArtifactParams{
		FetchDocID:  doc.ID,
		Kind:        "body",
		RefKey:      "main",
		Url:         strPtr(d.DetailURL),
		MaxAttempts: 5,
		CreatedAt:   now,
	}); err != nil {
		return fmt.Errorf("enqueue body artifact doc=%d: %w", doc.ID, err)
	}

	// Persist the discovery-time source metadata to bronze (title, số ký hiệu, type,
	// issuer, validity, dates) plus the full raw record (raw_meta). fetched_at stays
	// NULL until Fetch enriches the row; the upsert COALESCE-preserves each phase.
	if _, err := a.bronze.UpsertSourceDocument(ctx, dbbronze.UpsertSourceDocumentParams{
		Source:       source,
		ExternalID:   d.ExternalID,
		DocNumber:    strPtr(d.Number),
		Title:        strPtr(d.Title),
		DocType:      strPtr(string(d.DocType)),
		Issuer:       strPtr(d.Issuer),
		IssuedAt:     timePtr(d.IssuedAt),
		EffectiveAt:  timePtr(d.EffectiveAt),
		StatusRaw:    strPtr(d.Status),
		DetailUrl:    strPtr(d.DetailURL),
		ContentHash:  &hash,
		RawMeta:      d.RawMeta,
		DiscoveredAt: now,
	}); err != nil {
		return fmt.Errorf("upsert bronze source_document %s/%s: %w", source, d.ExternalID, err)
	}
	return nil
}

// discoveryHash fingerprints the discovery-time fields so re-discovery can detect
// a genuine source change (it never re-opens a completed doc otherwise).
func discoveryHash(d ingest.DiscoveredDoc) string {
	sum := sha256.Sum256([]byte(d.Number + "|" + d.Title + "|" + d.DetailURL + "|" + string(d.DocType)))
	return hex.EncodeToString(sum[:])
}

// strPtr returns nil for blank strings so they map to SQL NULL.
func strPtr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}
