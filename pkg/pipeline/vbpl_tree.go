package pipeline

import (
	"context"
	"encoding/json"
	"html"
	"regexp"
	"strconv"
	"strings"
)

type vbplProvisionNode struct {
	ID         string               `json:"id"`
	Key        string               `json:"key"`
	Title      string               `json:"title"`
	PType      int16                `json:"ptype"`
	Level      string               `json:"level"`
	OrderIndex int                  `json:"orderIndex"`
	Content    vbplProvisionContent `json:"content"`
	Children   []vbplProvisionNode  `json:"children"`
}

type vbplProvisionContent struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

func (a *Activities) selectProvisionTreeSections(ctx context.Context, sourceDocID int64) ([]Section, sectionStats, []string, bool, error) {
	payloads, err := a.bronze.ListRawPayloadsByDocument(ctx, sourceDocID)
	if err != nil {
		return nil, sectionStats{}, nil, false, err
	}
	for _, payload := range payloads {
		if payload.Kind != "provision_tree_json" || payload.Content == nil || strings.TrimSpace(*payload.Content) == "" {
			continue
		}
		roots, stats, warnings, ok := parseVBPLProvisionTreePayload(*payload.Content)
		return roots, stats, warnings, ok, nil
	}
	return nil, sectionStats{}, nil, false, nil
}

func parseVBPLProvisionTreePayload(payload string) ([]Section, sectionStats, []string, bool) {
	nodes, ok := decodeVBPLProvisionTree(payload)
	if !ok {
		return nil, sectionStats{}, []string{"invalid_vbpl_provision_tree"}, false
	}
	if len(nodes) == 0 {
		return nil, sectionStats{}, []string{"empty_vbpl_provision_tree"}, false
	}

	counts := make(map[string]int)
	roots := buildVBPLTreeSections(nodes, "", counts)
	stats := sectionStatsFor(roots)
	warnings := validateSectionTree(roots, stats)
	return roots, stats, warnings, stats.Total > 0 && stats.Content > 0
}

func decodeVBPLProvisionTree(payload string) ([]vbplProvisionNode, bool) {
	var nodes []vbplProvisionNode
	if err := json.Unmarshal([]byte(payload), &nodes); err == nil {
		return nodes, true
	}

	var envelope struct {
		Data []vbplProvisionNode `json:"data"`
	}
	if err := json.Unmarshal([]byte(payload), &envelope); err == nil {
		return envelope.Data, true
	}
	return nil, false
}

func buildVBPLTreeSections(nodes []vbplProvisionNode, parentPath string, counts map[string]int) []Section {
	out := make([]Section, 0, len(nodes))
	for i := range nodes {
		node := nodes[i]
		kind := vbplKind(node.Level, node.PType)
		if kind == "" {
			children := buildVBPLTreeSections(node.Children, parentPath, counts)
			out = append(out, children...)
			continue
		}

		label, heading, segment, ordinal, labelVariants := vbplTitleParts(kind, node, i+1)
		path := segment
		if parentPath != "" {
			path = parentPath + "/" + segment
		}
		path = uniqueCitationPath(path, counts)
		section := Section{
			Kind:         kind,
			Ordinal:      ordinal,
			NodeKey:      firstNonEmpty(node.Key, node.ID),
			PType:        node.PType,
			Label:        label,
			Heading:      heading,
			Content:      vbplOwnContent(node, labelVariants),
			CitationPath: path,
		}
		section.Children = buildVBPLTreeSections(node.Children, path, counts)
		out = append(out, section)
	}
	return out
}

func vbplKind(level string, ptype int16) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "part":
		return "phan"
	case "chapter":
		return "chuong"
	case "section":
		return "muc"
	case "article":
		return "dieu"
	case "clause":
		return "khoan"
	case "point":
		return "diem"
	}
	switch ptype {
	case 1:
		return "phan"
	case 2:
		return "chuong"
	case 3, 4:
		return "muc"
	case 5:
		return "dieu"
	case 6:
		return "khoan"
	case 7:
		return "diem"
	default:
		return ""
	}
}

func vbplTitleParts(kind string, node vbplProvisionNode, siblingOrdinal int) (label, heading, segment string, ordinal int, variants []string) {
	title := normalizeTreeText(firstNonEmpty(node.Title, node.Content.Title))
	ordinal = siblingOrdinal

	switch kind {
	case "phan":
		label, heading, segment, ordinal = parseVBPLRomanTitle(title, "Phần", "phan", ordinal)
	case "chuong":
		label, heading, segment, ordinal = parseVBPLRomanTitle(title, "Chương", "chuong", ordinal)
	case "muc":
		label, heading, segment, ordinal = parseVBPLRomanTitle(title, "Mục", "muc", ordinal)
	case "dieu":
		label, heading, segment, ordinal = parseVBPLArticleTitle(title, ordinal)
	case "khoan":
		label, heading, segment, ordinal = parseVBPLClauseTitle(title, ordinal)
	case "diem":
		label, heading, segment, ordinal = parseVBPLPointTitle(title, ordinal)
	}

	if label == "" {
		label = defaultVBPLLabel(kind, ordinal)
	}
	if segment == "" {
		segment = kind + "-" + strconv.Itoa(ordinal)
	}
	variants = append(variants, title, label)
	switch kind {
	case "khoan":
		variants = append(variants, strings.TrimSuffix(label, "."), label)
	case "diem":
		variants = append(variants, "Điểm "+strings.TrimSuffix(label, ")"), label)
	case "dieu", "phan", "chuong", "muc":
		variants = append(variants, label+".")
	}
	return label, strings.Trim(heading, " .\t\r\n"), segment, ordinal, variants
}

var (
	vbplArticleTitleRe = regexp.MustCompile(`^Điều\s+(\d+[A-Za-zĐđ]?)(?:[.\s]+(.*))?$`)
	vbplClauseTitleRe  = regexp.MustCompile(`^(?:Khoản\s+)?(\d+)(?:[.\s]+(.*))?$`)
	vbplPointTitleRe   = regexp.MustCompile(`(?i)^(?:Điểm\s+)?([a-zđ])(?:[\)\.\s]+(.*))?$`)
)

func parseVBPLRomanTitle(title, prefix, pathPrefix string, fallbackOrdinal int) (label, heading, segment string, ordinal int) {
	ordinal = fallbackOrdinal
	fields := strings.Fields(title)
	if len(fields) >= 2 && strings.EqualFold(fields[0], prefix) {
		raw := strings.Trim(fields[1], ".:")
		label = prefix + " " + raw
		heading = strings.TrimSpace(strings.TrimPrefix(title, label))
		heading = strings.TrimLeft(heading, ".: ")
		if n := romanOrArabicOrdinal(raw); n > 0 {
			ordinal = n
			segment = pathPrefix + "-" + strconv.Itoa(n)
		}
	}
	return label, heading, segment, ordinal
}

func parseVBPLArticleTitle(title string, fallbackOrdinal int) (label, heading, segment string, ordinal int) {
	ordinal = fallbackOrdinal
	if m := vbplArticleTitleRe.FindStringSubmatch(title); m != nil {
		raw := m[1]
		label = "Điều " + raw
		heading = strings.TrimSpace(m[2])
		if n := atoiLeading(raw); n > 0 {
			ordinal = n
		}
		segment = "dieu-" + strings.ToLower(raw)
	}
	return label, heading, segment, ordinal
}

func parseVBPLClauseTitle(title string, fallbackOrdinal int) (label, heading, segment string, ordinal int) {
	ordinal = fallbackOrdinal
	if m := vbplClauseTitleRe.FindStringSubmatch(title); m != nil {
		raw := m[1]
		label = raw + "."
		heading = strings.TrimSpace(m[2])
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			ordinal = n
		}
		segment = "khoan-" + raw
	}
	return label, heading, segment, ordinal
}

func parseVBPLPointTitle(title string, fallbackOrdinal int) (label, heading, segment string, ordinal int) {
	ordinal = fallbackOrdinal
	if m := vbplPointTitleRe.FindStringSubmatch(title); m != nil {
		raw := strings.ToLower(m[1])
		label = raw + ")"
		heading = strings.TrimSpace(m[2])
		if n := diemLetterOrdinal(raw); n > 0 {
			ordinal = n
		}
		segment = "diem-" + raw
	}
	return label, heading, segment, ordinal
}

func romanOrArabicOrdinal(raw string) int {
	raw = strings.ToUpper(strings.TrimSpace(raw))
	if raw == "" {
		return 0
	}
	if n, err := strconv.Atoi(raw); err == nil {
		return n
	}
	return romanToInt(raw)
}

func defaultVBPLLabel(kind string, ordinal int) string {
	switch kind {
	case "phan":
		return "Phần " + strconv.Itoa(ordinal)
	case "chuong":
		return "Chương " + strconv.Itoa(ordinal)
	case "muc":
		return "Mục " + strconv.Itoa(ordinal)
	case "dieu":
		return "Điều " + strconv.Itoa(ordinal)
	case "khoan":
		return strconv.Itoa(ordinal) + "."
	case "diem":
		return "Điểm " + strconv.Itoa(ordinal)
	default:
		return strconv.Itoa(ordinal)
	}
}

func vbplOwnContent(node vbplProvisionNode, labelVariants []string) string {
	text := normalizeTreeText(htmlToTreeText(node.Content.Content))
	for _, child := range node.Children {
		childText := normalizeTreeText(htmlToTreeText(child.Content.Content))
		if childText == "" {
			continue
		}
		text = normalizeTreeText(strings.Replace(text, childText, "", 1))
	}
	return stripTreePrefixes(text, labelVariants)
}

var (
	vbplBreakRe = regexp.MustCompile(`(?i)<\s*(br|/p|/div|/li)\s*/?>`)
	vbplTagRe   = regexp.MustCompile(`<[^>]+>`)
)

func htmlToTreeText(s string) string {
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = strings.ReplaceAll(s, "\u202f", " ")
	s = vbplBreakRe.ReplaceAllString(s, "\n")
	s = vbplTagRe.ReplaceAllString(s, " ")
	return html.UnescapeString(s)
}

func stripTreePrefixes(text string, variants []string) string {
	text = normalizeTreeText(text)
	for _, variant := range variants {
		v := normalizeTreeText(htmlToTreeText(variant))
		if v == "" || !strings.HasPrefix(text, v) {
			continue
		}
		text = strings.TrimSpace(strings.TrimPrefix(text, v))
		text = strings.TrimLeft(text, ".:) ")
		return normalizeTreeText(text)
	}
	return normalizeTreeText(text)
}

func normalizeTreeText(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v := strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
