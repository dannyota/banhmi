package pipeline

import (
	"testing"
	"time"
)

func TestEnactingEffectiveDate(t *testing.T) {
	date := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	cases := []struct {
		name  string
		roots []Section
		want  time.Time
		ok    bool
	}{
		{
			name: "nghi dinh self-reference (52/2024 — VBPL wrongly says 2027)",
			roots: []Section{{Kind: "dieu", Heading: "Hiệu lực thi hành", Children: []Section{
				{Kind: "khoan", Content: "Nghị định này có hiệu lực thi hành từ ngày 01 tháng 7 năm 2024."},
			}}},
			want: date(2024, 7, 1), ok: true,
		},
		{
			name: "thong tu self-reference with 'kể' (50/2025)",
			roots: []Section{{Kind: "dieu", Heading: "Hiệu lực thi hành", Children: []Section{
				{Kind: "khoan", Content: "Thông tư này có hiệu lực thi hành kể từ ngày 15 tháng 02 năm 2026."},
				{Kind: "khoan", Content: "Các Thông tư sau đây hết hiệu lực kể từ ngày Thông tư này có hiệu lực thi hành:"},
			}}},
			want: date(2026, 2, 15), ok: true,
		},
		{
			name: "77/2025/TT-NHNN — clause 2026 vs VBPL effFrom typo 2025; trailing exception ignored",
			roots: []Section{{Kind: "dieu", Heading: "Hiệu lực thi hành", Children: []Section{
				{Kind: "khoan", Content: "Thông tư này có hiệu lực thi hành kể từ ngày 01 tháng 3 năm 2026, trừ trường hợp quy định tại khoản 2, khoản 3 Điều này."},
			}}},
			want: date(2026, 3, 1), ok: true,
		},
		{
			name: "21/2024/TT-BTTTT — clause 2025 vs VBPL effFrom typo 2015",
			roots: []Section{{Kind: "dieu", Heading: "Hiệu lực thi hành", Children: []Section{
				{Kind: "khoan", Content: "Thông tư này có hiệu lực thi hành kể từ ngày 15 tháng 02 năm 2025."},
			}}},
			want: date(2025, 2, 15), ok: true,
		},
		{
			name: "genuinely future doc — clause matches VBPL, no override later",
			roots: []Section{{Kind: "dieu", Children: []Section{
				{Kind: "khoan", Content: "Thông tư này có hiệu lực thi hành kể từ ngày 01 tháng 01 năm 2027."},
			}}},
			want: date(2027, 1, 1), ok: true,
		},
		{
			name: "repeal of OTHER docs must not match (no self effective date)",
			roots: []Section{{Kind: "dieu", Children: []Section{
				{Kind: "khoan", Content: "Nghị định số 101/2012/NĐ-CP ngày 22 tháng 11 năm 2012 hết hiệu lực kể từ ngày Nghị định này có hiệu lực thi hành."},
			}}},
			ok: false,
		},
		{
			name:  "no enacting clause present",
			roots: []Section{{Kind: "dieu", Heading: "Phạm vi điều chỉnh", Content: "Nghị định này quy định về..."}},
			ok:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := enactingEffectiveDate(tc.roots)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (got date %v)", ok, tc.ok, got)
			}
			if ok && !got.Equal(tc.want) {
				t.Errorf("date = %v, want %v", got, tc.want)
			}
		})
	}
}
