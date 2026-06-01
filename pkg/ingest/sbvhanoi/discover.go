package sbvhanoi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

const (
	listPageSize     = 200
	maxDiscoverPages = 200
)

// Discover crawls the SBV Hanoi legal-document list newest-first. A cold start
// walks every page using the portal's largest stable page size; incremental runs
// stop once the issued-date watermark is reached. keyword is optional and maps
// to the portal's own keyword box for manual exact-number checks.
func (s *Source) Discover(ctx context.Context, since time.Time, keyword string) ([]ingest.DiscoveredDoc, error) {
	var out []ingest.DiscoveredDoc
	coldStart := since.IsZero()
	keyword = strings.TrimSpace(keyword)

	for page := 1; page <= maxDiscoverPages; page++ {
		htmlText, err := s.fetchListPage(ctx, page, keyword)
		if err != nil {
			return nil, fmt.Errorf("list page %d: %w", page, err)
		}
		rows, lastPage := parseListPage(htmlText, s.baseURL)
		if len(rows) == 0 {
			break
		}
		for _, d := range rows {
			if !coldStart && !d.PublishedAt.IsZero() && !d.PublishedAt.After(since) {
				return out, nil
			}
			out = append(out, d)
		}
		if lastPage > 0 && page >= lastPage {
			break
		}
	}
	return out, nil
}

func (s *Source) fetchListPage(ctx context.Context, page int, keyword string) (string, error) {
	resp, err := s.get(ctx, s.listURL(page, keyword))
	if err != nil {
		return "", err
	}
	defer drainClose(resp.Body)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read list: %w", err)
	}
	return string(body), nil
}

func (s *Source) listURL(page int, keyword string) string {
	u, _ := url.Parse(strings.TrimRight(s.baseURL, "/") + listPath)
	q := u.Query()
	q.Set("p_p_id", "4_WAR_portalvbpqportlet")
	q.Set("p_p_lifecycle", "0")
	q.Set("p_p_state", "normal")
	q.Set("p_p_mode", "view")
	q.Set("p_p_col_id", "column-2")
	q.Set("p_p_col_pos", "1")
	q.Set("p_p_col_count", "2")
	q.Set("_4_WAR_portalvbpqportlet_mvcPath", "/html/portlet/list/view.jsp")
	q.Set("_4_WAR_portalvbpqportlet_delta", strconv.Itoa(listPageSize))
	q.Set("_4_WAR_portalvbpqportlet_advancedSearch", "false")
	q.Set("_4_WAR_portalvbpqportlet_andOperator", "true")
	q.Set("_4_WAR_portalvbpqportlet_resetCur", "false")
	q.Set("_4_WAR_portalvbpqportlet_cur", strconv.Itoa(page))
	if keyword != "" {
		q.Set("_4_WAR_portalvbpqportlet_keyword", keyword)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func parseListPage(htmlText, baseURL string) ([]ingest.DiscoveredDoc, int) {
	lastPage := 0
	for _, m := range listPageCurRe.FindAllStringSubmatch(htmlText, -1) {
		n, _ := strconv.Atoi(m[1])
		if n > lastPage {
			lastPage = n
		}
	}

	body := htmlText
	if i := strings.Index(body, `<tbody class="table-data">`); i >= 0 {
		body = body[i:]
	}
	if i := strings.Index(body, `</tbody>`); i >= 0 {
		body = body[:i]
	}

	var docs []ingest.DiscoveredDoc
	for _, row := range splitHTMLRows(body) {
		cells := splitHTMLCells(row)
		if len(cells) < 4 {
			continue
		}
		id := detailID(row)
		if id == "" {
			continue
		}
		number := cleanText(cells[0])
		title := cleanText(cells[1])
		issued := parseVNDate(cleanText(cells[2]))
		signer := cleanText(cells[3])
		raw := listRawMeta(id, number, title, issued, signer)
		docs = append(docs, ingest.DiscoveredDoc{
			SourceID:    SourceID,
			ExternalID:  id,
			Number:      number,
			Title:       title,
			Abstract:    title,
			DocType:     docTypeFromNumber(number, title),
			Issuer:      "NHNN Việt Nam",
			Signer:      signer,
			IssuedAt:    issued,
			PublishedAt: issued,
			DetailURL:   canonicalDetailURL(baseURL, id),
			RawMeta:     raw,
		})
	}
	return docs, lastPage
}

func splitHTMLRows(htmlText string) []string {
	return splitHTMLBlocks(htmlText, "<tr", "</tr>")
}

func splitHTMLCells(htmlText string) []string {
	return splitHTMLBlocks(htmlText, "<td", "</td>")
}

func splitHTMLBlocks(htmlText, startMark, endMark string) []string {
	var out []string
	for {
		start := strings.Index(htmlText, startMark)
		if start < 0 {
			return out
		}
		htmlText = htmlText[start:]
		end := strings.Index(htmlText, endMark)
		if end < 0 {
			return out
		}
		end += len(endMark)
		out = append(out, htmlText[:end])
		htmlText = htmlText[end:]
	}
}

func detailID(htmlText string) string {
	if m := detailIDRe.FindStringSubmatch(strings.ReplaceAll(htmlText, "&amp;", "&")); m != nil {
		return m[1]
	}
	return ""
}

func listRawMeta(id, number, title string, issued time.Time, signer string) json.RawMessage {
	raw, _ := json.Marshal(struct {
		ID       string `json:"id"`
		Number   string `json:"number"`
		Title    string `json:"title"`
		IssuedAt string `json:"issued_at,omitempty"`
		Signer   string `json:"signer,omitempty"`
	}{
		ID: id, Number: number, Title: title, IssuedAt: dateString(issued), Signer: signer,
	})
	return raw
}

func dateString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}
