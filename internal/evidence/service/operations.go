package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"danny.vn/banhmi/internal/evidence/api"
	"danny.vn/banhmi/pkg/rag/retrieve"
)

// Retriever is the read slice used by evidence search.
type Retriever interface {
	SearchEvidence(context.Context, string, retrieve.SearchOpts) (retrieve.Evidence, error)
}

// CorpusReader is the structured corpus read adapter used by non-search tools.
type CorpusReader interface {
	CorpusStatus(context.Context) (api.CorpusStatusOutput, error)
	QualityGaps(context.Context, api.QualityGapsInput) (api.QualityGapsOutput, error)
	Document(context.Context, api.DocumentInput) (api.DocumentOutput, error)
}

type operations struct {
	retriever Retriever
	corpus    CorpusReader
	guide     api.GuideOutput
	version   string
	log       *slog.Logger
}

// NewOperations constructs the transport-neutral implementation of all five
// evidence operations.
func NewOperations(retriever Retriever, corpus CorpusReader, guide api.GuideOutput, version string, log *slog.Logger) (api.Operations, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &operations{retriever: retriever, corpus: corpus, guide: guide, version: version, log: log}, nil
}

func (o *operations) Guide(context.Context, api.GuideInput) (api.GuideOutput, error) {
	return o.guide, nil
}

func (o *operations) CorpusStatus(ctx context.Context, _ api.CorpusStatusInput) (api.CorpusStatusOutput, error) {
	if o.corpus == nil {
		return api.CorpusStatusOutput{}, fmt.Errorf("corpus database is not configured")
	}
	out, err := o.corpus.CorpusStatus(ctx)
	if err != nil {
		o.log.Error("evidence: corpus status", "err", err)
		return api.CorpusStatusOutput{}, fmt.Errorf("corpus status: %w", err)
	}
	out.Version = o.version
	return out, nil
}

func (o *operations) QualityGaps(ctx context.Context, in api.QualityGapsInput) (api.QualityGapsOutput, error) {
	if o.corpus == nil {
		return api.QualityGapsOutput{}, fmt.Errorf("corpus database is not configured")
	}
	out, err := o.corpus.QualityGaps(ctx, in)
	if err != nil {
		o.log.Error("evidence: quality gaps", "err", err)
		return api.QualityGapsOutput{}, fmt.Errorf("quality gaps: %w", err)
	}
	return out, nil
}

func (o *operations) Search(ctx context.Context, in api.SearchInput) (api.SearchOutput, error) {
	if o.retriever == nil {
		return api.SearchOutput{}, fmt.Errorf("retriever is not configured")
	}
	detail, known := parseSearchDetail(in.Detail)
	if !known {
		o.log.Warn("evidence: unknown search detail level, using standard", "detail", in.Detail)
	}
	relatedK := searchRelatedK(in)
	if detail == detailCompact {
		relatedK = 0
	}
	ev, err := o.retriever.SearchEvidence(ctx, strings.TrimSpace(in.Query), retrieve.SearchOpts{
		TopK: in.TopK, InForceOnly: in.InForceOnly, RelatedK: relatedK,
		AsOf: in.AsOf, IssuedFrom: in.IssuedFrom, IssuedTo: in.IssuedTo,
		Issuer: in.Issuer, DocType: in.DocType,
	})
	if err != nil {
		o.log.Error("evidence: search", "err", err)
		return api.SearchOutput{}, fmt.Errorf("search: %w", err)
	}
	out := api.SearchOutput{Hits: toSearchHits(ev.Hits, detail), Gaps: toGaps(ev.Gaps), Abstain: ev.Abstain, Scope: toScopeEvidence(ev.Scope)}
	if detail != detailCompact {
		out.RelatedHits = toRelatedHits(ev.RelatedHits, detail)
	}
	return out, nil
}

func (o *operations) Document(ctx context.Context, in api.DocumentInput) (api.DocumentOutput, error) {
	if o.corpus == nil {
		return api.DocumentOutput{}, fmt.Errorf("corpus database is not configured")
	}
	for _, name := range in.Include {
		if normalized := strings.ToLower(strings.TrimSpace(name)); normalized != "" && !documentSections[normalized] {
			o.log.Warn("evidence: unknown document include section, ignoring", "section", name)
		}
	}
	out, err := o.corpus.Document(ctx, in)
	if err != nil {
		o.log.Error("evidence: document", "err", err)
		return api.DocumentOutput{}, fmt.Errorf("document: %w", err)
	}
	return out, nil
}

var documentSections = map[string]bool{"chunks": true, "relations": true, "amendments": true, "timeline": true, "provenance": true}

type searchDetail string

const (
	detailCompact  searchDetail = "compact"
	detailStandard searchDetail = "standard"
	detailFull     searchDetail = "full"
	defaultRelatedK             = 8
	relatedSnippetMax           = 320
)

func parseSearchDetail(value string) (searchDetail, bool) {
	switch searchDetail(strings.ToLower(strings.TrimSpace(value))) {
	case "", detailStandard:
		return detailStandard, true
	case detailCompact:
		return detailCompact, true
	case detailFull:
		return detailFull, true
	default:
		return detailStandard, false
	}
}

func searchRelatedK(in api.SearchInput) int {
	if in.IncludeRelated != nil && !*in.IncludeRelated {
		return 0
	}
	if in.RelatedK > 0 {
		return in.RelatedK
	}
	return defaultRelatedK
}

func toSearchHits(hits []retrieve.Hit, detail searchDetail) []api.SearchHit {
	out := make([]api.SearchHit, 0, len(hits))
	for _, hit := range hits {
		validity := toValidity(hit.Validity)
		validity.Warning = joinWarnings(validityWarning(hit.IssuedDate, validity.EffectiveFrom), supersededWarning(hit.Validity.SupersededBy))
		shaped := api.SearchHit{
			DocNumber: hit.DocNumber, Title: hit.Title, IssuedDate: hit.IssuedDate,
			Source: hit.Source, SourceURL: hit.SourceURL,
			Cite: citeString(hit.DocNumber, hit.Citation, validity.StatusLabel, hit.SourceURL),
			Location: hit.Citation, ParentCitation: hit.ParentCitation, ContextPrefix: hit.ContextPrefix,
			Snippet: hit.Content, DocumentID: hit.DocumentID, ChunkID: hit.ChunkID,
			Score: hit.Score, Validity: validity, Text: toTextProvenance(hit.Text),
		}
		if hit.ArticleCitation != "" {
			shaped.Provision = &api.Provision{Citation: hit.ArticleCitation}
		}
		switch detail {
		case detailFull:
			shaped.VectorRank, shaped.BM25Rank = hit.VectorRank, hit.BM25Rank
			shaped.Similarity, shaped.BM25Score = hit.Similarity, hit.BM25Score
			shaped.Relations = toSearchRelations(hit.Relations, false)
			if shaped.Provision != nil {
				shaped.Provision.Text, shaped.Provision.Truncated = hit.Article, hit.ArticleTruncated
			}
		case detailStandard:
			shaped.Text = trimTextProvenance(shaped.Text)
			shaped.Relations = toSearchRelations(hit.Relations, false)
		case detailCompact:
			shaped.Text = trimTextProvenance(shaped.Text)
			shaped.Validity = compactValidity(shaped.Validity)
			shaped.ContextPrefix = ""
			shaped.Provision = nil
		}
		out = append(out, shaped)
	}
	return out
}

func trimTextProvenance(value api.TextProvenance) api.TextProvenance {
	return api.TextProvenance{HasBindingText: value.HasBindingText, HasNonBindingText: value.HasNonBindingText, NeedsReview: value.NeedsReview, Quality: value.Quality}
}

func compactValidity(value api.ValidityEvidence) api.ValidityEvidence {
	return api.ValidityEvidence{StatusClass: value.StatusClass, StatusLabel: value.StatusLabel, Warning: value.Warning}
}

func relationLabel(value string) string {
	if strings.HasPrefix(value, "vbpl_type_") {
		return "related"
	}
	return value
}

func toSearchRelations(relations []retrieve.Relation, full bool) []api.SearchRelation {
	out := make([]api.SearchRelation, 0, len(relations))
	for _, relation := range relations {
		shaped := api.SearchRelation{
			RelationID: relation.RelationID, Direction: relation.Direction, RelationType: relationLabel(relation.RelationType),
			Source: relation.Source, ToCitation: relation.ToCitation, DocumentID: relation.DocumentID,
			DocNumber: relation.DocNumber, Title: relation.Title, Resolved: relation.Resolved,
			TargetIndexed: relation.TargetIndexed, TargetHasBindingText: relation.TargetHasBindingText,
			TargetNeedsReview: relation.TargetNeedsReview, RelationTypeRaw: relation.RelationTypeRaw,
			TargetAmendedBy: relation.TargetAmendedBy,
		}
		if validity := toValidity(relation.TargetValidity); validity != (api.ValidityEvidence{}) {
			shaped.TargetValidity = &validity
		}
		if full {
			text := toTextProvenance(relation.TargetText)
			shaped.TargetText = &text
			shaped.Evidence = toRelationEvidence(relation.Evidence)
		}
		out = append(out, shaped)
	}
	return out
}

// ShapeRelations converts stored relation evidence into the shared API shape.
// The legacy database adapter uses this while it remains behind CorpusReader.
func ShapeRelations(relations []retrieve.Relation, full bool) []api.SearchRelation {
	return toSearchRelations(relations, full)
}

func toRelatedHits(hits []retrieve.RelatedHit, detail searchDetail) []api.RelatedHit {
	out := make([]api.RelatedHit, 0, len(hits))
	for _, hit := range hits {
		text := toTextProvenance(hit.Text)
		if detail != detailFull {
			text = trimTextProvenance(text)
		}
		out = append(out, api.RelatedHit{
			BaseChunkID: hit.BaseChunkID, BaseDocumentID: hit.BaseDocumentID, BaseDocNumber: hit.BaseDocNumber,
			RelationID: hit.RelationID, Direction: hit.Direction, RelationType: hit.RelationType,
			Source: hit.Source, ToCitation: hit.ToCitation, DocNumber: hit.DocNumber, Title: hit.Title,
			SourceURL: hit.SourceURL, Cite: citeString(hit.DocNumber, hit.Citation, statusLabel(hit.Validity.StatusClass), hit.SourceURL),
			StatusClass: hit.Validity.StatusClass, EffectiveDate: hit.Validity.EffectiveFrom,
			Validity: toValidity(hit.Validity), Text: text, Location: hit.Citation,
			ContextPrefix: hit.ContextPrefix, Snippet: clampSnippet(hit.Content, relatedSnippetMax),
			DocumentID: hit.DocumentID, ChunkID: hit.ChunkID, Rank: hit.Rank,
		})
	}
	return out
}

func toValidity(in retrieve.ValidityEvidence) api.ValidityEvidence {
	return api.ValidityEvidence{SectionID: in.SectionID, StatusCode: in.StatusCode, StatusClass: in.StatusClass, StatusLabel: statusLabel(in.StatusClass), EffectiveFrom: in.EffectiveFrom, EffectiveTo: in.EffectiveTo, Source: in.Source, Reason: in.Reason}
}

func toTextProvenance(in retrieve.TextEvidence) api.TextProvenance {
	out := api.TextProvenance{HasBindingText: in.HasBindingText, HasNonBindingText: in.HasNonBindingText, NeedsReview: in.NeedsReview, Authorities: append([]string(nil), in.Authorities...), Sources: append([]string(nil), in.Sources...), ExtractEngines: append([]string(nil), in.ExtractEngines...), MaxConfidence: in.MaxConfidence}
	out.Quality = textQuality(out)
	return out
}

func textQuality(value api.TextProvenance) string {
	quality := ""
	if value.HasBindingText {
		quality = "binding text"
	} else if value.HasNonBindingText {
		quality = "non-binding text"
	}
	if quality != "" && value.NeedsReview {
		quality += ", needs review"
	}
	return quality
}

func toRelationEvidence(in retrieve.RelationEvidence) *api.RelationEvidence {
	if in.EvidenceID == 0 && in.EvidenceKind == "" && in.Operator == "" && in.TargetText == "" && in.TargetCitation == "" && in.Citation == "" && in.Snippet == "" && in.SourceAuthority == "" && in.Confidence == 0 && !in.Promoted {
		return nil
	}
	return &api.RelationEvidence{EvidenceID: in.EvidenceID, EvidenceKind: in.EvidenceKind, Operator: in.Operator, TargetText: in.TargetText, TargetCitation: in.TargetCitation, Citation: in.Citation, Snippet: in.Snippet, SourceAuthority: in.SourceAuthority, Confidence: in.Confidence, Promoted: in.Promoted}
}

func toGaps(gaps []retrieve.Gap) []api.Gap {
	out := make([]api.Gap, 0, len(gaps))
	for _, gap := range gaps {
		out = append(out, api.Gap{Kind: string(gap.Kind), Message: gap.Message, BlocksAnswer: gap.BlocksAnswer, DocumentID: gap.DocumentID, DocNumber: gap.DocNumber, Title: gap.Title, RelationID: gap.RelationID, RelationType: gap.RelationType})
	}
	return out
}

func toScopeEvidence(in retrieve.ScopeEvidence) api.ScopeEvidence {
	return api.ScopeEvidence{Checked: in.Checked, InDomain: in.InDomain, MatchedTerms: append([]string(nil), in.MatchedTerms...), KnownReference: in.KnownReference}
}

func clampSnippet(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if maxRunes <= 1 || len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes-1]) + "…"
}

// ClampSnippet returns a rune-safe preview for legacy document payloads.
func ClampSnippet(value string, maxRunes int) string {
	return clampSnippet(value, maxRunes)
}

func statusLabel(class string) string {
	switch strings.ToLower(strings.TrimSpace(class)) {
	case "in_force":
		return "In force"
	case "partial":
		return "Partially in force"
	case "expired":
		return "Expired/repealed"
	case "not_yet":
		return "Not yet effective"
	case "suspended":
		return "Suspended"
	case "unknown":
		return "Validity unknown — verify against the official source"
	case "":
		return ""
	default:
		return class
	}
}

// StatusLabel returns the stable agent-facing validity label.
func StatusLabel(class string) string { return statusLabel(class) }

func citeString(docNumber, citation, status, sourceURL string) string {
	parts := make([]string, 0, 2)
	if value := strings.TrimSpace(citation); value != "" {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(docNumber); value != "" {
		parts = append(parts, value)
	}
	citationText := strings.Join(parts, ", ")
	if value := strings.TrimSpace(status); value != "" {
		citationText += " — " + value
	}
	if value := strings.TrimSpace(sourceURL); value != "" {
		citationText += " — " + value
	}
	return citationText
}

// Citation builds the ready-to-paste evidence citation used by search and
// document payloads.
func Citation(docNumber, citation, status, sourceURL string) string {
	return citeString(docNumber, citation, status, sourceURL)
}

func validityWarning(issuedDate, effectiveFrom string) string {
	if issuedDate == "" || effectiveFrom == "" {
		return ""
	}
	issued, issueErr := time.Parse("2006-01-02", issuedDate)
	effective, effectiveErr := time.Parse("2006-01-02", effectiveFrom)
	if issueErr != nil || effectiveErr != nil || !effective.Before(issued) {
		return ""
	}
	return fmt.Sprintf("source effective date (%s) precedes the issue date (%s); a document cannot take effect before it is issued — likely a source data error. banhmi does not auto-correct it: verify the effective date against the document's own enacting clause (Điều khoản thi hành).", effectiveFrom, issuedDate)
}

// ValidityWarning exposes the shared source-date consistency warning.
func ValidityWarning(issuedDate, effectiveFrom string) string {
	return validityWarning(issuedDate, effectiveFrom)
}

func supersededWarning(documents []string) string {
	if len(documents) == 0 {
		return ""
	}
	return fmt.Sprintf("the source still badges this document current, but a confirmed relation records %s as replacing/repealing it — the source's own metadata is contradictory. banhmi does not override the badge: open the named document(s) and verify which text is operative before relying on this one.", strings.Join(documents, ", "))
}

// SupersededWarning exposes the shared source-metadata contradiction warning.
func SupersededWarning(documents []string) string { return supersededWarning(documents) }

func joinWarnings(values ...string) string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return strings.Join(out, " ALSO: ")
}

// JoinWarnings preserves every applicable evidence warning.
func JoinWarnings(values ...string) string { return joinWarnings(values...) }
