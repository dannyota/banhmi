package model

import "testing"

func TestProvenanceVocabularies(t *testing.T) {
	for _, role := range []ConversionArtifactRole{ConversionRawBlocks, ConversionNormalizedBlocks, ConversionRawText, ConversionNormalizedText, ConversionManifest} {
		if !role.Valid() {
			t.Errorf("invalid conversion role %q", role)
		}
	}
	for _, space := range []ProvenanceSpace{ProvenanceOriginalBytes, ProvenanceMemberBytes, ProvenanceRawText, ProvenanceNormalizedText} {
		if !space.Valid() {
			t.Errorf("invalid provenance space %q", space)
		}
	}
	for _, kind := range []QualityRecordKind{QualityRecordMetric, QualityRecordFinding, QualityRecordGap} {
		if !kind.Valid() {
			t.Errorf("invalid quality record kind %q", kind)
		}
	}
	for _, outsider := range []string{"", "outsider"} {
		if ConversionArtifactRole(outsider).Valid() || ProvenanceSpace(outsider).Valid() || QualityRecordKind(outsider).Valid() {
			t.Errorf("admitted outsider %q", outsider)
		}
	}
}
