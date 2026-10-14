package vanban

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"mime"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/source/api"
)

// Decoder is a pure, version-pinned source parser for the converter sandbox.
// Construction receives decoded policy configuration, never a transport client.
type Decoder struct {
	ref api.DecoderRef
	planner *Planner
}

var _ api.Decoder = (*Decoder)(nil)

// NewDecoder validates the pin and copies the planner's approved host policy.
func NewDecoder(ref api.DecoderRef, policy api.SourcePolicy) (*Decoder, error) {
	if ref.Validate() != nil || ref.Country != "vn" || ref.SourceID != SourceID { return nil, reject("INVALID_DECODER") }
	planner, err := New(policy)
	if err != nil { return nil, reject("INVALID_POLICY") }
	return &Decoder{ref: ref, planner: planner}, nil
}

// DecodeError is a machine-readable rejection without captured content.
type DecodeError struct { Code string }

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
	if err := i.Validate(); err != nil { return reject("INVALID_CAPTURE") }
	if !utf8.Valid(i.Content) { return reject("INVALID_UTF8") }
	return nil
}

func media(i api.CapturedInput, allowed ...string) bool {
	kind, params, err := mime.ParseMediaType(i.Capture.MediaType)
	if err != nil { return false }
	if charset := strings.ToLower(params["charset"]); charset != "" && charset != "utf-8" && charset != "utf8" { return false }
	for _, value := range allowed { if kind == value { return true } }
	return false
}

func observation(i api.CapturedInput, id string, ordinal int) api.DecodedObservation {
	return api.DecodedObservation{SourceID: SourceID, ExternalID: id, VersionKey: string(i.Capture.Asset.SHA256), Capture: i.Capture, Ordinal: ordinal, SourceTree: model.SourceTree{Limits: decodedLimits(i.Budget)}, Limits: decodedLimits(i.Budget)}
}

func anchor(i api.CapturedInput, object string) model.Span {
	return model.Span{ArtifactHash: i.Capture.Asset.SHA256, Space: model.OriginalBytes, Object: object, ReasonCode: "BYTE_MAPPING_UNAVAILABLE"}
}

func field(o *api.DecodedObservation, i api.CapturedInput, name, value, object string) {
	if value == "" { return }
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
	if format == "jpg" { format = "jpeg" }
	if format == "tif" { format = "tiff" }
	if format == "htm" { format = "html" }
	if format == "" || !model.ArtifactFormatValid(format) { format = "unknown" }
	role := "main"
	lower := strings.ToLower(name)
	for _, label := range []string{"phụ lục", "phu luc", "phu_luc", "phuluc", "biểu mẫu", "bieu mau", "bieu_mau", "danh mục", "danh muc", "đính kèm", "dinh kem", "appendix"} {
		if strings.Contains(lower, label) { role = "appendix"; break }
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(SourceID+"\x00"+i.Capture.CaptureKey+"\x00"+strconv.Itoa(ordinal)+"\x00"+name)))
	return model.ArtifactInventoryEntry{ArtifactKey: key, URL: locator, DeclaredFilename: name, Format: format, Role: role, Ordinal: ordinal, Required: true}
}

// resolvedLink rejects traversal before resolution so normalization cannot hide
// a hostile relative path. The planner then applies its exact endpoint policy.
func resolvedLink(base, raw string) (string, error) {
	ref, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || ref.User != nil || ref.Fragment != "" || strings.ContainsAny(ref.Path, "\\") { return "", reject("UNAPPROVED_LINK") }
	for _, segment := range strings.Split(ref.Path, "/") {
		if segment == "." || segment == ".." || strings.Contains(segment, "%") { return "", reject("UNAPPROVED_LINK") }
	}
	origin, err := url.Parse(base)
	if err != nil { return "", reject("UNAPPROVED_LINK") }
	return origin.ResolveReference(ref).String(), nil
}

func validatePage(p api.DecodedDiscoveryPage) (api.DecodedDiscoveryPage, error) {
	if err := p.Validate(); err != nil { return api.DecodedDiscoveryPage{}, reject("INVALID_DECODED_PAGE") }
	return p, nil
}

func validateDetail(r api.DecodedDetailResult) (api.DecodedDetailResult, error) {
	if err := r.Validate(); err != nil { return api.DecodedDetailResult{}, reject("INVALID_DECODED_DETAIL") }
	return r, nil
}

// htmlNode is a bounded tokenizer tree, not a browser DOM. It keeps explicit
// source element boundaries and never runs scripts or resolves external assets.
type htmlNode struct {
	tag string
	attrs []html.Attribute
	children []*htmlNode
	parent *htmlNode
	text string
	start int
	end int
}

func parseHTML(i api.CapturedInput) (*htmlNode, error) {
	if !media(i, "text/html", "application/xhtml+xml") { return nil, reject("CONTENT_TYPE") }
	root := &htmlNode{}
	stack := []*htmlNode{root}
	z := html.NewTokenizer(bytes.NewReader(i.Content))
	z.SetMaxBuf(int(i.Budget.MaxInputBytes))
	offset, count := 0, 0
	for {
		kind := z.Next()
		if kind == html.ErrorToken {
			if z.Err() != io.EOF || len(stack) != 1 || offset != len(i.Content) { return nil, reject("MALFORMED_HTML") }
			break
		}
		count++
		if count > i.Budget.MaxItems { return nil, reject("ELEMENT_BOUND") }
		start := offset
		offset += len(z.Raw())
		t := z.Token()
		switch kind {
		case html.StartTagToken, html.SelfClosingTagToken:
			if len(t.Attr) > i.Budget.MaxItems-count { return nil, reject("ELEMENT_BOUND") }
			count += len(t.Attr)
			seen := make(map[string]bool, len(t.Attr))
			for _, a := range t.Attr {
				if seen[a.Key] { return nil, reject("DUPLICATE_HTML_ATTRIBUTE") }
				seen[a.Key] = true
			}
			n := &htmlNode{tag: t.Data, attrs: t.Attr, start: start, end: offset}
			parent := stack[len(stack)-1]
			n.parent = parent
			parent.children = append(parent.children,n)
			void := strings.Contains(" area base br col embed hr img input link meta param source track wbr ", " "+t.Data+" ")
			if !void && kind != html.SelfClosingTagToken {
				if len(stack) >= 129 { return nil, reject("DEPTH_BOUND") }
				stack = append(stack,n)
			}
		case html.EndTagToken:
			if len(stack) <= 1 || stack[len(stack)-1].tag != t.Data { return nil, reject("MALFORMED_HTML") }
			stack[len(stack)-1].end = offset
			stack = stack[:len(stack)-1]
		case html.TextToken:
			parent := stack[len(stack)-1]
			parent.children = append(parent.children,&htmlNode{text: t.Data, start: start, end: offset})
		}
	}
	if len(root.children) == 0 { return nil, reject("MALFORMED_HTML") }
	return root,nil
}

func attr(n *htmlNode, name string) string {
	for _, a := range n.attrs { if a.Key == name { return a.Val } }
	return ""
}

func class(n *htmlNode, value string) bool {
	for _, c := range strings.Fields(attr(n,"class")) { if c == value { return true } }
	return false
}

func nodes(n *htmlNode, match func(*htmlNode) bool) []*htmlNode {
	var out []*htmlNode
	var walk func(*htmlNode)
	walk = func(n *htmlNode) {
		if match(n) { out = append(out,n) }
		for _, child := range n.children { walk(child) }
	}
	walk(n)
	return out
}

func text(n *htmlNode) string {
	var b strings.Builder
	var walk func(*htmlNode)
	walk = func(n *htmlNode) {
		if n.tag == "script" || n.tag == "style" { return }
		b.WriteString(n.text)
		for _, child := range n.children { walk(child) }
		if n.tag == "br" || n.tag == "p" { b.WriteByte(' ') }
	}
	walk(n)
	return strings.Join(strings.Fields(b.String())," ")
}

func nodeField(o *api.DecodedObservation, i api.CapturedInput, name string, n *htmlNode) {
	value := text(n)
	if value == "" { return }
	span := model.Span{ArtifactHash: i.Capture.Asset.SHA256, Space: model.OriginalBytes, Range: &model.ByteRange{Start: int64(n.start), End: int64(n.end)}}
	o.Fields = append(o.Fields,api.DecodedField{Name: name, Value: value, Sources: []model.Span{span}, Limits: decodedLimits(i.Budget)})
	if name == "number" { o.Fields = append(o.Fields,api.DecodedField{Name: "number_normalized", Value: strings.Join(strings.Fields(value),""), Sources: []model.Span{span}, Limits: decodedLimits(i.Budget)}) }
}

// listingNumber removes only the legacy grid sequence before a full number.
// The observed number and its byte span remain unchanged.
func listingNumber(value string) string {
	value = strings.Join(strings.Fields(value), "")
	prefix, number, ok := strings.Cut(value, ".")
	if !ok || prefix == "" || strings.Trim(prefix, "0123456789") != "" { return value }
	parts := strings.SplitN(number, "/", 3)
	if len(parts) != 3 || parts[0] == "" || strings.Trim(parts[0], "0123456789") != "" || len(parts[1]) != 4 || strings.Trim(parts[1], "0123456789") != "" { return value }
	return number
}

func attachmentLink(n *htmlNode, region *htmlNode) bool {
	for p := n.parent; p != nil; p = p.parent {
		if class(p, "bl-doc-file") || class(p, "bl-doc-files") { return true }
		if p.tag == "tr" {
			for _, cell := range p.children {
				if cell.tag == "td" || cell.tag == "th" {
					return strings.Trim(strings.ToLower(text(cell)), " :") == "tài liệu đính kèm"
				}
			}
		}
		if p == region { break }
	}
	return false
}

func metadataName(label string) string {
	switch strings.Trim(strings.ToLower(strings.Join(strings.Fields(label)," ")), " :") {
	case "số ký hiệu", "số, ký hiệu", "số/ký hiệu", "số/kí hiệu": return "number"
	case "trích yếu": return "title"
	case "loại văn bản": return "type"
	case "thể loại": return "source_category"
	case "cơ quan ban hành": return "issuer"
	case "người ký": return "signer"
	case "ngày ban hành": return "issued_at"
	case "ngày hiệu lực", "ngày có hiệu lực": return "effective_at"
	case "ngày hết hiệu lực": return "expires_at"
	case "công báo": return "gazette_number"
	}
	return ""
}

func metadata(o *api.DecodedObservation, i api.CapturedInput, region *htmlNode) {
	for _, row := range nodes(region,func(n *htmlNode) bool { return n.tag == "tr" }) {
		var cells []*htmlNode
		for _, c := range row.children { if c.tag == "td" || c.tag == "th" { cells=append(cells,c) } }
		if len(cells) != 2 { continue }
		if name := metadataName(text(cells[0])); name != "" { nodeField(o,i,name,cells[1]) }
	}
}

func pageNumber(r api.DiscoverRequest) (int,error) {
	if r.Cursor == "" { return 1,nil }
	page,err := strconv.Atoi(r.Cursor)
	if err != nil || page < 1 || page > r.Budget.MaxPages { return 0,reject("INVALID_CURSOR") }
	return page,nil
}

func (d *Decoder) artifact(i api.CapturedInput, r api.DetailRequest, locator, name string, ordinal int) (model.ArtifactInventoryEntry, model.FollowUpRequest, error) {
	a := inventory(i,locator,name,ordinal)
	_,err := d.planner.Artifact(api.ArtifactRequest{Country: r.Country, SourceID: r.SourceID, Artifact: a, ParentObservation: "observation", PolicyHash: r.PolicyHash, WindowKey: r.WindowKey, BudgetHash: r.BudgetHash, Budget: r.Budget})
	if err != nil { return model.ArtifactInventoryEntry{},model.FollowUpRequest{},reject("UNAPPROVED_ARTIFACT") }
	return a,follow(i,r.WindowKey,r.PolicyHash,r.BudgetHash,model.PurposeArtifact,locator,ordinal),nil
}

// Listing mirrors parseListRows, parseHidden and nextPage over one capture.
func (d *Decoder) Listing(i api.CapturedInput, r api.DiscoverRequest) (api.DecodedDiscoveryPage,error) {
	if err := r.Validate(); err != nil { return api.DecodedDiscoveryPage{},reject("INVALID_REQUEST") }
	if err := d.check(i,r.Country,r.SourceID,r.Budget,r.PolicyHash); err != nil { return api.DecodedDiscoveryPage{},err }
	page,err := pageNumber(r)
	if err != nil { return api.DecodedDiscoveryPage{},err }
	root,err := parseHTML(i)
	if err != nil { return api.DecodedDiscoveryPage{},err }
	grids := nodes(root,func(n *htmlNode) bool { return n.tag == "table" && strings.HasSuffix(attr(n,"id"),"grvDocument") })
	if len(grids) != 1 { return api.DecodedDiscoveryPage{},reject("MISSING_LISTING") }
	form := grids[0].parent
	for form != nil && form.tag != "form" { form = form.parent }
	if form == nil { return api.DecodedDiscoveryPage{}, reject("MISSING_LISTING_FORM") }
	out := pageValue(i,r)
	candidates := 0
	for _, row := range nodes(grids[0],func(n *htmlNode) bool { return n.tag == "tr" }) {
		codes := nodes(row,func(n *htmlNode) bool { return class(n,"code") })
		links := nodes(row,func(n *htmlNode) bool { return n.tag == "a" && strings.Contains(attr(n,"href"),"docid") })
		fields := nodes(row,func(n *htmlNode) bool { return class(n,"substract") || class(n,"issued-date") })
		if len(codes) == 0 && len(links) == 0 && len(fields) == 0 { continue }
		candidates++
		if candidates > r.Budget.MaxItems { return api.DecodedDiscoveryPage{},reject("ELEMENT_BOUND") }
		if len(codes) != 1 || text(codes[0]) == "" || len(links) == 0 { return api.DecodedDiscoveryPage{},reject("MALFORMED_ROW") }
		id, locator := "", ""
		for _, link := range links {
			resolved, err := resolvedLink(baseURL+"/",attr(link,"href"))
			if err != nil { return api.DecodedDiscoveryPage{},err }
			u, err := url.Parse(resolved)
			if err != nil { return api.DecodedDiscoveryPage{},reject("UNAPPROVED_DETAIL") }
			query, err := url.ParseQuery(u.RawQuery)
			if err != nil { return api.DecodedDiscoveryPage{},reject("UNAPPROVED_DETAIL") }
			linkID := query.Get("docid")
			plan, err := d.planner.Detail(api.DetailRequest{Country: r.Country, SourceID: r.SourceID, ExternalID: linkID, ListingCaptures: []model.CaptureRef{i.Capture}, WindowKey: r.WindowKey, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget})
			if err != nil { return api.DecodedDiscoveryPage{},reject("UNAPPROVED_DETAIL") }
			canonical, _ := url.Parse(plan.URL)
			if u.Scheme != canonical.Scheme || u.Host != canonical.Host || u.Path != canonical.Path || query.Encode() != canonical.Query().Encode() { return api.DecodedDiscoveryPage{},reject("UNAPPROVED_DETAIL") }
			if id != "" && id != linkID { return api.DecodedDiscoveryPage{},reject("MALFORMED_ROW") }
			id, locator = linkID, plan.URL
		}
		o := observation(i,id,len(out.Observations))
		nodeField(&o,i,"number",codes[0])
		o.Fields[len(o.Fields)-1].Value = listingNumber(text(codes[0]))
		for _, n := range fields {
			name := "title"
			if class(n,"issued-date") { name="issued_at" }
			nodeField(&o,i,name,n)
		}
		out.Observations=append(out.Observations,o)
		out.FollowUps=append(out.FollowUps,follow(i,r.WindowKey,r.PolicyHash,r.BudgetHash,model.PurposeDetail,locator,len(out.FollowUps)))
	}
	var continuation api.DecodedContinuation
	seen := map[string]bool{}
	for _, n := range nodes(form,func(n *htmlNode) bool { return n.tag == "input" && strings.EqualFold(attr(n,"type"),"hidden") }) {
		name:=attr(n,"name")
		if seen[name] { return api.DecodedDiscoveryPage{},reject("DUPLICATE_FORM_FIELD") }
		seen[name]=true
		continuation.Fields=append(continuation.Fields,api.FormField{Name: name,Value: attr(n,"value")})
	}
	maxPage:=page
	for _, n := range nodes(form,func(n *htmlNode) bool { return n.tag == "a" && strings.Contains(attr(n,"href"),"__doPostBack") && strings.Contains(attr(n,"href"),"Page$") }) {
		href:=attr(n,"href")
		parts:=strings.Split(href,"'")
		if len(parts)!=5 || parts[0]!="javascript:__doPostBack(" || parts[2]!="," || parts[4]!=")" || !strings.HasPrefix(parts[3],"Page$") || strings.ReplaceAll(parts[1],"$","_") != attr(grids[0],"id") { return api.DecodedDiscoveryPage{},reject("MALFORMED_PAGER") }
		next,err:=strconv.Atoi(strings.TrimPrefix(parts[3],"Page$"))
		if err!=nil || next<1 { return api.DecodedDiscoveryPage{},reject("MALFORMED_PAGER") }
		maxPage=max(maxPage,next)
		if next==page+1 {
			if continuation.Target!="" && continuation.Target!=parts[1] { return api.DecodedDiscoveryPage{},reject("AMBIGUOUS_PAGER") }
			continuation.Target=parts[1]
		}
	}
	// Validate hidden state even when no next-page target is offered.
	state:=continuation
	if state.Target=="" { state.Target="state" }
	if err:=state.Validate(decodedLimits(r.Budget)); err!=nil { return api.DecodedDiscoveryPage{},reject("INVALID_FORM_STATE") }
	cap:=240
	if r.Mode==api.DiscoveryIncremental { cap=40 }
	if continuation.Target!="" {
		if !seen["__VIEWSTATE"] || !seen["__EVENTVALIDATION"] { return api.DecodedDiscoveryPage{},reject("MISSING_FORM_STATE") }
		out.Continuation=&continuation
		if page>=min(cap,r.Budget.MaxPages) { out.Findings=append(out.Findings,finding(i,"PAGE_CAP")) }
	} else if maxPage>page || len(out.Observations)==0 || candidates>=50 {
		out.Findings=append(out.Findings,finding(i,"UNPROVED_EXHAUSTION"))
	} else {
		out.Cursor.Complete=true
		out.Cursor.CommittedCursor=r.Cursor
		out.Cursor.NextCursor=r.Cursor
	}
	return validatePage(out)
}

// Detail mirrors parseDetailPage and inventories every declared attachment.
func (d *Decoder) Detail(i api.CapturedInput,r api.DetailRequest) (api.DecodedDetailResult,error) {
	if err:=r.Validate(); err!=nil { return api.DecodedDetailResult{},reject("INVALID_REQUEST") }
	if err:=d.check(i,r.Country,r.SourceID,r.Budget,r.PolicyHash); err!=nil { return api.DecodedDetailResult{},err }
	if _,err:=d.planner.Detail(r); err!=nil { return api.DecodedDetailResult{},reject("INVALID_REQUEST") }
	root,err:=parseHTML(i)
	if err!=nil { return api.DecodedDetailResult{},err }
	regions:=nodes(root,func(n *htmlNode) bool { return class(n,"Content") })
	if len(regions)!=1 { return api.DecodedDetailResult{},reject("MISSING_METADATA") }
	out:=api.DecodedDetailResult{Observation: observation(i,r.ExternalID,0),Limits: decodedLimits(r.Budget)}
	metadata(&out.Observation,i,regions[0])
	if len(out.Observation.Fields)==0 { return api.DecodedDetailResult{},reject("MISSING_METADATA") }
	seen:=map[string]bool{}
	for _, n:=range nodes(regions[0],func(n *htmlNode) bool { return n.tag=="a" && attr(n,"href")!="" && attachmentLink(n,regions[0]) }) {
		locator,err:=resolvedLink(baseURL+"/",attr(n,"href"))
		if err!=nil { return api.DecodedDetailResult{},err }
		if seen[locator] { continue }
		seen[locator]=true
		u,_:=url.Parse(locator)
		a,f,err:=d.artifact(i,r,locator,path.Base(u.Path),len(out.Artifacts))
		if err!=nil { return api.DecodedDetailResult{},err }
		out.Artifacts=append(out.Artifacts,a)
		out.FollowUps=append(out.FollowUps,f)
	}
	if len(out.Artifacts)==0 { out.Findings=append(out.Findings,finding(i,"MISSING_REQUIRED_ARTIFACT")) }
	return validateDetail(out)
}
