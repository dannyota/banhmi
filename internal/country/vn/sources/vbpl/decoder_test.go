package vbpl

import (
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/source/api"
)

func decoderFixture(t *testing.T, name, contentType string) (api.CapturedInput, api.DiscoverRequest, *Decoder) {
	t.Helper()
	content, err := os.ReadFile("testdata/"+name)
	if err != nil { t.Fatal(err) }
	hash := model.Digest(strings.Repeat("a", 64))
	budget := model.ResourceBudget{MaxItems: 10000, MaxInputBytes: 1<<20, MaxPages: 300, MaxElapsed: time.Minute, MaxConcurrent: 1}
	capture := model.CaptureRef{Country: "vn", SourceID: SourceID, CaptureKey: "capture", Asset: model.AssetRef{Path: "captures/page", SHA256: model.Digest(fmt.Sprintf("%x", sha256.Sum256(content))), ByteSize: int64(len(content))}, MediaType: contentType, RequestHash: hash, ResponseHash: hash, TransportReceipt: model.AssetRef{Path: "captures/receipt", SHA256: hash}}
	policy := api.SourcePolicy{Manifest: model.AssetRef{Path: "policy/source", SHA256: hash}, Digest: hash}
	ref := api.DecoderRef{Country: "vn", SourceID: SourceID, Version: "synthetic-v1", ImageDigest: hash}
	decoder, err := NewDecoder(ref, policy, Config{StateBankAgencies: []string{"62"}, ArtifactOrigins: []string{"https://files.example.invalid"}})
	if err != nil { t.Fatal(err) }
	request := api.DiscoverRequest{Country: "vn", SourceID: SourceID, WindowKey: "window", SliceKey: "sweep", Mode: api.DiscoveryFullSweep, Start: time.Date(2020,1,1,0,0,0,0,time.UTC), Cutoff: time.Date(2026,1,1,0,0,0,0,time.UTC), PolicyHash: hash, BudgetHash: hash, Budget: budget}
	return api.CapturedInput{Capture: capture, Content: content, Budget: budget}, request, decoder
}

func changedBytes(i api.CapturedInput, content []byte) api.CapturedInput {
	i.Content = content
	i.Capture.Asset.ByteSize = int64(len(content))
	i.Capture.Asset.SHA256 = model.Digest(fmt.Sprintf("%x", sha256.Sum256(content)))
	return i
}

func detailRequest(i api.CapturedInput, r api.DiscoverRequest) api.DetailRequest {
	return api.DetailRequest{Country: "vn", SourceID: SourceID, ExternalID: "123", ListingCaptures: []model.CaptureRef{i.Capture}, WindowKey: r.WindowKey, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget}
}

func TestDecoderBoundaryAndPurity(t *testing.T) {
	files, err := parser.ParseDir(token.NewFileSet(), ".", func(f os.FileInfo) bool { return !strings.HasSuffix(f.Name(), "_test.go") }, 0)
	if err != nil { t.Fatal(err) }
	for _, pkg := range files { for name, f := range pkg.Files {
		for _, imp := range f.Imports {
			value, err := strconv.Unquote(imp.Path.Value)
			if err != nil { t.Fatal(err) }
			if strings.Contains(value, ".") && value != "golang.org/x/net/html" && value != "danny.vn/banhmi/internal/corpus/model" && value != "danny.vn/banhmi/internal/corpus/source/api" && value != "danny.vn/banhmi/internal/country/vn" { t.Errorf("%s imports %s", name, value) }
			if !strings.Contains(value,".") {
				allowed:=map[string]bool{"bytes":true,"crypto/sha256":true,"encoding/json":true,"encoding/xml":true,"fmt":true,"io":true,"mime":true,"net/url":true,"path":true,"sort":true,"strconv":true,"strings":true,"unicode/utf8":true}
				if !allowed[value] { t.Errorf("%s has unapproved standard dependency %s",name,value) }
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok { if selector, ok := call.Fun.(*ast.SelectorExpr); ok { if id, ok := selector.X.(*ast.Ident); ok && id.Name == "time" && selector.Sel.Name == "Now" { t.Errorf("%s reads clock", name) } } }
			return true
		})
	} }
}

func TestListingGolden(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.json", "application/json")
	p, err := d.Listing(i,r)
	if err != nil { t.Fatal(err) }
	if len(p.Observations) != 1 || p.Observations[0].ExternalID != "123" || len(p.FollowUps) != 1 || p.Cursor.Complete != true { t.Fatalf("unexpected page: %+v",p) }
	if err := p.Validate(); err != nil { t.Fatal(err) }
	values:=map[string]string{}
	for _, f:=range p.Observations[0].Fields { values[f.Name]=f.Value }
	if values["number"]!="116/ 2025/QH15" || values["number_normalized"]!="116/2025/QH15" || values["title"]!="Văn bản tổng hợp thử nghiệm" && values["title"]!="Văn bản thử nghiệm" { t.Fatalf("golden field mismatch: %+v",values) }
	if values["type"]!="" && values["type"]!="Nghị quyết" { t.Fatal("invented type") }
	for _, f := range p.Observations[0].Fields { if len(f.Sources) != 1 || f.Sources[0].ArtifactHash != i.Capture.Asset.SHA256 { t.Fatal("lost provenance") } }
}

func TestDetailGolden(t *testing.T) {
	i, r, d := decoderFixture(t,"detail.json", "application/json")
	out, err := d.Detail(i,detailRequest(i,r))
	if err != nil { t.Fatal(err) }
	if out.Observation.ExternalID != "123" { t.Fatal("wrong identity") }
	if err := out.Validate(); err != nil { t.Fatal(err) }
	if len(out.FollowUps) != 3 { t.Fatal("missing VBPL metadata captures") }
}

func TestListingRejectsBadInput(t *testing.T) {
	i, r, d := decoderFixture(t,"listing.json", "application/json")
	for name, mutate := range map[string]func(api.CapturedInput) api.CapturedInput{
		"truncated": func(i api.CapturedInput) api.CapturedInput { return changedBytes(i, i.Content[:len(i.Content)/2]) },
		"malformed": func(i api.CapturedInput) api.CapturedInput { return changedBytes(i, []byte("<broken")) },
		"oversized": func(i api.CapturedInput) api.CapturedInput { return changedBytes(i, make([]byte,i.Budget.MaxInputBytes+1)) },
		"wrong media": func(i api.CapturedInput) api.CapturedInput { i.Capture.MediaType="application/pdf"; return i },
		"invalid encoding": func(i api.CapturedInput) api.CapturedInput { return changedBytes(i, []byte{0xff}) },
		"source": func(i api.CapturedInput) api.CapturedInput { i.Capture.SourceID="other"; return i },
	} { t.Run(name,func(t *testing.T) { _, err := d.Listing(mutate(i),r); if _, ok := err.(*DecodeError); !ok { t.Fatalf("expected typed rejection: %v",err) } }) }
	i.Budget.MaxItems=1
	r.Budget=i.Budget
	if _,err := d.Listing(i,r); err == nil { t.Fatal("element bound ignored") }
}

func TestDetailSubresponseGoldens(t *testing.T) {
	for _, name:=range []string{"files.json","diagram.json","tree.json"} {
		t.Run(name,func(t *testing.T) {
			i,r,d:=decoderFixture(t,name,"application/json")
			out,err:=d.Detail(i,detailRequest(i,r))
			if err!=nil { t.Fatal(err) }
			switch name {
			case "files.json":
				if len(out.Artifacts)!=3 || out.Artifacts[0].Format!="docx" || out.Artifacts[1].Role!="appendix" || out.Artifacts[2].Format!="unknown" || len(out.FollowUps)!=3 { t.Fatalf("inventory mismatch: %+v",out) }
			case "diagram.json":
				if len(out.Observation.Relations)!=2 || out.Observation.Relations[0].Kind!="vbpl_type_12" { t.Fatal("raw relation lost") }
			case "tree.json":
				if len(out.Observation.SourceTree.Nodes)!=2 || out.Observation.SourceTree.Nodes[1].ParentID!="node-1" { t.Fatal("source tree lost") }
			}
		})
	}
}

func TestIncompleteListingAndMissingArtifact(t *testing.T) {
	i,r,d:=decoderFixture(t,"listing.json","application/json")
	i=changedBytes(i,[]byte(strings.Replace(string(i.Content),`"total":1`,`"total":100`,1)))
	out,err:=d.Listing(i,r)
	if err!=nil { t.Fatal(err) }
	if out.Cursor.Complete || out.Cursor.NextCursor!=out.Cursor.CommittedCursor { t.Fatal("short inconsistent page completed") }
	i=changedBytes(i,[]byte(`{"success":true,"data":[]}`))
	detail,err:=d.Detail(i,detailRequest(i,r))
	if err!=nil || len(detail.Findings)==0 || len(detail.Artifacts)!=0 { t.Fatalf("missing files hidden: %v %+v",err,detail) }
}

func TestArtifactHostAndEndpointRejection(t *testing.T) {
	i,r,d:=decoderFixture(t,"files.json","application/json")
	for _, locator:=range []string{"https://evil.invalid/other.xyz","//evil.invalid/other.xyz","https://files.example.invalid/../other.xyz","https://files.example.invalid/%252e%252e/other.xyz"} {
		content:=strings.Replace(string(i.Content),"https://files.example.invalid/other.xyz",locator,1)
		if _,err:=d.Detail(changedBytes(i,[]byte(content)),detailRequest(i,r)); err==nil { t.Fatalf("unsafe artifact accepted: %s",locator) }
	}
}

func TestJSONBoundsAndUnknownFields(t *testing.T) {
	i,r,d:=decoderFixture(t,"listing.json","application/json")
	for _, content:=range []string{string(i.Content)+`{}`,`{"success":true,"data":{"total":0}}`, `{"success":true,"data":{"total":0,"items":null}}`,strings.Repeat("[",129)+strings.Repeat("]",129)} {
		if _,err:=d.Listing(changedBytes(i,[]byte(content)),r); err==nil { t.Fatal("malformed listing accepted") }
	}
}

func TestDecoderOutputSharesFrozenBudget(t *testing.T) {
	i,r,d:=decoderFixture(t,"listing.json","application/json")
	// This allowance passes capture-size verification but cannot hold the
	// decoded metadata, duplicate provenance and follow-up strings together.
	i.Budget.MaxInputBytes=int64(len(i.Content))
	r.Budget=i.Budget
	if _,err:=d.Listing(i,r); err==nil { t.Fatal("decoded output reset frozen byte allowance") }
	var zero Decoder
	if _,err:=zero.Listing(i,r); err==nil { t.Fatal("zero decoder accepted work") }
}

func TestDetailRejectsMalformedMissingAndOversizedInput(t *testing.T) {
	i,r,d:=decoderFixture(t,"detail.json","application/json")
	request:=detailRequest(i,r)
	for _, content:=range [][]byte{i.Content[:len(i.Content)/2],[]byte(`{"success":true,"data":{"id":"123"}}`),make([]byte,i.Budget.MaxInputBytes+1)} {
		if _,err:=d.Detail(changedBytes(i,content),request); err==nil { t.Fatal("bad detail accepted") }
	}
	bad:=i
	bad.Capture.MediaType="text/html"
	if _,err:=d.Detail(bad,request); err==nil { t.Fatal("unexpected detail media accepted") }
}

func TestDuplicateJSONKeysCannotHideListingFields(t *testing.T) {
	i,r,d:=decoderFixture(t,"listing.json","application/json")
	content:=strings.Replace(string(i.Content),`"total":1`,`"total":100,"total":1`,1)
	if _,err:=d.Listing(changedBytes(i,[]byte(content)),r); err==nil { t.Fatal("duplicate total produced complete page") }
}
