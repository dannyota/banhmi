package quality

import (
	"reflect"
	"testing"

	"danny.vn/banhmi/internal/corpus/model"
)

func TestGateFindingOrderAndCopies(t *testing.T) {
	g, request, refs := fixtureGate(t)
	first := request.Document.Blocks[0]
	first.NormalizedText = ""
	second := first
	second.ID = "second"
	second.NormalizedText = "a\u0301"
	second.SourceSpans = copySpans(first.SourceSpans)
	second.SourceSpans[0].BlockID = second.ID
	request.Document.Blocks = []model.DecodedBlock{first, second}
	request.Parts.Findings = []model.Finding{artifactFinding(request.Document.OriginalHash, "PRODUCER_ONE", model.SeverityInfo), artifactFinding(request.Document.OriginalHash, "PRODUCER_TWO", model.SeverityWarning)}
	request.Parts.Findings[0].Spans = nil
	request.Parts.Findings[0].Evidence = nil
	report, err := g.Report(request, refs)
	if err != nil {
		t.Fatal("ordered report failed")
	}
	codes := []string{}
	for _, f := range report.Artifact.Findings {
		codes = append(codes, f.ReasonCode)
		if f.Spans == nil || f.Evidence == nil {
			t.Fatal("report retained a null array")
		}
	}
	if !reflect.DeepEqual(codes, []string{"FIDELITY_UNQUALIFIED", "GEOMETRY_UNKNOWN", "EMPTY_TEXT_BLOCK", "TEXT_NOT_NFC", "TEXT_TOO_FEW_LETTERS", "PRODUCER_ONE", "PRODUCER_TWO"}) {
		t.Fatal("finding group order mismatch")
	}
	if request.Parts.Findings[0].Spans != nil || request.Parts.Findings[0].Evidence != nil {
		t.Fatal("producer arrays were materialized in place")
	}
	*report.Artifact.Findings[3].Spans[0].Range = model.ByteRange{Start: 1, End: 2}
	if request.Document.Blocks[1].SourceSpans[0].Range.Start != 0 {
		t.Fatal("finding span aliases producer input")
	}
}

// TestGateFindingNilArrays isolates normalization of each producer array.
func TestGateFindingNilArrays(t *testing.T) {
	for _, id := range []string{"nil-spans", "nil-evidence"} {
		t.Run(id, func(t *testing.T) {
			g, request, refs := fixtureGate(t)
			producer := artifactFinding(request.Document.OriginalHash, "PRODUCER", model.SeverityWarning)
			producer.Spans = copySpans(request.Document.Blocks[0].SourceSpans)
			producer.Evidence = []model.AssetRef{refs.Document}
			if id == "nil-spans" {
				producer.Spans = nil
			} else {
				producer.Evidence = nil
			}
			request.Parts.Findings = []model.Finding{producer}
			if request.Validate() != nil {
				t.Fatal("nil-array producer fixture invalid")
			}
			report, err := g.Report(request, refs)
			if err != nil || len(report.Artifact.Findings) != 3 {
				t.Fatal("nil-array producer report failed")
			}
			for _, finding := range report.Artifact.Findings {
				if finding.ReasonCode == "REPORT_LIMIT" {
					t.Fatal("nil-array producer became a limit report")
				}
			}
			got := report.Artifact.Findings[2]
			if got.Spans == nil || got.Evidence == nil || !reflect.DeepEqual(got, copyFinding(producer)) {
				t.Fatal("nil-array producer details changed")
			}
			if !reflect.DeepEqual(request.Parts.Findings[0], producer) {
				t.Fatal("nil-array producer input changed")
			}
		})
	}
}
