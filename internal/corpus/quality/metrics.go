package quality

import "danny.vn/banhmi/internal/corpus/model"

func unknownMetric(r model.MetricRequirement, reason string) model.QualityMetric {
	return model.QualityMetric{
		Kind:       r.Kind,
		State:      model.MetricUnknown,
		Threshold:  r.Threshold,
		Mandatory:  r.Mandatory,
		ReasonCode: reason,
		Evidence:   []model.AssetRef{},
		Exclusions: []model.Span{},
	}
}

func baselineReason(kind model.MetricKind) string {
	switch kind {
	case model.MetricGeometry, model.MetricFidelity:
		return "BASELINE_UNQUALIFIED"
	case model.MetricPageAccounting:
		return "PAGE_INVENTORY_UNAVAILABLE"
	case model.MetricTextCoverage:
		return "REGION_INVENTORY_UNAVAILABLE"
	case model.MetricTableIntegrity:
		return "TABLE_INVENTORY_UNAVAILABLE"
	case model.MetricMetadataAgreement:
		return "METADATA_UNAVAILABLE"
	case model.MetricStructureCompleteness:
		return "SOURCE_STRUCTURE_UNAVAILABLE"
	case model.MetricProvenanceCompleteness:
		return "PROVENANCE_VERIFICATION_UNAVAILABLE"
	case model.MetricDiacriticValidity, model.MetricCitationParse:
		return "LANGUAGE_RULES_UNQUALIFIED"
	case model.MetricCorruption:
		return "CORRUPTION_RULES_UNQUALIFIED"
	}
	return "BASELINE_UNQUALIFIED"
}
