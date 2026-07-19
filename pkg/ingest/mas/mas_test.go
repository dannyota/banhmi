package mas

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

func TestSolrResponseParsing(t *testing.T) {
	raw := `{
		"response": {
			"numFound": 3,
			"docs": [
				{
					"document_title_string_s": "Notice on Technology Risk Management (FSM-N05)",
					"page_url_s": "/regulation/notices/notice-fsm-n05",
					"mas_date_tdt": "2024-05-10T00:00:00Z",
					"mas_contenttype_s": "Notices",
					"document_shortsummary_t": "Applies to financial institutions",
					"itemid_s": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
				},
				{
					"document_title_string_s": "MAS Notice 655 [Cancelled]",
					"page_url_s": "/regulation/notices/notice-655",
					"mas_date_tdt": "2020-01-15T00:00:00Z",
					"mas_contenttype_s": "Notices",
					"document_shortsummary_t": "Prevention of money laundering",
					"itemid_s": "11111111-2222-3333-4444-555555555555"
				},
				{
					"document_title_string_s": "Guidelines on Outsourcing (PSN02)",
					"page_url_s": "/regulation/guidelines/guidelines-on-outsourcing",
					"mas_date_tdt": "2018-06-01T00:00:00Z",
					"mas_contenttype_s": "Guidelines",
					"document_shortsummary_t": "Outsourcing arrangements",
					"itemid_s": "99999999-8888-7777-6666-444444444444"
				}
			]
		}
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(raw))
	}))
	defer srv.Close()

	s := New(srv.Client(), nil)
	s.baseURL = srv.URL

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	if len(docs) != 3 {
		t.Fatalf("docs = %d, want 3", len(docs))
	}

	// First doc: normal notice with number.
	d := docs[0]
	if d.SourceID != "mas" {
		t.Errorf("source = %q", d.SourceID)
	}
	if d.ExternalID != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("external_id = %q", d.ExternalID)
	}
	if d.Number != "FSM-N05" {
		t.Errorf("number = %q, want FSM-N05", d.Number)
	}
	if d.Title != "Notice on Technology Risk Management (FSM-N05)" {
		t.Errorf("title = %q", d.Title)
	}
	if d.Status != "" {
		t.Errorf("status = %q, want empty", d.Status)
	}
	if string(d.DocType) != "Notices" {
		t.Errorf("doc_type = %q", d.DocType)
	}
	if d.IssuedAt.IsZero() {
		t.Error("issued_at is zero")
	}
	if d.RawMeta == nil {
		t.Error("raw_meta is nil")
	}

	// Second doc: cancelled notice.
	d = docs[1]
	if d.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", d.Status)
	}
	if d.Title != "MAS Notice 655" {
		t.Errorf("title = %q, want 'MAS Notice 655' (without [Cancelled])", d.Title)
	}
	if d.Number != "655" {
		t.Errorf("number = %q, want 655", d.Number)
	}

	// Third doc: guideline.
	d = docs[2]
	if d.Number != "PSN02" {
		t.Errorf("number = %q, want PSN02", d.Number)
	}
	if string(d.DocType) != "Guidelines" {
		t.Errorf("doc_type = %q, want Guidelines", d.DocType)
	}
}

func TestExtractNoticeNumber(t *testing.T) {
	tests := []struct {
		title string
		want  string
	}{
		// Existing patterns.
		{"Notice on Technology Risk Management (FSM-N05)", "FSM-N05"},
		{"Notice to Banks — MAS Notice 655", "655"},
		{"Guidelines on Outsourcing (PSN02)", "PSN02"},
		{"Notice on Cyber Hygiene (CMG-N03)", "CMG-N03"},
		{"MAS Notice 1111 on Reporting", "1111"},
		{"Notice on Prevention of Money Laundering (PSN06)", "PSN06"},
		{"Notice SFA04-N12 on Securities", "SFA04-N12"},
		{"Notice on Conduct MAS-N01", "MAS-N01"},

		// Bare "Notice NNN" without "MAS" prefix.
		{"Notice 655 Cyber Hygiene", "655"},
		{"Notice 655A Cyber Hygiene", "655A"},
		{"Notice 1014 Prevention of Money Laundering", "1014"},
		{"Notice 101 Maintenance of Insurance Funds", "101"},

		// "Notice SFA XX-NNN" with space.
		{"Notice SFA 04-N13 Risk Based Capital", "SFA 04-N13"},
		{"Notice SFA 02/02A/03-N01 Capital Requirements", "SFA 02/02A/03-N01"},
		{"Notice SFA 02A/03-N01 Financial Market Infrastructure", "SFA 02A/03-N01"},
		{"Notice SFA 06AA-N01 Submission of Period Reports", "SFA 06AA-N01"},
		{"Notice SFA 04/13-N01 Cancellation Period", "SFA 04/13-N01"},

		// PSN with letter suffixes.
		{"PSN01A Prevention of Money Laundering", "PSN01A"},
		{"PSN01AA Prevention of Money Laundering", "PSN01AA"},
		{"PSN04A Notice on Submission", "PSN04A"},

		// TCA notice.
		{"Notice TCA N06 Cyber Hygiene", "TCA N06"},

		// Bracket-enclosed references.
		{"Guidelines on Disclosure of Financial Information in Prospectuses [SFA 13-G18]", "SFA 13-G18"},
		{"Guidelines on Licensing and Conduct [SFA 04-G05]", "SFA 04-G05"},
		{"Guidelines on Implementation of Insurance Fund Concept [ID 1/09]", "ID 1/09"},
		{"Guidelines on Structured Deposits [FAA-G09]", "FAA-G09"},
		{"Guidelines on the Regulation of Short Selling [SFA 07A-G01]", "SFA 07A-G01"},

		// "ID N/NN" insurance directive references.
		{"ID 2/04 Applications for Exemptions - Insurance Act 1966", "ID 2/04"},
		{"Market Conduct and Service Standards for Direct General Insurers [ID 1/03]", "ID 1/03"},

		// "Guidelines to Notice NNN" — extracts the notice number.
		{"Guidelines to Notice 626 on Prevention of Money Laundering", "626"},
		{"Guidelines to Notice 626A on Prevention of Money Laundering", "626A"},
		{"Guidelines to Notice SFA 04-N02 on Prevention of Money Laundering", "SFA 04-N02"},
		{"Guidelines to Notice SFA 03AA-N01 on Prevention of Money Laundering", "SFA 03AA-N01"},
		{"Guidelines to Notice SFA 13-N01 on Prevention of Money Laundering", "SFA 13-N01"},

		// "Notice on ... (SFA XX-NNN)" — parenthesized.
		{"Notice on Supervision of Market Participants (SFA 02-N02)", "SFA 02-N02"},

		// Zero-width space in title.
		{"​Guidelines on Outsourcing", ""},
		{"Notice ​SFA 04-N07 Prohibited Representations", "SFA 04-N07"},
		{"​Guidelines on Risk Management Practices – Operational Risk", ""},
		{"​Guidelines on Structured Deposits [FAA-G09]", "FAA-G09"},

		// MAS Notice with letter suffix.
		{"MAS Notice 655A on Cyber Hygiene", "655A"},

		// No recognizable number.
		{"Guidelines on Environmental Risk Management", ""},
		{"Guidelines on Business Continuity Management", ""},
		{"Guidelines on Outsourcing (Banks)", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := extractNumber(tt.title)
		if got != tt.want {
			t.Errorf("extractNumber(%q) = %q, want %q", tt.title, got, tt.want)
		}
	}
}

func TestCancelledStatus(t *testing.T) {
	tests := []struct {
		raw        string
		wantTitle  string
		wantStatus string
	}{
		{
			"MAS Notice 655 [Cancelled]",
			"MAS Notice 655",
			"cancelled",
		},
		{
			"Notice on Technology Risk Management (FSM-N05)",
			"Notice on Technology Risk Management (FSM-N05)",
			"",
		},
		{
			"[Cancelled] Old Notice 123",
			"Old Notice 123",
			"cancelled",
		},
	}
	for _, tt := range tests {
		title, status := parseTitleStatus(tt.raw)
		if title != tt.wantTitle {
			t.Errorf("parseTitleStatus(%q) title = %q, want %q", tt.raw, title, tt.wantTitle)
		}
		if status != tt.wantStatus {
			t.Errorf("parseTitleStatus(%q) status = %q, want %q", tt.raw, status, tt.wantStatus)
		}
	}
}

func TestFetchDetailExtractsPDFs(t *testing.T) {
	html := `<html><body>
		<p>Issued pursuant to: <a href="https://sso.agc.gov.sg/Act/BA">Banking Act</a>, Section 55</p>
		<p>Applies to: All banks in Singapore, merchant banks</p>
		<div>
			<a href="/-/media/mas-media-library/regulation/notices/cmg/notice-cmg-n03/notice-cmg-n03.pdf">Download PDF</a>
			<a href="/-/media/mas-media-library/regulation/notices/cmg/notice-cmg-n03/appendix-a.pdf">Appendix A</a>
		</div>
	</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()

	s := New(srv.Client(), nil)
	s.baseURL = srv.URL

	doc, err := s.FetchDetail(context.Background(), ingest.DetailRef{
		ExternalID: "test-guid",
		DetailURL:  srv.URL + "/regulation/notices/notice-cmg-n03",
	})
	if err != nil {
		t.Fatalf("FetchDetail error: %v", err)
	}
	if len(doc.Files) != 2 {
		t.Fatalf("files = %d, want 2", len(doc.Files))
	}
	if doc.Files[0].Name != "notice-cmg-n03.pdf" {
		t.Errorf("file[0].Name = %q", doc.Files[0].Name)
	}
	if doc.Files[0].Ext != "pdf" {
		t.Errorf("file[0].Ext = %q", doc.Files[0].Ext)
	}
	if doc.Files[1].Name != "appendix-a.pdf" {
		t.Errorf("file[1].Name = %q", doc.Files[1].Name)
	}
	// Check abstract has parsed metadata.
	if doc.Abstract == "" {
		t.Error("abstract is empty, expected metadata from detail page")
	}
}

func TestFetchDetailNoPDFs(t *testing.T) {
	html := `<html><body><p>No documents available.</p></body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()

	s := New(srv.Client(), nil)
	s.baseURL = srv.URL

	doc, err := s.FetchDetail(context.Background(), ingest.DetailRef{
		ExternalID: "test-guid",
		DetailURL:  srv.URL + "/some-page",
	})
	if err != nil {
		t.Fatalf("FetchDetail error: %v", err)
	}
	if len(doc.Files) != 0 {
		t.Fatalf("files = %d, want 0", len(doc.Files))
	}
}
