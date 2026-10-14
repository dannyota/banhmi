package conversion

import (
	"reflect"
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/model"
)

func TestNormalizationReversal(t *testing.T) {
	for _, raw := range []string{"a\u0306\u0301\r\n  x___", "unchanged", "\t\r\n", ""} {
		input := model.TextInput{Text: raw, Sources: []model.Span{{ArtifactHash: digest([]byte(raw)), Space: model.RawText, Object: "text", ReasonCode: "BYTE_MAPPING_UNAVAILABLE"}}, Limits: model.RuleLimits{MaxItems: 20, MaxBytes: 4096}}
		value, findings, err := (TextNormalizer{}).Normalize(input)
		if err != nil || len(findings) != 0 {
			t.Fatalf("normalize: %v, findings %v", err, findings)
		}
		original, err := reverseText(value)
		if err != nil || original != raw {
			t.Fatalf("reversal failed: %v", err)
		}
		again, _, err := (TextNormalizer{}).Normalize(input)
		if err != nil || !reflect.DeepEqual(value, again) {
			t.Fatal("normalization is not deterministic")
		}
		for _, segment := range value.OffsetMap {
			if err := segment.Validate(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestNormalizationRefusesLostAnchorAndCorruptText(t *testing.T) {
	input := model.TextInput{Text: "x", Limits: model.RuleLimits{MaxItems: 20, MaxBytes: 4096}}
	if _, _, err := (TextNormalizer{}).Normalize(input); err == nil {
		t.Fatal("accepted lost anchor")
	}
	input.Sources = []model.Span{{ArtifactHash: digest([]byte("source")), Space: model.OriginalBytes, Range: &model.ByteRange{Start: 0, End: 1}}}
	for _, raw := range []string{string([]byte{0xff}), "\ufffd", "\ue001"} {
		input.Text = raw
		value, findings, err := (TextNormalizer{}).Normalize(input)
		if err == nil && (len(findings) == 0 || value.Text != raw) {
			t.Fatal("corruption was silently repaired")
		}
	}
	input.Text = strings.Repeat("x", 4097)
	if _, _, err := (TextNormalizer{}).Normalize(input); err == nil {
		t.Fatal("accepted excessive text")
	}
}

func TestReversalRejectsChangedReplacement(t *testing.T) {
	value := model.MappedValue{Text: "wrong", EditLedger: []model.DecodedEdit{{Kind: model.EditNormalize, Rule: NormalizationRuleNFCCRLF, Version: NormalizationVersionNFCCRLF, Removed: "a\u0301", Replacement: "á"}}}
	if _, err := reverseText(value); err == nil {
		t.Fatal("accepted irreversible ledger")
	}
}
