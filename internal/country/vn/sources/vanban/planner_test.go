package vanban

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/source/api"
)

func fixture(t *testing.T) (*Planner, api.DiscoverRequest, model.CaptureRef) {
	t.Helper()
	digest := model.Digest(strings.Repeat("a", 64))
	p, err := New(api.SourcePolicy{Manifest: model.AssetRef{Path: "policy/vanban.json", SHA256: digest, ByteSize: 1}, Digest: digest})
	if err != nil {
		t.Fatal(err)
	}
	r := api.DiscoverRequest{Country: "vn", SourceID: SourceID, WindowKey: "window", SliceKey: "central", Mode: api.DiscoveryIncremental, Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Cutoff: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), PolicyHash: digest, BudgetHash: model.Digest(strings.Repeat("b", 64)), Budget: model.ResourceBudget{MaxItems: 200, MaxInputBytes: 10000, MaxPages: 300, MaxElapsed: time.Minute, MaxConcurrent: 1}}
	c := model.CaptureRef{Country: "vn", SourceID: SourceID, CaptureKey: "capture", Asset: model.AssetRef{Path: "capture/list", SHA256: digest}, MediaType: "text/html", RequestHash: digest, ResponseHash: digest, TransportReceipt: model.AssetRef{Path: "capture/receipt", SHA256: digest}}
	return p, r, c
}

func TestListingAndPostbackGolden(t *testing.T) {
	p, r, _ := fixture(t)
	origin, err := p.Listing(r)
	if err != nil || origin.URL != "https://vanban.chinhphu.vn/he-thong-van-ban?classid=1&mode=1" || origin.Method != "GET" {
		t.Fatalf("listing: %+v %v", origin, err)
	}
	hidden := url.Values{"__VIEWSTATE": {"state+&="}, "__EVENTVALIDATION": {"validation"}, "__VIEWSTATEGENERATOR": {"generator"}, "site": {"constant"}}
	next, err := p.NextPage(r, origin, "capture", "ctrl_191017_163$grvDocument", hidden)
	if err != nil {
		t.Fatal(err)
	}
	form, err := url.ParseQuery(string(next.Body))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"__EVENTTARGET": "ctrl_191017_163$grvDocument", "__EVENTARGUMENT": "Page$2", "__VIEWSTATE": "state+&=", "__EVENTVALIDATION": "validation", "__VIEWSTATEGENERATOR": "generator", "site": "constant", "ctrl_190922_45$txtSearch": "", "ctrl_191017_163$drdDocCategory": "0", "ctrl_191017_163$drdDocOrg": "0", "ctrl_191017_163$drdDocYear": "0", "ctrl_191017_163$txtSearchKeyword": "", "ctrl_191017_163$drdRecordPerPage": "50"}
	if len(form) != len(want) || next.Method != "POST" || next.URL != origin.URL || next.ParentCapture != "capture" || next.Cursor != "2" {
		t.Fatalf("postback: %+v %v", next, form)
	}
	for key, value := range want {
		if form.Get(key) != value {
			t.Errorf("%s = %q, want %q", key, form.Get(key), value)
		}
	}
	if hidden.Get("__EVENTTARGET") != "" || next.ValidateOrigin(r.WindowKey, r.BudgetHash, r.Budget) != nil {
		t.Fatal("postback mutated state or reset budget")
	}
	for _, mode := range []api.DiscoveryMode{api.DiscoveryIncremental, api.DiscoveryBackfill} {
		r.Mode = mode
		origin.Cursor = "40"
		if mode == api.DiscoveryBackfill {
			origin.Cursor = "240"
		}
		if _, err := p.NextPage(r, origin, "capture", "ctrl$grvDocument", hidden); err == nil {
			t.Fatal("legacy page cap accepted")
		}
	}
	r.Cursor = "https://evil.example/../x"
	if _, err := p.Listing(r); err == nil {
		t.Fatal("URL cursor accepted")
	}
}

func TestDetailArtifactAndFollowUp(t *testing.T) {
	p, r, capture := fixture(t)
	d := api.DetailRequest{Country: r.Country, SourceID: r.SourceID, ExternalID: "216334", ListingCaptures: []model.CaptureRef{capture}, PolicyHash: r.PolicyHash, WindowKey: r.WindowKey, BudgetHash: r.BudgetHash, Budget: r.Budget}
	origin, err := p.Detail(d)
	if err != nil || origin.URL != "https://vanban.chinhphu.vn/?pageid=27160&docid=216334&classid=1" {
		t.Fatalf("canonical detail: %+v %v", origin, err)
	}
	for _, id := range []string{"1&docid=2", "../1", "https://evil.example", "%2e%2e", ""} {
		d.ExternalID = id
		if _, err := p.Detail(d); err == nil {
			t.Fatalf("ID accepted: %q", id)
		}
	}
	locator := "https://datafiles.chinhphu.vn/cpp/files/vbpq/2026/01/luat134.signed.pdf"
	a := api.ArtifactRequest{Country: r.Country, SourceID: r.SourceID, ParentObservation: "observation", WindowKey: r.WindowKey, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget, Artifact: model.ArtifactInventoryEntry{ArtifactKey: "file", Role: "main", URL: locator}}
	plan, err := p.Artifact(a)
	if err != nil || plan.URL != locator {
		t.Fatalf("artifact: %+v %v", plan, err)
	}
	follow := model.FollowUpRequest{RequestKey: "follow", WindowKey: r.WindowKey, ParentCapture: "detail-capture", Purpose: model.PurposeArtifact, URL: locator, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget}
	child, err := p.FollowUp(origin, follow)
	if err != nil || child.ValidateOrigin(r.WindowKey, r.BudgetHash, r.Budget) != nil {
		t.Fatalf("follow-up: %+v %v", child, err)
	}
	follow.Budget.MaxPages++
	if _, err := p.FollowUp(origin, follow); err == nil {
		t.Fatal("budget reset accepted")
	}
	for _, locator := range []string{"https://evil.example/file", "http://datafiles.chinhphu.vn/file", "https://datafiles.chinhphu.vn:443/file", "https://datafiles.chinhphu.vn/a/../file", "https://datafiles.chinhphu.vn/%2e%2e/file", "https://datafiles.chinhphu.vn/%252e%252e/file", "https://datafiles.chinhphu.vn/a%5cb"} {
		a.Artifact.URL = locator
		if _, err := p.Artifact(a); err == nil {
			t.Fatalf("unsafe artifact accepted: %q", locator)
		}
	}
}

func TestPaginationRejectsUnboundedAndChangedOrigin(t *testing.T) {
	p, r, _ := fixture(t)
	origin, err := p.Listing(r)
	if err != nil {
		t.Fatal(err)
	}
	hidden := url.Values{"__VIEWSTATE": {"state"}}
	for _, target := range []string{"../grvDocument", "https://evil.example/grvDocument", "x&y=grvDocument"} {
		if _, err := p.NextPage(r, origin, "capture", target, hidden); err == nil {
			t.Fatalf("unsafe target %q", target)
		}
	}
	changed := r
	changed.BudgetHash = changed.PolicyHash
	if _, err := p.NextPage(changed, origin, "capture", "ctrl$grvDocument", hidden); err == nil {
		t.Fatal("budget identity changed")
	}
	hidden.Set("__VIEWSTATE", strings.Repeat("x", 10001))
	if _, err := p.NextPage(r, origin, "capture", "ctrl$grvDocument", hidden); err == nil {
		t.Fatal("unbounded form accepted")
	}
}

func TestInventoryBoundsAndPreservation(t *testing.T) {
	p, r, _ := fixture(t)
	origin, err := p.Listing(r)
	if err != nil {
		t.Fatal(err)
	}
	entries := []model.ArtifactInventoryEntry{{ArtifactKey: "doc", Role: "main", URL: "https://datafiles.chinhphu.vn/file.doc"}, {ArtifactKey: "other", Role: "appendix", URL: "https://datafiles.chinhphu.vn/file.bin", Ordinal: 1}}
	plans, err := p.Artifacts(origin, entries, "observation", "detail-capture")
	if err != nil || len(plans) != 2 {
		t.Fatalf("inventory: %+v %v", plans, err)
	}
	for i, plan := range plans {
		if plan.URL != entries[i].URL || plan.ParentCapture != "detail-capture" || plan.ValidateOrigin(r.WindowKey, r.BudgetHash, r.Budget) != nil {
			t.Fatalf("inventory origin changed: %+v", plan)
		}
	}
	if _, err := p.Artifacts(origin, make([]model.ArtifactInventoryEntry, r.Budget.MaxItems+1), "observation", "detail-capture"); err == nil {
		t.Fatal("oversized inventory accepted")
	}
	r.Budget.MaxItems = 49
	if _, err := p.Listing(r); err == nil {
		t.Fatal("initial WebForms GET with insufficient allowance accepted")
	}
}
