package formats

import (
	"fmt"
	"sort"

	"danny.vn/banhmi/internal/corpus/model"
)

func coverageShape(gaps []Gap, retained []Retained) error {
	partitions := make(map[int][]model.Span)
	for i, gap := range gaps {
		if gap.Ordinal < 0 || gap.Ordinal >= len(retained) || !gap.Scope.Valid() || gap.ScopeKey == "" || gap.ReasonCode == "" || (i > 0 && !gapLess(gaps[i-1], gap)) {
			return fmt.Errorf("gaps require valid unique sorted scopes and ordinals")
		}
		if err := coverageSpans(gap.Spans); err != nil {
			return err
		}
		partitions[gap.Ordinal] = append(partitions[gap.Ordinal], gap.Spans...)
	}
	for i, entry := range retained {
		if entry.Ordinal != i {
			return fmt.Errorf("retained ordinals must cover candidate in order")
		}
		if err := coverageSpans(entry.Spans); err != nil {
			return err
		}
		partitions[entry.Ordinal] = append(partitions[entry.Ordinal], entry.Spans...)
	}
	for _, spans := range partitions {
		sort.Slice(spans, func(i, j int) bool { return spanLess(spans[i], spans[j]) })
		for i := 1; i < len(spans); i++ {
			if spans[i-1].ArtifactHash != spans[i].ArtifactHash || spans[i-1].Range.End > spans[i].Range.Start {
				return fmt.Errorf("ordinal coverage uses different text objects or overlapping ranges")
			}
		}
	}
	return nil
}

func gapLess(a, b Gap) bool {
	if a.Ordinal != b.Ordinal {
		return a.Ordinal < b.Ordinal
	}
	if a.Scope != b.Scope {
		return a.Scope < b.Scope
	}
	if a.ScopeKey != b.ScopeKey {
		return a.ScopeKey < b.ScopeKey
	}
	return a.ReasonCode < b.ReasonCode
}

func coverageSpans(spans []model.Span) error {
	if spans == nil {
		return fmt.Errorf("coverage spans must be non-null")
	}
	for i, span := range spans {
		if err := span.Validate(); err != nil {
			return err
		}
		if span.BlockID != "document" || span.Space != model.NormalizedText || span.TokenOrdinal != nil || span.Range == nil || span.ReasonCode != "" || span.Page != nil || span.Object != "" || span.Member != "" {
			return fmt.Errorf("coverage requires aggregate normalized-text byte ranges")
		}
		if i > 0 {
			previous := spans[i-1]
			if !spanLess(previous, span) || (previous.ArtifactHash == span.ArtifactHash && previous.Range.End > span.Range.Start) {
				return fmt.Errorf("coverage spans must be sorted and nonoverlapping")
			}
		}
	}
	return nil
}

func spanLess(a, b model.Span) bool {
	if a.ArtifactHash != b.ArtifactHash {
		return a.ArtifactHash < b.ArtifactHash
	}
	if a.BlockID != b.BlockID {
		return a.BlockID < b.BlockID
	}
	if a.Space != b.Space {
		return a.Space < b.Space
	}
	if a.Range.Start != b.Range.Start {
		return a.Range.Start < b.Range.Start
	}
	return a.Range.End < b.Range.End
}

// CoverageArtifact supplies verified aggregate text, or null for missing
// conversion, at one candidate ordinal. No object is opened by partition checks.
type CoverageArtifact struct {
	Ordinal        int
	NormalizedText *model.AssetRef
}

// ValidatePartition proves complete retained/excluded coverage over decoded
// text lengths. Empty or missing text requires an explicit artifact or revision
// gap with no fabricated range. Overlap across distinct gap scopes also rejects.
func ValidatePartition(gaps []Gap, retained []Retained, artifacts []CoverageArtifact, limits Limits) error {
	if err := limits.Validate(); err != nil {
		return err
	}
	if err := validateTree(struct {
		Gaps      []Gap
		Retained  []Retained
		Artifacts []CoverageArtifact
	}{gaps, retained, artifacts}, limits); err != nil {
		return err
	}
	if err := coverageShape(gaps, retained); err != nil {
		return err
	}
	if len(artifacts) != len(retained) {
		return fmt.Errorf("partition requires every input ordinal")
	}
	byOrdinal := make([][]model.Span, len(artifacts))
	explicit := make([]bool, len(artifacts))
	for _, gap := range gaps {
		byOrdinal[gap.Ordinal] = append(byOrdinal[gap.Ordinal], gap.Spans...)
		if gap.Scope == model.GapArtifact || gap.Scope == model.GapDocumentRevision {
			explicit[gap.Ordinal] = true
		}
	}
	for i, artifact := range artifacts {
		if artifact.Ordinal != i {
			return fmt.Errorf("partition artifact ordinals must be complete and sorted")
		}
		spans := append(byOrdinal[i], retained[i].Spans...)
		if artifact.NormalizedText == nil || artifact.NormalizedText.ByteSize == 0 {
			if len(spans) != 0 || !explicit[i] {
				return fmt.Errorf("empty or missing text requires empty coverage and explicit gap")
			}
			continue
		}
		sort.Slice(spans, func(i, j int) bool { return spanLess(spans[i], spans[j]) })
		var end int64
		for _, span := range spans {
			if span.ArtifactHash != artifact.NormalizedText.SHA256 || span.Range.Start != end || span.Range.End > artifact.NormalizedText.ByteSize {
				return fmt.Errorf("partition has wrong object, overlap, uncovered bytes or overflow")
			}
			end = span.Range.End
		}
		if end != artifact.NormalizedText.ByteSize {
			return fmt.Errorf("partition does not cover complete normalized text")
		}
	}
	return nil
}

// ValidateSpanReferences requires every report span hash to resolve in the
// verified typed closure. A span hash alone cannot introduce another object.
func ValidateSpanReferences(spans []model.Span, assets []model.AssetRef, limits Limits) error {
	if err := limits.Validate(); err != nil {
		return err
	}
	if err := validateTree(struct {
		Spans  []model.Span
		Assets []model.AssetRef
	}{spans, assets}, limits); err != nil {
		return err
	}
	objects := make(map[model.Digest]model.AssetRef, len(assets))
	for _, asset := range assets {
		objects[asset.SHA256] = asset
	}
	for _, span := range spans {
		asset, ok := objects[span.ArtifactHash]
		if !ok || (span.Range != nil && (span.Range.End > asset.ByteSize || span.ReasonCode != "")) {
			return fmt.Errorf("span lacks typed closure reference or exceeds bytes")
		}
	}
	return nil
}
