package vbpl

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

const (
	// sweepPageSize is the page size for the keyword-less agency sweep. The State
	// Bank corpus is small and bounded (~2k docs); discovery pages through it
	// newest-first in chunks of this size rather than one giant response — politer,
	// streamable, and it grows gracefully with the corpus. A cold start pages to
	// data.total; an incremental run stops at the watermark (see Discover).
	sweepPageSize = 500
)

type docAllRequest struct {
	PageNumber    int      `json:"pageNumber"`
	PageSize      int      `json:"pageSize"`
	SortBy        string   `json:"sortBy"`
	SortDirection string   `json:"sortDirection"`
	GroupVbpl     bool     `json:"groupVbpl"`
	AgencyLevel   string   `json:"agencyLevel"`
	OptionDoc     string   `json:"optionDoc"`
	MatchMode     string   `json:"matchMode"`
	AgencyIds     []string `json:"agencyIds,omitempty"`
	Keyword       string   `json:"keyword,omitempty"`
}

type docAllResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Total int               `json:"total"`
		Items []json.RawMessage `json:"items"`
	} `json:"data"`
}

// codeName is vbpl's common {code, name, …} object (effStatus, docType, …); a
// JSON null decodes to the zero value.
type codeName struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type docItem struct {
	ID                     string   `json:"id"`
	DocNum                 string   `json:"docNum"`
	Title                  string   `json:"title"`
	DocAbs                 string   `json:"docAbs"` // body/preamble text; feeds pkg/scope strong-term matching
	IssueDate              string   `json:"issueDate"`
	EffFrom                string   `json:"effFrom"`
	EffTo                  string   `json:"effTo"`
	EffStatus              codeName `json:"effStatus"`
	DocType                codeName `json:"docType"`
	AgencyName             string   `json:"agencyName"`
	HasContent             bool     `json:"hasContent"`
	IsConsolidatedDocument bool     `json:"isConsolidatedDocument"`
}

// Discover returns vbpl documents newest-first, limited to those issued strictly
// after since (pass the zero time to take the whole slice). It has two modes,
// selected by keyword:
//
//   - empty keyword → the State Bank agency sweep: a keyword-less query over
//     sbvAgencyIDs (62/908) returns the whole NHNN feed; the caller's pkg/scope
//     filter decides scope on title + docAbs.
//   - non-empty keyword → a title search (optionDoc=title) for that term over the
//     cross-cutting central issuers (nonSbvAgencyIDs); the server returns only
//     title matches, so the keyword itself is the scope filter.
//
// Either way it pages in chunks of sweepPageSize: a cold start (since is the zero
// time) pages to data.total so the first crawl misses nothing; an incremental run
// stops as soon as it reaches a document issued at or before since (newest-first
// watermark). postJSON's 429/5xx backoff handles source pressure.
func (s *Source) Discover(ctx context.Context, since time.Time, keyword string) ([]ingest.DiscoveredDoc, error) {
	keyword = strings.TrimSpace(keyword)
	agencyIDs := s.sbvAgencyIDs
	if keyword != "" {
		agencyIDs = s.nonSbvAgencyIDs
		if len(agencyIDs) == 0 {
			// A keyword search with no agency scope would query every central
			// issuer, and the keyword path has no downstream scope.Match — it would
			// enqueue cross-sector matches as in scope. Refuse rather than
			// over-capture; configure non-SBV agencies in config.issuer_code.
			s.log.Warn("vbpl keyword search skipped: no non-SBV agency ids configured", "keyword", keyword)
			return nil, nil
		}
	}
	coldStart := since.IsZero()
	var out []ingest.DiscoveredDoc
	for page := 1; ; page++ {
		var resp docAllResponse
		req := docAllRequest{
			PageNumber:    page,
			PageSize:      sweepPageSize,
			SortBy:        "issueDate",
			SortDirection: "desc",
			GroupVbpl:     false,
			AgencyLevel:   "TRUNG_UONG",
			OptionDoc:     "title",
			MatchMode:     "all_words",
			AgencyIds:     agencyIDs,
			Keyword:       keyword,
		}
		if err := s.postJSON(ctx, docAllURL, req, &resp); err != nil {
			return nil, fmt.Errorf("vbpl discover keyword=%q page %d: %w", keyword, page, err)
		}
		if len(resp.Data.Items) == 0 {
			break
		}
		for _, raw := range resp.Data.Items {
			var it docItem
			if err := json.Unmarshal(raw, &it); err != nil {
				return nil, fmt.Errorf("vbpl discover keyword=%q page %d: decode item: %w", keyword, page, err)
			}
			issued := parseDate(it.IssueDate)
			// An unparseable date (zero) must not trip the newest-first stop and
			// truncate the run — keep it (recall first; the upsert dedupes).
			if !coldStart && !issued.IsZero() && !issued.After(since) {
				return out, nil // newest-first: reached the watermark
			}
			out = append(out, toDoc(it, issued, raw))
		}
		if page*sweepPageSize >= resp.Data.Total {
			break
		}
	}
	return out, nil
}

func toDoc(it docItem, issued time.Time, raw json.RawMessage) ingest.DiscoveredDoc {
	return ingest.DiscoveredDoc{
		SourceID:       SourceID,
		ExternalID:     it.ID, // vbpl id; the enrich step fetches metadata/files by this id
		DocGUID:        it.ID,
		Number:         it.DocNum,
		Title:          it.Title,
		Abstract:       it.DocAbs,
		DocType:        ingest.DocType(it.DocType.Name),
		DocTypeCode:    it.DocType.Code,
		Issuer:         it.AgencyName,
		Status:         it.EffStatus.Code, // CHL / HHL / CCHL …
		IssuedAt:       issued,
		EffectiveAt:    parseDate(it.EffFrom),
		ExpireAt:       parseDate(it.EffTo),
		PublishedAt:    issued,
		DetailURL:      detailURL(it.ID), // human URL for inspection; Fetch uses ExternalID directly
		RawMeta:        raw,              // full doc/all item → bronze.source_document.raw_meta
		HasContent:     it.HasContent,
		IsConsolidated: it.IsConsolidatedDocument,
	}
}

// detailURL builds the canonical human detail URL for a vbpl document id
// (SOURCES.md: https://vbpl.vn/van-ban/chi-tiet/{id}). The API identity remains
// the opaque ExternalID returned by doc/all.
func detailURL(id string) string {
	if strings.TrimSpace(id) == "" {
		return ""
	}
	return originURL + "/van-ban/chi-tiet/" + id
}

// parseDate parses vbpl's "2006-01-02T15:04:05" timestamps (no zone).
func parseDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse("2006-01-02T15:04:05", s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
