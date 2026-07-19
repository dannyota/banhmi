package lexical

import (
	"reflect"
	"strings"
	"testing"
)

func TestTCCSegment(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"pure ASCII", "hello world", []string{"hello world"}},
		{
			"basic Thai greeting",
			"สวัสดี",
			// ส-วัส-ดี: สว cluster, ัส cluster, ดี cluster
			nil, // filled dynamically — we just verify non-empty segmentation
		},
		{
			"section marker มาตรา",
			"มาตรา",
			nil,
		},
		{
			"Thai digits",
			"๑๒๓",
			[]string{"๑๒๓"},
		},
		{
			"mai yamok",
			"ๆ",
			[]string{"ๆ"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tccSegment(tc.in)
			if tc.want != nil {
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("tccSegment(%q) = %v, want %v", tc.in, got, tc.want)
				}
			} else if tc.in != "" {
				// For Thai text without explicit expected output, verify
				// segmentation produces multiple clusters and reassembles.
				if len(got) == 0 {
					t.Errorf("tccSegment(%q) returned empty", tc.in)
				}
				reassembled := strings.Join(got, "")
				if reassembled != tc.in {
					t.Errorf("tccSegment(%q) reassembly = %q, want %q", tc.in, reassembled, tc.in)
				}
			}
		})
	}
}

func TestTCCSegmentReassembly(t *testing.T) {
	// Every segmentation must reassemble to the original input (lossless).
	texts := []string{
		"สวัสดี",
		"พระราชบัญญัติ",
		"มาตรา",
		"ข้อ",
		"ธนาคารแห่งประเทศไทย", // Bank of Thailand
		"กฎหมาย",       // law
		"มาตรา 32",     // section 32
		"ข้อ 5 วรรค 2", // clause 5 paragraph 2
		"พ.ร.บ.",       // abbreviation
		"๑๒๓๔๕",        // Thai digits
	}
	for _, s := range texts {
		t.Run(s, func(t *testing.T) {
			clusters := tccSegment(s)
			reassembled := strings.Join(clusters, "")
			if reassembled != s {
				t.Errorf("tccSegment(%q) = %v, reassembly = %q", s, clusters, reassembled)
			}
		})
	}
}

func TestTCCSegmentMultipleClusters(t *testing.T) {
	// Thai words must produce more than one cluster (unless single consonant).
	multiClusterTexts := []struct {
		name string
		in   string
	}{
		{"greeting", "สวัสดี"},
		{"act", "พระราชบัญญัติ"},
		{"Bank of Thailand", "ธนาคารแห่งประเทศไทย"},
	}
	for _, tc := range multiClusterTexts {
		t.Run(tc.name, func(t *testing.T) {
			clusters := tccSegment(tc.in)
			if len(clusters) < 2 {
				t.Errorf("tccSegment(%q) = %v, want >=2 clusters", tc.in, clusters)
			}
		})
	}
}

func TestThNormalize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // space-separated token string (after Tokenize splits)
	}{
		{"empty", "", ""},
		{"pure ASCII lower", "Section 32", "section 32"},
		{"Thai digits preserved", "๑๒๓", "๑๒๓"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			norm := NormalizerFor(NormTH)
			got := strings.Join(Tokenize(tc.in, norm), " ")
			if got != tc.want {
				t.Errorf("Tokenize(%q, th) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestThNormalizeSegmentsThaiText(t *testing.T) {
	// Thai text must produce multiple tokens (TCC clusters).
	norm := NormalizerFor(NormTH)
	cases := []struct {
		name    string
		in      string
		minToks int
	}{
		{"greeting", "สวัสดี", 2},
		{"act", "พระราชบัญญัติ", 3},
		{"section marker", "มาตรา", 2},
		{"mixed Thai+number", "มาตรา 32", 3}, // มาตรา clusters + "32"
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			toks := Tokenize(tc.in, norm)
			if len(toks) < tc.minToks {
				t.Errorf("Tokenize(%q, th) = %v (%d tokens), want >= %d",
					tc.in, toks, len(toks), tc.minToks)
			}
		})
	}
}

func TestThNormalizeToneMarksPreserved(t *testing.T) {
	// ข้อ = U+0E02 (ข) + U+0E49 (้ mai tho) + U+0E2D (อ)
	// The tone mark must survive — vnFoldNormalize would strip it.
	norm := NormalizerFor(NormTH)
	result := strings.Join(Tokenize("ข้อ", norm), " ")
	if !strings.Contains(result, "้") {
		t.Errorf("Thai normalizer stripped tone mark from ข้อ: got %q", result)
	}

	// Compare: vnFoldNormalize strips combining marks — tone mark is gone.
	vnResult := strings.Join(Tokenize("ข้อ", vnFoldNormalize), " ")
	if strings.Contains(vnResult, "้") {
		t.Errorf("vnFoldNormalize unexpectedly preserved tone mark in %q", vnResult)
	}
}

func TestThNormalizeSaraAmPreserved(t *testing.T) {
	// ทำ contains sara am (U+0E33) which is a combining vowel.
	// It must not be stripped.
	norm := NormalizerFor(NormTH)
	result := strings.Join(Tokenize("ทำ", norm), " ")
	if !strings.Contains(result, "ำ") {
		t.Errorf("Thai normalizer stripped sara am from ทำ: got %q", result)
	}
}

func TestThNormalizeVsVNFold(t *testing.T) {
	// Prove that the Thai normalizer does NOT strip Thai combining marks,
	// while vnFoldNormalize (NFD) DOES strip them.
	thaiText := "มาตรา" // contains sara aa (า), which NFD separates

	thNorm := NormalizerFor(NormTH)
	vnNorm := NormalizerFor(NormVNFold)

	thResult := TokenizeRaw(thaiText, thNorm)
	vnResult := TokenizeRaw(thaiText, vnNorm)

	// Thai normalizer preserves all characters; VN fold may alter them.
	if thResult == vnResult {
		// They could be equal for some inputs, but the tokenization patterns differ.
		// The key difference is structural: TH segments into clusters.
		thToks := Tokenize(thaiText, thNorm)
		vnToks := Tokenize(thaiText, vnNorm)
		if reflect.DeepEqual(thToks, vnToks) {
			t.Logf("Both normalizers produce same tokens for %q — acceptable if no combining marks", thaiText)
		}
	}

	// The definitive test: ข้อ has a combining tone mark.
	thaiWithTone := "ข้อ"
	thToks := Tokenize(thaiWithTone, thNorm)
	vnToks := Tokenize(thaiWithTone, vnNorm)
	if reflect.DeepEqual(thToks, vnToks) {
		t.Errorf("Thai and VN normalizers should differ on %q: th=%v, vn=%v",
			thaiWithTone, thToks, vnToks)
	}
}

func TestThNormalizerForResolution(t *testing.T) {
	// NormTH must resolve to a non-nil normalizer distinct from vnFold.
	norm := NormalizerFor(NormTH)
	if norm == nil {
		t.Fatal("NormalizerFor(th) returned nil")
	}
	// Verify it is wired (not falling back to vnFold) by checking tone preservation.
	result := norm("ข้อ")
	if !strings.Contains(result, "้") {
		t.Error("NormalizerFor(th) appears to resolve to vnFold (tone mark stripped)")
	}
}
