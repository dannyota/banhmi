package residency

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"

	"danny.vn/banhmi/internal/corpus/conversion"
	"danny.vn/banhmi/internal/corpus/model"
)

func conversionBounds(request conversion.ConversionManifest, outputs model.AssetRef) error {
	if request.Request.Artifact.Asset.ByteSize > request.Request.Budget.MaxInputBytes || outputs.ByteSize > min(request.OutputBytes, 1<<20) {
		return limitError
	}
	return nil
}

// conversionChains reconstructs the frozen baseline from authenticated blocks.
// Only expected source-decode dependencies can reach members through spans.
func (c *objectClosure) conversionChains(request conversion.ConversionManifest, document model.ConvertedDocument, format string, maps []model.OffsetMapSegment, edits []model.EditLedgerEntry, members map[string]model.AssetRef, member func(model.AssetRef) error) error {
	var expectedMaps []model.OffsetMapSegment
	var expectedEdits []model.EditLedgerEntry
	var raw, normalized []byte
	var decodedMember model.Digest
	object := func(data []byte) (model.AssetRef, error) {
		ref := model.AssetRef{Path: "objects/" + string(hash(data)), SHA256: hash(data), ByteSize: int64(len(data))}
		if err := member(ref); err != nil {
			return ref, err
		}
		actual, err := c.resolve(ref)
		if err != nil || !bytes.Equal(actual, data) {
			return ref, bindingError
		}
		return ref, nil
	}
	for n, block := range document.Blocks {
		if err := contextError(c.ctx); err != nil {
			return err
		}
		if block.Ordinal != n || block.ParentID != "" || block.Language != request.Language || block.Authority != model.AuthorityUnknown || block.Content != nil || block.Findings != nil || len(block.SourceSpans) == 0 {
			return bindingError
		}
		if err := conversionSourceRoots(format, document.Original.Asset, block, &decodedMember, members, member); err != nil {
			return err
		}
		rawRef, err := object([]byte(block.RawText))
		if err != nil {
			return err
		}
		rawSpan := model.Span{ArtifactHash: rawRef.SHA256, Space: model.RawText, Object: "text", ReasonCode: "BYTE_MAPPING_UNAVAILABLE"}
		if rawRef.ByteSize != 0 {
			rawSpan.Range = &model.ByteRange{Start: 0, End: rawRef.ByteSize}
			rawSpan.ReasonCode = ""
		}
		value, findings, err := (conversion.TextNormalizer{}).Normalize(model.TextInput{Text: block.RawText, Sources: []model.Span{rawSpan}, Limits: model.RuleLimits{MaxItems: request.Request.Budget.MaxItems, MaxBytes: request.OutputBytes}})
		if err != nil || len(findings) != 0 {
			return bindingError
		}
		normalRef, err := object([]byte(value.Text))
		if err != nil || normalRef != block.NormalizedText {
			return bindingError
		}
		canonical, err := json.Marshal([]string{"block-v1", string(block.Kind), block.RawText, value.Text})
		if err != nil || block.ID != fmt.Sprintf("block-%06d-%s", n, hash(canonical)) {
			return bindingError
		}
		expectedMaps = append(expectedMaps, model.OffsetMapSegment{Sources: block.SourceSpans, Destinations: []model.Span{rawSpan}, Transform: conversion.TransformSourceDecode, Ordinal: len(expectedMaps)})
		for _, segment := range value.OffsetMap {
			segment.Ordinal = len(expectedMaps)
			expectedMaps = append(expectedMaps, segment)
		}
		for _, edit := range value.EditLedger {
			removed, err := object([]byte(edit.Removed))
			if err != nil {
				return err
			}
			replacement, err := object([]byte(edit.Replacement))
			if err != nil {
				return err
			}
			data, err := json.Marshal(edit)
			if err != nil {
				return bindingError
			}
			evidence, err := object(data)
			if err != nil {
				return err
			}
			expectedEdits = append(expectedEdits, model.EditLedgerEntry{Kind: edit.Kind, Ordinal: len(expectedEdits), Rule: edit.Rule, Version: edit.Version, Inputs: edit.Inputs, Outputs: edit.Outputs, Removed: &removed, Replacement: &replacement, Evidence: evidence})
		}
		if int64(len(block.RawText)) > document.RawText.ByteSize-int64(len(raw)) || int64(len(value.Text)) > document.NormalizedText.ByteSize-int64(len(normalized)) {
			return bindingError
		}
		raw = append(raw, block.RawText...)
		normalized = append(normalized, value.Text...)
	}
	if len(document.Blocks) == 0 || !reflect.DeepEqual(maps, expectedMaps) || !reflect.DeepEqual(edits, expectedEdits) || !bytes.Equal(raw, c.objects[document.RawText.SHA256]) || !bytes.Equal(normalized, c.objects[document.NormalizedText.SHA256]) {
		return bindingError
	}
	return nil
}

func conversionSourceRoots(format string, original model.AssetRef, block model.Block, decodedMember *model.Digest, members map[string]model.AssetRef, member func(model.AssetRef) error) error {
	if format == "html" {
		for _, span := range block.SourceSpans {
			if span.Validate() != nil || span.Space != model.OriginalBytes || span.ArtifactHash != original.SHA256 || span.Member != "" || span.Range == nil || span.Range.End > original.ByteSize || span.BlockID != block.ID || span.Page != nil || span.Object != "" || span.ReasonCode != "" || span.TokenOrdinal == nil {
				return bindingError
			}
		}
		return nil
	}
	if format != "docx" || len(block.SourceSpans) != 2 {
		return bindingError
	}
	root, decoded := block.SourceSpans[0], block.SourceSpans[1]
	if root.Validate() != nil || root.Space != model.OriginalBytes || root.ArtifactHash != original.SHA256 || root.Member != "word/document.xml" || root.Range != nil || root.ReasonCode != "BYTE_MAPPING_UNAVAILABLE" || root.Object != fmt.Sprintf("paragraph/%d", block.Ordinal) || root.TokenOrdinal == nil || root.BlockID != block.ID || root.Page != nil {
		return bindingError
	}
	if decoded.Validate() != nil || decoded.Space != model.DecodedMember || decoded.Member != root.Member || decoded.Object != root.Object || decoded.TokenOrdinal == nil || *decoded.TokenOrdinal != *root.TokenOrdinal || decoded.BlockID != block.ID || decoded.Range == nil || decoded.Page != nil || decoded.ReasonCode != "" || (*decodedMember != "" && decoded.ArtifactHash != *decodedMember) {
		return bindingError
	}
	*decodedMember = decoded.ArtifactHash
	// The output inventory, not this span, supplies the reference and size.
	ref, ok := members["objects/"+string(decoded.ArtifactHash)]
	if !ok || decoded.Range.End > ref.ByteSize {
		return bindingError
	}
	return member(ref)
}

func conversionSpans(value any, original model.AssetRef, members map[string]model.AssetRef, pages int) error {
	var walk func(reflect.Value) error
	walk = func(v reflect.Value) error {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return nil
			}
			return walk(v.Elem())
		}
		if v.CanInterface() {
			if span, ok := v.Interface().(model.Span); ok {
				if span.Page != nil && *span.Page > pages {
					return limitError
				}
				ref := original
				if span.ArtifactHash != original.SHA256 {
					var ok bool
					ref, ok = members["objects/"+string(span.ArtifactHash)]
					if !ok {
						return bindingError
					}
				}
				if span.Validate() != nil || span.Range != nil && span.Range.End > ref.ByteSize {
					return bindingError
				}
			}
		}
		switch v.Kind() {
		case reflect.Struct:
			for n := 0; n < v.NumField(); n++ {
				if err := walk(v.Field(n)); err != nil {
					return err
				}
			}
		case reflect.Slice:
			for n := 0; n < v.Len(); n++ {
				if err := walk(v.Index(n)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(reflect.ValueOf(value))
}
