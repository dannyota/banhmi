package sbvhanoi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"

	"danny.vn/banhmi/pkg/ingest"
)

// FetchDetail fetches one SBV Hanoi legal-document detail page and returns the
// full metadata plus official attached PDF/DOCX files.
func (s *Source) FetchDetail(ctx context.Context, ref ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	id := strings.TrimSpace(ref.ExternalID)
	if id == "" {
		id = detailID(ref.DetailURL)
	}
	if id == "" {
		return nil, fmt.Errorf("fetch detail: empty external id")
	}
	detailURL := canonicalDetailURL(s.baseURL, id)
	resp, err := s.get(ctx, detailURL)
	if err != nil {
		return nil, fmt.Errorf("fetch detail %s: %w", id, err)
	}
	defer drainClose(resp.Body)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read detail %s: %w", id, err)
	}
	return parseDetailPage(string(body), s.baseURL, id, detailURL), nil
}

func parseDetailPage(htmlText, baseURL, id, detailURL string) *ingest.DiscoveredDoc {
	doc := &ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: id,
		DetailURL:  detailURL,
	}
	for _, row := range splitDetailRows(htmlText) {
		cells := splitHTMLCells(row)
		if len(cells) < 2 {
			continue
		}
		label := normalizeLabel(cleanText(cells[0]))
		value := cleanText(cells[1])
		if value == "" {
			continue
		}
		switch label {
		case "so/ki hieu", "so/ky hieu":
			doc.Number = value
		case "ngay ban hanh":
			doc.IssuedAt = parseVNDate(value)
			doc.PublishedAt = doc.IssuedAt
		case "ngay co hieu luc":
			doc.EffectiveAt = parseVNDate(value)
		case "nguoi ky":
			doc.Signer = value
		case "trich yeu":
			doc.Title = value
			doc.Abstract = value
		case "co quan ban hanh":
			doc.Issuer = value
		case "the loai":
			doc.DocType = ingest.DocType(value)
		}
	}
	if doc.DocType == "" {
		doc.DocType = docTypeFromNumber(doc.Number, doc.Title)
	}
	if doc.Issuer == "" {
		doc.Issuer = "NHNN Việt Nam"
	}
	doc.Files = parseAttachmentFiles(htmlText, baseURL)
	doc.RawMeta = detailRawMeta(doc)
	return doc
}

func splitDetailRows(htmlText string) []string {
	body := htmlText
	if i := strings.Index(body, `<div class="vbpq-detail">`); i >= 0 {
		body = body[i:]
	}
	if i := strings.Index(body, `</table>`); i >= 0 {
		body = body[:i]
	}
	return splitHTMLRows(body)
}

func parseAttachmentFiles(htmlText, baseURL string) []ingest.FileRef {
	block := attachmentBlock(htmlText)
	var refs []ingest.FileRef
	seen := map[string]bool{}
	for _, href := range hrefs(block) {
		abs := absoluteURL(baseURL, href)
		if seen[abs] || !strings.Contains(abs, "/documents/") {
			continue
		}
		seen[abs] = true
		name := nameFromDocumentURL(abs)
		ext := fileExt(name)
		switch ext {
		case "pdf", "docx":
			refs = append(refs, ingest.FileRef{
				URL:      abs,
				Name:     name,
				Ext:      ext,
				Kind:     fileKind(name, ext),
				MIMEType: mimeForExt(ext),
			})
		default:
			// Legacy .doc is preserved by the source but not extractable by the
			// current deterministic pipeline; skip until a converter is added.
		}
	}
	return refs
}

func attachmentBlock(htmlText string) string {
	i := strings.Index(htmlText, "Tài liệu đính kèm")
	if i < 0 {
		return ""
	}
	block := htmlText[i:]
	if j := strings.Index(block, "Văn bản khác"); j >= 0 {
		block = block[:j]
	}
	return block
}

func hrefs(htmlText string) []string {
	var out []string
	for {
		i := strings.Index(htmlText, "<a ")
		if i < 0 {
			return out
		}
		htmlText = htmlText[i:]
		h := strings.Index(htmlText, `href="`)
		if h < 0 {
			htmlText = htmlText[2:]
			continue
		}
		h += len(`href="`)
		rest := htmlText[h:]
		end := strings.IndexByte(rest, '"')
		if end < 0 {
			return out
		}
		out = append(out, rest[:end])
		htmlText = rest[end:]
	}
}

func absoluteURL(baseURL, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err == nil && u.IsAbs() {
		return u.String()
	}
	base, err := url.Parse(strings.TrimRight(baseURL, "/") + "/")
	if err != nil {
		return raw
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return base.ResolveReference(ref).String()
}

func nameFromDocumentURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return path.Base(raw)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		p, _ := url.PathUnescape(parts[i])
		if ext := fileExt(p); ext != "" {
			return p
		}
	}
	return path.Base(u.Path)
}

func fileExt(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return strings.Trim(name[i+1:], " /")
	}
	return ""
}

func fileKind(name, ext string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "phụ lục"), strings.Contains(n, "phu_luc"),
		strings.Contains(n, "phu luc"), strings.Contains(n, "bieu_mau"),
		strings.Contains(n, "biểu mẫu"), strings.Contains(n, "appendix"):
		return "appendix"
	case ext == "pdf":
		return "main"
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
		Files       []ingest.FileRef `json:"files,omitempty"`
		DetailURL   string           `json:"detail_url,omitempty"`
		IssuedAt    string           `json:"issued_at,omitempty"`
		EffectiveAt string           `json:"effective_at,omitempty"`
	}{
		ID:          doc.ExternalID,
		Number:      doc.Number,
		Title:       doc.Title,
		DocType:     doc.DocType,
		Issuer:      doc.Issuer,
		Signer:      doc.Signer,
		Files:       doc.Files,
		DetailURL:   doc.DetailURL,
		IssuedAt:    dateString(doc.IssuedAt),
		EffectiveAt: dateString(doc.EffectiveAt),
	})
	return raw
}
