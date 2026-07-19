package ocs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

const (
	listPath = "/searchlaw/indexs/list_table_search"
	pacePage = 200 * time.Millisecond // polite crawl delay between pages
	maxPages = 250                    // safety cap (expect ~189 pages)
)

// lawCodeAct is the lawCode segment that identifies Acts (พระราชบัญญัติ).
const lawCodeAct = "-1B-"

// listResponse is the JSON envelope returned by the OCS listing API.
type listResponse struct {
	Meta listMeta `json:"meta"`
	Data []ocsLaw `json:"data"`
}

// listMeta carries pagination metadata. The OCS API returns some numeric
// fields as strings (e.g. page:"1"), so we use json.Number to handle both.
type listMeta struct {
	Page    json.Number `json:"page"`
	PerPage json.Number `json:"perpage"`
	Total   json.Number `json:"total"`
	Pages   json.Number `json:"pages"`
}

func (m listMeta) pages() int {
	n, _ := m.Pages.Int64()
	return int(n)
}

func (m listMeta) total() int {
	n, _ := m.Total.Int64()
	return int(n)
}

// ocsLaw is one law record from the listing API.
type ocsLaw struct {
	LawCode       string          `json:"lawCode"`
	LawNameTh     string          `json:"lawNameTh"`
	LawNameEn     json.RawMessage `json:"lawNameEn"` // string or boolean false
	EncTimelineID string          `json:"encTimelineID"`
	Year          int             `json:"year"`
	PublishDate   string          `json:"publishDate"` // "D/M/YYYY" Buddhist Era
	FileUUID      string          `json:"fileUUID"`    // URL or ""
	Childrens     json.RawMessage `json:"childrens"`   // "" or array
	State         string          `json:"state"`
	Num           int             `json:"num"`
}

// parseLawNameEn extracts the English name from the polymorphic lawNameEn field.
// The API returns either a JSON string or boolean false.
func parseLawNameEn(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Not a string (likely boolean false) — ignore.
	return ""
}

// parsePublishDate parses a B.E. date string "D/M/YYYY" where YYYY is Buddhist
// Era (CE = BE - 543). Returns zero time on parse failure.
func parsePublishDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	parts := strings.Split(s, "/")
	if len(parts) != 3 {
		return time.Time{}
	}
	day, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || day < 1 || day > 31 {
		return time.Time{}
	}
	month, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || month < 1 || month > 12 {
		return time.Time{}
	}
	yearBE, err := strconv.Atoi(strings.TrimSpace(parts[2]))
	if err != nil {
		return time.Time{}
	}
	yearCE := yearBE - 543
	if yearCE < 1800 || yearCE > 2200 {
		return time.Time{}
	}
	return time.Date(yearCE, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}

// mapState maps the OCS state code to a status string.
func mapState(state string) string {
	switch state {
	case "01":
		return "in_force"
	case "02":
		return "repealed"
	default:
		return ""
	}
}

// isActLawCode returns true if the lawCode contains the Act type segment.
func isActLawCode(code string) bool {
	return strings.Contains(code, lawCodeAct)
}

// Discover paginates the OCS listing API and returns all Acts (lawCode
// containing "-1B-"). The since and keyword parameters are ignored: OCS has no
// server-side date or keyword filter, so every call is a full sweep.
//
// The 500ms inter-page pace keeps the crawl polite (~189 pages, ~95s total).
func (s *Source) Discover(ctx context.Context, _ time.Time, _ string) ([]ingest.DiscoveredDoc, error) {
	var out []ingest.DiscoveredDoc
	totalPages := 1 // updated from first response

	for page := 1; page <= totalPages && page <= maxPages; page++ {
		u := fmt.Sprintf("%s%s?page=%d", s.baseURL, listPath, page)
		body, err := s.get(ctx, u)
		if err != nil {
			return out, fmt.Errorf("ocs listing page %d: %w", page, err)
		}

		var resp listResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return out, fmt.Errorf("ocs listing page %d: parse json: %w", page, err)
		}

		if page == 1 {
			totalPages = resp.Meta.pages()
			if totalPages > maxPages {
				totalPages = maxPages
			}
			s.log.Info("ocs discover started", "total", resp.Meta.Total, "pages", resp.Meta.Pages)
		}

		for i := range resp.Data {
			law := &resp.Data[i]
			if !isActLawCode(law.LawCode) {
				continue
			}

			rawMeta, _ := json.Marshal(law)

			detailURL := fmt.Sprintf("%s/searchlaw/law/%s", s.baseURL, law.LawCode)
			if law.EncTimelineID != "" {
				detailURL += "?tid=" + url.QueryEscape(law.EncTimelineID)
			}

			doc := ingest.DiscoveredDoc{
				SourceID:   SourceID,
				ExternalID: law.LawCode,
				Number:     law.LawCode,
				Title:      law.LawNameTh,
				DocType:    "พระราชบัญญัติ",
				DetailURL:  detailURL,
				Status:     mapState(law.State),
				IssuedAt:   parsePublishDate(law.PublishDate),
				RawMeta:    rawMeta,
			}

			// Add PDF file if fileUUID contains a download URL.
			if law.FileUUID != "" && strings.HasPrefix(law.FileUUID, "http") {
				doc.Files = []ingest.FileRef{{
					URL:      law.FileUUID,
					Name:     law.LawCode + ".pdf",
					Ext:      "pdf",
					Kind:     "main",
					MIMEType: "application/pdf",
				}}
			}

			out = append(out, doc)
		}

		if page%10 == 0 {
			s.log.Info("ocs discover progress", "page", page, "of", totalPages, "acts", len(out))
		}

		if page < totalPages {
			if err := sleep(ctx, pacePage); err != nil {
				return out, err
			}
		}
	}

	s.log.Info("ocs discover done", "acts", len(out), "pages_walked", min(totalPages, maxPages))
	return out, nil
}
