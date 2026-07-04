// Package jurisdiction is the single registry of per-country descriptors — the
// multi-jurisdiction seam. Every per-country switch in the shared code resolves
// through a Descriptor field instead of comparing jurisdiction code strings, so
// adding a country means adding one registry entry (plus its irreducible new
// code: source packages in pkg/app, a structure parser if none is reusable, and
// an MCP brief in pkg/mcp — each guarded by a registry-coverage test).
//
// Vietnam is the compiled fallback: an absent or unknown code must never change
// what a deployment advertises (see docs/design/jurisdictions/PLAYBOOK.md).
package jurisdiction

import "strings"

// DefaultCode is the compiled-fallback jurisdiction (Vietnam).
const DefaultCode = "vn"

// Structure-parser keys, resolved to parser implementations by pkg/pipeline.
// Countries may share a parser (the key names the parser, not the country).
const (
	// ParserVNMarkdown walks Markdown into the Vietnamese Điều/Khoản/Điểm tree.
	ParserVNMarkdown = "vn-markdown"
	// ParserMYAct walks born-digital Act/policy PDF text into the Malaysian
	// Part/Section/Subsection tree.
	ParserMYAct = "my-act"
	// ParserIDUU walks Indonesian regulation text into the
	// BAB/Bagian/Paragraf/Pasal/Ayat/Huruf tree.
	ParserIDUU = "id-uu"
)

// Descriptor is one country's configuration record. Fields are data only;
// behavior stays in the owning packages, selected by key (StructureParser) or
// by Code where the per-country code is irreducible (sources, MCP brief).
type Descriptor struct {
	// Code is the ISO 3166-1 alpha-2 jurisdiction code, lower case ("vn", "my").
	Code string
	// DBName is the default database name — one database per country. An
	// explicit YAML/env database name always wins; this guards a non-VN worker
	// from writing into the VN database by omission.
	DBName string

	// OCRLanguages is the EasyOCR language list for the country's binding legal
	// language ("en"). Empty defers to the configured value — VN, the compiled
	// fallback, keeps its default ("vi") in config.Default.
	OCRLanguages string
	// DiacriticDensityGate applies the Vietnamese diacritic-density content
	// gate. Off for languages with ~zero diacritics (the language-neutral gate
	// checks still apply).
	DiacriticDensityGate bool
	// ParagraphLabel is the citation label for mechanically split long leaves
	// ("Đoạn", "Paragraph").
	ParagraphLabel string

	// StructureParser keys the normalize-time provision-tree parser (Parser*
	// constants), resolved in pkg/pipeline.
	StructureParser string
	// UnknownValidityInForce defaults an unknown validity status to in_force.
	// Only for curated corpora whose sources emit no structured status code
	// (MY); VN keeps the strict "unknown means unknown" rule.
	UnknownValidityInForce bool

	// LexicalRouterBoost routes diacritic-free or document-reference queries to
	// the BM25 lexical arm. Those signals are Vietnamese-shaped (English is
	// always diacritic-free), so only VN sets it.
	LexicalRouterBoost bool

	// ScopeSeedFile is the scope-vocabulary CSV under deploy/seed/.
	ScopeSeedFile string
	// GoldenFile is the retrieval-eval golden set, repo-relative.
	GoldenFile string
}

// registry lists every jurisdiction in launch order, VN (the compiled
// fallback) first. Append-only; never remove a live country.
var registry = []Descriptor{
	{
		Code:                 "vn",
		DBName:               "banhmi",
		DiacriticDensityGate: true,
		ParagraphLabel:       "Đoạn",
		StructureParser:      ParserVNMarkdown,
		LexicalRouterBoost:   true,
		ScopeSeedFile:        "scope_term.csv",
		GoldenFile:           "deploy/eval/golden.json",
	},
	{
		Code:                   "my",
		DBName:                 "laksa",
		OCRLanguages:           "en",
		ParagraphLabel:         "Paragraph",
		StructureParser:        ParserMYAct,
		UnknownValidityInForce: true,
		ScopeSeedFile:          "scope_term_my.csv",
		GoldenFile:             "deploy/eval/golden_my.json",
	},
	{
		Code:                   "id",
		DBName:                 "rendang",
		OCRLanguages:           "id",
		ParagraphLabel:         "Alinea",
		StructureParser:        ParserIDUU,
		UnknownValidityInForce: true,
		ScopeSeedFile:          "scope_term_id.csv",
		GoldenFile:             "deploy/eval/golden_id.json",
	},
}

// Lookup resolves a jurisdiction code to its descriptor. The code is trimmed
// and lower-cased; empty resolves to the VN fallback. Unknown codes return
// false so startup wiring can fail fast instead of serving the wrong country.
func Lookup(code string) (Descriptor, bool) {
	c := strings.ToLower(strings.TrimSpace(code))
	if c == "" {
		c = DefaultCode
	}
	for _, d := range registry {
		if d.Code == c {
			return d, true
		}
	}
	return Descriptor{}, false
}

// For resolves a jurisdiction code, falling back to VN for unknown codes. Use
// it at consumption sites that must never fail (the fallback invariant);
// startup wiring should use Lookup and reject unknown codes.
func For(code string) Descriptor {
	if d, ok := Lookup(code); ok {
		return d
	}
	d, _ := Lookup(DefaultCode)
	return d
}

// All returns every registered jurisdiction in launch order, VN first. Callers
// that fan out per country (seeding, coverage tests) iterate this.
func All() []Descriptor {
	out := make([]Descriptor, len(registry))
	copy(out, registry)
	return out
}
