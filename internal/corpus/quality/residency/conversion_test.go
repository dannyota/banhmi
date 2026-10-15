package residency

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/conversion"
	"danny.vn/banhmi/internal/corpus/model"
)

type conversionDamage struct {
	aggregate       bool
	normalAggregate bool
	unused          bool
	request         func(*conversion.ConversionManifest)
	block           func(*model.Block)
	chain           func(*[]model.OffsetMapSegment, *[]model.EditLedgerEntry)
	quality         func(*conversionQuality)
	outputs         func([]byte) []byte
	recipe          []byte
	manifest        func([]byte) []byte
}

func TestResidencyConversionPolicyPathCollision(t *testing.T) {
	baseline, _ := convertedRequest(t)
	r, objects := convertedFixture(t, "text/html", "abc", conversionDamage{
		request: func(m *conversion.ConversionManifest) { m.Request.Recipe.Path = baseline.Policy.Path },
	})
	report, err := testEngine(t).Evaluate(context.Background(), r, objects)
	if err != bindingError || !reflect.DeepEqual(report, ResidencyReport{}) {
		t.Fatal("conversion recipe policy path collision")
	}
}

func TestResidencyConversionFixedInputBounds(t *testing.T) {
	for _, size := range []int{64 << 10, (64 << 10) + 1} {
		for _, kind := range []string{"request", "recipe", "malformed-request"} {
			name := kind + "-exact"
			if size > 64<<10 {
				name = kind + "-over"
			}
			t.Run(name, func(t *testing.T) {
				damage := conversionDamage{}
				switch kind {
				case "request":
					damage.request = func(m *conversion.ConversionManifest) {
						data, err := json.Marshal(m)
						if err != nil || len(data) > size {
							t.Fatal("conversion request padding setup")
						}
						m.ObservationKey += strings.Repeat("x", size-len(data))
					}
				case "recipe":
					damage.recipe = bytes.Repeat([]byte("x"), size)
				case "malformed-request":
					damage.manifest = func([]byte) []byte { return bytes.Repeat([]byte(" "), size) }
				}
				r, objects := convertedFixture(t, "text/html", "abc", damage)
				if kind == "request" && r.Evidence[0].Conversion.Request.ByteSize != int64(size) {
					t.Fatal("conversion request padding size")
				}
				var want error
				if size > 64<<10 {
					want = limitError
				} else if kind == "malformed-request" {
					want = inputError
				}
				report, err := testEngine(t).Evaluate(context.Background(), r, objects)
				if err != want || err != nil && !reflect.DeepEqual(report, ResidencyReport{}) {
					t.Fatal("conversion fixed input bound")
				}
			})
		}
	}
}

func TestResidencyConvertedEvidence(t *testing.T) {
	r, objects := convertedRequest(t)
	e := testEngine(t)
	installProof(t, e, r)
	report, err := e.Evaluate(context.Background(), r, objects)
	if err != nil || report.Complete || report.Class != model.ResidencyUnclassified || report.ReasonCode != "INCOMPLETE_SURFACE" || report.AdmissionProof != nil {
		t.Fatal("converted result")
	}
	for n, mutate := range []func(*ScreenRequest){
		func(r *ScreenRequest) { r.Evidence[0].SubjectHash = hash([]byte("other")) },
		func(r *ScreenRequest) { r.Evidence[0].Asset = r.Subject },
		func(r *ScreenRequest) { r.Evidence[0].Conversion.Document = r.Evidence[0].Conversion.Request },
		func(r *ScreenRequest) { r.Evidence[0].Conversion.Outputs.ByteSize++ },
		func(r *ScreenRequest) { r.Evidence[0].Conversion = nil },
	} {
		changed := r
		changed.Evidence = append([]ScreenEvidence(nil), r.Evidence...)
		conversion := *r.Evidence[0].Conversion
		changed.Evidence[0].Conversion = &conversion
		mutate(&changed)
		got, err := e.Evaluate(context.Background(), changed, objects)
		if err == nil || !reflect.DeepEqual(got, ResidencyReport{}) {
			t.Fatalf("converted-%d binding", n)
		}
	}
	r, objects = convertedRequestMedia(t, "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	installProof(t, e, r)
	report, err = e.Evaluate(context.Background(), r, objects)
	if err != nil || report.Complete || report.Class != model.ResidencyUnclassified || report.AdmissionProof != nil {
		t.Fatal("converted-docx result")
	}
}

func TestResidencyConversionChains(t *testing.T) {
	for _, media := range []string{"text/html", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"} {
		for _, text := range []string{"abc", "a\u0301\r\n", "a@example.invalid"} {
			r, objects := convertedFixture(t, media, text, conversionDamage{})
			report, err := testEngine(t).Evaluate(context.Background(), r, objects)
			if err != nil || report.Complete || report.AdmissionProof != nil {
				t.Fatal("chain valid control")
			}
			if text == "a@example.invalid" && (report.Class != model.ResidencyPersonal || report.Counts[0].Matches == 0) {
				t.Fatal("chain signal control")
			}
		}
	}
	for _, fixture := range []struct {
		name   string
		text   string
		damage conversionDamage
	}{
		{"aggregate-omits-signal", "a@example.invalid", conversionDamage{aggregate: true}},
		{"normalized-aggregate-omits-signal", "a@example.invalid", conversionDamage{normalAggregate: true}},
		{"unused-member", "abc", conversionDamage{unused: true}},
		{"block-identity", "abc", conversionDamage{block: func(b *model.Block) {
			b.ID = "block-0"
			b.SourceSpans[0].BlockID = b.ID
		}}},
		{"source-decode-omitted", "abc", conversionDamage{chain: func(m *[]model.OffsetMapSegment, _ *[]model.EditLedgerEntry) { *m = (*m)[1:] }}},
		{"map-destination", "abc", conversionDamage{chain: func(m *[]model.OffsetMapSegment, _ *[]model.EditLedgerEntry) { (*m)[1].Destinations[0].Range.End-- }}},
		{"ledger-omitted", "a\u0301\r\n", conversionDamage{chain: func(_ *[]model.OffsetMapSegment, e *[]model.EditLedgerEntry) { *e = nil }}},
		{"ledger-rule", "a\u0301\r\n", conversionDamage{chain: func(_ *[]model.OffsetMapSegment, e *[]model.EditLedgerEntry) { (*e)[0].Rule = "other" }}},
		{"geometry-omitted", "abc", conversionDamage{quality: func(q *conversionQuality) { q.Findings = q.Findings[1:] }}},
		{"fidelity-omitted", "abc", conversionDamage{quality: func(q *conversionQuality) { q.Findings = q.Findings[:1] }}},
		{"html-source-root", "abc", conversionDamage{block: func(b *model.Block) { b.SourceSpans[0].Member = "word/document.xml" }}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			r, objects := convertedFixture(t, "text/html", fixture.text, fixture.damage)
			report, err := testEngine(t).Evaluate(context.Background(), r, objects)
			if err != bindingError || !reflect.DeepEqual(report, ResidencyReport{}) {
				t.Fatal("rehashed closure accepted or wrong failure")
			}
		})
	}
	for _, damage := range []conversionDamage{
		{block: func(b *model.Block) { b.SourceSpans = b.SourceSpans[:1] }},
		{block: func(b *model.Block) { b.SourceSpans[1].Member = "other.xml" }},
		{block: func(b *model.Block) { b.SourceSpans[0].Range = &model.ByteRange{Start: 9, End: 12} }},
	} {
		r, objects := convertedFixture(t, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "abc", damage)
		if report, err := testEngine(t).Evaluate(context.Background(), r, objects); err != bindingError || !reflect.DeepEqual(report, ResidencyReport{}) {
			t.Fatal("docx source control")
		}
	}
}

func TestResidencyConversionBounds(t *testing.T) {
	r, objects := convertedRequest(t)
	var outputs []model.AssetRef
	for _, object := range objects {
		if object.Ref == r.Evidence[0].Conversion.Outputs && json.Unmarshal(object.Bytes, &outputs) != nil {
			t.Fatal("bounds outputs")
		}
	}
	var total int64
	for _, ref := range outputs {
		total += ref.ByteSize
	}
	for _, fixture := range []struct {
		name   string
		change func(*conversion.ConversionManifest)
		want   error
	}{
		{"input-exact", func(m *conversion.ConversionManifest) { m.Request.Budget.MaxInputBytes = r.Subject.ByteSize }, nil},
		{"input-over", func(m *conversion.ConversionManifest) { m.Request.Budget.MaxInputBytes = r.Subject.ByteSize - 1 }, limitError},
		{"members-exact", func(m *conversion.ConversionManifest) { m.Request.Budget.MaxItems = len(outputs) }, nil},
		{"members-over", func(m *conversion.ConversionManifest) { m.Request.Budget.MaxItems = len(outputs) - 1 }, limitError},
		{"one-member", func(m *conversion.ConversionManifest) { m.Request.Budget.MaxItems = 1 }, limitError},
		{"output-exact", func(m *conversion.ConversionManifest) { m.OutputBytes = total }, nil},
		{"output-over", func(m *conversion.ConversionManifest) { m.OutputBytes = total - 1 }, limitError},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			r, objects := convertedFixture(t, "text/html", "abc", conversionDamage{request: fixture.change})
			report, err := testEngine(t).Evaluate(context.Background(), r, objects)
			if err != fixture.want || err != nil && !reflect.DeepEqual(report, ResidencyReport{}) {
				t.Fatal("retained conversion bound")
			}
		})
	}
	// Isolate the predecode manifest ceiling from manifest syntax and membership.
	for _, outputBytes := range []int64{1024, 2 << 20} {
		m := conversion.ConversionManifest{OutputBytes: outputBytes}
		for _, extra := range []int64{0, 1} {
			err := conversionBounds(m, model.AssetRef{ByteSize: min(outputBytes, 1<<20) + extra})
			if extra == 0 && err != nil || extra == 1 && err != limitError {
				t.Fatal("manifest exact-or-over ceiling")
			}
		}
	}
}

// The error class proves the retained ceiling runs before dependent JSON
// decoding. At the ceiling the malformed bytes reach the syntax check.
func TestResidencyConversionBoundsBeforeDecode(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		request func(*conversion.ConversionManifest)
		size    int
		want    error
	}{
		{"input-before-output-json", func(m *conversion.ConversionManifest) { m.Request.Budget.MaxInputBytes = 1 }, 1, limitError},
		{"output-allowance-exact", func(m *conversion.ConversionManifest) { m.OutputBytes = 1024 }, 1024, inputError},
		{"output-allowance-over", func(m *conversion.ConversionManifest) { m.OutputBytes = 1024 }, 1025, limitError},
		{"manifest-ceiling-exact", func(m *conversion.ConversionManifest) { m.OutputBytes = 2 << 20 }, 1 << 20, inputError},
		{"manifest-ceiling-over", func(m *conversion.ConversionManifest) { m.OutputBytes = 2 << 20 }, (1 << 20) + 1, limitError},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			r, objects := convertedFixture(t, "text/html", "abc", conversionDamage{
				request: fixture.request,
				outputs: func([]byte) []byte { return bytes.Repeat([]byte(" "), fixture.size) },
			})
			report, err := testEngine(t).Evaluate(context.Background(), r, objects)
			if err != fixture.want || !reflect.DeepEqual(report, ResidencyReport{}) {
				t.Fatal("conversion bound decoding order")
			}
		})
	}
}
