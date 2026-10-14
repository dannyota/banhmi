package model

// ConversionArtifactRole identifies an immutable conversion output, not a source role.
type ConversionArtifactRole string

const (
	ConversionRawBlocks        ConversionArtifactRole = "raw_blocks"
	ConversionNormalizedBlocks ConversionArtifactRole = "normalized_blocks"
	ConversionRawText          ConversionArtifactRole = "raw_text"
	ConversionNormalizedText   ConversionArtifactRole = "normalized_text"
	ConversionManifest         ConversionArtifactRole = "output_manifest"
)

// Valid reports membership in the persistence vocabulary.
func (r ConversionArtifactRole) Valid() bool {
	switch r {
	case ConversionRawBlocks, ConversionNormalizedBlocks, ConversionRawText, ConversionNormalizedText, ConversionManifest:
		return true
	default:
		return false
	}
}

// ProvenanceSpace is the database coordinate vocabulary. Frozen interchange
// CoordinateSpace values require explicit translation at the adapter boundary.
type ProvenanceSpace string

const (
	ProvenanceOriginalBytes  ProvenanceSpace = "original_bytes"
	ProvenanceMemberBytes    ProvenanceSpace = "member_bytes"
	ProvenanceRawText        ProvenanceSpace = "raw_text"
	ProvenanceNormalizedText ProvenanceSpace = "normalized_text"
)

// Valid reports membership in the persistence vocabulary.
func (s ProvenanceSpace) Valid() bool {
	return s == ProvenanceOriginalBytes || s == ProvenanceMemberBytes || s == ProvenanceRawText || s == ProvenanceNormalizedText
}

// QualityRecordKind discriminates queryable metrics, findings and explicit gaps
// in the report's child relation. The complete evidence remains a hashed blob.
type QualityRecordKind string

const (
	QualityRecordMetric  QualityRecordKind = "metric"
	QualityRecordFinding QualityRecordKind = "finding"
	QualityRecordGap     QualityRecordKind = "gap"
)

// Valid reports membership in the persistence vocabulary.
func (k QualityRecordKind) Valid() bool {
	return k == QualityRecordMetric || k == QualityRecordFinding || k == QualityRecordGap
}
