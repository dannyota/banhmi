package vbpl

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/source/api"
)

func fixture(t *testing.T) (*Planner, api.DiscoverRequest, model.CaptureRef) {
	t.Helper()
	digest := model.Digest(strings.Repeat("a", 64))
	policy := api.SourcePolicy{Manifest: model.AssetRef{Path: "policy/vbpl.json", SHA256: digest, ByteSize: 1}, Digest: digest}
	p, err := New(policy, Config{StateBankAgencies: []string{"62", "908"}, CentralAgencies: []string{"1"}, Keywords: []string{"ngân hàng", "https://evil.example/../x"}, ArtifactOrigins: []string{"https://vbpl-bientap-gateway.moj.gov.vn"}})
	if err != nil {
		t.Fatal(err)
	}
	r := api.DiscoverRequest{Country: "vn", SourceID: SourceID, WindowKey: "window", SliceKey: "sweep", Mode: api.DiscoveryIncremental, Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Cutoff: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), PolicyHash: digest, BudgetHash: model.Digest(strings.Repeat("b", 64)), Budget: model.ResourceBudget{MaxItems: 200, MaxInputBytes: 10000, MaxPages: 3, MaxElapsed: time.Minute, MaxConcurrent: 1}}
	c := model.CaptureRef{Country: "vn", SourceID: SourceID, CaptureKey: "capture", Asset: model.AssetRef{Path: "capture/list", SHA256: digest}, MediaType: "application/json", RequestHash: digest, ResponseHash: digest, TransportReceipt: model.AssetRef{Path: "capture/receipt", SHA256: digest}}
	return p, r, c
}

func TestListingGoldenAndPagination(t *testing.T) {
	p, r, _ := fixture(t)
	plan, err := p.Listing(r)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"pageNumber":1,"pageSize":50,"sortBy":"issueDate","sortDirection":"desc","groupVbpl":false,"agencyLevel":"TRUNG_UONG","optionDoc":"title","matchMode":"all_words","agencyIds":["62","908"]}`
	if plan.URL != gateway+"/doc/all" || plan.Method != "POST" || string(plan.Body) != want {
		t.Fatalf("legacy Discover golden differs: %+v body=%s", plan, plan.Body)
	}
	next, err := p.NextPage(r, "capture")
	if err != nil || next.Cursor != "2" || next.ParentCapture != "capture" || next.RequestKey == plan.RequestKey {
		t.Fatalf("next page: %+v, %v", next, err)
	}
	if err := next.ValidateOrigin(r.WindowKey, r.BudgetHash, r.Budget); err != nil {
		t.Fatal(err)
	}
	r.Cursor = "3"
	if _, err := p.NextPage(r, "capture"); err == nil {
		t.Fatal("page cap accepted")
	}
	for _, cursor := range []string{"0", "-1", "4", "https://evil.example", "../2"} {
		r.Cursor = cursor
		if _, err := p.Listing(r); err == nil {
			t.Fatalf("accepted cursor %q", cursor)
		}
	}
	for _, slice := range []string{"keyword:0", "keyword:1"} {
		r.Cursor, r.SliceKey = "", slice
		plan, err := p.Listing(r)
		if err != nil || plan.URL != gateway+"/doc/all" {
			t.Fatalf("keyword escaped endpoint: %+v %v", plan, err)
		}
		var payload struct {
			AgencyIDs []string `json:"agencyIds"`
			Keyword   string   `json:"keyword"`
		}
		if err := json.Unmarshal(plan.Body, &payload); err != nil || !reflect.DeepEqual(payload.AgencyIDs, []string{"1"}) || payload.Keyword == "" {
			t.Fatalf("keyword scope: %s, %v", plan.Body, err)
		}
	}
}

func TestDetailArtifactsAndFrozenFollowUps(t *testing.T) {
	p, r, capture := fixture(t)
	detail := api.DetailRequest{Country: r.Country, SourceID: r.SourceID, ExternalID: "835e3190-54dd-4bfa-8b01-c3b9b7cd2e6a", ListingCaptures: []model.CaptureRef{capture}, PolicyHash: r.PolicyHash, WindowKey: r.WindowKey, BudgetHash: r.BudgetHash, Budget: r.Budget}
	plan, err := p.Detail(detail)
	if err != nil || plan.URL != gateway+"/doc/"+detail.ExternalID {
		t.Fatalf("detail: %+v %v", plan, err)
	}
	children, err := p.DetailFollowUps(plan, detail.ExternalID, "detail-capture")
	if err != nil || len(children) != 2 {
		t.Fatalf("follow-ups: %+v %v", children, err)
	}
	want := []string{gateway + "/doc/minio/buckets/vbpl/folders/" + detail.ExternalID + "/files?parts=1,2,3,4,5", gateway + "/doc/" + detail.ExternalID + "/diagram"}
	for i, child := range children {
		if child.URL != want[i] || child.ParentCapture != "detail-capture" || child.PolicyHash != r.PolicyHash {
			t.Fatalf("child: %+v", child)
		}
		if err := child.ValidateOrigin(r.WindowKey, r.BudgetHash, r.Budget); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"../x", "%2e%2e", "https://evil.example", "a/b", "a?b=c", ""} {
		detail.ExternalID = id
		if _, err := p.Detail(detail); err == nil {
			t.Fatalf("accepted ID %q", id)
		}
	}
	a := api.ArtifactRequest{Country: r.Country, SourceID: r.SourceID, ParentObservation: "observation", WindowKey: r.WindowKey, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget, Artifact: model.ArtifactInventoryEntry{ArtifactKey: "file", Role: "main", URL: gateway + "/download/file.doc"}}
	artifact, err := p.Artifact(a)
	if err != nil || artifact.URL != a.Artifact.URL || artifact.Purpose != model.PurposeArtifact {
		t.Fatalf("artifact: %+v %v", artifact, err)
	}
	for _, locator := range []string{"https://evil.example/file", "http://vbpl-bientap-gateway.moj.gov.vn/file", gateway + "/../file", gateway + "/%2e%2e/file", gateway + "/%252e%252e/file", "https://vbpl-bientap-gateway.moj.gov.vn:443/file", gateway + "/file#x", gateway + "/a%5cb"} {
		a.Artifact.URL = locator
		if _, err := p.Artifact(a); err == nil {
			t.Fatalf("accepted artifact %q", locator)
		}
	}
}

func TestConstructorCopiesPolicyInputs(t *testing.T) {
	p, r, _ := fixture(t)
	config := Config{StateBankAgencies: []string{"62"}, ArtifactOrigins: []string{"https://vbpl-bientap-gateway.moj.gov.vn"}}
	copyPlanner, err := New(p.policy, config)
	if err != nil {
		t.Fatal(err)
	}
	config.StateBankAgencies[0] = "changed"
	config.ArtifactOrigins[0] = "https://evil.example"
	plan, err := copyPlanner.Listing(r)
	if err != nil || strings.Contains(string(plan.Body), "changed") {
		t.Fatalf("constructor retained mutable input: %+v %v", plan, err)
	}
	r.PolicyHash = r.BudgetHash
	if _, err := p.Listing(r); err == nil {
		t.Fatal("policy drift accepted")
	}
	if _, err := New(p.policy, Config{ArtifactOrigins: []string{"https://evil.example/path"}}); err == nil {
		t.Fatal("path accepted as approved origin")
	}
}

func TestInventoryBoundsAndPreservation(t *testing.T) {
	p, r, _ := fixture(t)
	origin, err := p.Listing(r)
	if err != nil {
		t.Fatal(err)
	}
	entries := []model.ArtifactInventoryEntry{{ArtifactKey: "doc", Role: "main", URL: gateway + "/download/file.doc"}, {ArtifactKey: "other", Role: "appendix", URL: gateway + "/download/file.bin", Ordinal: 1}}
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
	r.Budget.MaxItems = 10
	plan, err := p.Listing(r)
	if err != nil || !strings.Contains(string(plan.Body), `"pageSize":10`) {
		t.Fatalf("page size not bounded: %+v %v", plan, err)
	}
	r.SourceID = "other"
	if _, err := p.Listing(r); err == nil {
		t.Fatal("cross-source request accepted")
	}
}

func TestDetailPlansPinResponseKind(t *testing.T) {
	p, r, capture := fixture(t)
	request := api.DetailRequest{Country: r.Country, SourceID: r.SourceID, ExternalID: "123", ListingCaptures: []model.CaptureRef{capture}, PolicyHash: r.PolicyHash, WindowKey: r.WindowKey, BudgetHash: r.BudgetHash, Budget: r.Budget}
	paths := map[string]string{"metadata": "/doc/123", "files": "/doc/minio/buckets/vbpl/folders/123/files?parts=1,2,3,4,5", "tree": "/doc/provision/tree/123", "diagram": "/doc/123/diagram"}
	keys := map[string]bool{}
	for kind, path := range paths {
		request.ExpectedResponseKind = kind
		plan, err := p.Detail(request)
		if err != nil || plan.URL != gateway+path || plan.ExpectedResponseKind != kind { t.Fatalf("%s plan: %+v %v", kind, plan, err) }
		if keys[plan.RequestKey] { t.Fatal("different response plans share request identity") }
		keys[plan.RequestKey] = true
	}
	request.ExpectedResponseKind = ""
	origin, err := p.Detail(request)
	if err != nil || origin.ExpectedResponseKind != "metadata" { t.Fatal("primary response kind missing") }
	children, err := p.DetailFollowUps(origin, "123", "detail-capture")
	if err != nil || len(children) != 2 || children[0].ExpectedResponseKind != "files" || children[1].ExpectedResponseKind != "diagram" { t.Fatalf("child kind missing: %+v %v", children, err) }
}

func TestRequestIdentityIncludesExpectedResponseKind(t *testing.T) {
	p, r, capture := fixture(t)
	request := api.DetailRequest{Country: r.Country, SourceID: r.SourceID, ExternalID: "123", ListingCaptures: []model.CaptureRef{capture}, PolicyHash: r.PolicyHash, WindowKey: r.WindowKey, BudgetHash: r.BudgetHash, Budget: r.Budget}
	plan, err := p.Detail(request)
	if err != nil { t.Fatal(err) }
	// finish binds the trusted local manifest, including parser dispatch. A
	// changed dispatch value cannot retain the original request identity.
	changed := plan
	changed.ExpectedResponseKind = "files"
	changed, err = p.finish(changed)
	if err != nil { t.Fatal(err) }
	if plan.RequestKey == changed.RequestKey { t.Fatal("response kind omitted from request identity") }
}
