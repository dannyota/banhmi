package vbpl

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"danny.vn/banhmi/pkg/ingest"
)

// docDetailResponse is the GET /doc/{id} envelope. Only the fields banhmi
// consumes are modeled; the rest of data is ignored. Objects that vbpl serves as
// null in some documents (effStatus, docType, documentContent) decode to the zero
// value, so reads are nil-safe without per-field guards.
// detailData is the GET /doc/{id} `data` object. Only the fields banhmi consumes
// are modeled; the rest is preserved verbatim via the bronze `detail_json` raw
// payload (see FetchDetail), so skipped fields can be mined later without a
// re-crawl. Objects vbpl serves as null in some documents (effStatus, docType,
// organization, documentContent) decode to the zero value, so reads are nil-safe.
type detailData struct {
	ID                     string   `json:"id"`
	DocNum                 string   `json:"docNum"`
	Title                  string   `json:"title"`
	IssueDate              string   `json:"issueDate"`
	EffFrom                string   `json:"effFrom"`
	EffTo                  string   `json:"effTo"`
	AgencyName             string   `json:"agencyName"`
	Organization           codeName `json:"organization"` // stable issuer code/name ({code,name} OR null)
	EffStatus              codeName `json:"effStatus"`    // {code,name} OR null — parse defensively
	DocType                codeName `json:"docType"`      // {code,name} OR null
	HasContent             bool     `json:"hasContent"`
	IsConsolidatedDocument bool     `json:"isConsolidatedDocument"`
	// DocumentContent.content is the born-digital body HTML carried inline; we
	// keep it in DiscoveredDoc.HTML rather than downloading the *_content.html.
	DocumentContent struct {
		Content string `json:"content"`
	} `json:"documentContent"`
	References []vbplReference `json:"references"`
}

type vbplReference struct {
	ReferenceType int           `json:"referenceType"`
	Target        vbplRefTarget `json:"targetDocument"`
}

type vbplRefTarget struct {
	ID     json.RawMessage `json:"id"`
	DocNum string          `json:"docNum"`
	Title  string          `json:"title"`
}

// fileEntry is one descriptor from the files endpoint. presignedUrl is a
// ready-to-GET S3 URL (FPT Cloud, ~24h expiry). relatedType is informative:
// 1=official source/original file, 2=downloadable legal/support file, 4/5=HTML
// bodies. Main-vs-appendix/scan still comes from filename and extension.
type fileEntry struct {
	FileName     string `json:"fileName"`
	PresignedURL string `json:"presignedUrl"`
	Size         int64  `json:"size"`
	RelatedType  int    `json:"relatedType"`
}

type filesResponse struct {
	Success bool        `json:"success"`
	Data    []fileEntry `json:"data"`
}

type diagramResponse struct {
	Success bool `json:"success"`
	Data    struct {
		DocumentNamesByType map[string][]diagramDocument `json:"documentNamesByType"`
	} `json:"data"`
}

type diagramDocument struct {
	ID   json.RawMessage `json:"id"`
	Name string          `json:"name"`
}

type provisionTreeResponse struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
}

// FetchDetail fetches a document's metadata and file list from the vbpl JSON API
// using the opaque id returned by doc/all. The id can be a legacy numeric ItemID
// or a UUID, so it is never parsed as a number. DetailURL is only a fallback for
// ad-hoc callers that have not come through Discover.
func (s *Source) FetchDetail(ctx context.Context, ref ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	id, err := docID(ref)
	if err != nil {
		return nil, err
	}

	// Decode twice: capture the verbatim `data` object as raw JSON (preserved to
	// bronze so fields we don't yet map — signer, organization, referenceProvisions,
	// flags — can be mined later without re-crawling), then into the typed struct.
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := s.getJSON(ctx, apiBase+"/doc/"+id, &envelope); err != nil {
		return nil, fmt.Errorf("fetch detail %s: %w", id, err)
	}
	var d detailData
	if err := json.Unmarshal(envelope.Data, &d); err != nil {
		return nil, fmt.Errorf("decode detail %s: %w", id, err)
	}

	var files filesResponse
	filesURL := apiBase + "/doc/minio/buckets/vbpl/folders/" + id + "/files?parts=1,2,3,4,5"
	if err := s.getJSON(ctx, filesURL, &files); err != nil {
		return nil, fmt.Errorf("fetch files %s: %w", id, err)
	}

	var diagram diagramResponse
	if err := s.getJSON(ctx, apiBase+"/doc/"+id+"/diagram", &diagram); err != nil {
		return nil, fmt.Errorf("fetch diagram %s: %w", id, err)
	}
	if !diagram.Success {
		return nil, fmt.Errorf("fetch diagram %s: unsuccessful response", id)
	}

	doc := &ingest.DiscoveredDoc{
		SourceID:       SourceID,
		ExternalID:     id,
		DocGUID:        d.ID,
		Number:         d.DocNum,
		Title:          d.Title,
		DocType:        ingest.DocType(d.DocType.Name),
		DocTypeCode:    d.DocType.Code,
		Issuer:         d.AgencyName,
		IssuerCode:     strings.TrimSpace(d.Organization.Code), // stable issuer identity (vs. fuzzy agencyName)
		Status:         d.EffStatus.Code,                       // CHL / HHL / HHL1P … ("" when effStatus is null)
		IssuedAt:       parseDate(d.IssueDate),
		EffectiveAt:    parseDate(d.EffFrom),
		ExpireAt:       parseDate(d.EffTo),
		DetailURL:      detailURL(id),
		HTML:           d.DocumentContent.Content,
		Files:          preferredFiles(files.Data),
		Relations:      mergeVBPLRelations(s.vbplRelations(d.References), s.vbplDiagramRelations(diagram.Data.DocumentNamesByType)),
		HasContent:     d.HasContent || strings.TrimSpace(d.DocumentContent.Content) != "",
		IsConsolidated: d.IsConsolidatedDocument,
		RawMeta:        detailRawMeta(envelope.Data),
	}
	return doc, nil
}

// detailRawMeta returns the verbatim detail `data` object minus the bulky inline
// HTML body (documentContent — kept in DiscoveredDoc.HTML / the content_html
// payload, so duplicating ~130KB per doc here would only bloat storage). Returns
// nil on malformed input so a fetch is never failed over preservation. Persisting
// this keeps signer, organization, referenceProvisions, publicDate, the document
// taxonomy, and other unmapped fields minable later without re-crawling.
func detailRawMeta(data json.RawMessage) json.RawMessage {
	if len(data) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	delete(m, "documentContent")
	out, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return out
}

func (s *Source) vbplRelations(refs []vbplReference) []ingest.Relation {
	out := make([]ingest.Relation, 0, len(refs))
	for _, ref := range refs {
		targetNumber := strings.TrimSpace(ref.Target.DocNum)
		targetID := rawJSONID(ref.Target.ID)
		if targetNumber == "" && targetID == "" {
			continue
		}
		targetURL := ""
		if targetID != "" {
			targetURL = detailURL(targetID)
		}
		out = append(out, ingest.Relation{
			Type:         s.relationLabel(ref.ReferenceType),
			TypeRaw:      ref.ReferenceType,
			TargetNumber: targetNumber,
			TargetID:     targetID,
			TargetTitle:  strings.TrimSpace(ref.Target.Title),
			TargetURL:    targetURL,
		})
	}
	return out
}

func (s *Source) vbplDiagramRelations(byType map[string][]diagramDocument) []ingest.Relation {
	var out []ingest.Relation
	for rawType, docs := range byType {
		referenceType, err := strconv.Atoi(rawType)
		if err != nil {
			continue
		}
		for _, doc := range docs {
			targetID := rawJSONID(doc.ID)
			targetTitle := strings.TrimSpace(doc.Name)
			targetNumber := docNumberFromDiagramName(targetTitle)
			if targetNumber == "" && targetID == "" {
				continue
			}
			targetURL := ""
			if targetID != "" {
				targetURL = detailURL(targetID)
			}
			out = append(out, ingest.Relation{
				Type:         s.relationLabel(referenceType),
				TypeRaw:      referenceType,
				TargetNumber: targetNumber,
				TargetID:     targetID,
				TargetTitle:  targetTitle,
				TargetURL:    targetURL,
			})
		}
	}
	return out
}

func mergeVBPLRelations(groups ...[]ingest.Relation) []ingest.Relation {
	seen := map[string]bool{}
	var out []ingest.Relation
	for _, group := range groups {
		for _, rel := range group {
			keys := vbplRelationKeys(rel)
			if len(keys) == 0 || anySeen(seen, keys) {
				continue
			}
			for _, key := range keys {
				seen[key] = true
			}
			out = append(out, rel)
		}
	}
	return out
}

func anySeen(seen map[string]bool, keys []string) bool {
	for _, key := range keys {
		if seen[key] {
			return true
		}
	}
	return false
}

func vbplRelationKeys(rel ingest.Relation) []string {
	typeKey := rel.Type
	if rel.TypeRaw != 0 {
		typeKey = strconv.Itoa(rel.TypeRaw)
	}
	typeKey = strings.TrimSpace(typeKey)
	if typeKey == "" {
		return nil
	}
	if targetID := strings.TrimSpace(rel.TargetID); targetID != "" {
		return []string{typeKey + "|id|" + targetID}
	}
	if targetNumber := canonicalVBPLDocNumber(rel.TargetNumber); targetNumber != "" {
		return []string{typeKey + "|num|" + targetNumber}
	}
	if targetTitle := strings.TrimSpace(rel.TargetTitle); targetTitle != "" {
		return []string{typeKey + "|title|" + strings.ToUpper(targetTitle)}
	}
	return nil
}

// relationLabel resolves a vbpl referenceType code to a relation_type label via
// the operator-editable config.relation_type map, falling back to the built-in
// vbplReferenceType defaults when the map lacks the code or is unset.
func (s *Source) relationLabel(t int) string {
	if s.relationTypes != nil {
		if label, ok := s.relationTypes[t]; ok && strings.TrimSpace(label) != "" {
			return label
		}
	}
	return vbplReferenceType(t)
}

func vbplReferenceType(t int) string {
	switch t {
	case 3:
		return "legal_basis"
	case 10:
		return "amends_supplements"
	case 12:
		return "replaces"
	default:
		return fmt.Sprintf("vbpl_type_%d", t)
	}
}

func rawJSONID(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var out string
		if err := json.Unmarshal(raw, &out); err == nil {
			return strings.TrimSpace(out)
		}
	}
	return strings.TrimSpace(s)
}

var diagramDocNumberRe = regexp.MustCompile(`(?i)(?:\b\d{1,4}\s*/\s*\d{4}\s*/\s*[\pL\d]+(?:\s*-\s*[\pL\d]+)*|\b\d{1,4}\s*/\s*[\pL][\pL\d]*(?:\s*-\s*[\pL\d]+)*)`)

func docNumberFromDiagramName(name string) string {
	match := diagramDocNumberRe.FindString(name)
	return canonicalVBPLDocNumber(match)
}

func canonicalVBPLDocNumber(number string) string {
	number = strings.TrimSpace(number)
	if number == "" {
		return ""
	}
	number = regexp.MustCompile(`\s*([/-])\s*`).ReplaceAllString(number, "$1")
	number = strings.Trim(number, " \t\r\n,.;:()[]{}")
	return strings.ToUpper(number)
}

// FetchTree fetches the official VBPL Điều/Khoản provision tree. VBPL returns an
// empty array for some recently published or placeholder documents; that is not a
// hard failure, because Normalize can fall back to the official HTML/DOCX text.
func (s *Source) FetchTree(ctx context.Context, ref ingest.DetailRef) (string, bool, error) {
	id, err := docID(ref)
	if err != nil {
		return "", false, err
	}

	var tree provisionTreeResponse
	if err := s.getJSON(ctx, apiBase+"/doc/provision/tree/"+id, &tree); err != nil {
		return "", false, fmt.Errorf("fetch provision tree %s: %w", id, err)
	}
	if !tree.Success {
		return "", false, fmt.Errorf("fetch provision tree %s: unsuccessful response", id)
	}

	content := strings.TrimSpace(string(tree.Data))
	switch content {
	case "", "null", "[]":
		return "", false, nil
	}
	var nodes []json.RawMessage
	if err := json.Unmarshal(tree.Data, &nodes); err != nil {
		return "", false, fmt.Errorf("decode provision tree %s: %w", id, err)
	}
	if len(nodes) == 0 {
		return "", false, nil
	}
	return content, true, nil
}

// docID returns the source API id to use for vbpl detail calls. The Discover
// response already gives this id as text; it may be numeric or UUID, and the
// gateway accepts either verbatim. DetailURL parsing is only a compatibility
// fallback for one-off calls.
func docID(ref ingest.DetailRef) (string, error) {
	if id := strings.TrimSpace(ref.ExternalID); id != "" {
		return id, nil
	}
	return parseDocID(ref.DetailURL)
}

// parseDocID extracts the document id — the last path segment — from a vbpl
// detail URL (…/van-ban/chi-tiet/{id}). The id is either a legacy numeric ItemID
// (…/chi-tiet/144532) or the newer UUID form (…/chi-tiet/835e3190-54dd-…); the
// gateway's doc/{id} endpoint accepts either verbatim, so the segment is used
// as-is — extracting a substring (e.g. a trailing numeric run) would silently
// resolve a UUID to the wrong document. Query and fragment are stripped first.
func parseDocID(detailURL string) (string, error) {
	s := strings.TrimSpace(detailURL)
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimRight(s, "/")
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	if s == "" {
		return "", fmt.Errorf("parse doc id from %q: no id segment", detailURL)
	}
	return s, nil
}

// preferredFiles selects every downloadable legal file banhmi can preserve:
// DOCX, legacy DOC, and PDF. It excludes the inline HTML file (already carried
// in DiscoveredDoc.HTML). Legacy .doc is preserved as source evidence and can be
// rendered through the LibreOffice PDF bridge during extraction. When a document
// has DOCX/DOC/HTML and a scanned "Văn bản gốc" PDF, both are kept: Fetch
// preserves source evidence, while Extract later chooses DOCX -> HTML -> DOC ->
// PDF/OCR for extraction quality. Text files are ordered main body first, then
// support material, then PDF, so ordinal 0 remains the preferred text evidence
// when a main DOCX/DOC exists.
func preferredFiles(entries []fileEntry) []ingest.FileRef {
	var textFiles, pdf []ingest.FileRef
	for _, e := range entries {
		if strings.TrimSpace(e.PresignedURL) == "" {
			continue
		}
		ext := fileExt(e.FileName)
		ref := ingest.FileRef{
			URL:      e.PresignedURL,
			Name:     e.FileName,
			Ext:      ext,
			Kind:     fileKind(e.FileName, ext),
			MIMEType: mimeForExt(ext),
		}
		switch ext {
		case "docx", "doc":
			textFiles = append(textFiles, ref)
		case "pdf":
			pdf = append(pdf, ref)
		default:
			// Inline *.html and anything else: skip.
		}
	}
	sortTextFiles(textFiles)
	sortFilesMainFirst(pdf)
	out := append([]ingest.FileRef{}, textFiles...)
	return append(out, pdf...)
}

func sortTextFiles(files []ingest.FileRef) {
	sort.SliceStable(files, func(i, j int) bool {
		if ri, rj := fileKindRank(files[i].Kind), fileKindRank(files[j].Kind); ri != rj {
			return ri < rj
		}
		return fileFormatRank(files[i].Ext) < fileFormatRank(files[j].Ext)
	})
}

func sortFilesMainFirst(files []ingest.FileRef) {
	sort.SliceStable(files, func(i, j int) bool {
		return fileKindRank(files[i].Kind) < fileKindRank(files[j].Kind)
	})
}

func fileFormatRank(ext string) int {
	switch ext {
	case "docx":
		return 0
	case "doc":
		return 1
	case "pdf":
		return 2
	default:
		return 9
	}
}

func fileKindRank(kind string) int {
	switch kind {
	case "main":
		return 0
	case "appendix":
		return 1
	case "attachment":
		return 2
	case "version_snapshot":
		return 3
	case "original_scan":
		return 4
	default:
		return 9
	}
}

func fileKind(name, ext string) string {
	if isAppendixName(name) {
		return "appendix"
	}
	if ext == "pdf" {
		return "original_scan"
	}
	return "main"
}

// isAppendixName reports whether a file is an appendix/attachment rather than the
// main body. VBPL names appendices with both accented and unaccented Vietnamese
// ("Phụ lục", "Phu luc"), forms ("Biểu mẫu"), and attached lists ("Danh mục …
// ban hành kèm theo"). The main body normally carries the document type
// ("Thông tư …", "Nghị định …").
func isAppendixName(name string) bool {
	n := foldVietnamese(name)
	return strings.Contains(n, "phu luc") ||
		strings.Contains(n, "phuluc") ||
		strings.Contains(n, "bieu mau") ||
		strings.Contains(n, "bieumau") ||
		strings.Contains(n, "mau so") ||
		strings.Contains(n, "danh muc") ||
		strings.Contains(n, "ban hanh kem theo") ||
		strings.Contains(n, "dinh kem")
}

func foldVietnamese(s string) string {
	decomposed := norm.NFD.String(strings.ToLower(s))
	folded := strings.Map(func(r rune) rune {
		switch {
		case r == 'đ':
			return 'd'
		case unicode.Is(unicode.Mn, r):
			return -1
		default:
			return r
		}
	}, decomposed)
	return norm.NFC.String(folded)
}

// fileExt returns the lowercase extension of a file name without the dot
// ("Thông tư 09-2020-TT-NHNN.docx" -> "docx"); empty when there is none.
func fileExt(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return strings.ToLower(name[i+1:])
	}
	return ""
}

// mimeForExt is a best-effort content type for a file extension; empty when
// unknown (the downloaded response's own Content-Type is authoritative).
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
