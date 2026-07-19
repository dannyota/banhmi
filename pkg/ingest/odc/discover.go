package odc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// searchQueries are the CKAN search terms used to discover financial documents.
var searchQueries = []string{
	"banking financial institution",
	"financial regulation",
	"securities law",
	"payment system",
	"insurance law",
	"microfinance",
	"anti money laundering",
	"electronic commerce",
	"consumer protection",
	"foreign exchange",
	"national bank cambodia",
	"financial leasing",
	"negotiable instruments",
	"commercial enterprise",
	"cybersecurity",
	"data protection",
}

// ckanResponse is the CKAN API package_search envelope.
type ckanResponse struct {
	Success bool `json:"success"`
	Result  struct {
		Count   int           `json:"count"`
		Results []ckanPackage `json:"results"`
	} `json:"result"`
}

// ckanPackage is one CKAN dataset.
type ckanPackage struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Title     string          `json:"title"`
	Notes     string          `json:"notes"`
	Spatial   json.RawMessage `json:"odm_spatial_range"` // ["kh"] or ["kh","la","mm",...]
	Resources []ckanResource  `json:"resources"`

	// ODC custom fields (some are objects in the API, not strings).
	DocNumber     json.RawMessage `json:"odm_document_number"`
	DocType       json.RawMessage `json:"odm_document_type"`
	EffectiveDate string          `json:"odm_effective_date"`
	LawsStatus    json.RawMessage `json:"odm_laws_status"`
}

// ckanResource is one downloadable resource in a CKAN dataset.
type ckanResource struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Name   string `json:"name"`
	Format string `json:"format"`
}

// Discover queries the CKAN API for banking/financial documents and returns
// discovered docs with their PDF resources. The since and keyword parameters
// are ignored — the ODC dataset is small.
func (s *Source) Discover(ctx context.Context, _ time.Time, _ string) ([]ingest.DiscoveredDoc, error) {
	seen := map[string]bool{}
	var docs []ingest.DiscoveredDoc

	for _, query := range searchQueries {
		pkgs, err := s.searchPackages(ctx, query)
		if err != nil {
			s.log.Warn("odc search failed", "query", query, "err", err)
			continue
		}
		for _, pkg := range pkgs {
			if seen[pkg.ID] {
				continue
			}
			if !isCambodiaOnly(pkg.Spatial) {
				continue
			}
			seen[pkg.ID] = true

			doc := packageToDoc(pkg)
			if len(doc.Files) == 0 {
				continue // skip packages with no downloadable PDFs
			}
			docs = append(docs, doc)
		}
	}

	s.log.Info("odc discover", "docs", len(docs))
	return docs, nil
}

// searchPackages queries the CKAN package_search endpoint.
func (s *Source) searchPackages(ctx context.Context, query string) ([]ckanPackage, error) {
	apiURL := fmt.Sprintf("%s/api/3/action/package_search?q=%s&rows=100", s.baseURL, strings.ReplaceAll(query, " ", "+"))
	body, err := s.get(ctx, apiURL)
	if err != nil {
		return nil, fmt.Errorf("odc search %q: %w", query, err)
	}
	var resp ckanResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, fmt.Errorf("odc parse response: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("odc API returned success=false")
	}
	return resp.Result.Results, nil
}

// packageToDoc converts a CKAN package to a DiscoveredDoc.
func packageToDoc(pkg ckanPackage) ingest.DiscoveredDoc {
	doc := ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: pkg.ID,
		Number:     odcDocNumber(pkg),
		Title:      pkg.Title,
		Abstract:   truncate(pkg.Notes, 500),
		DocType:    ingest.DocType(docTypeLabel(rawString(pkg.DocType))),
		Status:     rawString(pkg.LawsStatus),
		DetailURL:  "https://data.opendevelopmentcambodia.net/dataset/" + pkg.Name,
	}

	if pkg.EffectiveDate != "" {
		if t, err := time.Parse("2006-01-02", pkg.EffectiveDate); err == nil {
			doc.EffectiveAt = t
		}
	}

	for _, r := range pkg.Resources {
		ext := strings.ToLower(r.Format)
		if ext != "pdf" && ext != "docx" && ext != "doc" {
			continue
		}
		doc.Files = append(doc.Files, ingest.FileRef{
			URL:      r.URL,
			Name:     r.Name,
			Ext:      ext,
			Kind:     "main",
			MIMEType: mimeForExt(ext),
		})
	}

	return doc
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	// ODC fields are often translated dicts: {"en": "...", "km": "..."}
	var m map[string]string
	if json.Unmarshal(raw, &m) == nil {
		if v := m["en"]; v != "" {
			return v
		}
		if v := m["km"]; v != "" {
			return v
		}
	}
	return ""
}

// isCambodiaOnly returns true if the spatial range is exclusively Cambodia.
// Regional/multi-country documents (ASEAN reports, Mekong comparisons) are
// excluded — each jurisdiction's corpus must be independent.
func isCambodiaOnly(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true // no spatial info = assume Cambodia (it's the Cambodia portal)
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		return len(arr) == 1 && arr[0] == "kh"
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s == "kh"
	}
	return true
}

// odcDocNumber extracts a doc_number: prefer odm_document_number.en, fall back to CKAN name slug.
func odcDocNumber(pkg ckanPackage) string {
	if n := rawString(pkg.DocNumber); n != "" {
		return n
	}
	return pkg.Name
}

// docTypeLabel normalizes ODC document type taxonomy values.
func docTypeLabel(raw string) string {
	if raw == "" {
		return "Legislation"
	}
	return raw
}

// mimeForExt returns a MIME type for a file extension.
func mimeForExt(ext string) string {
	switch ext {
	case "pdf":
		return "application/pdf"
	case "docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case "doc":
		return "application/msword"
	default:
		return ""
	}
}

// truncate limits a string to n bytes, breaking at a space boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if idx := strings.LastIndex(s[:n], " "); idx > 0 {
		return s[:idx]
	}
	return s[:n]
}
