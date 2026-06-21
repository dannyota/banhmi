package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.temporal.io/sdk/activity"

	"danny.vn/banhmi/pkg/ingest"
	dbbronze "danny.vn/banhmi/pkg/store/bronze"
	dbsilver "danny.vn/banhmi/pkg/store/silver"
)

type relationCandidate struct {
	targetNumber    string
	targetID        string
	targetTitle     string
	relationType    string
	relationTypeRaw *int32
	operator        string
	fromSectionID   *int64
	evidenceKind    string
	source          string
	sourceAuthority string
	citation        string
	targetCitation  *string
	snippet         string
	confidence      float64
	promoted        bool
}

type relationTextDoc struct {
	documentID      int64
	currentNumber   string
	currentNorm     string
	title           string
	source          string
	sourceAuthority string
}

type relationTextSection struct {
	id           int64
	citationPath string
	heading      string
	content      string
}

var docNumberMentionRe = regexp.MustCompile(`(?i)\b\d{1,4}\s*/\s*(?:\d{4}\s*/\s*)?[\pL\d]+(?:\s*-\s*[\pL\d]+)+`)

func (a *Activities) persistRelationEvidenceBestEffort(
	ctx context.Context,
	target normalizeTarget,
	now time.Time,
	result *NormalizeResult,
) {
	evidence, relations, err := a.persistRelationEvidence(ctx, target, now, result)
	if err != nil {
		result.Warnings = append(result.Warnings, "relation_evidence_write_failed")
		activity.GetLogger(ctx).Warn("normalize: relation evidence failed",
			"doc", target.fetchDoc.ExternalID, "document_id", target.document.ID, "err", err)
		return
	}
	result.RelationEvidenceWritten = evidence
	result.RelationsWritten = relations
}

func (a *Activities) persistRelationEvidence(
	ctx context.Context,
	target normalizeTarget,
	now time.Time,
	result *NormalizeResult,
) (int, int, error) {
	payloads, err := a.bronze.ListRawPayloadsByDocument(ctx, target.sourceDoc.ID)
	if err != nil {
		return 0, 0, fmt.Errorf("list raw payloads: %w", err)
	}
	sectionRows, err := a.silver.ListSectionsByDocument(ctx, target.document.ID)
	if err != nil {
		return 0, 0, fmt.Errorf("list sections: %w", err)
	}
	sections := silverSectionRows(sectionRows)

	candidates := collectTextRelationCandidates(relationTextDoc{
		documentID:      target.document.ID,
		currentNumber:   nullableString(target.document.DocNumber),
		currentNorm:     target.document.DocNumberNorm,
		title:           nullableString(target.sourceDoc.Title),
		source:          target.sourceDoc.Source,
		sourceAuthority: relationSourceAuthority(target.sourceDoc, *result),
	}, relationSections(sections))
	// Structured source relations are strongest. Write them after text evidence so
	// document_relation retains the source raw type when both paths see the same edge.
	candidates = append(candidates, collectStructuredRelationCandidates(target.sourceDoc, payloads)...)

	source := target.sourceDoc.Source
	if a.dbpool == nil {
		evidenceWritten, relationsWritten, err := a.writeRelationCandidates(ctx, a.silver, target.document.ID, source, candidates, now)
		if err != nil {
			return evidenceWritten, relationsWritten, err
		}
		a.backfillRelationCandidatesBestEffort(ctx, target, candidates, now, result)
		return evidenceWritten, relationsWritten, nil
	}
	tx, err := a.dbpool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("begin relation evidence transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	evidenceWritten, relationsWritten, err := a.writeRelationCandidates(ctx, a.silver.WithTx(tx), target.document.ID, source, candidates, now)
	if err != nil {
		return evidenceWritten, relationsWritten, err
	}
	if err := tx.Commit(ctx); err != nil {
		return evidenceWritten, relationsWritten, fmt.Errorf("commit relation evidence transaction: %w", err)
	}
	a.backfillRelationCandidatesBestEffort(ctx, target, candidates, now, result)
	return evidenceWritten, relationsWritten, nil
}

func (a *Activities) writeRelationCandidates(
	ctx context.Context,
	q *dbsilver.Queries,
	documentID int64,
	source string,
	candidates []relationCandidate,
	now time.Time,
) (int, int, error) {
	if err := q.DeleteRelationEvidenceByDocumentSource(ctx, dbsilver.DeleteRelationEvidenceByDocumentSourceParams{
		FromDocumentID: documentID,
		Source:         source,
	}); err != nil {
		return 0, 0, fmt.Errorf("delete relation evidence: %w", err)
	}
	if err := q.DeleteDocumentRelationsByDocumentSource(ctx, dbsilver.DeleteDocumentRelationsByDocumentSourceParams{
		FromDocumentID: documentID,
		Source:         strPtr(source),
	}); err != nil {
		return 0, 0, fmt.Errorf("delete document relations: %w", err)
	}

	evidenceWritten, relationsWritten := 0, 0
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.targetNumber) == "" {
			continue
		}
		refID, err := upsertRelationTargetRef(ctx, q, candidate, now)
		if err != nil {
			return evidenceWritten, relationsWritten, err
		}
		id, err := q.UpsertRelationEvidence(ctx, dbsilver.UpsertRelationEvidenceParams{
			FromDocumentID:  documentID,
			FromSectionID:   candidate.fromSectionID,
			TargetRefID:     refID,
			EvidenceKey:     relationEvidenceKey(candidate),
			EvidenceKind:    candidate.evidenceKind,
			RelationType:    relationTypeOrMention(candidate.relationType),
			RelationTypeRaw: candidate.relationTypeRaw,
			Operator:        candidate.operator,
			TargetText:      candidate.targetNumber,
			TargetCitation:  candidate.targetCitation,
			Citation:        candidate.citation,
			Snippet:         candidate.snippet,
			Source:          candidate.source,
			SourceAuthority: candidate.sourceAuthority,
			Confidence:      candidate.confidence,
			Promoted:        candidate.promoted,
			CreatedAt:       now,
		})
		if err != nil {
			return evidenceWritten, relationsWritten, fmt.Errorf("upsert relation evidence: %w", err)
		}
		if id != 0 {
			evidenceWritten++
		}
		if candidate.promoted && relationTypeOrMention(candidate.relationType) != "mentions" {
			if _, err := q.UpsertDocumentRelation(ctx, dbsilver.UpsertDocumentRelationParams{
				FromDocumentID:  documentID,
				ToRefID:         refID,
				RelationType:    relationTypeOrMention(candidate.relationType),
				RelationTypeRaw: candidate.relationTypeRaw,
				FromSectionID:   candidate.fromSectionID,
				ToCitation:      candidate.targetCitation,
				Source:          strPtr(candidate.source),
			}); err != nil {
				return evidenceWritten, relationsWritten, fmt.Errorf("upsert document relation: %w", err)
			}
			relationsWritten++
		}
	}
	return evidenceWritten, relationsWritten, nil
}

func upsertRelationTargetRef(ctx context.Context, q *dbsilver.Queries, candidate relationCandidate, now time.Time) (int64, error) {
	refKey := relationTargetRefKey(candidate)
	docID, err := relationTargetDocumentID(ctx, q, candidate)
	if err != nil {
		return 0, err
	}

	srcRef, err := json.Marshal(map[string]any{
		"source":        candidate.source,
		"target_id":     candidate.targetID,
		"target_number": candidate.targetNumber,
		"target_title":  candidate.targetTitle,
	})
	if err != nil {
		return 0, fmt.Errorf("marshal doc_ref source: %w", err)
	}
	// Label by number only when the number is a real identity — the VBPL
	// "KHÔNG SỐ" sentinel falls through to the title ("Hiến pháp năm 1992 …"),
	// so un-numbered stubs stay readable in quality_gaps.
	label := strings.TrimSpace(candidate.targetNumber)
	if canonicalDocNumber(label) == "" {
		label = strings.TrimSpace(candidate.targetTitle)
	}
	return q.UpsertDocRef(ctx, dbsilver.UpsertDocRefParams{
		RefKey:     refKey,
		DocumentID: docID,
		Label:      strPtr(label),
		SrcRef:     srcRef,
		CreatedAt:  now,
	})
}

func relationTargetDocumentID(ctx context.Context, q *dbsilver.Queries, candidate relationCandidate) (*int64, error) {
	if sourceKey := sourceDocRefKey(candidate.source, candidate.targetID); sourceKey != "" {
		id, err := q.DocumentIDByAlias(ctx, dbsilver.DocumentIDByAliasParams{
			Source:     strings.TrimSpace(candidate.source),
			ExternalID: strings.TrimSpace(candidate.targetID),
		})
		if err == nil {
			return &id, nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("resolve target document alias %s: %w", sourceKey, err)
	}

	norm := normalizeDocNumberForStorage(candidate.targetNumber)
	if norm == "" {
		return nil, nil
	}
	ids, err := q.DocumentIDsByNumberNorm(ctx, norm)
	if err != nil {
		return nil, fmt.Errorf("resolve target document %s: %w", candidate.targetNumber, err)
	}
	// Zero documents: target not ingested. Two or more: the bare number is
	// ambiguous (distinct documents share it) — leave the reference a stub.
	if len(ids) == 1 {
		return &ids[0], nil
	}
	return nil, nil
}

func collectStructuredRelationCandidates(sd dbbronze.BronzeSourceDocument, payloads []dbbronze.BronzeRawPayload) []relationCandidate {
	var out []relationCandidate
	for _, payload := range payloads {
		if payload.Kind != "references_json" || payload.Content == nil || strings.TrimSpace(*payload.Content) == "" {
			continue
		}
		var refs []ingest.Relation
		if err := json.Unmarshal([]byte(*payload.Content), &refs); err != nil {
			continue
		}
		for _, ref := range refs {
			// A source target id is identity enough even when the number is
			// missing or the VBPL "KHÔNG SỐ" sentinel (Hiến pháp, un-numbered
			// ordinances) — those references must keep their edges.
			targetNumber := canonicalDocNumber(ref.TargetNumber)
			if targetNumber == "" && strings.TrimSpace(ref.TargetID) == "" {
				continue
			}
			if targetNumber != "" && normalizeDocNumberForStorage(targetNumber) == sourceDocNumberNorm(sd) {
				continue
			}
			var raw *int32
			if ref.TypeRaw != 0 {
				v := int32(ref.TypeRaw) //nolint:gosec // source relation codes are small integers.
				raw = &v
			}
			// Sources whose relations come from an official structured endpoint
			// (vbpl's relation graph; agclom's P.U. subsidiary-legislation feed) are
			// trusted and promoted; text-mined relations stay weak.
			trustedStructured := sd.Source == "vbpl" || sd.Source == "agclom"
			relationType := "weak_relation"
			evidenceKind := "weak_relation"
			sourceAuthority := "source_structured"
			confidence := 0.65
			promoted := false
			operator := strings.TrimSpace(ref.Type)
			if operator == "" && ref.TypeRaw != 0 {
				operator = fmt.Sprintf("type_%d", ref.TypeRaw)
			}
			if trustedStructured {
				relationType = relationTypeOrMention(operator)
				if sd.Source == "agclom" {
					// agclom edges are a principal Act → its P.U.(A)/(B) subsidiary
					// legislation (operator pua/pub) — an authoritative parent→child
					// link, not an amendment. The pua/pub detail stays in operator.
					relationType = "subsidiary_legislation"
				}
				evidenceKind = "structured_relation"
				sourceAuthority = "official_structured"
				confidence = 1
				promoted = true
			}
			out = append(out, relationCandidate{
				targetNumber:    targetNumber,
				targetID:        strings.TrimSpace(ref.TargetID),
				targetTitle:     strings.TrimSpace(ref.TargetTitle),
				relationType:    relationType,
				relationTypeRaw: raw,
				operator:        operator,
				evidenceKind:    evidenceKind,
				source:          sd.Source,
				sourceAuthority: sourceAuthority,
				citation:        sd.Source + ":references",
				snippet:         truncateForEvidence(strings.TrimSpace(ref.TargetTitle), 500),
				confidence:      confidence,
				promoted:        promoted,
			})
		}
	}
	return dedupeRelationCandidates(out)
}

func collectTextRelationCandidates(doc relationTextDoc, sections []relationTextSection) []relationCandidate {
	if doc.currentNorm == "" {
		doc.currentNorm = normalizeDocNumberForStorage(doc.currentNumber)
	}
	var out []relationCandidate
	titleDoc := doc
	titleDoc.sourceAuthority = metadataRelationAuthority(doc.source)
	titleCandidates := relationCandidatesFromText(titleDoc, nil, "title", doc.title, true)
	out = append(out, titleCandidates...)
	titleTargets := weakTitleTargets(titleCandidates)

	for _, section := range sections {
		text := strings.TrimSpace(strings.Join([]string{section.heading, section.content}, "\n"))
		if text == "" {
			continue
		}
		sectionID := section.id
		before := len(out)
		out = append(out, relationCandidatesFromText(doc, &sectionID, section.citationPath, text, false)...)
		if len(out) > before {
			continue
		}
		operatorText := strings.TrimSpace(section.content)
		if operatorText == "" {
			operatorText = text
		}
		operator := weakRelationOperator(operatorText)
		if operator == "" && operatorText != text {
			operatorText = text
			operator = weakRelationOperator(operatorText)
		}
		if operator == "" || len(titleTargets) != 1 {
			continue
		}
		target := titleTargets[0]
		out = append(out, relationCandidate{
			targetNumber:    target.targetNumber,
			targetID:        target.targetID,
			targetTitle:     target.targetTitle,
			relationType:    "weak_relation",
			operator:        operator,
			fromSectionID:   &sectionID,
			evidenceKind:    "weak_relation",
			source:          doc.source,
			sourceAuthority: doc.sourceAuthority,
			citation:        section.citationPath,
			targetCitation:  strPtr(extractTargetCitation(operatorText)),
			snippet:         truncateForEvidence(operatorText, 500),
			confidence:      confidenceForWeakEvidence(doc.sourceAuthority, false, operator),
			promoted:        false,
		})
	}
	return dedupeRelationCandidates(out)
}

func relationCandidatesFromText(doc relationTextDoc, sectionID *int64, citation, text string, titleLevel bool) []relationCandidate {
	var out []relationCandidate
	matches := docNumberMentionRe.FindAllStringIndex(text, -1)
	for _, match := range matches {
		raw := text[match[0]:match[1]]
		targetNumber := canonicalDocNumber(raw)
		if targetNumber == "" || normalizeDocNumberForStorage(targetNumber) == doc.currentNorm {
			continue
		}
		statement, localStart, _ := statementAround(text, match[0], match[1])
		relationContext := relationContextForTarget(statement, localStart)
		operator := weakRelationOperator(relationContext)
		if operator == "" {
			operator = weakRelationOperator(statement)
		}
		out = append(out, relationCandidate{
			targetNumber:    targetNumber,
			relationType:    "weak_relation",
			operator:        operator,
			fromSectionID:   sectionID,
			evidenceKind:    "weak_relation",
			source:          doc.source,
			sourceAuthority: doc.sourceAuthority,
			citation:        citation,
			targetCitation:  targetCitationForEvidence(text, titleLevel),
			snippet:         truncateForEvidence(statement, 500),
			confidence:      confidenceForWeakEvidence(doc.sourceAuthority, titleLevel, operator),
			promoted:        false,
		})
	}
	return out
}

func weakTitleTargets(candidates []relationCandidate) []relationCandidate {
	var out []relationCandidate
	for _, candidate := range candidates {
		if candidate.evidenceKind == "weak_relation" {
			out = append(out, candidate)
		}
	}
	return out
}

func relationSections(rows []dbsilver.SilverDocumentSection) []relationTextSection {
	out := make([]relationTextSection, 0, len(rows))
	for _, row := range rows {
		out = append(out, relationTextSection{
			id:           row.ID,
			citationPath: row.CitationPath,
			heading:      nullableString(row.Heading),
			content:      nullableString(row.Content),
		})
	}
	return out
}

func weakRelationOperator(text string) string {
	low := strings.ToLower(text)
	switch {
	case strings.Contains(low, "đính chính"):
		return "đính chính"
	case strings.Contains(low, "bãi bỏ") || strings.Contains(low, "hết hiệu lực"):
		if strings.Contains(low, "hết hiệu lực") {
			return "hết hiệu lực"
		}
		return "bãi bỏ"
	case strings.Contains(low, "thay thế"):
		return "thay thế"
	case strings.Contains(low, "sửa đổi") && (strings.Contains(low, "bổ sung") || strings.Contains(low, "bổ xung")):
		return "sửa đổi, bổ sung"
	case strings.Contains(low, "sửa đổi"):
		return "sửa đổi"
	case strings.Contains(low, "bổ sung") || strings.Contains(low, "bổ xung"):
		return "bổ sung"
	case strings.Contains(low, "căn cứ"):
		return "căn cứ"
	default:
		return ""
	}
}

func relationContextForTarget(statement string, targetStart int) string {
	if targetStart < 0 {
		targetStart = 0
	}
	if targetStart > len(statement) {
		targetStart = len(statement)
	}
	prefix := strings.ToLower(statement[:targetStart])
	start := 0
	operator := ""
	for _, op := range []string{"đính chính", "bãi bỏ", "hết hiệu lực", "thay thế", "sửa đổi", "bổ sung", "bổ xung", "căn cứ"} {
		if i := strings.LastIndex(prefix, op); i >= start {
			start = i
			operator = op
		}
	}
	if operator == "bổ sung" {
		if i := strings.LastIndex(prefix[:start], "sửa đổi"); i >= 0 && start-i <= 20 {
			start = i
		}
	}
	if operator == "bổ xung" {
		if i := strings.LastIndex(prefix[:start], "sửa đổi"); i >= 0 && start-i <= 20 {
			start = i
		}
	}
	if operator == "" {
		return ""
	}
	if len([]rune(prefix[start:])) > 180 {
		return ""
	}
	return statement[start:]
}

func statementAround(text string, start, end int) (string, int, int) {
	if start < 0 {
		start = 0
	}
	if end > len(text) {
		end = len(text)
	}
	left := 0
	for i := start - 1; i >= 0; i-- {
		switch text[i] {
		case '.', ';', '\n':
			left = i + 1
			i = -1
		}
	}
	right := len(text)
	for i := end; i < len(text); i++ {
		switch text[i] {
		case '.', ';', '\n':
			right = i
			i = len(text)
		}
	}
	statement := strings.TrimSpace(text[left:right])
	trimmedLeft := left + len(text[left:right]) - len(strings.TrimLeft(text[left:right], " \t\r\n"))
	localStart := start - trimmedLeft
	localEnd := end - trimmedLeft
	if localStart < 0 {
		localStart = 0
	}
	if localEnd > len(statement) {
		localEnd = len(statement)
	}
	return statement, localStart, localEnd
}

func targetCitationForEvidence(text string, titleLevel bool) *string {
	if titleLevel {
		return nil
	}
	if citation := extractTargetCitation(text); citation != "" {
		return &citation
	}
	return nil
}

func extractTargetCitation(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.Index(strings.ToLower(text), "như sau"); i > 0 {
		text = strings.TrimSpace(text[:i])
	}
	text = strings.Trim(text, " :;,.")
	return truncateForEvidence(text, 180)
}

func relationSourceAuthority(sd dbbronze.BronzeSourceDocument, result NormalizeResult) string {
	switch result.TextAuthority {
	case "vbpl_provision_tree":
		return "official_tree"
	case "gazette_borndigital":
		return "official_text"
	case "transcription_html":
		return "non_binding_transcription"
	case "ocr_extractive", "ocr_generative":
		return "review_text"
	default:
		if sd.Source == "vbpl" || sd.Source == "congbao" || sd.Source == "sbv_hanoi" {
			return "official_metadata"
		}
		return "unknown"
	}
}

func metadataRelationAuthority(source string) string {
	if source == "vbpl" || source == "congbao" || source == "sbv_hanoi" {
		return "official_metadata"
	}
	return "unknown"
}

func confidenceForWeakEvidence(authority string, titleLevel bool, operator string) float64 {
	hasOperator := strings.TrimSpace(operator) != ""
	if titleLevel {
		switch authority {
		case "official_tree", "official_text", "official_metadata":
			if hasOperator {
				return 0.65
			}
			return 0.5
		case "non_binding_transcription":
			if hasOperator {
				return 0.45
			}
			return 0.35
		default:
			return 0.3
		}
	}
	switch authority {
	case "official_tree", "official_text", "official_metadata":
		if hasOperator {
			return 0.55
		}
		return 0.45
	case "non_binding_transcription":
		if hasOperator {
			return 0.35
		}
		return 0.25
	default:
		return 0.2
	}
}

func dedupeRelationCandidates(candidates []relationCandidate) []relationCandidate {
	seen := map[string]bool{}
	out := make([]relationCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		key := relationEvidenceKey(candidate)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, candidate)
	}
	return out
}

func relationEvidenceKey(candidate relationCandidate) string {
	target := relationEvidenceTargetKey(candidate)
	base := strings.Join([]string{
		candidate.source,
		candidate.sourceAuthority,
		candidate.evidenceKind,
		relationTypeOrMention(candidate.relationType),
		target,
		candidate.citation,
		nullableString(candidate.targetCitation),
	}, "|")
	sum := sha256.Sum256([]byte(base))
	return hex.EncodeToString(sum[:])
}

func relationEvidenceTargetKey(candidate relationCandidate) string {
	if sourceKey := sourceDocRefKey(candidate.source, candidate.targetID); sourceKey != "" {
		return sourceKey
	}
	return normalizeDocNumberForStorage(candidate.targetNumber)
}

func relationTargetRefKey(candidate relationCandidate) string {
	if sourceKey := sourceDocRefKey(candidate.source, candidate.targetID); sourceKey != "" {
		return sourceKey
	}
	return canonicalDocRefKey(candidate.targetNumber)
}

func sourceDocRefKey(source, externalID string) string {
	source = strings.TrimSpace(source)
	externalID = strings.TrimSpace(externalID)
	if source == "" || externalID == "" {
		return ""
	}
	return source + ":" + externalID
}

func relationTypeOrMention(relationType string) string {
	if s := strings.TrimSpace(relationType); s != "" {
		return s
	}
	return "mentions"
}

func sourceDocNumberNorm(sd dbbronze.BronzeSourceDocument) string {
	if s := strings.TrimSpace(sd.DocNumberNorm); s != "" {
		return s
	}
	return normalizeDocNumberForStorage(nullableString(sd.DocNumber))
}

func canonicalDocRefKey(number string) string {
	return strings.ToUpper(strings.Join(strings.Fields(canonicalDocNumber(number)), " "))
}

func canonicalDocNumber(number string) string {
	number = strings.TrimSpace(number)
	if number == "" {
		return ""
	}
	number = regexp.MustCompile(`\s*([/-])\s*`).ReplaceAllString(number, "$1")
	number = strings.Trim(number, " \t\r\n,.;:()[]{}")
	number = strings.ToUpper(number)
	// VBPL writes the sentinel "KHÔNG SỐ" ("no number") for un-numbered
	// documents (Hiến pháp, old ordinances). It is not an identity: treating it
	// as a number would key every un-numbered document together.
	if strings.Join(strings.Fields(number), " ") == "KHÔNG SỐ" {
		return ""
	}
	return number
}

func truncateForEvidence(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return strings.TrimSpace(string(runes[:limit]))
}

func nullableString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
