package pipeline

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	dbsilver "danny.vn/banhmi/pkg/store/silver"
)

// VBHN (Văn bản hợp nhất) documents are official consolidations: a base document
// with its amendments folded into one readable text. A consolidation carries no
// effect-status of its own, so its validity is derived from the base family here,
// a deterministic post-normalize pass (VN only) modeled on bnm_supersession.go.
//
// Within one base family the newest consolidation (by issued_at) mirrors the
// base document's current status_class; older consolidations are expired
// (superseded by the newer consolidation). A consolidation whose base cannot be
// resolved (no `consolidates` relation, or an out-of-corpus target) resolves to
// unknown.

// vbhnReason values name why a consolidation received its derived validity.
// The "_text" variant marks a base resolved from the consolidation's own text
// (no `consolidates` relation in the source data) — see vbhnBaseFromText.
const (
	vbhnReasonMirrorBase     = "consolidates_base_status"
	vbhnReasonMirrorBaseText = "consolidates_base_status_text"
	vbhnReasonBaseUnresolved = "consolidates_base_unresolved"
	vbhnReasonSuperseded     = "superseded_by_newer_consolidation"
)

// vbhnConsolidation is one consolidated (VBHN) silver document paired with the
// base document its `consolidates` relation points at and the base's current
// status. baseDocumentID is 0 when the base is unresolved (missing relation or
// out-of-corpus target); baseStatusClass is empty then too.
type vbhnConsolidation struct {
	documentID      int64
	docKey          string
	issuedAt        time.Time
	baseDocumentID  int64
	baseDocKey      string
	baseStatusCode  string
	baseStatusClass string
	// baseFromText marks a base resolved by parsing the consolidation's own
	// text because the source carries no `consolidates` relation for it.
	baseFromText bool
}

// vbhnDecision is the document-level validity to write for one consolidation.
// All fields are comparable so decisions can be diffed directly in tests.
type vbhnDecision struct {
	documentID  int64
	statusCode  string
	statusClass string
	reason      string
}

// decideVBHNValidity groups consolidations by their base document and derives a
// document-level validity for each. The newest consolidation in a family mirrors
// the base's current status; older consolidations are expired. An unresolved base
// (baseDocumentID == 0) forms its own singleton family and resolves to unknown.
// The result is ordered by documentID for deterministic application.
func decideVBHNValidity(cons []vbhnConsolidation) []vbhnDecision {
	// Group by base family. Unresolved bases each form a singleton keyed by the
	// negative document id so they never collide with a real base group.
	groups := map[int64][]vbhnConsolidation{}
	for _, c := range cons {
		key := c.baseDocumentID
		if key == 0 {
			key = -c.documentID
		}
		groups[key] = append(groups[key], c)
	}

	var out []vbhnDecision
	for _, members := range groups {
		// Newest first; tie-break on documentID so the pick is deterministic.
		sort.Slice(members, func(i, j int) bool {
			if members[i].issuedAt.Equal(members[j].issuedAt) {
				return members[i].documentID > members[j].documentID
			}
			return members[i].issuedAt.After(members[j].issuedAt)
		})
		for i, m := range members {
			if i == 0 {
				out = append(out, newestVBHNDecision(m))
				continue
			}
			out = append(out, vbhnDecision{
				documentID:  m.documentID,
				statusCode:  "SUPERSEDED",
				statusClass: "expired",
				reason:      vbhnReasonSuperseded,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].documentID < out[j].documentID })
	return out
}

// newestVBHNDecision mirrors the base document's current status onto the newest
// consolidation. An unresolved or still-unknown base yields an unknown decision.
func newestVBHNDecision(m vbhnConsolidation) vbhnDecision {
	if m.baseDocumentID == 0 || m.baseStatusClass == "" || m.baseStatusClass == "unknown" {
		return vbhnDecision{
			documentID:  m.documentID,
			statusCode:  m.baseStatusCode, // "" when unresolved
			statusClass: "unknown",
			reason:      vbhnReasonBaseUnresolved,
		}
	}
	reason := vbhnReasonMirrorBase
	if m.baseFromText {
		reason = vbhnReasonMirrorBaseText
	}
	return vbhnDecision{
		documentID:  m.documentID,
		statusCode:  m.baseStatusCode,
		statusClass: m.baseStatusClass,
		reason:      fmt.Sprintf("%s:%s", reason, m.baseDocKey),
	}
}

// persistVBHNValidityBestEffort derives and writes document-level validity for
// every consolidated (VBHN) document in the corpus. It runs only for VN and only
// when the document just normalized is itself a consolidation — the trigger for a
// whole-corpus, idempotent recompute. It is best-effort: a failure is logged and
// recorded as a warning, never fatal to Normalize.
func (a *Activities) persistVBHNValidityBestEffort(
	ctx context.Context,
	target normalizeTarget,
	now time.Time,
	result *NormalizeResult,
) {
	if a.jur.Code != "vn" || !target.document.IsConsolidated {
		return
	}
	n, err := a.deriveVBHNValidity(ctx, now)
	if err != nil {
		a.log.Warn("vbhn validity derivation failed",
			"doc", target.fetchDoc.ExternalID, "err", err)
		result.Warnings = append(result.Warnings, "vbhn_validity_failed")
		return
	}
	if n > 0 {
		a.log.Info("vbhn validity derived",
			"doc", target.fetchDoc.ExternalID, "decisions", n)
	}
}

// deriveVBHNValidity reads every consolidation with its resolved base, decides
// the family validities, and applies them. Returns the number of decisions
// written.
func (a *Activities) deriveVBHNValidity(ctx context.Context, now time.Time) (int, error) {
	// Whole-corpus recompute under an advisory lock: concurrent normalize
	// workers each trigger this pass, and two racing supersede-then-insert
	// sequences leave duplicate open validity rows (observed 16ms apart,
	// 2026-07-24). One session-scoped lock serializes the recompute; waiting is
	// fine — the pass is idempotent and the second runner becomes a no-op.
	conn, err := a.dbpool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire for vbhn lock: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(hashtext('vbhn_validity'))"); err != nil {
		return 0, fmt.Errorf("vbhn advisory lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock(hashtext('vbhn_validity'))")
	}()

	cons, err := a.listVBHNConsolidations(ctx)
	if err != nil {
		return 0, err
	}
	if len(cons) == 0 {
		return 0, nil
	}
	// Second chance for consolidations whose source carries no `consolidates`
	// relation: the VBHN's own preamble/footnotes name the base document
	// deterministically — parse it and resolve against the corpus.
	for i := range cons {
		if cons[i].baseDocumentID != 0 {
			continue
		}
		if err := a.resolveVBHNBaseFromText(ctx, &cons[i]); err != nil {
			return 0, err
		}
	}
	decisions := decideVBHNValidity(cons)
	for _, d := range decisions {
		if err := a.applyVBHNDecision(ctx, d, now); err != nil {
			return 0, err
		}
	}
	return len(decisions), nil
}

// listVBHNConsolidations returns every consolidated silver document with the base
// document its `consolidates` relation points at (earliest-issued resolved target
// when several are present — amendments always postdate the base) and the base's
// current status. Base fields are zero when the base is unresolved.
func (a *Activities) listVBHNConsolidations(ctx context.Context) ([]vbhnConsolidation, error) {
	if a.dbpool == nil {
		return nil, fmt.Errorf("db pool is required for vbhn validity")
	}
	const q = `
SELECT
    d.id,
    d.doc_key,
    d.issued_at,
    base.id            AS base_document_id,
    base.doc_key       AS base_doc_key,
    bv.status_code     AS base_status_code,
    bv.status_class    AS base_status_class
FROM silver.document d
LEFT JOIN silver.document_relation r
       ON r.from_document_id = d.id AND r.relation_type = 'consolidates'
LEFT JOIN silver.doc_ref ref ON ref.id = r.to_ref_id
LEFT JOIN silver.document base ON base.id = ref.document_id
LEFT JOIN LATERAL (
    SELECT status_code, status_class
    FROM silver.validity_period
    WHERE document_id = base.id AND section_id IS NULL AND superseded_at IS NULL
    ORDER BY observed_at DESC
    LIMIT 1
) bv ON true
WHERE d.is_consolidated = true
ORDER BY d.id, base.issued_at NULLS LAST, base.id`
	rows, err := a.dbpool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// One VBHN can carry several `consolidates` edges (base + folded-in
	// amendments). The ORDER BY places resolved targets before the unresolved
	// row and, among resolved ones, the earliest-issued first — so the first row
	// per document is authoritative (its base if any relation resolves, else an
	// all-NULL unresolved placeholder). Keep only that first row per document.
	var out []vbhnConsolidation
	seen := map[int64]bool{}
	for rows.Next() {
		var (
			docID           int64
			docKey          string
			issuedAt        *time.Time
			baseID          *int64
			baseKey         *string
			baseStatusCode  *string
			baseStatusClass *string
		)
		if err := rows.Scan(&docID, &docKey, &issuedAt, &baseID, &baseKey, &baseStatusCode, &baseStatusClass); err != nil {
			return nil, err
		}
		if seen[docID] {
			continue
		}
		seen[docID] = true
		c := vbhnConsolidation{documentID: docID, docKey: docKey}
		if issuedAt != nil {
			c.issuedAt = *issuedAt
		}
		if baseID != nil {
			c.baseDocumentID = *baseID
			if baseKey != nil {
				c.baseDocKey = *baseKey
			}
			if baseStatusCode != nil {
				c.baseStatusCode = *baseStatusCode
			}
			if baseStatusClass != nil {
				c.baseStatusClass = *baseStatusClass
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// vbhnDocNumberPattern matches a Vietnamese document number after "số ",
// tolerating a stray space after a slash ("08/2025/ TT-NHNN"), a gazette-PDF
// line-wrap artifact.
const vbhnDocNumberPattern = `(\d+(?:/\d{2,4})?/\s?[A-ZĐ][A-ZĐ0-9-]*)`

// vbhnFootnoteRe finds the amendment-footnote phrasing: "sửa đổi[, bổ sung]
// [một số điều] [của|tại] [Điều N] <TYPE> số <NUM>" — the named document is the
// base being amended.
var vbhnFootnoteRe = regexp.MustCompile(
	`sửa đổi(?:, bổ sung)?(?: một số điều)?(?: của| tại)?(?: Điều \d+)? ` +
		`(?:Thông tư(?: liên tịch)?|Quyết định|Nghị định) số ` + vbhnDocNumberPattern)

// vbhnNumberRe finds any "số <NUM>" occurrence — used on the preamble window
// before "được sửa đổi, bổ sung bởi:", where the last number is the base.
var vbhnNumberRe = regexp.MustCompile(`số\s+` + vbhnDocNumberPattern)

// vbhnBaseFromText extracts the base document number a consolidation names in
// its own text. Every footnote names the base once per amendment, so the most
// frequent candidate wins; the preamble marker (base title before "được sửa
// đổi, bổ sung bởi:") counts as one vote too. Returns "" when the text names
// no candidate — the caller leaves the consolidation unresolved.
func vbhnBaseFromText(text string) string {
	votes := map[string]int{}
	for _, m := range vbhnFootnoteRe.FindAllStringSubmatch(text, -1) {
		votes[vbhnNormalizeDocNumber(m[1])]++
	}
	// Preamble: last "số <NUM>" within the 400 chars before the marker phrase.
	if i := strings.Index(text, "được sửa đổi, bổ sung bởi:"); i >= 0 {
		window := text[max(0, i-400):i]
		nums := vbhnNumberRe.FindAllStringSubmatch(window, -1)
		if len(nums) > 0 {
			votes[vbhnNormalizeDocNumber(nums[len(nums)-1][1])]++
		}
	}
	best, bestVotes := "", 0
	for num, n := range votes {
		if n > bestVotes || (n == bestVotes && num < best) {
			best, bestVotes = num, n
		}
	}
	return best
}

// vbhnNormalizeDocNumber removes internal whitespace ("08/2025/ TT-NHNN" →
// "08/2025/TT-NHNN") and trailing punctuation from an extracted number.
func vbhnNormalizeDocNumber(num string) string {
	num = strings.Join(strings.Fields(num), "")
	return strings.TrimRight(num, ".,;:")
}

// resolveVBHNBaseFromText fills c's base fields by parsing the consolidation's
// stored text for the base document number and resolving it to exactly one
// non-consolidated corpus document. Ambiguous or absent candidates leave c
// unresolved (honest unknown). Row-not-found is not an error.
func (a *Activities) resolveVBHNBaseFromText(ctx context.Context, c *vbhnConsolidation) error {
	var text string
	err := a.dbpool.QueryRow(ctx, `
SELECT markdown FROM silver.document_text
WHERE document_id = $1
ORDER BY is_binding DESC, id
LIMIT 1`, c.documentID).Scan(&text)
	if err != nil {
		return nil //nolint:nilerr // no text row → nothing to parse; stay unresolved
	}
	num := vbhnBaseFromText(text)
	if num == "" {
		return nil
	}
	rows, err := a.dbpool.Query(ctx, `
SELECT d.id, d.doc_key, bv.status_code, bv.status_class
FROM silver.document d
LEFT JOIN LATERAL (
    SELECT status_code, status_class
    FROM silver.validity_period
    WHERE document_id = d.id AND section_id IS NULL AND superseded_at IS NULL
    ORDER BY observed_at DESC
    LIMIT 1
) bv ON true
WHERE NOT d.is_consolidated AND upper(split_part(d.doc_key, '|', 2)) = upper($1)`, num)
	if err != nil {
		return fmt.Errorf("resolve vbhn base %q: %w", num, err)
	}
	defer rows.Close()
	var (
		matches           int
		curDoc            int64
		curKey            string
		curCode, curClass string
	)
	for rows.Next() {
		var (
			id                      int64
			docKey                  string
			statusCode, statusClass *string
		)
		if err := rows.Scan(&id, &docKey, &statusCode, &statusClass); err != nil {
			return err
		}
		matches++
		curDoc, curKey = id, docKey
		curCode, curClass = "", ""
		if statusCode != nil {
			curCode = *statusCode
		}
		if statusClass != nil {
			curClass = *statusClass
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if matches != 1 {
		return nil // absent or ambiguous — stay unresolved
	}
	c.baseDocumentID = curDoc
	c.baseDocKey = curKey
	c.baseStatusCode = curCode
	c.baseStatusClass = curClass
	c.baseFromText = true
	a.log.Info("vbhn base resolved from text",
		"doc", c.docKey, "base", curKey, "base_status", curClass)
	return nil
}

// applyVBHNDecision supersedes the consolidation's open validity record and
// inserts the derived one. Mirrors the supersede-then-insert pattern used by the
// BNM supersession pass. It is convergent: when the open record already matches
// the decision it writes nothing — the pass reruns on every consolidated-doc
// normalize, and without this guard a full VBHN ingest would rewrite every
// family's validity once per document (~N² rows).
func (a *Activities) applyVBHNDecision(ctx context.Context, d vbhnDecision, now time.Time) error {
	if cur, err := a.silver.CurrentValidityByDocument(ctx, d.documentID); err == nil &&
		cur.StatusCode == d.statusCode && cur.StatusClass == d.statusClass &&
		cur.Reason != nil && *cur.Reason == d.reason &&
		cur.Source != nil && *cur.Source == "vbhn" {
		return nil
	}
	if err := a.silver.SupersedeValidityPeriods(ctx, dbsilver.SupersedeValidityPeriodsParams{
		DocumentID:   d.documentID,
		SupersededAt: &now,
	}); err != nil {
		return fmt.Errorf("supersede vbhn validity for %d: %w", d.documentID, err)
	}
	reason := d.reason
	if _, err := a.silver.InsertValidityPeriod(ctx, dbsilver.InsertValidityPeriodParams{
		DocumentID:  d.documentID,
		StatusCode:  d.statusCode,
		StatusClass: d.statusClass,
		Reason:      &reason,
		Source:      strPtr("vbhn"),
		ObservedAt:  now,
	}); err != nil {
		return fmt.Errorf("insert vbhn validity for %d: %w", d.documentID, err)
	}
	return nil
}
