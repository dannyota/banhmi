package intake

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/blob"
	"danny.vn/banhmi/internal/corpus/conversion"
	"danny.vn/banhmi/internal/corpus/conversion/api"
	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/quality/formats"
	"danny.vn/banhmi/internal/corpus/store/document"
)

const acceptanceDOCXBody = `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>a&#13;` + "\n" + `</w:t></w:r></w:p><w:p><w:r><w:t>b</w:t></w:r></w:p></w:body></w:document>`

func acceptanceDOCX(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	f, err := w.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(f, acceptanceDOCXBody); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func acceptanceParagraphs(t *testing.T) []model.Span {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(acceptanceDOCXBody))
	var spans []model.Span
	var start int64
	var paragraph int
	for ordinal := 0; ; ordinal++ {
		before := d.InputOffset()
		value, err := d.Token()
		if err == io.EOF {
			return spans
		}
		if err != nil {
			t.Fatal(err)
		}
		switch value := value.(type) {
		case xml.StartElement:
			if value.Name.Local == "p" {
				start, paragraph = before, ordinal
			}
		case xml.EndElement:
			if value.Name.Local == "p" {
				index := paragraph
				spans = append(spans, model.Span{Range: &model.ByteRange{Start: start, End: d.InputOffset()}, TokenOrdinal: &index})
			}
		}
	}
}

// attachAcceptanceConversion retains two blocks, one reversible normalization,
// and a complete sandbox closure. Mutations happen before all dependent hashes,
// canonical objects and selected Silver report projections are produced.
func attachAcceptanceConversion(t *testing.T, f *acceptanceFixture, docx, mutate string) (model.ConvertedDocument, []model.OffsetMapSegment, []model.EditLedgerEntry) {
	t.Helper()
	added := map[model.Digest]model.AssetRef{}
	put := func(data []byte, path string) model.AssetRef {
		m := f.register(t, data)
		ref := catalogRef(m)
		added[ref.SHA256] = ref
		if path == "" {
			path = "objects/" + string(ref.SHA256)
		}
		ref.Path = path
		return ref
	}
	asset := func(data []byte) model.AssetRef { return put(data, "") }
	request := conversion.ConversionManifest{RequestKey: fmt.Sprintf("convert-%032x", 1), Request: api.ConvertRequest{Artifact: f.input.Artifacts[0].Capture, Recipe: f.input.ConversionRecipe, Budget: model.ResourceBudget(f.work.Budget)}, ObservationKey: f.input.ObservationKey, ArtifactRole: f.input.Artifacts[0].Role, Language: f.input.Language, HTMLSelector: f.input.HTMLSelector, DetectorHash: f.input.DetectorDigest, CandidateDigest: f.request.CandidateDigest, OutputBytes: 1 << 20}
	if mutate == "input-bound" || mutate == "input-limit" {
		request.Request.Budget.MaxInputBytes = request.Request.Artifact.Asset.ByteSize
		if mutate == "input-bound" {
			request.Request.Budget.MaxInputBytes--
		}
	} else if mutate == "span-bound" || mutate == "span-limit" {
		request.Request.Budget.MaxItems = 32
	}
	requestRef := put(acceptanceJSON(t, request), "request/conversion.json")
	detected := "html"
	if docx != "" {
		detected = "docx"
	}
	detector := asset(acceptanceJSON(t, struct {
		Version  string
		Format   string
		Original model.Digest
		Recipe   model.Digest
	}{"detect-v1", detected, request.Request.Artifact.Asset.SHA256, request.DetectorHash}))
	if mutate == "detector-path" {
		detector.Path = "alias/detector"
	}
	member := asset([]byte(acceptanceDOCXBody))
	paragraphs := acceptanceParagraphs(t)
	var blocks []model.Block
	var maps []model.OffsetMapSegment
	var edits []model.EditLedgerEntry
	var dependencies []model.AssetRef
	var missing model.Digest
	for k, raw := range []string{"a\r\n", "b"} {
		normal := strings.ReplaceAll(raw, "\r\n", "\n")
		rawRef, normalRef := asset([]byte(raw)), asset([]byte(normal))
		if k == 1 && mutate == "block-path" {
			normalRef.Path = "alias/block-text"
		}
		kind := model.BlockParagraph
		if k == 0 && mutate == "paragraph-kind" {
			kind = model.BlockHeading
		}
		identity := formats.Hash(acceptanceJSON(t, []string{"block-v1", string(kind), raw, normal}))
		block := model.Block{ID: fmt.Sprintf("block-%06d-%s", k, identity), Ordinal: k, Kind: kind, RawText: raw, NormalizedText: normalRef, Language: f.input.Language, Authority: model.AuthorityUnknown}
		ordinal := k
		root := model.Span{ArtifactHash: request.Request.Artifact.Asset.SHA256, BlockID: block.ID, Space: model.OriginalBytes, Range: &model.ByteRange{Start: int64(k), End: int64(k + 1)}, TokenOrdinal: &ordinal}
		block.SourceSpans = []model.Span{root}
		if k == 0 && (mutate == "span-bound" || mutate == "span-limit") {
			count := request.Request.Budget.MaxItems
			if mutate == "span-bound" {
				count++
			}
			for len(block.SourceSpans) < count {
				next := root
				start := int64(len(block.SourceSpans))
				next.Range = &model.ByteRange{Start: start, End: start + 1}
				block.SourceSpans = append(block.SourceSpans, next)
			}
		}
		if k == 1 && (mutate == "span-bound" || mutate == "span-limit") {
			block.SourceSpans[0].Range = &model.ByteRange{Start: 40, End: 41}
		}
		if strings.HasPrefix(mutate, "html-") {
			if k == 0 {
				switch mutate {
				case "html-ordinal":
					block.SourceSpans[0].TokenOrdinal = nil
				case "html-adjacent", "html-reversed", "html-overlap":
					second := root
					second.Range = &model.ByteRange{Start: 1, End: 2}
					block.SourceSpans = append(block.SourceSpans, second)
					if mutate == "html-reversed" {
						block.SourceSpans[0].Range = &model.ByteRange{Start: 2, End: 3}
					} else if mutate == "html-overlap" {
						block.SourceSpans[0].Range = &model.ByteRange{Start: 0, End: 2}
					}
				}
			} else {
				block.SourceSpans[0].Range = &model.ByteRange{Start: 4, End: 5}
				if mutate == "html-reused" {
					block.SourceSpans[0].Range = &model.ByteRange{Start: 0, End: 1}
				}
			}
		}
		if docx != "" {
			root.TokenOrdinal = paragraphs[k].TokenOrdinal
			root.Range, root.Member, root.Object, root.ReasonCode = nil, "word/document.xml", fmt.Sprintf("paragraph/%d", k), "BYTE_MAPPING_UNAVAILABLE"
			decoded := root
			decoded.ArtifactHash, decoded.Space, decoded.Range, decoded.ReasonCode = member.SHA256, model.DecodedMember, paragraphs[k].Range, ""
			block.SourceSpans = []model.Span{root, decoded}
			if k == 0 {
				s := &block.SourceSpans[1]
				switch mutate {
				case "decoded-member":
					s.Member = "word/other.xml"
				case "root-member":
					block.SourceSpans[0].Member = "word/other.xml"
				case "root-object":
					block.SourceSpans[0].Object = "paragraph/1"
				case "root-range":
					block.SourceSpans[0].Range = &model.ByteRange{Start: 0, End: 1}
				case "root-reason":
					block.SourceSpans[0].ReasonCode = "OTHER_REASON"
				case "root-hash":
					block.SourceSpans[0].ArtifactHash = f.input.ConversionRecipe.SHA256
				case "root-space":
					block.SourceSpans[0].Space = model.DecodedMember
				case "decoded-space":
					s.Space = model.OriginalBytes
				case "root-block":
					block.SourceSpans[0].BlockID = "other-block"
				case "decoded-block":
					s.BlockID = "other-block"
				case "root-count":
					block.SourceSpans = block.SourceSpans[:1]
				case "decoded-object":
					s.Object = "paragraph/1"
				case "root-ordinal":
					block.SourceSpans[0].TokenOrdinal = nil
				case "decoded-ordinal":
					s.TokenOrdinal = nil
				case "unequal-ordinal":
					other := 1
					s.TokenOrdinal = &other
				case "root-page", "decoded-page":
					page := 1
					if mutate == "root-page" {
						block.SourceSpans[0].Page = &page
					} else {
						s.Page = &page
					}
				case "decoded-reason":
					s.ReasonCode = "BYTE_MAPPING_UNAVAILABLE"
				case "decoded-range":
					s.Range = nil
				}
			}
			if k == 1 && mutate == "member-hash" {
				block.SourceSpans[1].ArtifactHash = rawRef.SHA256
				block.SourceSpans[1].Range = &model.ByteRange{Start: 0, End: 1}
			}
		}
		rawSpan := model.Span{ArtifactHash: rawRef.SHA256, Space: model.RawText, Range: &model.ByteRange{Start: 0, End: rawRef.ByteSize}, Object: "text"}
		normalSpan := model.Span{ArtifactHash: normalRef.SHA256, Space: model.NormalizedText, Range: &model.ByteRange{Start: 0, End: normalRef.ByteSize}}
		transform := conversion.TransformIdentity
		if raw != normal {
			transform = conversion.TransformNFCCRLF
			decoded := model.DecodedEdit{Kind: model.EditNormalize, Rule: conversion.NormalizationRuleNFCCRLF, Version: conversion.NormalizationVersionNFCCRLF, Inputs: []model.Span{rawSpan}, Outputs: []model.Span{normalSpan}, Removed: raw, Replacement: normal}
			evidence := asset(acceptanceJSON(t, decoded))
			if mutate == "edit-path" {
				evidence.Path = "alias/edit-evidence"
			}
			edits = append(edits, model.EditLedgerEntry{Kind: decoded.Kind, Ordinal: 0, Rule: decoded.Rule, Version: decoded.Version, Inputs: decoded.Inputs, Outputs: decoded.Outputs, Removed: &rawRef, Replacement: &normalRef, Evidence: evidence})
			if mutate == "missing-evidence" {
				missing = evidence.SHA256
			} else if mutate == "missing-removed" {
				missing = rawRef.SHA256
			} else if mutate == "missing-replacement" {
				missing = normalRef.SHA256
			}
			dependencies = append(dependencies, evidence)
		}
		if k == 1 && mutate == "missing-block-text" {
			missing = normalRef.SHA256
		}
		dependencies = append(dependencies, rawRef, normalRef)
		blocks = append(blocks, block)
		maps = append(maps, model.OffsetMapSegment{Ordinal: k * 2, Sources: block.SourceSpans, Destinations: []model.Span{rawSpan}, Transform: conversion.TransformSourceDecode}, model.OffsetMapSegment{Ordinal: k*2 + 1, Sources: []model.Span{rawSpan}, Destinations: []model.Span{normalSpan}, Transform: transform})
	}
	if docx != "" {
		dependencies = append(dependencies, member)
		if mutate == "missing-decoded-member" {
			missing = member.SHA256
		}
	} else {
		delete(added, member.SHA256)
	}
	type rawBlock struct {
		ID      string
		Kind    model.BlockKind
		Text    string
		Sources []model.Span
	}
	var rawBlocks []rawBlock
	for _, block := range blocks {
		rawBlocks = append(rawBlocks, rawBlock{block.ID, block.Kind, block.RawText, block.SourceSpans})
	}
	doc := model.ConvertedDocument{SchemaVersion: "blocks-v1", Original: request.Request.Artifact, ObservationKey: request.ObservationKey, ArtifactRole: request.ArtifactRole, RecipeHash: request.Request.Recipe.SHA256, DetectorHash: detector.SHA256, CandidateDigest: request.CandidateDigest,
		RawBlocks: asset(acceptanceJSON(t, rawBlocks)), NormalizedBlocks: asset(acceptanceJSON(t, blocks)), RawText: asset([]byte("a\r\nb")), NormalizedText: asset([]byte("a\nb")), OffsetMap: asset(acceptanceJSON(t, maps)), EditLedger: asset(acceptanceJSON(t, edits)), Blocks: blocks}
	if mutate == "aggregate-path" {
		doc.RawText.Path = "alias/raw-text"
	} else if mutate == "json-charge-alias" {
		doc.OffsetMap = doc.EditLedger
		doc.OffsetMap.ByteSize = 1
	}
	documentRef := put(acceptanceJSON(t, doc), "results/"+string(requestRef.SHA256)+"/document.json")
	if mutate == "document-path" {
		documentRef.Path = "alias/document.json"
	}
	metrics := []model.QualityMetric{}
	findings := []model.Finding{}
	for k, kind := range []model.MetricKind{model.MetricGeometry, model.MetricFidelity} {
		metrics = append(metrics, model.QualityMetric{Kind: kind, State: model.MetricUnknown, Threshold: "qualification-required", Mandatory: true, ReasonCode: "BASELINE_UNQUALIFIED"})
		findings = append(findings, model.Finding{ReasonCode: []string{"GEOMETRY_UNKNOWN", "FIDELITY_UNQUALIFIED"}[k], Severity: model.SeverityBlocking, Scope: model.GapArtifact, ScopeKey: string(doc.Original.Asset.SHA256)})
	}
	if mutate == "foreign-original-span" {
		findings[0].Spans = []model.Span{{ArtifactHash: member.SHA256, Space: model.OriginalBytes, Range: &model.ByteRange{Start: 0, End: 1}}}
	}
	quality := put(acceptanceJSON(t, struct {
		SchemaVersion   string
		ArtifactHash    model.Digest
		CandidateDigest model.Digest
		RequestDigest   model.Digest
		RecipeHash      model.Digest
		DetectorHash    model.Digest
		DocumentHash    model.Digest
		Outcome         model.QualityOutcome
		Metrics         []model.QualityMetric
		Findings        []model.Finding
	}{"conversion-quality-v1", doc.Original.Asset.SHA256, doc.CandidateDigest, requestRef.SHA256, doc.RecipeHash, doc.DetectorHash, documentRef.SHA256, model.QualityQuarantined, metrics, findings}), "results/"+string(requestRef.SHA256)+"/quality.json")
	dependencies = append(dependencies, doc.RawBlocks, doc.NormalizedBlocks, doc.RawText, doc.NormalizedText, doc.OffsetMap, doc.EditLedger, detector, documentRef, quality)
	members := []model.AssetRef{}
	seen := map[model.AssetRef]bool{}
	for _, ref := range dependencies {
		if !seen[ref] && ref.SHA256 != missing {
			members, seen[ref] = append(members, ref), true
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Path < members[j].Path })
	manifest := put(acceptanceJSON(t, members), "manifest/conversion.json")
	gate := f.gate
	gate.RequestManifest, gate.Document, gate.OutputManifest = &requestRef, &documentRef, &manifest
	gateCodec, err := formats.GateReportCodec(f.policy, formats.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	gateBytes, _, err := gateCodec.Encode(gate)
	if err != nil {
		t.Fatal(err)
	}
	gateMetadata := f.register(t, gateBytes)
	replaceAcceptanceOutput(f, f.rollup.ArtifactReports[0].Report.SHA256, catalogRef(gateMetadata))
	f.rollup.ArtifactReports[0].Report = catalogRef(gateMetadata)
	f.rollup.MissingOrdinals = []int{}
	f.rollup.Retained[0].Spans = []model.Span{{ArtifactHash: doc.NormalizedText.SHA256, BlockID: "document", Space: model.NormalizedText, Range: &model.ByteRange{Start: 0, End: doc.NormalizedText.ByteSize}}}
	output := acceptanceConvertedOutput(f, doc)
	outputBytes, _, err := formats.CandidateOutputCodec(formats.DefaultLimits()).Encode(output)
	if err != nil {
		t.Fatal(err)
	}
	outputRef := put(outputBytes, "")
	f.rollup.OutputManifest = &outputRef
	rollupCodec, err := formats.GateRollupCodec(f.policy, formats.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	rollupBytes, _, err := rollupCodec.Encode(f.rollup)
	if err != nil {
		t.Fatal(err)
	}
	rollupMetadata := f.register(t, rollupBytes)
	replaceAcceptanceOutput(f, f.request.QualityReportHash, catalogRef(rollupMetadata))
	f.request.QualityReportHash, f.request.OutputDigest = model.Digest(rollupMetadata.SHA256), outputRef.SHA256
	for _, ref := range f.request.Outputs {
		added[ref.SHA256] = ref
	}
	f.request.Outputs = nil
	for _, ref := range added {
		f.request.Outputs = append(f.request.Outputs, ref)
	}
	sort.Slice(f.request.Outputs, func(i, j int) bool { return f.request.Outputs[i].SHA256 < f.request.Outputs[j].SHA256 })
	parent, err := formats.ProjectArtifactParent(gate, catalogRef(gateMetadata), f.policy, formats.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	f.persistReport(t, gateMetadata, parent, gate.Artifact.Metrics, gate.Artifact.Findings, []formats.Gap{})
	parent, err = formats.ProjectRollupParent(f.rollup, catalogRef(rollupMetadata), f.policy, formats.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	f.persistReport(t, rollupMetadata, parent, f.rollup.Metrics, f.rollup.Findings, f.rollup.Gaps)
	f.gate = gate
	return doc, maps, edits
}

func acceptanceConvertedOutput(f *acceptanceFixture, doc model.ConvertedDocument) formats.CandidateOutput {
	output := formats.CandidateOutput{SchemaVersion: "candidate-output-v1", CandidateDigest: f.request.CandidateDigest, NormalizerManifestDigest: f.input.NormalizerManifest.SHA256, GatePolicyDigest: f.input.GatePolicyDigest,
		Artifacts: []formats.OutputArtifact{{Ordinal: 0, OriginalHash: doc.Original.Asset.SHA256, DetectorDigest: doc.DetectorHash, RawBlocks: doc.RawBlocks, NormalizedBlocks: doc.NormalizedBlocks, RawText: doc.RawText, NormalizedText: doc.NormalizedText, OffsetMap: doc.OffsetMap, EditLedger: doc.EditLedger}}}
	output.TextHash = formats.TextHash(output.Artifacts)
	return output
}

// completeAcceptancePrerequisites supplies an immutable revision and every
// conversion projection before a disposition negative. The conversion builder
// already retains the complete candidate output and codec-validated reports.
func completeAcceptancePrerequisites(t *testing.T, f *acceptanceFixture, doc model.ConvertedDocument, maps []model.OffsetMapSegment, edits []model.EditLedgerEntry) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	output := acceptanceConvertedOutput(f, doc)
	var metadataHash model.Digest
	var external string
	must(f.app.QueryRow(f.db.ctx, `SELECT o.metadata_digest,s.external_id FROM bronze.observation_revision o JOIN bronze.source_record s ON s.id=o.source_record_id WHERE o.observation_key=$1`, f.input.ObservationKey).Scan(&metadataHash, &external))
	revisions, err := document.NewRevisions(f.app, "writer-a", document.ProvisionLimits{MaxItems: 1000, MaxPayloadBytes: 1 << 20, MaxCitationBytes: 1000, MaxAnchorBytes: 1000, MaxAnchors: 100})
	must(err)
	identity := strings.Split(f.request.RevisionKey, ":rev:")[0]
	minted, err := revisions.MintDocumentIdentity(f.db.ctx, f.grant, identity, document.ObservedAssertion{ObservationKey: f.input.ObservationKey, SourceID: doc.Original.SourceID, ExternalID: external, TitleHash: metadataHash})
	must(err)
	_, err = revisions.InsertDocumentRevision(f.db.ctx, f.grant, document.RevisionInput{DocumentIdentityID: minted.ID, RevisionKey: f.request.RevisionKey, SourceObservationSetHash: formats.Hash(acceptanceJSON(t, []string{f.input.ObservationKey})), TextHash: output.TextHash, MetadataHash: metadataHash, ConversionRecipeDigest: doc.RecipeHash, NormalizationRecipeDigest: f.input.NormalizerManifest.SHA256, ParserRecipeDigest: f.input.ParserManifest.SHA256, PolicyRecipeDigest: f.input.GatePolicyDigest})
	must(err)
	physical := map[model.Digest]model.AssetRef{}
	for _, ref := range f.request.Outputs {
		physical[ref.SHA256] = ref
	}
	metadata := func(ref model.AssetRef) blob.Metadata {
		var m blob.Metadata
		must(f.app.QueryRow(f.db.ctx, `SELECT sha256,byte_size,media_type,object_ref,capture_source,capture_ref,captured_at FROM bronze.blob WHERE sha256=$1`, string(ref.SHA256)).Scan(&m.SHA256, &m.ByteSize, &m.MediaType, &m.ObjectRef, &m.CaptureSource, &m.CaptureRef, &m.CapturedAt))
		return m
	}
	roles := []struct {
		role model.ConversionArtifactRole
		ref  model.AssetRef
	}{{model.ConversionRawBlocks, doc.RawBlocks}, {model.ConversionNormalizedBlocks, doc.NormalizedBlocks}, {model.ConversionRawText, doc.RawText}, {model.ConversionNormalizedText, doc.NormalizedText}, {model.ConversionManifest, *f.gate.OutputManifest}}
	var members []document.ConversionArtifactMember
	for _, role := range roles {
		members = append(members, document.ConversionArtifactMember{Role: role.role, Hash: role.ref.SHA256, ByteSize: role.ref.ByteSize})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Role < members[j].Role })
	target := document.ConversionTarget{RevisionKey: f.request.RevisionKey, CandidateDigest: f.request.CandidateDigest}
	for _, role := range roles {
		_, err = f.store.RegisterConversionArtifact(f.db.ctx, f.grant, target, document.ConversionArtifactInput{Role: role.role, Blob: metadata(role.ref), SchemaVersion: "blocks-v1", OriginalHash: doc.Original.Asset.SHA256, ConversionRecipeDigest: doc.RecipeHash, NormalizationRecipeDigest: f.input.NormalizerManifest.SHA256, DetectorDigest: doc.DetectorHash, Manifest: metadata(*f.gate.OutputManifest), Members: members})
		must(err)
	}
	for _, segment := range maps {
		for sourceOrdinal, source := range segment.Sources {
			for destinationOrdinal, destination := range segment.Destinations {
				sourceSpan, err := formats.ProjectReportSpan(source, physical[source.ArtifactHash])
				must(err)
				destinationSpan, err := formats.ProjectReportSpan(destination, physical[destination.ArtifactHash])
				must(err)
				_, err = f.store.InsertOffsetMapSegments(f.db.ctx, f.grant, target, physical[source.ArtifactHash], physical[destination.ArtifactHash], []document.ConversionSegment{{MapHash: doc.OffsetMap.SHA256, Source: sourceSpan, Destination: destinationSpan, TransformOrder: int32(segment.Ordinal), Ordinal: int32(sourceOrdinal*len(segment.Destinations) + destinationOrdinal), Transform: segment.Transform}})
				must(err)
			}
		}
	}
	for _, edit := range edits {
		_, err = f.store.AppendEditLedgerEntry(f.db.ctx, f.grant, target, document.ConversionEditInput{LedgerHash: doc.EditLedger.SHA256, Ordinal: int32(edit.Ordinal), Operation: edit.Kind, Rule: edit.Rule, RuleVersion: edit.Version, Input: physical[edit.Removed.SHA256], Output: physical[edit.Replacement.SHA256], InputRange: *edit.Inputs[0].Range, OutputRange: *edit.Outputs[0].Range, RemovedBytes: f.objects.data[physical[edit.Removed.SHA256].Path], ReplacementBytes: f.objects.data[physical[edit.Replacement.SHA256].Path], EncodingEvidence: []byte(`{}`), FontEvidence: []byte(`{}`), SourceAnchors: acceptanceJSON(t, edit.Inputs)})
		must(err)
	}
}

func TestAcceptanceNestedConversionMembership(t *testing.T) {
	for _, name := range []string{"", "missing-block-text", "missing-decoded-member", "missing-evidence", "missing-removed", "missing-replacement", "foreign-original-span"} {
		t.Run("docx/"+name, func(t *testing.T) {
			f := newAcceptanceFixtureWith(t, model.QualityQuarantined, "synthetic conversion", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", acceptanceDOCX(t))
			attachAcceptanceConversion(t, f, "docx", name)
			before := acceptanceSnapshot(t, f)
			_, err := f.adapter.Apply(f.db.ctx, f.request)
			if name == "" {
				if err != nil {
					t.Fatal("complete conversion rejected", err)
				}
				return
			}
			requireAcceptanceCause(t, err, "acceptance conversion membership:")
			if !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
				t.Fatal("missing nested member changed rows")
			}
		})
	}
}

func TestAcceptanceDOCXSourceRoots(t *testing.T) {
	for _, name := range []string{"root-member", "root-object", "root-range", "root-reason", "root-hash", "root-space", "decoded-space", "root-block", "decoded-block", "root-count", "decoded-member", "decoded-object", "root-ordinal", "decoded-ordinal", "unequal-ordinal", "root-page", "decoded-page", "decoded-reason", "decoded-range", "member-hash", "paragraph-kind"} {
		t.Run(name, func(t *testing.T) {
			f := newAcceptanceFixtureWith(t, model.QualityQuarantined, "synthetic DOCX roots", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", acceptanceDOCX(t))
			attachAcceptanceConversion(t, f, "docx", name)
			before := acceptanceSnapshot(t, f)
			_, err := f.adapter.Apply(f.db.ctx, f.request)
			stage := "acceptance conversion source roots"
			if name == "root-reason" || name == "decoded-range" {
				stage = "unavailable byte mapping requires reason and usable anchor"
			}
			requireAcceptanceCause(t, err, stage)
			if !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
				t.Fatal("invalid source root changed rows")
			}
		})
	}
}

func TestAcceptanceHTMLSourceRoots(t *testing.T) {
	for _, name := range []string{"html-adjacent", "html-ordinal", "html-reversed", "html-overlap", "html-reused"} {
		t.Run(name, func(t *testing.T) {
			f := newAcceptanceFixture(t, model.QualityQuarantined)
			attachAcceptanceConversion(t, f, "", name)
			before := acceptanceSnapshot(t, f)
			_, err := f.adapter.Apply(f.db.ctx, f.request)
			if name == "html-adjacent" {
				if err != nil {
					t.Fatal("ordered distinct HTML ranges rejected", err)
				}
				return
			}
			requireAcceptanceCause(t, err, "acceptance conversion source roots")
			if !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
				t.Fatal("invalid HTML source roots changed rows")
			}
		})
	}
}

func TestAcceptanceCanonicalConversionPaths(t *testing.T) {
	for _, name := range []string{"aggregate-path", "block-path", "edit-path", "detector-path", "document-path"} {
		t.Run(name, func(t *testing.T) {
			f := newAcceptanceFixtureWith(t, model.QualityQuarantined, "synthetic conversion paths", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", acceptanceDOCX(t))
			attachAcceptanceConversion(t, f, "docx", name)
			before := acceptanceSnapshot(t, f)
			_, err := f.adapter.Apply(f.db.ctx, f.request)
			stage := "acceptance conversion membership: noncanonical"
			if name == "detector-path" {
				stage = "acceptance detector missing from sandbox manifest"
			} else if name == "document-path" {
				stage = "acceptance conversion request or document path"
			}
			requireAcceptanceCause(t, err, stage)
			if !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
				t.Fatal("noncanonical conversion path changed rows")
			}
		})
	}
}
