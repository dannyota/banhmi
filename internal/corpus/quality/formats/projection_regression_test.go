package formats

import (
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/model"
)

func TestProjectionSeparatesLogicalAndCatalogPaths(t *testing.T) {
	limits := DefaultLimits()
	a := model.AssetRef{Path: "p", SHA256: model.Digest(strings.Repeat("a", 64)), ByteSize: 1}
	b := model.AssetRef{Path: "p", SHA256: model.Digest(strings.Repeat("b", 64)), ByteSize: 1}
	physicalA := a
	physicalA.Path = "q"
	span := model.Span{ArtifactHash: b.SHA256, Space: model.OriginalBytes, Range: &model.ByteRange{Start: 0, End: 1}}
	finding := model.Finding{ReasonCode: "SYNTHETIC", Severity: model.SeverityWarning, Scope: model.GapArtifact, ScopeKey: "artifact-1", Spans: []model.Span{span}, Evidence: []model.AssetRef{a}}
	project := func(catalog []model.AssetRef, bounds Limits) ([]QualityRow, error) {
		return ProjectQualityRows([]model.QualityMetric{}, []model.Finding{finding}, []Gap{}, catalog, bounds)
	}
	rows, err := project([]model.AssetRef{physicalA, b}, limits)
	if err != nil || len(rows) != 1 || len(rows[0].Evidence) != 2 || rows[0].Evidence[0] != physicalA || rows[0].Evidence[1] != b {
		t.Fatal("logical alias collided with unrelated catalog path", err)
	}
	if finding.Evidence[0] != a {
		t.Fatal("catalog resolution rewrote logical evidence")
	}
	if _, err := project([]model.AssetRef{a, b}, limits); err == nil {
		t.Fatal("conflicting catalog paths accepted")
	}
	finding.Evidence = append(finding.Evidence, b)
	if _, err := project([]model.AssetRef{physicalA, b}, limits); err == nil {
		t.Fatal("conflicting logical paths accepted")
	}
	finding.Evidence = []model.AssetRef{a}
	wrongSize := physicalA
	wrongSize.ByteSize++
	if _, err := project([]model.AssetRef{wrongSize, b}, limits); err == nil {
		t.Fatal("conflicting hash sizes across namespaces accepted")
	}
	limits.MaxBlobs = 1
	if _, err := project([]model.AssetRef{physicalA, b}, limits); err == nil {
		t.Fatal("aggregate blob bound split by namespace")
	}
	limits = DefaultLimits()
	limits.MaxObjectBytes = 1
	if _, err := project([]model.AssetRef{physicalA, b}, limits); err == nil {
		t.Fatal("aggregate object bound split by namespace")
	}
	limits = DefaultLimits()
	limits.MaxRows = 4 // Finding, span, evidence and two catalog entries need five.
	if _, err := project([]model.AssetRef{physicalA, b}, limits); err == nil {
		t.Fatal("aggregate row bound split by namespace")
	}
}

func TestProjectedSpanRejectsInvalidText(t *testing.T) {
	asset := model.AssetRef{Path: "p", SHA256: model.Digest(strings.Repeat("a", 64)), ByteSize: 1}
	valid := model.Span{ArtifactHash: asset.SHA256, Space: model.OriginalBytes, Object: "p", ReasonCode: "BYTE_MAPPING_UNAVAILABLE"}
	if _, err := ProjectReportSpan(valid, asset); err != nil {
		t.Fatal("valid unavailable-range anchor rejected", err)
	}
	for _, field := range []string{"object", "member", "reason"} {
		for _, invalid := range []string{"p\x00", "p\xff"} {
			t.Run(field+"/"+invalid, func(t *testing.T) {
				span := valid
				switch field {
				case "object":
					span.Object = invalid
				case "member":
					span.Member = invalid
				case "reason":
					span.ReasonCode = invalid
				}
				if _, err := ProjectReportSpan(span, asset); err == nil {
					t.Fatal("invalid writer text accepted by direct projection")
				}
				finding := model.Finding{ReasonCode: "SYNTHETIC", Severity: model.SeverityWarning, Scope: model.GapArtifact, ScopeKey: "artifact-1", Spans: []model.Span{span}, Evidence: []model.AssetRef{}}
				if _, err := ProjectQualityRows([]model.QualityMetric{}, []model.Finding{finding}, []Gap{}, []model.AssetRef{asset}, DefaultLimits()); err == nil {
					t.Fatal("invalid writer text accepted by row projection")
				}
			})
		}
	}
}
