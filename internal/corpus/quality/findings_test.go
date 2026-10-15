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
