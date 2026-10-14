package vbpl

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/source/api"
)

// Decoder is a pure, version-pinned source parser for the converter sandbox.
// Construction receives decoded policy configuration, never a transport client.
type Decoder struct {
	ref     api.DecoderRef
	planner *Planner
}

var _ api.Decoder = (*Decoder)(nil)

// NewDecoder validates the pin and copies the planner's approved host policy.
func NewDecoder(ref api.DecoderRef, policy api.SourcePolicy, config Config) (*Decoder, error) {
	if ref.Validate() != nil || ref.Country != "vn" || ref.SourceID != SourceID {
		return nil, reject("INVALID_DECODER")
	}
	planner, err := New(policy, config)
	if err != nil {
		return nil, reject("INVALID_POLICY")
	}
	return &Decoder{ref: ref, planner: planner}, nil
}

// DecodeError is a machine-readable rejection without captured content.
type DecodeError struct{ Code string }

// Error returns only the rejection code.
func (e *DecodeError) Error() string { return "decode: " + e.Code }

func reject(code string) error { return &DecodeError{Code: code} }

func decodedLimits(b model.ResourceBudget) model.RuleLimits {
	return model.RuleLimits{MaxItems: b.MaxItems, MaxBytes: b.MaxInputBytes}
}

func (d *Decoder) check(i api.CapturedInput, country, source string, budget model.ResourceBudget, policy model.Digest) error {
	if d == nil || d.planner == nil || d.ref.Validate() != nil || country != "vn" || source != SourceID || i.Capture.Country != country || i.Capture.SourceID != source || i.Budget != budget || policy != d.planner.policy.Digest {
		return reject("ORIGIN_MISMATCH")
	}
	if err := i.Validate(); err != nil {
		return reject("INVALID_CAPTURE")
	}
	if !utf8.Valid(i.Content) {
		return reject("INVALID_UTF8")
	}
	return nil
}

func media(i api.CapturedInput, allowed ...string) bool {
	kind, params, err := mime.ParseMediaType(i.Capture.MediaType)
	if err != nil {
		return false
	}
	if charset := strings.ToLower(params["charset"]); charset != "" && charset != "utf-8" && charset != "utf8" {
		return false
	}
	for _, value := range allowed {
		if kind == value {
			return true
		}
	}
	return false
}

func observation(i api.CapturedInput, id string, ordinal int) api.DecodedObservation {
	return api.DecodedObservation{SourceID: SourceID, ExternalID: id, VersionKey: string(i.Capture.Asset.SHA256), Capture: i.Capture, Ordinal: ordinal, SourceTree: model.SourceTree{Limits: decodedLimits(i.Budget)}, Limits: decodedLimits(i.Budget)}
}

func anchor(i api.CapturedInput, object string) model.Span {
	return model.Span{ArtifactHash: i.Capture.Asset.SHA256, Space: model.OriginalBytes, Object: object, ReasonCode: "BYTE_MAPPING_UNAVAILABLE"}
}

func field(o *api.DecodedObservation, i api.CapturedInput, name, value, object string) {
	if value == "" {
		return
	}
	o.Fields = append(o.Fields, api.DecodedField{Name: name, Value: value, Sources: []model.Span{anchor(i, object)}, Limits: decodedLimits(i.Budget)})
	if name == "number" {
		normalized := strings.Join(strings.Fields(value), "")
		o.Fields = append(o.Fields, api.DecodedField{Name: "number_normalized", Value: normalized, Sources: []model.Span{anchor(i, object)}, Limits: decodedLimits(i.Budget)})
	}
}

func follow(i api.CapturedInput, window string, policy, hash model.Digest, purpose model.RequestPurpose, locator string, ordinal int) model.FollowUpRequest {
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(window+"\x00"+string(purpose)+"\x00"+locator+"\x00"+i.Capture.CaptureKey)))
	return model.FollowUpRequest{RequestKey: key, WindowKey: window, ParentCapture: i.Capture.CaptureKey, Purpose: purpose, URL: locator, Ordinal: ordinal, PolicyHash: policy, BudgetHash: hash, Budget: i.Budget}
}

func pageValue(i api.CapturedInput, r api.DiscoverRequest) api.DecodedDiscoveryPage {
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(r.WindowKey+"\x00"+r.SliceKey+"\x00"+i.Capture.CaptureKey)))
	return api.DecodedDiscoveryPage{PageKey: key, Capture: i.Capture, Cursor: model.CursorPosition{WindowKey: r.WindowKey, SliceKey: r.SliceKey, InitialCursor: r.Cursor, CommittedCursor: r.Cursor, NextCursor: r.Cursor}, Limits: decodedLimits(r.Budget)}
}

func finding(i api.CapturedInput, reason string) model.Finding {
	return model.Finding{ReasonCode: reason, Severity: model.SeverityBlocking, Scope: model.GapSourceWindow, ScopeKey: i.Capture.CaptureKey, Evidence: []model.AssetRef{i.Capture.Asset}}
}

func inventory(i api.CapturedInput, locator, name string, ordinal int) model.ArtifactInventoryEntry {
	ext := strings.ToLower(path.Ext(name))
	format := strings.TrimPrefix(ext, ".")
	if format == "jpg" {
		format = "jpeg"
	}
	if format == "tif" {
		format = "tiff"
	}
	if format == "htm" {
		format = "html"
	}
	if format == "" || !model.ArtifactFormatValid(format) {
		format = "unknown"
	}
	role := "main"
	lower := foldFilename(name)
	for _, label := range []string{"phụ lục", "phu luc", "phu_luc", "phuluc", "biểu mẫu", "bieu mau", "bieu_mau", "bieumau", "mau so", "ban hanh kem theo", "danh mục", "danh muc", "đính kèm", "dinh kem", "appendix"} {
		if strings.Contains(lower, label) {
			role = "appendix"
			break
		}
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(SourceID+"\x00"+i.Capture.CaptureKey+"\x00"+strconv.Itoa(ordinal)+"\x00"+name)))
	return model.ArtifactInventoryEntry{ArtifactKey: key, URL: locator, DeclaredFilename: name, Format: format, Role: role, Ordinal: ordinal, Required: true}
}

// foldFilename matches v1's accent-insensitive Vietnamese filename policy.
// Removing combining marks also handles decomposed source spellings.
func foldFilename(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		for _, group := range []string{"aàáảãạăằắẳẵặâầấẩẫậ", "eèéẻẽẹêềếểễệ", "iìíỉĩị", "oòóỏõọôồốổỗộơờớởỡợ", "uùúủũụưừứửữự", "yỳýỷỹỵ", "dđ"} {
			if strings.ContainsRune(group, r) {
				return rune(group[0])
			}
		}
		return r
	}, strings.ToLower(name))
}

// resolvedLink rejects traversal before resolution so normalization cannot hide
// a hostile relative path. The planner then applies its exact endpoint policy.
func resolvedLink(base, raw string) (string, error) {
	ref, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || ref.User != nil || ref.Fragment != "" || strings.ContainsAny(ref.Path, "\\") {
		return "", reject("UNAPPROVED_LINK")
	}
	for _, segment := range strings.Split(ref.Path, "/") {
		if segment == "." || segment == ".." || strings.Contains(segment, "%") {
			return "", reject("UNAPPROVED_LINK")
		}
	}
	origin, err := url.Parse(base)
	if err != nil {
		return "", reject("UNAPPROVED_LINK")
	}
	return origin.ResolveReference(ref).String(), nil
}

func validatePage(p api.DecodedDiscoveryPage) (api.DecodedDiscoveryPage, error) {
	if err := p.Validate(); err != nil {
		return api.DecodedDiscoveryPage{}, reject("INVALID_DECODED_PAGE")
	}
	return p, nil
}

func validateDetail(r api.DecodedDetailResult) (api.DecodedDetailResult, error) {
	if err := r.Validate(); err != nil {
		return api.DecodedDetailResult{}, reject("INVALID_DECODED_DETAIL")
	}
	return r, nil
}

// jsonPreflight bounds ignored values too and rejects duplicate object keys.
// It reads one token at a time before typed decoding, with no entity resolution.
func jsonPreflight(i api.CapturedInput) error {
	if !media(i, "application/json") {
		return reject("CONTENT_TYPE")
	}
	decoder := json.NewDecoder(bytes.NewReader(i.Content))
	decoder.UseNumber()
	count := 0
	read := func() (json.Token, error) {
		count++
		if count > i.Budget.MaxItems {
			return nil, reject("ELEMENT_BOUND")
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, reject("MALFORMED_JSON")
		}
		return token, nil
	}
	var value func(int) error
	value = func(depth int) error {
		if depth > 128 {
			return reject("DEPTH_BOUND")
		}
		token, err := read()
		if err != nil {
			return err
		}
		delimiter, container := token.(json.Delim)
		if !container {
			return nil
		}
		if delimiter != '{' && delimiter != '[' {
			return reject("MALFORMED_JSON")
		}
		seen := map[string]bool{}
		for decoder.More() {
			if delimiter == '{' {
				key, err := read()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return reject("DUPLICATE_JSON_KEY")
				}
				seen[name] = true
				for _, field := range []string{"success", "data", "total", "items", "id", "docNum", "title", "docAbs", "issueDate", "effFrom", "effTo", "agencyName", "organization", "docType", "effStatus", "code", "name", "hasContent", "isConsolidatedDocument", "documentContent", "content", "references", "referenceType", "targetDocument", "fileName", "presignedUrl", "size", "relatedType", "documentNamesByType", "key", "children"} {
					if name != field && strings.EqualFold(name, field) {
						return reject("JSON_FIELD_CASE")
					}
				}
			}
			if err := value(depth + 1); err != nil {
				return err
			}
		}
		end, err := read()
		if err != nil {
			return err
		}
		if delimiter == '{' && end != json.Delim('}') || delimiter == '[' && end != json.Delim(']') {
			return reject("MALFORMED_JSON")
		}
		return nil
	}
	if err := value(1); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return reject("MALFORMED_JSON")
	}
	return nil
}

// exactObject reads recognized fields by their exact source spelling. Unknown
// fields remain allowed and are bounded by jsonPreflight before typed decoding.
func exactObject(raw []byte, fields map[string]any) error {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return reject("MALFORMED_JSON")
	}
	for name, target := range fields {
		if value, ok := object[name]; ok {
			if err := json.Unmarshal(value, target); err != nil {
				return reject("MALFORMED_JSON")
			}
		}
	}
	return nil
}

type nativeCode struct {
	Code string `json:"code"`
	Name string `json:"name"`
}
type nativeDocument struct {
	ID           string     `json:"id"`
	Number       string     `json:"docNum"`
	Title        string     `json:"title"`
	Abstract     string     `json:"docAbs"`
	IssueDate    string     `json:"issueDate"`
	Effective    string     `json:"effFrom"`
	Expiry       string     `json:"effTo"`
	Agency       string     `json:"agencyName"`
	Organization nativeCode `json:"organization"`
	Type         nativeCode `json:"docType"`
	Status       nativeCode `json:"effStatus"`
	HasContent   *bool      `json:"hasContent"`
	Consolidated *bool      `json:"isConsolidatedDocument"`
	Content      *struct {
		Content string `json:"content"`
	} `json:"documentContent"`
	References []struct {
		Kind   *int `json:"referenceType"`
		Target struct {
			ID     json.RawMessage `json:"id"`
			Number string          `json:"docNum"`
			Title  string          `json:"title"`
		} `json:"targetDocument"`
	} `json:"references"`
}

func (n *nativeDocument) UnmarshalJSON(raw []byte) error {
	return exactObject(raw, map[string]any{
		"id": &n.ID, "docNum": &n.Number, "title": &n.Title,
		"docAbs": &n.Abstract, "issueDate": &n.IssueDate,
		"effFrom": &n.Effective, "effTo": &n.Expiry,
		"agencyName": &n.Agency, "organization": &n.Organization,
		"docType": &n.Type, "effStatus": &n.Status,
		"hasContent": &n.HasContent, "isConsolidatedDocument": &n.Consolidated,
		"documentContent": &n.Content, "references": &n.References,
	})
}

func (n *nativeCode) UnmarshalJSON(raw []byte) error {
	// Null code objects remain absent observations.
	if string(raw) == "null" {
		return nil
	}
	return exactObject(raw, map[string]any{"code": &n.Code, "name": &n.Name})
}

func nativeObservation(i api.CapturedInput, n nativeDocument, ordinal int, object string) (api.DecodedObservation, error) {
	if !safeID(n.ID) {
		return api.DecodedObservation{}, reject("MISSING_ID")
	}
	if n.Number == "" && n.Title == "" {
		return api.DecodedObservation{}, reject("MISSING_METADATA")
	}
	o := observation(i, n.ID, ordinal)
	for _, f := range []struct{ name, value, key string }{
		{"number", n.Number, "docNum"}, {"title", n.Title, "title"}, {"abstract", n.Abstract, "docAbs"}, {"issuer", n.Agency, "agencyName"}, {"issuer_code", n.Organization.Code, "organization/code"}, {"type", n.Type.Name, "docType/name"}, {"type_code", n.Type.Code, "docType/code"}, {"status", n.Status.Code, "effStatus/code"}, {"issued_at", n.IssueDate, "issueDate"}, {"effective_at", n.Effective, "effFrom"}, {"expires_at", n.Expiry, "effTo"},
	} {
		field(&o, i, f.name, f.value, object+"/"+f.key)
	}
	if n.HasContent != nil {
		field(&o, i, "has_content", strconv.FormatBool(*n.HasContent), object+"/hasContent")
	}
	if n.Consolidated != nil {
		field(&o, i, "is_consolidated", strconv.FormatBool(*n.Consolidated), object+"/isConsolidatedDocument")
	}
	if n.Content != nil {
		field(&o, i, "content_html", n.Content.Content, object+"/documentContent/content")
	}
	for index, r := range n.References {
		field(&o, i, "relation_target_number", r.Target.Number, fmt.Sprintf("%s/references/%d/targetDocument/docNum", object, index))
		field(&o, i, "relation_target_title", r.Target.Title, fmt.Sprintf("%s/references/%d/targetDocument/title", object, index))
		if r.Kind == nil {
			continue
		}
		target, err := nativeID(r.Target.ID)
		if err != nil {
			return api.DecodedObservation{}, err
		}
		if target == "" {
			target = r.Target.Number
		}
		if target == "" {
			return api.DecodedObservation{}, reject("MISSING_RELATION_TARGET")
		}
		o.Relations = append(o.Relations, api.DecodedRelation{Kind: relationKind(*r.Kind), Target: target, Sources: []model.Span{anchor(i, fmt.Sprintf("%s/references/%d", object, index))}, Limits: decodedLimits(i.Budget)})
	}
	return o, nil
}

func relationFindings(i api.CapturedInput, n nativeDocument, object string) []model.Finding {
	var findings []model.Finding
	for index, relation := range n.References {
		if relation.Kind != nil {
			continue
		}
		gap := finding(i, "INCOMPLETE_RELATION")
		gap.Spans = []model.Span{anchor(i, fmt.Sprintf("%s/references/%d", object, index))}
		findings = append(findings, gap)
	}
	return findings
}

func nativeID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var value string
	if raw[0] == '"' {
		if json.Unmarshal(raw, &value) != nil {
			return "", reject("INVALID_NATIVE_ID")
		}
	} else {
		value = string(raw)
		if strings.Trim(value, "0123456789") != "" {
			return "", reject("INVALID_NATIVE_ID")
		}
	}
	if !safeID(value) {
		return "", reject("INVALID_NATIVE_ID")
	}
	return value, nil
}

func relationKind(code int) string {
	// Keep the raw code in the kind so policy can interpret it later.
	return "vbpl_type_" + strconv.Itoa(code)
}

// Listing mirrors doc/all JSON decoding. Only an explicit total consistent with
// the requested page size can prove exhaustion; failures never advance cursors.
func (d *Decoder) Listing(i api.CapturedInput, r api.DiscoverRequest) (api.DecodedDiscoveryPage, error) {
	if err := r.Validate(); err != nil {
		return api.DecodedDiscoveryPage{}, reject("INVALID_REQUEST")
	}
	if err := d.check(i, r.Country, r.SourceID, r.Budget, r.PolicyHash); err != nil {
		return api.DecodedDiscoveryPage{}, err
	}
	plan, err := d.planner.Listing(r)
	if err != nil {
		return api.DecodedDiscoveryPage{}, reject("INVALID_REQUEST")
	}
	if err := jsonPreflight(i); err != nil {
		return api.DecodedDiscoveryPage{}, err
	}
	var response struct {
		Success bool
		Data    json.RawMessage
	}
	if exactObject(i.Content, map[string]any{"success": &response.Success, "data": &response.Data}) != nil || !response.Success {
		return api.DecodedDiscoveryPage{}, reject("MALFORMED_LISTING")
	}
	var data struct {
		Total *int
		Items *[]nativeDocument
	}
	if exactObject(response.Data, map[string]any{"total": &data.Total, "items": &data.Items}) != nil || data.Total == nil || data.Items == nil || *data.Total < 0 {
		return api.DecodedDiscoveryPage{}, reject("MALFORMED_LISTING")
	}
	page, _ := strconv.Atoi(plan.Cursor)
	size := min(50, r.Budget.MaxItems)
	items := *data.Items
	if len(items) > size || len(items) > *data.Total {
		return api.DecodedDiscoveryPage{}, reject("INCONSISTENT_TOTAL")
	}
	out := pageValue(i, r)
	seen := map[string]bool{}
	for ordinal, n := range items {
		if seen[n.ID] {
			return api.DecodedDiscoveryPage{}, reject("DUPLICATE_ID")
		}
		seen[n.ID] = true
		o, err := nativeObservation(i, n, ordinal, fmt.Sprintf("/data/items/%d", ordinal))
		if err != nil {
			return api.DecodedDiscoveryPage{}, err
		}
		out.Observations = append(out.Observations, o)
		out.Findings = append(out.Findings, relationFindings(i, n, fmt.Sprintf("/data/items/%d", ordinal))...)
		out.FollowUps = append(out.FollowUps, follow(i, r.WindowKey, r.PolicyHash, r.BudgetHash, model.PurposeDetail, gateway+"/doc/"+n.ID, ordinal))
	}
	// Avoid page*size overflow and reject short pages before a declared tail.
	remaining := *data.Total
	if page-1 > remaining/size {
		remaining = 0
	} else {
		remaining -= (page - 1) * size
	}
	if remaining == len(items) {
		out.Cursor.Complete = true
	} else if len(items) != size || page >= r.Budget.MaxPages {
		out.Findings = append(out.Findings, finding(i, "INCOMPLETE_LISTING"))
	}
	return validatePage(out)
}

// Detail dispatches only by trusted planner metadata. Captured shapes cannot
// substitute another endpoint's observation or suppress required follow-ups.
func (d *Decoder) Detail(i api.CapturedInput, r api.DetailRequest) (api.DecodedDetailResult, error) {
	if err := r.Validate(); err != nil {
		return api.DecodedDetailResult{}, reject("INVALID_REQUEST")
	}
	if err := d.check(i, r.Country, r.SourceID, r.Budget, r.PolicyHash); err != nil {
		return api.DecodedDetailResult{}, err
	}
	origin, err := d.planner.Detail(r)
	if err != nil {
		return api.DecodedDetailResult{}, reject("INVALID_REQUEST")
	}
	if err := jsonPreflight(i); err != nil {
		return api.DecodedDetailResult{}, err
	}
	var response struct {
		Success bool
		Data    json.RawMessage
	}
	if exactObject(i.Content, map[string]any{"success": &response.Success, "data": &response.Data}) != nil || !response.Success || len(response.Data) == 0 || string(response.Data) == "null" {
		return api.DecodedDetailResult{}, reject("MALFORMED_DETAIL")
	}
	out := api.DecodedDetailResult{Observation: observation(i, r.ExternalID, 0), Limits: decodedLimits(r.Budget)}
	kind := origin.ExpectedResponseKind
	if kind == "files" || kind == "tree" {
		if response.Data[0] != '[' {
			return api.DecodedDetailResult{}, reject("RESPONSE_KIND_MISMATCH")
		}
		var entries []json.RawMessage
		if json.Unmarshal(response.Data, &entries) != nil {
			return api.DecodedDetailResult{}, reject("MALFORMED_DETAIL")
		}
		for _, raw := range entries {
			var entry map[string]json.RawMessage
			if json.Unmarshal(raw, &entry) != nil || entry == nil {
				return api.DecodedDetailResult{}, reject("RESPONSE_KIND_MISMATCH")
			}
			if kind == "files" {
				if _, ok := entry["fileName"]; !ok {
					return api.DecodedDetailResult{}, reject("RESPONSE_KIND_MISMATCH")
				}
			} else {
				if _, ok := entry["id"]; !ok {
					return api.DecodedDetailResult{}, reject("RESPONSE_KIND_MISMATCH")
				}
				if _, ok := entry["fileName"]; ok {
					return api.DecodedDetailResult{}, reject("RESPONSE_KIND_MISMATCH")
				}
				if _, ok := entry["presignedUrl"]; ok {
					return api.DecodedDetailResult{}, reject("RESPONSE_KIND_MISMATCH")
				}
			}
		}
		if len(entries) == 0 {
			reason := "EMPTY_FILES"
			if kind == "tree" {
				reason = "EMPTY_TREE"
			}
			out.Findings = append(out.Findings, finding(i, reason))
			return validateDetail(out)
		}
		if kind == "files" {
			return d.decodeFiles(i, r, entries, out)
		}
		return decodeTree(i, entries, out)
	}
	if response.Data[0] != '{' {
		return api.DecodedDetailResult{}, reject("RESPONSE_KIND_MISMATCH")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(response.Data, &object) != nil {
		return api.DecodedDetailResult{}, reject("MALFORMED_DETAIL")
	}
	diagram, hasDiagram := object["documentNamesByType"]
	_, hasID := object["id"]
	if kind == "diagram" {
		if !hasDiagram || hasID {
			return api.DecodedDetailResult{}, reject("RESPONSE_KIND_MISMATCH")
		}
		return decodeDiagram(i, diagram, out)
	}
	if !hasID || hasDiagram {
		return api.DecodedDetailResult{}, reject("RESPONSE_KIND_MISMATCH")
	}

	var n nativeDocument
	if json.Unmarshal(response.Data, &n) != nil || n.ID != r.ExternalID {
		return api.DecodedDetailResult{}, reject("DETAIL_ID_MISMATCH")
	}
	out.Observation, err = nativeObservation(i, n, 0, "/data")
	out.Findings = append(out.Findings, relationFindings(i, n, "/data")...)
	if err != nil {
		return api.DecodedDetailResult{}, err
	}
	plans, err := d.planner.DetailFollowUps(origin, r.ExternalID, i.Capture.CaptureKey)
	if err != nil {
		return api.DecodedDetailResult{}, reject("INVALID_FOLLOW_UP")
	}
	for _, plan := range plans {
		out.FollowUps = append(out.FollowUps, follow(i, r.WindowKey, r.PolicyHash, r.BudgetHash, model.PurposeDetail, plan.URL, len(out.FollowUps)))
	}
	treeURL := gateway + "/doc/provision/tree/" + r.ExternalID
	if apiURL(treeURL, model.PurposeDetail, nil) != nil {
		return api.DecodedDetailResult{}, reject("INVALID_FOLLOW_UP")
	}
	out.FollowUps = append(out.FollowUps, follow(i, r.WindowKey, r.PolicyHash, r.BudgetHash, model.PurposeDetail, treeURL, len(out.FollowUps)))
	return validateDetail(out)
}

func (d *Decoder) decodeFiles(i api.CapturedInput, r api.DetailRequest, entries []json.RawMessage, out api.DecodedDetailResult) (api.DecodedDetailResult, error) {
	seen := map[string]bool{}
	for index, raw := range entries {
		var file struct {
			Name        string `json:"fileName"`
			URL         string `json:"presignedUrl"`
			Size        int64  `json:"size"`
			RelatedType *int   `json:"relatedType"`
		}
		if json.Unmarshal(raw, &file) != nil || file.Name == "" || file.URL == "" || file.Size < 0 {
			return api.DecodedDetailResult{}, reject("MISSING_REQUIRED_ARTIFACT")
		}
		if seen[file.URL] {
			continue
		}
		seen[file.URL] = true
		locator, err := resolvedLink(gateway+"/", file.URL)
		if err != nil || apiURL(locator, model.PurposeArtifact, d.planner.config.ArtifactOrigins) != nil {
			return api.DecodedDetailResult{}, reject("UNAPPROVED_ARTIFACT")
		}
		a := inventory(i, locator, file.Name, index)
		if a.Role == "main" && a.Format == "pdf" {
			a.Role = "original_scan"
		}
		out.Artifacts = append(out.Artifacts, a)
		if file.RelatedType != nil {
			field(&out.Observation, i, "file_related_type", strconv.Itoa(*file.RelatedType), fmt.Sprintf("/data/%d/relatedType", index))
		}
	}
	// Preserve all files, but order qualified text ahead of scans as v1 does.
	sort.SliceStable(out.Artifacts, func(a, b int) bool {
		x, y := out.Artifacts[a], out.Artifacts[b]
		rank := func(a model.ArtifactInventoryEntry) int {
			role := 0
			if a.Role == "appendix" {
				role = 10
			}
			switch a.Format {
			case "docx":
				return role
			case "html":
				return role + 1
			case "doc":
				return role + 2
			case "pdf":
				return 30 + role
			default:
				return 50 + role
			}
		}
		return rank(x) < rank(y)
	})
	for ordinal := range out.Artifacts {
		out.Artifacts[ordinal].Ordinal = ordinal
		a := out.Artifacts[ordinal]
		out.FollowUps = append(out.FollowUps, follow(i, r.WindowKey, r.PolicyHash, r.BudgetHash, model.PurposeArtifact, a.URL, ordinal))
	}
	return validateDetail(out)
}

func decodeDiagram(i api.CapturedInput, raw json.RawMessage, out api.DecodedDetailResult) (api.DecodedDetailResult, error) {
	var groups map[string][]struct {
		ID   json.RawMessage `json:"id"`
		Name string          `json:"name"`
	}
	if json.Unmarshal(raw, &groups) != nil || groups == nil {
		return api.DecodedDetailResult{}, reject("MALFORMED_DIAGRAM")
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		code, err := strconv.Atoi(key)
		if err != nil {
			return api.DecodedDetailResult{}, reject("INVALID_RELATION_CODE")
		}
		for ordinal, n := range groups[key] {
			id, err := nativeID(n.ID)
			if err != nil || id == "" {
				return api.DecodedDetailResult{}, reject("MISSING_RELATION_TARGET")
			}
			object := fmt.Sprintf("/data/documentNamesByType/%s/%d", key, ordinal)
			out.Observation.Relations = append(out.Observation.Relations, api.DecodedRelation{Kind: relationKind(code), Target: id, Sources: []model.Span{anchor(i, object)}, Limits: decodedLimits(i.Budget)})
			field(&out.Observation, i, "relation_target_title", n.Name, object+"/name")
		}
	}
	return validateDetail(out)
}

type nativeTreeNode struct {
	ID       string           `json:"id"`
	Key      string           `json:"key"`
	Title    string           `json:"title"`
	Children []nativeTreeNode `json:"children"`
	Content  struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	} `json:"content"`
}

func decodeTree(i api.CapturedInput, entries []json.RawMessage, out api.DecodedDetailResult) (api.DecodedDetailResult, error) {
	out.Observation.SourceTree.Limits = decodedLimits(i.Budget)
	seen := map[string]bool{}
	var walk func([]nativeTreeNode, string, string) error
	walk = func(entries []nativeTreeNode, parent, object string) error {
		for index, n := range entries {
			if n.ID == "" || seen[n.ID] {
				return reject("MALFORMED_TREE")
			}
			seen[n.ID] = true
			location := fmt.Sprintf("%s/%d", object, index)
			out.Observation.SourceTree.Nodes = append(out.Observation.SourceTree.Nodes, model.SourceNode{ID: n.ID, ParentID: parent, Label: n.Title, Sources: []model.Span{anchor(i, location)}})
			field(&out.Observation, i, "tree_content", n.Content.Content, location+"/content/content")
			field(&out.Observation, i, "tree_key", n.Key, location+"/key")
			if err := walk(n.Children, n.ID, location+"/children"); err != nil {
				return err
			}
		}
		return nil
	}
	// Decode each top-level subtree once. Repeated RawMessage decoding at each
	// level would copy the same nested bytes up to the depth limit.
	for index, raw := range entries {
		var node nativeTreeNode
		if json.Unmarshal(raw, &node) != nil {
			return api.DecodedDetailResult{}, reject("MALFORMED_TREE")
		}
		if node.ID == "" || seen[node.ID] {
			return api.DecodedDetailResult{}, reject("MALFORMED_TREE")
		}
		seen[node.ID] = true
		location := fmt.Sprintf("/data/%d", index)
		out.Observation.SourceTree.Nodes = append(out.Observation.SourceTree.Nodes, model.SourceNode{ID: node.ID, Label: node.Title, Sources: []model.Span{anchor(i, location)}})
		field(&out.Observation, i, "tree_content", node.Content.Content, location+"/content/content")
		field(&out.Observation, i, "tree_key", node.Key, location+"/key")
		if err := walk(node.Children, node.ID, location+"/children"); err != nil {
			return api.DecodedDetailResult{}, err
		}
	}
	return validateDetail(out)
}
