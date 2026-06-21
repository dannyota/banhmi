package pipeline

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"danny.vn/banhmi/pkg/base/config"
	basedb "danny.vn/banhmi/pkg/base/db"
	"danny.vn/banhmi/pkg/extract"
)

type normalizeValidationDoc struct {
	DocumentID    int64
	DocNumber     string
	Source        string
	DocType       string
	Authority     string
	TextSource    string
	ExtractEngine string
	NeedsReview   bool
	Markdown      string
}

type normalizeValidationGroup struct {
	Docs     int
	Articles int
	Clauses  int
	Points   int
}

// TestNormalizeDBCorpusValidation is an opt-in integration test over the real
// local corpus. It does not mutate the DB; it dry-runs Normalize parsing over
// binding document_text rows and fails on structural signals that would make
// downstream citations weak or empty.
func TestNormalizeDBCorpusValidation(t *testing.T) {
	if os.Getenv("BANHMI_VALIDATE_DB") != "1" {
		t.Skip("set BANHMI_VALIDATE_DB=1 to validate Normalize against the local DB corpus")
	}

	pool := normalizeValidationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	docs := loadNormalizeValidationDocs(t, ctx, pool, normalizeValidationLimit())
	if len(docs) == 0 {
		t.Fatal("no binding document_text rows found; run Extract before Normalize validation")
	}

	bySource := make(map[string]bool)
	byType := make(map[string]bool)
	byAuthority := make(map[string]bool)
	groups := make(map[string]normalizeValidationGroup)
	var issues []string

	for _, doc := range docs {
		source := fallback(doc.Source, "unknown")
		docType := fallback(doc.DocType, "unknown")
		authority := fallback(doc.Authority, "unknown")
		bySource[source] = true
		byType[docType] = true
		byAuthority[authority] = true

		ref := normalizeDocRef(doc)
		if skipReason := bindingTextQualitySkipReason(extract.DefaultGate(), doc.Markdown); skipReason != "" {
			issues = append(issues, fmt.Sprintf("%s binding text would be skipped: %s", ref, skipReason))
			continue
		}

		roots, stats, warnings := parseNormalizeSections("vn", doc.Markdown)
		key := source + "|" + docType + "|" + authority
		group := groups[key]
		group.Docs++
		group.Articles += stats.Dieu
		group.Clauses += stats.Khoan
		group.Points += stats.Diem
		groups[key] = group

		if stats.Total == 0 {
			issues = append(issues, fmt.Sprintf("%s parsed zero sections", ref))
		}
		if stats.Total > 0 && stats.Dieu == 0 && expectsArticleStructure(doc.Markdown) && !hasLegacyChunkableSections(roots) {
			issues = append(issues, fmt.Sprintf("%s parsed zero Điều", ref))
		}
		for _, warning := range warnings {
			if strings.HasPrefix(warning, "missing_citation_path") ||
				strings.HasPrefix(warning, "duplicate_citation_path:") {
				issues = append(issues, fmt.Sprintf("%s %s", ref, warning))
			}
		}
		if stats.Dieu >= 5 && stats.Khoan == 0 && hasUnquotedExplicitArticleLine(doc.Markdown) &&
			hasUnquotedNumberedClauseLine(doc.Markdown) && !hasNumberedOutlineFallback(roots) {
			issues = append(issues, fmt.Sprintf("%s has %d Điều and line-start numbered clauses but parsed zero Khoản", ref, stats.Dieu))
		}
		if stats.Khoan >= 3 && stats.Diem == 0 && hasUnquotedPointLine(doc.Markdown) {
			issues = append(issues, fmt.Sprintf("%s has %d Khoản and line-start lettered points but parsed zero Điểm", ref, stats.Khoan))
		}
	}

	if len(bySource) < 2 {
		issues = append(issues, fmt.Sprintf("coverage too narrow: got %d source(s), want at least 2", len(bySource)))
	}
	if len(byType) < 3 {
		issues = append(issues, fmt.Sprintf("coverage too narrow: got %d document type(s), want at least 3", len(byType)))
	}
	if len(byAuthority) < 2 {
		issues = append(issues, fmt.Sprintf("coverage too narrow: got %d text authorit(ies), want at least 2", len(byAuthority)))
	}

	logNormalizeValidationGroups(t, groups)
	if len(issues) > 0 {
		for i, issue := range issues {
			if i == 30 {
				t.Logf("... %d more issue(s)", len(issues)-i)
				break
			}
			t.Log(issue)
		}
		t.Fatalf("Normalize DB validation found %d issue(s) across %d docs", len(issues), len(docs))
	}
}

func normalizeValidationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("BANHMI_DATABASE_PASSWORD") == "" {
		t.Skip("BANHMI_DATABASE_PASSWORD not set; skipping DB corpus validation")
	}

	cfg, err := loadNormalizeValidationConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := basedb.NewPool(ctx, cfg.Database)
	if err != nil {
		t.Skipf("cannot connect to local DB, skipping DB corpus validation: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func loadNormalizeValidationConfig() (*config.Config, error) {
	for _, path := range []string{"config/config.yaml", "../../config/config.yaml"} {
		if _, err := os.Stat(path); err == nil {
			return config.Load(path)
		}
	}
	return config.Load("config/config.yaml")
}

func normalizeValidationLimit() int {
	raw := strings.TrimSpace(os.Getenv("BANHMI_VALIDATE_DB_LIMIT"))
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func hasUnquotedNumberedClauseLine(markdown string) bool {
	return hasUnquotedMatchingLine(markdown, func(line string) bool {
		return khoanRe.MatchString(line)
	})
}

func hasUnquotedExplicitArticleLine(markdown string) bool {
	return hasUnquotedMatchingLine(markdown, func(line string) bool {
		m := dieuRe.FindStringSubmatch(line)
		if m == nil {
			return false
		}
		rest := strings.TrimSpace(line[len(m[0]):])
		return rest == "" || strings.HasPrefix(rest, ".") || strings.HasPrefix(rest, ":")
	})
}

func hasUnquotedPointLine(markdown string) bool {
	return hasUnquotedMatchingLine(markdown, func(line string) bool {
		return diemRe.MatchString(line)
	})
}

func hasUnquotedMatchingLine(markdown string, match func(string) bool) bool {
	inQuotedBlock := false
	for raw := range strings.SplitSeq(markdown, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		suppressStructure := inQuotedBlock || startsWithOpeningQuote(line)
		clean := stripMDStructuralMarkers(stripMDEmphasis(line))
		if !suppressStructure && match(clean) {
			return true
		}
		inQuotedBlock = updateQuotedBlock(inQuotedBlock, line)
	}
	return false
}

func loadNormalizeValidationDocs(t *testing.T, ctx context.Context, pool *pgxpool.Pool, limit int) []normalizeValidationDoc {
	t.Helper()
	query := `
WITH ranked_text AS (
	SELECT
		dt.*,
		row_number() OVER (
			PARTITION BY dt.document_id
			ORDER BY
				CASE dt.authority
					WHEN 'human_verified' THEN 1
					WHEN 'gazette_borndigital' THEN 2
					WHEN 'transcription_html' THEN 3
					WHEN 'ocr_extractive' THEN 4
					WHEN 'ocr_generative' THEN 5
					ELSE 99
				END,
				dt.needs_review ASC,
				dt.id
		) AS rn
	FROM silver.document_text dt
	WHERE dt.is_binding = TRUE
		AND length(btrim(coalesce(dt.markdown, ''))) > 0
)
SELECT
	d.id,
	coalesce(d.doc_number, ''),
	coalesce(sd.source, ''),
	coalesce(nullif(sd.doc_type, ''), nullif(d.doc_type, ''), ''),
	rt.authority,
	rt.source,
	coalesce(rt.extract_engine, ''),
	rt.needs_review,
	rt.markdown
FROM ranked_text rt
JOIN silver.document d ON d.id = rt.document_id
LEFT JOIN bronze.source_document sd ON sd.id = d.source_document_id
WHERE rt.rn = 1
ORDER BY
	coalesce(nullif(sd.doc_type, ''), nullif(d.doc_type, ''), ''),
	rt.authority,
	length(rt.markdown) DESC,
	d.id`
	if limit > 0 {
		query += fmt.Sprintf("\nLIMIT %d", limit)
	}

	rows, err := pool.Query(ctx, query)
	if err != nil {
		t.Fatalf("query validation docs: %v", err)
	}
	defer rows.Close()

	var docs []normalizeValidationDoc
	for rows.Next() {
		var doc normalizeValidationDoc
		if err := rows.Scan(
			&doc.DocumentID,
			&doc.DocNumber,
			&doc.Source,
			&doc.DocType,
			&doc.Authority,
			&doc.TextSource,
			&doc.ExtractEngine,
			&doc.NeedsReview,
			&doc.Markdown,
		); err != nil {
			t.Fatalf("scan validation doc: %v", err)
		}
		docs = append(docs, doc)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate validation docs: %v", err)
	}
	return docs
}

func logNormalizeValidationGroups(t *testing.T, groups map[string]normalizeValidationGroup) {
	t.Helper()
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		group := groups[key]
		t.Logf("group %s docs=%d dieu=%d khoan=%d diem=%d",
			key, group.Docs, group.Articles, group.Clauses, group.Points)
	}
}

func normalizeDocRef(doc normalizeValidationDoc) string {
	docNumber := fallback(doc.DocNumber, fmt.Sprintf("document_id=%d", doc.DocumentID))
	return fmt.Sprintf("doc=%s source=%s type=%s authority=%s",
		docNumber,
		fallback(doc.Source, "unknown"),
		fallback(doc.DocType, "unknown"),
		fallback(doc.Authority, "unknown"),
	)
}

func expectsArticleStructure(markdown string) bool {
	for raw := range strings.SplitSeq(markdown, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		clean := stripMDStructuralMarkers(line)
		if dieuRe.MatchString(clean) ||
			isArticleLikeNumericHeading(clean) ||
			hasBoldNumericHeading(line) {
			return true
		}
	}
	return false
}

func hasNumberedOutlineFallback(sections []Section) bool {
	var found bool
	var walk func([]Section)
	walk = func(nodes []Section) {
		for _, node := range nodes {
			if strings.Contains(node.CitationPath, "dieu-outline-") {
				found = true
				return
			}
			walk(node.Children)
		}
	}
	walk(sections)
	return found
}

func hasLegacyChunkableSections(sections []Section) bool {
	var found bool
	var walk func([]Section)
	walk = func(nodes []Section) {
		for _, node := range nodes {
			if node.Kind == "khoan" && strings.TrimSpace(node.Content) != "" {
				found = true
				return
			}
			walk(node.Children)
		}
	}
	walk(sections)
	return found
}

func fallback(value, replacement string) string {
	if strings.TrimSpace(value) == "" {
		return replacement
	}
	return value
}
