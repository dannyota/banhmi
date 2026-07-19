package agclom

import (
	"context"
	"encoding/json"
	stdhtml "html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

const subsidPath = "/json-subsid-2024.php" // relations: P.U.(A)/(B) acting on an Act

var (
	pubDateRe  = regexp.MustCompile(`Publication Date:\s*([0-9]{2}/[0-9]{2}/[0-9]{4})`)
	commenceRe = regexp.MustCompile(`Commencement Date:\s*([0-9]{2}/[0-9]{2}/[0-9]{4})`)
	// commenceRemarkRe matches "Commencement Remark:" followed by a date in
	// D/M/YYYY, DD/MM/YYYY, or DD-MM-YYYY format. Several Acts (519, 671, 701,
	// 759, 854, 866) use this label instead of "Commencement Date:". The date may
	// be prefixed by a bracket (e.g. "[28 September 2007") or followed by a P.U.
	// reference; we capture only the leading numeric date.
	commenceRemarkRe = regexp.MustCompile(`Commencement Remark:\s*\[?\s*([0-9]{1,2}[/-][0-9]{1,2}[/-][0-9]{4})`)
	assentRe         = regexp.MustCompile(`Royal Assent Date:\s*([0-9]{2}/[0-9]{2}/[0-9]{4})`)
	// repealedByRe marks an Act the AGC detail page shows as repealed (it names the
	// repealing Act, e.g. "Repealed by Act 758"); principal Acts have no such note.
	repealedByRe = regexp.MustCompile(`(?i)Repealed by\b`)
	biReprintRe  = regexp.MustCompile(`outputaktap/([0-9]+)_BI/([^"'<>]+?\.pdf)`)
	// viewerPDFRe matches the PDF an older Act (no generated _BI reprint) shows in
	// its pdf.js viewer. The path under .../akta/ varies — LOM/EN/<name>.pdf or
	// outputaktap/<name>.pdf — so capture the whole sub-path and split it, e.g.
	// viewer.html?file=../../../ilims/upload/portal/akta/LOM/EN/Act 658.pdf
	// viewer.html?file=../../../ilims/upload/portal/akta/outputaktap/Act 589 (2006).pdf
	viewerPDFRe = regexp.MustCompile(`viewer\.html\?file=[^"]*?ilims/upload/portal/akta/([^"'&<>]+?\.pdf)`)
)

// subsidResponse is the DataTables JSON from json-subsid-2024.php?act=<id>.
type subsidResponse struct {
	RecordsTotal int            `json:"recordsTotal"`
	Records      []subsidRecord `json:"records"`
}

type subsidRecord struct {
	Type             string `json:"subsidiaryLegislationType"` // "pua" | "pub"
	NoPU             string `json:"noPU"`                      // e.g. "P.U. (A) 61/2025"
	TitleBI          string `json:"titleBI"`
	CommencementDate string `json:"commencementDate"`
	PublicationDate  string `json:"publicationDate"`
}

// FetchDetail enriches an Act from its detail page (validity dates + the current
// English reprint PDF) and the subsidiary-legislation endpoint (P.U. relations).
// The detail page lists every historical reprint; the current one is the entry
// with the highest project id (ids increment as reprints are generated).
func (s *Source) FetchDetail(ctx context.Context, ref ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	page, err := s.do(ctx, detailURL(s.baseURL, ref.ExternalID), nil)
	if err != nil {
		return nil, err
	}

	d := &ingest.DiscoveredDoc{
		SourceID:    SourceID,
		ExternalID:  ref.ExternalID,
		DocType:     "Act",
		DetailURL:   ref.DetailURL,
		Status:      actStatus(page),
		IssuedAt:    matchDate(pubDateRe, page),
		EffectiveAt: matchCommencementDate(page),
	}
	if d.IssuedAt.IsZero() {
		d.IssuedAt = matchDate(assentRe, page)
	}
	if f, ok := currentReprint(page, s.baseURL); ok {
		d.Files = []ingest.FileRef{f}
	}

	rels, err := s.relations(ctx, ref.ExternalID)
	if err != nil {
		s.log.Warn("agclom relations fetch failed", "act", ref.ExternalID, "err", err)
	} else {
		d.Relations = rels
	}
	return d, nil
}

// relations fetches the P.U.(A)/(B) subsidiary-legislation timeline acting on the Act.
func (s *Source) relations(ctx context.Context, actID string) ([]ingest.Relation, error) {
	form := url.Values{}
	form.Set("draw", "1")
	form.Set("start", "0")
	form.Set("length", "1000")
	body, err := s.do(ctx, s.baseURL+subsidPath+"?act="+url.QueryEscape(actID), form)
	if err != nil {
		return nil, err
	}
	var resp subsidResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, err
	}
	out := make([]ingest.Relation, 0, len(resp.Records))
	for _, r := range resp.Records {
		if strings.TrimSpace(r.NoPU) == "" {
			continue
		}
		out = append(out, ingest.Relation{
			Type:         strings.TrimSpace(r.Type), // "pua"/"pub" — normalized via config.relation_type
			TargetNumber: strings.TrimSpace(r.NoPU),
			TargetTitle:  cleanText(r.TitleBI),
		})
	}
	return out, nil
}

// currentReprint picks the current English reprint PDF from a detail page: the
// reprint with the highest project id (ids increment as reprints are generated).
func currentReprint(page, baseURL string) (ingest.FileRef, bool) {
	matches := biReprintRe.FindAllStringSubmatch(page, -1)
	bestPID := -1
	var bestName, bestPath string
	for _, m := range matches {
		pid, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if pid > bestPID {
			bestPID = pid
			bestName = stdhtml.UnescapeString(m[2])
			bestPath = "/upload/portal/akta/outputaktap/" + m[1] + "_BI/"
		}
	}
	if bestPID >= 0 {
		return ingest.FileRef{
			URL:      pdfURL(baseURL, bestPath, bestName),
			Name:     bestName,
			Ext:      "pdf",
			Kind:     "main",
			MIMEType: "application/pdf",
		}, true
	}
	// Older Acts have no generated reprint — take the current PDF the detail page
	// shows in its pdf.js viewer (path varies: LOM/EN/… or outputaktap/…).
	if m := viewerPDFRe.FindStringSubmatch(page); m != nil {
		rel := stdhtml.UnescapeString(m[1])
		dir, name := "", rel
		if i := strings.LastIndex(rel, "/"); i >= 0 {
			dir, name = rel[:i+1], rel[i+1:]
		}
		return ingest.FileRef{
			URL:      pdfURL(baseURL, "/upload/portal/akta/"+dir, name),
			Name:     name,
			Ext:      "pdf",
			Kind:     "main",
			MIMEType: "application/pdf",
		}, true
	}
	return ingest.FileRef{}, false
}

// actStatus classifies an Act from its detail page: REPEALED when the page names a
// repealing Act, else PRINCIPAL (in force). These map to expired/in_force via the
// config.validity_status seed.
func actStatus(page string) string {
	if repealedByRe.MatchString(page) {
		return "REPEALED"
	}
	return "PRINCIPAL"
}

func matchDate(re *regexp.Regexp, s string) time.Time {
	if m := re.FindStringSubmatch(s); m != nil {
		return parseFlexDate(m[1])
	}
	return time.Time{}
}

// matchCommencementDate tries "Commencement Date:" (DD/MM/YYYY) first, then
// falls back to "Commencement Remark:" (D/M/YYYY or DD-MM-YYYY). Several Acts
// only carry the Remark label on their detail pages.
func matchCommencementDate(page string) time.Time {
	if t := matchDate(commenceRe, page); !t.IsZero() {
		return t
	}
	return matchDate(commenceRemarkRe, page)
}

// parseFlexDate parses a date in DD/MM/YYYY, D/M/YYYY, or DD-MM-YYYY format.
func parseFlexDate(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"02/01/2006", "2/1/2006", "02-01-2006", "2-1-2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
