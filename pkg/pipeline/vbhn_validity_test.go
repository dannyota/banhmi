package pipeline

import (
	"testing"
	"time"
)

func TestDecideVBHNValidity(t *testing.T) {
	mk := func(y int) time.Time { return time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC) }

	tests := []struct {
		name string
		in   []vbhnConsolidation
		want map[int64]vbhnDecision // keyed by documentID
	}{
		{
			name: "newest mirrors in_force base",
			in: []vbhnConsolidation{
				{documentID: 10, docKey: "VBHN|10", baseDocumentID: 1, baseDocKey: "THÔNG TƯ|1", issuedAt: mk(2024), baseStatusCode: "CHL", baseStatusClass: "in_force"},
			},
			want: map[int64]vbhnDecision{
				10: {documentID: 10, statusCode: "CHL", statusClass: "in_force", reason: "consolidates_base_status:THÔNG TƯ|1"},
			},
		},
		{
			name: "newest mirrors expired base",
			in: []vbhnConsolidation{
				{documentID: 11, docKey: "VBHN|11", baseDocumentID: 2, baseDocKey: "THÔNG TƯ|2", issuedAt: mk(2023), baseStatusCode: "HHL", baseStatusClass: "expired"},
			},
			want: map[int64]vbhnDecision{
				11: {documentID: 11, statusCode: "HHL", statusClass: "expired", reason: "consolidates_base_status:THÔNG TƯ|2"},
			},
		},
		{
			name: "older consolidation expired, newest mirrors base",
			in: []vbhnConsolidation{
				{documentID: 20, docKey: "VBHN|20", baseDocumentID: 3, baseDocKey: "THÔNG TƯ|3", issuedAt: mk(2020), baseStatusCode: "CHL", baseStatusClass: "in_force"},
				{documentID: 21, docKey: "VBHN|21", baseDocumentID: 3, baseDocKey: "THÔNG TƯ|3", issuedAt: mk(2024), baseStatusCode: "CHL", baseStatusClass: "in_force"},
			},
			want: map[int64]vbhnDecision{
				21: {documentID: 21, statusCode: "CHL", statusClass: "in_force", reason: "consolidates_base_status:THÔNG TƯ|3"},
				20: {documentID: 20, statusCode: "SUPERSEDED", statusClass: "expired", reason: "superseded_by_newer_consolidation"},
			},
		},
		{
			name: "unresolved base is unknown",
			in: []vbhnConsolidation{
				{documentID: 30, docKey: "VBHN|30", baseDocumentID: 0, issuedAt: mk(2024)},
			},
			want: map[int64]vbhnDecision{
				30: {documentID: 30, statusCode: "", statusClass: "unknown", reason: "consolidates_base_unresolved"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideVBHNValidity(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("decisions = %d, want %d (%+v)", len(got), len(tt.want), got)
			}
			// decideVBHNValidity returns decisions ordered by documentID.
			for i := 1; i < len(got); i++ {
				if got[i-1].documentID > got[i].documentID {
					t.Fatalf("decisions not ordered by documentID: %+v", got)
				}
			}
			for _, d := range got {
				w, ok := tt.want[d.documentID]
				if !ok {
					t.Fatalf("unexpected decision for doc %d: %+v", d.documentID, d)
				}
				if d != w {
					t.Fatalf("doc %d = %+v, want %+v", d.documentID, d, w)
				}
			}
		})
	}
}

// vbhnBaseFromText extracts the base document number from real gazette
// phrasings: the footnote "sửa đổi, bổ sung một số điều của/tại <TYPE> số X"
// (with the line-wrap space artifact "11/2013/ TT-NHNN"), the Điều-specific
// "sửa đổi Điều 2 Quyết định số X", and the preamble window before
// "được sửa đổi, bổ sung bởi:". The most frequent candidate wins; text naming
// no candidate yields "".
func TestVBHNBaseFromText(t *testing.T) {
	tests := []struct {
		name, text, want string
	}{
		{
			name: "footnote với của, space artifact, amender named first",
			text: "Điều này được sửa đổi theo quy định tại điểm e khoản 2 Điều 1 của Thông tư số 08/2025/ TT-NHNN " +
				"sửa đổi, bổ sung một số điều của Thông tư số 43/2015/TT-NHNN ngày 31 tháng 12 năm 2015 " +
				"của Thống đốc Ngân hàng Nhà nước Việt Nam quy định về tổ chức và hoạt động",
			want: "43/2015/TT-NHNN",
		},
		{
			name: "footnote với tại, majority over two footnotes",
			text: "sửa đổi, bổ sung một số điều tại Thông tư số 11/2013/ TT-NHNN ngày 15 tháng 5 năm 2013 quy định về cho vay. " +
				"sửa đổi, bổ sung một số điều tại Thông tư số 11/2013/ TT-NHNN ngày 15 tháng 5 năm 2013 của Ngân hàng Nhà nước",
			want: "11/2013/TT-NHNN",
		},
		{
			name: "một số điều without của (12/2018 phrasing)",
			text: "sửa đổi, bổ sung một số điều Thông tư số 22/2014/TT-NHNN ngày 15 tháng 8 năm 2014 hướng dẫn thực hiện chính sách tín dụng",
			want: "22/2014/TT-NHNN",
		},
		{
			name: "Điều-specific quyết định amendment",
			text: "về việc sửa đổi Điều 2 Quyết định số 479/2004/QĐ-NHNN ngày 29/4/2004 của Thống đốc Ngân hàng Nhà nước " +
				"ban hành Hệ thống tài khoản kế toán các Tổ chức tín dụng, có hiệu lực kể từ ngày 04 tháng 10 năm 2004",
			want: "479/2004/QĐ-NHNN",
		},
		{
			name: "preamble window before được sửa đổi bổ sung bởi",
			text: "Thông tư số 19/2016/TT-NHNN ngày 30 tháng 6 năm 2016 của Thống đốc Ngân hàng Nhà nước Việt Nam " +
				"quy định về hoạt động thẻ ngân hàng, có hiệu lực kể từ ngày 15 tháng 8 năm 2016, được sửa đổi, bổ sung bởi:",
			want: "19/2016/TT-NHNN",
		},
		{
			name: "no doc-number-bearing phrase stays unresolved",
			text: "sửa đổi, bổ sung một số văn bản quy phạm pháp luật của Ngân hàng Nhà nước Việt Nam " +
				"quy định về thành phần hồ sơ có bản sao chứng thực giấy tờ, văn bản",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := vbhnBaseFromText(tt.text); got != tt.want {
				t.Errorf("vbhnBaseFromText() = %q, want %q", got, tt.want)
			}
		})
	}
}
