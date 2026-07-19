package ocs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

func TestListResponseParsing(t *testing.T) {
	raw := `{
		"meta": {"page": 1, "perpage": 10, "total": 1884, "pages": 189},
		"data": [
			{
				"lawCode": "ธ0012-1B-0001",
				"lawNameTh": "พระราชบัญญัติธุรกิจสถาบันการเงิน พ.ศ. 2551",
				"lawNameEn": "FINANCIAL INSTITUTION BUSINESS ACT, B.E. 2551 (2008)",
				"encTimelineID": "cXVaTkRBPT09",
				"year": 2008,
				"publishDate": "5/2/2551",
				"fileUUID": "https://www.ocs.go.th/download/abc123",
				"childrens": "",
				"state": "01",
				"num": 1
			},
			{
				"lawCode": "ธ0012-1B-0002",
				"lawNameTh": "พระราชบัญญัติหลักทรัพย์และตลาดหลักทรัพย์ พ.ศ. 2535",
				"lawNameEn": false,
				"encTimelineID": "dGhpcz09",
				"year": 1992,
				"publishDate": "12/3/2535",
				"fileUUID": "",
				"childrens": [{"name": "sub", "items": []}],
				"state": "02",
				"num": 2
			}
		]
	}`

	var resp listResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if resp.Meta.total() != 1884 {
		t.Errorf("meta.total = %d, want 1884", resp.Meta.total())
	}
	if resp.Meta.pages() != 189 {
		t.Errorf("meta.pages = %d, want 189", resp.Meta.pages())
	}
	if len(resp.Data) != 2 {
		t.Fatalf("data len = %d, want 2", len(resp.Data))
	}

	// First law: lawNameEn is a string.
	en1 := parseLawNameEn(resp.Data[0].LawNameEn)
	if en1 != "FINANCIAL INSTITUTION BUSINESS ACT, B.E. 2551 (2008)" {
		t.Errorf("lawNameEn[0] = %q", en1)
	}

	// Second law: lawNameEn is boolean false.
	en2 := parseLawNameEn(resp.Data[1].LawNameEn)
	if en2 != "" {
		t.Errorf("lawNameEn[1] = %q, want empty", en2)
	}

	// fileUUID: first has a URL, second is empty.
	if resp.Data[0].FileUUID != "https://www.ocs.go.th/download/abc123" {
		t.Errorf("fileUUID[0] = %q", resp.Data[0].FileUUID)
	}
	if resp.Data[1].FileUUID != "" {
		t.Errorf("fileUUID[1] = %q, want empty", resp.Data[1].FileUUID)
	}

	// Childrens: first is empty string, second is an array.
	if string(resp.Data[0].Childrens) != `""` {
		t.Errorf("childrens[0] = %s, want empty string", resp.Data[0].Childrens)
	}
	var children1 []json.RawMessage
	if err := json.Unmarshal(resp.Data[1].Childrens, &children1); err != nil {
		t.Errorf("childrens[1] not an array: %v", err)
	}
}

func TestLawCodeFilter(t *testing.T) {
	tests := []struct {
		code string
		want bool
	}{
		{"ธ0012-1B-0001", true},    // Act
		{"ก0001-10-0001", false},   // Constitution
		{"ก0001-1A-0001", false},   // Organic Act
		{"ธ0012-1C-0001", false},   // Emergency Decree
		{"ก0001-1D-0001", false},   // Code
		{"ก0001-2A-0001", false},   // Royal Decree
		{"ก0001-2B-0001", false},   // Ministerial Regulation
		{"ธ0012-1B-0099", true},    // Another Act
		{"", false},                // empty
		{"no-segment-here", false}, // no type segment
	}
	for _, tt := range tests {
		got := isActLawCode(tt.code)
		if got != tt.want {
			t.Errorf("isActLawCode(%q) = %v, want %v", tt.code, got, tt.want)
		}
	}
}

func TestParsePublishDate(t *testing.T) {
	tests := []struct {
		input string
		want  time.Time
	}{
		// Normal B.E. dates.
		{"5/2/2551", time.Date(2008, 2, 5, 0, 0, 0, 0, time.UTC)},
		{"12/3/2535", time.Date(1992, 3, 12, 0, 0, 0, 0, time.UTC)},
		{"1/1/2566", time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"31/12/2568", time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)},
		// Edge cases.
		{"", time.Time{}},
		{"invalid", time.Time{}},
		{"1/2", time.Time{}},       // too few parts
		{"1/2/3/4", time.Time{}},   // too many parts
		{"a/2/2551", time.Time{}},  // non-numeric day
		{"1/b/2551", time.Time{}},  // non-numeric month
		{"1/2/abcd", time.Time{}},  // non-numeric year
		{"0/2/2551", time.Time{}},  // day out of range
		{"1/0/2551", time.Time{}},  // month out of range
		{"1/13/2551", time.Time{}}, // month out of range
	}
	for _, tt := range tests {
		got := parsePublishDate(tt.input)
		if !got.Equal(tt.want) {
			t.Errorf("parsePublishDate(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestMapState(t *testing.T) {
	tests := []struct {
		state string
		want  string
	}{
		{"01", "in_force"},
		{"02", "repealed"},
		{"03", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := mapState(tt.state)
		if got != tt.want {
			t.Errorf("mapState(%q) = %q, want %q", tt.state, got, tt.want)
		}
	}
}

func TestParseLawNameEn(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"string value", `"FINANCIAL INSTITUTION BUSINESS ACT"`, "FINANCIAL INSTITUTION BUSINESS ACT"},
		{"boolean false", `false`, ""},
		{"boolean true", `true`, ""},
		{"null", `null`, ""},
		{"empty", ``, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseLawNameEn(json.RawMessage(tt.raw))
			if got != tt.want {
				t.Errorf("parseLawNameEn(%s) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestDiscoverFiltersAndPaginates(t *testing.T) {
	// Build a 2-page server: page 1 has a mix of types, page 2 has one Act.
	page1 := `{
		"meta": {"page": 1, "perpage": 3, "total": 4, "pages": 2},
		"data": [
			{
				"lawCode": "ธ0012-1B-0001",
				"lawNameTh": "พ.ร.บ. สถาบันการเงิน",
				"lawNameEn": false,
				"encTimelineID": "enc1",
				"year": 2008,
				"publishDate": "5/2/2551",
				"fileUUID": "https://example.com/dl/1.pdf",
				"childrens": "",
				"state": "01",
				"num": 1
			},
			{
				"lawCode": "ก0001-10-0001",
				"lawNameTh": "รัฐธรรมนูญ",
				"lawNameEn": false,
				"encTimelineID": "enc2",
				"year": 2017,
				"publishDate": "6/4/2560",
				"fileUUID": "",
				"childrens": "",
				"state": "01",
				"num": 2
			},
			{
				"lawCode": "ธ0015-1B-0003",
				"lawNameTh": "พ.ร.บ. หลักทรัพย์",
				"lawNameEn": "Securities Act",
				"encTimelineID": "enc3",
				"year": 1992,
				"publishDate": "12/3/2535",
				"fileUUID": "",
				"childrens": "",
				"state": "02",
				"num": 3
			}
		]
	}`
	page2 := `{
		"meta": {"page": 2, "perpage": 3, "total": 4, "pages": 2},
		"data": [
			{
				"lawCode": "ธ0019-1B-0004",
				"lawNameTh": "พ.ร.บ. คุ้มครองข้อมูลส่วนบุคคล",
				"lawNameEn": "Personal Data Protection Act",
				"encTimelineID": "enc4",
				"year": 2019,
				"publishDate": "27/5/2562",
				"fileUUID": "https://example.com/dl/4.pdf",
				"childrens": "",
				"state": "01",
				"num": 4
			}
		]
	}`

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case "1", "":
			fmt.Fprint(w, page1)
		case "2":
			fmt.Fprint(w, page2)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := New(srv.Client(), nil)
	s.baseURL = srv.URL

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// Should have 3 Acts (filtered out the Constitution).
	if len(docs) != 3 {
		t.Fatalf("docs = %d, want 3", len(docs))
	}

	// First doc.
	if docs[0].ExternalID != "ธ0012-1B-0001" {
		t.Errorf("doc[0].ExternalID = %q", docs[0].ExternalID)
	}
	if docs[0].Title != "พ.ร.บ. สถาบันการเงิน" {
		t.Errorf("doc[0].Title = %q", docs[0].Title)
	}
	if docs[0].Status != "in_force" {
		t.Errorf("doc[0].Status = %q", docs[0].Status)
	}
	if len(docs[0].Files) != 1 {
		t.Errorf("doc[0].Files = %d, want 1", len(docs[0].Files))
	}
	if docs[0].IssuedAt.Year() != 2008 {
		t.Errorf("doc[0].IssuedAt = %v, want 2008", docs[0].IssuedAt)
	}

	// Second doc: Act with no file, repealed.
	if docs[1].ExternalID != "ธ0015-1B-0003" {
		t.Errorf("doc[1].ExternalID = %q", docs[1].ExternalID)
	}
	if docs[1].Status != "repealed" {
		t.Errorf("doc[1].Status = %q", docs[1].Status)
	}
	if len(docs[1].Files) != 0 {
		t.Errorf("doc[1].Files = %d, want 0", len(docs[1].Files))
	}

	// Third doc: from page 2.
	if docs[2].ExternalID != "ธ0019-1B-0004" {
		t.Errorf("doc[2].ExternalID = %q", docs[2].ExternalID)
	}
	if docs[2].DocType != "พระราชบัญญัติ" {
		t.Errorf("doc[2].DocType = %q", docs[2].DocType)
	}

	// All docs have RawMeta.
	for i, d := range docs {
		if len(d.RawMeta) == 0 {
			t.Errorf("doc[%d].RawMeta is empty", i)
		}
	}
}

func TestDiscoverEmptyResponse(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"meta": {"page": 1, "perpage": 10, "total": 0, "pages": 0}, "data": []}`)
	}))
	defer srv.Close()

	s := New(srv.Client(), nil)
	s.baseURL = srv.URL

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("docs = %d, want 0", len(docs))
	}
}

func TestFetchDetailNoTid(t *testing.T) {
	// Without a tid parameter, FetchDetail returns a stub doc (no API call).
	s := New(nil, nil)
	doc, err := s.FetchDetail(context.Background(), ingest.DetailRef{
		ExternalID: "ธ0012-1B-0001",
		DetailURL:  "https://www.ocs.go.th/searchlaw/law/ธ0012-1B-0001",
	})
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}
	if doc.ExternalID != "ธ0012-1B-0001" {
		t.Errorf("ExternalID = %q", doc.ExternalID)
	}
	if doc.SourceID != SourceID {
		t.Errorf("SourceID = %q", doc.SourceID)
	}
	if doc.HTML != "" {
		t.Errorf("HTML should be empty without tid, got len=%d", len(doc.HTML))
	}
}

func TestFetchDetailWithSections(t *testing.T) {
	getLawDocResp := `{
		"respBody": {
			"lawInfo": {
				"lawNameTh": "พระราชบัญญัติธุรกิจสถาบันการเงิน พ.ศ. 2551",
				"publishDateAd": "2008-02-13",
				"effectiveDateStartAd": "2008-08-05",
				"stateId": "01"
			},
			"lawSections": [
				{
					"sectionTypeId": 8,
					"sectionNo": "1",
					"sectionLabel": "หมวด 1 บททั่วไป",
					"sectionContent": "<p>บททั่วไป</p>"
				},
				{
					"sectionTypeId": 4,
					"sectionNo": "1",
					"sectionLabel": "มาตรา 1",
					"sectionContent": "<p>พระราชบัญญัตินี้เรียกว่า...</p>"
				},
				{
					"sectionTypeId": 4,
					"sectionNo": "2",
					"sectionLabel": "มาตรา 2",
					"sectionContent": "<p>พระราชบัญญัตินี้ให้ใช้บังคับ...</p>"
				},
				{
					"sectionTypeId": 9,
					"sectionNo": "1",
					"sectionLabel": "ส่วน 1 การจัดตั้ง",
					"sectionContent": "<p>การจัดตั้ง</p>"
				}
			]
		}
	}`

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, getLawDocResp)
	}))
	defer srv.Close()

	s := New(srv.Client(), nil)
	s.textBaseURL = srv.URL

	doc, err := s.FetchDetail(context.Background(), ingest.DetailRef{
		ExternalID: "ธ0012-1B-0001",
		DetailURL:  "https://www.ocs.go.th/searchlaw/law/ธ0012-1B-0001?tid=cXVaTkRBPT09",
	})
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}

	// Metadata.
	if doc.Title != "พระราชบัญญัติธุรกิจสถาบันการเงิน พ.ศ. 2551" {
		t.Errorf("Title = %q", doc.Title)
	}
	if doc.Status != "in_force" {
		t.Errorf("Status = %q", doc.Status)
	}
	if doc.IssuedAt.Year() != 2008 || doc.IssuedAt.Month() != 2 || doc.IssuedAt.Day() != 13 {
		t.Errorf("IssuedAt = %v", doc.IssuedAt)
	}
	if doc.EffectiveAt.Year() != 2008 || doc.EffectiveAt.Month() != 8 || doc.EffectiveAt.Day() != 5 {
		t.Errorf("EffectiveAt = %v", doc.EffectiveAt)
	}

	// HTML body.
	if doc.HTML == "" {
		t.Fatal("HTML is empty")
	}
	// Should contain chapter and section headings.
	for _, want := range []string{"<h3>หมวด 1 บททั่วไป</h3>", "<h4>มาตรา 1</h4>", "<h4>มาตรา 2</h4>", "<h2>ส่วน 1 การจัดตั้ง</h2>"} {
		if !strings.Contains(doc.HTML, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
	// Should contain the section content.
	if !strings.Contains(doc.HTML, "พระราชบัญญัตินี้เรียกว่า") {
		t.Error("HTML missing section content")
	}
}

func TestFetchDetailEmptySections(t *testing.T) {
	getLawDocResp := `{
		"respBody": {
			"lawInfo": {
				"lawNameTh": "พ.ร.บ. ทดสอบ",
				"publishDateAd": "",
				"effectiveDateStartAd": "",
				"stateId": "02"
			},
			"lawSections": []
		}
	}`

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, getLawDocResp)
	}))
	defer srv.Close()

	s := New(srv.Client(), nil)
	s.textBaseURL = srv.URL

	doc, err := s.FetchDetail(context.Background(), ingest.DetailRef{
		ExternalID: "ธ0012-1B-0099",
		DetailURL:  "https://www.ocs.go.th/searchlaw/law/ธ0012-1B-0099?tid=abc123",
	})
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}
	if doc.Title != "พ.ร.บ. ทดสอบ" {
		t.Errorf("Title = %q", doc.Title)
	}
	if doc.Status != "repealed" {
		t.Errorf("Status = %q", doc.Status)
	}
	if doc.HTML != "" {
		t.Errorf("HTML should be empty for zero sections, got len=%d", len(doc.HTML))
	}
}

func TestFetchDetailEmptyID(t *testing.T) {
	s := New(nil, nil)
	_, err := s.FetchDetail(context.Background(), ingest.DetailRef{})
	if err == nil {
		t.Fatal("FetchDetail with empty ID should error")
	}
}

func TestExtractTimelineID(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"with tid", "https://www.ocs.go.th/searchlaw/law/ธ0012-1B-0001?tid=cXVaTkRBPT09", "cXVaTkRBPT09"},
		{"no tid", "https://www.ocs.go.th/searchlaw/law/ธ0012-1B-0001", ""},
		{"empty", "", ""},
		{"encoded", "https://www.ocs.go.th/searchlaw/law/x?tid=a%2Bb%3D%3D", "a+b=="},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTimelineID(tt.url)
			if got != tt.want {
				t.Errorf("extractTimelineID(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestBuildSectionsHTML(t *testing.T) {
	sections := []lawSection{
		{SectionTypeID: json.Number(fmt.Sprint(sectionTypePart)), SectionNo: "1", SectionLabel: "ส่วน 1 ทั่วไป", SectionContent: "<p>general</p>"},
		{SectionTypeID: json.Number(fmt.Sprint(sectionTypeChapter)), SectionNo: "1", SectionLabel: "หมวด 1", SectionContent: "<p>chapter 1</p>"},
		{SectionTypeID: json.Number(fmt.Sprint(sectionTypeMatra)), SectionNo: "1", SectionLabel: "มาตรา 1", SectionContent: "<p>section 1</p>"},
		{SectionTypeID: json.Number(fmt.Sprint(sectionTypeMatra)), SectionNo: "2", SectionLabel: "", SectionContent: "<p>section 2</p>"},
		{SectionTypeID: json.Number("99"), SectionNo: "x", SectionLabel: "unknown", SectionContent: "<p>other</p>"},
		{SectionTypeID: json.Number(fmt.Sprint(sectionTypeMatra)), SectionNo: "3", SectionLabel: "มาตรา 3", SectionContent: "  "},
	}

	html := buildSectionsHTML(sections)

	// Part heading.
	if !strings.Contains(html, "<h2>ส่วน 1 ทั่วไป</h2>") {
		t.Error("missing part heading")
	}
	// Chapter heading.
	if !strings.Contains(html, "<h3>หมวด 1</h3>") {
		t.Error("missing chapter heading")
	}
	// Section headings.
	if !strings.Contains(html, "<h4>มาตรา 1</h4>") {
		t.Error("missing section 1 heading")
	}
	// Empty label falls back.
	if !strings.Contains(html, "<h4>มาตรา 2</h4>") {
		t.Error("missing section 2 fallback heading")
	}
	// Unknown type has no heading but content is included.
	if !strings.Contains(html, "<p>other</p>") {
		t.Error("missing unknown-type content")
	}
	// Whitespace-only content is skipped.
	if strings.Contains(html, "มาตรา 3") {
		t.Error("whitespace-only section should be skipped")
	}
}

func TestBuildSectionsHTMLEmpty(t *testing.T) {
	if got := buildSectionsHTML(nil); got != "" {
		t.Errorf("nil sections should return empty, got %q", got)
	}
	if got := buildSectionsHTML([]lawSection{}); got != "" {
		t.Errorf("empty sections should return empty, got %q", got)
	}
}

func TestParseAdDate(t *testing.T) {
	tests := []struct {
		input string
		want  time.Time
	}{
		{"2008-02-13", time.Date(2008, 2, 13, 0, 0, 0, 0, time.UTC)},
		{"2025-12-31", time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)},
		{"", time.Time{}},
		{"invalid", time.Time{}},
		{"13/02/2008", time.Time{}}, // wrong format
	}
	for _, tt := range tests {
		got := parseAdDate(tt.input)
		if !got.Equal(tt.want) {
			t.Errorf("parseAdDate(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestDiscoverDetailURLContainsTid(t *testing.T) {
	page1 := `{
		"meta": {"page": 1, "perpage": 10, "total": 1, "pages": 1},
		"data": [{
			"lawCode": "ธ0012-1B-0001",
			"lawNameTh": "test act",
			"lawNameEn": false,
			"encTimelineID": "cXVaTkRBPT09",
			"year": 2008,
			"publishDate": "5/2/2551",
			"fileUUID": "",
			"childrens": "",
			"state": "01",
			"num": 1
		}]
	}`

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, page1)
	}))
	defer srv.Close()

	s := New(srv.Client(), nil)
	s.baseURL = srv.URL

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("docs = %d, want 1", len(docs))
	}

	// DetailURL should contain the tid parameter.
	tid := extractTimelineID(docs[0].DetailURL)
	if tid != "cXVaTkRBPT09" {
		t.Errorf("tid in DetailURL = %q, want %q", tid, "cXVaTkRBPT09")
	}
}

func TestSourceID(t *testing.T) {
	s := New(nil, nil)
	if s.ID() != "ocs" {
		t.Errorf("ID() = %q, want %q", s.ID(), "ocs")
	}
}
