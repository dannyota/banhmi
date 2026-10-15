package vanban

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
	i, r, d := decoderFixture(t, "listing.html", "text/html")
	p, err := d.Listing(i, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Observations) != 1 || p.Observations[0].ExternalID != "123" || len(p.FollowUps) != 1 || p.Cursor.Complete != true {
		t.Fatalf("unexpected page: %+v", p)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, f := range p.Observations[0].Fields {
		values[f.Name] = f.Value
	}
	if values["number"] != "116/ 2025/QH15" || values["number_normalized"] != "116/2025/QH15" || values["title"] != "Văn bản tổng hợp thử nghiệm" && values["title"] != "Văn bản thử nghiệm" {
		t.Fatalf("golden field mismatch: %+v", values)
	}
	if values["type"] != "" && values["type"] != "Nghị quyết" {
		t.Fatal("invented type")
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
	i, r, d := decoderFixture(t, "listing.html", "text/html")
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

func TestWebFormsContinuation(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.html", "text/html")
	pager := `<tr><td><a href="javascript:__doPostBack(&#39;ctrl_191017_163$grvDocument&#39;,&#39;Page$2&#39;)">2</a></td></tr>`
	i = changedBytes(i, []byte(strings.Replace(string(i.Content), "</table>", pager+"</table>", 1)))
	out, err := d.Listing(i, r)
	if err != nil {
		t.Fatal(err)
	}
	if out.Cursor.Complete || out.Continuation == nil || out.Continuation.Target != "ctrl_191017_163$grvDocument" || len(out.Continuation.Fields) != 2 || out.Continuation.Fields[0].Value != "synthetic+&=" {
		t.Fatalf("postback state lost: %+v", out)
	}
	origin, err := d.planner.Listing(r)
	if err != nil {
		t.Fatal(err)
	}
	next, err := d.planner.NextPage(api.ContinuationRequest{Origin: origin, WindowKey: r.WindowKey, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget, Mode: r.Mode, ParentCapture: i.Capture, Target: out.Continuation.Target, Fields: out.Continuation.Fields})
	if err != nil || next.Cursor != "2" {
		t.Fatalf("continuation cannot reach planner: %v", err)
	}
	r.Budget.MaxPages = 1
	i.Budget = r.Budget
	out, err = d.Listing(i, r)
	if err != nil || out.Cursor.Complete || len(out.Findings) == 0 {
		t.Fatalf("capped page completed: %v %+v", err, out)
	}
}

func TestWebFormsRejectsDuplicateAndMissingState(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.html", "text/html")
	duplicate := strings.Replace(string(i.Content), "</form>", `<input type="hidden" name="__VIEWSTATE" value="other"/></form>`, 1)
	if _, err := d.Listing(changedBytes(i, []byte(duplicate)), r); err == nil {
		t.Fatal("duplicate hidden state accepted")
	}
	pager := `<tr><td><a href="javascript:__doPostBack('ctrl_191017_163$grvDocument','Page$2')">2</a></td></tr>`
	missing := strings.Replace(string(i.Content), "__VIEWSTATE", "__OTHER", 1)
	missing = strings.Replace(missing, "</table>", pager+"</table>", 1)
	if _, err := d.Listing(changedBytes(i, []byte(missing)), r); err == nil {
		t.Fatal("missing required state accepted")
	} else if e, ok := err.(*DecodeError); !ok || e.Code != "MISSING_FORM_STATE" {
		t.Fatalf("missing state failed for unrelated reason: %v", err)
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
	for _, replacement := range []string{"https://evil.invalid/cpp/files/synthetic.doc", "//evil.invalid/cpp/files/synthetic.doc", "https://datafiles.chinhphu.vn/incorrect/synthetic.doc", "https://datafiles.chinhphu.vn/%2e%2e/cpp/files/synthetic.doc"} {
		content := strings.Replace(string(i.Content), "https://datafiles.chinhphu.vn/cpp/files/synthetic.doc", replacement, 1)
		if content == string(i.Content) {
			t.Fatal("negative fixture did not replace artifact")
		}
		if _, err := d.Detail(changedBytes(i, []byte(content)), request); err == nil {
			t.Fatalf("unsafe artifact accepted: %s", replacement)
		}
	}
}

func TestListingRejectsSmuggledHostAndUnknownCharset(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.html", "text/html")
	content := strings.Replace(string(i.Content), "/?pageid=", "//evil.invalid", 1)
	if content == string(i.Content) {
		t.Fatal("negative fixture did not replace detail")
	}
	if _, err := d.Listing(changedBytes(i, []byte(content)), r); err == nil {
		t.Fatal("relative host escaped source")
	}
	i.Capture.MediaType = "text/html; charset=windows-1258"
	if _, err := d.Listing(i, r); err == nil {
		t.Fatal("unverified charset accepted")
	}
}

func TestDecoderOutputSharesFrozenBudget(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.html", "text/html")
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

func TestTrailingUnfinishedHTMLCannotCompletePage(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.html", "text/html")
	i = changedBytes(i, append(append([]byte(nil), i.Content...), []byte("<unfinished")...))
	if _, err := d.Listing(i, r); err == nil {
		t.Fatal("unfinished trailing tag completed coverage")
	}
}

func TestDuplicateHiddenAttributeRejected(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.html", "text/html")
	content := strings.Replace(string(i.Content), `name="__VIEWSTATE"`, `name="__VIEWSTATE" name="other"`, 1)
	if _, err := d.Listing(changedBytes(i, []byte(content)), r); err == nil {
		t.Fatal("ambiguous hidden-field attribute accepted")
	}
}

func TestHiddenAttributeSyntax(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.html", "text/html")
	for _, tc := range []struct{ attrs, code string }{
		{`name="__VIEWSTATE" NAME='other'`, "DUPLICATE_HTML_ATTRIBUTE"},
		{`name=__VIEWSTATE name=other`, "DUPLICATE_HTML_ATTRIBUTE"},
		{`name='__VIEWSTATE'name=other`, "DUPLICATE_HTML_ATTRIBUTE"},
		{`name="__VIEWSTATE" /name=other`, "DUPLICATE_HTML_ATTRIBUTE"},
		{`name="__VIEWSTATE" name`, "DUPLICATE_HTML_ATTRIBUTE"},
		{`name='__VIEWSTATE' data-note='name="other"'`, ""},
		{`name=__VIEWSTATE data-note=name=other`, ""},
		{`name="__VIEWSTATE" =ignored=other`, ""},
	} {
		t.Run(tc.attrs, func(t *testing.T) {
			content := strings.Replace(string(i.Content), `name="__VIEWSTATE"`, tc.attrs, 1)
			if content == string(i.Content) {
				t.Fatal("fixture mutation missed hidden input")
			}
			_, err := d.Listing(changedBytes(i, []byte(content)), r)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("unambiguous attributes rejected: %v", err)
				}
			} else if e, ok := err.(*DecodeError); !ok || e.Code != tc.code {
				t.Fatalf("expected duplicate-attribute rejection: %v", err)
			}
		})
	}
}

func TestListingSiblingPagerAndRepeatedAnchors(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.html", "text/html")
	content := string(i.Content)
	content = strings.Replace(content, "116/ 2025/QH15", "66.116/2025/QH15", 1)
	content = strings.Replace(content, "</table>", `</table><div class="grid-pager"><a href="javascript:__doPostBack('ctrl_191017_163$grvDocument','Page$2')">2</a></div>`, 1)
	out, err := d.Listing(changedBytes(i, []byte(content)), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Observations) != 1 || len(out.FollowUps) != 1 || out.Cursor.Complete || out.Continuation == nil || out.Continuation.Target != "ctrl_191017_163$grvDocument" {
		t.Fatalf("legacy-shaped listing lost identity or pager: %+v", out)
	}
	values := map[string]string{}
	for _, f := range out.Observations[0].Fields {
		values[f.Name] = f.Value
		if f.Name == "number_normalized" {
			span := f.Sources[0].Range
			if span == nil || !strings.Contains(content[span.Start:span.End], "66.116/2025/QH15") {
				t.Fatal("normalized number lost raw span")
			}
		}
	}
	if values["number"] != "66.116/2025/QH15" || values["number_normalized"] != "116/2025/QH15" {
		t.Fatalf("listing sequence normalization: %+v", values)
	}
}

func TestListingRejectsMalformedCandidateRows(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.html", "text/html")
	for name, row := range map[string]string{
		"missing code":           `<tr><td><a href="/?pageid=27160&amp;docid=456&amp;classid=1">document</a></td></tr>`,
		"empty code":             `<tr><td><a href="/?pageid=27160&amp;docid=456&amp;classid=1"><span class="code"></span></a></td></tr>`,
		"missing locator":        `<tr><td><span class="substract">document</span></td></tr>`,
		"conflicting locator":    `<tr><td><span class="code">1/2025/QH15</span><a href="/?pageid=27160&amp;docid=456&amp;classid=1">number</a><a href="/?pageid=27160&amp;docid=789&amp;classid=1">title</a></td></tr>`,
		"malformed second query": `<tr><td><span class="code">1/2025/QH15</span><a href="/?pageid=27160&amp;docid=456&amp;classid=1">number</a><a href="/?pageid=27160&amp;docid=456&amp;classid=1&amp;bad;x">title</a></td></tr>`,
		"unsafe second locator":  `<tr><td><span class="code">1/2025/QH15</span><a href="/?pageid=27160&amp;docid=456&amp;classid=1">number</a><a href="https://evil.invalid/?pageid=27160&amp;docid=456&amp;classid=1">title</a></td></tr>`,
	} {
		t.Run(name, func(t *testing.T) {
			content := strings.Replace(string(i.Content), "</table>", row+"</table>", 1)
			if _, err := d.Listing(changedBytes(i, []byte(content)), r); err == nil {
				t.Fatal("malformed candidate completed listing")
			}
		})
	}
}

func TestListingPagerMustBelongToGridForm(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.html", "text/html")
	wrong := strings.Replace(string(i.Content), "</table>", `</table><a href="javascript:__doPostBack('other$grvDocument','Page$2')">2</a>`, 1)
	if _, err := d.Listing(changedBytes(i, []byte(wrong)), r); err == nil {
		t.Fatal("different grid postback accepted")
	}
	unrelated := strings.Replace(string(i.Content), "</table>", `</table><a href="javascript:__doPostBack('search','Search')">search</a>`, 1)
	out, err := d.Listing(changedBytes(i, []byte(unrelated)), r)
	if err != nil || !out.Cursor.Complete {
		t.Fatalf("unrelated control affected listing: %v", err)
	}
	separate := strings.Replace(string(i.Content), "</form>", `</form><form><input type="hidden" name="__VIEWSTATE" value="unrelated"/><a href="javascript:__doPostBack('other$grvDocument','Page$2')">2</a></form>`, 1)
	out, err = d.Listing(changedBytes(i, []byte(separate)), r)
	if err != nil || !out.Cursor.Complete || out.Continuation != nil {
		t.Fatalf("unrelated form affected listing: %v %+v", err, out)
	}
}

func TestDetailNumberKeepsSequenceLikePrefix(t *testing.T) {
	i, r, d := decoderFixture(t, "detail.html", "text/html")
	i = changedBytes(i, []byte(strings.Replace(string(i.Content), "116/ 2025/QH15", "66.116/2025/QH15", 1)))
	out, err := d.Detail(i, detailRequest(i, r))
	if err != nil {
		t.Fatal(err)
	}
	number := ""
	for _, f := range out.Observation.Fields {
		if f.Name == "number_normalized" {
			number = f.Value
		}
	}
	if number != "66.116/2025/QH15" {
		t.Fatalf("listing-only rule changed detail number: %q", number)
	}
}

func TestHTMLDepthAndAttributeBoundaries(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.html", "text/html")
	for _, tc := range []struct {
		depth int
		code  string
	}{{128, ""}, {129, "DEPTH_BOUND"}} {
		content := string(i.Content) + strings.Repeat("<div>", tc.depth) + "text" + strings.Repeat("</div>", tc.depth)
		out, err := d.Listing(changedBytes(i, []byte(content)), r)
		if tc.code == "" && err == nil && (len(out.Observations) != 1 || !out.Cursor.Complete) {
			t.Fatal("valid deep response lost listing")
		}
		if tc.code == "" && err != nil {
			t.Fatalf("valid depth rejected: %v", err)
		}
		if tc.code != "" {
			if e, ok := err.(*DecodeError); !ok || e.Code != tc.code {
				t.Fatalf("depth bound: %v", err)
			}
		}
	}
	// The fixture consumes 41 tokens and attributes. A final void token
	// consumes one token and two attributes, with no later token to fail.
	content := append(append([]byte(nil), i.Content...), []byte(`<input a="1" b="2"/>`)...)
	for _, tc := range []struct {
		max  int
		code string
	}{{44, ""}, {43, "ELEMENT_BOUND"}} {
		input := changedBytes(i, content)
		input.Budget.MaxItems = tc.max
		_, err := parseHTML(input)
		if tc.code == "" && err != nil {
			t.Fatalf("attribute boundary rejected: %v", err)
		}
		if tc.code != "" {
			if e, ok := err.(*DecodeError); !ok || e.Code != tc.code {
				t.Fatalf("attribute bound: %v", err)
			}
		}
	}
}

func TestDetailIgnoresMetadataAndRelatedLinks(t *testing.T) {
	i, r, d := decoderFixture(t, "detail.html", "text/html")
	content := strings.Replace(string(i.Content), "<td>Nghị quyết</td>", `<td><a href="/?pageid=27160&amp;docid=456&amp;classid=1">Nghị quyết</a><a href="https://datafiles.chinhphu.vn/cpp/files/metadata.pdf">metadata</a></td>`, 1)
	content = strings.Replace(content, "</div>", `<div class="related"><a href="/?pageid=27160&amp;docid=456&amp;classid=1">related</a><a href="https://datafiles.chinhphu.vn/cpp/files/related.pdf">related file</a></div></div>`, 1)
	out, err := d.Detail(changedBytes(i, []byte(content)), detailRequest(i, r))
	if err != nil || len(out.Artifacts) != 2 || len(out.FollowUps) != 2 {
		t.Fatalf("non-attachment link entered inventory: %v %+v", err, out)
	}
}

func TestDetailFileRegionRetainsEveryDeclaredFormat(t *testing.T) {
	i, r, d := decoderFixture(t, "detail.html", "text/html")
	content := strings.Replace(string(i.Content), "</div>", `<div class="bl-doc-file"><a href="https://datafiles.chinhphu.vn/cpp/files/appendix.xlsx">appendix</a></div></div>`, 1)
	out, err := d.Detail(changedBytes(i, []byte(content)), detailRequest(i, r))
	if err != nil || len(out.Artifacts) != 3 || out.Artifacts[2].Format != "xlsx" {
		t.Fatalf("file region attachment omitted: %v %+v", err, out)
	}
}

func TestListingFullPageAndMissingCodeCannotProveExhaustion(t *testing.T) {
	i, r, d := decoderFixture(t, "listing.html", "text/html")
	var rows strings.Builder
	for n := 1; n <= 50; n++ {
		fmt.Fprintf(&rows, `<tr><td><a href="/?pageid=27160&amp;docid=%d&amp;classid=1"><span class="code">%d/2025/QH15</span></a></td></tr>`, n, n)
	}
	content := `<form><table id="ctrl_grvDocument">` + rows.String() + `</table></form>`
	out, err := d.Listing(changedBytes(i, []byte(content)), r)
	if err != nil || len(out.Observations) != 50 || out.Cursor.Complete || len(out.Findings) != 1 || out.Findings[0].ReasonCode != "UNPROVED_EXHAUSTION" {
		t.Fatalf("full page proved exhaustion: %v %+v", err, out)
	}
	content = strings.Replace(content, `<span class="code">50/2025/QH15</span>`, "", 1)
	if _, err := d.Listing(changedBytes(i, []byte(content)), r); err == nil {
		t.Fatal("49 decoded rows plus malformed final row proved exhaustion")
	}
}
