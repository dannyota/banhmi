package retrieve

import (
	"context"
	"strconv"
	"strings"
)

// maxArticleRunes bounds the full-Điều text attached to a hit. A normal long Điều
// (several Khoản) fits comfortably; only pathological articles — chiefly amendment
// laws whose single "Điều 1" carries the whole law across hundreds of chunks — exceed
// it. Those are NOT truncated from the start (that could drop the matched clause);
// they return a pointer (citation + truncated) so the agent opens the document tool.
const maxArticleRunes = 14000

type articleText struct {
	text      string
	truncated bool
	attach    bool
}

// attachArticles fills each hit's full enclosing-Điều text by reassembling all of the
// Điều's chunks. Search ranks fine-grained chunks (a long Điều is split by
// Khoản/Điểm/Đoạn); this gives the agent the whole article for context while the
// snippet stays the precise matched provision. Best-effort: a DB error leaves Article
// empty and never fails the search.
func (r *hybridRetriever) attachArticles(ctx context.Context, hits []Hit) {
	if len(hits) == 0 {
		return
	}
	// Resolve each hit to its enclosing Điều; only citations that roll up to a Điều get
	// a full-article attachment. Dedup the (document, Điều) targets for the query.
	dieuOf := make([]string, len(hits))
	seen := make(map[string]bool, len(hits))
	var docIDs []int64
	var dieus []string
	for i, h := range hits {
		dc := parentCitation(h.Citation, rollupDieu)
		if !strings.HasPrefix(strings.ToLower(dc), "điều ") {
			continue
		}
		dieuOf[i] = dc
		k := keyOf(h.DocumentID, dc)
		if seen[k] {
			continue
		}
		seen[k] = true
		docIDs = append(docIDs, h.DocumentID)
		dieus = append(dieus, dc)
	}
	if len(docIDs) == 0 {
		return
	}

	// One query: for each (document_id, Điều) target, gather the Điều's chunk bodies in
	// document order. The match is exact — `= "Điều 7"` for an unsplit Điều, plus
	// `LIKE "Điều 7, %"` for its Khoản/Điểm/Đoạn shards — so it never catches "Điều 70".
	const q = `
WITH targets(document_id, dieu) AS (
    SELECT unnest($1::bigint[]), unnest($2::text[])
)
SELECT t.document_id, t.dieu, c.content
FROM targets t
JOIN gold.chunk c
  ON c.document_id = t.document_id
 AND (c.citation = t.dieu OR c.citation LIKE t.dieu || ', %')
ORDER BY t.document_id, t.dieu, c.ordinal, c.id`

	rows, err := r.pool.Query(ctx, q, docIDs, dieus)
	if err != nil {
		r.log.Warn("retrieve: attach articles failed; returning matched snippets only", "err", err)
		return
	}
	defer rows.Close()

	parts := make(map[string][]string)
	for rows.Next() {
		var docID int64
		var dieu, content string
		if err := rows.Scan(&docID, &dieu, &content); err != nil {
			r.log.Warn("retrieve: scan article chunk", "err", err)
			return
		}
		k := keyOf(docID, dieu)
		parts[k] = append(parts[k], content)
	}
	if err := rows.Err(); err != nil {
		r.log.Warn("retrieve: article rows", "err", err)
		return
	}

	assembled := make(map[string]articleText, len(parts))
	for k, cs := range parts {
		assembled[k] = articleProvision(assembleArticle(cs), maxArticleRunes)
	}

	for i := range hits {
		if dieuOf[i] == "" {
			continue
		}
		a, ok := assembled[keyOf(hits[i].DocumentID, dieuOf[i])]
		if !ok || !a.attach {
			continue
		}
		hits[i].ArticleCitation = dieuOf[i]
		hits[i].Article = a.text
		hits[i].ArticleTruncated = a.truncated
	}
}

func keyOf(docID int64, dieu string) string {
	return strconv.FormatInt(docID, 10) + "|" + dieu
}

// articleProvision decides what to attach for a Điều from its assembled text:
//   - empty → attach nothing.
//   - fits the cap → attach the full verbatim Điều.
//   - over the cap (a mega amendment article) → attach a pointer (truncated, no text)
//     rather than a truncated-from-start blob that could omit the matched clause; the
//     matched snippet stands and the agent opens the document tool for the full Điều.
func articleProvision(full string, maxRunes int) articleText {
	full = strings.TrimSpace(full)
	switch {
	case full == "":
		return articleText{}
	case len([]rune(full)) <= maxRunes:
		return articleText{text: full, attach: true}
	default:
		return articleText{truncated: true, attach: true}
	}
}

// assembleArticle joins a Điều's chunk bodies (already document-ordered) into one
// verbatim article. The indexer prepends ancestor lead-in lines (the Điều heading, and
// for a split Khoản its intro) to every descendant chunk, so consecutive chunks repeat
// them. Each chunk's leading lines that it shares with the previous lead-bearing chunk
// are stripped, so every ancestor line appears once. A Đoạn continuation shard carries
// no repeated lead (shares nothing): it is emitted whole and the previous lead is kept,
// so the next lead-bearing chunk still dedups against it.
func assembleArticle(chunks []string) string {
	if len(chunks) == 0 {
		return ""
	}
	var b strings.Builder
	writeLines := func(lines []string) {
		for _, line := range lines {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	prevLead := strings.Split(strings.TrimSpace(chunks[0]), "\n")
	writeLines(prevLead)
	for _, c := range chunks[1:] {
		lines := strings.Split(strings.TrimSpace(c), "\n")
		n := sharedPrefixLen(prevLead, lines)
		if n == 0 {
			writeLines(lines)
			continue
		}
		writeLines(lines[n:])
		prevLead = lines
	}
	return strings.TrimRight(b.String(), "\n")
}

// sharedPrefixLen counts the leading lines a and b share verbatim.
func sharedPrefixLen(a, b []string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}
