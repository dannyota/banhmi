package pipeline

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Section is one node in the Vietnamese legal provision tree produced by
// ParseSections. The hierarchy mirrors the statutory structure:
// Phần > Chương > Mục > Điều > Khoản > Điểm.
type Section struct {
	Kind         string    // phan|chuong|muc|dieu|khoan|diem
	Ordinal      int       // sequential position among siblings (1-based)
	NodeKey      string    // source-native stable node id/key, when available
	PType        int16     // source-native provision type code, when available
	Label        string    // human label, e.g. "Điều 7", "Khoản 2", "Điểm a"
	Heading      string    // heading text after the label on the same line, trimmed
	Content      string    // body text beneath the heading (trimmed)
	CitationPath string    // stable path key, e.g. "dieu-7/khoan-2"
	Children     []Section // ordered sub-nodes
}

// ParseSections parses Vietnamese legal Markdown text (as emitted by the
// DOCX/PDF extractors) into a tree of Sections. It is a deterministic
// line-by-line state machine — no AI.
//
// The parser is engine-uniform: it classifies every line on its own merits and
// does NOT depend on the extractor inserting blank lines before headings. This
// matters because clauses and points are written as "1." and "a)" (not the
// words "Khoản"/"Điểm"), so they never get a preceding blank line — a
// block-splitting parser silently drops all of them. Khoản/Điểm are only
// recognised inside an open Điều, so numbered lists in a preamble or a table do
// not masquerade as clauses.
func ParseSections(markdown string) []Section {
	// Normalise no-break spaces to plain spaces so heading patterns match: real
	// DOCX writes e.g. "Chương II" with a no-break space, which \s rejects.
	markdown = strings.ReplaceAll(markdown, "\u00a0", " ") // NBSP
	markdown = strings.ReplaceAll(markdown, "\u202f", " ") // narrow NBSP
	roots := buildTree(markdown)
	if len(roots) == 0 && !supplementOnlyText(markdown) {
		if outline := buildNumberedOutlineTree(markdown); len(outline) > 0 {
			return outline
		}
		return buildWholeDocumentFallback(markdown)
	}
	return roots
}

// ---- patterns ----------------------------------------------------------------
// All anchored at line start. Structural keywords require a numeral after them,
// which keeps prose like "Chương trình", "Mục lục", or "Điều kiện" from matching.

// phanRe matches "Phần <Roman>".
var phanRe = regexp.MustCompile(`^Phần\s+([IVXLC]+)\b`)

// chuongRe matches "Chương <Roman>".
var chuongRe = regexp.MustCompile(`^Chương\s+([IVXLC]+)\b`)

// mucRe matches "Mục <Roman or Arabic>".
var mucRe = regexp.MustCompile(`^Mục\s+([IVXLC0-9]+)\b`)

// dieuRe matches "Điều <N>" with an optional letter suffix for inserted
// articles (e.g. "Điều 21b" added by an amending decree).
var dieuRe = regexp.MustCompile(`^Điều\s+(\d+[a-zđ]?)\b`)

// khoanRe matches a numbered clause opening a line: "1. ", "12. ".
var khoanRe = regexp.MustCompile(`^(\d+)\.\s`)

// numericHeadingRe matches MarkItDown rows where a PDF visually had "Điều N" but
// text extraction only kept "N. <bold heading>". We only promote these when the
// heading itself is bold in the raw Markdown.
var numericHeadingRe = regexp.MustCompile(`^(\d+)\.\s+(.+)$`)

// numberedOutlineRe matches old narrative circulars that use "1.", "1.1.",
// etc. as their only citable structure.
var numberedOutlineRe = regexp.MustCompile(`^(\d+(?:\.\d+)*)(?:[\.)])\s+(.+)$`)

// romanOutlineHeadingRe and alphaOutlineHeadingRe cover older official texts
// that use outline sections ("I.", "I -", "A.") instead of statutory Điều
// headings.
var romanOutlineHeadingRe = regexp.MustCompile(`^([IVXLC]+)\s*[\.\-]\s+(.+)$`)
var alphaOutlineHeadingRe = regexp.MustCompile(`^([A-ZĐ])\s*[\.\-]\s+(.+)$`)

// diemRe matches a lettered point opening a line: "a) ", "a. ", "a/ ", "đ) ".
var diemRe = regexp.MustCompile(`^([a-zđ])[\)\./]\s`)

// romanToInt converts an uppercase Roman numeral string to an integer.
// Supports values sufficient for Vietnamese legal docs.
func romanToInt(s string) int {
	vals := map[byte]int{'I': 1, 'V': 5, 'X': 10, 'L': 50, 'C': 100}
	total := 0
	prev := 0
	for i := len(s) - 1; i >= 0; i-- {
		v := vals[s[i]]
		if v < prev {
			total -= v
		} else {
			total += v
		}
		prev = v
	}
	return total
}

// atoiLeading parses the leading decimal digits of s (e.g. "21b" -> 21).
func atoiLeading(s string) int {
	n := 0
	for i := 0; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}

// diemLetterOrdinal converts a Vietnamese điểm letter to legal ordering:
// a, b, c, d, đ, e, g, h, i, k...
var diemMap = func() map[string]int {
	letters := []string{"a", "b", "c", "d", "đ", "e", "g", "h", "i", "k", "l", "m", "n", "o", "p", "q", "r", "s", "t", "u", "v", "x", "y"}
	m := make(map[string]int, len(letters))
	for i, letter := range letters {
		m[letter] = i + 1
	}
	return m
}()

func diemLetterOrdinal(letter string) int {
	if n, ok := diemMap[letter]; ok {
		return n
	}
	return 0
}

// ---- line classification -----------------------------------------------------

type blockKind int

const (
	bkText blockKind = iota
	bkPhan
	bkChuong
	bkMuc
	bkDieu
	bkKhoan
	bkDiem
)

// levelOf returns the numeric depth of a blockKind for stack pruning.
func levelOf(k blockKind) int {
	switch k {
	case bkPhan:
		return 1
	case bkChuong:
		return 2
	case bkMuc:
		return 3
	case bkDieu:
		return 4
	case bkKhoan:
		return 5
	case bkDiem:
		return 6
	}
	return 0
}

// isStructural reports whether a kind takes a heading/title (and so may have its
// title on the next line). Khoản/Điểm carry their text inline instead.
func isStructural(k blockKind) bool {
	return k == bkPhan || k == bkChuong || k == bkMuc || k == bkDieu
}

type token struct {
	kind          blockKind
	ordinal       int
	letter        string // for điểm
	label         string // full heading label, e.g. "Điều 7"
	heading       string // structural title on the same line (may be "")
	body          string // inline body for khoan/diem; raw text for bkText
	pathSeg       string // citation_path segment, e.g. "dieu-21b", "khoan-2", "diem-a"
	legacyOutline bool   // older outline-only sections like "I."/"A."
	explicitDieu  bool   // true when the article came from an explicit "Điều N" label
}

func afterLabel(line, label string) string {
	rest := strings.TrimSpace(line)
	for _, field := range strings.Fields(label) {
		if !strings.HasPrefix(rest, field) {
			return strings.TrimSpace(strings.TrimPrefix(line, label))
		}
		rest = strings.TrimSpace(rest[len(field):])
	}
	return strings.TrimLeft(strings.TrimSpace(rest), ".: ")
}

func startsWithOpeningQuote(line string) bool {
	line = strings.TrimSpace(line)
	return strings.HasPrefix(line, "“") || strings.HasPrefix(line, "\"")
}

func updateQuotedBlock(inQuote bool, line string) bool {
	delta := strings.Count(line, "“") - strings.Count(line, "”")
	switch {
	case delta > 0:
		return true
	case delta < 0:
		return false
	default:
		return inQuote
	}
}

// stripMDEmphasis removes one layer of wrapping Markdown emphasis from a line so
// "**Điều 1. ...**" is matched as "Điều 1. ...". The MarkItDown path can
// wrap headings in bold; plain text has no markers, so this is a
// no-op there. Only fully-wrapped lines are unwrapped — inline emphasis and table
// rows (which start with '|') are left intact.
func stripMDEmphasis(s string) string {
	s = strings.TrimSpace(s)
	for _, m := range []string{"**", "__", "*", "_"} {
		if len(s) >= 2*len(m) && strings.HasPrefix(s, m) && strings.HasSuffix(s, m) {
			return strings.TrimSpace(s[len(m) : len(s)-len(m)])
		}
	}
	return s
}

// stripMDStructuralMarkers removes emphasis markers that MarkItDown often puts
// around only the structural label: "**Điều 1.** Nội dung". Classification should
// see "Điều 1. Nội dung"; the stored heading should not carry Markdown markers.
func stripMDStructuralMarkers(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "“\"'")
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "#") {
		s = strings.TrimSpace(strings.TrimPrefix(s, "#"))
	}
	for {
		changed := false
		for _, m := range []string{"***", "**", "__", "*", "_"} {
			if strings.HasPrefix(s, m) {
				s = strings.TrimSpace(strings.TrimPrefix(s, m))
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	for _, m := range []string{"***", "**", "__", "*", "_"} {
		s = strings.ReplaceAll(s, m, "")
	}
	return strings.TrimSpace(s)
}

func hasBoldNumericHeading(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if inner := stripMDEmphasis(trimmed); inner != trimmed {
		return numericHeadingRe.MatchString(inner)
	}
	m := numericHeadingRe.FindStringSubmatch(trimmed)
	if m == nil {
		return false
	}
	heading := strings.TrimSpace(m[2])
	return strings.HasPrefix(heading, "**") ||
		strings.HasPrefix(heading, "***") ||
		strings.HasPrefix(heading, "__")
}

func isArticleLikeNumericHeading(clean string) bool {
	m := numericHeadingRe.FindStringSubmatch(clean)
	if m == nil {
		return false
	}
	heading := strings.ToLower(strings.TrimSpace(strings.Trim(m[2], ".:;")))
	for _, prefix := range []string{
		"phạm vi điều chỉnh",
		"đối tượng áp dụng",
		"giải thích từ ngữ",
		"hiệu lực thi hành",
		"điều khoản thi hành",
		"tổ chức thực hiện",
	} {
		if strings.HasPrefix(heading, prefix) {
			return true
		}
	}
	return false
}

func isOutlineHeading(raw, heading string) bool {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "**") || strings.Contains(raw, "__") {
		return true
	}

	var letters, upper int
	for _, r := range heading {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if unicode.IsUpper(r) {
			upper++
		}
	}
	return letters >= 5 && float64(upper)/float64(letters) >= 0.70
}

func alphaOrdinal(letter string) int {
	return diemLetterOrdinal(strings.ToLower(letter))
}

func numericArticleToken(line string) (token, bool) {
	clean := stripMDStructuralMarkers(line)
	m := numericHeadingRe.FindStringSubmatch(clean)
	if m == nil {
		return token{}, false
	}
	return token{kind: bkDieu, heading: strings.TrimSpace(m[2])}, true
}

func clauseToken(clean string) (token, bool) {
	if m := khoanRe.FindStringSubmatch(clean); m != nil {
		return token{kind: bkKhoan, ordinal: atoiLeading(m[1]), label: m[1] + ".", body: strings.TrimSpace(clean[len(m[0]):]), pathSeg: "khoan-" + m[1]}, true
	}
	if m := diemRe.FindStringSubmatch(clean); m != nil {
		letter := m[1]
		return token{kind: bkDiem, ordinal: diemLetterOrdinal(letter), letter: letter, label: letter + ")", body: strings.TrimSpace(clean[len(m[0]):]), pathSeg: "diem-" + letter}, true
	}
	return token{}, false
}

// classifyLine identifies the structural role of a single trimmed line.
// Khoản/Điểm are only recognised when an open Điều/Khoản or a legacy outline
// section is on the stack.
func classifyLine(line string, canStartClause, inExplicitArticle bool) token {
	clean := stripMDStructuralMarkers(line)
	if m := phanRe.FindStringSubmatch(clean); m != nil {
		label := "Phần " + m[1]
		return token{kind: bkPhan, ordinal: romanToInt(m[1]), label: label, heading: afterLabel(clean, label), pathSeg: "phan-" + m[1]}
	}
	if m := chuongRe.FindStringSubmatch(clean); m != nil {
		label := "Chương " + m[1]
		return token{kind: bkChuong, ordinal: romanToInt(m[1]), label: label, heading: afterLabel(clean, label), pathSeg: "chuong-" + m[1]}
	}
	if m := mucRe.FindStringSubmatch(clean); m != nil {
		label := "Mục " + m[1]
		ord := romanToInt(m[1])
		if ord == 0 {
			ord = atoiLeading(m[1])
		}
		return token{kind: bkMuc, ordinal: ord, label: label, heading: afterLabel(clean, label), pathSeg: "muc-" + m[1]}
	}
	if m := dieuRe.FindStringSubmatch(clean); m != nil {
		key := m[1] // "1" or "21b"
		rest := strings.TrimSpace(clean[len(m[0]):])
		if rest != "" && !strings.HasPrefix(rest, ".") && !strings.HasPrefix(rest, ":") {
			return token{kind: bkText, body: line}
		}
		label := "Điều " + key
		h := strings.TrimSpace(strings.TrimPrefix(afterLabel(clean, label), "."))
		return token{kind: bkDieu, ordinal: atoiLeading(key), label: label, heading: h, pathSeg: "dieu-" + key, explicitDieu: true}
	}
	if canStartClause && inExplicitArticle {
		if t, ok := clauseToken(clean); ok {
			return t
		}
	}
	if isArticleLikeNumericHeading(clean) {
		if m := numericHeadingRe.FindStringSubmatch(clean); m != nil {
			return token{kind: bkDieu, heading: strings.TrimSpace(m[2])}
		}
	}
	if hasBoldNumericHeading(line) {
		if m := numericHeadingRe.FindStringSubmatch(clean); m != nil {
			return token{kind: bkDieu, heading: strings.TrimSpace(m[2])}
		}
	}
	if m := romanOutlineHeadingRe.FindStringSubmatch(clean); m != nil && isOutlineHeading(line, m[2]) {
		label := m[1] + "."
		return token{
			kind: bkChuong, ordinal: romanToInt(m[1]), label: label,
			heading: strings.TrimSpace(m[2]), pathSeg: "chuong-" + m[1],
			legacyOutline: true,
		}
	}
	if m := alphaOutlineHeadingRe.FindStringSubmatch(clean); m != nil && isOutlineHeading(line, m[2]) {
		label := m[1] + "."
		return token{
			kind: bkMuc, ordinal: alphaOrdinal(m[1]), label: label,
			heading: strings.TrimSpace(m[2]), pathSeg: "muc-" + m[1],
			legacyOutline: true,
		}
	}
	if canStartClause {
		if t, ok := clauseToken(clean); ok {
			return t
		}
	}
	return token{kind: bkText, body: line}
}

// ---- tree builder ------------------------------------------------------------

// frame holds the construction context for one open node. node points into its
// parent's Children slice; it is only written while on the stack (a node is
// always popped before a sibling is appended to its parent), so the pointer
// stays valid for the writes we make through it.
type frame struct {
	kind          blockKind
	citationPath  string
	legacyOutline bool
	explicitDieu  bool
	node          *Section // nil for the root sentinel
}

func activeLegacyOutline(stack []frame) bool {
	for i := range stack {
		if stack[i].legacyOutline {
			return true
		}
	}
	return false
}

func activeExplicitArticle(stack []frame) bool {
	for i := range stack {
		if stack[i].kind == bkDieu && stack[i].explicitDieu {
			return true
		}
	}
	return false
}

func currentArticle(stack []frame) *Section {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].kind == bkDieu {
			return stack[i].node
		}
	}
	return nil
}

func hasArticleBodyOrChildren(s *Section) bool {
	return s != nil && (strings.TrimSpace(s.Content) != "" || len(s.Children) > 0)
}

func hasArticleContainer(stack []frame) bool {
	for i := range stack {
		if stack[i].kind == bkChuong || stack[i].kind == bkMuc {
			return true
		}
	}
	return false
}

func looksLikeLostArticleHeading(clean string) bool {
	m := numericHeadingRe.FindStringSubmatch(clean)
	if m == nil {
		return false
	}
	heading := strings.TrimSpace(m[2])
	if isArticleLikeNumericHeading(clean) {
		return true
	}
	if len([]rune(heading)) > 120 {
		return false
	}
	if strings.ContainsAny(heading, ":;") || strings.HasSuffix(heading, ".") {
		return false
	}
	return true
}

func shouldPromoteLostArticle(line string, stack []frame) bool {
	clean := stripMDStructuralMarkers(line)
	m := numericHeadingRe.FindStringSubmatch(clean)
	if m == nil || m[1] != "1" || activeLegacyOutline(stack) || startsWithOpeningQuote(line) {
		return false
	}
	if !looksLikeLostArticleHeading(clean) {
		return false
	}

	article := currentArticle(stack)
	if article != nil {
		return hasArticleContainer(stack) && hasArticleBodyOrChildren(article)
	}

	top := stack[len(stack)-1]
	return top.kind == bkChuong || top.kind == bkMuc
}

// buildTree assembles the Section tree by scanning markdown line by line. A
// heading of level L pops all open frames of level >= L, then pushes itself.
// Text lines attach to the current node (or become a structural node's title
// when one is awaiting it). Blank lines are separators and are ignored.
func buildTree(markdown string) []Section {
	roots := &[]Section{}
	stack := []frame{{kind: bkText}} // sentinel
	counters := make(map[blockKind]int)
	pathCounts := make(map[string]int)
	pendingTitle := false // top structural node is awaiting its title line
	inQuotedBlock := false

	childrenOf := func(f frame) *[]Section {
		if f.node == nil {
			return roots
		}
		return &f.node.Children
	}

	for raw := range strings.SplitSeq(markdown, "\n") {
		rawLine := strings.TrimSpace(raw)
		if rawLine == "" {
			continue
		}
		line := stripMDEmphasis(rawLine) // unwrap MarkItDown's "**heading**" before use
		suppressStructure := inQuotedBlock || startsWithOpeningQuote(rawLine)

		canStartClause := false
		for i := range stack {
			if stack[i].kind == bkDieu || stack[i].kind == bkKhoan || stack[i].legacyOutline {
				canStartClause = true
				break
			}
		}

		inExplicitArticle := activeExplicitArticle(stack)
		t := token{kind: bkText, body: line}
		if !suppressStructure {
			var ok bool
			t, ok = numericArticleToken(rawLine)
			if !ok || !shouldPromoteLostArticle(rawLine, stack) {
				t = classifyLine(rawLine, canStartClause, inExplicitArticle)
			}
		}

		if t.kind == bkText {
			top := stack[len(stack)-1]
			if top.node == nil {
				inQuotedBlock = updateQuotedBlock(inQuotedBlock, rawLine)
				continue // stray text above the first heading (preamble) — drop
			}
			if pendingTitle {
				top.node.Heading = line
				pendingTitle = false
			} else if top.node.Content == "" {
				top.node.Content = line
			} else {
				top.node.Content += "\n" + line
			}
			inQuotedBlock = updateQuotedBlock(inQuotedBlock, rawLine)
			continue
		}

		pendingTitle = false

		// Pop frames at the same or deeper level, and reset their counters.
		for len(stack) > 1 && levelOf(stack[len(stack)-1].kind) >= levelOf(t.kind) {
			stack = stack[:len(stack)-1]
		}
		for k := range counters {
			if levelOf(k) > levelOf(t.kind) {
				delete(counters, k)
			}
		}

		parent := stack[len(stack)-1]
		ord := t.ordinal
		if ord == 0 {
			counters[t.kind]++
			ord = counters[t.kind]
		}
		label := t.label
		if label == "" {
			label = defaultLabel(t.kind, ord)
		}
		citPath := t.pathSeg
		if citPath == "" {
			citPath = defaultPathSegment(t.kind, ord)
		}
		if parent.citationPath != "" {
			citPath = parent.citationPath + "/" + citPath
		}
		citPath = uniqueCitationPath(citPath, pathCounts)

		ch := childrenOf(parent)
		*ch = append(*ch, Section{
			Kind:         kindName(t.kind),
			Ordinal:      ord,
			Label:        label,
			Heading:      t.heading,
			Content:      t.body,
			CitationPath: citPath,
		})
		added := &(*ch)[len(*ch)-1]
		stack = append(stack, frame{
			kind: t.kind, citationPath: citPath,
			legacyOutline: t.legacyOutline, explicitDieu: t.explicitDieu,
			node: added,
		})

		if isStructural(t.kind) && t.heading == "" {
			pendingTitle = true
		}
		inQuotedBlock = updateQuotedBlock(inQuotedBlock, rawLine)
	}

	return *roots
}

func buildNumberedOutlineTree(markdown string) []Section {
	if countNumberedOutlineLines(markdown) < 2 {
		return nil
	}

	var roots []Section
	pathCounts := make(map[string]int)
	lastTop := -1
	lastChild := -1

	appendContinuation := func(line string) {
		switch {
		case lastTop >= 0 && lastChild >= 0:
			child := &roots[lastTop].Children[lastChild]
			if child.Content == "" {
				child.Content = line
			} else {
				child.Content += "\n" + line
			}
		case lastTop >= 0:
			root := &roots[lastTop]
			if root.Content == "" {
				root.Content = line
			} else {
				root.Content += "\n" + line
			}
		}
	}

	for raw := range strings.SplitSeq(markdown, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "|") {
			continue
		}
		line = stripMDEmphasis(line)
		clean := stripMDStructuralMarkers(line)
		m := numberedOutlineRe.FindStringSubmatch(clean)
		if m == nil {
			if lastTop >= 0 {
				appendContinuation(line)
			}
			continue
		}

		key := m[1]
		body := strings.TrimSpace(m[2])
		seg := strings.ReplaceAll(key, ".", "-")
		depth := strings.Count(key, ".") + 1
		ord := outlineOrdinal(key)

		if depth == 1 || lastTop < 0 {
			path := uniqueCitationPath("dieu-outline-"+seg, pathCounts)
			roots = append(roots, Section{
				Kind:         "dieu",
				Ordinal:      ord,
				Label:        key + ".",
				Content:      body,
				CitationPath: path,
			})
			lastTop = len(roots) - 1
			lastChild = -1
			continue
		}

		parentPath := roots[lastTop].CitationPath
		path := uniqueCitationPath(parentPath+"/khoan-outline-"+seg, pathCounts)
		roots[lastTop].Children = append(roots[lastTop].Children, Section{
			Kind:         "khoan",
			Ordinal:      ord,
			Label:        key + ".",
			Content:      body,
			CitationPath: path,
		})
		lastChild = len(roots[lastTop].Children) - 1
	}

	return roots
}

func buildWholeDocumentFallback(markdown string) []Section {
	content := wholeDocumentFallbackContent(markdown)
	if !shouldUseWholeDocumentFallback(content) {
		return nil
	}
	return []Section{{
		Kind:         "dieu",
		Ordinal:      1,
		Label:        "Toàn văn",
		Content:      content,
		CitationPath: "toan-van",
	}}
}

func wholeDocumentFallbackContent(markdown string) string {
	lines := make([]string, 0)
	for raw := range strings.SplitSeq(markdown, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || isMarkdownTableSeparator(line) {
			continue
		}
		line = stripMDEmphasis(line)
		for strings.HasPrefix(line, "#") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func isMarkdownTableSeparator(line string) bool {
	line = strings.TrimSpace(line)
	if !strings.Contains(line, "-") {
		return false
	}
	for _, r := range line {
		switch r {
		case '|', '-', ':', ' ':
			continue
		default:
			return false
		}
	}
	return true
}

func shouldUseWholeDocumentFallback(content string) bool {
	if len([]rune(content)) < 300 {
		return false
	}
	lines := 0
	for raw := range strings.SplitSeq(content, "\n") {
		if strings.TrimSpace(raw) != "" {
			lines++
		}
	}
	return lines >= 4
}

func countNumberedOutlineLines(markdown string) int {
	count := 0
	for raw := range strings.SplitSeq(markdown, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "|") {
			continue
		}
		clean := stripMDStructuralMarkers(stripMDEmphasis(line))
		if numberedOutlineRe.MatchString(clean) {
			count++
		}
	}
	return count
}

func outlineOrdinal(key string) int {
	parts := strings.Split(key, ".")
	if len(parts) == 0 {
		return 0
	}
	return atoiLeading(parts[len(parts)-1])
}

func defaultLabel(kind blockKind, ord int) string {
	switch kind {
	case bkDieu:
		return "Điều " + strconv.Itoa(ord)
	case bkKhoan:
		return strconv.Itoa(ord) + "."
	case bkDiem:
		return "Điểm " + strconv.Itoa(ord)
	case bkMuc:
		return "Mục " + strconv.Itoa(ord)
	case bkChuong:
		return "Chương " + strconv.Itoa(ord)
	case bkPhan:
		return "Phần " + strconv.Itoa(ord)
	default:
		return strconv.Itoa(ord)
	}
}

func defaultPathSegment(kind blockKind, ord int) string {
	return kindName(kind) + "-" + strconv.Itoa(ord)
}

func uniqueCitationPath(path string, counts map[string]int) string {
	counts[path]++
	if counts[path] == 1 {
		return path
	}
	return path + "~" + strconv.Itoa(counts[path])
}

// kindName maps a blockKind to the DB kind string.
func kindName(k blockKind) string {
	switch k {
	case bkPhan:
		return "phan"
	case bkChuong:
		return "chuong"
	case bkMuc:
		return "muc"
	case bkDieu:
		return "dieu"
	case bkKhoan:
		return "khoan"
	case bkDiem:
		return "diem"
	}
	return "dieu"
}
