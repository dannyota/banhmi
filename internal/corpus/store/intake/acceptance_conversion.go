package intake

import (
	"context"
	"fmt"

	"danny.vn/banhmi/internal/corpus/model"
)

// Conversion membership is local to one sandbox manifest. Catalog registration
// and execution protection do not make an omitted dependency a sandbox output.
func (v *acceptanceEvidence) conversionMembership(ctx context.Context, doc model.ConvertedDocument, manifest []model.AssetRef, maps []model.OffsetMapSegment, edits []model.EditLedgerEntry, extra ...model.Span) error {
	members := make(map[model.AssetRef]bool, len(manifest))
	byHash := make(map[model.Digest]model.AssetRef, len(manifest))
	for _, ref := range manifest {
		members[ref], byHash[ref.SHA256] = true, ref
	}
	require := func(ref model.AssetRef) error {
		if !members[ref] {
			return fmt.Errorf("acceptance conversion membership: missing dependency")
		}
		return v.bound(ctx, 1, 0)
	}
	span := func(s model.Span) error {
		// The captured original is an external input. Every other coordinate
		// space resolves to a content-addressed sandbox output.
		if s.Space == model.OriginalBytes {
			if s.ArtifactHash != doc.Original.Asset.SHA256 {
				return fmt.Errorf("acceptance conversion membership: foreign original span")
			}
			return nil
		}
		ref, ok := byHash[s.ArtifactHash]
		if !ok || ref.Path != "objects/"+string(s.ArtifactHash) {
			return fmt.Errorf("acceptance conversion membership: missing span object")
		}
		return require(ref)
	}
	for _, ref := range []model.AssetRef{doc.RawBlocks, doc.NormalizedBlocks, doc.RawText, doc.NormalizedText, doc.OffsetMap, doc.EditLedger} {
		if ref.Path != "objects/"+string(ref.SHA256) {
			return fmt.Errorf("acceptance conversion membership: noncanonical object path")
		}
		if err := require(ref); err != nil {
			return err
		}
	}
	for _, block := range doc.Blocks {
		if block.NormalizedText.Path != "objects/"+string(block.NormalizedText.SHA256) {
			return fmt.Errorf("acceptance conversion membership: noncanonical block path")
		}
		if err := require(block.NormalizedText); err != nil {
			return err
		}
		for _, s := range block.SourceSpans {
			if err := span(s); err != nil {
				return err
			}
		}
	}
	for _, segment := range maps {
		for _, spans := range [][]model.Span{segment.Sources, segment.Destinations} {
			for _, s := range spans {
				if err := span(s); err != nil {
					return err
				}
			}
		}
	}
	for _, edit := range edits {
		if edit.Removed == nil || edit.Replacement == nil {
			return fmt.Errorf("acceptance conversion membership: missing edit bytes")
		}
		for _, ref := range []model.AssetRef{edit.Evidence, *edit.Removed, *edit.Replacement} {
			if ref.Path != "objects/"+string(ref.SHA256) {
				return fmt.Errorf("acceptance conversion membership: noncanonical edit path")
			}
			if err := require(ref); err != nil {
				return err
			}
		}
		for _, spans := range [][]model.Span{edit.Inputs, edit.Outputs} {
			for _, s := range spans {
				if err := span(s); err != nil {
					return err
				}
			}
		}
	}
	for _, s := range extra {
		if err := span(s); err != nil {
			return err
		}
	}
	return nil
}

// sourceRoots repeats the frozen validSourceRoots checks over retained blocks.
// Range bounds and span grammar are checked against the catalog separately.
func (v *acceptanceEvidence) sourceRoots(doc model.ConvertedDocument) error {
	var member model.Digest
	consumed := make(map[model.ByteRange]bool)
	fail := func() error { return fmt.Errorf("acceptance conversion source roots") }
	for _, block := range doc.Blocks {
		if v.detected[doc.DetectorHash] == "html" {
			var previousEnd int64
			for i, s := range block.SourceSpans {
				if s.Space != model.OriginalBytes || s.ArtifactHash != doc.Original.Asset.SHA256 || s.Member != "" || s.Range == nil || s.Range.End > doc.Original.Asset.ByteSize || s.BlockID != block.ID || s.Page != nil || s.Object != "" || s.ReasonCode != "" || s.TokenOrdinal == nil {
					return fail()
				}
				if i > 0 && s.Range.Start < previousEnd || consumed[*s.Range] {
					return fail()
				}
				consumed[*s.Range], previousEnd = true, s.Range.End
			}
			continue
		}
		if v.detected[doc.DetectorHash] != "docx" || len(block.SourceSpans) != 2 || block.Kind != model.BlockParagraph {
			return fail()
		}
		root, decoded := block.SourceSpans[0], block.SourceSpans[1]
		if root.Space != model.OriginalBytes || root.ArtifactHash != doc.Original.Asset.SHA256 || root.Member != "word/document.xml" || root.Range != nil || root.ReasonCode != "BYTE_MAPPING_UNAVAILABLE" || root.Object != fmt.Sprintf("paragraph/%d", block.Ordinal) || root.TokenOrdinal == nil || root.BlockID != block.ID || root.Page != nil {
			return fail()
		}
		if decoded.Space != model.DecodedMember || decoded.Member != root.Member || decoded.Object != root.Object || decoded.TokenOrdinal == nil || *decoded.TokenOrdinal != *root.TokenOrdinal || decoded.BlockID != block.ID || decoded.Range == nil || decoded.Page != nil || decoded.ReasonCode != "" || member != "" && decoded.ArtifactHash != member {
			return fail()
		}
		member = decoded.ArtifactHash
	}
	return nil
}
