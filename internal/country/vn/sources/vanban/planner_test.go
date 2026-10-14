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
	next, err := p.NextPage(continuation(r, origin, "capture", "ctrl_191017_163$grvDocument", hidden))
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
		if _, err := p.NextPage(continuation(r, origin, "capture", "ctrl$grvDocument", hidden)); err == nil {
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
		if _, err := p.NextPage(continuation(r, origin, "capture", target, hidden)); err == nil {
			t.Fatalf("unsafe target %q", target)
		}
	}
	changed := r
	changed.BudgetHash = changed.PolicyHash
	if _, err := p.NextPage(continuation(changed, origin, "capture", "ctrl$grvDocument", hidden)); err == nil {
		t.Fatal("budget identity changed")
	}
	hidden.Set("__VIEWSTATE", strings.Repeat("x", 10001))
	if _, err := p.NextPage(continuation(r, origin, "capture", "ctrl$grvDocument", hidden)); err == nil {
		t.Fatal("unbounded form accepted")
	}
}

func TestInventoryBoundsAndPreservation(t *testing.T) {
	p, r, _ := fixture(t)
	origin, err := p.Listing(r)
	if err != nil {
		t.Fatal(err)
	}
	entries := []model.ArtifactInventoryEntry{{ArtifactKey: "doc", Role: "main", URL: "https://datafiles.chinhphu.vn/cpp/files/file.doc"}, {ArtifactKey: "other", Role: "appendix", URL: "https://datafiles.chinhphu.vn/cpp/files/file.bin", Ordinal: 1}}
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

func continuation(r api.DiscoverRequest, origin api.RequestPlan, parent, target string, hidden url.Values) api.ContinuationRequest {
	fields := make([]api.FormField, 0, len(hidden))
	for key, values := range hidden {
		for _, value := range values {
			fields = append(fields, api.FormField{Name: key, Value: value})
		}
	}
	capture := model.CaptureRef{Country: r.Country, SourceID: r.SourceID, CaptureKey: parent, Asset: model.AssetRef{Path: "capture/list", SHA256: r.PolicyHash}, MediaType: "text/html", RequestHash: r.PolicyHash, ResponseHash: r.PolicyHash, TransportReceipt: model.AssetRef{Path: "capture/receipt", SHA256: r.PolicyHash}}
	return api.ContinuationRequest{Origin: origin, WindowKey: r.WindowKey, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget, Mode: r.Mode, ParentCapture: capture, Target: target, Fields: fields}
}

func TestContinuationThroughFactoryPort(t *testing.T) {
	p, r, _ := fixture(t)
	factory := api.Factory{SourceID: SourceID, NewPlanner: func() api.Planner { return p }}
	planner := factory.NewPlanner()
	continuer, ok := planner.(api.Continuer)
	if !ok {
		t.Fatal("factory planner lacks continuation capability")
	}
	origin, err := planner.Listing(r)
	if err != nil {
		t.Fatal(err)
	}
	request := continuation(r, origin, "capture", "ctrl$grvDocument", url.Values{"__VIEWSTATE": {"state+&="}})
	next, err := continuer.NextPage(request)
	if err != nil || next.Method != "POST" || next.Cursor != "2" || next.ParentCapture != "capture" || next.PolicyHash != r.PolicyHash || next.ValidateOrigin(r.WindowKey, r.BudgetHash, r.Budget) != nil {
		t.Fatalf("API continuation lost origin: %+v %v", next, err)
	}
	form, err := url.ParseQuery(string(next.Body))
	if err != nil || form.Get("__VIEWSTATE") != "state+&=" || form.Get("__EVENTARGUMENT") != "Page$2" {
		t.Fatalf("API continuation lost form: %v %v", form, err)
	}
	request.Origin = next
	third, err := continuer.NextPage(request)
	if err != nil || third.Cursor != "3" || third.ValidateOrigin(r.WindowKey, r.BudgetHash, r.Budget) != nil {
		t.Fatalf("repeated continuation lost budget: %+v %v", third, err)
	}
	for name, mutate := range map[string]func(*api.ContinuationRequest){
		"host": func(r *api.ContinuationRequest) {
			r.Origin.URL = "https://evil.example/he-thong-van-ban?classid=1&mode=1"
		},
		"traversal": func(r *api.ContinuationRequest) {
			r.Origin.URL = "https://vanban.chinhphu.vn/a/../he-thong-van-ban?classid=1&mode=1"
		},
		"encoded traversal": func(r *api.ContinuationRequest) {
			r.Origin.URL = "https://vanban.chinhphu.vn/%252e%252e/he-thong-van-ban?classid=1&mode=1"
		},
		"window": func(r *api.ContinuationRequest) {
			r.WindowKey = "other"
		},
		"policy": func(r *api.ContinuationRequest) {
			r.PolicyHash = r.BudgetHash
		},
		"budget hash": func(r *api.ContinuationRequest) {
			r.BudgetHash = r.PolicyHash
		},
		"budget": func(r *api.ContinuationRequest) {
			r.Budget.MaxPages++
		},
		"capture source": func(r *api.ContinuationRequest) {
			r.ParentCapture.SourceID = "vbpl"
		},
		"target": func(r *api.ContinuationRequest) {
			r.Target = "../grvDocument"
		},
		"key": func(r *api.ContinuationRequest) {
			r.Fields = []api.FormField{{Name: "x&y", Value: "z"}}
		},
		"encoded body": func(r *api.ContinuationRequest) {
			r.Fields = []api.FormField{{Name: "__VIEWSTATE", Value: strings.Repeat("&", 4000)}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := request
			mutate(&bad)
			if _, err := continuer.NextPage(bad); err == nil {
				t.Fatal("unsafe continuation accepted through API")
			}
		})
	}
}

func TestListingCannotBeArtifact(t *testing.T) {
	p, r, _ := fixture(t)
	origin, err := p.Listing(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, locator := range []string{origin.URL, listingURL, "https://datafiles.chinhphu.vn/file.pdf", "https://datafiles.chinhphu.vn/cpp/files", "https://datafiles.chinhphu.vn/cpp/files/", "https://vanban.chinhphu.vn/cpp/files/file.pdf", "https://datafiles.chinhphu.vn/cpp/files/../file.pdf", "https://datafiles.chinhphu.vn/cpp/files/%2e%2e/file.pdf", "https://datafiles.chinhphu.vn/cpp/files/%252e%252e/file.pdf", "https://datafiles.chinhphu.vn/cpp/files/a%5cb"} {
		artifact := api.ArtifactRequest{Country: r.Country, SourceID: r.SourceID, ParentObservation: "observation", WindowKey: r.WindowKey, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget, Artifact: model.ArtifactInventoryEntry{ArtifactKey: "file", Role: "main", URL: locator}}
		if _, err := p.Artifact(artifact); err == nil {
			t.Fatalf("non-attachment accepted: %s", locator)
		}
		follow := model.FollowUpRequest{RequestKey: "follow", WindowKey: r.WindowKey, ParentCapture: "capture", Purpose: model.PurposeArtifact, URL: locator, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget}
		if _, err := p.FollowUp(origin, follow); err == nil {
			t.Fatalf("non-attachment follow-up accepted: %s", locator)
		}
	}
}
