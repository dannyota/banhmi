package agclom

import (
	"context"
	"encoding/json"
	"fmt"
	stdhtml "html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

const (
	updatedPath = "/json-updated-2024.php" // DataTables endpoint: principal Acts (newest reprints)
	pageLen     = 200
	maxPages    = 20 // safety bound (~885 Acts / 200 ≈ 5 pages)
)

// updatedResponse is the DataTables JSON returned by json-updated-2024.php.
type updatedResponse struct {
	RecordsTotal int             `json:"recordsTotal"`
	Records      []updatedRecord `json:"records"`
}

// updatedRecord is one principal-Act row. doc2downloadgeneratepdf is itself a
// JSON-encoded string (a nested array, one entry per language edition).
type updatedRecord struct {
	ActID  string `json:"lgt_act_id"`
	ActNo  string `json:"lgt_act_no"`
	Title  string `json:"title"`                   // HTML: <a ...lang=BI>TITLE</a> + "As At <date>"
	GenPDF string `json:"doc2downloadgeneratepdf"` // JSON string: [{path,docName,icon},…]
}

type genPDFEntry struct {
	Path    string `json:"path"`
	DocName string `json:"docName"`
	Icon    string `json:"icon"`
}

var (
	titleBIRe = regexp.MustCompile(`(?is)<a[^>]*\bact=\d+&lang=BI[^"]*">(.*?)</a>`)
	asAtRe    = regexp.MustCompile(`(?is)As At\s*</i>\s*<i>\s*([0-9]{2}-[0-9]{2}-[0-9]{4})`)
	tagRe     = regexp.MustCompile(`(?s)<[^>]+>`)
	spaceRe   = regexp.MustCompile(`\s+`)
)

// Discover paginates the principal-Act list (newest reprints first) and returns
// every Act as a candidate; the pipeline applies scope.Match topically (agclom is
// a keyword-less full feed, like the congbao RSS). since is currently advisory —
// the full list is returned and the ledger dedups by content_hash; an incremental
// watermark can be added once the list's update-date ordering is confirmed.
func (s *Source) Discover(ctx context.Context, since time.Time, _ string) ([]ingest.DiscoveredDoc, error) {
	var out []ingest.DiscoveredDoc
	for page := 0; page < maxPages; page++ {
		form := url.Values{}
		form.Set("draw", strconv.Itoa(page+1))
		form.Set("start", strconv.Itoa(page*pageLen))
		form.Set("length", strconv.Itoa(pageLen))
		body, err := s.do(ctx, s.baseURL+updatedPath, form)
		if err != nil {
			return out, fmt.Errorf("updated page %d: %w", page, err)
		}
		docs, total, err := parseUpdated(body, s.baseURL)
		if err != nil {
			return out, fmt.Errorf("parse updated page %d: %w", page, err)
		}
		out = append(out, docs...)
		if len(docs) == 0 || len(out) >= total {
			break
		}
		if err := sleep(ctx, pacePage); err != nil {
			return out, err
		}
	}
	s.log.Info("agclom discover", "acts", len(out))
	return out, nil
}

// parseUpdated decodes one json-updated-2024.php page into discovered documents and
// the total record count. Each Act yields the English (BI) reprint PDF as its main
// file.
func parseUpdated(body, baseURL string) ([]ingest.DiscoveredDoc, int, error) {
	var resp updatedResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, 0, fmt.Errorf("decode response: %w", err)
	}
	out := make([]ingest.DiscoveredDoc, 0, len(resp.Records))
	for _, r := range resp.Records {
		if r.ActID == "" {
			continue
		}
		title := strings.TrimLeft(cleanText(firstSubmatch(titleBIRe, r.Title)), "* ") // strip LOM revised-marker
		var asAt time.Time
		if m := asAtRe.FindStringSubmatch(r.Title); m != nil {
			asAt = parseDMY(m[1])
		}
		var files []ingest.FileRef
		if f, ok := biFile(r.GenPDF, baseURL); ok {
			files = []ingest.FileRef{f}
		}
		out = append(out, ingest.DiscoveredDoc{
			SourceID:    SourceID,
			ExternalID:  r.ActID,
			Number:      "Act " + r.ActNo,
			Title:       title,
			Abstract:    title,
			DocType:     "Act",
			IssuedAt:    asAt,
			PublishedAt: asAt,
			DetailURL:   detailURL(baseURL, r.ActID),
			Files:       files,
			RawMeta:     json.RawMessage(mustJSON(r)),
		})
	}
	return out, resp.RecordsTotal, nil
}

// biFile parses the nested doc2downloadgeneratepdf JSON and returns the English
// (BI) reprint as a FileRef. The BI edition is identified by its path segment
// ("_BI/") or its English icon ("-en-").
func biFile(genPDF, baseURL string) (ingest.FileRef, bool) {
	genPDF = strings.TrimSpace(genPDF)
	if genPDF == "" {
		return ingest.FileRef{}, false
	}
	var entries []genPDFEntry
	if err := json.Unmarshal([]byte(genPDF), &entries); err != nil {
		return ingest.FileRef{}, false
	}
	for _, e := range entries {
		if strings.Contains(e.Path, "_BI/") || strings.Contains(e.Icon, "-en-") {
			return ingest.FileRef{
				URL:      pdfURL(baseURL, e.Path, e.DocName),
				Name:     e.DocName,
				Ext:      "pdf",
				Kind:     "main",
				MIMEType: "application/pdf",
			}, true
		}
	}
	return ingest.FileRef{}, false
}

func firstSubmatch(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

func cleanText(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = stdhtml.UnescapeString(s)
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

func parseDMY(s string) time.Time {
	if t, err := time.Parse("02-01-2006", strings.TrimSpace(s)); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}
