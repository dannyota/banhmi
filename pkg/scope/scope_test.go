package scope

import "testing"

// testMatcher builds a Matcher from a representative subset of the seeded scope
// vocabulary — enough to exercise every case below without a database. The live
// vocabulary is loaded from the config schema (see docs/design/SCHEMA.md).
func testMatcher() *Matcher {
	return New(
		[]string{ // strong — in scope for any issuer (title + body)
			"an toàn thông tin", "an toàn hệ thống thông tin", "dữ liệu cá nhân",
			"trung gian thanh toán", "công nghiệp công nghệ số", "chữ ký điện tử",
			"dịch vụ tin cậy",
		},
		[]string{"chữ ký số"},                                 // strong_title — any issuer, title only
		[]string{"công nghệ thông tin"},                       // weak — needs a banking signal
		[]string{"ngân hàng", "tổ chức tín dụng", "tín dụng"}, // signals
	)
}

func TestMatch(t *testing.T) {
	m := testMatcher()
	tests := []struct {
		name     string
		number   string
		title    string
		abstract string
		inScope  bool
	}{
		{
			name:    "NHNN infosec circular (strong + signal)",
			number:  "09/2020/TT-NHNN",
			title:   "Quy định về an toàn hệ thống thông tin trong hoạt động ngân hàng",
			inScope: true,
		},
		{
			name:    "personal data law, non-bank issuer (strong, no signal needed)",
			number:  "91/2025/QH15",
			title:   "Luật Bảo vệ dữ liệu cá nhân",
			inScope: true,
		},
		{
			name:    "intermediary payment (strong)",
			number:  "40/2024/TT-NHNN",
			title:   "Quy định về hoạt động cung ứng dịch vụ trung gian thanh toán",
			inScope: true,
		},
		{
			name:    "digital-industry program (strong: công nghiệp công nghệ số)",
			number:  "840/QĐ-TTg",
			title:   "Phê duyệt Chương trình phát triển công nghiệp công nghệ số giai đoạn 2026 - 2030",
			inScope: true,
		},
		{
			name:    "weak term WITH banking signal (công nghệ thông tin + ngân hàng)",
			number:  "43/2023/TT-NHNN",
			title:   "Ứng dụng công nghệ thông tin trong hoạt động ngân hàng",
			inScope: true,
		},
		{
			name:    "weak term WITHOUT banking signal (health IT) — out",
			number:  "10/2024/TT-BYT",
			title:   "Ứng dụng công nghệ thông tin trong khám bệnh, chữa bệnh",
			inScope: false,
		},
		{
			name:    "interest rate circular — out",
			number:  "12/2024/TT-NHNN",
			title:   "Quy định về lãi suất tái cấp vốn của Ngân hàng Nhà nước",
			inScope: false,
		},
		{
			name:    "capital adequacy: 'an toàn vốn' must NOT match 'an toàn thông tin' — out",
			number:  "22/2019/TT-NHNN",
			title:   "Quy định các giới hạn, tỷ lệ bảo đảm an toàn vốn trong hoạt động của ngân hàng",
			inScope: false,
		},
		{
			name:    "terse amendment title needs a domain term, not a document-number list",
			number:  "77/2025/TT-NHNN",
			title:   "Sửa đổi, bổ sung một số điều về an toàn thông tin trong dịch vụ trực tuyến",
			inScope: true,
		},
		{
			name:    "personal-data decree, Government issuer (strong)",
			number:  "13/2023/NĐ-CP",
			title:   "Nghị định về bảo vệ dữ liệu cá nhân",
			inScope: true,
		},
		{
			name:    "e-signature decree (strong: chữ ký điện tử)",
			number:  "23/2025/NĐ-CP",
			title:   "Nghị định quy định về chữ ký điện tử và dịch vụ tin cậy",
			inScope: true,
		},
		{
			name:    "generic accounting circular — out",
			number:  "200/2014/TT-BTC",
			title:   "Hướng dẫn chế độ kế toán doanh nghiệp",
			inScope: false,
		},
		{
			name:     "strong term only in body — in (terse title, docAbs cites it)",
			number:   "77/2025/TT-NHNN",
			title:    "Sửa đổi, bổ sung một số điều của các Thông tư về cấp phép",
			abstract: "Căn cứ Luật An toàn thông tin mạng số 86/2015/QH13; Thống đốc ban hành Thông tư...",
			inScope:  true,
		},
		{
			name:     "weak term only in body — out (body floods with công nghệ thông tin)",
			number:   "15/2024/TT-NHNN",
			title:    "Quy định về tỷ lệ bảo đảm an toàn vốn",
			abstract: "...ứng dụng công nghệ thông tin trong quản trị; hệ thống thông tin nội bộ...",
			inScope:  false,
		},
		{
			name:     "document number only in body is not scope policy",
			number:   "80/2025/TT-NHNN",
			title:    "Sửa đổi một số điều về hoạt động cấp tín dụng",
			abstract: "...sửa đổi, bổ sung một số điều của Thông tư 50/2024/TT-NHNN của Thống đốc...",
			inScope:  false,
		},
		{
			name:    "strong_title in title — in (chữ ký số)",
			number:  "30/2024/TT-NHNN",
			title:   "Quy định về chữ ký số trong hoạt động ngân hàng",
			inScope: true,
		},
		{
			name:     "strong_title only in body — out (title-only, body boilerplate)",
			number:   "31/2024/TT-NHNN",
			title:    "Quy định về quản lý ngoại hối",
			abstract: "hồ sơ điện tử được sử dụng chữ ký số theo quy định về thủ tục hành chính",
			inScope:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.Match(tt.number, tt.title, tt.abstract)
			if got.InScope != tt.inScope {
				t.Fatalf("Match(%q, %q).InScope = %v, want %v (matched=%v)",
					tt.number, tt.title, got.InScope, tt.inScope, got.Matched)
			}
			if got.InScope && len(got.Matched) == 0 {
				t.Fatalf("in scope but no matched terms recorded")
			}
		})
	}
}

// TestLoad checks the config-row path buckets terms by class correctly.
// TestMatchQuery covers the query-time selective-fold fallback: a query typed
// with NO Vietnamese diacritics is retried against folded vocabulary, while
// diacritic-bearing queries stay strict (never folded), and the index-time Match
// is unaffected.
func TestMatchQuery(t *testing.T) {
	m := testMatcher()
	tests := []struct {
		name  string
		query string
		want  bool
	}{
		// No-diacritics queries — the recall fix. Folded vocab resolves them.
		{"nodiacritic strong", "ngan hang phai dam bao an toan he thong thong tin nhu the nao", true},
		{"nodiacritic strong no signal needed", "an toan thong tin la gi", true},
		{"nodiacritic weak with signal", "ngan hang thue ngoai cong nghe thong tin", true},
		{"nodiacritic weak without signal stays out", "cong nghe thong tin trong y te", false},
		{"nodiacritic genuinely out of scope", "cach pha ca phe sua da ngon", false},
		// Diacritic-bearing queries are unchanged (strict, never folded).
		{"diacritic in scope", "an toàn thông tin ngân hàng", true},
		{"diacritic out of scope (capital adequacy, not safety)", "tỷ lệ an toàn vốn", false},
		{"diacritic out of scope coffee", "cách pha cà phê sữa đá", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := m.MatchQuery(tt.query); got.InScope != tt.want {
				t.Fatalf("MatchQuery(%q).InScope = %v, want %v (matched=%v)", tt.query, got.InScope, tt.want, got.Matched)
			}
		})
	}
	// Index-time Match must NOT fold: a no-diacritic title stays out of scope.
	if got := m.Match("", "an toan thong tin", ""); got.InScope {
		t.Fatalf("Match must not diacritic-fold (index-time): got InScope=true for folded title")
	}
}

func TestLoad(t *testing.T) {
	m := Load(
		[]Term{
			{Text: "dữ liệu cá nhân", Class: ClassStrong},
			{Text: "công nghệ thông tin", Class: ClassWeak},
			{Text: "ngân hàng", Class: ClassSignal},
			{Text: "ignored", Class: "bogus"},
		},
	)
	if got := m.Match("13/2023/NĐ-CP", "Nghị định về bảo vệ dữ liệu cá nhân", ""); !got.InScope {
		t.Fatalf("strong term via Load should be in scope")
	}
	if got := m.Match("10/2024/TT-BYT", "Ứng dụng công nghệ thông tin trong y tế", ""); got.InScope {
		t.Fatalf("weak term without signal should be out of scope")
	}
}
