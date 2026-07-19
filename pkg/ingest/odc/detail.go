package odc

import (
	"context"
	"encoding/json"
	"fmt"

	"danny.vn/banhmi/pkg/ingest"
)

// FetchDetail fetches a single CKAN package by ID and returns enriched metadata.
func (s *Source) FetchDetail(ctx context.Context, ref ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	apiURL := fmt.Sprintf("%s/api/3/action/package_show?id=%s", s.baseURL, ref.ExternalID)
	body, err := s.get(ctx, apiURL)
	if err != nil {
		return nil, fmt.Errorf("odc detail %s: %w", ref.ExternalID, err)
	}
	var resp struct {
		Success bool        `json:"success"`
		Result  ckanPackage `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, fmt.Errorf("odc parse detail: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("odc detail API returned success=false")
	}
	doc := packageToDoc(resp.Result)
	raw, _ := json.Marshal(resp.Result)
	doc.RawMeta = raw
	return &doc, nil
}
