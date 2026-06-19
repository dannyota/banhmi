package vanban

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"danny.vn/banhmi/pkg/ingest"
)

// FetchDetail fetches one vanban document detail page and returns its full
// metadata plus the authoritative born-digital file (signed PDF or DOCX) on the
// datafiles.chinhphu.vn CDN. vanban exposes no relation graph and no effStatus
// badge — only issue/effective dates — so those fields stay empty (vbpl enriches
// them when it later indexes the document).
func (s *Source) FetchDetail(ctx context.Context, ref ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	id := strings.TrimSpace(ref.ExternalID)
	if id == "" {
		if m := docidRe.FindStringSubmatch(ref.DetailURL); m != nil {
			id = m[1]
		}
	}
	if id == "" {
		return nil, fmt.Errorf("fetch detail: empty docid")
	}
	detailURL := canonicalDetailURL(s.baseURL, id)
	htmlText, err := s.do(ctx, detailURL, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch detail %s: %w", id, err)
	}
	return parseDetailPage(htmlText, s.baseURL, id, detailURL), nil
}

func parseDetailPage(htmlText, baseURL, id, detailURL string) *ingest.DiscoveredDoc {
	doc := &ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: id,
		DetailURL:  detailURL,
	}
	for _, row := range splitRows(metaTableRegion(htmlText)) {
		cells := splitCells(row)
		if len(cells) < 2 {
			continue
		}
		label := normalizeLabel(cleanText(cells[0]))
		value := cleanText(cells[1])
		if value == "" {
			continue
		}
		switch label {
		case "so ky hieu":
			doc.Number = value
		case "ngay ban hanh":
			doc.IssuedAt = parseVNDate(value)
			doc.PublishedAt = doc.IssuedAt
		case "ngay co hieu luc":
			doc.EffectiveAt = parseVNDate(value)
		case "ngay het hieu luc":
			doc.ExpireAt = parseVNDate(value)
		case "loai van ban":
			doc.DocType = ingest.DocType(value)
		case "co quan ban hanh":
			doc.Issuer = value
		case "nguoi ky":
			doc.Signer = value
		case "trich yeu":
			doc.Title = value
			doc.Abstract = value
		}
	}
	if doc.DocType == "" {
		doc.DocType = docTypeFromNumber(doc.Number, doc.Title)
	}
	doc.Files = parseDetailFiles(htmlText)
	doc.HasContent = len(doc.Files) > 0
	doc.RawMeta = detailRawMeta(doc)
	return doc
}

// metaTableRegion narrows to the detail "Content" block that holds the metadata
// table, so generic <tr> scanning does not pick up layout tables.
func metaTableRegion(htmlText string) string {
	i := strings.Index(htmlText, `class="Content"`)
	if i < 0 {
		return htmlText
	}
	body := htmlText[i:]
	if j := strings.Index(body, "</table>"); j >= 0 {
		body = body[:j]
	}
	return body
}

// parseDetailFiles collects the born-digital files on the chinhphu CDN
// (datafiles.chinhphu.vn/cpp/files/...). Signed PDFs and DOCX are kept; legacy
// .doc is skipped (not handled by the deterministic pipeline yet, matching
// sbv_hanoi).
func parseDetailFiles(htmlText string) []ingest.FileRef {
	var refs []ingest.FileRef
	seen := map[string]bool{}
	for _, href := range fileHrefs(htmlText) {
		if seen[href] || !strings.Contains(href, "datafiles.chinhphu.vn") || !strings.Contains(href, "/cpp/files/") {
			continue
		}
		name := fileName(href)
		ext := fileExt(name)
		if ext != "pdf" && ext != "docx" {
			continue
		}
		seen[href] = true
		refs = append(refs, ingest.FileRef{
			URL:      href,
			Name:     name,
			Ext:      ext,
			Kind:     fileKind(name, ext),
			MIMEType: mimeForExt(ext),
		})
	}
	return refs
}

func fileHrefs(htmlText string) []string {
	var out []string
	for _, m := range hrefAttrRe.FindAllStringSubmatch(htmlText, -1) {
		out = append(out, strings.TrimSpace(m[1]))
	}
	return out
}

func fileName(rawURL string) string {
	u := rawURL
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	if i := strings.LastIndexByte(u, '/'); i >= 0 {
		u = u[i+1:]
	}
	return u
}

func fileExt(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i+1:]
	}
	return ""
}

func fileKind(name, ext string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "phu_luc"), strings.Contains(n, "phuluc"), strings.Contains(n, "appendix"):
		return "appendix"
	default:
		return "main"
	}
}

func mimeForExt(ext string) string {
	switch ext {
	case "pdf":
		return "application/pdf"
	case "docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	default:
		return ""
	}
}

func detailRawMeta(doc *ingest.DiscoveredDoc) json.RawMessage {
	raw, _ := json.Marshal(struct {
		ID          string           `json:"id"`
		Number      string           `json:"number,omitempty"`
		Title       string           `json:"title,omitempty"`
		DocType     ingest.DocType   `json:"doc_type,omitempty"`
		Issuer      string           `json:"issuer,omitempty"`
		Signer      string           `json:"signer,omitempty"`
		IssuedAt    string           `json:"issued_at,omitempty"`
		EffectiveAt string           `json:"effective_at,omitempty"`
		Files       []ingest.FileRef `json:"files,omitempty"`
		DetailURL   string           `json:"detail_url,omitempty"`
	}{
		ID:          doc.ExternalID,
		Number:      doc.Number,
		Title:       doc.Title,
		DocType:     doc.DocType,
		Issuer:      doc.Issuer,
		Signer:      doc.Signer,
		IssuedAt:    dateString(doc.IssuedAt),
		EffectiveAt: dateString(doc.EffectiveAt),
		Files:       doc.Files,
		DetailURL:   doc.DetailURL,
	})
	return raw
}
