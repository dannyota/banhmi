// Package vanban plans WebForms requests without fetching or decoding HTML.
package vanban

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/source/api"
	"danny.vn/banhmi/internal/country/vn"
)

const SourceID = "vanban"
const baseURL = "https://vanban.chinhphu.vn"
const listingURL = baseURL + "/he-thong-van-ban?classid=1&mode=1"

// Planner retains an immutable source policy reference only.
type Planner struct {
	policy api.SourcePolicy
}

var _ api.Planner = (*Planner)(nil)

// New is metadata-only. Core resolves policy, sessions and pacing separately.
func New(policy api.SourcePolicy) (*Planner, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if vn.Declare().Code != "vn" {
		return nil, fmt.Errorf("Vietnam descriptor required")
	}
	return &Planner{policy: policy}, nil
}

// Listing mirrors Discover's initial GET. Continuations need decoded hidden
// values and use NextPage; a page token alone cannot recreate WebForms state.
func (p *Planner) Listing(r api.DiscoverRequest) (api.RequestPlan, error) {
	if err := r.Validate(); err != nil {
		return api.RequestPlan{}, err
	}
	if r.Cursor != "" && r.Cursor != "1" {
		return api.RequestPlan{}, fmt.Errorf("continuation requires decoded form state")
	}
	// The site hard-caps at 50 and has no qualified smaller initial GET.
	if r.Budget.MaxItems < 50 {
		return api.RequestPlan{}, fmt.Errorf("listing requires a 50-item allowance")
	}
	return p.finish(api.RequestPlan{Country: r.Country, SourceID: r.SourceID, WindowKey: r.WindowKey, Purpose: model.PurposeListing, URL: listingURL, Method: "GET", Cursor: "1", Headers: []api.RequestHeader{{Name: "Referer", Value: listingURL}}, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget})
}

// NextPage mirrors pageBack and defaultDropdowns. Core supplies verified decoded
// hidden fields and the GridView target, and carries sessions in its transport.
// Reaching either legacy cap is an incomplete-window outcome for the consumer.
func (p *Planner) NextPage(r api.DiscoverRequest, origin api.RequestPlan, parentCapture, target string, hidden url.Values) (api.RequestPlan, error) {
	if err := r.Validate(); err != nil {
		return api.RequestPlan{}, err
	}
	if _, err := p.finish(origin); err != nil {
		return api.RequestPlan{}, err
	}
	if err := origin.ValidateOrigin(r.WindowKey, r.BudgetHash, r.Budget); err != nil {
		return api.RequestPlan{}, err
	}
	if r.Country != origin.Country || r.SourceID != origin.SourceID || r.PolicyHash != origin.PolicyHash || origin.Purpose != model.PurposeListing || parentCapture == "" || !strings.HasSuffix(target, "grvDocument") {
		return api.RequestPlan{}, fmt.Errorf("invalid pagination origin or target")
	}
	for _, c := range target {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$') {
			return api.RequestPlan{}, fmt.Errorf("invalid postback target")
		}
	}
	page, err := strconv.Atoi(origin.Cursor)
	cap := 240
	if r.Mode == api.DiscoveryIncremental {
		cap = 40
	}
	if err != nil || page < 1 || page >= min(cap, r.Budget.MaxPages) || r.Budget.MaxItems < 50 {
		return api.RequestPlan{}, fmt.Errorf("page exceeds frozen or legacy cap")
	}
	if len(hidden) > r.Budget.MaxItems {
		return api.RequestPlan{}, fmt.Errorf("form fields exceed item bound")
	}
	remaining := r.Budget.MaxInputBytes - int64(len(target))
	for key, values := range hidden {
		remaining -= int64(len(key))
		if len(values) != 1 {
			return api.RequestPlan{}, fmt.Errorf("hidden field must have one decoded value")
		}
		remaining -= int64(len(values[0]))
		if remaining < 0 {
			return api.RequestPlan{}, fmt.Errorf("form fields exceed byte bound")
		}
	}
	form := make(url.Values, len(hidden)+8)
	for key, values := range hidden {
		form.Set(key, values[0])
	}
	form.Set("ctrl_190922_45$txtSearch", "")
	form.Set("ctrl_191017_163$drdDocCategory", "0")
	form.Set("ctrl_191017_163$drdDocOrg", "0")
	form.Set("ctrl_191017_163$drdDocYear", "0")
	form.Set("ctrl_191017_163$txtSearchKeyword", "")
	form.Set("ctrl_191017_163$drdRecordPerPage", "50")
	form.Set("__EVENTTARGET", target)
	form.Set("__EVENTARGUMENT", "Page$"+strconv.Itoa(page+1))
	if len(form) > r.Budget.MaxItems {
		return api.RequestPlan{}, fmt.Errorf("postback exceeds item bound")
	}
	next := origin
	next.Method, next.Body, next.Cursor, next.ParentCapture = "POST", []byte(form.Encode()), strconv.Itoa(page+1), parentCapture
	next.Headers = []api.RequestHeader{{Name: "Content-Type", Value: "application/x-www-form-urlencoded"}, {Name: "Origin", Value: baseURL}, {Name: "Referer", Value: listingURL}}
	return p.finish(next)
}

// Detail mirrors canonicalDetailURL; IDs stay query values, never path segments.
func (p *Planner) Detail(r api.DetailRequest) (api.RequestPlan, error) {
	if err := r.Validate(); err != nil {
		return api.RequestPlan{}, err
	}
	if r.ExternalID == "" || strings.Trim(r.ExternalID, "0123456789") != "" {
		return api.RequestPlan{}, fmt.Errorf("numeric document ID required")
	}
	return p.finish(api.RequestPlan{Country: r.Country, SourceID: r.SourceID, WindowKey: r.WindowKey, Purpose: model.PurposeDetail, URL: baseURL + "/?pageid=27160&docid=" + r.ExternalID + "&classid=1", Method: "GET", ParentCapture: r.ListingCaptures[0].CaptureKey, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget})
}

// Artifact plans every decoded attachment, regardless of supported conversion.
func (p *Planner) Artifact(r api.ArtifactRequest) (api.RequestPlan, error) {
	if r.Artifact.Ordinal >= r.Budget.MaxItems || int64(len(r.Artifact.URL)) > r.Budget.MaxInputBytes || r.Artifact.Capture != nil && (r.Artifact.Capture.Country != r.Country || r.Artifact.Capture.SourceID != r.SourceID) {
		return api.RequestPlan{}, fmt.Errorf("artifact exceeds bounds or crosses source")
	}
	if err := r.Validate(); err != nil {
		return api.RequestPlan{}, err
	}
	return p.finish(api.RequestPlan{Country: r.Country, SourceID: r.SourceID, WindowKey: r.WindowKey, Purpose: model.PurposeArtifact, URL: r.Artifact.URL, Method: "GET", PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget})
}

// Artifacts plans the decoded inventory without applying a converter format filter.
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

// FollowUp admits decoded detail and attachment GETs under their original budget.
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
		approved = plan.URL == listingURL
	case model.PurposeDetail:
		q := u.Query()
		approved = u.Host == "vanban.chinhphu.vn" && u.Path == "/" && len(q) == 3 && len(q["docid"]) == 1 && q.Get("docid") != "" && strings.Trim(q.Get("docid"), "0123456789") == "" && q.Get("pageid") == "27160" && q.Get("classid") == "1"
	case model.PurposeArtifact:
		approved = u.Host == "datafiles.chinhphu.vn" || u.Host == "vanban.chinhphu.vn"
	}
	if !approved {
		return api.RequestPlan{}, fmt.Errorf("URL outside approved source origins and paths")
	}
	plan.RequestKey = "pending"
	if err := plan.Validate(); err != nil {
		return api.RequestPlan{}, err
	}
	plan.RequestKey = fmt.Sprintf("%x", sha256.Sum256([]byte(plan.WindowKey+"\x00"+string(plan.Purpose)+"\x00"+plan.URL+"\x00"+string(plan.Body)+"\x00"+string(plan.PolicyHash)+"\x00"+string(plan.BudgetHash)+"\x00"+plan.ParentCapture)))
	return plan, nil
}
