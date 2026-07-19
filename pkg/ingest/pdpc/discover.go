package pdpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// inScopeTopics are the PDPC guidance topics banhmi crawls.
var inScopeTopics = map[string]bool{
	"Advisory Guidelines":        true,
	"Sector-Specific Guidelines": true,
}

// listingResponse is the JSON envelope from the PDPC listing API.
type listingResponse struct {
	TotalItems int           `json:"totalItems"`
	Data       []listingItem `json:"data"`
}

// listingItem is one document in the PDPC listing API response.
type listingItem struct {
	ID    string `json:"id"`    // UUID
	Topic string `json:"topic"` // e.g. "Advisory Guidelines"
	Title string `json:"title"`
	Href  string `json:"href"` // relative path, e.g. "/organisations/..."
	Date  string `json:"date"` // e.g. "23 Sep 2013"
}

// Discover calls the PDPC listing API and returns advisory/sector-specific
// guidelines. The `since` watermark and `keyword` parameters are ignored — PDPC's
// API does not support incremental queries, and the corpus is small enough to
// fetch in full each time.
func (s *Source) Discover(ctx context.Context, _ time.Time, _ string) ([]ingest.DiscoveredDoc, error) {
	var all []ingest.DiscoveredDoc
	page := 1
	for {
		items, total, err := s.fetchPage(ctx, page)
		if err != nil {
			return all, fmt.Errorf("pdpc discover page %d: %w", page, err)
		}
		for _, it := range items {
			if !inScopeTopics[it.Topic] {
				continue
			}
			doc := ingest.DiscoveredDoc{
				SourceID:   SourceID,
				ExternalID: it.ID,
				Title:      it.Title,
				Abstract:   it.Title,
				DocType:    ingest.DocType(it.Topic),
				DetailURL:  s.baseURL + it.Href,
			}
			if t, err := time.Parse("02 Jan 2006", it.Date); err == nil {
				doc.IssuedAt = t
			}
			all = append(all, doc)
		}
		if len(items) == 0 || len(all) >= total || page*100 >= total {
			break
		}
		page++
		if err := sleep(ctx, pacePage); err != nil {
			return all, err
		}
	}
	s.log.Info("pdpc discover", "docs", len(all))
	return all, nil
}

func (s *Source) fetchPage(ctx context.Context, page int) ([]listingItem, int, error) {
	u, _ := url.Parse(s.baseURL + listingEndpoint)
	q := u.Query()
	q.Set("listingtype", "regulatory_guidance")
	q.Set("slug", "organisations/regulations-decisions/regulatory-guidance")
	q.Set("pathname", "/organisations/regulations-decisions/regulatory-guidance")
	q.Set("itemsperpage", "100")
	q.Set("page", strconv.Itoa(page))
	u.RawQuery = q.Encode()

	body, err := s.get(ctx, u.String())
	if err != nil {
		return nil, 0, err
	}
	var resp listingResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, 0, fmt.Errorf("parse listing response: %w", err)
	}
	return resp.Data, resp.TotalItems, nil
}
