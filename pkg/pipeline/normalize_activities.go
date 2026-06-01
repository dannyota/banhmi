package pipeline

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.temporal.io/sdk/activity"

	"danny.vn/banhmi/pkg/extract"
	dbbronze "danny.vn/banhmi/pkg/store/bronze"
	dbingest "danny.vn/banhmi/pkg/store/ingest"
	dbsilver "danny.vn/banhmi/pkg/store/silver"
)

// NormalizeResult summarizes a Normalize run.
type NormalizeResult struct {
	DocumentID              int64
	TextAuthority           string
	TextSource              string
	TextEngine              string
	SectionsParsed          int
	SectionsWritten         int
	ArticleCount            int
	ClauseCount             int
	PointCount              int
	RelationEvidenceWritten int
	RelationsWritten        int
	RelationTargetsEnqueued int
	ValidityStatusCode      string
	ValidityStatusClass     string
	Warnings                []string
	SkipReason              string
}

// IndexResult summarizes an Index run.
type IndexResult struct {
	DocumentID    int64
	ChunksWritten int
}

// Normalize reads the binding document_text for a document, parses it into the
// silver.document_section provision tree, and writes a doc-level
// silver.validity_period. It is idempotent: every write is an upsert on the
// natural key. Inputs and outputs are row ids, never blobs.
func (a *Activities) Normalize(ctx context.Context, p StageParams) (NormalizeResult, error) {
	log := activity.GetLogger(ctx)
	now := time.Now().UTC()

	target, err := a.loadNormalizeTarget(ctx, p)
	if err != nil {
		return NormalizeResult{}, err
	}
	result := NormalizeResult{DocumentID: target.document.ID}
	result.ValidityStatusCode, result.ValidityStatusClass = a.normalizeValidity(ctx, target.sourceDoc)

	roots, stats, treeWarnings, ok, err := a.selectProvisionTreeSections(ctx, target.sourceDoc.ID)
	if err != nil {
		return NormalizeResult{}, fmt.Errorf("select provision tree doc=%d: %w", target.sourceDoc.ID, err)
	}
	result.Warnings = append(result.Warnings, treeWarnings...)
	if ok {
		result.TextAuthority = "vbpl_provision_tree"
		result.TextSource = "provision_tree_json"
		result.applySectionStats(stats)

		written, err := a.replaceNormalizeSections(ctx, target.document.ID, roots)
		if err != nil {
			return NormalizeResult{}, fmt.Errorf("replace sections doc=%d: %w", target.document.ID, err)
		}
		result.SectionsWritten = written
		a.persistDocumentValidityBestEffort(ctx, target.document.ID, target.sourceDoc, now, target.fetchDoc.ExternalID, enactingEffectivePtr(roots), &result)
		a.persistRelationEvidenceBestEffort(ctx, target, now, &result)
		log.Info("normalize complete",
			"doc", target.fetchDoc.ExternalID, "document_id", target.document.ID,
			"text_authority", result.TextAuthority, "text_source", result.TextSource,
			"sections_parsed", result.SectionsParsed, "sections_written", result.SectionsWritten,
			"articles", result.ArticleCount, "clauses", result.ClauseCount, "points", result.PointCount,
			"relation_evidence", result.RelationEvidenceWritten, "relations", result.RelationsWritten,
			"relation_targets_enqueued", result.RelationTargetsEnqueued,
			"status_code", result.ValidityStatusCode, "status_class", result.ValidityStatusClass,
			"warnings", result.Warnings)
		return result, nil
	}

	txt, skipReason, textWarnings, err := a.selectBindingText(ctx, target.document.ID)
	if err != nil {
		return NormalizeResult{}, err
	}
	result.Warnings = append(result.Warnings, textWarnings...)
	if skipReason != "" {
		result.SkipReason = skipReason
		result.Warnings = append(result.Warnings, skipReason)
		if _, err := a.replaceNormalizeSections(ctx, target.document.ID, nil); err != nil {
			return NormalizeResult{}, fmt.Errorf("replace sections doc=%d: %w", target.document.ID, err)
		}
		a.persistDocumentValidityBestEffort(ctx, target.document.ID, target.sourceDoc, now, target.fetchDoc.ExternalID, nil, &result)
		a.persistRelationEvidenceBestEffort(ctx, target, now, &result)
		log.Warn("normalize: skipping section parse",
			"doc", target.fetchDoc.ExternalID, "document_id", target.document.ID,
			"reason", skipReason, "status_code", result.ValidityStatusCode,
			"status_class", result.ValidityStatusClass)
		return result, nil
	}

	result.TextAuthority = txt.Authority
	result.TextSource = txt.Source
	if txt.ExtractEngine != nil {
		result.TextEngine = *txt.ExtractEngine
	}

	roots, stats, warnings := parseNormalizeSections(*txt.Markdown)
	result.applySectionStats(stats)
	result.Warnings = append(result.Warnings, warnings...)

	written, err := a.replaceNormalizeSections(ctx, target.document.ID, roots)
	if err != nil {
		return NormalizeResult{}, fmt.Errorf("replace sections doc=%d: %w", target.document.ID, err)
	}
	result.SectionsWritten = written

	a.persistDocumentValidityBestEffort(ctx, target.document.ID, target.sourceDoc, now, target.fetchDoc.ExternalID, enactingEffectivePtr(roots), &result)
	a.persistRelationEvidenceBestEffort(ctx, target, now, &result)

	log.Info("normalize complete",
		"doc", target.fetchDoc.ExternalID, "document_id", target.document.ID,
		"text_authority", result.TextAuthority, "text_source", result.TextSource,
		"sections_parsed", result.SectionsParsed, "sections_written", result.SectionsWritten,
		"articles", result.ArticleCount, "clauses", result.ClauseCount, "points", result.PointCount,
		"relation_evidence", result.RelationEvidenceWritten, "relations", result.RelationsWritten,
		"relation_targets_enqueued", result.RelationTargetsEnqueued,
		"status_code", result.ValidityStatusCode, "status_class", result.ValidityStatusClass,
		"warnings", result.Warnings)
	return result, nil
}

type normalizeTarget struct {
	fetchDoc  dbingest.IngestFetchDoc
	sourceDoc dbbronze.BronzeSourceDocument
	document  dbsilver.SilverDocument
}

func (a *Activities) loadNormalizeTarget(ctx context.Context, p StageParams) (normalizeTarget, error) {
	fd, err := a.ledger.GetFetchDocByID(ctx, p.FetchDocID)
	if err != nil {
		return normalizeTarget{}, fmt.Errorf("get fetch_doc %d: %w", p.FetchDocID, err)
	}
	sd, err := a.bronze.SourceDocumentByExternalID(ctx, dbbronze.SourceDocumentByExternalIDParams{
		Source: fd.Source, ExternalID: fd.ExternalID,
	})
	if err != nil {
		return normalizeTarget{}, fmt.Errorf("source_document %s/%s: %w", fd.Source, fd.ExternalID, err)
	}
	doc, err := a.silver.DocumentByKey(ctx, docKey(sd))
	if err != nil {
		return normalizeTarget{}, fmt.Errorf("silver document for %s: %w", fd.ExternalID, err)
	}
	return normalizeTarget{fetchDoc: fd, sourceDoc: sd, document: doc}, nil
}

func (a *Activities) selectBindingText(ctx context.Context, documentID int64) (dbsilver.SilverDocumentText, string, []string, error) {
	texts, err := a.silver.ListTextsByDocument(ctx, documentID)
	if err != nil {
		return dbsilver.SilverDocumentText{}, "", nil, fmt.Errorf("list document texts doc=%d: %w", documentID, err)
	}
	txt, skipReason, warnings := chooseBindingText(texts)
	return txt, skipReason, warnings, nil
}

func chooseBindingText(texts []dbsilver.SilverDocumentText) (dbsilver.SilverDocumentText, string, []string) {
	var rejected []string
	bindingSeen := false
	for _, txt := range texts {
		if !txt.IsBinding {
			rejected = append(rejected, "skipped_non_binding_text:"+textCandidateLabel(txt))
			continue
		}
		bindingSeen = true
		reason := ""
		switch {
		case txt.Markdown == nil || strings.TrimSpace(*txt.Markdown) == "":
			reason = "empty_binding_text"
		default:
			reason = bindingTextQualitySkipReason(*txt.Markdown)
		}
		if reason == "" {
			return txt, "", rejected
		}
		rejected = append(rejected, "skipped_binding_candidate:"+textCandidateLabel(txt)+":"+reason)
	}
	if !bindingSeen {
		return dbsilver.SilverDocumentText{}, "no_binding_text", rejected
	}
	return dbsilver.SilverDocumentText{}, "no_usable_binding_text", rejected
}

func textCandidateLabel(txt dbsilver.SilverDocumentText) string {
	source := strings.TrimSpace(txt.Source)
	if source == "" {
		source = "unknown"
	}
	return txt.Authority + "/" + source
}

func bindingTextQualitySkipReason(markdown string) string {
	if supplementOnlyText(markdown) {
		return "supplement_only_binding_text"
	}
	if localizedMojibakeText(markdown) {
		return "localized_mojibake_binding_text"
	}
	gate := extract.DefaultGate().Assess(markdown)
	if !gate.OK {
		return "low_quality_binding_text:" + gate.Reason
	}
	return ""
}

func localizedMojibakeText(markdown string) bool {
	for _, rawLine := range strings.Split(markdown, "\n") {
		line := strings.TrimSpace(rawLine)
		if len([]rune(line)) < 12 {
			continue
		}
		total := 0
		markers := 0
		for _, r := range line {
			total++
			if strings.ContainsRune("√∆·ªƒ∫≠‚ÄØ", r) {
				markers++
			}
		}
		if markers >= 3 && float64(markers)/float64(total) >= 0.08 {
			return true
		}
	}
	return false
}

func parseNormalizeSections(markdown string) ([]Section, sectionStats, []string) {
	roots := ParseSections(markdown)
	stats := sectionStatsFor(roots)
	warnings := validateSectionTree(roots, stats)
	return roots, stats, warnings
}

func (a *Activities) replaceNormalizeSections(ctx context.Context, docID int64, roots []Section) (int, error) {
	if a.dbpool == nil {
		if _, err := a.silver.DeleteSectionsByDocument(ctx, docID); err != nil {
			return 0, err
		}
		return writeSections(ctx, a.silver, docID, roots, nil)
	}
	tx, err := a.dbpool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin normalize transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := a.silver.WithTx(tx)
	if _, err := q.DeleteSectionsByDocument(ctx, docID); err != nil {
		return 0, fmt.Errorf("delete sections: %w", err)
	}
	written, err := writeSections(ctx, q, docID, roots, nil)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit normalize transaction: %w", err)
	}
	return written, nil
}

func (a *Activities) persistDocumentValidity(ctx context.Context, docID int64, sd dbbronze.BronzeSourceDocument, now time.Time, enacting *time.Time) (string, string, error) {
	return a.upsertValidityPeriod(ctx, docID, sd, now, enacting)
}

func (a *Activities) persistDocumentValidityBestEffort(ctx context.Context, docID int64, sd dbbronze.BronzeSourceDocument, now time.Time, externalID string, enacting *time.Time, result *NormalizeResult) {
	statusCode, statusClass, err := a.persistDocumentValidity(ctx, docID, sd, now, enacting)
	if err != nil {
		activity.GetLogger(ctx).Warn("normalize: upsert validity_period failed",
			"doc", externalID, "document_id", docID, "err", err)
		result.Warnings = append(result.Warnings, "validity_write_failed")
		return
	}
	// Reflect the persisted status (incl. any enacting-clause override) in the
	// result so logs and downstream readers match what was written.
	result.ValidityStatusCode = statusCode
	result.ValidityStatusClass = statusClass
}

func (r *NormalizeResult) applySectionStats(stats sectionStats) {
	r.SectionsParsed = stats.Total
	r.ArticleCount = stats.Dieu
	r.ClauseCount = stats.Khoan
	r.PointCount = stats.Diem
}

type sectionStats struct {
	Total   int
	Phan    int
	Chuong  int
	Muc     int
	Dieu    int
	Khoan   int
	Diem    int
	Content int
}

func sectionStatsFor(sections []Section) sectionStats {
	var stats sectionStats
	var walk func([]Section)
	walk = func(nodes []Section) {
		for _, s := range nodes {
			stats.Total++
			switch s.Kind {
			case "phan":
				stats.Phan++
			case "chuong":
				stats.Chuong++
			case "muc":
				stats.Muc++
			case "dieu":
				stats.Dieu++
			case "khoan":
				stats.Khoan++
			case "diem":
				stats.Diem++
			}
			if strings.TrimSpace(s.Content) != "" {
				stats.Content++
			}
			walk(s.Children)
		}
	}
	walk(sections)
	return stats
}

func validateSectionTree(sections []Section, stats sectionStats) []string {
	warnings := make([]string, 0, 2)
	if stats.Total == 0 {
		warnings = append(warnings, "no_sections_parsed")
	}
	if stats.Dieu == 0 {
		warnings = append(warnings, "no_article_sections_parsed")
	}
	if stats.Total > 0 && stats.Content == 0 {
		warnings = append(warnings, "no_section_content")
	}

	seen := make(map[string]bool)
	var walk func([]Section)
	walk = func(nodes []Section) {
		for _, s := range nodes {
			path := strings.TrimSpace(s.CitationPath)
			switch {
			case path == "":
				warnings = append(warnings, "missing_citation_path")
			case seen[path]:
				warnings = append(warnings, "duplicate_citation_path:"+path)
			default:
				seen[path] = true
			}
			walk(s.Children)
		}
	}
	walk(sections)
	return warnings
}

func writeSections(ctx context.Context, q *dbsilver.Queries, docID int64, sections []Section, parentID *int64) (int, error) {
	total := 0
	for i := range sections {
		s := &sections[i]
		var label, heading, content, nodeKey *string
		var ptype pgtype.Int2
		if s.NodeKey != "" {
			nodeKey = &s.NodeKey
		}
		if s.PType != 0 {
			ptype = pgtype.Int2{Int16: s.PType, Valid: true}
		}
		if s.Label != "" {
			label = &s.Label
		}
		if s.Heading != "" {
			heading = &s.Heading
		}
		if s.Content != "" {
			content = &s.Content
		}
		id, err := q.UpsertSection(ctx, dbsilver.UpsertSectionParams{
			DocumentID:   docID,
			ParentID:     parentID,
			NodeKey:      nodeKey,
			Ptype:        ptype,
			Kind:         s.Kind,
			Ordinal:      int32(s.Ordinal), //nolint:gosec // ordinals fit int32
			Label:        label,
			Heading:      heading,
			CitationPath: s.CitationPath,
			Content:      content,
		})
		if err != nil {
			return total, fmt.Errorf("upsert section %s: %w", s.CitationPath, err)
		}
		total++
		if len(s.Children) > 0 {
			n, err := writeSections(ctx, q, docID, s.Children, &id)
			if err != nil {
				return total, err
			}
			total += n
		}
	}
	return total, nil
}

// statusCodeToClass maps vbpl/phapluat status codes to the status_class enum.
// Unknown/empty codes default to "in_force" (conservative: do not hide).
func statusCodeToClass(code string) string {
	switch strings.ToUpper(code) {
	case "CHL": // Còn hiệu lực
		return "in_force"
	case "HHL": // Hết hiệu lực
		return "expired"
	case "HHL1P", "HHL1PHAN": // Hết hiệu lực một phần
		return "partial"
	case "CCHL", "TNHL", "CHUACOHIEULUC": // Chưa có hiệu lực
		return "not_yet"
	case "TDHL": // Tạm dừng hiệu lực
		return "suspended"
	default:
		return "in_force"
	}
}

// statusClassForCode resolves a source effect-status code to a status_class using
// the operator-editable config.validity_status table (loaded once, lazily), and
// falls back to the built-in statusCodeToClass defaults for codes the table does
// not cover or when config is unavailable.
func (a *Activities) statusClassForCode(ctx context.Context, code string) string {
	a.validityOnce.Do(func() {
		if a.configQ == nil {
			return
		}
		rows, err := a.configQ.ListValidityStatuses(ctx)
		if err != nil || len(rows) == 0 {
			return // leave nil → built-in defaults
		}
		m := make(map[string]string, len(rows))
		for _, r := range rows {
			m[strings.ToUpper(strings.TrimSpace(r.Code))] = r.StatusClass
		}
		a.validityClasses = m
	})
	if c, ok := a.validityClasses[strings.ToUpper(strings.TrimSpace(code))]; ok {
		return c
	}
	return statusCodeToClass(code)
}

// enactingEffectiveRe matches a document's self-referential effective-date
// statement, e.g. "Thông tư này có hiệu lực thi hành kể từ ngày 01 tháng 7 năm
// 2024". The document-type word + "này" anchors the date to THIS document, so it
// is not confused with a cross-referenced document's date or a transitional
// sub-provision. It also will not match "hết hiệu lực" (other docs being
// repealed). Capture groups: day, month, year.
var enactingEffectiveRe = regexp.MustCompile(
	`(?i)(?:thông tư|nghị định|luật|quyết định|nghị quyết|pháp lệnh|văn bản)\s+này\s+có\s+hiệu\s+lực(?:\s+thi\s+hành)?(?:\s+kể)?\s+từ\s+ngày\s+(\d{1,2})\s+tháng\s+(\d{1,2})\s+năm\s+(\d{4})`)

// enactingEffectiveDate walks the provision tree for the document's own enacting
// clause and returns the effective date it states. Deterministic, no AI: it reads
// the law's authoritative self-statement of when it takes effect. Returns false
// when no self-referential effective-date statement is found. Used to correct rare
// cases where VBPL's structured effStatus/effFrom contradicts the document text.
func enactingEffectiveDate(roots []Section) (time.Time, bool) {
	var found time.Time
	var ok bool
	var walk func(secs []Section)
	walk = func(secs []Section) {
		for _, s := range secs {
			if ok {
				return
			}
			if m := enactingEffectiveRe.FindStringSubmatch(s.Content); m != nil {
				if d, err := parseEnactingDMY(m[1], m[2], m[3]); err == nil {
					found, ok = d, true
					return
				}
			}
			walk(s.Children)
		}
	}
	walk(roots)
	return found, ok
}

func enactingEffectivePtr(roots []Section) *time.Time {
	if d, ok := enactingEffectiveDate(roots); ok {
		return &d
	}
	return nil
}

func parseEnactingDMY(dd, mm, yyyy string) (time.Time, error) {
	d, err := strconv.Atoi(dd)
	if err != nil {
		return time.Time{}, err
	}
	m, err := strconv.Atoi(mm)
	if err != nil {
		return time.Time{}, err
	}
	y, err := strconv.Atoi(yyyy)
	if err != nil {
		return time.Time{}, err
	}
	if m < 1 || m > 12 || d < 1 || d > 31 || y < 1900 {
		return time.Time{}, fmt.Errorf("invalid enacting date %s-%s-%s", yyyy, mm, dd)
	}
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC), nil
}

func (a *Activities) normalizeValidity(ctx context.Context, sd dbbronze.BronzeSourceDocument) (string, string) {
	statusCode := ""
	if sd.StatusRaw != nil {
		statusCode = strings.TrimSpace(*sd.StatusRaw)
	}
	statusClass := a.statusClassForCode(ctx, statusCode)
	if statusCode == "" {
		statusCode = "CHL" // default raw code
	}
	return statusCode, statusClass
}

// upsertValidityPeriod supersedes any open doc-level validity record then
// inserts a fresh one derived from the bronze source_document. enacting is the
// effective date stated in the document's own enacting clause (nil if none found);
// it corrects rare cases where VBPL's structured status contradicts the text.
func (a *Activities) upsertValidityPeriod(ctx context.Context, docID int64, sd dbbronze.BronzeSourceDocument, now time.Time, enacting *time.Time) (string, string, error) {
	statusCode, statusClass := a.normalizeValidity(ctx, sd)
	effFrom := sd.EffectiveAt
	var reason *string

	// VBPL occasionally marks a document not-yet-effective with an erroneous future
	// effFrom (e.g. 52/2024/NĐ-CP: effFrom 2027 while its own clause says 2024-07-01).
	// The document's own enacting clause is authoritative: when it states an effective
	// date that has already passed, the document is in force. Trust the source text.
	if statusClass == "not_yet" && enacting != nil && !enacting.After(now) {
		statusClass = "in_force"
		eff := *enacting
		effFrom = &eff
		r := "enacting_clause_overrides_vbpl_not_yet"
		reason = &r
		activity.GetLogger(ctx).Info("validity: enacting clause overrides VBPL not-yet status",
			"document_id", docID, "vbpl_status_code", statusCode,
			"clause_eff_from", eff.Format("2006-01-02"))
	}

	// Supersede any existing open record.
	if err := a.silver.SupersedeValidityPeriods(ctx, dbsilver.SupersedeValidityPeriodsParams{
		DocumentID:   docID,
		SupersededAt: &now,
	}); err != nil {
		return "", "", fmt.Errorf("supersede validity_periods doc=%d: %w", docID, err)
	}

	_, err := a.silver.InsertValidityPeriod(ctx, dbsilver.InsertValidityPeriodParams{
		DocumentID:    docID,
		SectionID:     nil,
		VersionID:     nil,
		StatusCode:    statusCode,
		StatusClass:   statusClass,
		EffFrom:       effFrom,
		EffTo:         sd.ExpireAt,
		Reason:        reason,
		CausedByRefID: nil,
		Source:        strPtr(sd.Source),
		ObservedAt:    now,
		SupersededAt:  nil,
	})
	if err != nil {
		return "", "", fmt.Errorf("insert validity_period doc=%d: %w", docID, err)
	}
	return statusCode, statusClass, nil
}
