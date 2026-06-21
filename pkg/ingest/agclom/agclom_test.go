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
