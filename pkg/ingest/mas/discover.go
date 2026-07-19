package mas

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// contentTypes are the MAS document types banhmi discovers. Each is queried
// separately against the Solr API.
var contentTypes = []string{"Notices", "Guidelines"}

// solrResponse is the top-level Solr JSON response.
type solrResponse struct {
	Response struct {
		NumFound int       `json:"numFound"`
		Docs     []solrDoc `json:"docs"`
	} `json:"response"`
}

// solrDoc is one document from the Solr response.
type solrDoc struct {
	Title       string          `json:"document_title_string_s"`
	PageURL     string          `json:"page_url_s"`
	Date        string          `json:"mas_date_tdt"`
	ContentType string          `json:"mas_contenttype_s"`
	Summary     json.RawMessage `json:"document_shortsummary_t"`
	ItemID      string          `json:"itemid_s"`
	TopicPath   []string        `json:"topic_path"`
}

func (d solrDoc) summary() string {
	if len(d.Summary) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(d.Summary, &s) == nil {
		return s
	}
	var ss []string
	if json.Unmarshal(d.Summary, &ss) == nil && len(ss) > 0 {
		return strings.Join(ss, " ")
	}
	return ""
}

// numberPatterns extracts MAS notice/guideline reference numbers from titles.
// Tried in order; first match wins. Longer/more-specific patterns come first.
var numberPatterns = []*regexp.Regexp{
	// "MAS Notice 655", "MAS Notice 1111"
	regexp.MustCompile(`(?i)\bMAS\s+Notice\s+(\d+[A-Z]?)\b`),
	// "SFA 04-N13", "SFA 02A/03-N01", "SFA 06AA-N01", "SFA 02/02A/03-N01"
	// Captures the full compound reference (e.g. "SFA 04-N13").
	regexp.MustCompile(`\b((?:SFA|TCA)\s+[\dA-Z/]+-[NG]\d+)\b`),
	// "FSM-N05", "CMG-N03", "DIA-N01"
	regexp.MustCompile(`\b([A-Z]{2,4}-[NG]\d+)\b`),
	// "SFA04-N12" (no space) style
	regexp.MustCompile(`\b([A-Z]{2,5}\d+-[NG]\d+)\b`),
	// "PSN01A", "PSN01AA", "PSN02", "PSN06", "PSG01" — with optional letter suffix
	regexp.MustCompile(`\b(PS[NG]\d+[A-Z]*)\b`),
	// "MAS-N01" style
	regexp.MustCompile(`\b(MAS-[NG]\d+)\b`),
	// "TCA N06" (space, no dash) — normalize to "TCA-N06" handled in extractNumber
	regexp.MustCompile(`\b(TCA\s+[NG]\d+)\b`),
	// Bare "Notice 655", "Notice 655A", "Notice 1014" (no "MAS" prefix).
	regexp.MustCompile(`\bNotice\s+(\d+[A-Z]?)\b`),
	// "ID 2/04", "ID 1/03" — insurance directive references
	regexp.MustCompile(`\b(ID\s+\d+/\d+)\b`),
	// Bracket-enclosed references: "[SFA 13-G18]", "[FAA-G09]", "[ID 1/09]", "[SFA 04-G05]"
	regexp.MustCompile(`\[((?:SFA|FAA|ID|CMG|TCA)\s*[\dA-Z/]+-?[A-Z]?\d*(?:/\d+)?)\]`),
}

// Discover calls the Solr API for Notices and Guidelines and returns the
// combined results. The since and keyword parameters are ignored — MAS discovery
// is a full sweep (the corpus is small: ~450 documents).
func (s *Source) Discover(ctx context.Context, _ time.Time, _ string) ([]ingest.DiscoveredDoc, error) {
	seen := map[string]bool{}
	var out []ingest.DiscoveredDoc

	for _, ct := range contentTypes {
		docs, err := s.discoverContentType(ctx, ct)
		if err != nil {
			return out, fmt.Errorf("mas discover %s: %w", ct, err)
		}
		for i := range docs {
			if seen[docs[i].ExternalID] {
				continue
			}
			seen[docs[i].ExternalID] = true
			out = append(out, docs[i])
		}
		if err := sleep(ctx, pacePage); err != nil {
			return out, err
		}
	}

	s.log.Info("mas discover", "docs", len(out))
	return out, nil
}

// discoverContentType queries the Solr API for a single content type.
func (s *Source) discoverContentType(ctx context.Context, contentType string) ([]ingest.DiscoveredDoc, error) {
	u := fmt.Sprintf(
		"%s/api/v1/search?fq=site_s:MAS&json.nl=map&q=*:*&start=0"+
			"&fq=mas_sector_sm:*"+
			"&fq=mas_mastercontenttypes_sm:%%22Regulatory+Instrument%%22"+
			"&fq=mas_contenttype_s:%%22%s%%22"+
			"&rows=500&sort=document_title_string_s+asc",
		s.baseURL, strings.ReplaceAll(contentType, " ", "+"),
	)

	body, err := s.get(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("fetch solr: %w", err)
	}

	var resp solrResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse solr response: %w", err)
	}

	out := make([]ingest.DiscoveredDoc, 0, len(resp.Response.Docs))
	for _, doc := range resp.Response.Docs {
		d, err := mapSolrDoc(s.baseURL, doc)
		if err != nil {
			s.log.Warn("mas skip document", "itemid", doc.ItemID, "err", err)
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

// mapSolrDoc converts a Solr document to a DiscoveredDoc.
func mapSolrDoc(baseURL string, doc solrDoc) (ingest.DiscoveredDoc, error) {
	if doc.ItemID == "" {
		return ingest.DiscoveredDoc{}, fmt.Errorf("empty itemid_s")
	}

	title, status := parseTitleStatus(doc.Title)
	number := extractNumber(doc.Title)

	var issuedAt time.Time
	if doc.Date != "" {
		t, err := time.Parse(time.RFC3339, doc.Date)
		if err != nil {
			// Try without timezone
			t, err = time.Parse("2006-01-02T15:04:05Z", doc.Date)
			if err != nil {
				// Non-fatal — continue without date.
			}
		}
		issuedAt = t
	}

	rawMeta, _ := json.Marshal(doc)

	detailURL := doc.PageURL
	if detailURL != "" && !strings.HasPrefix(detailURL, "http") {
		detailURL = strings.TrimRight(baseURL, "/") + detailURL
	}

	return ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: doc.ItemID,
		Number:     number,
		Title:      title,
		Abstract:   doc.summary(),
		DocType:    ingest.DocType(doc.ContentType),
		DetailURL:  detailURL,
		Status:     status,
		IssuedAt:   issuedAt,
		RawMeta:    rawMeta,
	}, nil
}

// parseTitleStatus extracts the clean title and status from the raw title.
// MAS marks cancelled notices with "[Cancelled]" in the title.
func parseTitleStatus(raw string) (title, status string) {
	if strings.Contains(raw, "[Cancelled]") {
		title = strings.TrimSpace(strings.ReplaceAll(raw, "[Cancelled]", ""))
		return title, "cancelled"
	}
	return strings.TrimSpace(raw), ""
}

// extractNumber extracts a MAS notice/guideline reference number from the title.
// Returns empty string if no recognizable pattern is found.
func extractNumber(title string) string {
	// Strip zero-width spaces (U+200B) that some MAS Solr records contain.
	clean := strings.ReplaceAll(title, "​", "")

	for _, re := range numberPatterns {
		if m := re.FindStringSubmatch(clean); len(m) > 1 {
			return normalizeNumber(m[1])
		}
	}
	return ""
}

// normalizeNumber collapses internal whitespace in compound references
// ("SFA 04-N13" → "SFA 04-N13" kept as-is, "TCA N06" → "TCA N06") and
// trims surrounding space.
func normalizeNumber(s string) string {
	// Collapse runs of whitespace to a single space.
	parts := strings.Fields(s)
	return strings.Join(parts, " ")
}
