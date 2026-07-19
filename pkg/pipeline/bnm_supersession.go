package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	dbsilver "danny.vn/banhmi/pkg/store/silver"
)

// bnmSupersessionEntry is a single superseded document reference parsed from a
// BNM PD's "policy document superseded" clause.
type bnmSupersessionEntry struct {
	// Title is the superseded document's title as stated in the clause.
	Title string
	// IssuedDate is the issue/effective date stated in the clause (if any).
	IssuedDate string
}

// bnmSupersessionClause is the parsed supersession clause from a BNM PD.
type bnmSupersessionClause struct {
	Entries []bnmSupersessionEntry
}

// bnmSupersessionMatch is a resolved supersession: a parsed entry matched to a
// silver document.
type bnmSupersessionMatch struct {
	Entry      bnmSupersessionEntry
	DocumentID int64
	DocKey     string
	Title      string
}

// bnmSupersessionResult summarizes the supersession extraction for a single
// BNM PD.
type bnmSupersessionResult struct {
	ClauseFound bool
	Entries     int
	Matched     int
	Unmatched   int
	Matches     []bnmSupersessionMatch
	Warnings    []string
}

// bnmSupersessionRe matches the BNM-standard "this policy document supersedes"
// anchor. The clause text follows on the same or subsequent lines. The regex is
// case-insensitive and allows the go-fitz line-broken "Policy\nDocument" form.
var bnmSupersessionRe = regexp.MustCompile(
	`(?i)this\s+policy\s+document\s+supersedes?\b`)

// parseBNMSupersessionClause extracts all superseded document references from a
// BNM PD section's content text. Returns nil when no "policy document
// supersedes" anchor is found.
func parseBNMSupersessionClause(content string) *bnmSupersessionClause {
	// Normalize line breaks: go-fitz often breaks "Policy\nDocument" across lines.
	normalized := strings.Join(strings.Fields(content), " ")
	if !bnmSupersessionRe.MatchString(normalized) {
		return nil
	}

	// Extract the text after the supersession anchor.
	loc := bnmSupersessionRe.FindStringIndex(normalized)
	if loc == nil {
		return nil
	}
	tail := normalized[loc[1]:]

	// The clause can be:
	//  - simple: "... supersedes the Policy Document on X issued on D."
	//  - enumerated: "... supersedes the following: (a) ...; (b) ...; and (c) ...."
	entries := parseBNMSupersededEntries(tail)
	if len(entries) == 0 {
		return nil
	}
	return &bnmSupersessionClause{Entries: entries}
}

// parseBNMSupersededEntries parses individual superseded document references from
// the tail text after the "supersedes" anchor.
func parseBNMSupersededEntries(tail string) []bnmSupersessionEntry {
	// Split on letter enumeration markers (a), (b), (c) ... or semicolons
	// to handle multi-item clauses.
	parts := splitEnumeratedClause(tail)

	var entries []bnmSupersessionEntry
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if entry, ok := parseOneBNMSupersededEntry(part); ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

// splitEnumeratedClause splits a BNM supersession clause into its constituent
// items. BNM uses "(a) ...; (b) ...; and (c) ..." or "; (a) ... (b) ..." or
// just a single statement.
func splitEnumeratedClause(text string) []string {
	// Match BNM enumeration: (a), (b), (c), etc.
	enumRe := regexp.MustCompile(`\(\s*[a-z]\s*\)`)
	locs := enumRe.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		// No enumeration — treat the whole tail as a single item.
		return []string{text}
	}

	var parts []string
	for i, loc := range locs {
		start := loc[1] // after the (a)/(b)/... marker
		var end int
		if i+1 < len(locs) {
			end = locs[i+1][0]
		} else {
			end = len(text)
		}
		parts = append(parts, text[start:end])
	}
	return parts
}

// issuedOnRe extracts "issued on <date>" from a clause part.
var issuedOnRe = regexp.MustCompile(`(?i)\(?\s*issued\s+on\s+(\d{1,2}\s+\w+\s+\d{4})\s*\)?`)

// parseOneBNMSupersededEntry extracts the title and issue date from a single
// superseded-document reference. Returns false if the text does not contain a
// recognizable document reference.
func parseOneBNMSupersededEntry(text string) (bnmSupersessionEntry, bool) {
	text = strings.TrimSpace(text)
	// Strip leading "the following", connectives, trailing punctuation.
	text = strings.TrimLeft(text, " \t;,.")
	for _, prefix := range []string{"the following:", "the following", "the "} {
		if strings.HasPrefix(strings.ToLower(text), prefix) {
			text = strings.TrimSpace(text[len(prefix):])
		}
	}

	// Try to extract "issued on <date>"
	var issuedDate string
	if m := issuedOnRe.FindStringSubmatch(text); m != nil {
		issuedDate = strings.TrimSpace(m[1])
		// Remove the "issued on ..." part to get the title.
		text = strings.TrimSpace(issuedOnRe.ReplaceAllString(text, ""))
	}

	// Clean up the title.
	title := cleanBNMSupersededTitle(text)
	if title == "" {
		return bnmSupersessionEntry{}, false
	}

	return bnmSupersessionEntry{
		Title:      title,
		IssuedDate: issuedDate,
	}, true
}

// cleanBNMSupersededTitle normalizes a superseded document title: strips
// "Policy Document on", "Guidelines on", etc. prefixes and trailing noise
// such as trailing "policy document" (from the "<Title> policy document
// issued on <Date>" form).
func cleanBNMSupersededTitle(text string) string {
	text = strings.TrimSpace(text)
	// Remove BNM PD prefixes to get the core title.
	for _, prefix := range []string{
		"Policy Document on ",
		"Policy document on ",
		"policy document on ",
		"Guidelines on ",
		"guidelines on ",
		"Circular on ",
		"circular on ",
	} {
		text = strings.TrimPrefix(text, prefix)
	}
	// Strip trailing connectives, "policy document" suffix, and punctuation.
	text = strings.TrimRight(text, " \t\r\n;,.")
	for _, suffix := range []string{" policy document", " and", " which"} {
		low := strings.ToLower(text)
		if strings.HasSuffix(low, suffix) {
			text = text[:len(text)-len(suffix)]
		}
	}
	text = strings.TrimRight(text, " \t\r\n;,.")
	return text
}

// normalizeBNMTitle produces a normalized form for title comparison: lowercase,
// collapsed whitespace, stripped parenthetical notes, stripped common prefix
// words.
func normalizeBNMTitle(title string) string {
	t := strings.ToLower(strings.TrimSpace(title))
	// Collapse whitespace.
	t = strings.Join(strings.Fields(t), " ")
	// Remove parenthetical notes like (revised), (amended), etc.
	parenRe := regexp.MustCompile(`\s*\([^)]*\)\s*`)
	t = parenRe.ReplaceAllString(t, " ")
	// Strip common BNM PD prefix words.
	for _, prefix := range []string{
		"policy document on ",
		"guidelines on ",
		"circular on ",
	} {
		t = strings.TrimPrefix(t, prefix)
	}
	t = strings.TrimRight(t, " \t\r\n;,.")
	return strings.Join(strings.Fields(t), " ")
}

// bnmTitleMatch reports whether two titles refer to the same document. It uses
// case-insensitive normalized equality or near-containment: a match if the
// normalized titles are equal, or one contains the other with a length ratio
// of at least 0.85 (catching minor variations like "Disclosure" vs
// "Disclosures", stripped parentheticals, or trailing subtitles). The ratio
// guard prevents a short base title from matching a longer, materially
// different document (e.g. a main AML/CFT PD matching its "Supplementary
// Document No. 1 – MSB Sector" counterpart).
func bnmTitleMatch(clauseTitle, corpusTitle string) bool {
	cn := normalizeBNMTitle(clauseTitle)
	dn := normalizeBNMTitle(corpusTitle)
	if cn == "" || dn == "" {
		return false
	}
	if cn == dn {
		return true
	}
	// Near-containment: the shorter must be at least 85% of the longer.
	shorter, longer := cn, dn
	if len(shorter) > len(longer) {
		shorter, longer = longer, shorter
	}
	if !strings.Contains(longer, shorter) {
		return false
	}
	ratio := float64(len(shorter)) / float64(len(longer))
	return ratio >= 0.85
}

// persistBNMSupersessionBestEffort detects and writes BNM PD supersession
// relations and expires the superseded documents. It runs only for jurisdiction
// MY / source bnm. This is called from Normalize after validity and relations
// are written.
func (a *Activities) persistBNMSupersessionBestEffort(
	ctx context.Context,
	target normalizeTarget,
	now time.Time,
	result *NormalizeResult,
) {
	if a.jur.Code != "my" || target.sourceDoc.Source != "bnm" {
		return
	}

	res, err := a.extractAndApplyBNMSupersession(ctx, target, now)
	if err != nil {
		a.log.Warn("bnm supersession extraction failed",
			"doc", target.fetchDoc.ExternalID, "err", err)
		result.Warnings = append(result.Warnings, "bnm_supersession_failed")
		return
	}
	if !res.ClauseFound {
		return
	}
	result.Warnings = append(result.Warnings, res.Warnings...)
	if res.Matched > 0 {
		a.log.Info("bnm supersession applied",
			"doc", target.fetchDoc.ExternalID,
			"entries", res.Entries,
			"matched", res.Matched,
			"unmatched", res.Unmatched)
	}
}

func (a *Activities) extractAndApplyBNMSupersession(
	ctx context.Context,
	target normalizeTarget,
	now time.Time,
) (bnmSupersessionResult, error) {
	var res bnmSupersessionResult

	// Read all sections for this document.
	sectionRows, err := a.silver.ListSectionsByDocument(ctx, target.document.ID)
	if err != nil {
		return res, fmt.Errorf("list sections: %w", err)
	}

	// Scan sections for a supersession clause.
	var clause *bnmSupersessionClause
	for _, sec := range sectionRows {
		content := nullableString(sec.Content)
		if content == "" {
			continue
		}
		if c := parseBNMSupersessionClause(content); c != nil {
			clause = c
			break
		}
	}
	if clause == nil {
		return res, nil
	}
	res.ClauseFound = true
	res.Entries = len(clause.Entries)

	// Load all BNM PD documents for matching.
	bnmDocs, err := a.listBNMPolicyDocuments(ctx)
	if err != nil {
		return res, fmt.Errorf("list bnm docs: %w", err)
	}

	// Match each superseded entry to a corpus document.
	for _, entry := range clause.Entries {
		match, ambiguous := matchBNMSupersededEntry(entry, bnmDocs, target.document.ID)
		if ambiguous {
			res.Unmatched++
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("bnm_supersession_ambiguous_match:%s", entry.Title))
			continue
		}
		if match == nil {
			res.Unmatched++
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("bnm_supersession_unresolved:%s", entry.Title))
			continue
		}
		res.Matched++
		res.Matches = append(res.Matches, *match)
	}

	// Apply: create relations and expire superseded documents.
	for _, match := range res.Matches {
		if err := a.applyBNMSupersession(ctx, target, match, now); err != nil {
			return res, fmt.Errorf("apply supersession doc=%d->%d: %w",
				target.document.ID, match.DocumentID, err)
		}
	}

	return res, nil
}

// listBNMPolicyDocuments returns all BNM PD silver documents, used for
// supersession title matching.
func (a *Activities) listBNMPolicyDocuments(ctx context.Context) ([]dbsilver.SilverDocument, error) {
	if a.dbpool == nil {
		return nil, fmt.Errorf("db pool is required for bnm supersession")
	}
	const q = `
SELECT id, doc_key, doc_number, doc_number_norm, title, doc_type, doc_type_code,
       issuer, issuer_code, issued_at, signer, is_consolidated, markdown,
       source_document_id, metadata_priority, index_class, created_at, updated_at
FROM silver.document
WHERE doc_key LIKE 'POLICY DOCUMENT|BNM/%'
ORDER BY id`
	rows, err := a.dbpool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var docs []dbsilver.SilverDocument
	for rows.Next() {
		var d dbsilver.SilverDocument
		if err := rows.Scan(
			&d.ID, &d.DocKey, &d.DocNumber, &d.DocNumberNorm,
			&d.Title, &d.DocType, &d.DocTypeCode,
			&d.Issuer, &d.IssuerCode, &d.IssuedAt, &d.Signer,
			&d.IsConsolidated, &d.Markdown,
			&d.SourceDocumentID, &d.MetadataPriority, &d.IndexClass,
			&d.CreatedAt, &d.UpdatedAt,
		); err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}

// matchBNMSupersededEntry tries to match a parsed supersession entry to a corpus
// document. Returns nil, false if no match; nil, true if ambiguous (multiple
// matches). Never matches the acting document itself.
func matchBNMSupersededEntry(
	entry bnmSupersessionEntry,
	docs []dbsilver.SilverDocument,
	actingDocID int64,
) (*bnmSupersessionMatch, bool) {
	var candidates []dbsilver.SilverDocument
	for _, doc := range docs {
		if doc.ID == actingDocID {
			continue
		}
		docTitle := nullableString(doc.Title)
		if bnmTitleMatch(entry.Title, docTitle) {
			candidates = append(candidates, doc)
		}
	}

	if len(candidates) == 0 {
		return nil, false
	}
	if len(candidates) > 1 {
		// Ambiguous — multiple corpus docs match the title.
		return nil, true
	}

	return &bnmSupersessionMatch{
		Entry:      entry,
		DocumentID: candidates[0].ID,
		DocKey:     candidates[0].DocKey,
		Title:      nullableString(candidates[0].Title),
	}, false
}

// applyBNMSupersession creates a supersedes/superseded_by relation pair and
// expires the superseded document.
func (a *Activities) applyBNMSupersession(
	ctx context.Context,
	target normalizeTarget,
	match bnmSupersessionMatch,
	now time.Time,
) error {
	// Create a doc_ref for the superseded document.
	srcRef, _ := json.Marshal(map[string]any{
		"source":     "bnm",
		"title":      match.Title,
		"doc_key":    match.DocKey,
		"superseded": true,
	})
	refKey := "bnm:superseded:" + match.DocKey
	refID, err := a.silver.UpsertDocRef(ctx, dbsilver.UpsertDocRefParams{
		RefKey:     refKey,
		DocumentID: &match.DocumentID,
		Label:      strPtr(match.Title),
		SrcRef:     srcRef,
		CreatedAt:  now,
	})
	if err != nil {
		return fmt.Errorf("upsert doc_ref for superseded %d: %w", match.DocumentID, err)
	}

	// Create the "replaces" relation from acting doc to superseded doc.
	if _, err := a.silver.UpsertDocumentRelation(ctx, dbsilver.UpsertDocumentRelationParams{
		FromDocumentID: target.document.ID,
		ToRefID:        refID,
		RelationType:   "replaces",
		Source:         strPtr("bnm"),
	}); err != nil {
		return fmt.Errorf("upsert replaces relation: %w", err)
	}

	// Expire the superseded document's validity.
	// Supersede any existing open validity record, then insert an expired one
	// with a real status_code so the preserve-richer-status guard
	// (statusCode == "") treats this as a known status.
	if err := a.silver.SupersedeValidityPeriods(ctx, dbsilver.SupersedeValidityPeriodsParams{
		DocumentID:   match.DocumentID,
		SupersededAt: &now,
	}); err != nil {
		return fmt.Errorf("supersede validity for %d: %w", match.DocumentID, err)
	}

	reason := fmt.Sprintf("superseded_by:%s", target.document.DocKey)
	_, err = a.silver.InsertValidityPeriod(ctx, dbsilver.InsertValidityPeriodParams{
		DocumentID:    match.DocumentID,
		SectionID:     nil,
		VersionID:     nil,
		StatusCode:    "SUPERSEDED",
		StatusClass:   "expired",
		EffFrom:       nil,
		EffTo:         nil,
		Reason:        &reason,
		CausedByRefID: &refID,
		Source:        strPtr("bnm"),
		ObservedAt:    now,
		SupersededAt:  nil,
	})
	if err != nil {
		return fmt.Errorf("insert expired validity for %d: %w", match.DocumentID, err)
	}

	a.log.Info("bnm supersession: expired superseded document",
		"acting_doc", target.fetchDoc.ExternalID,
		"superseded_doc_id", match.DocumentID,
		"superseded_title", match.Title,
		"reason", reason)

	return nil
}
