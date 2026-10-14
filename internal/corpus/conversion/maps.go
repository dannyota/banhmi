package conversion

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"danny.vn/banhmi/internal/corpus/conversion/api"
	"danny.vn/banhmi/internal/corpus/model"
)

// TextNormalizer computes NFC and CRLF normalization without I/O. It preserves
// indentation, form blanks and hyphens, and rejects suspicious input. Legacy
// encodings require qualified evidence and are not guessed by this baseline.
type TextNormalizer struct{}

var _ api.Normalizer = TextNormalizer{}

// Normalize returns a many-to-many whole-text map and, when changed, a complete
// reversible replacement. Input and output offsets count UTF-8 bytes. Keeping
// the entire changed range makes composition and CRLF contraction reversible
// without pretending that individual composed bytes have one-to-one origins.
func (TextNormalizer) Normalize(input model.TextInput) (model.MappedValue, []model.Finding, error) {
	if input.Limits.MaxBytes > 0 && int64(len(input.Text)) > input.Limits.MaxBytes {
		return model.MappedValue{}, nil, resourceFailure("output")
	}
	if input.Limits.MaxItems > 0 && len(input.Sources) > input.Limits.MaxItems {
		return model.MappedValue{}, nil, resourceFailure("items")
	}
	if input.Limits.Validate() != nil || len(input.Sources) == 0 {
		return model.MappedValue{}, nil, fmt.Errorf("normalize: INPUT_INVALID")
	}
	remaining := input.Limits.MaxBytes - int64(len(input.Text))
	for _, span := range input.Sources {
		if span.Validate() != nil {
			return model.MappedValue{}, nil, fmt.Errorf("normalize: INPUT_INVALID")
		}
		for _, value := range []string{string(span.ArtifactHash), span.BlockID, string(span.Space), span.Object, span.Member, span.ReasonCode} {
			if !utf8.ValidString(value) {
				return model.MappedValue{}, nil, fmt.Errorf("normalize: INPUT_INVALID")
			}
			if int64(len(value)) > remaining {
				return model.MappedValue{}, nil, resourceFailure("output")
			}
			remaining -= int64(len(value))
		}
	}
	var findings []model.Finding
	for _, r := range input.Text {
		if (r < 0x20 && r != '\t' && r != '\n' && r != '\r') || r == utf8.RuneError || (r >= 0xe000 && r <= 0xf8ff) || (r >= 0xf0000 && r <= 0xffffd) || (r >= 0x100000 && r <= 0x10fffd) {
			findings = append(findings, artifactFinding("TEXT_CORRUPT", input.Sources[0].ArtifactHash))
			break
		}
	}
	if len(findings) != 0 {
		return model.MappedValue{}, findings, fmt.Errorf("normalize: TEXT_CORRUPT")
	}
	text := norm.NFC.String(strings.ReplaceAll(input.Text, "\r\n", "\n"))
	if int64(len(text)) > input.Limits.MaxBytes {
		return model.MappedValue{}, findings, resourceFailure("output")
	}
	value := model.MappedValue{Text: text}
	destination := model.Span{ArtifactHash: digest([]byte(text)), Space: model.NormalizedText, Range: &model.ByteRange{Start: 0, End: int64(len(text))}}
	if text == "" {
		destination.Range = nil
		destination.Object = "text"
		destination.ReasonCode = "BYTE_MAPPING_UNAVAILABLE"
	}
	value.OffsetMap = []model.OffsetMapSegment{{Sources: append([]model.Span(nil), input.Sources...), Destinations: []model.Span{destination}, Transform: TransformIdentity, Ordinal: 0}}
	if text != input.Text {
		value.OffsetMap[0].Transform = TransformNFCCRLF
		value.EditLedger = []model.DecodedEdit{{Kind: model.EditNormalize, Rule: NormalizationRuleNFCCRLF, Version: NormalizationVersionNFCCRLF, Inputs: append([]model.Span(nil), input.Sources...), Outputs: []model.Span{destination}, Removed: input.Text, Replacement: text}}
	}
	if len(input.Sources)+len(value.OffsetMap)+len(value.EditLedger)+1 > input.Limits.MaxItems {
		return model.MappedValue{}, findings, resourceFailure("items")
	}
	if restored, err := reverseText(value); err != nil || restored != input.Text {
		return model.MappedValue{}, append(findings, artifactFinding("IRREVERSIBLE_EDIT", input.Sources[0].ArtifactHash)), fmt.Errorf("normalize: IRREVERSIBLE_EDIT")
	}
	return value, findings, nil
}

func reverseText(value model.MappedValue) (string, error) {
	text := value.Text
	for i := len(value.EditLedger) - 1; i >= 0; i-- {
		edit := value.EditLedger[i]
		if edit.Rule != NormalizationRuleNFCCRLF || edit.Version != NormalizationVersionNFCCRLF || edit.Kind != model.EditNormalize || text != edit.Replacement {
			return "", fmt.Errorf("reverse: IRREVERSIBLE_EDIT")
		}
		text = edit.Removed
	}
	return text, nil
}
