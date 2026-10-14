// Package congbao plans Gazette requests from decoded local values only.
package congbao

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/source/api"
	"danny.vn/banhmi/internal/country/vn"
)

// SourceID identifies the declared Vietnam source for request planning.
const SourceID = "congbao"
const baseURL = "https://congbao.chinhphu.vn"
const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
const searchURL = "https://api-searchcongbao.chinhphu.vn/search/van-ban/nhom/vbqpp"

// Planner is a pure value pinned to an immutable policy reference.
type Planner struct {
	policy api.SourcePolicy
}

var _ api.Planner = (*Planner)(nil)
var _ api.NumberSearcher = (*Planner)(nil)

// New validates metadata only; it never reads the policy manifest.
func New(policy api.SourcePolicy) (*Planner, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if vn.Declare().Code != "vn" {
		return nil, fmt.Errorf("Vietnam descriptor required")
	}
	return &Planner{policy: policy}, nil
}

// Listing mirrors Discover's single recent RSS feed. RSS has no paginator and
// cannot establish historical completeness, even for a full-sweep window.
func (p *Planner) Listing(r api.DiscoverRequest) (api.RequestPlan, error) {
	if err := r.Validate(); err != nil {
		return api.RequestPlan{}, err
	}
	if r.Cursor != "" && r.Cursor != "1" {
		return api.RequestPlan{}, fmt.Errorf("RSS has no next page")
	}
	return p.finish(api.RequestPlan{Country: r.Country, SourceID: r.SourceID, WindowKey: r.WindowKey, Purpose: model.PurposeListing, URL: baseURL + "/cac-van-ban-moi-ban-hanh.rss", Method: "GET", Cursor: "1", PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget})
}

// Detail mirrors detailURLFromSearch's fallback when type/number are unavailable.
// Prefer DetailAt for an exact decoded RSS locator; IDs never carry URLs.
func (p *Planner) Detail(r api.DetailRequest) (api.RequestPlan, error) {
	return p.DetailAt(r, baseURL+"/van-ban/van-ban-"+r.ExternalID+".htm")
}

// DetailAt mirrors FetchDetail using an already decoded RSS/search locator.
func (p *Planner) DetailAt(r api.DetailRequest, locator string) (api.RequestPlan, error) {
	if err := r.Validate(); err != nil {
		return api.RequestPlan{}, err
	}
	if r.ExternalID == "" || strings.Trim(r.ExternalID, "0123456789") != "" {
		return api.RequestPlan{}, fmt.Errorf("numeric document ID required")
	}
	if int64(len(locator)) > r.Budget.MaxInputBytes {
		return api.RequestPlan{}, fmt.Errorf("detail locator exceeds byte bound")
	}
	if strings.HasPrefix(locator, "/van-ban/") {
		locator = baseURL + locator
	}
	u, err := url.Parse(locator)
	if err != nil || !strings.HasSuffix(u.Path, "-"+r.ExternalID+".htm") {
		return api.RequestPlan{}, fmt.Errorf("detail locator differs from native ID")
	}
	return p.finish(api.RequestPlan{Country: r.Country, SourceID: r.SourceID, WindowKey: r.WindowKey, Purpose: model.PurposeDetail, URL: locator, Method: "GET", ParentCapture: r.ListingCaptures[0].CaptureKey, Headers: []api.RequestHeader{{Name: "Referer", Value: baseURL + "/"}}, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget})
}

// Artifact plans one inventory entry without filtering legacy DOC or other files.
func (p *Planner) Artifact(r api.ArtifactRequest) (api.RequestPlan, error) {
	if r.Artifact.Ordinal >= r.Budget.MaxItems || int64(len(r.Artifact.URL)) > r.Budget.MaxInputBytes || r.Artifact.Capture != nil && (r.Artifact.Capture.Country != r.Country || r.Artifact.Capture.SourceID != r.SourceID) {
		return api.RequestPlan{}, fmt.Errorf("artifact exceeds bounds or crosses source")
	}
	if err := r.Validate(); err != nil {
		return api.RequestPlan{}, err
	}
	return p.finish(api.RequestPlan{Country: r.Country, SourceID: r.SourceID, WindowKey: r.WindowKey, Purpose: model.PurposeArtifact, URL: r.Artifact.URL, Method: "GET", PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget})
}

// Artifacts enumerates decoded download links in their supplied inventory order.
// Parsing, extension inference and source file-order decoding belong to M1.9.
func (p *Planner) Artifacts(origin api.RequestPlan, entries []model.ArtifactInventoryEntry, observation, parentCapture string) ([]api.RequestPlan, error) {
	if _, err := p.finish(origin); err != nil {
		return nil, err
	}
	if len(entries) > origin.Budget.MaxItems || parentCapture == "" {
		return nil, fmt.Errorf("artifact inventory exceeds item bound or lacks capture")
	}
	remaining := origin.Budget.MaxInputBytes
	for _, entry := range entries {
		remaining -= int64(len(entry.URL))
		if remaining < 0 {
			return nil, fmt.Errorf("artifact inventory exceeds byte bound")
		}
	}
	plans := make([]api.RequestPlan, 0, len(entries))
	for _, entry := range entries {
		plan, err := p.Artifact(api.ArtifactRequest{Country: origin.Country, SourceID: origin.SourceID, Artifact: entry, ParentObservation: observation, PolicyHash: origin.PolicyHash, WindowKey: origin.WindowKey, BudgetHash: origin.BudgetHash, Budget: origin.Budget})
		if err != nil {
			return nil, err
		}
		plan.ParentCapture = parentCapture
		plan, err = p.finish(plan)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

// PlanNumber mirrors SearchByNumber's fuzzy query with optional title hint.
// It preserves the decoded printed number; the decoder must recheck exact
// equality, type/title compatibility and all candidates rather than choose one.
func (p *Planner) PlanNumber(r api.NumberRequest) (api.RequestPlan, error) {
	if err := r.Validate(); err != nil {
		return api.RequestPlan{}, err
	}
	query := strings.TrimSpace(r.Number.Text)
	if query == "" {
		return api.RequestPlan{}, fmt.Errorf("number required")
	}
	if hint := strings.TrimSpace(r.Number.Context.Title); hint != "" {
		query += " " + hint
	}
	if int64(len(query)) > r.Budget.MaxInputBytes {
		return api.RequestPlan{}, fmt.Errorf("number query exceeds byte bound")
	}
	body, err := json.Marshal(struct {
		Filters  map[string]string `json:"filters"`
		Page     int               `json:"page"`
		PageSize int               `json:"page_size"`
		Query    string            `json:"query"`
	}{map[string]string{}, 1, min(50, r.Budget.MaxItems), query})
	if err != nil {
		return api.RequestPlan{}, err
	}
	return p.finish(api.RequestPlan{Country: r.Country, SourceID: r.SourceID, WindowKey: r.WindowKey, Purpose: model.PurposeNumber, URL: searchURL, Method: "POST", Body: body, Headers: []api.RequestHeader{{Name: "Content-Type", Value: "application/json"}, {Name: "Accept", Value: "application/json"}, {Name: "Origin", Value: baseURL}, {Name: "Referer", Value: baseURL + "/"}}, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget})
}

// FollowUp accepts only decoded GET detail/artifact locators under the original
// window. It never reuses a search POST body for a download or widens its hosts.
func (p *Planner) FollowUp(origin api.RequestPlan, r model.FollowUpRequest) (api.RequestPlan, error) {
	if _, err := p.finish(origin); err != nil {
		return api.RequestPlan{}, err
	}
	if err := r.ValidateOrigin(origin.WindowKey, origin.BudgetHash, origin.Budget); err != nil {
		return api.RequestPlan{}, err
	}
	if r.PolicyHash != origin.PolicyHash || (r.Purpose != model.PurposeDetail && r.Purpose != model.PurposeArtifact) || r.Ordinal >= origin.Budget.MaxItems {
		return api.RequestPlan{}, fmt.Errorf("invalid follow-up purpose, policy or ordinal")
	}
	return p.finish(api.RequestPlan{Country: origin.Country, SourceID: origin.SourceID, WindowKey: r.WindowKey, Purpose: r.Purpose, URL: r.URL, Method: "GET", ParentCapture: r.ParentCapture, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget})
}

func (p *Planner) finish(plan api.RequestPlan) (api.RequestPlan, error) {
	if plan.Country != "vn" || plan.SourceID != SourceID || plan.PolicyHash != p.policy.Digest || p.policy.Validate() != nil {
		return api.RequestPlan{}, fmt.Errorf("request differs from frozen source policy")
	}
	if int64(len(plan.URL)) > plan.Budget.MaxInputBytes {
		return api.RequestPlan{}, fmt.Errorf("URL exceeds byte bound")
	}
	if err := model.ValidatePublicURL(plan.URL); err != nil {
		return api.RequestPlan{}, err
	}
	u, _ := url.Parse(plan.URL)
	if u.Scheme != "https" || strings.ContainsAny(u.Path, "\\") {
		return api.RequestPlan{}, fmt.Errorf("unapproved scheme or path")
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." || strings.Contains(segment, "%") {
			return api.RequestPlan{}, fmt.Errorf("unsafe URL path")
		}
	}
	approved := false
	switch plan.Purpose {
	case model.PurposeListing:
		approved = plan.URL == baseURL+"/cac-van-ban-moi-ban-hanh.rss"
	case model.PurposeNumber:
		approved = plan.URL == searchURL
	case model.PurposeDetail:
		approved = u.Host == "congbao.chinhphu.vn" && strings.HasPrefix(u.Path, "/van-ban/") && strings.HasSuffix(u.Path, ".htm")
	case model.PurposeArtifact:
		approved = u.Host == "g7.cdnchinhphu.vn" && u.Path == "/api/download/stream" || u.Host == "congbaocdn.chinhphu.vn" && strings.HasPrefix(u.Path, "/CongBaoCP/VanBan/")
	}
	if !approved {
		return api.RequestPlan{}, fmt.Errorf("URL outside approved source origins and paths")
	}
	if plan.Purpose == model.PurposeArtifact && plan.Method == "GET" {
		plan.Headers = []api.RequestHeader{{Name: "User-Agent", Value: browserUA}, {Name: "Referer", Value: baseURL + "/"}, {Name: "Accept", Value: "*/*"}}
	}
	plan.RequestKey = "pending"
	if err := plan.Validate(); err != nil {
		return api.RequestPlan{}, err
	}
	plan.RequestKey = fmt.Sprintf("%x", sha256.Sum256([]byte(plan.WindowKey+"\x00"+string(plan.Purpose)+"\x00"+plan.URL+"\x00"+string(plan.Body)+"\x00"+string(plan.PolicyHash)+"\x00"+string(plan.BudgetHash)+"\x00"+plan.ParentCapture)))
	return plan, nil
}
