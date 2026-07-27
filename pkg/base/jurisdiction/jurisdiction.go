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

// DocRefCanonicalizer keys.
const (
	// RefCanonDefault treats the reference key as a bare document number.
	RefCanonDefault = "default"
	// RefCanonIDForms additionally lifts an Indonesian sector-coded number out of
	// a verbose reference and folds NOMOR/NO./TAHUN fillers.
	RefCanonIDForms = "id-forms"
)

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
	// ParserSGAct walks Singapore Act/Notice text into the Part/Section/Subsection tree.
	// Near-reuses the MY parser (same citation family).
	ParserSGAct = "sg-act"
	// ParserTHAct walks Thai Act text into the มาตรา/หมวด/ส่วน tree (OCS structured JSON)
	// and BOT ข้อ (clause) notifications.
	ParserTHAct = "th-act"
	// ParserKHAct walks Cambodian Act/Prakas text into the Article/Clause tree.
	ParserKHAct = "kh-act"
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
	// HNSWCandidateMultiplier sizes the ANN candidate pool for this jurisdiction's
	// vector arm. It is per-jurisdiction because the right value depends on the
	// corpus: VN is pinned to -1 (exact scan, no ANN) because one golden case ranks
	// >1200 deep before the current-law filter, so any candidate pool misses it;
	// MY and ID are fine on HNSW. Zero means "use the configured default".
	//
	// This MUST stay per-jurisdiction: cmd/server runs every country in one
	// process, so a single BANHMI_HNSW_CANDIDATE_MULTIPLIER env var cannot express
	// three different values — it would give VN the ANN path that loses the case,
	// or force MY/ID onto a slow exact scan.
	HNSWCandidateMultiplier int
	// MojibakeMarkers are the characters that appear when THIS language's text is
	// misdecoded (e.g. Vietnamese UTF-8 read as Latin-1: "Điều" → "√ê√¨·ª"). The
	// set is language-specific, so it belongs to the jurisdiction — the same
	// glyphs mean something else elsewhere: "√" is a checkmark in Indonesian
	// tables, and treating it as corruption discarded 19 Bank Indonesia
	// regulations (PADG payment-system rules) before this was per-jurisdiction.
	// Empty disables the check — correct for near-ASCII languages, where
	// misdecoding shows up as U+FFFD / private-use glyphs that the
	// language-neutral gate (extract.Assess) already catches.
	MojibakeMarkers string
	// ParagraphLabel is the citation label for mechanically split long leaves
	// ("Đoạn", "Paragraph").
	ParagraphLabel string
	// EffectiveDateLabel is the native-language prefix for the effective-date
	// line in chunk context prefixes ("Có hiệu lực", "Effective", "Berlaku").
	EffectiveDateLabel string

	// ArticleCitationPrefix is the lower-case citation prefix for article-level
	// provisions ("điều ", "section ", "pasal "). Used by the retriever for
	// rollup dedup and full-article assembly.
	ArticleCitationPrefix string
	// SubArticleCitationPrefix is the lower-case citation prefix for
	// sub-article provisions ("khoản ", "subsection ", "ayat ").
	SubArticleCitationPrefix string

	// StructureParser keys the normalize-time provision-tree parser (Parser*
	// constants), resolved in pkg/pipeline.
	StructureParser string
	// UnknownValidityInForce defaults an unknown validity status to in_force.
	// For corpora whose sources may emit no structured status code: the
	// normalize path's "preserve richer status" guard ensures a status-less
	// observation never overwrites a known status from another source.
	UnknownValidityInForce bool

	// TextNormalizer keys the text normalization strategy for the BM25 tokenizer
	// (pkg/rag/lexical): "vn-fold" (default) applies NFD decomposition, strips
	// combining marks (diacritics), and folds đ→d — suitable for VN, MY, ID, and
	// any Latin-script language where diacritics are additive decoration.
	//
	// Thai (future): Thai combining marks are integral to the script; stripping
	// them destroys meaning. A future "th" normalizer must lower-case and split
	// on non-letter/digit boundaries WITHOUT NFD decomposition. This field is the
	// seam for that — wire a new normalizer key to its function in
	// lexical.NormalizerFor, add the key here, and both write (cmd/lexindex) and
	// read (query tokenize + DiacriticFree routing) paths resolve it.
	TextNormalizer string

	// RelationBackfillSources lists the sources whose promoted structured
	// relations may enqueue their target for fetching. Backfill creates an
	// ingest.fetch_doc keyed by (source, external_id), so a source qualifies only
	// if its relation payload carries the target's source id. Measured 2026-07-27:
	// vbpl supplies one for 3,493 of 7,158 references; every other source in every
	// jurisdiction supplies none, so listing them would enqueue nothing. Empty
	// means this jurisdiction backfills no relation targets.
	RelationBackfillSources []string

	// DocRefCanonicalizer keys how a doc_ref key is folded into candidate document
	// numbers when re-resolving relation targets (RefCanon* constants, resolved in
	// pkg/pipeline). Vietnamese reference keys are already bare numbers; Indonesian
	// ones arrive as whole source sentences ("PERATURAN OTORITAS JASA KEUANGAN
	// NOMOR 31/POJK.07/2020 TENTANG ...") and need the number lifted out and the
	// NOMOR/TAHUN fillers folded away.
	DocRefCanonicalizer string

	// LexicalRouterBoost routes diacritic-free or document-reference queries to
	// the BM25 lexical arm. Those signals are Vietnamese-shaped (English is
	// always diacritic-free), so only VN sets it.
	LexicalRouterBoost bool

	// IdentifierScopedRetrieval scopes search to the document(s) a query names
	// explicitly (số ký hiệu), so an amending document that cites the target's
	// number verbatim cannot outrank the target itself. Validated on VN only;
	// other jurisdictions keep the unscoped path until proven on their corpora.
	IdentifierScopedRetrieval bool

	// ScopeSeedFile is the scope-vocabulary CSV under deploy/seed/.
	ScopeSeedFile string
	// GoldenFile is the retrieval-eval golden set, repo-relative.
	GoldenFile string

	// EvalArticleKeyword is the lower-case citation keyword the eval harness
	// uses to match golden article expectations against chunk citations
	// (e.g. "điều", "section", "pasal"). Must be non-empty.
	EvalArticleKeyword string
	// EvalClauseKeyword is the lower-case citation keyword for clause-level
	// matching (e.g. "khoản", "ayat"). Empty means the jurisdiction cites
	// clauses as bare parenthesized tokens like "(6)".
	EvalClauseKeyword string
	// EvalPointKeyword is the lower-case citation keyword for point-level
	// matching (e.g. "điểm", "huruf"). Empty means the jurisdiction cites
	// points as bare parenthesized tokens like "(b)".
	EvalPointKeyword string
}

// registry lists every jurisdiction in launch order, VN (the compiled
// fallback) first. Append-only; never remove a live country.
var registry = []Descriptor{
	{
		Code:                      "vn",
		DBName:                    "banhmi",
		DiacriticDensityGate:      true,
		HNSWCandidateMultiplier:   -1,           // exact scan: a golden case ranks >1200 deep, ANN misses it
		MojibakeMarkers:           "√∆·ªƒ∫≠‚ÄØ", // Vietnamese UTF-8 misdecoded as Latin-1/MacRoman
		TextNormalizer:            "vn-fold",
		ParagraphLabel:            "Đoạn",
		EffectiveDateLabel:        "Có hiệu lực",
		ArticleCitationPrefix:     "điều ",
		SubArticleCitationPrefix:  "khoản ",
		StructureParser:           ParserVNMarkdown,
		RelationBackfillSources:   []string{"vbpl"},
		UnknownValidityInForce:    true, // vanban/sbv_hanoi emit no status; vbpl-known statuses still win via the preserve-richer-status guard
		LexicalRouterBoost:        true,
		IdentifierScopedRetrieval: true,
		ScopeSeedFile:             "scope_term.csv",
		GoldenFile:                "deploy/eval/golden.json",
		EvalArticleKeyword:        "điều",
		EvalClauseKeyword:         "khoản",
		EvalPointKeyword:          "điểm",
	},
	{
		Code:                     "my",
		DBName:                   "laksa",
		OCRLanguages:             "en",
		TextNormalizer:           "vn-fold",
		ParagraphLabel:           "Paragraph",
		EffectiveDateLabel:       "Effective",
		ArticleCitationPrefix:    "section ",
		SubArticleCitationPrefix: "subsection ",
		StructureParser:          ParserMYAct,
		UnknownValidityInForce:   true,
		ScopeSeedFile:            "scope_term_my.csv",
		GoldenFile:               "deploy/eval/golden_my.json",
		EvalArticleKeyword:       "section",
		EvalClauseKeyword:        "",
		EvalPointKeyword:         "",
	},
	{
		Code:                     "id",
		DBName:                   "rendang",
		OCRLanguages:             "id",
		TextNormalizer:           "vn-fold",
		ParagraphLabel:           "Alinea",
		EffectiveDateLabel:       "Berlaku",
		ArticleCitationPrefix:    "pasal ",
		SubArticleCitationPrefix: "ayat ",
		StructureParser:          ParserIDUU,
		RelationBackfillSources:  []string{"bpk"},
		UnknownValidityInForce:   true,
		DocRefCanonicalizer:      RefCanonIDForms,
		ScopeSeedFile:            "scope_term_id.csv",
		GoldenFile:               "deploy/eval/golden_id.json",
		EvalArticleKeyword:       "pasal",
		EvalClauseKeyword:        "ayat",
		EvalPointKeyword:         "huruf",
	},
	{
		Code:                     "sg",
		DBName:                   "kaya",
		OCRLanguages:             "en",
		TextNormalizer:           "vn-fold",
		ParagraphLabel:           "Paragraph",
		EffectiveDateLabel:       "Effective",
		ArticleCitationPrefix:    "section ",
		SubArticleCitationPrefix: "subsection ",
		StructureParser:          ParserSGAct,
		UnknownValidityInForce:   true,
		ScopeSeedFile:            "scope_term_sg.csv",
		GoldenFile:               "deploy/eval/golden_sg.json",
		EvalArticleKeyword:       "section",
		EvalClauseKeyword:        "",
		EvalPointKeyword:         "",
	},
	{
		Code:                     "th",
		DBName:                   "tomyum",
		OCRLanguages:             "th",
		TextNormalizer:           "th",
		ParagraphLabel:           "วรรค",
		EffectiveDateLabel:       "มีผลบังคับใช้",
		ArticleCitationPrefix:    "มาตรา ",
		SubArticleCitationPrefix: "วรรค ",
		StructureParser:          ParserTHAct,
		UnknownValidityInForce:   true,
		ScopeSeedFile:            "scope_term_th.csv",
		GoldenFile:               "deploy/eval/golden_th.json",
		EvalArticleKeyword:       "มาตรา",
		EvalClauseKeyword:        "วรรค",
		EvalPointKeyword:         "ข้อ",
	},
	{
		Code:                     "kh",
		DBName:                   "amok",
		OCRLanguages:             "en",
		TextNormalizer:           "vn-fold",
		ParagraphLabel:           "Paragraph",
		EffectiveDateLabel:       "Effective",
		ArticleCitationPrefix:    "article ",
		SubArticleCitationPrefix: "clause ",
		StructureParser:          ParserKHAct,
		UnknownValidityInForce:   true,
		ScopeSeedFile:            "scope_term_kh.csv",
		GoldenFile:               "deploy/eval/golden_kh.json",
		EvalArticleKeyword:       "article",
		EvalClauseKeyword:        "",
		EvalPointKeyword:         "",
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
