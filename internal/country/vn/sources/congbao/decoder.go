package congbao

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"mime"
	"net/url"
	"path"
	"sort"
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
	text string
	start int
	end int
}

// htmlAttributeCount counts source attributes before the tokenizer drops repeats.
// HTML quotes delimit values; slashes delimit names but remain in unquoted values.
func htmlAttributeCount(raw []byte) int {
	space := func(c byte) bool {
		return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
	}
	pos := 1
	for pos < len(raw) && !space(raw[pos]) && raw[pos] != '/' && raw[pos] != '>' { pos++ }
	count := 0
	for pos < len(raw) {
		for pos < len(raw) && (space(raw[pos]) || raw[pos] == '/') { pos++ }
		if pos == len(raw) || raw[pos] == '>' { break }
		count++
		// An initial equals sign belongs to the name, as in the tokenizer.
		pos++
		for pos < len(raw) && !space(raw[pos]) && raw[pos] != '=' && raw[pos] != '/' && raw[pos] != '>' { pos++ }
		for pos < len(raw) && space(raw[pos]) { pos++ }
		if pos == len(raw) || raw[pos] != '=' { continue }
		pos++
		for pos < len(raw) && space(raw[pos]) { pos++ }
		if pos == len(raw) { break }
		if quote := raw[pos]; quote == '\'' || quote == '"' {
			pos++
			for pos < len(raw) && raw[pos] != quote { pos++ }
			if pos < len(raw) { pos++ }
		} else {
			for pos < len(raw) && !space(raw[pos]) && raw[pos] != '>' { pos++ }
		}
	}
	return count
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
		rawAttrs := 0
		if kind == html.StartTagToken || kind == html.SelfClosingTagToken {
			rawAttrs = htmlAttributeCount(z.Raw())
			if rawAttrs > i.Budget.MaxItems-count { return nil, reject("ELEMENT_BOUND") }
		}
		t := z.Token()
		switch kind {
		case html.StartTagToken, html.SelfClosingTagToken:
			if rawAttrs != len(t.Attr) { return nil, reject("DUPLICATE_HTML_ATTRIBUTE") }
			if len(t.Attr) > i.Budget.MaxItems-count { return nil, reject("ELEMENT_BOUND") }
			count += len(t.Attr)
			seen := make(map[string]bool, len(t.Attr))
			for _, a := range t.Attr {
				if seen[a.Key] { return nil, reject("DUPLICATE_HTML_ATTRIBUTE") }
				seen[a.Key] = true
			}
			n := &htmlNode{tag: t.Data, attrs: t.Attr, start: start, end: offset}
			parent := stack[len(stack)-1]
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

func hasAttr(n *htmlNode, name string) bool {
	for _, a := range n.attrs { if a.Key == name { return true } }
	return false
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

type rssItem struct { Title, Description, Link, GUID, PubDate string }

func decodeRSS(i api.CapturedInput) ([]rssItem,error) {
	decoder:=xml.NewDecoder(bytes.NewReader(i.Content))
	depth,count:=0,0
	roots:=0
	rootRSS,channel:=false,false
	var items []rssItem
	type frame struct { name xml.Name; seen map[string]bool; text []byte }
	var stack []frame
	for {
		token,err:=decoder.Token()
		if err==io.EOF { break }
		if err!=nil { return nil,reject("MALFORMED_XML") }
		count++
		if count>i.Budget.MaxItems { return nil,reject("ELEMENT_BOUND") }
		switch token:=token.(type) {
		case xml.Directive: return nil,reject("XML_DIRECTIVE")
		case xml.StartElement:
			if depth==0 { roots++; rootRSS=token.Name==(xml.Name{Local: "rss"}) }
			// Core RSS elements have no namespace and must have core ancestors.
			// Extensions stay in the token walk so their full subtrees consume budgets.
			coreChannel:=depth==1 && stack[0].name==(xml.Name{Local: "rss"}) && token.Name==(xml.Name{Local: "channel"})
			coreItem:=depth==2 && stack[0].name==(xml.Name{Local: "rss"}) && stack[1].name==(xml.Name{Local: "channel"}) && token.Name==(xml.Name{Local: "item"})
			if coreChannel { channel=true }
			if coreItem { items=append(items,rssItem{}) }
			if depth>0 && token.Name.Space=="" {
				parent:=&stack[depth-1]
				singleton:=coreChannel
				if depth==3 && stack[0].name==(xml.Name{Local: "rss"}) && stack[1].name==(xml.Name{Local: "channel"}) && parent.name==(xml.Name{Local: "item"}) {
					switch token.Name.Local {
					case "title", "description", "link", "guid", "pubDate": singleton=true
					}
				}
				if singleton {
					if parent.seen[token.Name.Local] { return nil,reject("DUPLICATE_RSS_ELEMENT") }
					if parent.seen==nil { parent.seen=map[string]bool{} }
					parent.seen[token.Name.Local]=true
				}
			}
			stack=append(stack,frame{name: token.Name})
			count+=len(token.Attr)
			depth++
			if depth>128 { return nil,reject("DEPTH_BOUND") }
			if count>i.Budget.MaxItems { return nil,reject("ELEMENT_BOUND") }
		case xml.EndElement:
			if depth==4 && stack[0].name==(xml.Name{Local: "rss"}) && stack[1].name==(xml.Name{Local: "channel"}) && stack[2].name==(xml.Name{Local: "item"}) && stack[3].name.Space=="" {
				item:=&items[len(items)-1]
				value:=string(stack[3].text)
				switch stack[3].name.Local {
				case "title": item.Title=value
				case "description": item.Description=value
				case "link": item.Link=value
				case "guid": item.GUID=value
				case "pubDate": item.PubDate=value
				}
			}
			depth--
			stack=stack[:depth]
		case xml.CharData:
			if depth==0 && strings.TrimSpace(string(token))!="" { return nil,reject("MALFORMED_XML") }
			if depth==4 && stack[0].name==(xml.Name{Local: "rss"}) && stack[1].name==(xml.Name{Local: "channel"}) && stack[2].name==(xml.Name{Local: "item"}) && stack[3].name.Space=="" {
				switch stack[3].name.Local {
				case "title", "description", "link", "guid", "pubDate": stack[3].text=append(stack[3].text,token...)
				}
			}
		}
	}
	if depth!=0 || roots!=1 { return nil,reject("MALFORMED_XML") }
	if !rootRSS || !channel { return nil,reject("MALFORMED_RSS") }
	return items,nil
}

// Listing mirrors Discover's RSS items. A recent feed never proves historical
// window completion, even when every feed item is successfully decoded.
func (d *Decoder) Listing(i api.CapturedInput,r api.DiscoverRequest) (api.DecodedDiscoveryPage,error) {
	if err:=r.Validate(); err!=nil { return api.DecodedDiscoveryPage{},reject("INVALID_REQUEST") }
	if err:=d.check(i,r.Country,r.SourceID,r.Budget,r.PolicyHash); err!=nil { return api.DecodedDiscoveryPage{},err }
	if _,err:=d.planner.Listing(r); err!=nil { return api.DecodedDiscoveryPage{},reject("INVALID_REQUEST") }
	if media(i,"application/json") { return d.searchListing(i,r) }
	if !media(i,"application/rss+xml","application/xml","text/xml") { return api.DecodedDiscoveryPage{},reject("CONTENT_TYPE") }
	items,err:=decodeRSS(i)
	if err!=nil { return api.DecodedDiscoveryPage{},err }
	out:=pageValue(i,r)
	for ordinal,item:=range items {
		raw:=item.Link
		locatorKey:="link"
		if raw=="" { raw=item.GUID; locatorKey="guid" }
		if raw=="" { return api.DecodedDiscoveryPage{},reject("MISSING_DETAIL") }
		locator,err:=resolvedLink(baseURL+"/",raw)
		if err!=nil { return api.DecodedDiscoveryPage{},err }
		u,_:=url.Parse(locator)
		stem:=strings.TrimSuffix(path.Base(u.Path),".htm")
		cut:=strings.LastIndex(stem,"-")
		if cut<0 { return api.DecodedDiscoveryPage{},reject("MISSING_ID") }
		id:=stem[cut+1:]
		if _,err:=d.planner.DetailAt(api.DetailRequest{Country: r.Country,SourceID: r.SourceID,ExternalID: id,ListingCaptures: []model.CaptureRef{i.Capture},WindowKey: r.WindowKey,PolicyHash: r.PolicyHash,BudgetHash: r.BudgetHash,Budget: r.Budget},locator); err!=nil { return api.DecodedDiscoveryPage{},reject("UNAPPROVED_DETAIL") }
		o:=observation(i,id,ordinal)
		object:=fmt.Sprintf("/rss/channel/item/%d",ordinal)
		title:=item.Description
		titleKey:="description"
		if title=="" { title=item.Title; titleKey="title" }
		field(&o,i,"title",title,object+"/"+titleKey)
		field(&o,i,"published_at",item.PubDate,object+"/pubDate")
		// Slug-derived values are labeled as hints, never printed citations.
		field(&o,i,"source_slug",stem[:cut],object+"/"+locatorKey)
		out.Observations=append(out.Observations,o)
		out.FollowUps=append(out.FollowUps,follow(i,r.WindowKey,r.PolicyHash,r.BudgetHash,model.PurposeDetail,locator,ordinal))
	}
	out.Findings=append(out.Findings,finding(i,"RSS_RECENT_SIGNAL"))
	return validatePage(out)
}

// Detail mirrors parseMetadataTable and parseDownloadLinks over captured HTML.
func (d *Decoder) Detail(i api.CapturedInput,r api.DetailRequest) (api.DecodedDetailResult,error) {
	if err:=r.Validate(); err!=nil { return api.DecodedDetailResult{},reject("INVALID_REQUEST") }
	if err:=d.check(i,r.Country,r.SourceID,r.Budget,r.PolicyHash); err!=nil { return api.DecodedDetailResult{},err }
	if _,err:=d.planner.Detail(r); err!=nil { return api.DecodedDetailResult{},reject("INVALID_REQUEST") }
	if media(i,"application/json") { return d.searchDetail(i,r) }
	root,err:=parseHTML(i)
	if err!=nil { return api.DecodedDetailResult{},err }
	out:=api.DecodedDetailResult{Observation: observation(i,r.ExternalID,0),Limits: decodedLimits(r.Budget)}
	for _, row:=range nodes(root,func(n *htmlNode) bool { return n.tag=="div" && class(n,"row") }) {
		var labels, values []*htmlNode
		for _, child:=range row.children {
			if class(child,"name") { labels=append(labels,child) }
			if class(child,"value") { values=append(values,child) }
		}
		// Layout rows can enclose metadata rows or unrelated content. Only
		// direct label/value children identify an actual metadata row.
		if len(labels)==0 { continue }
		if len(labels)!=1 || len(values)!=1 { return api.DecodedDetailResult{},reject("MALFORMED_METADATA") }
		value:=values[0]
		var children []*htmlNode
		for _, child:=range value.children { if class(child,"child-value") { children=append(children,child) } }
		if len(children)>1 { return api.DecodedDetailResult{},reject("MALFORMED_METADATA") }
		if len(children)==1 { value=children[0] }
		if name:=metadataName(text(labels[0])); name!="" { nodeField(&out.Observation,i,name,value) }
	}
	if len(out.Observation.Fields)==0 { return api.DecodedDetailResult{},reject("MISSING_METADATA") }
	seen:=map[string]bool{}
	for _, n:=range nodes(root,func(n *htmlNode) bool { return n.tag=="a" && hasAttr(n,"data-file") }) {
		locator,err:=resolvedLink(baseURL+"/",attr(n,"href"))
		if err!=nil { return api.DecodedDetailResult{},err }
		if seen[locator] { continue }
		seen[locator]=true
		name:=attr(n,"data-file")
		if cut:=strings.LastIndex(name,"/"); cut>=0 { name=name[cut+1:] }
		a,f,err:=d.artifact(i,r,locator,name,len(out.Artifacts))
		if err!=nil { return api.DecodedDetailResult{},err }
		out.Artifacts=append(out.Artifacts,a)
		out.FollowUps=append(out.FollowUps,f)
	}
	if len(out.Artifacts)==0 { out.Findings=append(out.Findings,finding(i,"MISSING_REQUIRED_ARTIFACT")) }
	return validateDetail(out)
}
// jsonPreflight bounds ignored values too and rejects duplicate object keys.
// It reads one token at a time before typed decoding, with no entity resolution.
func jsonPreflight(i api.CapturedInput) error {
	if !media(i, "application/json") { return reject("CONTENT_TYPE") }
	decoder := json.NewDecoder(bytes.NewReader(i.Content))
	decoder.UseNumber()
	count := 0
	read := func() (json.Token, error) {
		count++
		if count > i.Budget.MaxItems { return nil, reject("ELEMENT_BOUND") }
		token, err := decoder.Token()
		if err != nil { return nil, reject("MALFORMED_JSON") }
		return token, nil
	}
	var value func(int) error
	value = func(depth int) error {
		if depth > 128 { return reject("DEPTH_BOUND") }
		token, err := read()
		if err != nil { return err }
		delimiter, container := token.(json.Delim)
		if !container { return nil }
		if delimiter != '{' && delimiter != '[' { return reject("MALFORMED_JSON") }
		seen := map[string]bool{}
		for decoder.More() {
			if delimiter == '{' {
				key, err := read()
				if err != nil { return err }
				name, ok := key.(string)
				if !ok || seen[name] { return reject("DUPLICATE_JSON_KEY") }
				seen[name] = true
				// encoding/json matches struct keys without regard to case. Reject
				// aliases before typed decoding so every emitted anchor is exact.
				for _, recognized:=range []string{"success","total","data","id_van_ban","so_ky_hieu","tieu_de","trich_yeu","loai_van_ban","ngay_ban_hanh","ngay_co_hieu_luc","ten_co_quan","nguoi_ky","danh_sach_dang_trong_cong_bao","danh_sach_ky_cong_bao","ten","danh_sach_tep_van_ban","duong_dan","thu_tu","ten_file","file_extension"} {
					if name!=recognized && strings.EqualFold(name,recognized) { return reject("CASE_VARIANT_JSON_KEY") }
				}
			}
			if err := value(depth+1); err != nil { return err }
		}
		end, err := read()
		if err != nil { return err }
		if delimiter == '{' && end != json.Delim('}') || delimiter == '[' && end != json.Delim(']') { return reject("MALFORMED_JSON") }
		return nil
	}
	if err := value(1); err != nil { return err }
	if _, err := decoder.Token(); err != io.EOF { return reject("MALFORMED_JSON") }
	return nil
}

// decodeJSONFields selects exact source keys before decoding their values.
// Struct field-name matching never determines recognized source members.
func decodeJSONFields(raw []byte, destinations map[string]any) error {
	var members map[string]json.RawMessage
	if err:=json.Unmarshal(raw,&members); err!=nil { return err }
	if members==nil { return reject("MALFORMED_SEARCH") }
	for key,destination:=range destinations {
		if value,ok:=members[key]; ok {
			if err:=json.Unmarshal(value,destination); err!=nil { return err }
		}
	}
	return nil
}

type searchGazetteIssue struct { Name string; Date string }

func (g *searchGazetteIssue) UnmarshalJSON(raw []byte) error {
	*g=searchGazetteIssue{}
	return decodeJSONFields(raw,map[string]any{"ten":&g.Name,"ngay_ban_hanh":&g.Date})
}

type searchFile struct { URL string; Order int; Name string; Extension string }

func (f *searchFile) UnmarshalJSON(raw []byte) error {
	*f=searchFile{}
	return decodeJSONFields(raw,map[string]any{"duong_dan":&f.URL,"thu_tu":&f.Order,"ten_file":&f.Name,"file_extension":&f.Extension})
}

type searchDocument struct {
	ID json.RawMessage
	Number string
	Title string
	Abstract string
	Type string
	Issued string
	Effective string
	Issuers []string
	Signer string
	Gazette string
	GazetteIssues []searchGazetteIssue
	Files []searchFile
}

func (n *searchDocument) UnmarshalJSON(raw []byte) error {
	*n=searchDocument{}
	return decodeJSONFields(raw,map[string]any{
		"id_van_ban":&n.ID,"so_ky_hieu":&n.Number,"tieu_de":&n.Title,"trich_yeu":&n.Abstract,
		"loai_van_ban":&n.Type,"ngay_ban_hanh":&n.Issued,"ngay_co_hieu_luc":&n.Effective,
		"ten_co_quan":&n.Issuers,"nguoi_ky":&n.Signer,"danh_sach_dang_trong_cong_bao":&n.Gazette,
		"danh_sach_ky_cong_bao":&n.GazetteIssues,"danh_sach_tep_van_ban":&n.Files,
	})
}

func searchResponse(i api.CapturedInput) ([]searchDocument,error) {
	if err:=jsonPreflight(i); err!=nil { return nil,err }
	var success bool
	var total *int
	var data *[]searchDocument
	if decodeJSONFields(i.Content,map[string]any{"success":&success,"total":&total,"data":&data})!=nil || !success || total==nil || data==nil || *total<0 || len(*data)>*total { return nil,reject("MALFORMED_SEARCH") }
	return *data,nil
}

func searchIdentity(raw json.RawMessage) (string,error) {
	var id string
	if len(raw)>0 && raw[0]=='"' { if json.Unmarshal(raw,&id)!=nil { return "",reject("MISSING_ID") } } else { id=string(raw) }
	if id=="" || strings.Trim(id,"0123456789")!="" { return "",reject("MISSING_ID") }
	return id,nil
}

func searchObservation(i api.CapturedInput,n searchDocument,ordinal int) (api.DecodedObservation,error) {
	id,err:=searchIdentity(n.ID)
	if err!=nil { return api.DecodedObservation{},err }
	if n.Number=="" && n.Title=="" && n.Abstract=="" { return api.DecodedObservation{},reject("MISSING_METADATA") }
	o:=observation(i,id,ordinal)
	object:=fmt.Sprintf("/data/%d",ordinal)
	for _, f:=range []struct { name,value,key string }{{"number",n.Number,"so_ky_hieu"},{"title",n.Title,"tieu_de"},{"abstract",n.Abstract,"trich_yeu"},{"type",n.Type,"loai_van_ban"},{"issued_at",n.Issued,"ngay_ban_hanh"},{"effective_at",n.Effective,"ngay_co_hieu_luc"},{"signer",n.Signer,"nguoi_ky"},{"gazette_number",n.Gazette,"danh_sach_dang_trong_cong_bao"}} { field(&o,i,f.name,f.value,object+"/"+f.key) }
	for index,issuer:=range n.Issuers { field(&o,i,"issuer",issuer,fmt.Sprintf("%s/ten_co_quan/%d",object,index)) }
	for index,issue:=range n.GazetteIssues {
		issueObject:=fmt.Sprintf("%s/danh_sach_ky_cong_bao/%d",object,index)
		field(&o,i,"gazette_number",issue.Name,issueObject+"/ten")
		field(&o,i,"gazette_date",issue.Date,issueObject+"/ngay_ban_hanh")
		if index==0 { field(&o,i,"published_at",issue.Date,issueObject+"/ngay_ban_hanh") }
	}
	return o,nil
}

// searchListing mirrors searchResponse decoding but retains every candidate.
// The frozen Decoder port has no requested-number input, so equality and absence
// decisions belong to the caller with the originating NumberRequest.
func (d *Decoder) searchListing(i api.CapturedInput,r api.DiscoverRequest) (api.DecodedDiscoveryPage,error) {
	items,err:=searchResponse(i)
	if err!=nil { return api.DecodedDiscoveryPage{},err }
	out:=pageValue(i,r)
	seen:=map[string]bool{}
	for ordinal,n:=range items {
		o,err:=searchObservation(i,n,ordinal)
		if err!=nil { return api.DecodedDiscoveryPage{},err }
		if seen[o.ExternalID] { return api.DecodedDiscoveryPage{},reject("DUPLICATE_ID") }
		seen[o.ExternalID]=true
		out.Observations=append(out.Observations,o)
		out.FollowUps=append(out.FollowUps,follow(i,r.WindowKey,r.PolicyHash,r.BudgetHash,model.PurposeDetail,baseURL+"/van-ban/van-ban-"+o.ExternalID+".htm",ordinal))
	}
	out.Findings=append(out.Findings,finding(i,"NUMBER_SEARCH_CANDIDATES"))
	return validatePage(out)
}

func (d *Decoder) searchDetail(i api.CapturedInput,r api.DetailRequest) (api.DecodedDetailResult,error) {
	items,err:=searchResponse(i)
	if err!=nil { return api.DecodedDetailResult{},err }
	index:=-1
	for ordinal,n:=range items {
		id,err:=searchIdentity(n.ID)
		if err!=nil { return api.DecodedDetailResult{},err }
		if id==r.ExternalID {
			if index>=0 { return api.DecodedDetailResult{},reject("AMBIGUOUS_ID") }
			index=ordinal
		}
	}
	if index<0 { return api.DecodedDetailResult{},reject("DETAIL_ID_MISMATCH") }
	n:=items[index]
	o,err:=searchObservation(i,n,index)
	if err!=nil { return api.DecodedDetailResult{},err }
	out:=api.DecodedDetailResult{Observation: o,Limits: decodedLimits(r.Budget)}
	sort.SliceStable(n.Files,func(a,b int) bool { return n.Files[a].Order<n.Files[b].Order })
	seen:=map[string]bool{}
	for _, file:=range n.Files {
		if file.URL=="" { return api.DecodedDetailResult{},reject("MISSING_REQUIRED_ARTIFACT") }
		locator,err:=resolvedLink(baseURL+"/",file.URL)
		if err!=nil { return api.DecodedDetailResult{},err }
		if seen[locator] { continue }
		seen[locator]=true
		a,f,err:=d.artifact(i,r,locator,file.Name,len(out.Artifacts))
		if err!=nil { return api.DecodedDetailResult{},err }
		// The search CDN uses a trailing format without a dot. Preserve the
		// supplied filename; record a format hint without fabricating its label.
		if a.Format=="unknown" {
			u,_:=url.Parse(locator)
			for _, format:=range []string{"docx","pdf","doc"} { if strings.HasSuffix(strings.ToLower(u.Path),format) { a.Format=format; break } }
		}
		out.Artifacts=append(out.Artifacts,a)
		out.FollowUps=append(out.FollowUps,f)
	}
	if len(out.Artifacts)==0 { out.Findings=append(out.Findings,finding(i,"MISSING_REQUIRED_ARTIFACT")) }
	return validateDetail(out)
}
