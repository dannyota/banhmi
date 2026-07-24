package pipeline

import "testing"

func TestVNCollapseSpacedDiacritics(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// Real corpus cases that the safe variant fixes.
		{"khoan right-merge and Truong left-merge", "Trườ ng hợp các kh oản", "Trường hợp các khoản"},
		{"khoi wedge", "tổng kh ố i lượng", "tổng khối lượng"},
		{"Dinh and hoat", "Đì nh chỉ hoạ t động", "Đình chỉ hoạt động"},
		{"lenh left-merge", "Bộ Tư lệ nh", "Bộ Tư lệnh"},
		{"Trach nhiem", "Trá ch nhiệ m thi hành", "Trách nhiệm thi hành"},
		{"uppercase NGAN HANG", "NGÂN HÀ NG", "NGÂN HÀNG"},
		{"hach toan bang", "hạ ch toá n trong bả ng cân", "hạch toán trong bảng cân"},
		{"phong via onset then coda", "Dự ph ò ng cụ thể", "Dự phòng cụ thể"},
		{"cong trinh", "Chi phí công tr ì nh", "Chi phí công trình"},
		{"the onset right-merge at line end", "Dự phòng cụ th ể", "Dự phòng cụ thể"},

		// Negative cases: must NOT change.
		{"list marker a)", "a) Mức xác định", "a) Mức xác định"},
		{"enumeration a b c", "điểm a, điểm b và điểm c", "điểm a, điểm b và điểm c"},
		{"roman chapter", "Chương I", "Chương I"},
		{"roman appendix", "Phụ lục I", "Phụ lục I"},
		{"uu dai thue", "ưu đãi về thuế", "ưu đãi về thuế"},
		{"di hoc o Ha Noi", "đi học ở Hà Nội", "đi học ở Hà Nội"},
		{"sach va vo", "sách và vở", "sách và vở"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := vnCollapseSpacedDiacritics(tt.in); got != tt.want {
				t.Fatalf("vnCollapseSpacedDiacritics(%q)\n  = %q\nwant %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestVNCollapseSpacedDiacriticsPreservesStructure(t *testing.T) {
	// Newlines and multi-space runs survive; merges never cross a line.
	in := "Trườ ng hợp\nkh oản  hai"
	want := "Trường hợp\nkhoản  hai"
	if got := vnCollapseSpacedDiacritics(in); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// A fragment at end of one line must not merge into the next line.
	if got := vnCollapseSpacedDiacritics("Bộ Tư\nlệ nh"); got != "Bộ Tư\nlệnh" {
		t.Fatalf("cross-line merge leaked: %q", got)
	}
}
