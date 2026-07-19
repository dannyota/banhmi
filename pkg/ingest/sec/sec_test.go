package sec

import (
	"context"
	"testing"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// --- NRS ID extraction ---

func TestNRSIDExtraction(t *testing.T) {
	tests := []struct {
		name string
		html string
		want string
	}{
		{
			name: "OpenWindow call",
			html: `<a href="#" onclick="OpenWindow('11113', 'nrs')">View</a>`,
			want: "11113",
		},
		{
			name: "OpenWindow with spaces",
			html: `onclick="OpenWindow( '12345' , 'nrs')"`,
			want: "12345",
		},
		{
			name: "title parenthetical",
			html: `<td>เรื่อง หลักเกณฑ์การดำเนินงาน (11113)</td>`,
			want: "11113",
		},
		{
			name: "no ID found",
			html: `<td>some random content</td>`,
			want: "",
		},
		{
			name: "OpenWindow preferred over parenthetical",
			html: `<a onclick="OpenWindow('99999', 'x')">Title (11113)</a>`,
			want: "99999",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractNRSID(tt.html)
			if got != tt.want {
				t.Fatalf("extractNRSID = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- Row parsing ---

func TestRowParsing(t *testing.T) {
	html := `<table>
<tr>
<td>ประกาศคณะกรรมการ ก.ล.ต. ที่ กม. 3/2568</td>
<td><a href="#" onclick="OpenWindow('11113', 'nrs')">หลักเกณฑ์การประกอบธุรกิจสินทรัพย์ดิจิทัล (11113)</a></td>
<td>มาตรา 35</td>
<td><a href="https://publish.sec.or.th/nrs/11113s.pdf">PDF</a> <a href="https://publish.sec.or.th/nrs/11113p.docx">DOCX</a></td>
<td><img src="ready_flag.png"></td>
<td>15/01/2568</td>
<td>01/02/2568</td>
</tr>
</table>`

	docs := parseNRSTable(html, time.Time{})
	if len(docs) != 1 {
		t.Fatalf("docs = %d, want 1", len(docs))
	}

	d := docs[0]
	if d.ExternalID != "11113" {
		t.Errorf("ExternalID = %q, want 11113", d.ExternalID)
	}
	if d.SourceID != "sec" {
		t.Errorf("SourceID = %q, want sec", d.SourceID)
	}
	if d.Number != "ประกาศคณะกรรมการ ก.ล.ต. ที่ กม. 3/2568" {
		t.Errorf("Number = %q", d.Number)
	}
	if d.Title != "หลักเกณฑ์การประกอบธุรกิจสินทรัพย์ดิจิทัล" {
		t.Errorf("Title = %q", d.Title)
	}
	if d.DocType != "SEC Notification" {
		t.Errorf("DocType = %q", d.DocType)
	}
	if d.Status != "active" {
		t.Errorf("Status = %q, want active", d.Status)
	}
	// 15/01/2568 BE = 15 Jan 2025 CE
	wantIssued := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	if !d.IssuedAt.Equal(wantIssued) {
		t.Errorf("IssuedAt = %v, want %v", d.IssuedAt, wantIssued)
	}
	// 01/02/2568 BE = 1 Feb 2025 CE
	wantEffective := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	if !d.EffectiveAt.Equal(wantEffective) {
		t.Errorf("EffectiveAt = %v, want %v", d.EffectiveAt, wantEffective)
	}
}

func TestRowParsingExpired(t *testing.T) {
	html := `<table>
<tr>
<td>ประกาศคณะกรรมการ ก.ล.ต. ที่ กจ. 5/2562</td>
<td><a href="#" onclick="OpenWindow('8000', 'nrs')">ข้อกำหนดเก่า (8000)</a></td>
<td></td>
<td><a href="https://publish.sec.or.th/nrs/8000s.pdf">PDF</a></td>
<td>สิ้นผลใช้บังคับ</td>
<td>20/06/2562</td>
<td></td>
</tr>
</table>`

	docs := parseNRSTable(html, time.Time{})
	if len(docs) != 1 {
		t.Fatalf("docs = %d, want 1", len(docs))
	}
	if docs[0].Status != "expired" {
		t.Errorf("Status = %q, want expired", docs[0].Status)
	}
}

func TestRowParsingWatermark(t *testing.T) {
	html := `<table>
<tr>
<td>type1</td>
<td><a onclick="OpenWindow('1001', 'x')">Old doc (1001)</a></td>
<td></td>
<td><a href="https://publish.sec.or.th/nrs/1001s.pdf">PDF</a></td>
<td><img src="ready_flag.png"></td>
<td>01/01/2563</td>
<td></td>
</tr>
<tr>
<td>type2</td>
<td><a onclick="OpenWindow('1002', 'x')">New doc (1002)</a></td>
<td></td>
<td><a href="https://publish.sec.or.th/nrs/1002s.pdf">PDF</a></td>
<td><img src="ready_flag.png"></td>
<td>15/06/2568</td>
<td></td>
</tr>
</table>`

	// Watermark 2024-01-01: only the 2025 doc passes.
	since := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	docs := parseNRSTable(html, since)
	if len(docs) != 1 {
		t.Fatalf("docs = %d, want 1 (only the 2025 doc)", len(docs))
	}
	if docs[0].ExternalID != "1002" {
		t.Errorf("ExternalID = %q, want 1002", docs[0].ExternalID)
	}
}

// --- File cascade ---

func TestFileCascade(t *testing.T) {
	tests := []struct {
		name      string
		html      string
		nrsID     string
		wantCount int
		wantFirst string // expected ext of the first (preferred) file
	}{
		{
			name:      "DOCX + signed PDF",
			html:      `<a href="https://publish.sec.or.th/nrs/11113p.docx">DOCX</a> <a href="https://publish.sec.or.th/nrs/11113s.pdf">PDF</a>`,
			nrsID:     "11113",
			wantCount: 2,
			wantFirst: "docx",
		},
		{
			name:      "readable PDF + signed PDF",
			html:      `<a href="https://publish.sec.or.th/nrs/22222p_r.pdf">rPDF</a> <a href="https://publish.sec.or.th/nrs/22222s.pdf">PDF</a>`,
			nrsID:     "22222",
			wantCount: 2,
			wantFirst: "pdf",
		},
		{
			name:      "DOCX + readable PDF + signed PDF",
			html:      `<a href="https://publish.sec.or.th/nrs/33333p.docx">DOCX</a> <a href="https://publish.sec.or.th/nrs/33333p_r.pdf">rPDF</a> <a href="https://publish.sec.or.th/nrs/33333s.pdf">sPDF</a>`,
			nrsID:     "33333",
			wantCount: 3,
			wantFirst: "docx",
		},
		{
			name:      "signed PDF only",
			html:      `<a href="https://publish.sec.or.th/nrs/44444s.pdf">PDF</a>`,
			nrsID:     "44444",
			wantCount: 1,
			wantFirst: "pdf",
		},
		{
			name:      "no links, fallback to constructed URL",
			html:      `no links here`,
			nrsID:     "55555",
			wantCount: 1,
			wantFirst: "pdf",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := extractFileRefs(tt.html, tt.nrsID)
			if len(files) != tt.wantCount {
				t.Fatalf("files = %d, want %d: %+v", len(files), tt.wantCount, files)
			}
			if files[0].Ext != tt.wantFirst {
				t.Errorf("first file ext = %q, want %q", files[0].Ext, tt.wantFirst)
			}
		})
	}
}

func TestFileCascadeKinds(t *testing.T) {
	html := `<a href="https://publish.sec.or.th/nrs/11113p.docx">DOCX</a>
	         <a href="https://publish.sec.or.th/nrs/11113p_r.pdf">rPDF</a>
	         <a href="https://publish.sec.or.th/nrs/11113s.pdf">sPDF</a>`

	files := extractFileRefs(html, "11113")
	if len(files) != 3 {
		t.Fatalf("files = %d, want 3", len(files))
	}
	// DOCX = main, readable PDF = attachment (because DOCX is main), signed PDF = original_scan.
	if files[0].Kind != "main" {
		t.Errorf("DOCX kind = %q, want main", files[0].Kind)
	}
	if files[1].Kind != "attachment" {
		t.Errorf("readable PDF kind = %q, want attachment (DOCX present)", files[1].Kind)
	}
	if files[2].Kind != "original_scan" {
		t.Errorf("signed PDF kind = %q, want original_scan", files[2].Kind)
	}
}

// --- CP874 decoding ---

func TestCP874Decode(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  string
	}{
		{
			name:  "ASCII passthrough",
			input: []byte("Hello, World!"),
			want:  "Hello, World!",
		},
		{
			name: "Thai text (ko kai to kho khai)",
			// 0xA1 = ก (ko kai), 0xA2 = ข (kho khai), 0xA3 = ฃ (kho khuat)
			input: []byte{0xA1, 0xA2, 0xA3},
			want:  "กขฃ",
		},
		{
			name: "mixed ASCII and Thai",
			// "SEC " + ก + ข
			input: append([]byte("SEC "), 0xA1, 0xA2),
			want:  "SEC กข",
		},
		{
			name:  "empty input",
			input: []byte{},
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeCP874(tt.input)
			if err != nil {
				t.Fatalf("decodeCP874 error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("decodeCP874 = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- Buddhist Era date parsing ---

func TestBEDateParsing(t *testing.T) {
	tests := []struct {
		name string
		text string
		want time.Time
	}{
		{
			name: "standard BE date 2568",
			text: "15/01/2568",
			want: time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "BE date 2562",
			text: "20/06/2562",
			want: time.Date(2019, 6, 20, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "embedded in text",
			text: "วันที่ 15/01/2568 เรื่อง",
			want: time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "single digit day/month",
			text: "1/2/2567",
			want: time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "no date",
			text: "no date here",
			want: time.Time{},
		},
		{
			name: "empty",
			text: "",
			want: time.Time{},
		},
		{
			name: "invalid month",
			text: "15/13/2568",
			want: time.Time{},
		},
		{
			name: "invalid day",
			text: "32/01/2568",
			want: time.Time{},
		},
		{
			name: "year too old after conversion",
			text: "15/01/2400",
			want: time.Time{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseBEDate(tt.text)
			if !got.Equal(tt.want) {
				t.Fatalf("parseBEDate(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

// --- Source identity ---

func TestSourceID(t *testing.T) {
	s := New(nil, nil, nil)
	if s.ID() != "sec" {
		t.Fatalf("ID() = %q, want sec", s.ID())
	}
}

func TestNewWithProxy(t *testing.T) {
	cfg := &Config{ProxyURL: "http://1.2.3.4:8888"}
	s := New(cfg, nil, nil)
	// Verify the download client is different from the discovery client.
	if s.download == s.discovery {
		t.Fatal("download client should differ from discovery when proxy is set")
	}
}

func TestNewWithoutProxy(t *testing.T) {
	s := New(nil, nil, nil)
	// Without proxy, both clients should be the same.
	if s.download != s.discovery {
		t.Fatal("download client should equal discovery when no proxy is set")
	}
}

func TestNewWithInvalidProxy(t *testing.T) {
	cfg := &Config{ProxyURL: "://invalid"}
	s := New(cfg, nil, nil)
	// Invalid proxy falls back to discovery client.
	if s.download != s.discovery {
		t.Fatal("download client should fall back to discovery with invalid proxy URL")
	}
}

// --- FetchDetail ---

func TestFetchDetail(t *testing.T) {
	s := New(nil, nil, nil)
	ref := ingest.DetailRef{ExternalID: "11113", DetailURL: "https://capital.sec.or.th/nrs_search"}
	doc, err := s.FetchDetail(context.Background(), ref)
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}
	if doc.ExternalID != "11113" {
		t.Errorf("ExternalID = %q, want 11113", doc.ExternalID)
	}
	if doc.DocType != "SEC Notification" {
		t.Errorf("DocType = %q", doc.DocType)
	}
	if len(doc.Files) != 1 {
		t.Fatalf("Files = %d, want 1", len(doc.Files))
	}
	if doc.Files[0].URL != "https://publish.sec.or.th/nrs/11113s.pdf" {
		t.Errorf("File URL = %q", doc.Files[0].URL)
	}
}
