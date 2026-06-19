package vanban

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

const (
	// backfillYearFloor bounds a cold-start walk: rows issued before this year are
	// not enqueued. vanban's value is the fresh tail vbpl lags; older central law is
	// already covered by vbpl. 2018 matches the approved backfill window, subject to
	// the page cap below.
	backfillYearFloor = 2018
	// coldStartMaxPages caps a cold-start walk (50 rows/page; the site hard-caps page
	// size at 50). It bounds the one-time backfill to fit the Discover activity
	// timeout; if the floor year is not reached within the cap, Discover logs it
	// rather than truncating silently. Operators can raise it for a deeper backfill.
	coldStartMaxPages = 240
	// incrementalMaxPages caps an incremental walk. The daily feed rarely exceeds a
	// page or two of new central documents; this is a safety bound, not a target.
	incrementalMaxPages = 40
)

// Discover walks the newest-first central VBQPPL list and returns documents to
// scope-filter downstream. The site is ASP.NET WebForms with a hard 50-row page
// cap, so discovery pages via the GridView Page$N postback (the only paginator that
// reproduces from a plain HTTP client; the issuer-filtered search does not
// paginate). A cold start (since is the zero time) walks back to backfillYearFloor
// or the page cap; an incremental run stops at the issued-date watermark. The
// pipeline applies scope.Match — vanban is a keyword-less feed like the congbao RSS.
func (s *Source) Discover(ctx context.Context, since time.Time, _ string) ([]ingest.DiscoveredDoc, error) {
	coldStart := since.IsZero()
	maxPages := incrementalMaxPages
	if coldStart {
		maxPages = coldStartMaxPages
	}

	htmlText, err := s.do(ctx, s.baseURL+listPath, nil) // GET page 1
	if err != nil {
		return nil, fmt.Errorf("list page 1: %w", err)
	}

	var out []ingest.DiscoveredDoc
	var oldest time.Time
	page := 1
	for {
		rows := parseListRows(htmlText, s.baseURL)
		if len(rows) == 0 {
			break
		}
		stop := false
		for _, d := range rows {
			if !d.PublishedAt.IsZero() {
				oldest = d.PublishedAt
			}
			if !coldStart && !d.PublishedAt.IsZero() && !d.PublishedAt.After(since) {
				stop = true // reached the watermark (newest-first)
				break
			}
			if coldStart && !d.PublishedAt.IsZero() && d.PublishedAt.Year() < backfillYearFloor {
				stop = true // walked back past the backfill floor
				break
			}
			out = append(out, d)
		}
		if stop {
			break
		}
		target, next, ok := nextPage(htmlText, page)
		if !ok {
			break // no further pages
		}
		if page >= maxPages {
			s.log.Warn("vanban discover hit page cap; older documents not walked this run",
				"pages", page, "max_pages", maxPages, "cold_start", coldStart,
				"oldest_reached", dateString(oldest))
			break
		}
		if err := sleep(ctx, pacePostback); err != nil {
			return out, err
		}
		htmlText, err = s.pageBack(ctx, htmlText, target, next)
		if err != nil {
			return out, fmt.Errorf("list page %d: %w", next, err)
		}
		page = next
	}
	s.log.Info("vanban discover walked", "pages", page, "rows", len(out), "cold_start", coldStart,
		"oldest_reached", dateString(oldest))
	return out, nil
}

// pageBack issues the GridView Page$next postback, carrying the prior response's
// ASP.NET hidden state plus the filter dropdowns at their defaults (no search
// button — that resets paging).
func (s *Source) pageBack(ctx context.Context, prevHTML, target string, next int) (string, error) {
	form := parseHidden(prevHTML)
	for k, v := range defaultDropdowns() {
		form.Set(k, v)
	}
	form.Set("__EVENTTARGET", target)
	form.Set("__EVENTARGUMENT", "Page$"+strconv.Itoa(next))
	return s.do(ctx, s.baseURL+listPath, form)
}

// defaultDropdowns are the list filter controls at "no filter" — the document grid
// renders the full newest-first central feed.
func defaultDropdowns() map[string]string {
	return map[string]string{
		"ctrl_190922_45$txtSearch":         "",
		"ctrl_191017_163$drdDocCategory":   "0",
		"ctrl_191017_163$drdDocOrg":        "0",
		"ctrl_191017_163$drdDocYear":       "0",
		"ctrl_191017_163$txtSearchKeyword": "",
		"ctrl_191017_163$drdRecordPerPage": "50",
	}
}

// nextPage returns the GridView postback target and the page number to request
// after the current page, if the pager offers it. The control id is read from the
// page so a re-skinned grid still paginates.
func nextPage(htmlText string, current int) (target string, next int, ok bool) {
	matches := pagerTargetRe.FindAllStringSubmatch(htmlText, -1)
	if len(matches) == 0 {
		return "", 0, false
	}
	target = matches[0][1]
	want := current + 1
	for _, m := range matches {
		if n, _ := strconv.Atoi(m[2]); n == want {
			return target, want, true
		}
	}
	return "", 0, false
}

// parseListRows parses the grvDocument table rows into discovered documents. Each
// row carries the số ký hiệu (span.code), issue date (span.issued-date), trích yếu
// (span.substract), and the detail docid in its anchor href.
func parseListRows(htmlText, baseURL string) []ingest.DiscoveredDoc {
	body := htmlText
	if i := strings.Index(body, "grvDocument"); i >= 0 {
		body = body[i:]
	}
	var docs []ingest.DiscoveredDoc
	for _, row := range splitRows(body) {
		dm := docidRe.FindStringSubmatch(row)
		cm := codeSpanRe.FindStringSubmatch(row)
		if dm == nil || cm == nil {
			continue // not a document row (header/pager)
		}
		docID := dm[1]
		number := cleanDocNumber(cm[1])
		title := ""
		if sm := substractSpanRe.FindStringSubmatch(row); sm != nil {
			title = cleanText(sm[1])
		}
		var issued time.Time
		if im := issuedDateRe.FindStringSubmatch(row); im != nil {
			issued = parseVNDate(cleanText(im[1]))
		}
		docs = append(docs, ingest.DiscoveredDoc{
			SourceID:    SourceID,
			ExternalID:  docID,
			Number:      number,
			Title:       title,
			Abstract:    title,
			DocType:     docTypeFromNumber(number, title),
			IssuedAt:    issued,
			PublishedAt: issued,
			DetailURL:   canonicalDetailURL(baseURL, docID),
			RawMeta:     listRawMeta(docID, number, title, issued),
		})
	}
	return docs
}

// splitRows returns each <tr>…</tr> block in htmlText.
func splitRows(htmlText string) []string {
	var out []string
	for {
		start := strings.Index(htmlText, "<tr")
		if start < 0 {
			return out
		}
		htmlText = htmlText[start:]
		end := strings.Index(htmlText, "</tr>")
		if end < 0 {
			return out
		}
		end += len("</tr>")
		out = append(out, htmlText[:end])
		htmlText = htmlText[end:]
	}
}

func listRawMeta(id, number, title string, issued time.Time) json.RawMessage {
	raw, _ := json.Marshal(struct {
		ID       string `json:"id"`
		Number   string `json:"number,omitempty"`
		Title    string `json:"title,omitempty"`
		IssuedAt string `json:"issued_at,omitempty"`
	}{ID: id, Number: number, Title: title, IssuedAt: dateString(issued)})
	return raw
}

func dateString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}
