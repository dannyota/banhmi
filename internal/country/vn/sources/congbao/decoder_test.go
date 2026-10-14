package congbao

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
	decoder, err := NewDecoder(ref, policy)
	if err != nil { t.Fatal(err) }
	request := api.DiscoverRequest{Country: "vn", SourceID: SourceID, WindowKey: "window", SliceKey: "listing", Mode: api.DiscoveryFullSweep, Start: time.Date(2020,1,1,0,0,0,0,time.UTC), Cutoff: time.Date(2026,1,1,0,0,0,0,time.UTC), PolicyHash: hash, BudgetHash: hash, Budget: budget}
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
	i, r, d := decoderFixture(t, "listing.xml", "application/rss+xml")
	p, err := d.Listing(i,r)
	if err != nil { t.Fatal(err) }
	if len(p.Observations) != 1 || p.Observations[0].ExternalID != "123" || len(p.FollowUps) != 1 || p.Cursor.Complete != false { t.Fatalf("unexpected page: %+v",p) }
	if err := p.Validate(); err != nil { t.Fatal(err) }
	for _, f := range p.Observations[0].Fields { if len(f.Sources) != 1 || f.Sources[0].ArtifactHash != i.Capture.Asset.SHA256 { t.Fatal("lost provenance") } }
}

func TestDetailGolden(t *testing.T) {
	i, r, d := decoderFixture(t,"detail.html", "text/html")
	out, err := d.Detail(i,detailRequest(i,r))
	if err != nil { t.Fatal(err) }
	if out.Observation.ExternalID != "123" { t.Fatal("wrong identity") }
	if err := out.Validate(); err != nil { t.Fatal(err) }
	if len(out.Artifacts) != 2 || out.Artifacts[0].Format != "doc" || out.Artifacts[1].Format != "unknown" || len(out.FollowUps) != 2 { t.Fatalf("inventory lost unsupported file: %+v",out) }
}

func TestListingRejectsBadInput(t *testing.T) {
	i, r, d := decoderFixture(t,"listing.xml", "application/rss+xml")
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

func TestXMLRejectsEntitiesAndDepth(t *testing.T) {
	i,r,d:=decoderFixture(t,"listing.xml","application/rss+xml")
	for _, content:=range []string{`<!DOCTYPE rss [<!ENTITY x SYSTEM "https://evil.invalid/">]><rss><channel>&x;</channel></rss>`, `<!DOCTYPE rss [<!ENTITY x "test">]><rss><channel>&x;</channel></rss>`,strings.Repeat("<x>",129)+strings.Repeat("</x>",129), `<rss><channel></rss>`} {
		if _,err:=d.Listing(changedBytes(i,[]byte(content)),r); err==nil { t.Fatal("unsafe XML accepted") }
	}
}

func TestDetailNegativesAndMissingArtifact(t *testing.T) {
	i,r,d:=decoderFixture(t,"detail.html","text/html")
	request:=detailRequest(i,r)
	for _, content:=range [][]byte{i.Content[:len(i.Content)/2],[]byte("<broken"),make([]byte,i.Budget.MaxInputBytes+1)} {
		if _,err:=d.Detail(changedBytes(i,content),request); err==nil { t.Fatal("bad detail accepted") }
	}
	bad:=i
	bad.Capture.MediaType="application/json"
	if _,err:=d.Detail(bad,request); err==nil { t.Fatal("unexpected detail media accepted") }
	content:=string(i.Content)
	for {
		start:=strings.Index(content,"<a ")
		if start<0 { break }
		end:=strings.Index(content[start:],"</a>")
		if end<0 { t.Fatal("fixture link is not closed") }
		content=content[:start]+content[start+end+4:]
	}
	out,err:=d.Detail(changedBytes(i,[]byte(content)),request)
	if err!=nil || len(out.Artifacts)!=0 || len(out.Findings)!=1 || out.Findings[0].ReasonCode!="MISSING_REQUIRED_ARTIFACT" { t.Fatalf("missing artifact hidden: %v %+v",err,out) }
}

func TestArtifactHostSmuggling(t *testing.T) {
	i,r,d:=decoderFixture(t,"detail.html","text/html")
	request:=detailRequest(i,r)
	for _, replacement:=range []string{"https://evil.invalid/api/download/stream?file=test", "//evil.invalid/api/download/stream?file=test", "https://g7.cdnchinhphu.vn/incorrect/synthetic.doc", "https://g7.cdnchinhphu.vn/%2e%2e/api/download/stream?file=test"} {
		content:=strings.Replace(string(i.Content),"https://g7.cdnchinhphu.vn/api/download/stream?file=synthetic&amp;format=doc",replacement,1)
		if content==string(i.Content) { t.Fatal("negative fixture did not replace artifact") }
		if _,err:=d.Detail(changedBytes(i,[]byte(content)),request); err==nil { t.Fatalf("unsafe artifact accepted: %s",replacement) }
	}
}

func TestListingRejectsSmuggledHostAndUnknownCharset(t *testing.T) {
	i,r,d:=decoderFixture(t,"listing.xml","application/rss+xml")
	content:=strings.Replace(string(i.Content),"https://congbao.chinhphu.vn","//evil.invalid",1)
	if content==string(i.Content) { t.Fatal("negative fixture did not replace detail") }
	if _,err:=d.Listing(changedBytes(i,[]byte(content)),r); err==nil { t.Fatal("relative host escaped source") }
	i.Capture.MediaType="application/rss+xml; charset=windows-1258"
	if _,err:=d.Listing(i,r); err==nil { t.Fatal("unverified charset accepted") }
}

func TestSearchPreservesCandidatesAndDeclaredFilename(t *testing.T) {
	i,r,d:=decoderFixture(t,"search.json","application/json")
	out,err:=d.Listing(i,r)
	if err!=nil { t.Fatal(err) }
	if len(out.Observations)!=2 || out.Cursor.Complete { t.Fatal("search selected a candidate or proved absence") }
	detail,err:=d.Detail(i,detailRequest(i,r))
	if err!=nil { t.Fatal(err) }
	if len(detail.Artifacts)!=1 || detail.Artifacts[0].Format!="doc" || detail.Artifacts[0].DeclaredFilename!="Tên nguồn không có phần mở rộng" { t.Fatalf("invented declared filename: %+v",detail) }
}

func TestDecoderOutputSharesFrozenBudget(t *testing.T) {
	i,r,d:=decoderFixture(t,"listing.xml","application/rss+xml")
	// This allowance passes capture-size verification but cannot hold the
	// decoded metadata, duplicate provenance and follow-up strings together.
	i.Budget.MaxInputBytes=int64(len(i.Content))
	r.Budget=i.Budget
	if _,err:=d.Listing(i,r); err==nil { t.Fatal("decoded output reset frozen byte allowance") }
	var zero Decoder
	if _,err:=zero.Listing(i,r); err==nil { t.Fatal("zero decoder accepted work") }
}
