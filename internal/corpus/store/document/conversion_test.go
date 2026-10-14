package document

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
)

func TestConversionValidationBeforeTransaction(t *testing.T) {
	s := &forbiddenTransactionStarter{}
	c, err := NewConversion(s, "writer-a")
	if err != nil { t.Fatal(err) }
	g, target := adapterGrant(1), conversionAdapterTarget()
	ctx := context.Background()
	check := func(name string, operation func() error) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			var rejection *RejectionError
			if err := operation(); !errors.As(err, &rejection) { t.Fatalf("expected typed rejection: %v", err) }
			if s.calls != 0 { t.Fatal("invalid input opened a transaction") }
		})
	}
	check("target/revision", func() error { bad := target; bad.RevisionKey = "bad"; _, err := c.RegisterConversionArtifact(ctx, g, bad, conversionAdapterArtifact()); return err })
	check("target/candidate", func() error { bad := target; bad.CandidateDigest = "bad"; _, err := c.RegisterConversionArtifact(ctx, g, bad, conversionAdapterArtifact()); return err })
	check("constructor/writer", func() error { _, err := NewConversion(s, "BAD"); return err })
	check("constructor/starter", func() error { _, err := NewConversion(nil, "writer-a"); return err })
	for _, name := range []string{"role", "ordinal", "hash", "size", "object", "media", "capture-source", "capture-ref", "captured-time", "schema", "schema-nul", "original", "conversion-recipe", "normalization-recipe", "detector", "manifest", "manifest-empty", "manifest-bound", "manifest-role", "manifest-ordinal", "manifest-size", "manifest-hash", "manifest-duplicate", "membership"} {
		name := name
		check("artifact/"+name, func() error {
			v := conversionAdapterArtifact()
			switch name {
			case "role": v.Role = "unknown"
			case "ordinal": v.Ordinal = -1
			case "hash": v.Blob.SHA256 = "bad"
			case "size": v.Blob.ByteSize = -1
			case "object": v.Blob.ObjectRef = ""
			case "media": v.Blob.MediaType = ""
			case "capture-source": v.Blob.CaptureSource = ""
			case "capture-ref": v.Blob.CaptureRef = ""
			case "captured-time": v.Blob.CapturedAt = time.Time{}
			case "schema": v.SchemaVersion = ""
			case "schema-nul": v.SchemaVersion = "\x00"
			case "original": v.OriginalHash = "bad"
			case "conversion-recipe": v.ConversionRecipeDigest = "bad"
			case "normalization-recipe": v.NormalizationRecipeDigest = "bad"
			case "detector": v.DetectorDigest = "bad"
			case "manifest": v.Manifest.SHA256 = "bad"
			case "manifest-empty": v.Members = nil
			case "manifest-bound": v.Members = make([]ConversionArtifactMember, MaxConversionItems+1)
			case "manifest-role": v.Members[0].Role = "unknown"
			case "manifest-ordinal": v.Members[0].Ordinal = -1
			case "manifest-size": v.Members[0].ByteSize = -1
			case "manifest-hash": v.Members[0].Hash = "bad"
			case "manifest-duplicate": v.Members = append(v.Members, v.Members[0])
			case "membership": v.Members[0].Hash = conversionOtherDigest
			}
			_, err := c.RegisterConversionArtifact(ctx, g, target, v); return err
		})
	}
	for _, name := range []string{"empty", "bound", "duplicate", "map-hash", "order", "ordinal", "transform", "transform-nul", "space", "hash", "block", "token", "page", "page-overflow", "negative", "reversed", "beyond-size", "mapped-reason", "unavailable", "anchor", "anchor-nul", "pair"} {
		name := name
		check("segment/"+name, func() error {
			v, asset := conversionAdapterSegment(0), conversionAdapterAsset()
			items := []ConversionSegment{v}
			switch name {
			case "empty": items = nil
			case "bound": items = make([]ConversionSegment, MaxConversionItems+1)
			case "duplicate": items = append(items, v)
			case "map-hash": items[0].MapHash = "bad"
			case "order": items[0].TransformOrder = -1
			case "ordinal": items[0].Ordinal = -1
			case "transform": items[0].Transform = ""
			case "transform-nul": items[0].Transform = "\x00"
			case "space": items[0].Source.Space = "unknown"
			case "hash": items[0].Source.ArtifactHash = conversionOtherDigest
			case "block": items[0].Source.BlockID = ""
			case "token": zero := 0; items[0].Source.TokenOrdinal = &zero
			case "page": zero := 0; items[0].Source.Page = &zero
			case "page-overflow": large := int(2147483648); items[0].Source.Page = &large
			case "negative": items[0].Source.Range.Start = -1
			case "reversed": items[0].Source.Range.Start = 2
			case "beyond-size": items[0].Destination.Range.End = 17
			case "mapped-reason": items[0].Source.ReasonCode = "BYTE_MAPPING_UNAVAILABLE"
			case "unavailable": items[0].Source.Range = nil; items[0].Source.Object = "body"
			case "anchor": items[0].Source.Range = nil; items[0].Source.ReasonCode = "BYTE_MAPPING_UNAVAILABLE"
			case "anchor-nul": items[0].Source.Object = "\x00"
			case "pair": asset.ByteSize = -1
			}
			_, err := c.InsertOffsetMapSegments(ctx, g, target, asset, conversionAdapterAsset(), items); return err
		})
	}
	for _, name := range []string{"kind", "ordinal", "ledger", "rule", "version", "rule-nul", "input", "output", "negative", "reversed", "bounds", "length", "insert-side", "delete-side", "bytes", "encoding", "font", "anchors", "unicode", "numeric", "payload"} {
		name := name
		check("ledger/"+name, func() error {
			v := conversionAdapterEdit()
			switch name {
			case "kind": v.Operation = "unknown"
			case "ordinal": v.Ordinal = -1
			case "ledger": v.LedgerHash = "bad"
			case "rule": v.Rule = ""
			case "version": v.RuleVersion = ""
			case "rule-nul": v.Rule = "\x00"
			case "input": v.Input.SHA256 = "bad"
			case "output": v.Output.Path = "../escape"
			case "negative": v.InputRange.Start = -1
			case "reversed": v.OutputRange.Start = 2
			case "bounds": v.InputRange.End = 17
			case "length": v.RemovedBytes = nil
			case "insert-side": v.Operation = model.EditInsert
			case "delete-side": v.Operation = model.EditDelete
			case "bytes": v.RemovedBytes = make([]byte, MaxConversionPayloadBytes/4+1); v.InputRange.End = int64(len(v.RemovedBytes)); v.Input.ByteSize = v.InputRange.End
			case "encoding": v.EncodingEvidence = []byte(`[]`)
			case "font": v.FontEvidence = []byte(`null`)
			case "anchors": v.SourceAnchors = []byte(`{}`)
			case "unicode": v.SourceAnchors = []byte(`["\ud800"]`)
			case "numeric": v.EncodingEvidence = []byte(`{"n":1e131072}`)
			case "payload": v.Rule = strings.Repeat("a", MaxConversionPayloadBytes+1)
			}
			_, err := c.AppendEditLedgerEntry(ctx, g, target, v); return err
		})
	}
	for _, name := range []string{"outcome", "policy", "threshold", "count", "bound", "ordinal", "duplicate", "kind", "code", "severity", "scope", "scope-key", "reason", "unexpected-metric", "evidence", "spans", "json-nul", "artifact"} {
		name := name
		check("report/"+name, func() error {
			v := conversionAdapterReport()
			switch name {
			case "outcome": v.Outcome = "unknown"
			case "policy": v.PolicyDigest = "bad"
			case "threshold": v.ThresholdVersion = ""
			case "count": v.RecordCount = 2
			case "bound": v.RecordCount = MaxConversionItems+1; v.Records = make([]ConversionQualityRecord, MaxConversionItems+1)
			case "ordinal": v.Records[0].Ordinal = -1
			case "duplicate": v.RecordCount = 2; v.Records = append(v.Records, v.Records[0])
			case "kind": v.Records[0].Kind = "unknown"
			case "code": v.Records[0].Code = ""
			case "severity": bad := model.Severity("unknown"); v.Records[0].Severity = &bad
			case "scope": bad := model.GapKind("unknown"); v.Records[0].Scope = &bad
			case "scope-key": v.Records[0].ScopeKey = nil
			case "reason": v.Records[0].Reason = nil
			case "unexpected-metric": bad := model.MetricGeometry; v.Records[0].MetricKind = &bad
			case "evidence": v.Records[0].Evidence = []byte(`{}`)
			case "spans": v.Records[0].SourceSpans = []byte(`null`)
			case "json-nul": v.Records[0].SourceSpans = []byte(`["\u0000"]`)
			case "artifact": bad := model.Digest("bad"); v.ArtifactHash = &bad
			}
			_, err := c.RecordConversionQualityReport(ctx, g, target, v); return err
		})
	}
	for _, name := range []string{"prior-id", "candidate-id", "watermark", "same-digest", "identity", "digest", "recipe", "result"} {
		name := name
		check("comparison/"+name, func() error {
			v := ConversionComparisonInput{PriorReportID: 1, CandidateReportID: 2, PriorRevisionKey: revisionKey, PriorCandidateDigest: conversionOtherDigest, RecipeDigest: identityDigest, Result: conversionAdapterBlob(identityDigest)}
			switch name {
			case "prior-id": v.PriorReportID = 0
			case "candidate-id": v.CandidateReportID = 0
			case "watermark": v.AcceptanceWatermark = -1
			case "same-digest": v.PriorCandidateDigest = identityDigest
			case "identity": v.PriorRevisionKey = otherIdentityKey+":rev:"+identityDigest
			case "digest": v.PriorCandidateDigest = "bad"
			case "recipe": v.RecipeDigest = "bad"
			case "result": v.Result.ByteSize = -1
			}
			_, err := c.RecordRevisionComparison(ctx, g, target, v); return err
		})
	}
	for _, operation := range []func() error{
		func() error { _, err := c.ReadConversionArtifacts(ctx, g, revisionKey, model.ConversionRawText, 0, MaxConversionPageItems+1); return err },
		func() error { _, err := c.ReadOffsetMapSegments(ctx, g, revisionKey, "", 0, 1); return err },
		func() error { _, err := c.ReadEditLedgerByHashes(ctx, g, target, "bad", identityDigest, 0, 1); return err },
		func() error { _, err := c.ReadConversionQualityReports(ctx, g, target, -1, 1); return err },
		func() error { _, err := c.ReadConversionQualityFindings(ctx, g, target, 0, 0, 1); return err },
		func() error { _, err := c.ReadRevisionComparisons(ctx, g, identityDigest, conversionOtherDigest, 0, 0); return err },
	} { var rejection *RejectionError; if err := operation(); !errors.As(err, &rejection) || s.calls != 0 { t.Fatalf("invalid page opened a transaction: %v", err) } }
}

func TestConversionSpanTranslationAndEmptyRanges(t *testing.T) {
	asset := conversionAdapterAsset()
	for input, expected := range map[model.CoordinateSpace]model.ProvenanceSpace{model.OriginalBytes: model.ProvenanceOriginalBytes, model.DecodedMember: model.ProvenanceMemberBytes, model.RawText: model.ProvenanceRawText, model.NormalizedText: model.ProvenanceNormalizedText} {
		span := model.Span{ArtifactHash: identityDigest, BlockID: "block-a", Space: input, Range: &model.ByteRange{Start: 16, End: 16}}
		p, err := conversionSpan(span, asset)
		if err != nil || p["space"] != expected { t.Fatalf("coordinate translation: %v", err) }
		span.Range = nil; span.ReasonCode = "BYTE_MAPPING_UNAVAILABLE"; page := 1; span.Page = &page
		p, err = conversionSpan(span, asset)
		if err != nil { t.Fatal(err) }
		raw, err := conversionEncode(p); if err != nil { t.Fatal(err) }
		var stored map[string]any
		if err := json.Unmarshal(raw, &stored); err != nil || stored["start"] != nil || stored["end"] != nil { t.Fatalf("invented unavailable byte range: %v", err) }
	}
}

func TestConversionReceiptJSONEquality(t *testing.T) {
	stored := ConversionReceipt{ID: 7, Payload: []byte(`{"id":7,"document_version_key":"version-a","encoding_evidence":{"a":1,"b":2},"source_anchors":[{"page":1,"object":"body"}]}`)}
	for _, payload := range []string{
		`{"encoding_evidence":{"a":1},"source_anchors":[{"page":1,"object":"body"}]}`,
		`{"encoding_evidence":{"a":1,"b":2},"source_anchors":[{"page":1}]}`,
	} {
		if conversionMatch(stored, []byte(payload), "version-a") { t.Fatal("changed nested JSON classified as an identical replay") }
	}
	if !conversionMatch(stored, []byte(`{"encoding_evidence":{"b":2.0,"a":1e0},"source_anchors":[{"object":"body","page":1}]}`), "version-a") { t.Fatal("JSONB numeric equivalence changed replay classification") }
}

func TestConversionMetricValidationBeforeTransaction(t *testing.T) {
	s := &forbiddenTransactionStarter{}
	c, err := NewConversion(s, "writer-a"); if err != nil { t.Fatal(err) }
	for _, name := range []string{"severity", "scope", "scope-key", "kind", "state", "numerator", "denominator", "threshold", "mandatory", "passed", "negative-numerator", "negative-denominator", "zero-denominator", "unknown-reason", "unknown-pass", "unknown-score", "inapplicable-pass"} {
		t.Run(name, func(t *testing.T) {
			kind, state, n, d, threshold, mandatory, passed := model.MetricGeometry, model.MetricMeasured, int64(1), int64(1), "1", true, true
			r := ConversionQualityRecord{Kind: model.QualityRecordMetric, Code: "GEOMETRY", MetricKind: &kind, MetricState: &state, Numerator: &n,
				Denominator: &d, Threshold: &threshold, Mandatory: &mandatory, Passed: &passed, Evidence: []byte(`[]`), SourceSpans: []byte(`[]`)}
			switch name {
			case "severity": severity := model.SeverityInfo; r.Severity = &severity
			case "scope": scope := model.GapArtifact; r.Scope = &scope
			case "scope-key": key := "artifact-a"; r.ScopeKey = &key
			case "kind": r.MetricKind = nil
			case "state": state = "unknown-state"
			case "numerator": r.Numerator = nil
			case "denominator": r.Denominator = nil
			case "threshold": r.Threshold = nil
			case "mandatory": r.Mandatory = nil
			case "passed": r.Passed = nil
			case "negative-numerator": n = -1
			case "negative-denominator": d = -1
			case "zero-denominator": d = 0
			case "unknown-reason": state = model.MetricUnknown; n, d, passed = 0, 0, false
			case "unknown-pass": state = model.MetricUnknown; reason := "NO_INPUT"; r.Reason = &reason; n, d = 0, 0
			case "unknown-score": state = model.MetricUnknown; reason := "NO_INPUT"; r.Reason = &reason; passed = false
			case "inapplicable-pass": state = model.MetricNotApplicable; reason := "NO_INPUT"; r.Reason = &reason; n, d = 0, 0
			}
			v := conversionAdapterReport(); v.Records = []ConversionQualityRecord{r}
			_, err := c.RecordConversionQualityReport(context.Background(), adapterGrant(1), conversionAdapterTarget(), v)
			var rejection *RejectionError
			if !errors.As(err, &rejection) || s.calls != 0 { t.Fatalf("invalid metric opened transaction: %v", err) }
		})
	}
}
