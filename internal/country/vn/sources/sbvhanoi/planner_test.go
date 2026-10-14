package sbvhanoi

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
	p, err := New(api.SourcePolicy{Manifest: model.AssetRef{Path: "policy/sbv-hanoi.json", SHA256: digest, ByteSize: 1}, Digest: digest})
	if err != nil {
		t.Fatal(err)
	}
	r := api.DiscoverRequest{Country: "vn", SourceID: SourceID, WindowKey: "window", SliceKey: "legal", Mode: api.DiscoveryIncremental, Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Cutoff: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), PolicyHash: digest, BudgetHash: model.Digest(strings.Repeat("b", 64)), Budget: model.ResourceBudget{MaxItems: 200, MaxInputBytes: 10000, MaxPages: 3, MaxElapsed: time.Minute, MaxConcurrent: 1}}
	c := model.CaptureRef{Country: "vn", SourceID: SourceID, CaptureKey: "capture", Asset: model.AssetRef{Path: "capture/list", SHA256: digest}, MediaType: "text/html", RequestHash: digest, ResponseHash: digest, TransportReceipt: model.AssetRef{Path: "capture/receipt", SHA256: digest}}
	return p, r, c
}

func TestListingGoldenAndPagination(t *testing.T) {
	p, r, _ := fixture(t)
	plan, err := p.Listing(r)
	want := "https://sbv.hanoi.gov.vn/van-ban-quy-pham-phap-luat?_4_WAR_portalvbpqportlet_advancedSearch=false&_4_WAR_portalvbpqportlet_andOperator=true&_4_WAR_portalvbpqportlet_cur=1&_4_WAR_portalvbpqportlet_delta=200&_4_WAR_portalvbpqportlet_mvcPath=%2Fhtml%2Fportlet%2Flist%2Fview.jsp&_4_WAR_portalvbpqportlet_resetCur=false&p_p_col_count=2&p_p_col_id=column-2&p_p_col_pos=1&p_p_id=4_WAR_portalvbpqportlet&p_p_lifecycle=0&p_p_mode=view&p_p_state=normal"
	if err != nil || plan.URL != want || plan.Method != "GET" {
		t.Fatalf("legacy listURL: %+v %v", plan, err)
	}
	next, err := p.NextPage(r, "capture")
	if err != nil || next.URL != strings.Replace(want, "_cur=1", "_cur=2", 1) || next.ParentCapture != "capture" || next.ValidateOrigin(r.WindowKey, r.BudgetHash, r.Budget) != nil {
		t.Fatalf("pagination: %+v %v", next, err)
	}
	for _, cursor := range []string{"0", "4", "../2", "https://evil.example", "-1"} {
		r.Cursor = cursor
		if _, err := p.Listing(r); err == nil {
			t.Fatalf("unsafe cursor: %q", cursor)
		}
	}
	r.Cursor = "3"
	if _, err := p.NextPage(r, "capture"); err == nil {
		t.Fatal("page cap accepted")
	}
}

func TestDetailArtifactAndFollowUp(t *testing.T) {
	p, r, capture := fixture(t)
	d := api.DetailRequest{Country: r.Country, SourceID: r.SourceID, ExternalID: "77102", ListingCaptures: []model.CaptureRef{capture}, PolicyHash: r.PolicyHash, WindowKey: r.WindowKey, BudgetHash: r.BudgetHash, Budget: r.Budget}
	origin, err := p.Detail(d)
	want := "https://sbv.hanoi.gov.vn/van-ban-quy-pham-phap-luat?_4_WAR_portalvbpqportlet_id=77102&_4_WAR_portalvbpqportlet_mvcPath=%2Fhtml%2Fportlet%2Flist%2Fview_detail.jsp&p_p_col_count=2&p_p_col_id=column-2&p_p_col_pos=1&p_p_id=4_WAR_portalvbpqportlet&p_p_lifecycle=0&p_p_mode=view&p_p_state=normal"
	if err != nil || origin.URL != want {
		t.Fatalf("canonical detail: %+v %v", origin, err)
	}
	for _, id := range []string{"../1", "https://evil.example", "1&x=y", "%2e%2e", ""} {
		d.ExternalID = id
		if _, err := p.Detail(d); err == nil {
			t.Fatalf("unsafe ID: %q", id)
		}
	}
	locator := "https://sbv.hanoi.gov.vn/documents/20182/0/van-ban.doc"
	a := api.ArtifactRequest{Country: r.Country, SourceID: r.SourceID, ParentObservation: "observation", WindowKey: r.WindowKey, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget, Artifact: model.ArtifactInventoryEntry{ArtifactKey: "file", Role: "main", URL: locator}}
	plan, err := p.Artifact(a)
	if err != nil || plan.URL != locator {
		t.Fatalf("legacy DOC retained: %+v %v", plan, err)
	}
	follow := model.FollowUpRequest{RequestKey: "follow", WindowKey: r.WindowKey, ParentCapture: "capture", Purpose: model.PurposeArtifact, URL: locator, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget}
	child, err := p.FollowUp(origin, follow)
	if err != nil || child.ValidateOrigin(r.WindowKey, r.BudgetHash, r.Budget) != nil {
		t.Fatalf("follow-up: %+v %v", child, err)
	}
	follow.Budget.MaxPages++
	if _, err := p.FollowUp(origin, follow); err == nil {
		t.Fatal("budget reset accepted")
	}
	for _, locator := range []string{"https://evil.example/file", "https://sbv.gov.vn/file", "http://sbv.hanoi.gov.vn/file", "https://sbv.hanoi.gov.vn:443/file", "https://sbv.hanoi.gov.vn/../file", "https://sbv.hanoi.gov.vn/%2e%2e/file", "https://sbv.hanoi.gov.vn/%252e%252e/file", "https://sbv.hanoi.gov.vn/a%5cb"} {
		a.Artifact.URL = locator
		if _, err := p.Artifact(a); err == nil {
			t.Fatalf("unsafe artifact: %q", locator)
		}
	}
}

func TestNumberSearchMirrorsLegacyUnfilteredScan(t *testing.T) {
	p, r, _ := fixture(t)
	span := model.Span{ArtifactHash: r.PolicyHash, BlockID: "number", Space: model.RawText, Range: &model.ByteRange{Start: 0, End: 12}}
	n := api.NumberRequest{Country: r.Country, SourceID: r.SourceID, WindowKey: r.WindowKey, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget, Number: api.NumberInput{Text: "2345/QĐ-NHNN", Sources: []model.Span{span}, Context: api.IdentityContext{Fields: []model.Span{span}, Limits: model.RuleLimits{MaxItems: 20, MaxBytes: 10000}}}}
	plan, err := p.PlanNumber(n)
	listing, listingErr := p.Listing(r)
	if err != nil || listingErr != nil || plan.URL != listing.URL || plan.Purpose != model.PurposeNumber {
		t.Fatalf("number scan differs from legacy: %+v %v", plan, err)
	}
	next, err := p.NextNumberPage(plan, "capture")
	if err != nil || next.Cursor != "2" || next.ValidateOrigin(r.WindowKey, r.BudgetHash, r.Budget) != nil {
		t.Fatalf("number continuation: %+v %v", next, err)
	}
	u, _ := url.Parse(next.URL)
	if len(u.Query()) != 13 || u.Query().Get("_4_WAR_portalvbpqportlet_cur") != "2" {
		t.Fatalf("invented number filter: %s", next.URL)
	}
}

func TestInventoryBoundsAndPreservation(t *testing.T) {
	p, r, _ := fixture(t)
	origin, err := p.Listing(r)
	if err != nil {
		t.Fatal(err)
	}
	entries := []model.ArtifactInventoryEntry{{ArtifactKey: "doc", Role: "main", URL: "https://sbv.hanoi.gov.vn/documents/file.doc"}, {ArtifactKey: "other", Role: "appendix", URL: "https://sbv.hanoi.gov.vn/documents/file.bin", Ordinal: 1}}
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
	r.Budget.MaxItems = 20
	plan, err := p.Listing(r)
	if err != nil || !strings.Contains(plan.URL, "_delta=20&") {
		t.Fatalf("page size not bounded: %+v %v", plan, err)
	}
	r.PolicyHash = r.BudgetHash
	if _, err := p.Listing(r); err == nil {
		t.Fatal("changed policy accepted")
	}
}

func TestListingCannotBeArtifact(t *testing.T) {
	p, r, _ := fixture(t)
	origin, err := p.Listing(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, locator := range []string{origin.URL, baseURL + listPath, "https://sbv.hanoi.gov.vn/documents", "https://sbv.hanoi.gov.vn/documents/", "https://sbv.hanoi.gov.vn/documents-other/file.pdf", "https://sbv.hanoi.gov.vn/documents/../file.pdf", "https://sbv.hanoi.gov.vn/documents/%2e%2e/file.pdf", "https://sbv.hanoi.gov.vn/documents/%252e%252e/file.pdf", "https://sbv.hanoi.gov.vn/documents/a%5cb"} {
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
