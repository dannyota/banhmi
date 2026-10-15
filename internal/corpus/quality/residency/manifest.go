package residency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"

	"danny.vn/banhmi/internal/corpus/conversion"
	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/quality/api"
)

// ScreenRequest binds one export surface, policy and original execution.
type ScreenRequest struct {
	SchemaVersion string
	Country       string
	SourceID      string
	OwnerKey      string
	Operation     string
	SubjectKind   string
	Subject       model.AssetRef
	MediaType     string
	Policy        model.AssetRef
	Budget        model.ResourceBudget
	Capture       *api.ResidencyRequest
	Evidence      []ScreenEvidence
}

// ScreenEvidence supplies subject-bound metadata or conversion raw text.
type ScreenEvidence struct {
	Kind        string
	SubjectHash model.Digest
	Asset       model.AssetRef
	Conversion  *ConversionEvidence
}

// ConversionEvidence pins the typed conversion roots for raw text evidence.
type ConversionEvidence struct {
	Request  model.AssetRef
	Document model.AssetRef
	Outputs  model.AssetRef
}

// Codec retains one canonical private format under trusted local ceilings.
// It validates byte shape and references without reading dependencies.
type Codec[T any] struct {
	limits    ResidencyLimits
	maxBytes  int64
	validate  func(T) error
	marshal   func(T) ([]byte, error)
	unmarshal func([]byte, *T) error
}

// Encode returns canonical bytes and their full lowercase SHA-256 digest.
// On failure it returns no bytes or digest and only a fixed ErrorKind.
func (c Codec[T]) Encode(value T) ([]byte, model.Digest, error) {
	if c.limits.Validate() != nil || c.maxBytes <= 0 {
		return nil, "", limitError
	}
	if c.validate == nil {
		return nil, "", inputError
	}
	if err := treeBounds(value, c.limits, c.maxBytes); err != nil {
		return nil, "", err
	}
	if err := c.validate(value); err != nil {
		return nil, "", err
	}
	marshal := c.marshal
	if marshal == nil {
		marshal = func(v T) ([]byte, error) { return json.Marshal(v) }
	}
	data, err := marshal(value)
	if err != nil {
		return nil, "", inputError
	}
	if int64(len(data)) > c.maxBytes {
		return nil, "", limitError
	}
	if err := dependencyBounds(value, data, c.limits); err != nil {
		return nil, "", err
	}
	return data, hash(data), nil
}

// Decode rejects noncanonical bytes without repairing or returning partial values.
func (c Codec[T]) Decode(data []byte) (T, model.Digest, error) {
	var zero, value T
	if c.limits.Validate() != nil || c.maxBytes <= 0 || int64(len(data)) > c.maxBytes {
		return zero, "", limitError
	}
	if err := inspectJSON(data, c.limits.MaxItems, c.maxBytes, false, nil); err != nil {
		return zero, "", err
	}
	var err error
	if c.unmarshal != nil {
		err = c.unmarshal(data, &value)
	} else {
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		err = d.Decode(&value)
	}
	if err != nil {
		return zero, "", decodeError(err)
	}
	canonical, digest, err := c.Encode(value)
	if err != nil {
		return zero, "", err
	}
	if !bytes.Equal(data, canonical) {
		return zero, "", inputError
	}
	return value, digest, nil
}

func hash(data []byte) model.Digest {
	s := sha256.Sum256(data)
	return model.Digest(hex.EncodeToString(s[:]))
}

// RequestCodec constructs the screen-request-v1 rule A codec.
func RequestCodec(limits ResidencyLimits) Codec[ScreenRequest] {
	return Codec[ScreenRequest]{limits: limits, maxBytes: limits.MaxRequestBytes,
		validate: func(r ScreenRequest) error { return validateRequest(r, limits) }}
}

var sourceID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func validateRequest(r ScreenRequest, l ResidencyLimits) error {
	if r.SchemaVersion != "screen-request-v1" || r.Country != "vn" || !sourceID.MatchString(r.SourceID) || model.ValidateEnvelopeKey(r.OwnerKey, "execution") != nil || r.Budget.Validate() != nil {
		return inputError
	}
	if r.Policy.Path != "policies/"+string(r.Policy.SHA256) {
		return bindingError
	}
	if r.Subject.ByteSize > min(l.MaxSubjectBytes, r.Budget.MaxInputBytes) || int64(len(r.Evidence)) > min(l.MaxEvidence, int64(r.Budget.MaxItems)) {
		return limitError
	}
	switch r.SubjectKind {
	case "outbound_http":
		if r.Operation != "capture_export" || r.Capture != nil || len(r.Evidence) != 0 || r.MediaType != "application/http" || r.Subject.Path != "subjects/"+string(r.Subject.SHA256) {
			return inputError
		}
	case "captured_artifact":
		if r.Operation != "artifact_export" || r.Capture == nil || r.Capture.Artifact.Validate() != nil || r.Capture.Budget.Validate() != nil || model.ValidateEnvelopeKey(r.Capture.Artifact.CaptureKey, "capture") != nil {
			return inputError
		}
		c := r.Capture
		if r.Subject != c.Artifact.Asset || r.Policy != c.Policy || r.Budget != c.Budget || r.Country != c.Artifact.Country || r.SourceID != c.Artifact.SourceID || r.MediaType != c.Artifact.MediaType {
			return bindingError
		}
	default:
		return inputError
	}
	for i, e := range r.Evidence {
		if e.SubjectHash != r.Subject.SHA256 || (i > 0 && !evidenceLess(r.Evidence[i-1], e)) {
			return bindingError
		}
		switch e.Kind {
		case "metadata_utf8":
			if e.Conversion != nil {
				return inputError
			}
		case "converted_raw_utf8":
			if e.Conversion == nil || r.Capture == nil {
				return inputError
			}
		default:
			return inputError
		}
	}
	return nil
}

func evidenceLess(a, b ScreenEvidence) bool {
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	if a.Asset.SHA256 != b.Asset.SHA256 {
		return a.Asset.SHA256 < b.Asset.SHA256
	}
	return a.Asset.Path < b.Asset.Path
}

func treeBounds(value any, l ResidencyLimits, maxBytes int64) error {
	rows, remaining := l.MaxItems, maxBytes
	var walk func(reflect.Value, int) error
	walk = func(v reflect.Value, depth int) error {
		if depth > 64 {
			return limitError
		}
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return nil
			}
			return walk(v.Elem(), depth+1)
		}
		if v.CanInterface() {
			switch x := v.Interface().(type) {
			case model.Digest:
				if x.Validate() != nil {
					return inputError
				}
			case model.AssetRef:
				if x.Validate() != nil {
					return inputError
				}
				if len(x.Path) > 512 {
					return limitError
				}
			}
		}
		switch v.Kind() {
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if err := walk(v.Field(i), depth+1); err != nil {
					return err
				}
			}
		case reflect.Slice:
			if v.IsNil() {
				return inputError
			}
			if int64(v.Len()) > rows {
				return limitError
			}
			rows -= int64(v.Len())
			for i := 0; i < v.Len(); i++ {
				if err := walk(v.Index(i), depth+1); err != nil {
					return err
				}
			}
		case reflect.String:
			if int64(v.Len()) > remaining {
				return limitError
			}
			remaining -= int64(v.Len())
			if !utf8.ValidString(v.String()) || strings.ContainsRune(v.String(), 0) {
				return inputError
			}
		}
		return nil
	}
	return walk(reflect.ValueOf(value), 0)
}

func visitRefs(value any, visit func(model.AssetRef) error) error {
	var walk func(reflect.Value) error
	walk = func(v reflect.Value) error {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return nil
			}
			return walk(v.Elem())
		}
		if v.CanInterface() {
			if ref, ok := v.Interface().(model.AssetRef); ok {
				return visit(ref)
			}
		}
		switch v.Kind() {
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if err := walk(v.Field(i)); err != nil {
					return err
				}
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				if err := walk(v.Index(i)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(reflect.ValueOf(value))
}

func dependencyBounds(value any, root []byte, l ResidencyLimits) error {
	h := hash(root)
	sizes := map[model.Digest]int64{h: int64(len(root))}
	paths := make(map[string]model.Digest)
	remaining := l.MaxDecodedBytes - int64(len(root))
	if remaining < 0 {
		return limitError
	}
	return visitRefs(value, func(r model.AssetRef) error {
		if r.SHA256 == h {
			return bindingError
		}
		if old, ok := paths[r.Path]; ok && old != r.SHA256 {
			return bindingError
		}
		paths[r.Path] = r.SHA256
		if size, ok := sizes[r.SHA256]; ok {
			if size != r.ByteSize {
				return bindingError
			}
			return nil
		}
		if int64(len(sizes)) >= l.MaxObjects || r.ByteSize > remaining {
			return limitError
		}
		remaining -= r.ByteSize
		sizes[r.SHA256] = r.ByteSize
		return nil
	})
}

// inspectJSON validates syntax before decoding, retaining exact number tokens.
// emit, when present, receives decoded keys and strings in lexical token order.
func inspectJSON(data []byte, items, stringBytes int64, containerRoot bool, emit func(string) error) error {
	if !utf8.Valid(data) || !validEscapes(data) {
		return inputError
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var walk func(int) error
	str := func(s string) error {
		if strings.ContainsRune(s, 0) {
			return inputError
		}
		if int64(len(s)) > stringBytes {
			return limitError
		}
		stringBytes -= int64(len(s))
		if emit != nil {
			return emit(s)
		}
		return nil
	}
	walk = func(depth int) error {
		if depth > 64 {
			return limitError
		}
		t, err := d.Token()
		if err != nil {
			return inputError
		}
		if depth == 0 && containerRoot && t != json.Delim('{') && t != json.Delim('[') {
			return inputError
		}
		switch t := t.(type) {
		case string:
			return str(t)
		case json.Delim:
			if t != '{' && t != '[' {
				return inputError
			}
			seen := make(map[string]bool)
			for d.More() {
				if t == '{' {
					token, err := d.Token()
					name, ok := token.(string)
					if err != nil || !ok || seen[name] {
						return inputError
					}
					if err := str(name); err != nil {
						return err
					}
					seen[name] = true
				} else {
					if items == 0 {
						return limitError
					}
					items--
				}
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || (t == '{' && end != json.Delim('}')) || (t == '[' && end != json.Delim(']')) {
				return inputError
			}
		}
		return nil
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return inputError
	}
	return nil
}

// validEscapes prevents encoding/json's replacement of unpaired surrogates.
func validEscapes(data []byte) bool {
	hex4 := func(b []byte) (uint16, bool) {
		var n uint16
		for _, c := range b {
			n <<= 4
			switch {
			case c >= '0' && c <= '9':
				n += uint16(c - '0')
			case c >= 'a' && c <= 'f':
				n += uint16(c-'a') + 10
			case c >= 'A' && c <= 'F':
				n += uint16(c-'A') + 10
			default:
				return 0, false
			}
		}
		return n, true
	}
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) {
			return false
		}
		if data[i] != 'u' {
			continue
		}
		if len(data)-i < 5 {
			return false
		}
		n, ok := hex4(data[i+1 : i+5])
		if !ok || (n >= 0xdc00 && n <= 0xdfff) {
			return false
		}
		i += 4
		if n >= 0xd800 && n <= 0xdbff {
			if len(data)-i < 7 || data[i+1] != '\\' || data[i+2] != 'u' {
				return false
			}
			low, ok := hex4(data[i+3 : i+7])
			if !ok || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

type objectClosure struct {
	ctx     context.Context
	objects map[model.Digest][]byte
	used    map[model.Digest]bool
	paths   map[string]model.Digest
	aliases map[model.Digest]string
	items   int64
	limits  ResidencyLimits
}

func resolveClosure(ctx context.Context, r ScreenRequest, request, policy []byte, objects []ResolvedObject, l ResidencyLimits) (map[model.Digest][]byte, error) {
	if int64(len(objects)) > min(l.MaxObjects-2, int64(r.Budget.MaxItems)) {
		return nil, limitError
	}
	c := objectClosure{ctx: ctx, objects: make(map[model.Digest][]byte), used: make(map[model.Digest]bool),
		paths: make(map[string]model.Digest), aliases: make(map[model.Digest]string), items: min(l.MaxItems, int64(r.Budget.MaxItems)), limits: l}
	remaining := l.MaxDecodedBytes
	for _, root := range [][]byte{request, policy} {
		if int64(len(root)) > remaining {
			return nil, limitError
		}
		remaining -= int64(len(root))
	}
	for n, object := range objects {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		ref := object.Ref
		if ref.Validate() != nil || len(ref.Path) > 512 || (n > 0 && objects[n-1].Ref.SHA256 >= ref.SHA256) || ref.SHA256 == hash(request) || ref.SHA256 == hash(policy) || ref.ByteSize != int64(len(object.Bytes)) || hash(object.Bytes) != ref.SHA256 {
			return nil, bindingError
		}
		if ref.ByteSize > remaining {
			return nil, limitError
		}
		remaining -= ref.ByteSize
		c.objects[ref.SHA256] = object.Bytes
	}
	if err := visitRefs(r, func(ref model.AssetRef) error {
		if ref == r.Policy {
			return nil
		}
		_, err := c.resolve(ref)
		return err
	}); err != nil {
		return nil, err
	}
	for _, evidence := range r.Evidence {
		if !utf8.Valid(c.objects[evidence.Asset.SHA256]) || bytes.IndexByte(c.objects[evidence.Asset.SHA256], 0) >= 0 {
			return nil, inputError
		}
		if evidence.Conversion != nil {
			if err := c.conversion(r, evidence); err != nil {
				return nil, err
			}
		}
	}
	if len(c.used) != len(objects) {
		return nil, bindingError
	}
	for _, object := range objects {
		if c.aliases[object.Ref.SHA256] != object.Ref.Path {
			return nil, bindingError
		}
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	return c.objects, nil
}

func (c *objectClosure) resolve(ref model.AssetRef) ([]byte, error) {
	data, ok := c.objects[ref.SHA256]
	if !ok || ref.Validate() != nil || len(ref.Path) > 512 || int64(len(data)) != ref.ByteSize {
		return nil, bindingError
	}
	if old, ok := c.paths[ref.Path]; ok && old != ref.SHA256 {
		return nil, bindingError
	}
	c.paths[ref.Path] = ref.SHA256
	if old, ok := c.aliases[ref.SHA256]; !ok || ref.Path < old {
		c.aliases[ref.SHA256] = ref.Path
	}
	c.used[ref.SHA256] = true
	return data, nil
}

func (c *objectClosure) decode(ref model.AssetRef, value any) error {
	if err := contextError(c.ctx); err != nil {
		return err
	}
	data, err := c.resolve(ref)
	if err != nil {
		return err
	}
	if err := inspectJSON(data, c.items, c.limits.MaxDecodedBytes, false, nil); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return decodeError(err)
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(canonical, data) {
		return inputError
	}
	if err := visitRefs(value, func(child model.AssetRef) error {
		if child.SHA256 == ref.SHA256 {
			return bindingError
		}
		return nil
	}); err != nil {
		return err
	}
	var rows func(reflect.Value) error
	rows = func(v reflect.Value) error {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return nil
			}
			return rows(v.Elem())
		}
		switch v.Kind() {
		case reflect.Struct:
			for n := 0; n < v.NumField(); n++ {
				if err := rows(v.Field(n)); err != nil {
					return err
				}
			}
		case reflect.Slice:
			if int64(v.Len()) > c.items {
				return limitError
			}
			c.items -= int64(v.Len())
			for n := 0; n < v.Len(); n++ {
				if err := rows(v.Index(n)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return rows(reflect.ValueOf(value))
}

type conversionQuality struct {
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
}

func (c *objectClosure) conversion(r ScreenRequest, evidence ScreenEvidence) error {
	refs := evidence.Conversion
	var request conversion.ConversionManifest
	var document model.ConvertedDocument
	var outputs []model.AssetRef
	for _, item := range []struct {
		ref   model.AssetRef
		value any
	}{{refs.Request, &request}, {refs.Document, &document}, {refs.Outputs, &outputs}} {
		if err := c.decode(item.ref, item.value); err != nil {
			return err
		}
	}
	if outputs == nil || r.Capture == nil || !validConversionManifest(request) || request.Request.Artifact != r.Capture.Artifact || document.Original != r.Capture.Artifact || document.Validate(int(c.limits.MaxItems)) != nil || document.SchemaVersion != "blocks-v1" || document.RecipeHash != request.Request.Recipe.SHA256 || document.CandidateDigest != request.CandidateDigest || document.ObservationKey != request.ObservationKey || document.ArtifactRole != request.ArtifactRole || document.RawText != evidence.Asset {
		return bindingError
	}
	if err := visitRefs(request, func(ref model.AssetRef) error { _, err := c.resolve(ref); return err }); err != nil {
		return err
	}
	members := make(map[string]model.AssetRef)
	remaining := request.OutputBytes
	for n, ref := range outputs {
		if ref.Validate() != nil || n > 0 && outputs[n-1].Path >= ref.Path {
			return bindingError
		}
		if ref.ByteSize > remaining {
			return limitError
		}
		remaining -= ref.ByteSize
		if _, err := c.resolve(ref); err != nil {
			return err
		}
		members[ref.Path] = ref
	}
	member := func(ref model.AssetRef) error {
		if ref == document.Original.Asset || ref == document.Original.TransportReceipt {
			return nil
		}
		if members[ref.Path] != ref {
			return bindingError
		}
		return nil
	}
	prefix := "results/" + string(refs.Request.SHA256) + "/"
	if refs.Document.Path != prefix+"document.json" || members[refs.Document.Path] != refs.Document || member(evidence.Asset) != nil {
		return bindingError
	}
	if err := visitRefs(document, member); err != nil {
		return err
	}
	var quality conversionQuality
	qualityRef, ok := members[prefix+"quality.json"]
	if !ok {
		return bindingError
	}
	if err := c.decode(qualityRef, &quality); err != nil {
		return err
	}
	if quality.SchemaVersion != "conversion-quality-v1" || quality.ArtifactHash != r.Subject.SHA256 || quality.CandidateDigest != document.CandidateDigest || quality.RequestDigest != refs.Request.SHA256 || quality.RecipeHash != document.RecipeHash || quality.DetectorHash != document.DetectorHash || quality.DocumentHash != refs.Document.SHA256 || !validConversionQuality(quality) {
		return bindingError
	}
	if err := visitRefs(quality, member); err != nil {
		return err
	}
	for _, metric := range quality.Metrics {
		if metric.Validate() != nil {
			return bindingError
		}
	}
	var detector struct {
		Version  string
		Format   string
		Original model.Digest
		Recipe   model.Digest
	}
	detectorRef, ok := members["objects/"+string(document.DetectorHash)]
	if !ok {
		return bindingError
	}
	if err := c.decode(detectorRef, &detector); err != nil {
		return err
	}
	if detector.Version != "detect-v1" || detector.Original != r.Subject.SHA256 || detector.Recipe != request.DetectorHash || (detector.Format != "html" && detector.Format != "docx") || (detector.Format == "html" && textMedia(r.MediaType) != "text/html") || (detector.Format == "docx" && textMedia(r.MediaType) != "application/vnd.openxmlformats-officedocument.wordprocessingml.document") {
		return bindingError
	}
	var maps []model.OffsetMapSegment
	var edits []model.EditLedgerEntry
	var normalized []model.Block
	var raw []struct {
		ID      string
		Kind    model.BlockKind
		Text    string
		Sources []model.Span
	}
	for _, item := range []struct {
		ref   model.AssetRef
		value any
	}{{document.OffsetMap, &maps}, {document.EditLedger, &edits}, {document.RawBlocks, &raw}, {document.NormalizedBlocks, &normalized}} {
		if err := c.decode(item.ref, item.value); err != nil {
			return err
		}
		if err := visitRefs(item.value, member); err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(normalized, document.Blocks) || len(raw) != len(document.Blocks) {
		return bindingError
	}
	for n, block := range document.Blocks {
		if raw[n].ID != block.ID || raw[n].Kind != block.Kind || raw[n].Text != block.RawText || !reflect.DeepEqual(raw[n].Sources, block.SourceSpans) {
			return bindingError
		}
	}
	for n, m := range maps {
		if m.Validate() != nil || m.Ordinal != n {
			return bindingError
		}
	}
	for n, edit := range edits {
		if edit.Validate() != nil || edit.Ordinal != n {
			return bindingError
		}
		var decoded model.DecodedEdit
		if err := c.decode(edit.Evidence, &decoded); err != nil {
			return err
		}
		if decoded.Kind != edit.Kind || decoded.Rule != edit.Rule || decoded.Version != edit.Version || !reflect.DeepEqual(decoded.Inputs, edit.Inputs) || !reflect.DeepEqual(decoded.Outputs, edit.Outputs) || edit.Removed == nil || edit.Replacement == nil || decoded.Removed != string(c.objects[edit.Removed.SHA256]) || decoded.Replacement != string(c.objects[edit.Replacement.SHA256]) {
			return bindingError
		}
	}
	// Spans may resolve only within the already typed closure. They cannot
	// discover another object by hash or by a synthesized path.
	var spans func(reflect.Value) error
	spans = func(v reflect.Value) error {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return nil
			}
			return spans(v.Elem())
		}
		if v.CanInterface() {
			if span, ok := v.Interface().(model.Span); ok {
				if span.Page != nil && *span.Page > r.Budget.MaxPages {
					return limitError
				}
				if span.Validate() != nil {
					return bindingError
				}
				size := r.Subject.ByteSize
				if span.ArtifactHash != r.Subject.SHA256 {
					found := false
					for _, ref := range members {
						if ref.SHA256 == span.ArtifactHash {
							size, found = ref.ByteSize, true
						}
					}
					if !found {
						return bindingError
					}
				}
				if span.Range != nil && span.Range.End > size {
					return bindingError
				}
			}
		}
		switch v.Kind() {
		case reflect.Struct:
			for n := 0; n < v.NumField(); n++ {
				if err := spans(v.Field(n)); err != nil {
					return err
				}
			}
		case reflect.Slice:
			for n := 0; n < v.Len(); n++ {
				if err := spans(v.Index(n)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return spans(reflect.ValueOf(struct {
		Document model.ConvertedDocument
		Quality  conversionQuality
		Maps     []model.OffsetMapSegment
		Edits    []model.EditLedgerEntry
	}{document, quality, maps, edits}))
}
