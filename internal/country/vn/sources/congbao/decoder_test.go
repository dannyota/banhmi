package congbao

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/source/api"
)

func decoderFixture(t *testing.T, name, contentType string) (api.CapturedInput, api.DiscoverRequest, *Decoder) {
	t.Helper()
	content, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	hash := model.Digest(strings.Repeat("a", 64))
	budget := model.ResourceBudget{MaxItems: 10000, MaxInputBytes: 1 << 20, MaxPages: 300, MaxElapsed: time.Minute, MaxConcurrent: 1}
	capture := model.CaptureRef{Country: "vn", SourceID: SourceID, CaptureKey: "capture", Asset: model.AssetRef{Path: "captures/page", SHA256: model.Digest(fmt.Sprintf("%x", sha256.Sum256(content))), ByteSize: int64(len(content))}, MediaType: contentType, RequestHash: hash, ResponseHash: hash, TransportReceipt: model.AssetRef{Path: "captures/receipt", SHA256: hash}}
	policy := api.SourcePolicy{Manifest: model.AssetRef{Path: "policy/source", SHA256: hash}, Digest: hash}
	ref := api.DecoderRef{Country: "vn", SourceID: SourceID, Version: "synthetic-v1", ImageDigest: hash}
	decoder, err := NewDecoder(ref, policy)
	if err != nil {
		t.Fatal(err)
	}
	request := api.DiscoverRequest{Country: "vn", SourceID: SourceID, WindowKey: "window", SliceKey: "listing", Mode: api.DiscoveryFullSweep, Start: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Cutoff: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), PolicyHash: hash, BudgetHash: hash, Budget: budget}
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
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range files {
		for name, f := range pkg.Files {
			for _, imp := range f.Imports {
				value, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(value, ".") && value != "golang.org/x/net/html" && value != "danny.vn/banhmi/internal/corpus/model" && value != "danny.vn/banhmi/internal/corpus/source/api" && value != "danny.vn/banhmi/internal/country/vn" {
					t.Errorf("%s imports %s", name, value)
				}
				if !strings.Contains(value, ".") {
					allowed := map[string]bool{"bytes": true, "crypto/sha256": true, "encoding/json": true, "encoding/xml": true, "fmt": true, "io": true, "mime": true, "net/url": true, "path": true, "sort": true, "strconv": true, "strings": true, "unicode/utf8": true}
					if !allowed[value] {
						t.Errorf("%s has unapproved standard dependency %s", name, value)
					}
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
						if id, ok := selector.X.(*ast.Ident); ok && id.Name == "time" && selector.Sel.Name == "Now" {
							t.Errorf("%s reads clock", name)
						}
					}
				}
				return true
			})
		}
	}
}

func TestListingGolden(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.xml", "application/rss+xml")
	p, err := d.Listing(i, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Observations) != 1 || p.Observations[0].ExternalID != "123" || len(p.FollowUps) != 1 || p.Cursor.Complete != false {
		t.Fatalf("unexpected page: %+v", p)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, f := range p.Observations[0].Fields {
		if len(f.Sources) != 1 || f.Sources[0].ArtifactHash != i.Capture.Asset.SHA256 {
			t.Fatal("lost provenance")
		}
	}
}

func TestDetailGolden(t *testing.T) {
	i, r, d := decoderFixture(t, "detail.html", "text/html")
	out, err := d.Detail(i, detailRequest(i, r))
	if err != nil {
		t.Fatal(err)
	}
	if out.Observation.ExternalID != "123" {
		t.Fatal("wrong identity")
	}
	if err := out.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(out.Artifacts) != 2 || out.Artifacts[0].Format != "doc" || out.Artifacts[1].Format != "unknown" || len(out.FollowUps) != 2 {
		t.Fatalf("inventory lost unsupported file: %+v", out)
	}
}

func TestListingRejectsBadInput(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.xml", "application/rss+xml")
	for name, mutate := range map[string]func(api.CapturedInput) api.CapturedInput{
		"truncated": func(i api.CapturedInput) api.CapturedInput { return changedBytes(i, i.Content[:len(i.Content)/2]) },
		"malformed": func(i api.CapturedInput) api.CapturedInput { return changedBytes(i, []byte("<broken")) },
		"oversized": func(i api.CapturedInput) api.CapturedInput {
			return changedBytes(i, make([]byte, i.Budget.MaxInputBytes+1))
		},
		"wrong media":      func(i api.CapturedInput) api.CapturedInput { i.Capture.MediaType = "application/pdf"; return i },
		"invalid encoding": func(i api.CapturedInput) api.CapturedInput { return changedBytes(i, []byte{0xff}) },
		"source":           func(i api.CapturedInput) api.CapturedInput { i.Capture.SourceID = "other"; return i },
	} {
		t.Run(name, func(t *testing.T) {
			_, err := d.Listing(mutate(i), r)
			if _, ok := err.(*DecodeError); !ok {
				t.Fatalf("expected typed rejection: %v", err)
			}
		})
	}
	i.Budget.MaxItems = 1
	r.Budget = i.Budget
	if _, err := d.Listing(i, r); err == nil {
		t.Fatal("element bound ignored")
	}
}

func TestXMLRejectsEntitiesAndDepth(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.xml", "application/rss+xml")
	for _, content := range []string{`<!DOCTYPE rss [<!ENTITY x SYSTEM "https://evil.invalid/">]><rss><channel>&x;</channel></rss>`, `<!DOCTYPE rss [<!ENTITY x "test">]><rss><channel>&x;</channel></rss>`, strings.Repeat("<x>", 129) + strings.Repeat("</x>", 129), `<rss><channel></rss>`} {
		if _, err := d.Listing(changedBytes(i, []byte(content)), r); err == nil {
			t.Fatal("unsafe XML accepted")
		}
	}
}

func TestDetailNegativesAndMissingArtifact(t *testing.T) {
	i, r, d := decoderFixture(t, "detail.html", "text/html")
	request := detailRequest(i, r)
	for _, content := range [][]byte{i.Content[:len(i.Content)/2], []byte("<broken"), make([]byte, i.Budget.MaxInputBytes+1)} {
		if _, err := d.Detail(changedBytes(i, content), request); err == nil {
			t.Fatal("bad detail accepted")
		}
	}
	bad := i
	bad.Capture.MediaType = "application/json"
	if _, err := d.Detail(bad, request); err == nil {
		t.Fatal("unexpected detail media accepted")
	}
	content := string(i.Content)
	for {
		start := strings.Index(content, "<a ")
		if start < 0 {
			break
		}
		end := strings.Index(content[start:], "</a>")
		if end < 0 {
			t.Fatal("fixture link is not closed")
		}
		content = content[:start] + content[start+end+4:]
	}
	out, err := d.Detail(changedBytes(i, []byte(content)), request)
	if err != nil || len(out.Artifacts) != 0 || len(out.Findings) != 1 || out.Findings[0].ReasonCode != "MISSING_REQUIRED_ARTIFACT" {
		t.Fatalf("missing artifact hidden: %v %+v", err, out)
	}
}

func TestArtifactHostSmuggling(t *testing.T) {
	i, r, d := decoderFixture(t, "detail.html", "text/html")
	request := detailRequest(i, r)
	for _, replacement := range []string{"https://evil.invalid/api/download/stream?file=test", "//evil.invalid/api/download/stream?file=test", "https://g7.cdnchinhphu.vn/incorrect/synthetic.doc", "https://g7.cdnchinhphu.vn/%2e%2e/api/download/stream?file=test"} {
		content := strings.Replace(string(i.Content), "https://g7.cdnchinhphu.vn/api/download/stream?file=synthetic&amp;format=doc", replacement, 1)
		if content == string(i.Content) {
			t.Fatal("negative fixture did not replace artifact")
		}
		if _, err := d.Detail(changedBytes(i, []byte(content)), request); err == nil {
			t.Fatalf("unsafe artifact accepted: %s", replacement)
		}
	}
}

func TestListingRejectsSmuggledHostAndUnknownCharset(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.xml", "application/rss+xml")
	content := strings.Replace(string(i.Content), "https://congbao.chinhphu.vn", "//evil.invalid", 1)
	if content == string(i.Content) {
		t.Fatal("negative fixture did not replace detail")
	}
	if _, err := d.Listing(changedBytes(i, []byte(content)), r); err == nil {
		t.Fatal("relative host escaped source")
	}
	i.Capture.MediaType = "application/rss+xml; charset=windows-1258"
	if _, err := d.Listing(i, r); err == nil {
		t.Fatal("unverified charset accepted")
	}
}

func TestSearchPreservesCandidatesAndDeclaredFilename(t *testing.T) {
	i, r, d := decoderFixture(t, "search.json", "application/json")
	out, err := d.Listing(i, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Observations) != 2 || out.Cursor.Complete {
		t.Fatal("search selected a candidate or proved absence")
	}
	detail, err := d.Detail(i, detailRequest(i, r))
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Artifacts) != 1 || detail.Artifacts[0].Format != "doc" || detail.Artifacts[0].DeclaredFilename != "Tên nguồn không có phần mở rộng" {
		t.Fatalf("invented declared filename: %+v", detail)
	}
}

func TestDecoderOutputSharesFrozenBudget(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.xml", "application/rss+xml")
	// This allowance passes capture-size verification but cannot hold the
	// decoded metadata, duplicate provenance and follow-up strings together.
	i.Budget.MaxInputBytes = int64(len(i.Content))
	r.Budget = i.Budget
	if _, err := d.Listing(i, r); err == nil {
		t.Fatal("decoded output reset frozen byte allowance")
	}
	var zero Decoder
	if _, err := zero.Listing(i, r); err == nil {
		t.Fatal("zero decoder accepted work")
	}
}

func TestSearchRejectsCaseVariantKeys(t *testing.T) {
	i, r, d := decoderFixture(t, "search.json", "application/json")
	for _, key := range []string{"success", "total", "data", "id_van_ban", "so_ky_hieu", "duong_dan", "ten_file"} {
		t.Run(key, func(t *testing.T) {
			content := strings.Replace(string(i.Content), `"`+key+`":`, `"`+strings.ToUpper(key)+`":`, 1)
			_, err := d.Listing(changedBytes(i, []byte(content)), r)
			if e, ok := err.(*DecodeError); !ok || e.Code != "CASE_VARIANT_JSON_KEY" {
				t.Fatalf("expected exact-key rejection: %v", err)
			}
		})
	}
	content := strings.Replace(string(i.Content), `"success":true`, `"success":true,"SUCCESS":false`, 1)
	if _, err := d.Listing(changedBytes(i, []byte(content)), r); err == nil {
		t.Fatal("case alias accepted alongside exact key")
	}
}

func TestDetailRetainsEmptyFilename(t *testing.T) {
	i, r, d := decoderFixture(t, "detail.html", "text/html")
	content := strings.Replace(string(i.Content), `data-file="synthetic.doc"`, `data-file=""`, 1)
	out, err := d.Detail(changedBytes(i, []byte(content)), detailRequest(i, r))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Artifacts) != 2 || out.Artifacts[0].DeclaredFilename != "" || out.Artifacts[0].Format != "unknown" || len(out.FollowUps) != 2 {
		t.Fatalf("empty filename lost: %+v", out)
	}
}

func TestDuplicateDownloadAttributeRejected(t *testing.T) {
	i, r, d := decoderFixture(t, "detail.html", "text/html")
	content := strings.Replace(string(i.Content), `data-file="synthetic.doc"`, `data-file="synthetic.doc" DATA-FILE="other.pdf"`, 1)
	if content == string(i.Content) {
		t.Fatal("fixture mutation missed download")
	}
	_, err := d.Detail(changedBytes(i, []byte(content)), detailRequest(i, r))
	if e, ok := err.(*DecodeError); !ok || e.Code != "DUPLICATE_HTML_ATTRIBUTE" {
		t.Fatalf("expected duplicate-attribute rejection: %v", err)
	}
}

func TestDuplicateRSSScalarRejected(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.xml", "application/rss+xml")
	locator := "https://congbao.chinhphu.vn/van-ban/nghi-quyet-so-116-2025-qh15-123.htm"
	for _, element := range []string{
		"<title>second</title>", "<description>second</description>", "<pubDate>second</pubDate>",
		"<link>" + locator + "</link>", "<guid>" + locator + "</guid><guid>" + locator + "</guid>",
		`<x:title xmlns:x="urn:synthetic">extension</x:title><title>second</title>`,
	} {
		t.Run(element, func(t *testing.T) {
			content := string(i.Content)
			if strings.HasPrefix(element, "<guid>") {
				content = strings.Replace(content, "<link>"+locator+"</link>", "", 1)
			}
			content = strings.Replace(content, "</item>", element+"</item>", 1)
			if content == string(i.Content) {
				t.Fatal("fixture mutation missed RSS item")
			}
			_, err := d.Listing(changedBytes(i, []byte(content)), r)
			if e, ok := err.(*DecodeError); !ok || e.Code != "DUPLICATE_RSS_ELEMENT" {
				t.Fatalf("expected duplicate-element rejection: %v", err)
			}
		})
	}
	content := strings.Replace(string(i.Content), "</rss>", "<channel></channel></rss>", 1)
	_, err := d.Listing(changedBytes(i, []byte(content)), r)
	if e, ok := err.(*DecodeError); !ok || e.Code != "DUPLICATE_RSS_ELEMENT" {
		t.Fatalf("expected duplicate-channel rejection: %v", err)
	}
	content = strings.Replace(string(i.Content), "</channel>", "<item><description>second</description><link>"+strings.Replace(locator, "123.htm", "456.htm", 1)+"</link></item></channel>", 1)
	out, err := d.Listing(changedBytes(i, []byte(content)), r)
	if err != nil || len(out.Observations) != 2 {
		t.Fatalf("distinct RSS items rejected: %v", err)
	}
}

func TestRSSIgnoresExtensionElements(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.xml", "application/rss+xml")
	locator := "https://congbao.chinhphu.vn/van-ban/nghi-quyet-so-116-2025-qh15-123.htm"
	link := "<link>" + locator + "</link>"
	atomLink := `<atom:link xmlns:atom="http://www.w3.org/2005/Atom" rel="alternate" href="https://example.invalid/extension">https://example.invalid/extension<link>nested extension</link></atom:link>`
	for _, tc := range []struct{ name, replacement string }{
		{"AtomLinkBeforeRSSLink", atomLink + link},
		{"AtomLinkAfterRSSLink", link + atomLink},
		{"RepeatedDCCreator", link + `<dc:creator xmlns:dc="http://purl.org/dc/elements/1.1/">first</dc:creator><dc:creator xmlns:dc="http://purl.org/dc/elements/1.1/">second</dc:creator>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := strings.Replace(string(i.Content), link, tc.replacement, 1)
			if content == string(i.Content) {
				t.Fatal("fixture mutation missed RSS link")
			}
			out, err := d.Listing(changedBytes(i, []byte(content)), r)
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Observations) != 1 || out.Observations[0].ExternalID != "123" || len(out.FollowUps) != 1 || out.FollowUps[0].URL != locator {
				t.Fatalf("extension changed RSS detail: %+v", out)
			}
			if err := out.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDetailIgnoresLayoutRows(t *testing.T) {
	i, r, d := decoderFixture(t, "detail.html", "text/html")
	content := strings.Replace(string(i.Content), "<body>", `<body><div class="row"><div class="row">Layout only</div><div class="row"><div class="value">Layout value</div></div>`, 1)
	content = strings.Replace(content, "</body>", "</div></body>", 1)
	if _, err := d.Detail(changedBytes(i, []byte(content)), detailRequest(i, r)); err != nil {
		t.Fatalf("layout rejected: %v", err)
	}
	for _, row := range []string{`<div class="row"><span class="name">Số, ký hiệu</span></div>`, `<div class="row"><span class="name">Số, ký hiệu</span><div class="value">1</div><div class="value">2</div></div>`} {
		bad := strings.Replace(string(i.Content), "</body>", row+"</body>", 1)
		if _, err := d.Detail(changedBytes(i, []byte(bad)), detailRequest(i, r)); err == nil {
			t.Fatal("malformed metadata accepted")
		}
	}
}

func TestRSSFallbackAnchors(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.xml", "application/rss+xml")
	content := strings.Replace(string(i.Content), "<title></title><description>Văn bản thử nghiệm</description>", "<title>Tiêu đề thử nghiệm</title>", 1)
	content = strings.ReplaceAll(strings.ReplaceAll(content, "<link>", "<guid>"), "</link>", "</guid>")
	out, err := d.Listing(changedBytes(i, []byte(content)), r)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"title": "Tiêu đề thử nghiệm", "source_slug": "nghi-quyet-so-116-2025-qh15"}
	for _, f := range out.Observations[0].Fields {
		if value, ok := want[f.Name]; ok {
			key := "title"
			if f.Name == "source_slug" {
				key = "guid"
			}
			if f.Value != value || len(f.Sources) != 1 || f.Sources[0].Object != "/rss/channel/item/0/"+key {
				t.Fatalf("wrong fallback evidence: %+v", f)
			}
			delete(want, f.Name)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing fallback fields: %+v", want)
	}
}

func TestSearchRetainsGazetteIssueEvidence(t *testing.T) {
	i, r, d := decoderFixture(t, "search.json", "application/json")
	content := strings.Replace(string(i.Content), `"id_van_ban":123`, `"id_van_ban":123,"danh_sach_ky_cong_bao":[{"ten":"Số 1 + 2","ngay_ban_hanh":"2025-01-02"},{"ten":"Số 3","ngay_ban_hanh":"2025-01-03"}]`, 1)
	out, err := d.Listing(changedBytes(i, []byte(content)), r)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"/data/0/danh_sach_ky_cong_bao/0/ten": "Số 1 + 2", "/data/0/danh_sach_ky_cong_bao/0/ngay_ban_hanh": "2025-01-02", "/data/0/danh_sach_ky_cong_bao/1/ten": "Số 3", "/data/0/danh_sach_ky_cong_bao/1/ngay_ban_hanh": "2025-01-03"}
	published := false
	for _, f := range out.Observations[0].Fields {
		if value, ok := want[f.Sources[0].Object]; ok && value == f.Value {
			delete(want, f.Sources[0].Object)
		}
		if f.Name == "published_at" && f.Value == "2025-01-02" && f.Sources[0].Object == "/data/0/danh_sach_ky_cong_bao/0/ngay_ban_hanh" {
			published = true
		}
	}
	if len(want) != 0 || !published {
		t.Fatalf("missing gazette evidence: %+v", out.Observations[0])
	}
}

func TestRSSDepthBoundaryWithinValidFeed(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.xml", "application/rss+xml")
	for _, depth := range []int{126, 127} {
		content := strings.Replace(string(i.Content), "</channel>", strings.Repeat("<unknown>", depth)+strings.Repeat("</unknown>", depth)+"</channel>", 1)
		_, err := d.Listing(changedBytes(i, []byte(content)), r)
		if depth == 126 && err != nil {
			t.Fatalf("depth boundary rejected: %v", err)
		}
		if depth == 127 {
			if e, ok := err.(*DecodeError); !ok || e.Code != "DEPTH_BOUND" {
				t.Fatalf("expected depth rejection: %v", err)
			}
		}
	}
}

func TestHTMLDepthAndAttributeBoundaries(t *testing.T) {
	i, r, d := decoderFixture(t, "detail.html", "text/html")
	for _, depth := range []int{123, 124} {
		content := strings.Replace(string(i.Content), "<body>", "<body>"+strings.Repeat("<div>", depth), 1)
		content = strings.Replace(content, "</body>", strings.Repeat("</div>", depth)+"</body>", 1)
		_, err := d.Detail(changedBytes(i, []byte(content)), detailRequest(i, r))
		if depth == 123 && err != nil {
			t.Fatalf("depth boundary rejected: %v", err)
		}
		if depth == 124 {
			if e, ok := err.(*DecodeError); !ok || e.Code != "DEPTH_BOUND" {
				t.Fatalf("expected depth rejection: %v", err)
			}
		}
	}
	// Valid detail metadata stays present while ignored wrapper attributes consume the budget.
	for _, extra := range []int{0, 1} {
		var attrs strings.Builder
		for n := 0; n < 9987+extra; n++ {
			fmt.Fprintf(&attrs, ` a%d="x"`, n)
		}
		content := `<section` + attrs.String() + `><div class="row"><span class="name">Số, ký hiệu</span><div class="value">1</div></div></section>`
		_, err := d.Detail(changedBytes(i, []byte(content)), detailRequest(i, r))
		if extra == 0 && err != nil {
			t.Fatalf("attribute boundary rejected: %v", err)
		}
		if extra == 1 {
			if e, ok := err.(*DecodeError); !ok || e.Code != "ELEMENT_BOUND" {
				t.Fatalf("expected attribute resource rejection: %v", err)
			}
		}
	}
}

func TestSearchDepthBoundaryWithinValidListing(t *testing.T) {
	i, r, d := decoderFixture(t, "search.json", "application/json")
	for _, depth := range []int{126, 127} {
		content := strings.Replace(string(i.Content), `"success":true`, `"success":true,"unknown":`+strings.Repeat("[", depth)+"0"+strings.Repeat("]", depth), 1)
		_, err := d.Listing(changedBytes(i, []byte(content)), r)
		if depth == 126 && err != nil {
			t.Fatalf("JSON depth boundary rejected: %v", err)
		}
		if depth == 127 {
			if e, ok := err.(*DecodeError); !ok || e.Code != "DEPTH_BOUND" {
				t.Fatalf("expected JSON depth rejection: %v", err)
			}
		}
	}
}

func TestHTMLLastTokenAttributeBoundary(t *testing.T) {
	i, _, _ := decoderFixture(t, "detail.html", "text/html")
	// Count the valid detail's tokens, then put attributes on its final void
	// element. No later token can turn an ignored attribute guard into a pass.
	z := html.NewTokenizer(strings.NewReader(string(i.Content)))
	count := 0
	for kind := z.Next(); kind != html.ErrorToken; kind = z.Next() {
		count++
		if kind == html.StartTagToken || kind == html.SelfClosingTagToken {
			count += len(z.Token().Attr)
		}
	}
	i.Budget.MaxItems = count + 3
	for _, attrs := range []string{` a="1" b="2"`, ` a="1" b="2" c="3"`} {
		content := string(i.Content) + "<input" + attrs + ">"
		_, err := parseHTML(changedBytes(i, []byte(content)))
		if attrs == ` a="1" b="2"` && err != nil {
			t.Fatalf("last-token attribute boundary rejected: %v", err)
		}
		if attrs == ` a="1" b="2" c="3"` {
			if e, ok := err.(*DecodeError); !ok || e.Code != "ELEMENT_BOUND" {
				t.Fatalf("expected last-token attribute rejection: %v", err)
			}
		}
	}
}

func TestSearchDocumentDecodesOnlyExactKeys(t *testing.T) {
	var doc searchDocument
	content := []byte(`{"id_van_ban":123,"SO_KY_HIEU":"alias","TIEU_DE":"alias","so_ky_hieu":"exact","tieu_de":"exact title","danh_sach_ky_cong_bao":[{"TEN":"alias","ten":"exact issue","NGAY_BAN_HANH":"alias"}],"danh_sach_tep_van_ban":[{"DUONG_DAN":"alias","ten_file":"exact file"}]}`)
	if err := json.Unmarshal(content, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Number != "exact" || doc.Title != "exact title" || len(doc.GazetteIssues) != 1 || doc.GazetteIssues[0].Name != "exact issue" || doc.GazetteIssues[0].Date != "" || len(doc.Files) != 1 || doc.Files[0].URL != "" || doc.Files[0].Name != "exact file" {
		t.Fatalf("case alias decoded: %+v", doc)
	}
}
