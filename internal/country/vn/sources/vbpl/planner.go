// Package vbpl plans Vietnam gateway requests without resolving policy assets.
package vbpl

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/source/api"
	"danny.vn/banhmi/internal/country/vn"
)

// SourceID identifies the declared Vietnam source for request planning.
const SourceID = "vbpl"
const gateway = "https://vbpl-bientap-gateway.moj.gov.vn/api/qtdc/public"

// Config contains decoded, policy-pinned discovery slices and exact artifact
// origins. Core verifies these values against Policy before construction. An
// empty artifact list denies downloads; no presigned host is inferred from data.
type Config struct {
	StateBankAgencies []string
	CentralAgencies   []string
	Keywords          []string
	ArtifactOrigins   []string
}

// Planner owns copies of its frozen configuration. The zero value denies work.
type Planner struct {
	policy api.SourcePolicy
	config Config
}

var _ api.Planner = (*Planner)(nil)

// New validates references and copies decoded values without reading assets.
func New(policy api.SourcePolicy, config Config) (*Planner, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if vn.Declare().Code != "vn" {
		return nil, fmt.Errorf("Vietnam descriptor required")
	}
	for _, origin := range config.ArtifactOrigins {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.Host != u.Hostname() || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("artifact origin requires an exact HTTPS host")
		}
	}
	config.StateBankAgencies = append([]string(nil), config.StateBankAgencies...)
	config.CentralAgencies = append([]string(nil), config.CentralAgencies...)
	config.Keywords = append([]string(nil), config.Keywords...)
	config.ArtifactOrigins = append([]string(nil), config.ArtifactOrigins...)
	return &Planner{policy: policy, config: config}, nil
}

// Listing mirrors Discover's agency sweep and configured title-search slices.
// SliceKey is "sweep" or "keyword:<zero-based index>"; Cursor is a page number.
func (p *Planner) Listing(r api.DiscoverRequest) (api.RequestPlan, error) {
	if err := r.Validate(); err != nil {
		return api.RequestPlan{}, err
	}
	page := 1
	if r.Cursor != "" {
		var err error
		page, err = strconv.Atoi(r.Cursor)
		if err != nil {
			return api.RequestPlan{}, fmt.Errorf("invalid page cursor")
		}
	}
	if page < 1 || page > r.Budget.MaxPages {
		return api.RequestPlan{}, fmt.Errorf("page exceeds frozen window")
	}
	agencies, keyword := p.config.StateBankAgencies, ""
	if r.SliceKey != "sweep" {
		index, err := strconv.Atoi(strings.TrimPrefix(r.SliceKey, "keyword:"))
		if !strings.HasPrefix(r.SliceKey, "keyword:") || err != nil || index < 0 || index >= len(p.config.Keywords) {
			return api.RequestPlan{}, fmt.Errorf("unknown discovery slice")
		}
		keyword = strings.TrimSpace(p.config.Keywords[index])
		agencies = p.config.CentralAgencies
		if keyword == "" {
			return api.RequestPlan{}, fmt.Errorf("empty keyword slice")
		}
	}
	if len(agencies) == 0 || len(agencies) > r.Budget.MaxItems {
		return api.RequestPlan{}, fmt.Errorf("bounded agency scope required")
	}
	remaining := r.Budget.MaxInputBytes - int64(len(keyword))
	for _, agency := range agencies {
		remaining -= int64(len(agency))
	}
	if remaining < 0 {
		return api.RequestPlan{}, fmt.Errorf("discovery fields exceed byte bound")
	}
	size := min(50, r.Budget.MaxItems)
	body, err := json.Marshal(struct {
		PageNumber    int      `json:"pageNumber"`
		PageSize      int      `json:"pageSize"`
		SortBy        string   `json:"sortBy"`
		SortDirection string   `json:"sortDirection"`
		GroupVbpl     bool     `json:"groupVbpl"`
		AgencyLevel   string   `json:"agencyLevel"`
		OptionDoc     string   `json:"optionDoc"`
		MatchMode     string   `json:"matchMode"`
		AgencyIDs     []string `json:"agencyIds,omitempty"`
		Keyword       string   `json:"keyword,omitempty"`
	}{page, size, "issueDate", "desc", false, "TRUNG_UONG", "title", "all_words", agencies, keyword})
	if err != nil {
		return api.RequestPlan{}, err
	}
	plan := api.RequestPlan{Country: r.Country, SourceID: r.SourceID, WindowKey: r.WindowKey, Purpose: model.PurposeListing, URL: gateway + "/doc/all", Method: "POST", Body: body, Cursor: strconv.Itoa(page), PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget}
	plan.Headers = []api.RequestHeader{{Name: "Content-Type", Value: "application/json"}, {Name: "Origin", Value: "https://vbpl.vn"}, {Name: "Accept", Value: "application/json"}}
	return p.finish(plan)
}

// Detail mirrors FetchDetail's first metadata request, retaining opaque IDs.
func (p *Planner) Detail(r api.DetailRequest) (api.RequestPlan, error) {
	if err := r.Validate(); err != nil {
		return api.RequestPlan{}, err
	}
	if !safeID(r.ExternalID) {
		return api.RequestPlan{}, fmt.Errorf("invalid opaque document ID")
	}
	kind := r.ExpectedResponseKind
	if kind == "" {
		kind = "metadata"
	}
	locator := gateway + "/doc/" + r.ExternalID
	switch kind {
	case "files":
		locator = gateway + "/doc/minio/buckets/vbpl/folders/" + r.ExternalID + "/files?parts=1,2,3,4,5"
	case "tree":
		locator = gateway + "/doc/provision/tree/" + r.ExternalID
	case "diagram":
		locator += "/diagram"
	}
	return p.finish(api.RequestPlan{Country: r.Country, SourceID: r.SourceID, WindowKey: r.WindowKey, Purpose: model.PurposeDetail, ExpectedResponseKind: kind, URL: locator, Method: "GET", ParentCapture: r.ListingCaptures[0].CaptureKey, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget})
}

// Artifact captures one decoded inventory entry, including unsupported formats.
func (p *Planner) Artifact(r api.ArtifactRequest) (api.RequestPlan, error) {
	if r.Artifact.Ordinal >= r.Budget.MaxItems || int64(len(r.Artifact.URL)) > r.Budget.MaxInputBytes || r.Artifact.Capture != nil && (r.Artifact.Capture.Country != r.Country || r.Artifact.Capture.SourceID != r.SourceID) {
		return api.RequestPlan{}, fmt.Errorf("artifact exceeds bounds or crosses source")
	}
	if err := r.Validate(); err != nil {
		return api.RequestPlan{}, err
	}
	return p.finish(api.RequestPlan{Country: r.Country, SourceID: r.SourceID, WindowKey: r.WindowKey, Purpose: model.PurposeArtifact, URL: r.Artifact.URL, Method: "GET", PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget})
}

// Artifacts enumerates already decoded files without dropping unknown formats.
// Entry ordering and roles belong to the decoded inventory, not URL extensions.
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

// DetailFollowUps mirrors the files and diagram subrequests in FetchDetail.
// Both are required metadata captures, not downloaded-file transport envelopes.
func (p *Planner) DetailFollowUps(origin api.RequestPlan, id, parentCapture string) ([]api.RequestPlan, error) {
	if !safeID(id) || origin.Purpose != model.PurposeDetail || origin.URL != gateway+"/doc/"+id || parentCapture == "" {
		return nil, fmt.Errorf("follow-ups require matching detail origin")
	}
	if _, err := p.finish(origin); err != nil {
		return nil, err
	}
	if origin.Budget.MaxItems < 2 {
		return nil, fmt.Errorf("follow-ups exceed item bound")
	}
	locators := []string{gateway + "/doc/minio/buckets/vbpl/folders/" + id + "/files?parts=1,2,3,4,5", gateway + "/doc/" + id + "/diagram"}
	plans := make([]api.RequestPlan, 0, len(locators))
	for index, locator := range locators {
		next := origin
		next.ExpectedResponseKind = []string{"files", "diagram"}[index]
		next.URL, next.ParentCapture, next.RequestKey = locator, parentCapture, ""
		plan, err := p.finish(next)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

// NextPage plans one continuation; core decides whether decoded coverage needs it.
func (p *Planner) NextPage(r api.DiscoverRequest, parentCapture string) (api.RequestPlan, error) {
	previous, err := p.Listing(r)
	if err != nil || parentCapture == "" {
		return api.RequestPlan{}, fmt.Errorf("pagination requires valid listing and capture")
	}
	page, _ := strconv.Atoi(previous.Cursor)
	r.Cursor = strconv.Itoa(page + 1)
	next, err := p.Listing(r)
	if err != nil {
		return api.RequestPlan{}, err
	}
	next.ParentCapture = parentCapture
	return p.finish(next)
}

func safeID(id string) bool {
	if id == "" {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-') {
			return false
		}
	}
	return true
}

func (p *Planner) finish(plan api.RequestPlan) (api.RequestPlan, error) {
	if plan.Country != "vn" || plan.SourceID != SourceID || plan.PolicyHash != p.policy.Digest || p.policy.Validate() != nil {
		return api.RequestPlan{}, fmt.Errorf("request differs from frozen source policy")
	}
	if int64(len(plan.URL)) > plan.Budget.MaxInputBytes {
		return api.RequestPlan{}, fmt.Errorf("URL exceeds byte bound")
	}
	if err := apiURL(plan.URL, plan.Purpose, p.config.ArtifactOrigins); err != nil {
		return api.RequestPlan{}, err
	}
	if plan.Method == "GET" && plan.Purpose == model.PurposeDetail && len(plan.Headers) == 0 {
		plan.Headers = []api.RequestHeader{{Name: "Origin", Value: "https://vbpl.vn"}, {Name: "Referer", Value: "https://vbpl.vn/"}, {Name: "Accept", Value: "application/json"}}
	}
	plan.RequestKey = "pending"
	if err := plan.Validate(); err != nil {
		return api.RequestPlan{}, err
	}
	plan.RequestKey = fmt.Sprintf("%x", sha256.Sum256([]byte(plan.WindowKey+"\x00"+string(plan.Purpose)+"\x00"+plan.ExpectedResponseKind+"\x00"+plan.URL+"\x00"+string(plan.Body)+"\x00"+string(plan.PolicyHash)+"\x00"+string(plan.BudgetHash)+"\x00"+plan.ParentCapture)))
	return plan, nil
}

func apiURL(raw string, purpose model.RequestPurpose, artifacts []string) error {
	if err := model.ValidatePublicURL(raw); err != nil {
		return err
	}
	u, _ := url.Parse(raw)
	if u.Scheme != "https" || strings.ContainsAny(u.Path, "\\") {
		return fmt.Errorf("unapproved URL scheme or path")
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." || strings.Contains(segment, "%") {
			return fmt.Errorf("unsafe URL path")
		}
	}
	if purpose == model.PurposeArtifact {
		for _, origin := range artifacts {
			if u.Scheme+"://"+u.Host == origin {
				return nil
			}
		}
	} else if u.Host == "vbpl-bientap-gateway.moj.gov.vn" && strings.HasPrefix(u.Path, "/api/qtdc/public/doc/") {
		return nil
	}
	return fmt.Errorf("URL outside approved source origins")
}
