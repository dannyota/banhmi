//go:build linux

package conversion

import (
	"testing"

	"danny.vn/banhmi/internal/corpus/model"
)

// TestEmittedNormalizationConstants checks the persisted ledger, decoded edit
// evidence and offset maps, and exercises every exported rule and transform.
func TestEmittedNormalizationConstants(t *testing.T) {
	original := []byte("<body><p>a\u0301\r\n</p><p>unchanged</p><p></p></body>")
	_, assets, document, _ := semanticFixture(t, original, "text/html")
	var ledger []model.EditLedgerEntry
	var maps []model.OffsetMapSegment
	if decodeJSON(assets.files[document.EditLedger.Path], &ledger) != nil || decodeJSON(assets.files[document.OffsetMap.Path], &maps) != nil {
		t.Fatal("invalid normalization contract fixture")
	}
	if len(ledger) == 0 {
		t.Fatal("fixture emitted no normalization rule")
	}
	for _, edit := range ledger {
		if edit.Rule != NormalizationRuleNFCCRLF || edit.Version != NormalizationVersionNFCCRLF {
			t.Fatal("ledger emitted an unexported rule or version")
		}
		var decoded model.DecodedEdit
		if decodeJSON(assets.files[edit.Evidence.Path], &decoded) != nil {
			t.Fatal("invalid decoded normalization evidence")
		}
		if decoded.Rule != NormalizationRuleNFCCRLF || decoded.Version != NormalizationVersionNFCCRLF {
			t.Fatal("decoded evidence emitted an unexported rule or version")
		}
	}
	seen := map[string]bool{}
	for _, segment := range maps {
		switch segment.Transform {
		case TransformSourceDecode, TransformIdentity, TransformNFCCRLF:
			seen[segment.Transform] = true
		default:
			t.Fatal("map emitted an unexported transform")
		}
	}
	for _, transform := range []string{TransformSourceDecode, TransformIdentity, TransformNFCCRLF} {
		if !seen[transform] {
			t.Fatalf("fixture did not emit transform %s", transform)
		}
	}
}
