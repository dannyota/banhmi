package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"danny.vn/banhmi/pkg/base/jurisdiction"
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
	log        *slog.Logger
	dbpool     *pgxpool.Pool
	ledger     *dbingest.Queries
	bronze     *dbbronze.Queries
	silver     *dbsilver.Queries
	gold       *dbgold.Queries
	configQ    *dbconfig.Queries
	sources    map[string]ingest.Source
	storageDir string
	// files is the optional remote file cache (S3). nil means no remote cache;
	// files must exist locally or operations that need them will fail.
	files FileStore
	// embedder is the optional embedding client. nil means embeddings are
	// disabled for this run; Index still writes chunks and embeddings can be
	// backfilled later.
	embedder embed.Embedder
	// kaggleToken authenticates the bulk embed/OCR Kaggle clients (KGAT). It is a
	// secret sourced from config (KAGGLE_API_TOKEN); it stays on the worker here
	// and is never placed in workflow params (which Temporal persists in history).
	kaggleToken string
	// jur is the jurisdiction descriptor this worker serves (registry entry for
	// config Jurisdiction); it selects the structure parser, content-gate
	// profile, validity default, chunk labels, and scopes config loads such as
	// the scope matcher.
	jur jurisdiction.Descriptor

	// validityClasses maps an upper-cased source effect-status code to a
	// status_class, loaded once from config.validity_status. Missing entries fall
	// back to the built-in statusCodeToClass defaults; a nil map (load failed or no
	// configQ) falls back entirely.
	validityOnce    sync.Once
	validityClasses map[string]string

	// relationTypeMap maps a (source, code) pair to its config.relation_type row,
	// loaded once. It lets structured relations from sources whose status metadata
	// is official but label-coded (bi, bpk) resolve to banhmi labels and promote.
	// A nil map (load failed or no configQ) promotes nothing beyond the built-in
	// trusted sources — today's behavior.
	relationTypeOnce sync.Once
	relationTypeMap  map[relationTypeKey]relationTypeConfig
}

// relationTypeKey identifies one config.relation_type row: the source and its
// native relation code (vbpl integer codes as text, bi/bpk operator strings).
type relationTypeKey struct {
	source string
	code   string
}

// relationTypeConfig is the mapped label plus its amendment flag.
type relationTypeConfig struct {
	label      string
	isAmending bool
}

// loadRelationTypes returns the config.relation_type map, loading it on first
// use. Nil configQ or a load error leaves the map nil — no config-driven
// promotion, matching the pre-config behavior.
func (a *Activities) loadRelationTypes(ctx context.Context) map[relationTypeKey]relationTypeConfig {
	a.relationTypeOnce.Do(func() {
		if a.configQ == nil {
			return
		}
		rows, err := a.configQ.ListRelationTypes(ctx)
		if err != nil || len(rows) == 0 {
			return
		}
		m := make(map[relationTypeKey]relationTypeConfig, len(rows))
		for _, r := range rows {
			key := relationTypeKey{source: strings.TrimSpace(r.Source), code: strings.TrimSpace(r.Code)}
			m[key] = relationTypeConfig{label: r.Label, isAmending: r.IsAmending}
		}
		a.relationTypeMap = m
	})
	return a.relationTypeMap
}

// NewActivities constructs the activity set from its dependencies.
// embedder may be nil (disabled); Index still writes gold.chunk rows and
// embeddings can be backfilled later. OCR runs as a separate batch (OcrAll), not
// inline here.
func NewActivities(
	log *slog.Logger,
	dbpool *pgxpool.Pool,
	ledger *dbingest.Queries,
	bronze *dbbronze.Queries,
	silver *dbsilver.Queries,
	gold *dbgold.Queries,
	configQ *dbconfig.Queries,
	sources map[string]ingest.Source,
	storageDir string,
	files FileStore,
	embedder embed.Embedder,
	kaggleToken string,
	jur jurisdiction.Descriptor,
) *Activities {
	// The zero value must never select a half-configured jurisdiction; fall back
	// to VN, the compiled default (the playbook fallback invariant).
	if jur.Code == "" {
		jur = jurisdiction.For("")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Activities{
		log:         log,
		dbpool:      dbpool,
		ledger:      ledger,
		bronze:      bronze,
		silver:      silver,
		gold:        gold,
		configQ:     configQ,
		sources:     sources,
		storageDir:  storageDir,
		files:       files,
		embedder:    embedder,
		kaggleToken: kaggleToken,
		jur:         jur,
	}
}

// SourceIDs returns the IDs of all sources wired into a, sorted for
// deterministic order. It is a package-level function (not an Activities
// method) so Temporal's whole-struct activity registration never sees it —
// an exported method without an error return panics RegisterActivity.
func SourceIDs(a *Activities) []string {
	ids := make([]string, 0, len(a.sources))
	for id := range a.sources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
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
	log := a.log

	storedWatermark, err := a.watermark(ctx, p)
	if err != nil {
		return DiscoverResult{}, err
	}
	querySince := storedWatermark
	if p.FullScan {
		// Operator-forced full rescan: ignore the watermark, re-take the whole
		// feed. newWatermark below still baselines on storedWatermark, so the
		// cursor never regresses.
		querySince = time.Time{}
	}
	if !querySince.IsZero() {
		// vbpl sorts by issueDate, and several documents can share the same day.
		// Re-query a small overlap so a late-arriving document with the same
		// timestamp as the cursor is not silently missed. Upserts make repeats
		// cheap; newWatermark below never regresses.
		querySince = querySince.Add(-discoverOverlap)
	}

	// A source that fans out over sub-units (bpk jenis, bnm sectors, sc sections)
	// returns the documents it DID see alongside the error. Record those — the
	// upserts are idempotent — but leave the cursor where it is (below), so the
	// next run re-takes the whole window and fills the gap. Discarding the partial
	// haul instead would throw away a whole sweep because one deep page timed out.
	docs, discErr := src.Discover(ctx, querySince, p.Keyword)
	if discErr != nil && len(docs) == 0 {
		return DiscoverResult{}, fmt.Errorf("discover %s: %w", p.Source, discErr)
	}

	// Scope filtering: empty-keyword sources use scope.Match over configured
	// terms. A non-empty keyword means the source already filtered server-side,
	// so every doc is in scope and the keyword is its provenance. A source whose
	// sweep is itself pre-scoped (ingest.SweepInScoper, e.g. vbpl's SBV agency
	// feed) skips the vocabulary the same way — the matcher is still loaded for
	// non-sweep sources. Consolidated (VBHN) texts are now enqueued by trusted
	// sweeps too (indexed as primary). Dedup across sources (e.g. sbv_hanoi
	// vs vbpl) happens in the fetch step, not here.
	var matcher *scope.Matcher
	sweepTrusted := false
	if p.Keyword == "" {
		if ss, ok := src.(ingest.SweepInScoper); ok && ss.SweepInScope() {
			sweepTrusted = true
		}
		matcher, err = a.loadMatcher(ctx)
		if err != nil {
			return DiscoverResult{}, err
		}
	}

	excl, err := a.loadDiscoveryExclusions(ctx)
	if err != nil {
		return DiscoverResult{}, err
	}
	now := time.Now().UTC()
	newWatermark := storedWatermark
	enqueued, skipped := 0, 0
	for _, d := range docs {
		if strings.TrimSpace(d.ExternalID) == "" {
			continue
		}
		if d.PublishedAt.After(newWatermark) {
			newWatermark = d.PublishedAt
		}
		if excl.drop(d) {
			skipped++
			continue
		}
		provenance, matched, inScope := scopeDecision(d, p.Keyword, matcher, sweepTrusted)
		if !inScope {
			skipped++
			continue
		}
		if p.Limit > 0 && enqueued >= p.Limit {
			skipped++
			continue
		}
		if err := a.recordDiscovery(ctx, p, d, provenance, matched, now); err != nil {
			return DiscoverResult{}, err
		}
		enqueued++
	}

	// Incomplete sweep: the documents above are recorded, but the cursor must NOT
	// advance — advancing it over sub-units we never saw makes those documents
	// permanently invisible to every later incremental run.
	if discErr != nil {
		log.Warn("discover incomplete; cursor NOT advanced",
			"source", p.Source, "keyword", p.Keyword,
			"discovered", len(docs), "in_scope", enqueued, "skipped", skipped, "err", discErr)
		return DiscoverResult{Discovered: len(docs), Enqueued: enqueued, Skipped: skipped},
			fmt.Errorf("discover %s (incomplete, %d recorded): %w", p.Source, enqueued, discErr)
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
		"watermark", wm)
	return DiscoverResult{Discovered: len(docs), Enqueued: enqueued, Skipped: skipped, Watermark: wm}, nil
}

// loadMatcher builds the scope Matcher from the config schema. It is called once
// per Discover run so operator edits to config scope terms and re-seeds take
// effect on the next tick without restarting the worker.
func (a *Activities) loadMatcher(ctx context.Context) (*scope.Matcher, error) {
	rows, err := a.configQ.ListScopeTerms(ctx, a.jur.Code)
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
func (a *Activities) recordDiscovery(ctx context.Context, p DiscoverParams, d ingest.DiscoveredDoc, provenance string, matched []string, now time.Time) error {
	return a.recordDiscoveredDoc(ctx, p.Source, provenance, provenance, d, matched, 0, "", now)
}

// provenanceSweep marks documents enqueued by a pre-scoped sweep
// (ingest.SweepInScoper): the feed itself is the scope guarantee, no
// vocabulary term matched.
const provenanceSweep = "sweep"

// scopeDecision decides whether a discovered document is enqueued and returns
// its ledger provenance plus the per-reason keywords. A non-empty keyword means
// the source filtered server-side, so the doc is in scope with the keyword as
// provenance. A trusted sweep enqueues every document — including consolidated
// (VBHN) texts, now indexed as primary. Everything else (non-sweep sources)
// must match the scope vocabulary.
func scopeDecision(d ingest.DiscoveredDoc, keyword string, matcher *scope.Matcher, sweepTrusted bool) (provenance string, matched []string, inScope bool) {
	if keyword != "" || matcher == nil {
		return "keyword", []string{keyword}, true
	}
	if sweepTrusted {
		return provenanceSweep, []string{provenanceSweep}, true
	}
	sc := matcher.Match(d.Number, d.Title, d.Abstract)
	if !sc.InScope {
		return "", nil, false
	}
	return "keyword", sc.Matched, true
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
	// Keep the file references the source scraped at discovery time: they are the
	// authoritative download URLs, and without them a source can only re-derive or
	// synthesize one later (BOT synthesized, and 243 documents 404'd).
	doc, err := a.ledger.UpsertFetchDoc(ctx, dbingest.UpsertFetchDocParams{
		Source:          source,
		ExternalID:      d.ExternalID,
		InScope:         true,
		Provenance:      provenance,
		ContentHash:     &hash,
		DetailUrl:       strPtr(d.DetailURL),
		DiscoveredFiles: marshalDiscoveredFiles(d.Files),
		DiscoveredAt:    now,
		State:           nil, // COALESCE -> 'discovered'
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
	// File URLs are part of the fingerprint: when a source re-points a document's
	// download (BOT moved documents between path groups), the doc must re-open so
	// Fetch re-plans its artifacts against the new URL. Without this the ledger
	// keeps retrying a dead link forever.
	var files strings.Builder
	for _, f := range d.Files {
		files.WriteString("|" + f.URL)
	}
	sum := sha256.Sum256([]byte(d.Number + "|" + d.Title + "|" + d.DetailURL + "|" + string(d.DocType) + files.String()))
	return hex.EncodeToString(sum[:])
}

// strPtr returns nil for blank strings so they map to SQL NULL.
func strPtr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// marshalDiscoveredFiles encodes discovery-time file references for
// ingest.fetch_doc.discovered_files. Returns nil (SQL NULL) when the source
// scraped none, so the upsert's COALESCE preserves whatever a previous
// discovery captured rather than erasing it.
func marshalDiscoveredFiles(files []ingest.FileRef) []byte {
	if len(files) == 0 {
		return nil
	}
	b, err := json.Marshal(files)
	if err != nil {
		return nil
	}
	return b
}
