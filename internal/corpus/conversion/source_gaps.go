//go:build linux

package conversion

import (
	"encoding/json"
	"fmt"

	"danny.vn/banhmi/internal/corpus/conversion/sandbox"
	"danny.vn/banhmi/internal/corpus/model"
)

// sourceGaps inventories claims only. It never constructs converted blocks,
// normalization, maps, ledgers or converter reports. A multiset binds every
// occurrence, including two regions with the same reason, without prescribing
// the adapter's incidental ordering of independent findings.
type sourceGaps struct {
	hash     model.Digest
	limit    int
	findings []model.Finding
	overflow bool
}

func (g *sourceGaps) add(reason string, spans ...model.Span) {
	if g == nil || g.overflow {
		return
	}
	if len(g.findings) >= g.limit {
		g.overflow = true
		return
	}
	g.findings = append(g.findings, model.Finding{ReasonCode: reason, Severity: model.SeverityBlocking, Scope: model.GapArtifact, ScopeKey: string(g.hash), Spans: spans})
}

func (g *sourceGaps) err() error {
	if g != nil && g.overflow {
		return resourceFailure("items")
	}
	return nil
}

func validateSourceClaims(original []byte, document model.ConvertedDocument, quality qualityReport, m ConversionManifest, limits sandbox.Limits) error {
	g := &sourceGaps{hash: m.Request.Artifact.Asset.SHA256, limit: m.Request.Budget.MaxItems}
	if err := validateSourceDerivation(original, document, m, limits, g); err != nil {
		return err
	}
	g.add("GEOMETRY_UNKNOWN")
	g.add("FIDELITY_UNQUALIFIED")
	if err := g.err(); err != nil {
		return err
	}
	fail := func() error { return fmt.Errorf("validate: SOURCE_FINDING_MISMATCH") }
	if len(quality.Findings) != len(g.findings) {
		return fail()
	}
	expected := make(map[string]int, len(g.findings))
	for _, finding := range g.findings {
		data, err := json.Marshal(finding)
		if err != nil {
			return fail()
		}
		expected[string(data)]++
	}
	for _, finding := range quality.Findings {
		data, err := json.Marshal(finding)
		if err != nil || expected[string(data)] == 0 {
			return fail()
		}
		expected[string(data)]--
	}
	return nil
}
