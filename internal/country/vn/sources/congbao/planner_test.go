package congbao

import (
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/source/api"
)

func fixture(t *testing.T) (*Planner, api.DiscoverRequest, model.CaptureRef) {
	t.Helper()
	digest := model.Digest(strings.Repeat("a", 64))
	p, err := New(api.SourcePolicy{Manifest: model.AssetRef{Path: "policy/congbao.json", SHA256: digest, ByteSize: 1}, Digest: digest})
	if err != nil {
		t.Fatal(err)
	}
	r := api.DiscoverRequest{Country: "vn", SourceID: SourceID, WindowKey: "window", SliceKey: "rss", Mode: api.DiscoveryIncremental, Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Cutoff: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), PolicyHash: digest, BudgetHash: model.Digest(strings.Repeat("b", 64)), Budget: model.ResourceBudget{MaxItems: 200, MaxInputBytes: 10000, MaxPages: 3, MaxElapsed: time.Minute, MaxConcurrent: 1}}
	c := model.CaptureRef{Country: "vn", SourceID: SourceID, CaptureKey: "capture", Asset: model.AssetRef{Path: "capture/list", SHA256: digest}, MediaType: "application/xml", RequestHash: digest, ResponseHash: digest, TransportReceipt: model.AssetRef{Path: "capture/receipt", SHA256: digest}}
	return p, r, c
}

func TestLegacyRequestGoldens(t *testing.T) {
	p, r, capture := fixture(t)
	listing, err := p.Listing(r)
	if err != nil || listing.URL != baseURL+"/cac-van-ban-moi-ban-hanh.rss" || listing.Method != "GET" {
		t.Fatalf("RSS: %+v %v", listing, err)
	}
	r.Cursor = "2"
	if _, err := p.Listing(r); err == nil {
		t.Fatal("invented RSS pagination")
	}
	d := api.DetailRequest{Country: r.Country, SourceID: r.SourceID, ExternalID: "144532", ListingCaptures: []model.CaptureRef{capture}, PolicyHash: r.PolicyHash, WindowKey: r.WindowKey, BudgetHash: r.BudgetHash, Budget: r.Budget}
	locator := baseURL + "/van-ban/thong-tu-so-09-2020-tt-nhnn-144532.htm"
	detail, err := p.DetailAt(d, locator)
	if err != nil || detail.URL != locator || detail.ParentCapture != capture.CaptureKey {
		t.Fatalf("RSS detail: %+v %v", detail, err)
	}
	fallback, err := p.Detail(d)
	if err != nil || fallback.URL != baseURL+"/van-ban/van-ban-144532.htm" {
		t.Fatalf("search fallback: %+v %v", fallback, err)
	}
	for _, locator := range []string{baseURL + "/van-ban/a-1.htm", "https://evil.example/van-ban/a-144532.htm", baseURL + "/van-ban/../a-144532.htm", baseURL + "/van-ban/%252e%252e/a-144532.htm"} {
		if _, err := p.DetailAt(d, locator); err == nil {
			t.Fatalf("unsafe or mismatched detail accepted: %q", locator)
		}
	}
}

func TestNumberQueryPreservesDecodedVietnamese(t *testing.T) {
	p, r, _ := fixture(t)
	span := model.Span{ArtifactHash: r.PolicyHash, BlockID: "number", Space: model.RawText, Range: &model.ByteRange{Start: 0, End: 18}}
	request := api.NumberRequest{Country: r.Country, SourceID: r.SourceID, WindowKey: r.WindowKey, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget, Number: api.NumberInput{Text: "50/2024/TT-NHNN", Sources: []model.Span{span}, Context: api.IdentityContext{Title: "Thông tư", Fields: []model.Span{span}, Limits: model.RuleLimits{MaxItems: 20, MaxBytes: 10000}}}}
	plan, err := p.PlanNumber(request)
	if err != nil || plan.URL != searchURL || plan.Method != "POST" || string(plan.Body) != `{"filters":{},"page":1,"page_size":50,"query":"50/2024/TT-NHNN Thông tư"}` {
		t.Fatalf("legacy number search: %+v body=%s %v", plan, plan.Body, err)
	}
	if err := plan.ValidateOrigin(r.WindowKey, r.BudgetHash, r.Budget); err != nil {
		t.Fatal(err)
	}
	request.Number.Text = "https://evil.example/../x"
	plan, err = p.PlanNumber(request)
	if err != nil || plan.URL != searchURL {
		t.Fatalf("query widened origin: %+v %v", plan, err)
	}
}

func TestArtifactAndFollowUpOrigins(t *testing.T) {
	p, r, _ := fixture(t)
	origin, err := p.Listing(r)
	if err != nil {
		t.Fatal(err)
	}
	a := api.ArtifactRequest{Country: r.Country, SourceID: r.SourceID, ParentObservation: "observation", WindowKey: r.WindowKey, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget, Artifact: model.ArtifactInventoryEntry{ArtifactKey: "file", Role: "main"}}
	for _, locator := range []string{"https://g7.cdnchinhphu.vn/api/download/stream?file=doc.pdf&token=synthetic", "https://congbaocdn.chinhphu.vn/CongBaoCP/VanBan/2016/12/22071/16437-1-422016tt-btttt16587pdf"} {
		a.Artifact.URL = locator
		plan, err := p.Artifact(a)
		if err != nil || plan.URL != locator {
			t.Fatalf("artifact: %+v %v", plan, err)
		}
		follow := model.FollowUpRequest{RequestKey: "follow", WindowKey: r.WindowKey, ParentCapture: "capture", Purpose: model.PurposeArtifact, URL: locator, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget}
		child, err := p.FollowUp(origin, follow)
		if err != nil || child.ParentCapture != "capture" || child.ValidateOrigin(r.WindowKey, r.BudgetHash, r.Budget) != nil {
			t.Fatalf("follow-up: %+v %v", child, err)
		}
		follow.Budget.MaxPages++
		if _, err := p.FollowUp(origin, follow); err == nil {
			t.Fatal("budget reset accepted")
		}
		follow.Budget, follow.WindowKey = r.Budget, "other"
		if _, err := p.FollowUp(origin, follow); err == nil {
			t.Fatal("window change accepted")
		}
	}
	for _, locator := range []string{"https://evil.example/file", "http://g7.cdnchinhphu.vn/api/download/stream", "https://g7.cdnchinhphu.vn.evil.example/api/download/stream", "https://g7.cdnchinhphu.vn:443/api/download/stream", "https://congbaocdn.chinhphu.vn/CongBaoCP/VanBan/../file", "https://congbaocdn.chinhphu.vn/CongBaoCP/VanBan/%2e%2e/file", "https://congbaocdn.chinhphu.vn/CongBaoCP/VanBan/a%5cb"} {
		a.Artifact.URL = locator
		if _, err := p.Artifact(a); err == nil {
			t.Fatalf("unsafe artifact accepted: %q", locator)
		}
	}
}

func TestInventoryBoundsAndPreservation(t *testing.T) {
	p, r, _ := fixture(t)
	origin, err := p.Listing(r)
	if err != nil {
		t.Fatal(err)
	}
	entries := []model.ArtifactInventoryEntry{{ArtifactKey: "doc", Role: "main", URL: "https://congbaocdn.chinhphu.vn/CongBaoCP/VanBan/filedoc"}, {ArtifactKey: "other", Role: "appendix", URL: "https://g7.cdnchinhphu.vn/api/download/stream?file=other.bin", Ordinal: 1}}
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
	r.PolicyHash = r.BudgetHash
	if _, err := p.Listing(r); err == nil {
		t.Fatal("changed policy accepted")
	}
}

func TestArtifactDownloadHeaders(t *testing.T) {
	p, r, capture := fixture(t)
	listing, err := p.Listing(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Headers) != 0 {
		t.Fatalf("download headers added to listing: %v", listing.Headers)
	}
	detail, err := p.Detail(api.DetailRequest{Country: r.Country, SourceID: r.SourceID, ExternalID: "1", ListingCaptures: []model.CaptureRef{capture}, WindowKey: r.WindowKey, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget})
	if err != nil || len(detail.Headers) != 1 || detail.Headers[0] != (api.RequestHeader{Name: "Referer", Value: "https://congbao.chinhphu.vn/"}) {
		t.Fatalf("detail headers changed: %v %v", detail.Headers, err)
	}
	for _, locator := range []string{"https://g7.cdnchinhphu.vn/api/download/stream?file=example", "https://congbaocdn.chinhphu.vn/CongBaoCP/VanBan/file.doc"} {
		artifact, err := p.Artifact(api.ArtifactRequest{Country: r.Country, SourceID: r.SourceID, ParentObservation: "observation", WindowKey: r.WindowKey, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget, Artifact: model.ArtifactInventoryEntry{ArtifactKey: "file", Role: "main", URL: locator}})
		if err != nil {
			t.Fatal(err)
		}
		follow, err := p.FollowUp(listing, model.FollowUpRequest{RequestKey: "follow", WindowKey: r.WindowKey, ParentCapture: "capture", Purpose: model.PurposeArtifact, URL: locator, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget})
		if err != nil {
			t.Fatal(err)
		}
		for _, plan := range []api.RequestPlan{artifact, follow} {
			want := []api.RequestHeader{{Name: "User-Agent", Value: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"}, {Name: "Referer", Value: "https://congbao.chinhphu.vn/"}, {Name: "Accept", Value: "*/*"}}
			if plan.Method != "GET" || len(plan.Headers) != len(want) {
				t.Fatalf("download headers: %v", plan.Headers)
			}
			for i, header := range want {
				if plan.Headers[i] != header {
					t.Fatalf("download header %d: %v, want %v", i, plan.Headers[i], header)
				}
			}
			if plan.ValidateOrigin(r.WindowKey, r.BudgetHash, r.Budget) != nil {
				t.Fatal("download headers reset budget")
			}
		}
	}
}
