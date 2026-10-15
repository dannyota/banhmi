package intake

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"reflect"
	"strings"

	"danny.vn/banhmi/internal/corpus/conversion"
	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/quality/formats"
)

func acceptanceDecode(data []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	canonical, err := json.Marshal(dst)
	if err != nil || !bytes.Equal(canonical, data) {
		return fmt.Errorf("retained JSON is not canonical")
	}
	return nil
}

// JSON roles charge the catalog size before Open, including shared catalog
// winners with a different media type. Generic dependencies wait until every
// structured role has been discovered, so an evidence alias cannot read first.
func (v *acceptanceEvidence) chargeJSON(ctx context.Context, ref model.AssetRef) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if size, ok := v.jsonSeen[ref.SHA256]; ok {
		if size != ref.ByteSize {
			return fmt.Errorf("logical aliases disagree on size")
		}
		return nil
	}
	if prior, ok := v.refs[ref.SHA256]; ok {
		if prior.ByteSize != ref.ByteSize {
			return fmt.Errorf("logical aliases disagree on size")
		}
	} else {
		b, err := v.q.ReadSourceBlob(ctx, string(ref.SHA256))
		if err != nil {
			return err
		}
		if b.Sha256 != string(ref.SHA256) || b.ByteSize != ref.ByteSize {
			return fmt.Errorf("catalog reference mismatch")
		}
	}
	if err := v.bound(ctx, 0, ref.ByteSize); err != nil {
		return err
	}
	v.jsonSeen[ref.SHA256] = ref.ByteSize
	return nil
}

func (v *acceptanceEvidence) jsonObject(ctx context.Context, ref model.AssetRef) ([]byte, error) {
	if err := v.chargeJSON(ctx, ref); err != nil {
		return nil, err
	}
	return v.load(ctx, ref)
}

func (v *acceptanceEvidence) decodeObject(ctx context.Context, ref model.AssetRef, dst any) error {
	data, err := v.jsonObject(ctx, ref)
	if err != nil {
		return err
	}
	return acceptanceDecode(data, dst)
}

func (v *acceptanceEvidence) dependencies(ctx context.Context, value any) error {
	var walk func(reflect.Value) error
	walk = func(x reflect.Value) error {
		if x.Kind() == reflect.Pointer {
			if x.IsNil() {
				return nil
			}
			return walk(x.Elem())
		}
		if x.CanInterface() {
			if span, ok := x.Interface().(model.Span); ok {
				v.spans = append(v.spans, span)
			}
			if ref, ok := x.Interface().(model.AssetRef); ok {
				if ref != formats.ReportSentinel() {
					if err := v.bound(ctx, 1, 0); err != nil {
						return err
					}
					v.pending = append(v.pending, ref)
				}
				return nil
			}
		}
		switch x.Kind() {
		case reflect.Struct:
			for i := 0; i < x.NumField(); i++ {
				if err := walk(x.Field(i)); err != nil {
					return err
				}
			}
		case reflect.Slice, reflect.Array:
			if err := v.bound(ctx, int64(x.Len()), 0); err != nil {
				return err
			}
			for i := 0; i < x.Len(); i++ {
				if err := walk(x.Index(i)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(reflect.ValueOf(value))
}

func (v *acceptanceEvidence) loadDependencies(ctx context.Context) error {
	for _, ref := range v.pending {
		if _, err := v.load(ctx, ref); err != nil {
			return err
		}
	}
	for _, span := range v.spans {
		if _, err := formats.ProjectReportSpan(span, v.refs[span.ArtifactHash]); err != nil {
			return err
		}
	}
	return nil
}

func (v *acceptanceEvidence) detector(ctx context.Context, doc model.ConvertedDocument, manifest []model.AssetRef) error {
	var ref model.AssetRef
	for _, member := range manifest {
		if member.SHA256 == doc.DetectorHash {
			ref = member
			break
		}
	}
	if ref.SHA256 == "" || ref.Path != "objects/"+string(doc.DetectorHash) {
		return fmt.Errorf("acceptance detector missing from sandbox manifest")
	}
	data, err := v.jsonObject(ctx, ref)
	if err != nil {
		return err
	}
	var detector struct {
		Version  string
		Format   string
		Original model.Digest
		Recipe   model.Digest
	}
	if err := acceptanceDecode(data, &detector); err != nil {
		return fmt.Errorf("acceptance detector: %w", err)
	}
	media, parameters, err := mime.ParseMediaType(doc.Original.MediaType)
	if charset := parameters["charset"]; charset != "" && !strings.EqualFold(charset, "utf-8") && !strings.EqualFold(charset, "utf8") {
		return fmt.Errorf("acceptance detector unsupported charset")
	}
	if err != nil || detector.Version != "detect-v1" || detector.Original != doc.Original.Asset.SHA256 || detector.Recipe != v.input.DetectorDigest ||
		!((media == "text/html" && detector.Format == "html") || (media == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" && detector.Format == "docx")) {
		return fmt.Errorf("acceptance detector contract mismatch")
	}
	v.detected[doc.DetectorHash] = detector.Format
	return nil
}

func (v *acceptanceEvidence) blockBytes(doc model.ConvertedDocument) error {
	var raw, normalized []byte
	type rawBlock struct {
		ID      string
		Kind    model.BlockKind
		Text    string
		Sources []model.Span
	}
	var rawBlocks []rawBlock
	for k, block := range doc.Blocks {
		mapped, _, err := (conversion.TextNormalizer{}).Normalize(model.TextInput{Text: block.RawText, Sources: block.SourceSpans, Limits: model.RuleLimits{MaxItems: v.r.Work.Budget.MaxItems, MaxBytes: v.limits.MaxObjectBytes}})
		if err != nil {
			return err
		}
		text := mapped.Text
		identity, _ := json.Marshal([]string{"block-v1", string(block.Kind), block.RawText, text})
		if block.Ordinal != k || block.ID != fmt.Sprintf("block-%06d-%s", k, formats.Hash(identity)) || block.ParentID != "" || block.Authority != model.AuthorityUnknown || block.Content != nil || block.Findings != nil || block.Language != v.input.Language || !bytes.Equal(v.data[block.NormalizedText.SHA256], []byte(text)) {
			return fmt.Errorf("block differs from canonical conversion")
		}
		raw, normalized = append(raw, []byte(block.RawText)...), append(normalized, []byte(text)...)
		rawBlocks = append(rawBlocks, rawBlock{block.ID, block.Kind, block.RawText, block.SourceSpans})
	}
	rawJSON, _ := json.Marshal(rawBlocks)
	normalJSON, _ := json.Marshal(doc.Blocks)
	if !bytes.Equal(raw, v.data[doc.RawText.SHA256]) || !bytes.Equal(normalized, v.data[doc.NormalizedText.SHA256]) || !bytes.Equal(rawJSON, v.data[doc.RawBlocks.SHA256]) || !bytes.Equal(normalJSON, v.data[doc.NormalizedBlocks.SHA256]) {
		return fmt.Errorf("aggregate conversion bytes differ from blocks")
	}
	return nil
}
