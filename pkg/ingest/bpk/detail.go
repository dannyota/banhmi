package bpk

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// bentukShort maps verbose "Bentuk Singkat" values (from BPK detail pages)
// to the short doc-type codes used by jenisCode in discover.go. The detail
// page's Bentuk Singkat is NOT always the short code — e.g. "Peraturan OJK"
// instead of "POJK". This map normalizes them so the detail path produces the
// same doc_type and doc_number as the listing path.
var bentukShort = map[string]ingest.DocType{
	"UU":             "uu",
	"PP":             "pp",
	"Perpres":        "perpres",
	"PMK":            "pmk",
	"PBI":            "pbi",
	"Peraturan OJK":  "pojk",
	"POJK":           "pojk",
	"SEOJK":          "seojk",
	"SE OJK":         "seojk",
	"Peraturan BSSN": "bssn",
	"PPATK":          "ppatk",
	"LPS":            "lps",
	"Kominfo":        "kominfo",
	"Komdigi":        "komdigi",
}

// indonesianMonths maps Indonesian month names (lowercase) to time.Month.
var indonesianMonths = map[string]time.Month{
	"januari":   time.January,
	"februari":  time.February,
	"maret":     time.March,
	"april":     time.April,
	"mei":       time.May,
	"juni":      time.June,
	"juli":      time.July,
	"agustus":   time.August,
	"september": time.September,
	"oktober":   time.October,
	"november":  time.November,
	"desember":  time.December,
}

// Regex patterns for detail page parsing.
var (
	// metaRowRe matches a metadata label/value pair.
	// <div class="col-lg-3 fw-bold">Label</div>
	// <div class="col-lg-9">Value</div>
	metaRowRe = regexp.MustCompile(`(?is)<div[^>]*class="[^"]*col-lg-3[^"]*fw-bold[^"]*"[^>]*>(.*?)</div>\s*<div[^>]*class="[^"]*col-lg-9[^"]*"[^>]*>(.*?)</div>`)

	// dateRe matches an Indonesian date like "17 Oktober 2022".
	dateRe = regexp.MustCompile(`(\d{1,2})\s+([A-Za-z]+)\s+(\d{4})`)

	// detailFileRe matches download links on the detail page.
	// <a ... class="download-file ..." data-id="224884" href="/Download/224884/UU%20Nomor%2027%20Tahun%202022.pdf">
	detailFileRe = regexp.MustCompile(`(?is)<a[^>]*class="[^"]*download-file[^"]*"[^>]*data-(?:kategori="Peraturan"[^>]*data-)?id="(\d+)"[^>]*href="(/Download/\d+/[^"]+)"[^>]*>`)

	// materiPokokRe extracts the MATERI POKOK abstract paragraph.
	materiPokokRe = regexp.MustCompile(`(?is)MATERI POKOK.*?<div[^>]*class="[^"]*border-bottom[^"]*"[^>]*>.*?</div>\s*<p>(.*?)</p>`)

	// ujiMateriRe extracts UJI MATERI (judicial review) entries.
	ujiMateriRe = regexp.MustCompile(`(?is)UJI MATERI.*?<div[^>]*class="[^"]*border-bottom[^"]*".*?</div>(.*?)(?:</div>\s*</div>\s*</div>)`)

	// ujiMateriEntryRe matches one judicial review entry.
	ujiMateriEntryRe = regexp.MustCompile(`(?is)PUTUSAN\s+Nomor\s+(?:<a[^>]*href="(/DownloadUjiMateri/\d+/[^"]+)"[^>]*>)?([^<]+)(?:</a>)?\s*</div>\s*<div[^>]*>\s*<span>(.*?)</span>`)

	// statusPeraturanRe extracts the STATUS PERATURAN section from the detail page
	// sidebar (col-lg-4 column).
	statusPeraturanRe = regexp.MustCompile(`(?is)STATUS\s+<span[^>]*>PERATURAN</span>.*?<div[^>]*class="[^"]*border-bottom[^"]*"[^>]*>.*?</div>(.*?)(?:</div>\s*</div>\s*</div>)`)

	// belumTersediaRe detects "Belum Tersedia" (not yet available).
	belumTersediaRe = regexp.MustCompile(`(?i)Belum\s+Tersedia`)

	// detailHeaderRe extracts the header type+number line from the detail page.
	// <h4 ...>Undang-undang (UU) Nomor 27 Tahun 2022</h4>
	detailHeaderRe = regexp.MustCompile(`(?is)<h4[^>]*class="[^"]*text-white[^"]*opacity-50[^"]*"[^>]*>\s*(.*?)\s*</h4>`)
)

// FetchDetail fetches the detail page for a document and returns a fully
// enriched DiscoveredDoc with all metadata, dates, status, relations, and files.
func (s *Source) FetchDetail(ctx context.Context, ref ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	u := ref.DetailURL
	if u == "" {
		u = baseURL + "/Details/" + ref.ExternalID
	}

	body, err := s.client.Get(ctx, u)
	if err != nil {
		return nil, err
	}

	return parseDetail(body, ref.ExternalID, u)
}

// parseDetail parses a detail page HTML into a DiscoveredDoc. Exported for testing.
func parseDetail(body, externalID, detailURL string) (*ingest.DiscoveredDoc, error) {
	meta := parseMetadata(body)

	d := &ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: externalID,
		DetailURL:  detailURL,
		Status:     meta["Status"],
	}

	// Metadata fields.
	if v := meta["Judul"]; v != "" {
		d.Title = v
	}
	if v := meta["T.E.U."]; v != "" {
		d.Issuer = v
	}
	if v := meta["Bentuk Singkat"]; v != "" {
		d.DocTypeCode = v
	}
	if v := meta["Subjek"]; v != "" {
		d.Abstract = v
	}

	// Normalize DocType to the short code used by the listing path (jenisCode).
	// The detail page's "Bentuk Singkat" is the most reliable hint; "Bentuk" is
	// the verbose fallback. Both are mapped through bentukShort to produce the
	// same code the listing path uses.
	if dt, ok := bentukShort[meta["Bentuk Singkat"]]; ok {
		d.DocType = dt
	} else if dt, ok := bentukShort[meta["Bentuk"]]; ok {
		d.DocType = dt
	} else if v := meta["Bentuk"]; v != "" {
		d.DocType = ingest.DocType(v)
	}

	// Header type+number line — normalized through parseNumber to produce the
	// same format as the listing path (e.g. "POJK 21/2023" not the verbose
	// "Peraturan Otoritas Jasa Keuangan Nomor 21 Tahun 2023").
	if hm := detailHeaderRe.FindStringSubmatch(body); hm != nil {
		headerText := cleanText(hm[1])
		d.Number = parseNumber(headerText, d.DocType)
	}

	// Dates.
	d.IssuedAt = parseIndonesianDate(meta["Tanggal Penetapan"])
	d.EffectiveAt = parseIndonesianDate(meta["Tanggal Berlaku"])

	// Build Number from metadata if not set from header. Use the short doc-type
	// code (d.DocType, already normalized above) for consistency with the listing.
	if d.Number == "" && meta["Nomor"] != "" && meta["Tahun"] != "" && d.DocType != "" {
		d.Number = strings.ToUpper(string(d.DocType)) + " " + meta["Nomor"] + "/" + meta["Tahun"]
	}

	// MATERI POKOK (abstract). Appended to Abstract, NOT stored in HTML:
	// DiscoveredDoc.HTML means "inline law body" (the vbpl pattern) and would
	// make the extract cascade adopt the abstract as the document text,
	// shadowing the PDF.
	if mm := materiPokokRe.FindStringSubmatch(body); mm != nil {
		if abstract := cleanText(mm[1]); abstract != "" {
			if d.Abstract != "" {
				d.Abstract += " — " + abstract
			} else {
				d.Abstract = abstract
			}
		}
	}

	// Files from FILE-FILE PERATURAN section.
	d.Files = parseDetailFiles(body)

	// STATUS PERATURAN relations.
	d.Relations = parseDetailRelations(body)

	// RawMeta: persist all parsed metadata as JSON.
	rawMeta := make(map[string]string)
	for k, v := range meta {
		rawMeta[k] = v
	}
	// Add UJI MATERI notes.
	if notes := parseUjiMateri(body); len(notes) > 0 {
		rawMeta["uji_materi"] = strings.Join(notes, "; ")
	}
	if b, err := json.Marshal(rawMeta); err == nil {
		d.RawMeta = b
	}

	return d, nil
}

// parseMetadata extracts all label/value pairs from the METADATA PERATURAN table.
func parseMetadata(body string) map[string]string {
	meta := make(map[string]string)
	for _, m := range metaRowRe.FindAllStringSubmatch(body, -1) {
		label := cleanText(m[1])
		value := cleanText(m[2])
		if label != "" {
			meta[label] = value
		}
	}
	return meta
}

// parseIndonesianDate parses a date string in Indonesian format "17 Oktober 2022".
func parseIndonesianDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	m := dateRe.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}
	}
	day := 0
	for _, c := range m[1] {
		day = day*10 + int(c-'0')
	}
	month, ok := indonesianMonths[strings.ToLower(m[2])]
	if !ok {
		return time.Time{}
	}
	year := 0
	for _, c := range m[3] {
		year = year*10 + int(c-'0')
	}
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// parseDetailFiles extracts download file links from the detail page.
func parseDetailFiles(body string) []ingest.FileRef {
	var files []ingest.FileRef
	seen := map[string]bool{}
	for _, m := range detailFileRe.FindAllStringSubmatch(body, -1) {
		href := m[2]
		if seen[href] {
			continue
		}
		seen[href] = true
		name := fileNameFromHref(href)
		files = append(files, ingest.FileRef{
			URL:      baseURL + href,
			Name:     name,
			Ext:      fileExt(name),
			Kind:     "main",
			MIMEType: "application/pdf",
		})
	}
	return files
}

// parseDetailRelations extracts typed relations from the STATUS PERATURAN section.
func parseDetailRelations(body string) []ingest.Relation {
	sm := statusPeraturanRe.FindStringSubmatch(body)
	if sm == nil {
		return nil
	}
	block := sm[1]
	if belumTersediaRe.MatchString(block) {
		return nil
	}
	return parseInlineRelations(block)
}

// parseUjiMateri extracts judicial review notes from the UJI MATERI section.
func parseUjiMateri(body string) []string {
	um := ujiMateriRe.FindStringSubmatch(body)
	if um == nil {
		return nil
	}
	block := um[1]
	if belumTersediaRe.MatchString(block) {
		return nil
	}
	var notes []string
	for _, m := range ujiMateriEntryRe.FindAllStringSubmatch(block, -1) {
		number := cleanText(m[2])
		summary := cleanText(m[3])
		note := "Putusan " + number
		if summary != "" {
			note += ": " + summary
		}
		notes = append(notes, note)
	}
	return notes
}
