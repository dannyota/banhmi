// Package extract turns a downloaded document file into normalized text using a
// deterministic engine, and gates the result for quality. There is no cloud or
// generative AI in this path: DOCX/HTML/PDF text is converted with local
// MarkItDown, legacy DOC is rendered to PDF first, and only genuinely
// unextractable input falls back to self-hosted OCR.
// The quality gate (Assess) decides whether extracted text is trustworthy or must
// be routed to OCR / flagged for review — PDFs are never assumed uniform.
package extract

import (
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Source identifies which engine produced the text, recorded as provenance.
type Source string

const (
	SourceDOCX Source = "docx"
	SourcePDF  Source = "pdf"
	SourceOCR  Source = "ocr"
)

// Default gate thresholds. Values mirror the seed CSV (deploy/seed/setting.csv).
const (
	defaultMaxBadRatio         = 0.01
	defaultMinDiacriticDensity = 0.02
	defaultMinLetters          = 50
	defaultPassThreshold       = 0.6
	defaultMaxWhitespaceRatio  = 0.40
	defaultMaxPUARatio         = 0.005
	defaultMaxMojibakeRatio    = 0.005
)

// SourceUnavailable reports source placeholder text rather than legal content.
// Official sites sometimes publish a document row before the attachment/body is
// ready, commonly with "Đang cập nhật file đính kèm". This is not an OCR or
// conversion failure; it should trigger a later source recheck.
func SourceUnavailable(text string) bool {
	folded := foldVietnamese(strings.Join(strings.Fields(Normalize(text)), " "))
	if folded == "" {
		return true
	}
	return officialPlaceholderFolded(folded)
}

// OfficialPlaceholder reports explicit source placeholder text. Unlike
// SourceUnavailable, empty text is not enough; callers that can fall back to OCR
// use this to avoid mistaking a failed conversion for an unavailable source.
func OfficialPlaceholder(text string) bool {
	folded := foldVietnamese(strings.Join(strings.Fields(Normalize(text)), " "))
	if folded == "" {
		return false
	}
	return officialPlaceholderFolded(folded)
}

func officialPlaceholderFolded(folded string) bool {
	letters := 0
	for _, r := range folded {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if letters > 200 {
		return false
	}
	return strings.Contains(folded, "dang cap nhat") &&
		(strings.Contains(folded, "file") || strings.Contains(folded, "dinh kem") || letters < 80)
}

// GateConfig holds the operator-tunable thresholds for the Phase-2 content
// quality gate. Use GateFromSettings to build from the config schema, or
// DefaultGate for the compiled-in defaults.
type GateConfig struct {
	// MaxBadRatio is the maximum fraction of replacement/control characters
	// tolerated (e.g. 0.01 = 1 %).
	MaxBadRatio float64
	// MinDiacriticDensity is the minimum ratio of non-ASCII letters to all
	// letters below which real Vietnamese is unlikely (e.g. 0.02).
	MinDiacriticDensity float64
	// MinLetters is the minimum letter count below which text is too short to
	// judge (e.g. 50).
	MinLetters int
	// PassThreshold is the minimum confidence score for the gate to pass (e.g.
	// 0.6).
	PassThreshold float64
	// MaxWhitespaceRatio is the maximum fraction of whitespace runes above
	// which the document is probably image-heavy or badly extracted (e.g. 0.40).
	MaxWhitespaceRatio float64
	// MaxPUARatio is the maximum fraction of Unicode Private-Use-Area runes
	// (U+E000–U+F8FF), a strong mojibake signal from TCVN3/VNI legacy fonts
	// (e.g. 0.005).
	MaxPUARatio float64
}

// DefaultGate returns the compiled-in default thresholds, matching the seed CSV.
func DefaultGate() GateConfig {
	return GateConfig{
		MaxBadRatio:         defaultMaxBadRatio,
		MinDiacriticDensity: defaultMinDiacriticDensity,
		MinLetters:          defaultMinLetters,
		PassThreshold:       defaultPassThreshold,
		MaxWhitespaceRatio:  defaultMaxWhitespaceRatio,
		MaxPUARatio:         defaultMaxPUARatio,
	}
}

// GateFromSettings builds a GateConfig from the config.setting key/value map
// returned by dbconfig.Queries.ListSettings. Unknown or unparseable keys fall
// back to DefaultGate values so partial operator overrides are safe.
func GateFromSettings(m map[string]string) GateConfig {
	g := DefaultGate()
	parseF := func(key string, dst *float64) {
		if v, ok := m[key]; ok {
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				*dst = f
			}
		}
	}
	parseI := func(key string, dst *int) {
		if v, ok := m[key]; ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				*dst = n
			}
		}
	}
	parseF("extract.pdf.max_bad_ratio", &g.MaxBadRatio)
	parseF("extract.pdf.min_diacritic_density", &g.MinDiacriticDensity)
	parseI("extract.pdf.min_letters", &g.MinLetters)
	parseF("extract.pdf.pass_threshold", &g.PassThreshold)
	parseF("extract.pdf.max_whitespace_ratio", &g.MaxWhitespaceRatio)
	parseF("extract.pdf.max_pua_ratio", &g.MaxPUARatio)
	return g
}

// AssessResult is the detailed verdict from GateConfig.Assess.
type AssessResult struct {
	Confidence float64 // 0.0–1.0
	OK         bool    // true → text passes the gate
	Reason     string  // short human-readable diagnosis (non-empty when !OK)
}

// Assess scores extracted Vietnamese text against the tuned thresholds and
// decides whether to trust it. It is deterministic — no model or cloud call.
//
// Signals checked:
//   - Bad/replacement character ratio (strong negative).
//   - Diacritic density: real VN text is non-ASCII-letter-dense.
//   - Whitespace ratio: very high whitespace → image-heavy or mis-extracted.
//   - PUA rune ratio: TCVN3/VNI mojibake surfaces in U+E000–U+F8FF.
func (g GateConfig) Assess(text string) AssessResult {
	text = norm.NFC.String(text)

	var total, letters, nonASCIILetters, bad, ws, pua, mojibake int
	for _, r := range text {
		total++
		if r == '�' || (unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' && r != '\f') {
			bad++
		}
		if unicode.IsLetter(r) {
			letters++
			if r > unicode.MaxASCII {
				nonASCIILetters++
			}
		}
		if unicode.IsSpace(r) {
			ws++
		}
		if r >= 0xE000 && r <= 0xF8FF {
			pua++
		}
		if isMojibakeMarker(r) {
			mojibake++
		}
	}

	if total == 0 {
		return AssessResult{Reason: "empty text"}
	}

	badRatio := float64(bad) / float64(total)
	wsRatio := float64(ws) / float64(total)
	puaRatio := float64(pua) / float64(total)
	mojibakeRatio := float64(mojibake) / float64(total)
	diacriticDensity := 0.0
	if letters > 0 {
		diacriticDensity = float64(nonASCIILetters) / float64(letters)
	}

	confidence := 1.0
	var reasons []string

	if badRatio > g.MaxBadRatio {
		penalty := badRatio * 5.0
		confidence -= penalty
		reasons = append(reasons, "bad chars")
	}
	if letters < g.MinLetters {
		confidence -= 0.3
		reasons = append(reasons, "too few letters")
	}
	if wsRatio > g.MaxWhitespaceRatio {
		confidence -= 0.3
		reasons = append(reasons, "high whitespace")
	}
	if puaRatio > g.MaxPUARatio {
		confidence -= 0.5
		reasons = append(reasons, "PUA runes (TCVN3/VNI mojibake)")
	}
	if mojibake >= 8 && mojibakeRatio > defaultMaxMojibakeRatio {
		confidence -= 0.7
		reasons = append(reasons, "UTF-8 mojibake markers")
	}
	if letters >= 200 && diacriticDensity < g.MinDiacriticDensity {
		confidence -= 0.5
		reasons = append(reasons, "low diacritic density")
	}
	confidence = clamp01(confidence)

	hardFail := badRatio >= g.MaxBadRatio ||
		puaRatio > g.MaxPUARatio ||
		(mojibake >= 8 && mojibakeRatio > defaultMaxMojibakeRatio) ||
		letters < g.MinLetters
	ok := confidence >= g.PassThreshold && !hardFail

	reason := ""
	if !ok {
		reason = strings.Join(reasons, "; ")
		if reason == "" {
			reason = "below confidence threshold"
		}
	}
	return AssessResult{Confidence: confidence, OK: ok, Reason: reason}
}

func isMojibakeMarker(r rune) bool {
	return strings.ContainsRune("√∆·ªƒ∫≠‚ÄØ", r)
}

// Assess scores extracted Vietnamese text and decides whether to trust it using
// default gate thresholds. It is a backward-compatible wrapper around
// DefaultGate().Assess for callers that don't need operator-tuned thresholds.
func Assess(text string) (confidence float64, ok bool) {
	r := DefaultGate().Assess(text)
	return r.Confidence, r.OK
}

// Normalize applies the hard NFC invariant that holds on every extraction path.
func Normalize(text string) string {
	return norm.NFC.String(text)
}

func clamp01(x float64) float64 {
	switch {
	case x < 0:
		return 0
	case x > 1:
		return 1
	default:
		return x
	}
}

func foldVietnamese(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		switch {
		case r == 'đ':
			b.WriteByte('d')
		case unicode.Is(unicode.Mn, r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
