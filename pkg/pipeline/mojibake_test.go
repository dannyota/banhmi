package pipeline

import (
	"testing"

	"danny.vn/banhmi/pkg/base/jurisdiction"
)

// TestLocalizedMojibakeIsPerJurisdiction pins the two halves of the contract:
// VN still rejects genuinely misdecoded Vietnamese (unchanged), and ID no longer
// mistakes ordinary symbols for corruption. A checkmark table in a Bank Indonesia
// PADG once tripped the Vietnamese marker set and discarded the whole regulation.
func TestLocalizedMojibakeIsPerJurisdiction(t *testing.T) {
	vn := jurisdiction.For("vn").MojibakeMarkers
	id := jurisdiction.For("id").MojibakeMarkers
	my := jurisdiction.For("my").MojibakeMarkers

	if vn == "" {
		t.Fatal("vn must declare mojibake markers — it is the language the check exists for")
	}
	if id != "" || my != "" {
		t.Fatalf("near-ASCII jurisdictions must not carry VN markers: id=%q my=%q", id, my)
	}

	// Verbatim from PADG 20/15/2018 (BI-RTGS operating hours): "√" is a checkmark
	// glyph rendered from a symbol font, not corruption.
	checkmarkTable := "Keterangan Peserta Sistem BI-RTGS\n√ √ Peserta   √ √ 06.30 16.30\n"
	// Vietnamese UTF-8 misdecoded as Latin-1 — what the check exists to catch.
	realVNMojibake := "\n√ê√¨·ª√π 5. Ng√¢n h√†ng ph·∫£i b·∫£o ƒë·∫£m an to√†n\n"

	tests := []struct {
		name    string
		markers string
		text    string
		want    bool
	}{
		{"VN rejects real Vietnamese mojibake", vn, realVNMojibake, true},
		{"VN accepts clean Vietnamese", vn, "Điều 5. Ngân hàng phải bảo đảm an toàn thông tin\n", false},
		{"ID accepts the checkmark table (was discarding whole regulations)", id, checkmarkTable, false},
		{"MY accepts the checkmark table", my, checkmarkTable, false},
		// The decisive one: identical bytes, opposite verdicts — proof the signal
		// is a property of the language, not of the characters.
		{"VN would still flag that same table (markers are VN's, not universal)", vn, checkmarkTable, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := localizedMojibakeText(tt.markers, tt.text); got != tt.want {
				t.Fatalf("localizedMojibakeText() = %v, want %v", got, tt.want)
			}
		})
	}
}
