package bot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

func TestViewStateExtraction(t *testing.T) {
	html := `<html>
<input type="hidden" name="__VIEWSTATE" id="__VIEWSTATE" value="dDwtMTAyMTIzNDU2Nzt0PD..." />
<input type="hidden" name="__VIEWSTATEGENERATOR" id="__VIEWSTATEGENERATOR" value="CA0B0334" />
<input type="hidden" name="__EVENTVALIDATION" id="__EVENTVALIDATION" value="/wEdAAoAAQ..." />
<input type="hidden" name="__VIEWSTATEENCRYPTED" id="__VIEWSTATEENCRYPTED" value="" />
</html>`

	vs := extractViewState(html)
	if vs.ViewState != "dDwtMTAyMTIzNDU2Nzt0PD..." {
		t.Errorf("ViewState = %q", vs.ViewState)
	}
	if vs.ViewStateGenerator != "CA0B0334" {
		t.Errorf("ViewStateGenerator = %q", vs.ViewStateGenerator)
	}
	if vs.EventValidation != "/wEdAAoAAQ..." {
		t.Errorf("EventValidation = %q", vs.EventValidation)
	}
	if vs.ViewStateEncrypted != "" {
		t.Errorf("ViewStateEncrypted = %q (expected empty)", vs.ViewStateEncrypted)
	}
}

func TestViewStateExtractionMissingFields(t *testing.T) {
	html := `<html>
<input type="hidden" name="__VIEWSTATE" value="somestate" />
</html>`

	vs := extractViewState(html)
	if vs.ViewState != "somestate" {
		t.Errorf("ViewState = %q", vs.ViewState)
	}
	if vs.ViewStateGenerator != "" {
		t.Errorf("ViewStateGenerator should be empty, got %q", vs.ViewStateGenerator)
	}
	if vs.EventValidation != "" {
		t.Errorf("EventValidation should be empty, got %q", vs.EventValidation)
	}
}

func TestRowParsing(t *testing.T) {
	// Mimics the real BOT HTML: title cell contains a nested <table><tr><td>.
	rowHTML := `
<td class="namenews" align="center" valign="top" width="20%">หนังสือเวียน</td><td class="datenews" align="center" valign="top" width="10%" nowrap="nowrap">
16 ก.ค. 2569
</td><td class="tx-news" align="center" valign="top" width="5%">
<img valign='center' alt='New' src='../images/Ico_New_A.gif' border='0' />
</td><td class="tx-news" align="left" valign="top" width="50%">
<div class="tx-news1">
<a href='#' onclick="OpenWindow('PFIPCS_summary.aspx?packId=25670003','summary')">
<table border="0"><tr><p class='setrow'>เรื่อง การกำหนดอัตราดอกเบี้ย</p></tr></table>
</a>
</div>
</td><td class="tx-news" align="center" valign="top" width="5%">
<img alt='ใช้อยู่' title='ใช้อยู่' src='../images/blueCorrect.png' border='0' />
</td><td class="tx-news" align="center" valign="top" width="10%">
<a href='https://www.bot.or.th/content/dam/bot/fipcs/documents/FPG/2569/ThaiPDF/25670003.pdf' target='_blank' >TH</a>
</td>
`
	d, ok := parseRow(rowHTML)
	if !ok {
		t.Fatal("parseRow returned false")
	}
	if d.ExternalID != "25670003" {
		t.Errorf("ExternalID = %q, want 25670003", d.ExternalID)
	}
	if d.Status != "active" {
		t.Errorf("Status = %q, want active", d.Status)
	}
	if string(d.DocType) != "หนังสือเวียน" {
		t.Errorf("DocType = %q", d.DocType)
	}
	if !strings.Contains(d.Title, "การกำหนดอัตราดอกเบี้ย") {
		t.Errorf("Title = %q", d.Title)
	}
	if len(d.Files) != 1 {
		t.Fatalf("Files = %d, want 1", len(d.Files))
	}
	if !strings.Contains(d.Files[0].URL, "25670003.pdf") {
		t.Errorf("File URL = %q", d.Files[0].URL)
	}
}

func TestRowParsingRevokedStatus(t *testing.T) {
	rowHTML := `
<td class="namenews" align="center" valign="top" width="20%">ประกาศ ธปท.</td><td class="datenews" align="center" valign="top" width="10%">
1 ม.ค. 2560
</td><td class="tx-news" align="center" valign="top" width="5%">
</td><td class="tx-news" align="left" valign="top" width="50%">
<a href='#' onclick="OpenWindow('PFIPCS_summary.aspx?packId=25600001','summary')">เรื่อง ทดสอบ</a>
</td><td class="tx-news" align="center" valign="top" width="5%">
<img alt='ยกเลิก' title='ยกเลิก' src='../images/redCross.png' border='0' />
</td><td class="tx-news" align="center" valign="top" width="10%">
</td>
`
	d, ok := parseRow(rowHTML)
	if !ok {
		t.Fatal("parseRow returned false")
	}
	if d.Status != "revoked" {
		t.Errorf("Status = %q, want revoked", d.Status)
	}
}

func TestThaiDateParsing(t *testing.T) {
	tests := []struct {
		input string
		want  time.Time
	}{
		{"16 ก.ค. 2569", time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC)},
		{"1 ม.ค. 2560", time.Date(2017, time.January, 1, 0, 0, 0, 0, time.UTC)},
		{"28 ก.พ. 2566", time.Date(2023, time.February, 28, 0, 0, 0, 0, time.UTC)},
		{"15 มี.ค. 2565", time.Date(2022, time.March, 15, 0, 0, 0, 0, time.UTC)},
		{"30 เม.ย. 2568", time.Date(2025, time.April, 30, 0, 0, 0, 0, time.UTC)},
		{"10 พ.ค. 2567", time.Date(2024, time.May, 10, 0, 0, 0, 0, time.UTC)},
		{"5 มิ.ย. 2564", time.Date(2021, time.June, 5, 0, 0, 0, 0, time.UTC)},
		{"20 ส.ค. 2563", time.Date(2020, time.August, 20, 0, 0, 0, 0, time.UTC)},
		{"12 ก.ย. 2562", time.Date(2019, time.September, 12, 0, 0, 0, 0, time.UTC)},
		{"3 ต.ค. 2561", time.Date(2018, time.October, 3, 0, 0, 0, 0, time.UTC)},
		{"25 พ.ย. 2559", time.Date(2016, time.November, 25, 0, 0, 0, 0, time.UTC)},
		{"31 ธ.ค. 2558", time.Date(2015, time.December, 31, 0, 0, 0, 0, time.UTC)},
		{"", time.Time{}},
		{"invalid", time.Time{}},
	}
	for _, tt := range tests {
		got := parseThaiDate(tt.input)
		if !got.Equal(tt.want) {
			t.Errorf("parseThaiDate(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestPackIdExtraction(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`<a href="javascript:OpenWindow('PFIPCS_summary.aspx?packId=25670003','summary')">Title</a>`, "25670003"},
		{`<a href="javascript:OpenWindow( 'PFIPCS_summary.aspx?packId=25600001' , 'summary' )">Title</a>`, "25600001"},
		{`<a href="other">No pack id</a>`, ""},
	}
	for _, tt := range tests {
		m := packIdRe.FindStringSubmatch(tt.input)
		got := ""
		if m != nil {
			got = m[1]
		}
		if got != tt.want {
			t.Errorf("packIdRe on %q = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestPageCountParsing(t *testing.T) {
	body := `<select name="ctl00$ContentPlaceHolder1$dgDocument$ctl33$ddlPageSelector">
<option value="1">1</option>
<option value="2">2</option>
<option value="3">3</option>
<option value="52">52</option>
</select>`
	got := parsePageCount(body)
	if got != 52 {
		t.Errorf("parsePageCount = %d, want 52", got)
	}
}

func TestPageCountNoDropdown(t *testing.T) {
	got := parsePageCount("<html><body>no dropdown</body></html>")
	if got != 0 {
		t.Errorf("parsePageCount = %d, want 0", got)
	}
}

func TestDetailParsing(t *testing.T) {
	html := `<html>
<span id="ctl00_ContentPlaceHolder1_LblDocName">หนังสือเวียน ธปท. ฝนส.(03) ว. 3/2569</span>
<span id="ctl00_ContentPlaceHolder1_LblDocTitle">เรื่อง การกำหนดอัตราดอกเบี้ย</span>
<span id="ctl00_ContentPlaceHolder1_LblLetterDate">16 ก.ค. 2569</span>
<span id="ctl00_ContentPlaceHolder1_LblEffectiveDate">1 ส.ค. 2569</span>
<span id="ctl00_ContentPlaceHolder1_LblExpiryDate">31 ธ.ค. 2570</span>
<span id="ctl00_ContentPlaceHolder1_LblPurpose">กำหนดอัตราดอกเบี้ยเงินฝาก</span>
<span id="ctl00_ContentPlaceHolder1_LblSubstance">รายละเอียดเพิ่มเติม</span>
</html>`

	d := parseDetail(html, "25670003", "https://app.bot.or.th/FIPCS/Thai/PFIPCS_summary.aspx?packId=25670003")
	if d.Number != "หนังสือเวียน ธปท. ฝนส.(03) ว. 3/2569" {
		t.Errorf("Number = %q", d.Number)
	}
	if d.Title != "เรื่อง การกำหนดอัตราดอกเบี้ย" {
		t.Errorf("Title = %q", d.Title)
	}
	if d.IssuedAt != (time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("IssuedAt = %v", d.IssuedAt)
	}
	if d.EffectiveAt != (time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("EffectiveAt = %v", d.EffectiveAt)
	}
	if d.ExpireAt != (time.Date(2027, time.December, 31, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("ExpireAt = %v", d.ExpireAt)
	}
	if d.Abstract != "กำหนดอัตราดอกเบี้ยเงินฝาก — รายละเอียดเพิ่มเติม" {
		t.Errorf("Abstract = %q", d.Abstract)
	}
}

func TestDetailParsingPurposeOnly(t *testing.T) {
	html := `<html>
<span id="ctl00_ContentPlaceHolder1_LblDocName">ประกาศ ธปท. 1/2569</span>
<span id="ctl00_ContentPlaceHolder1_LblPurpose">วัตถุประสงค์ทดสอบ</span>
</html>`

	d := parseDetail(html, "25690001", "")
	if d.Abstract != "วัตถุประสงค์ทดสอบ" {
		t.Errorf("Abstract = %q, want purpose only", d.Abstract)
	}
}

func TestDiscoverEndToEnd(t *testing.T) {
	page1 := `<html>
<input type="hidden" name="__VIEWSTATE" value="state1" />
<input type="hidden" name="__VIEWSTATEGENERATOR" value="gen1" />
<input type="hidden" name="__EVENTVALIDATION" value="ev1" />
<select name="ctl00$ContentPlaceHolder1$dgDocument$ctl33$ddlPageSelector">
<option value="1">1</option>
</select>
<table>
<tr class="nonebgnewsWhite">
<td class="namenews" align="center" valign="top" width="20%">หนังสือเวียน</td><td class="datenews" align="center" valign="top" width="10%">
16 ก.ค. 2569
</td><td class="tx-news" align="center" valign="top" width="5%">
</td><td class="tx-news" align="left" valign="top" width="50%">
<div class="tx-news1"><a href='#' onclick="OpenWindow('PFIPCS_summary.aspx?packId=25670003','summary')">
<table border="0"><tr><p class='setrow'>Test Title</p></tr></table></a></div>
</td><td class="tx-news" align="center" valign="top" width="5%">
<img alt='ใช้อยู่' title='ใช้อยู่' src='../images/blueCorrect.png' border='0' />
</td><td class="tx-news" align="center" valign="top" width="10%">
<a href='https://www.bot.or.th/test.pdf' target='_blank'>TH</a>
</td>
</tr>
</table>
</html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(page1))
	}))
	defer srv.Close()

	s := New(srv.Client(), nil)
	s.baseURL = srv.URL

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	// Both groups see the same packId — deduped to 1.
	if len(docs) != 1 {
		t.Fatalf("docs = %d, want 1 (dedup across groups)", len(docs))
	}
	if docs[0].ExternalID != "25670003" {
		t.Errorf("ExternalID = %q", docs[0].ExternalID)
	}
	if docs[0].SourceID != "bot" {
		t.Errorf("SourceID = %q", docs[0].SourceID)
	}
}

func TestParseRowsNestedTable(t *testing.T) {
	// Two consecutive rows as they appear in the real BOT HTML, with nested
	// <table><tr>…</tr></table> inside the title cell.
	body := `<table>
<tr class="nonebgnewsWhite">
<td class="namenews" align="center" valign="top" width="20%">หนังสือเวียน</td><td class="datenews" align="center" valign="top" width="10%" nowrap="nowrap">
16 ก.ค. 2569
</td><td class="tx-news" align="center" valign="top" width="5%">
<img valign='center' alt='New' src='../images/Ico_New_A.gif' border='0' />
</td><td class="tx-news" align="left" valign="top" width="50%">
<div class="tx-news1">
<a href='#' onclick="OpenWindow('PFIPCS_summary.aspx?packId=25690143','summary')">
<table border="0"><tr><p class='setrow'>การยกเว้นค่าธรรมเนียม</p></tr></table>
</a></div>
</td><td class="tx-news" align="center" valign="top" width="5%">
<img alt='ใช้อยู่' title='ใช้อยู่' src='../images/blueCorrect.png' border='0' />
</td><td class="tx-news" align="center" valign="top" width="10%">
<a href='https://www.bot.or.th/content/dam/bot/fipcs/documents/DDD/2569/ThaiPDF/25690143.pdf' target='_blank' >TH</a>
</td>
</tr><tr class="nonebgnewsGray">
<td class="namenews" align="center" valign="top" width="20%">ประกาศกระทรวง</td><td class="datenews" align="center" valign="top" width="10%" nowrap="nowrap">
14 ก.ค. 2569
</td><td class="tx-news" align="center" valign="top" width="5%">
</td><td class="tx-news" align="left" valign="top" width="50%">
<div class="tx-news1">
<a href='#' onclick="OpenWindow('PFIPCS_summary.aspx?packId=25690142','summary')">
<table border="0"><tr><p class='setrow'>การจำหน่ายพันธบัตร</p></tr></table>
</a></div>
</td><td class="tx-news" align="center" valign="top" width="5%">
<img alt='ใช้อยู่' title='ใช้อยู่' src='../images/blueCorrect.png' border='0' />
</td><td class="tx-news" align="center" valign="top" width="10%">
<a href='https://www.bot.or.th/content/dam/bot/fipcs/documents/DDD/2569/ThaiPDF/25690142.pdf' target='_blank' >TH</a>
</td>
</tr>
</table>`
	docs := parseRows(body)
	if len(docs) != 2 {
		t.Fatalf("parseRows = %d docs, want 2", len(docs))
	}
	if docs[0].ExternalID != "25690143" {
		t.Errorf("doc[0].ExternalID = %q, want 25690143", docs[0].ExternalID)
	}
	if docs[1].ExternalID != "25690142" {
		t.Errorf("doc[1].ExternalID = %q, want 25690142", docs[1].ExternalID)
	}
	if len(docs[0].Files) != 1 {
		t.Errorf("doc[0].Files = %d, want 1", len(docs[0].Files))
	}
	if len(docs[1].Files) != 1 {
		t.Errorf("doc[1].Files = %d, want 1", len(docs[1].Files))
	}
}

func TestCleanText(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"<b>hello</b>  world", "hello world"},
		{"  spaces  everywhere  ", "spaces everywhere"},
		{"&amp; &lt;tag&gt;", "& <tag>"},
		{"", ""},
	}
	for _, tt := range tests {
		got := cleanText(tt.input)
		if got != tt.want {
			t.Errorf("cleanText(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// FetchDetail prefers the hrefs Discover scraped, and only synthesizes the
// conventional path when discovery captured none. A packId too short to slice
// must not panic.
func TestFetchDetailPrefersDiscoveredFiles(t *testing.T) {
	s := New(nil, nil)

	scraped := []ingest.FileRef{{
		URL:  "https://www.bot.or.th/content/dam/bot/fipcs/documents/PSD/2541/ThaiPDF/odd-name.pdf",
		Name: "odd-name.pdf", Ext: "pdf", Kind: "main", MIMEType: "application/pdf",
	}}
	got, err := s.FetchDetail(context.Background(), ingest.DetailRef{ExternalID: "25413004", Files: scraped})
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}
	if len(got.Files) != 1 || got.Files[0].URL != scraped[0].URL {
		t.Errorf("scraped href must win, got %+v", got.Files)
	}

	got, err = s.FetchDetail(context.Background(), ingest.DetailRef{ExternalID: "25413004"})
	if err != nil {
		t.Fatalf("FetchDetail(no files): %v", err)
	}
	if len(got.Files) != 1 || !strings.Contains(got.Files[0].URL, "/FPG/2541/ThaiPDF/25413004.pdf") {
		t.Errorf("expected the synthesized fallback, got %+v", got.Files)
	}

	got, err = s.FetchDetail(context.Background(), ingest.DetailRef{ExternalID: "123"})
	if err != nil {
		t.Fatalf("FetchDetail(short id): %v", err)
	}
	if len(got.Files) != 0 {
		t.Errorf("a packId too short to slice must yield no file, got %+v", got.Files)
	}
}

// The FIPCS listing emits Windows-style separators inside the dam path; Go
// percent-encodes a raw backslash and the CDN 404s, so Discover must normalize
// them. (curl silently normalizes, which is why a manual probe looks fine.)
func TestDiscoverNormalizesBackslashHrefs(t *testing.T) {
	rowHTML := `
<td class="namenews" align="center" valign="top" width="20%">ประกาศ</td><td class="datenews" align="center" valign="top" width="10%" nowrap="nowrap">
16 ก.ค. 2569
</td><td class="tx-news" align="center" valign="top" width="5%">
</td><td class="tx-news" align="left" valign="top" width="50%">
<a href='#' onclick="OpenWindow('PFIPCS_summary.aspx?packId=25473012','summary')">
<table border="0"><tr><p class='setrow'>เรื่อง ทดสอบ</p></tr></table>
</a>
</td><td class="tx-news" align="center" valign="top" width="5%">
<img alt='ใช้อยู่' title='ใช้อยู่' src='../images/blueCorrect.png' border='0' />
</td><td class="tx-news" align="center" valign="top" width="10%">
<a href='https://www.bot.or.th/content/dam/bot/fipcs/documents/DDD/2547\ThaiPDF\25473012.pdf' target='_blank' >TH</a>
</td>
`
	d, ok := parseRow(rowHTML)
	if !ok {
		t.Fatal("parseRow returned false")
	}
	if len(d.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(d.Files))
	}
	if strings.Contains(d.Files[0].URL, `\`) {
		t.Errorf("backslashes must be normalized, got %s", d.Files[0].URL)
	}
	if want := "/DDD/2547/ThaiPDF/25473012.pdf"; !strings.HasSuffix(d.Files[0].URL, want) {
		t.Errorf("URL = %s, want suffix %s", d.Files[0].URL, want)
	}
}
