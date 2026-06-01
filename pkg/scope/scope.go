// Package scope decides whether a discovered document is within banhmi's crawl
// scope — Vietnamese banking digital/technology regulation: IT systems &
// security, cybersecurity, data & personal-data protection, e-transactions &
// e-signatures, cloud, payments & intermediary payment, digital banking, eKYC,
// and the cross-cutting central laws that bind banks. It also reports which terms
// matched, for the ledger's discovery provenance.
//
// The model is two-class (see docs/design/SOURCES.md for the curated scope and its
// verified sources):
//
//   - strong terms are specific enough to put a document in scope for ANY issuer
//     (personal-data, cybersecurity, e-transaction, payment, digital-banking
//     phrases) — this is how cross-cutting laws from the National Assembly,
//     Government, or Ministry of Public Security are caught even though they are
//     not issued by the State Bank.
//   - weak terms denote technology but are common across sectors (công nghệ
//     thông tin, hệ thống thông tin, dữ liệu, chuyển đổi số …), so they count
//     only with a banking signal — an NHNN số ký hiệu or "ngân hàng" / "tổ chức
//     tín dụng" in the text — so a health-IT or e-government decree is not pulled.
//
// Field scope follows the term class: strong terms are matched against the số ký
// hiệu, title, and body text (when the feed supplies it), while weak terms are
// matched against the số ký hiệu and title only — common technology words flood
// body text and would over-match (see Match).
//
// The vocabulary is not hardcoded: it is loaded from the config schema into a
// Matcher (see docs/design/SCHEMA.md#config--tunable-policy-seed--operator-overrides).
// The pipeline rebuilds the Matcher per
// Discover run, so operators tune scope by editing config — no redeploy. Matching
// itself is NFC-normalized and lower-cased but NEVER diacritic-folded: folding "an
// toàn" → "an toan" over-matches Vietnamese. Phrases are kept tight on purpose —
// "an toàn thông tin" (in scope) is a term, while bare "an toàn" (also in "tỷ lệ
// an toàn vốn", capital adequacy) is not.
package scope

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Term classes. A document is in scope if it matches a strong term (any issuer,
// title + body), a strong_title term (any issuer, title only), or a weak term
// together with a banking signal.
const (
	ClassStrong = "strong"
	// ClassStrongTitle is a strong term (in scope for any issuer, no banking signal
	// needed) matched on the số ký hiệu + title ONLY, never the body. Used for
	// specific terms whose body occurrences are dominated by boilerplate — "chữ ký
	// số" in e-filing clauses, "chứng thực chữ ký" (notarization), "tài khoản thanh
	// toán" as a generic account reference (see docs/design/SOURCES.md).
	ClassStrongTitle = "strong_title"
	ClassWeak        = "weak"
	ClassSignal      = "signal"
)

// Result is the scope verdict for a document.
type Result struct {
	InScope bool
	Matched []string // normalized terms that put it in scope (provenance)
}

// Term is one scope-vocabulary entry as loaded from config: the phrase plus its
// class (ClassStrong / ClassStrongTitle / ClassWeak / ClassSignal).
type Term struct {
	Text  string
	Class string
}

// Matcher holds the normalized scope vocabulary. Build it with New or Load. It is
// immutable once built and safe for concurrent Match calls.
type Matcher struct {
	strong      []string
	strongTitle []string
	weak        []string
	signals     []string
}

// New builds a Matcher from explicit by-class term slices. Every input is
// normalized (NFC + lower-cased, never diacritic-folded).
func New(strong, strongTitle, weak, signals []string) *Matcher {
	return &Matcher{
		strong:      normalizeAll(strong),
		strongTitle: normalizeAll(strongTitle),
		weak:        normalizeAll(weak),
		signals:     normalizeAll(signals),
	}
}

// Load builds a Matcher from config rows bucketed by Class. Unknown classes are
// ignored. This is how the pipeline builds a Matcher per run from the config
// schema.
func Load(terms []Term) *Matcher {
	var strong, strongTitle, weak, signals []string
	for _, t := range terms {
		switch t.Class {
		case ClassStrong:
			strong = append(strong, t.Text)
		case ClassStrongTitle:
			strongTitle = append(strongTitle, t.Text)
		case ClassWeak:
			weak = append(weak, t.Text)
		case ClassSignal:
			signals = append(signals, t.Text)
		}
	}
	return New(strong, strongTitle, weak, signals)
}

// Match decides whether a document is in scope and returns the terms that
// matched. number and title are always consulted; abstract (vbpl's docAbs — the
// document's body/preamble text from the feed, often empty) is consulted only for
// strong terms (strong_title and weak terms match title only). Strong terms are
// specific phrases that stay selective in body text, so finding one there is a
// real signal — e.g. a terse amendment whose body cites "Luật An ninh mạng". Weak
// terms are common technology words ("công nghệ thông tin", "hệ thống thông tin")
// that appear in nearly every document body, so matching them there would pull in
// hundreds of incidental documents; they are kept to the số ký hiệu + title.
func (m *Matcher) Match(number, title, abstract string) Result {
	num := normalize(number)
	titleHay := num + "\n" + normalize(title)
	fullHay := titleHay
	if abstract != "" {
		fullHay = titleHay + "\n" + normalize(abstract)
	}
	signal := strings.Contains(num, "nhnn") || containsAny(titleHay, m.signals)

	var matched []string
	matched = appendMatches(matched, fullHay, m.strong)       // strong: số ký hiệu + title + abstract
	matched = appendMatches(matched, titleHay, m.strongTitle) // strong_title: số ký hiệu + title only
	if signal {
		matched = appendMatches(matched, titleHay, m.weak) // weak: số ký hiệu + title only
	}
	return Result{InScope: len(matched) > 0, Matched: matched}
}

func appendMatches(dst []string, hay string, terms []string) []string {
	for _, t := range terms {
		if strings.Contains(hay, t) {
			dst = append(dst, t)
		}
	}
	return dst
}

func containsAny(hay string, terms []string) bool {
	for _, t := range terms {
		if strings.Contains(hay, t) {
			return true
		}
	}
	return false
}

// normalize lower-cases and NFC-normalizes without folding diacritics.
func normalize(s string) string {
	return strings.ToLower(norm.NFC.String(s))
}

// normalizeAll normalizes every entry of in into a new slice.
func normalizeAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = normalize(s)
	}
	return out
}
