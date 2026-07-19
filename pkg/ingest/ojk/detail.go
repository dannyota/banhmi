package ojk

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// Detail page parse patterns (live-verified 2026-07-12). The metadata table is
// three-cell rows: <th><h4>Label</h4></th> <td><label>:</label></td>
// <td><label>Value</label></td>. Riwayat Peraturan and Dokumen are separate
// <h2>-headed sections.
var (
	// metaRowRe matches one metadata row; the value stops at the first
	// </label>, which is the value label's own close (inner <a> links, as in
	// Landasan Hukum, have no nested labels).
	metaRowRe = regexp.MustCompile(`(?is)<tr>\s*<th[^>]*><h4>(.*?)</h4></th>\s*<td[^>]*><label[^>]*>:</label></td>\s*<td[^>]*><label[^>]*>(.*?)</label>`)

	// sectionRe captures each <h2 class="card-label">-headed section's title;
	// used to slice out the Riwayat Peraturan and Dokumen bodies.
	sectionRe = regexp.MustCompile(`(?is)<h2><span class="card-label[^"]*">\s*(.*?)\s*</span></h2>`)

	// riwayatGroupRe matches a relation-group label inside Riwayat Peraturan:
	// <span class="text-primary fw-bolder fs-6">Dicabut :</span>
	riwayatGroupRe = regexp.MustCompile(`(?is)<span class="text-primary fw-bolder fs-6">\s*([^<:]+?)\s*:?\s*</span>`)

	// riwayatTargetRe matches one related regulation inside a group:
	// <a class="text-danger" href="/Web/ViewPeraturan/DownloadFileRiwayat/1286">POJK Nomor 4 Tahun 2021</a>
	riwayatTargetRe = regexp.MustCompile(`(?is)<a[^>]*class="[^"]*text-danger[^"]*"[^>]*href="(/Web/ViewPeraturan/DownloadFileRiwayat/\d+)"[^>]*>\s*(.*?)\s*</a>`)

	// belumAdaRe detects the empty-history marker "Keterangan Status/Riwayat Belum Ada".
	belumAdaRe = regexp.MustCompile(`(?i)Riwayat\s+Belum\s+Ada`)

	// landasanLinkRe matches one Landasan Hukum entry inside the metadata value:
	// <a href='/peraturan/peraturan/downloadfilelandasan/2327'>1. UU Nomor 25 Tahun 1992 tentang Perkoperasian</a>
	// The live HTML sometimes omits the final </a>, so an entry ends at the
	// next tag or at the end of the value.
	landasanLinkRe = regexp.MustCompile(`(?is)<a[^>]*href='([^']*downloadfilelandasan/\d+)'[^>]*>\s*(?:\d+\.\s*)?([^<]+)`)

	// dokumenRowRe matches one Dokumen-section row: kind label
	// (Abstrak/FAQ/Peraturan) and the row body.
	dokumenRowRe = regexp.MustCompile(`(?is)<tr>\s*<th[^>]*><h4>(.*?)</h4></th>(.*?)</tr>`)

	// dokumenFileRe extracts a file's UUID and advertised name from a row body:
	// href="/Web/ViewPeraturan/DownloadDokumen/{uuid}" ... onclick="downloadDokumen('name.pdf', ...)"
	dokumenFileRe = regexp.MustCompile(`(?is)DownloadDokumen/([0-9a-fA-F-]+)"[^>]*onclick="downloadDokumen\('([^']+)'`)

	// statusSejakRe splits "Berlaku Sejak Tanggal 31-12-2024" into the status
	// proper and the effective date.
	statusSejakRe = regexp.MustCompile(`(?i)^(.*?)\s*Sejak\s+Tanggal\s+(\d{2}-\d{2}-\d{4})$`)

	// detailJenisRe pulls the trailing jenis code from a detail URL
	// (.../Detail/{uuid}/{sektor}/{jenis} — sektor may be empty).
	detailJenisRe = regexp.MustCompile(`/Detail/[^/]+/[^/]*/([^/?#]+)`)
)

// FetchDetail fetches the server-rendered detail page for a document and
// returns the enriched DiscoveredDoc with full metadata, relations, and file
// references.
func (s *Source) FetchDetail(ctx context.Context, ref ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	u := ref.DetailURL
	if u == "" {
		return nil, fmt.Errorf("fetch detail %s: no detail url", ref.ExternalID)
	}

	body, err := s.client.Get(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("fetch detail %s: %w", ref.ExternalID, err)
	}

	// F5 BIG-IP WAF returns HTTP 200 with "Request Rejected" HTML instead
	// of a proper challenge status. Detect and error so the pipeline retries
	// rather than silently storing empty metadata.
	if strings.Contains(body, "<title>Request Rejected</title>") {
		return nil, fmt.Errorf("fetch detail %s: WAF request rejected", ref.ExternalID)
	}

	return parseDetail(body, ref.ExternalID, u)
}

// parseDetail parses a detail page HTML into a DiscoveredDoc.
func parseDetail(body, externalID, pageURL string) (*ingest.DiscoveredDoc, error) {
	meta := parseMetadata(body)

	doc := &ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: externalID,
		DetailURL:  pageURL,
	}

	// DocType must be set before Number so bpkFormatNumber can construct the
	// BPK-compatible doc_number. The short form (POJK/SEOJK) is mapped to
	// BPK's canonical long labels for doc_key dedup.
	if v := meta["Singkatan Jenis/Bentuk Peraturan"]; v != "" {
		doc.DocType = ingest.DocType(ojkToBPKDocType(v))
	} else if v := meta["Jenis/Bentuk Peraturan"]; v != "" {
		doc.DocType = ingest.DocType(ojkToBPKDocType(v))
	}

	// Title & number: Judul is the full official title; the number embedded in
	// it ("73/POJK.05/2016" or "47 Tahun 2024") is richer than the bare-digit
	// "Nomor Peraturan" field, which is only the fallback. The short number
	// is then formatted into BPK's canonical form for doc_key alignment.
	judul := meta["Judul"]
	doc.Title = judul
	if shortNum, _ := splitNumberTitle(judul); shortNum != "" {
		doc.Number = bpkFormatNumber(shortNum, doc.DocType)
	} else if v := meta["Nomor Peraturan"]; v != "" {
		doc.Number = v
	}
	// DocTypeCode: the numeric jenis code from the detail URL's last segment.
	if m := detailJenisRe.FindStringSubmatch(pageURL); m != nil {
		doc.DocTypeCode = m[1]
	}

	// Issuer from T.E.U Badan (e.g. "Indonesia.Otoritas Jasa Keuangan").
	doc.Issuer = meta["T.E.U Badan"]

	// Subjek is the closest to an abstract (bpk maps Subjek the same way).
	doc.Abstract = meta["Subjek"]

	// Dates (dd-mm-yyyy). Tanggal Pengundangan (promulgation) is the
	// publication watermark, NOT the effective date.
	doc.IssuedAt = parseListDate(meta["Tanggal Penetapan"])
	doc.PublishedAt = parseListDate(meta["Tanggal Pengundangan"])

	// Status: carry the raw string ("Berlaku", "Tidak Berlaku", "Berlaku
	// (Dicabut Sebagian)", ...) so partial repeal stays distinguishable from
	// full repeal. A "Sejak Tanggal dd-mm-yyyy" suffix is split off into
	// EffectiveAt; the unsplit value is preserved in RawMeta.
	doc.Status, doc.EffectiveAt = splitStatus(meta["Status Peraturan"])

	// Files from the Dokumen section (per-file UUIDs, distinct from the doc UUID).
	doc.Files = parseDokumen(sectionBody(body, "Dokumen"))

	// Relations: Riwayat Peraturan groups + Landasan Hukum (legal basis).
	doc.Relations = parseRiwayat(sectionBody(body, "Riwayat Peraturan"))
	doc.Relations = append(doc.Relations, parseLandasan(rawMetaValue(body, "Landasan Hukum"))...)

	// RawMeta: all parsed metadata rows.
	if b, err := json.Marshal(meta); err == nil {
		doc.RawMeta = b
	}

	return doc, nil
}

// parseMetadata extracts all label/value pairs from the metadata table.
// Values are tag-stripped and whitespace-collapsed.
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

// rawMetaValue returns the raw (un-stripped) HTML of one metadata row's value,
// for fields whose inner links matter (Landasan Hukum).
func rawMetaValue(body, label string) string {
	for _, m := range metaRowRe.FindAllStringSubmatch(body, -1) {
		if cleanText(m[1]) == label {
			return m[2]
		}
	}
	return ""
}

// sectionBody returns the HTML between the named <h2 card-label> section
// heading and the next section heading (or end of document).
func sectionBody(body, title string) string {
	locs := sectionRe.FindAllStringSubmatchIndex(body, -1)
	for i, loc := range locs {
		name := cleanText(body[loc[2]:loc[3]])
		if !strings.EqualFold(name, title) {
			continue
		}
		end := len(body)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		return body[loc[1]:end]
	}
	return ""
}

// parseRiwayat extracts typed relations from the Riwayat Peraturan section.
// Group labels are the source's own words viewed from this document — e.g.
// "Dicabut" (repealed by), "Diubah" (amended by) — and are carried raw. Each
// target links to a DownloadFileRiwayat PDF (a numeric file id, not a document
// UUID), so TargetURL is set and TargetID left empty.
func parseRiwayat(section string) []ingest.Relation {
	if section == "" || belumAdaRe.MatchString(section) {
		return nil
	}

	groups := riwayatGroupRe.FindAllStringSubmatchIndex(section, -1)
	var out []ingest.Relation
	for i, loc := range groups {
		relType := cleanText(section[loc[2]:loc[3]])
		end := len(section)
		if i+1 < len(groups) {
			end = groups[i+1][0]
		}
		segment := section[loc[1]:end]

		for _, tm := range riwayatTargetRe.FindAllStringSubmatch(segment, -1) {
			out = append(out, ingest.Relation{
				Type:         relType,
				TargetNumber: cleanText(tm[2]),
				TargetURL:    baseURL + tm[1],
			})
		}
	}
	return out
}

// parseLandasan extracts "Landasan Hukum" (legal basis) relations from the raw
// metadata value HTML. Each entry reads "N. UU Nomor 21 Tahun 2011 tentang
// Otoritas Jasa Keuangan"; number and title split on "tentang".
func parseLandasan(rawValue string) []ingest.Relation {
	var out []ingest.Relation
	for _, m := range landasanLinkRe.FindAllStringSubmatch(rawValue, -1) {
		text := cleanText(m[2])
		if text == "" {
			continue
		}
		rel := ingest.Relation{
			Type:      "Landasan Hukum",
			TargetURL: baseURL + m[1],
		}
		// Split "UU Nomor 21 Tahun 2011 tentang Otoritas Jasa Keuangan" into
		// the identifying part and the title, both carried raw.
		if number, title, found := strings.Cut(text, " tentang "); found {
			rel.TargetNumber = strings.TrimSpace(number)
			rel.TargetTitle = strings.TrimSpace(title)
		} else {
			rel.TargetNumber = text
		}
		out = append(out, rel)
	}
	return out
}

// splitStatus splits a raw "Status Peraturan" value into the status string
// and the optional effective date from a "Sejak Tanggal dd-mm-yyyy" suffix.
// "Berlaku Sejak Tanggal 31-12-2024" → ("Berlaku", 2024-12-31);
// "Berlaku (Dicabut Sebagian)" → unchanged, zero time.
func splitStatus(raw string) (string, time.Time) {
	if m := statusSejakRe.FindStringSubmatch(raw); m != nil {
		return strings.TrimSpace(m[1]), parseListDate(m[2])
	}
	return raw, time.Time{}
}

// dokumenKind maps a Dokumen row label to the bronze.raw_file role. The
// regulation text itself is "main"; abstract and FAQ PDFs are attachments.
// Live pages label rows Abstrak/FAQ/Peraturan when several files exist, but a
// page with only the regulation PDF leaves its single row unlabeled
// (<h4></h4>) — an empty label is therefore the regulation itself.
// dokumenKind maps the table-row label to a file kind. "Peraturan" (or blank)
// is the regulation body; everything else (Lampiran, etc.) is an attachment.
// When a document has multiple "main" files, pickFile takes the lowest ordinal.
func dokumenKind(label string) string {
	if label == "" || strings.EqualFold(label, "Peraturan") {
		return "main"
	}
	return "attachment"
}

// parseDokumen extracts the downloadable files from the Dokumen section.
// Every observed file is a born-digital PDF served by DownloadDokumen/{fileUUID}.
func parseDokumen(section string) []ingest.FileRef {
	var files []ingest.FileRef
	seen := map[string]bool{}

	// Strategy 1: table-row layout (observed on some detail pages):
	//   <tr><th><h4>Peraturan</h4></th><td>…DownloadDokumen…</td></tr>
	for _, rm := range dokumenRowRe.FindAllStringSubmatch(section, -1) {
		label := cleanText(rm[1])
		fm := dokumenFileRe.FindStringSubmatch(rm[2])
		if fm == nil {
			continue
		}
		fileUUID := fm[1]
		if seen[fileUUID] {
			continue
		}
		seen[fileUUID] = true
		name := strings.TrimSpace(fm[2])
		files = append(files, ingest.FileRef{
			URL:      downloadURL(fileUUID),
			Name:     name,
			Ext:      fileExt(name),
			Kind:     dokumenKind(label),
			MIMEType: "application/pdf",
		})
	}

	// Strategy 2: div-based layout (the majority of POJK/SEOJK pages):
	//   <div class="col-md-2"><a download href="/Web/ViewPeraturan/
	//   DownloadDokumen/{uuid}" onclick="downloadDokumen('name.pdf', ...)">
	// Scan the whole section for file links the row strategy missed.
	for _, fm := range dokumenFileRe.FindAllStringSubmatch(section, -1) {
		fileUUID := fm[1]
		if seen[fileUUID] {
			continue
		}
		seen[fileUUID] = true
		name := strings.TrimSpace(fm[2])
		files = append(files, ingest.FileRef{
			URL:      downloadURL(fileUUID),
			Name:     name,
			Ext:      fileExt(name),
			Kind:     fileKindFromName(name),
			MIMEType: "application/pdf",
		})
	}
	return files
}

// fileKindFromName infers the file role from the advertised filename when the
// table-row label isn't available. OJK names its "salinan" (certified copy)
// PDF as the regulation body; "sum" prefix is the summary/abstract variant.
func fileKindFromName(name string) string {
	lower := strings.ToLower(name)
	if strings.Contains(lower, "sum") || strings.Contains(lower, "abstrak") {
		return "attachment"
	}
	return "main"
}

// ojkToBPKDocType maps OJK short type labels to BPK's canonical long form for
// doc_key alignment. Unmapped labels pass through unchanged.
func ojkToBPKDocType(label string) string {
	upper := strings.ToUpper(strings.TrimSpace(label))
	switch upper {
	case "POJK":
		return "Peraturan Otoritas Jasa Keuangan"
	case "SEOJK":
		return "Surat Edaran Otoritas Jasa Keuangan"
	default:
		return label
	}
}

// fileExt returns the lowercase extension of a filename, without the dot.
func fileExt(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return strings.ToLower(name[i+1:])
	}
	return ""
}
