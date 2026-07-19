package pipeline

import (
	"testing"
)

func TestParseBNMSupersessionClause(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int // expected entries count; 0 means no clause found
	}{
		{
			name:    "single doc simple",
			content: `7.1 This policy document supersedes the Policy Document on Credit Card and Policy Document on Credit Card-i issued on 2 July 2019.`,
			// This is a single superseded reference with two PDs named together.
			// The parser will extract what it can.
			want: 1,
		},
		{
			name: "enumerated two docs",
			content: `7.1 This Policy Document supersedes the following:
(a) Policy document on Management of Customer Information and Permitted Disclosures issued on 3 April 2023; and
(b) BNM's Letter on Disclosure of Customer Information to Lembaga Hasil Dalam Negeri issued on 18 February 2022.`,
			want: 2,
		},
		{
			name: "enumerated four docs debit card",
			content: `7.1  This policy document supersedes the following:
(a) Circular on Debit Card Cash Out Facility issued on 29 October 2007;
(b) Circular on Contactless Functionality in Debit Cards and Prepaid Cards issued on 12 August 2016;
(c) Policy Document on Debit Card issued on 2 December 2016; and
(d) Policy Document on Debit Card-i issued on 2 December 2016.`,
			want: 4,
		},
		{
			name:    "single doc with date",
			content: `8.1 This policy document supersedes the Guidelines on Financial Reporting for Development Financial Institutions issued on 28 July 2020.`,
			want:    1,
		},
		{
			name:    "single doc simple e-KYC",
			content: `7.1 This policy document supersedes the Electronic Know-Your-Customer (e-KYC) policy document issued on 30 June 2020.`,
			want:    1,
		},
		{
			name:    "single doc outsourcing",
			content: `7.1 This policy document supersedes the policy document on Outsourcing issued on 28 December 2018.`,
			want:    1,
		},
		{
			name: "go-fitz line break in Policy Document",
			content: `7.1 This
Policy
Document supersedes the
following: (a) Policy document on Management of Customer Information and Permitted Disclosures issued on 3 April 2023; and (b) BNM's Letter on Disclosure of Customer Information to Lembaga Hasil Dalam Negeri issued on 18 February 2022.`,
			want: 2,
		},
		{
			name:    "no supersession clause",
			content: `7.1 This policy document sets out the requirements for technology risk management.`,
			want:    0,
		},
		{
			name:    "mentions supersede without this policy document",
			content: `S 14.1 The new framework shall supersede the existing provisions when it comes into effect.`,
			want:    0,
		},
		{
			name:    "single doc ORR",
			content: `Policy document superseded 6.1 This policy document supersedes the Operational Risk Reporting (ORR) policy document issued on 10 April 2025.`,
			want:    1,
		},
		{
			name:    "single doc sandbox",
			content: `4.2  This policy document supersedes the Policy Document on Financial Technology Regulatory Sandbox Framework issued on 18 October 2016.`,
			want:    1,
		},
		{
			name:    "single doc e-Money",
			content: `7.1 This policy document supersedes the policy document on Electronic Money (E-Money) issued on 30 December 2022.`,
			want:    1,
		},
		{
			name: "single doc IFTF",
			content: `7.1 This policy document supersedes the following document that has been issued by BNM:
(a) Interoperable Credit Transfer Framework issued on 23 December 2019.`,
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clause := parseBNMSupersessionClause(tt.content)
			if tt.want == 0 {
				if clause != nil {
					t.Fatalf("expected no clause, got %d entries", len(clause.Entries))
				}
				return
			}
			if clause == nil {
				t.Fatal("expected clause, got nil")
			}
			if len(clause.Entries) != tt.want {
				t.Fatalf("entries = %d, want %d; entries: %+v", len(clause.Entries), tt.want, clause.Entries)
			}
			// Every entry must have a non-empty title.
			for i, e := range clause.Entries {
				if e.Title == "" {
					t.Errorf("entry[%d].Title is empty", i)
				}
			}
		})
	}
}

func TestParseBNMSupersessionEntryTitles(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantTitle string
		wantDate  string
	}{
		{
			name:      "MCIPD with date",
			content:   `7.1 This policy document supersedes the policy document on Management of Customer Information and Permitted Disclosures issued on 17 October 2017.`,
			wantTitle: "Management of Customer Information and Permitted Disclosures",
			wantDate:  "17 October 2017",
		},
		{
			name:      "e-KYC with parenthetical",
			content:   `7.1 This policy document supersedes the Electronic Know-Your-Customer (e-KYC) policy document issued on 30 June 2020.`,
			wantTitle: "Electronic Know-Your-Customer (e-KYC)",
			wantDate:  "30 June 2020",
		},
		{
			name:      "outsourcing",
			content:   `7.1 This policy document supersedes the policy document on Outsourcing issued on 28 December 2018.`,
			wantTitle: "Outsourcing",
			wantDate:  "28 December 2018",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clause := parseBNMSupersessionClause(tt.content)
			if clause == nil || len(clause.Entries) == 0 {
				t.Fatal("expected at least one entry")
			}
			entry := clause.Entries[0]
			if entry.Title != tt.wantTitle {
				t.Errorf("title = %q, want %q", entry.Title, tt.wantTitle)
			}
			if entry.IssuedDate != tt.wantDate {
				t.Errorf("date = %q, want %q", entry.IssuedDate, tt.wantDate)
			}
		})
	}
}

func TestBNMTitleMatch(t *testing.T) {
	tests := []struct {
		name        string
		clauseTitle string
		corpusTitle string
		want        bool
	}{
		{
			name:        "exact",
			clauseTitle: "Outsourcing",
			corpusTitle: "Outsourcing",
			want:        true,
		},
		{
			name:        "case difference",
			clauseTitle: "outsourcing",
			corpusTitle: "Outsourcing",
			want:        true,
		},
		{
			name:        "plural difference Disclosure vs Disclosures",
			clauseTitle: "Management of Customer Information and Permitted Disclosures",
			corpusTitle: "Management of Customer Information and Permitted Disclosure",
			want:        true, // containment match
		},
		{
			name:        "corpus title is longer",
			clauseTitle: "Management of Customer Information and Permitted Disclosures",
			corpusTitle: "Management of Customer Information and Permitted Disclosures (Revised 2025)",
			want:        true, // containment after stripping parens
		},
		{
			name:        "no match",
			clauseTitle: "Outsourcing",
			corpusTitle: "Risk Management in Technology (RMiT)",
			want:        false,
		},
		{
			name:        "empty clause title",
			clauseTitle: "",
			corpusTitle: "Outsourcing",
			want:        false,
		},
		{
			name:        "empty corpus title",
			clauseTitle: "Outsourcing",
			corpusTitle: "",
			want:        false,
		},
		{
			name:        "e-KYC abbreviation",
			clauseTitle: "Electronic Know-Your-Customer (e-KYC)",
			corpusTitle: "Electronic Know-Your-Customer (e-KYC)",
			want:        true,
		},
		{
			name:        "financial reporting DFI",
			clauseTitle: "Financial Reporting for Development Financial Institutions",
			corpusTitle: "Financial Reporting for Development Financial Institutions",
			want:        true,
		},
		{
			name:        "unrelated long titles",
			clauseTitle: "Anti-Money Laundering, Countering Financing of Terrorism",
			corpusTitle: "Business Continuity Management",
			want:        false,
		},
		{
			name:        "main AML vs supplementary doc rejects",
			clauseTitle: "Anti-Money Laundering, Countering Financing of Terrorism and Targeted Financial Sanctions for Financial Institutions (AML/CFT and TFS for FIs)",
			corpusTitle: "Anti-Money Laundering, Countering Financing of Terrorism and Targeted Financial Sanctions for Financial Institutions (AML/CFT and TFS for FIs) (Supplementary Document No. 1) – Money Services Business Sector",
			want:        false, // ratio too low — materially different documents
		},
		{
			name:        "supplementary doc exact match",
			clauseTitle: "Anti-Money Laundering, Countering Financing of Terrorism and Targeted Financial Sanctions for Financial Institutions (AML/CFT and TFS for FIs) (Supplementary Document No.1) – Money Services Business Sector",
			corpusTitle: "Anti-Money Laundering, Countering Financing of Terrorism and Targeted Financial Sanctions for Financial Institutions (AML/CFT and TFS for FIs) (Supplementary Document No. 1) – Money Services Business Sector",
			want:        true, // same doc after normalization
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bnmTitleMatch(tt.clauseTitle, tt.corpusTitle)
			if got != tt.want {
				t.Errorf("bnmTitleMatch(%q, %q) = %v, want %v",
					tt.clauseTitle, tt.corpusTitle, got, tt.want)
			}
		})
	}
}

func TestNormalizeBNMTitle(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Outsourcing", "outsourcing"},
		{"Policy Document on Outsourcing", "outsourcing"},
		{"Guidelines on Best Practices", "best practices"},
		{"  Extra  Spaces  ", "extra spaces"},
		{"Title (Revised)", "title"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeBNMTitle(tt.input)
			if got != tt.want {
				t.Errorf("normalizeBNMTitle(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
