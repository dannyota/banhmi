package bi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// apiResponse is the BI JSON API envelope.
type apiResponse struct {
	Data       json.RawMessage `json:"Data"`
	StatusCode int             `json:"StatusCode"`
	Message    string          `json:"Message"`
}

// apiDetail maps the Data object from GetDataWebPeraturan. All fields are
// treated as nullable/variant: strings use *string, dates use *string (parsed
// defensively), and numeric IDs use *int. This is the lesson from vbpl's
// effStatus — never assume a field is non-null from a few samples.
type apiDetail struct {
	PeraturanID    *int    `json:"PeraturanID"`
	Judul          *string `json:"Judul"`
	Teu            *string `json:"Teu"`
	NomorPeraturan *string `json:"NomorPeraturan"`

	SingkatanJenisPeraturan *string `json:"SingkatanJenisPeraturan"`
	JenisPeraturanID        *int    `json:"JenisPeraturanID"`
	JenisPeraturanDesc      *string `json:"JenisPeraturanDesc"`

	IdTaksonomi   *int    `json:"IdTaksonomi"`
	TaksonomiDesc *string `json:"TaksonomiDesc"`

	TempatPenetapan     *string `json:"TempatPenetapan"`
	TanggalPenetapan    *string `json:"TanggalPenetapan"`
	TanggalPengundangan *string `json:"TanggalPengundangan"`
	TanggalBerlaku      *string `json:"TanggalBerlaku"`

	Subjek *string `json:"Subjek"`
	Sumber *string `json:"Sumber"`
	Status *string `json:"Status"`

	// Forward relation fields (authoritative — emitted as Relations).
	Mengubah *string `json:"Mengubah"` // amends: semicolon-delimited numbers
	Mencabut *string `json:"Mencabut"` // revokes: semicolon-delimited numbers

	// Reverse relation fields (NOT authoritative — kept in RawMeta only).
	Diubah           *string `json:"Diubah"`
	Dicabut          *string `json:"Dicabut"`
	PeraturanTerkait *string `json:"PeraturanTerkait"`

	Hit         *int  `json:"Hit"`
	HitDownload *int  `json:"HitDownload"`
	IsActive    *bool `json:"IsActive"`

	// Other fields preserved in RawMeta but not mapped to typed columns.
	Ringkasan    *string `json:"Ringkasan"`
	TempatTerbit *string `json:"TempatTerbit"`
	Lokasi       *string `json:"Lokasi"`
}

// FetchDetail calls the BI JSON API for a document and returns the enriched
// DiscoveredDoc with full metadata, relations, and a file reference.
func (s *Source) FetchDetail(ctx context.Context, ref ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	url := apiDetailURL(ref.ExternalID)
	body, err := s.client.Get(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetch detail %s: %w", ref.ExternalID, err)
	}

	var resp apiResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, fmt.Errorf("decode detail %s: %w", ref.ExternalID, err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("detail %s: api status %d: %s", ref.ExternalID, resp.StatusCode, resp.Message)
	}
	if len(resp.Data) == 0 || string(resp.Data) == "null" {
		return nil, fmt.Errorf("detail %s: empty data", ref.ExternalID)
	}

	var d apiDetail
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		return nil, fmt.Errorf("decode detail data %s: %w", ref.ExternalID, err)
	}

	doc := mapDetail(ref.ExternalID, &d, resp.Data)
	return doc, nil
}

// mapDetail converts the API detail into a DiscoveredDoc.
func mapDetail(externalID string, d *apiDetail, rawData json.RawMessage) *ingest.DiscoveredDoc {
	doc := &ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: externalID,
		DetailURL:  detailURL(externalID),
		RawMeta:    rawData,
	}

	// Number: use NomorPeraturan, normalized.
	if d.NomorPeraturan != nil {
		doc.Number = normalizeNumber(*d.NomorPeraturan)
	}

	// Title: Judul is the full title.
	if d.Judul != nil {
		doc.Title = strings.TrimSpace(*d.Judul)
	}

	// DocType from SingkatanJenisPeraturan or JenisPeraturanID.
	if d.SingkatanJenisPeraturan != nil {
		doc.DocType = ingest.DocType(strings.TrimSpace(*d.SingkatanJenisPeraturan))
	} else if d.JenisPeraturanID != nil {
		doc.DocType = docTypeFromJenis(*d.JenisPeraturanID)
	}

	if d.JenisPeraturanID != nil {
		doc.DocTypeCode = fmt.Sprintf("%d", *d.JenisPeraturanID)
	}

	// Issuer: Teu is the issuing body.
	if d.Teu != nil {
		doc.Issuer = strings.TrimSpace(*d.Teu)
	}

	// Dates: parse defensively (nullable ISO datetime strings).
	doc.IssuedAt = parseDate(d.TanggalPenetapan)
	doc.EffectiveAt = parseDate(d.TanggalBerlaku)

	// Status from API — note the caveat: repealed docs can still report "Berlaku".
	// The listing badge status (captured during Discover) is more reliable.
	if d.Status != nil {
		doc.Status = strings.TrimSpace(*d.Status)
	}

	// File: single PDF download.
	doc.Files = []ingest.FileRef{{
		URL:      downloadURL(externalID),
		Name:     fileNameForDoc(doc.Number, doc.DocType),
		Ext:      "pdf",
		Kind:     "main",
		MIMEType: "application/pdf",
	}}

	// Relations: forward fields only (Mengubah = amends, Mencabut = revokes).
	doc.Relations = parseRelations(d)

	return doc
}

// parseRelations extracts forward relations from the API detail. Only Mengubah
// (amends) and Mencabut (revokes) are emitted as authoritative Relations.
// Reverse fields (Diubah, Dicabut) and PeraturanTerkait are in RawMeta only.
func parseRelations(d *apiDetail) []ingest.Relation {
	var rels []ingest.Relation

	for _, num := range splitRelationField(d.Mengubah) {
		rels = append(rels, ingest.Relation{
			Type:         "Mengubah",
			TargetNumber: num,
		})
	}

	for _, num := range splitRelationField(d.Mencabut) {
		rels = append(rels, ingest.Relation{
			Type:         "Mencabut",
			TargetNumber: num,
		})
	}

	return rels
}

// splitRelationField splits a semicolon-delimited relation string into
// individual normalized numbers. Handles trailing semicolons, empty segments,
// and whitespace.
func splitRelationField(s *string) []string {
	if s == nil || *s == "" {
		return nil
	}
	parts := strings.Split(*s, ";")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// normalizeNumber normalizes a BI regulation number for consistent storage.
// BI numbers are inconsistent ("PBI No.10 Tahun 2025" vs "PBI NO.4 TAHUN 2025"
// vs "22/23/PBI/2020"). We normalize case to title case for the type prefix and
// collapse whitespace, but preserve the original format structure (slashes etc.).
func normalizeNumber(s string) string {
	s = strings.TrimSpace(s)
	// Collapse multiple spaces.
	s = spaceRe.ReplaceAllString(s, " ")
	return s
}

// parseDate defensively parses a nullable ISO datetime string. Returns the zero
// time on nil, empty, or unparseable input.
func parseDate(s *string) time.Time {
	if s == nil || *s == "" {
		return time.Time{}
	}
	raw := strings.TrimSpace(*s)

	// Try common ISO formats the API returns.
	for _, layout := range []string{
		"2006-01-02T15:04:05",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05-07:00",
		"2006-01-02T15:04:05.0000000",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

// fileNameForDoc constructs a descriptive filename from the document number and type.
func fileNameForDoc(number string, docType ingest.DocType) string {
	name := strings.TrimSpace(string(docType))
	if number != "" {
		name = strings.ReplaceAll(number, "/", "-")
	}
	return name + ".pdf"
}
