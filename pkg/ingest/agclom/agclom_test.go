package agclom

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseUpdated(t *testing.T) {
	genpdf, _ := json.Marshal([]genPDFEntry{
		{Path: "/upload/portal/akta/outputaktap/3552389_BI/", DocName: "RECORDS (DISPOSAL) (SARAWAK) ACT 1955 (Revised-2026).pdf", Icon: "pdf-en-printed.png"},
		{Path: "/upload/portal/akta/outputaktap/3552389_BM/", DocName: "AKTA REKOD (PELUPUSAN) (SARAWAK) 1955.pdf", Icon: "pdf-ms-printed.png"},
	})
	body, _ := json.Marshal(updatedResponse{
		RecordsTotal: 885,
		Records: []updatedRecord{{
			ActID: "883", ActNo: "883",
			Title:  `<a href="act-detail.php?act=883&lang=BI&date=15-06-2026#timeline">RECORDS (DISPOSAL) (SARAWAK) ACT 1955 (REVISED-2026)</a> <i>As At </i><i>15-06-2026</i><br><a href="act-detail.php?act=883&lang=BM&date=15-06-2026#timeline">AKTA REKOD</a>`,
			GenPDF: string(genpdf),
		}},
	})

	docs, total, err := parseUpdated(string(body), "https://lom.agc.gov.my")
	if err != nil {
		t.Fatalf("parseUpdated: %v", err)
	}
	if total != 885 {
		t.Fatalf("total = %d, want 885", total)
	}
	if len(docs) != 1 {
		t.Fatalf("docs = %d, want 1", len(docs))
	}
	d := docs[0]
	if d.ExternalID != "883" || d.Number != "Act 883" {
		t.Fatalf("id/number = %q/%q, want 883/Act 883", d.ExternalID, d.Number)
	}
	if d.Title != "RECORDS (DISPOSAL) (SARAWAK) ACT 1955 (REVISED-2026)" {
		t.Fatalf("title = %q", d.Title)
	}
	if d.IssuedAt != time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("issued = %v, want 2026-06-15", d.IssuedAt)
	}
	if len(d.Files) != 1 {
		t.Fatalf("files = %d, want 1 (the BI edition)", len(d.Files))
	}
	f := d.Files[0]
	if !strings.HasPrefix(f.URL, "https://lom.agc.gov.my/ilims/upload/portal/akta/outputaktap/3552389_BI/") {
		t.Fatalf("BI url = %q", f.URL)
	}
	if f.Ext != "pdf" || f.Kind != "main" {
		t.Fatalf("file ext/kind = %q/%q", f.Ext, f.Kind)
	}
	if strings.Contains(f.URL, "_BM/") {
		t.Fatal("picked the BM edition; want BI only")
	}
}

func TestCurrentReprintPicksHighestProjectID(t *testing.T) {
	// Detail page lists two historical English reprints; the current one has the
	// highest project id.
	page := `
<a href="../../../ilims/upload/portal/akta/outputaktap/1691496_BI/ACT 758_2.8.2021.pdf">old</a>
<a href="../../../ilims/upload/portal/akta/outputaktap/1867218_BI/Act 758 Final.pdf">current</a>
Publication Date: 22/03/2013
Royal Assent Date: 18/03/2013
Commencement Date: 17/02/2025`

	f, ok := currentReprint(page, "https://lom.agc.gov.my")
	if !ok {
		t.Fatal("currentReprint found nothing")
	}
	if f.Name != "Act 758 Final.pdf" || !strings.Contains(f.URL, "1867218_BI") {
		t.Fatalf("picked %q (%s), want the 1867218 reprint", f.Name, f.URL)
	}

	if got := matchDate(pubDateRe, page); got != time.Date(2013, 3, 22, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("publication date = %v, want 2013-03-22", got)
	}
	if got := matchDate(commenceRe, page); got != time.Date(2025, 2, 17, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("commencement date = %v, want 2025-02-17", got)
	}
}

func TestCurrentReprintFallsBackToViewerPDF(t *testing.T) {
	// Older Acts have no outputaktap reprint — take the PDF from the pdf.js viewer.
	// The sub-path under akta/ varies; filenames carry spaces and parentheses.
	cases := []struct {
		name, page, wantName, wantURL string
	}{
		{
			name:     "LOM/EN consolidated",
			page:     `<iframe data-src="pdfjs/web/viewer.html?file=../../../ilims/upload/portal/akta/LOM/EN/ACT 627 (REPRINT 2006).pdf&embedded=true"></iframe>`,
			wantName: "ACT 627 (REPRINT 2006).pdf",
			wantURL:  "https://lom.agc.gov.my/ilims/upload/portal/akta/LOM/EN/ACT%20627%20%28REPRINT%202006%29.pdf",
		},
		{
			name:     "outputaktap without project-id subdir (Act 589)",
			page:     `<iframe data-src="pdfjs/web/viewer.html?file=../../../ilims/upload/portal/akta/outputaktap/Act 589 (2006).pdf&embedded=true"></iframe>`,
			wantName: "Act 589 (2006).pdf",
			wantURL:  "https://lom.agc.gov.my/ilims/upload/portal/akta/outputaktap/Act%20589%20%282006%29.pdf",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, ok := currentReprint(c.page, "https://lom.agc.gov.my")
			if !ok {
				t.Fatal("currentReprint found nothing")
			}
			if f.Name != c.wantName {
				t.Fatalf("name = %q, want %q", f.Name, c.wantName)
			}
			if f.URL != c.wantURL {
				t.Fatalf("url = %q, want %q", f.URL, c.wantURL)
			}
		})
	}
}

func TestActStatus(t *testing.T) {
	if got := actStatus(`<span>Status</span> Principal Act <a>...</a> Repealed by Act 758.pdf`); got != "REPEALED" {
		t.Fatalf("repealed page status = %q, want REPEALED", got)
	}
	if got := actStatus(`<span>Status</span> Principal Act — in force`); got != "PRINCIPAL" {
		t.Fatalf("principal page status = %q, want PRINCIPAL", got)
	}
}

func TestMatchCommencementDate(t *testing.T) {
	cases := []struct {
		name string
		page string
		want time.Time
	}{
		{
			name: "standard Commencement Date DD/MM/YYYY",
			page: `Publication Date: 22/03/2013
Commencement Date: 17/02/2025`,
			want: time.Date(2025, 2, 17, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "Commencement Remark DD/MM/YYYY with PU ref (Act 854)",
			page: `Publication Date: 26/06/2024
Royal Assent Date: 18/06/2024
Commencement Remark: 26/08/2024 [P.U. (B) 334/2024]`,
			want: time.Date(2024, 8, 26, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "Commencement Remark D/M/YYYY (Act 866)",
			page: `Publication Date: 22/05/2025
Commencement Remark: 1/1/2026 [P.U. (B) 449/2025]`,
			want: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "Commencement Remark DD-MM-YYYY (Act 759)",
			page: `Publication Date: 22/03/2013
Commencement Remark: 30-6-2013 [P.U. (B) 277/2013]`,
			want: time.Date(2013, 6, 30, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "Commencement Remark D/M/YYYY (Act 701)",
			page: `Publication Date: 28/02/2020
Commencement Remark: 1/10/2020 [P.U. (B) 479/2020]`,
			want: time.Date(2020, 10, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "Commencement Remark with bracket prefix (Act 671)",
			page: `Publication Date: 01/07/2021
Commencement Remark: [28/09/2007 P.U. (B) 342/2007]`,
			want: time.Date(2007, 9, 28, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "Commencement Remark dash means no date (Act 519)",
			page: `Publication Date: 12/05/1994
Commencement Remark: -`,
			want: time.Time{},
		},
		{
			name: "both present prefers Commencement Date",
			page: `Commencement Date: 01/06/2020
Commencement Remark: 15/07/2020 [P.U. (B) 300/2020]`,
			want: time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := matchCommencementDate(c.page)
			if !got.Equal(c.want) {
				t.Fatalf("matchCommencementDate = %v, want %v", got, c.want)
			}
		})
	}
}

func TestCurrentReprintPrefersReprintOverLOMEN(t *testing.T) {
	// When both exist, the generated reprint wins (the LOM/EN fallback only fires
	// when there is no outputaktap reprint), so the 21 working Acts are unchanged.
	page := `
<a href="../../../ilims/upload/portal/akta/outputaktap/1867218_BI/Act 758 Final.pdf">current</a>
<iframe data-src="pdfjs/web/viewer.html?file=../../../ilims/upload/portal/akta/LOM/EN/Act 758.pdf&embedded=true"></iframe>`
	f, ok := currentReprint(page, "https://lom.agc.gov.my")
	if !ok || !strings.Contains(f.URL, "1867218_BI") {
		t.Fatalf("picked %q (%s), want the outputaktap reprint", f.Name, f.URL)
	}
}
