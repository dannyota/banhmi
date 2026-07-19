package scope

import (
	"encoding/csv"
	"os"
	"testing"
)

// TestIDScopeVocabularyAdmitsRegulators is a throwaway verification of the ID
// seed vocabulary against real BPK/BI document numbers and titles.
func TestIDScopeVocabularyAdmitsRegulators(t *testing.T) {
	f, err := os.Open("../../deploy/seed/scope_term_id.csv")
	if err != nil {
		t.Fatalf("open seed: %v", err)
	}
	defer func() { _ = f.Close() }()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	var terms []Term
	for _, r := range rows[1:] {
		terms = append(terms, Term{Text: r[0], Class: r[1]})
	}
	m := Load(terms)

	cases := []struct {
		number, title, abstract string
		want                    bool
		why                     string
	}{
		// Regulator-issued: must be IN scope by issuer.
		{"POJK 44/2024", "Rahasia Bank", "", true, "bank secrecy — was dropped before"},
		{"POJK 30/2024", "Konglomerasi Keuangan dan Perusahaan Induk Konglomerasi Keuangan", "", true, "financial conglomerates — was dropped before"},
		{"POJK 40/2024", "Penyelenggaraan Produk Bank Umum", "", true, "commercial bank products"},
		{"POJK 43/2024", "Pengembangan Kualitas Sumber Daya Manusia Lembaga Pembiayaan", "", true, "HR at financing cos — still OJK"},
		{"SEOJK 29/2022", "Laporan Keuangan Bank Perkreditan Rakyat", "", true, "OJK circular"},
		// BI number forms taken verbatim from pkg/ingest/bi/testdata — "No." (not
		// "Nomor"), and the classic slash form. Both must match.
		{"PBI No.10 Tahun 2025", "Sistem Pembayaran", "", true, "BI regulation, No. form"},
		{"PBI NO.4 TAHUN 2025", "Uang Elektronik", "", true, "BI regulation, upper-case"},
		{"23/9/PBI/2021", "Penyelenggaraan Kartu Kredit", "", true, "BI regulation, slash form"},
		{"PADG NO.15 TAHUN 2024", "Penyelesaian Transaksi Bilateral", "", true, "BI board regulation"},
		{"22/1/PADG/2021", "Setelmen Dana Seketika", "", true, "BI board regulation, slash form"},
		{"LPS 3/2021", "Program Penjaminan Simpanan", "", true, "deposit insurance"},
		{"PPATK 2/2023", "Tata Cara Pelaporan Transaksi Keuangan Mencurigakan", "", true, "AML"},
		{"BSSN 8/2020", "Sistem Pengamanan Dalam Penyelenggaraan Sistem Elektronik", "", true, "cybersecurity agency"},

		// Broad-mandate issuers: in scope ONLY on topic.
		{"UU 27/2022", "Pelindungan Data Pribadi", "", true, "data protection law — on topic"},
		{"UU 11/2008", "Informasi dan Transaksi Elektronik", "", true, "ITE law — on topic"},
		{"UU 4/2023", "Pengembangan dan Penguatan Sektor Keuangan", "", true, "P2SK — on topic"},
		{"UU 130/2024", "Kabupaten Bone di Provinsi Sulawesi Selatan", "", false, "regency creation — MUST be junk"},
		{"UU 30/2024", "Kabupaten Bangka di Provinsi Kepulauan Bangka Belitung", "", false, "regency creation — MUST be junk"},
		{"PP 30/2024", "Pengelolaan Sumber Daya Air", "", false, "water management — MUST be junk"},
		{"PMK 78/2023", "Tarif Bea Masuk atas Barang Impor", "", false, "customs tariff — MUST be junk"},
		// Perpres: signal, so only in scope with a topic match.
		{"Perpres 47/2023", "Strategi Keamanan Siber Nasional dan Manajemen Krisis Siber", "", true, "perpres signal + keamanan siber strong"},
		{"Perpres 99/2020", "Pengadaan Vaksin dan Pelaksanaan Vaksinasi", "", false, "perpres signal only — no topic match"},
		// PMK: signal, in scope only with topic match (same as before).
		{"PMK 133/2022", "Tata Kelola Teknologi Informasi dan Komunikasi", "", true, "pmk signal + tata kelola teknologi informasi strong"},
		{"KOMINFO 5/2020", "Penyelenggara Sistem Elektronik Lingkup Privat", "", true, "PSE — on topic"},
		{"KOMINFO 3/2021", "Standar Penyelenggaraan Penyiaran", "", false, "broadcast standards — MUST be junk"},
	}

	for _, c := range cases {
		got := m.Match(c.number, c.title, c.abstract)
		if got.InScope != c.want {
			t.Errorf("Match(%q, %q) InScope=%v want %v — %s (matched=%v)",
				c.number, c.title, got.InScope, c.want, c.why, got.Matched)
			continue
		}
		t.Logf("OK  in_scope=%-5v %-28s %s [%v]", got.InScope, c.number, c.why, got.Matched)
	}
}
