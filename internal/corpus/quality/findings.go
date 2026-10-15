package quality

import (
	"strings"

	"danny.vn/banhmi/internal/corpus/model"
)

func artifactFinding(original model.Digest, code string, severity model.Severity) model.Finding {
	return model.Finding{
		ReasonCode: code,
		Severity:   severity,
		Scope:      model.GapArtifact,
		ScopeKey:   string(original),
		Spans:      []model.Span{},
		Evidence:   []model.AssetRef{},
	}
}

func unknownFinding(original model.Digest, r model.MetricRequirement) model.Finding {
	code := strings.ToUpper(string(r.Kind)) + "_UNKNOWN"
	if r.Kind == model.MetricFidelity {
		code = "FIDELITY_UNQUALIFIED"
	}
	severity := model.SeverityWarning
	if r.Mandatory {
		severity = model.SeverityBlocking
	}
	return artifactFinding(original, code, severity)
}

func copySpans(spans []model.Span) []model.Span {
	result := make([]model.Span, len(spans))
	for i, span := range spans {
		if span.Range != nil {
			value := *span.Range
			span.Range = &value
		}
		if span.Page != nil {
			value := *span.Page
			span.Page = &value
		}
		if span.TokenOrdinal != nil {
			value := *span.TokenOrdinal
			span.TokenOrdinal = &value
		}
		result[i] = span
	}
	return result
}

func copyFinding(f model.Finding) model.Finding {
	f.Spans = copySpans(f.Spans)
	f.Evidence = append(make([]model.AssetRef, 0, len(f.Evidence)), f.Evidence...)
	return f
}

func textBearing(kind model.BlockKind) bool {
	return kind == model.BlockHeading || kind == model.BlockParagraph || kind == model.BlockListItem || kind == model.BlockTable
}
