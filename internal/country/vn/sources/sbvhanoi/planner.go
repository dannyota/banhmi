// Package sbvhanoi plans the supplemental Hanoi legal portal requests.
package sbvhanoi

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

// SourceID identifies the declared Vietnam source for request planning.
const SourceID = "sbv_hanoi"
const baseURL = "https://sbv.hanoi.gov.vn"
const listPath = "/van-ban-quy-pham-phap-luat"

// Planner owns a frozen reference without transport, parsing or persistence.
type Planner struct {
	policy api.SourcePolicy
}

var _ api.Planner = (*Planner)(nil)
var _ api.NumberSearcher = (*Planner)(nil)

// New performs no I/O and does not register the direct SBV source.
func New(policy api.SourcePolicy) (*Planner, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if vn.Declare().Code != "vn" {
		return nil, fmt.Errorf("Vietnam descriptor required")
	}
	return &Planner{policy: policy}, nil
}

func portalQuery() url.Values {
	return url.Values{
		"p_p_id":        {"4_WAR_portalvbpqportlet"},
		"p_p_lifecycle": {"0"},
		"p_p_state":     {"normal"},
		"p_p_mode":      {"view"},
		"p_p_col_id":    {"column-2"},
		"p_p_col_pos":   {"1"},
		"p_p_col_count": {"2"},
	}
}

func listingQuery(page, size int) url.Values {
	q := portalQuery()
	q.Set("_4_WAR_portalvbpqportlet_mvcPath", "/html/portlet/list/view.jsp")
	q.Set("_4_WAR_portalvbpqportlet_delta", strconv.Itoa(size))
	q.Set("_4_WAR_portalvbpqportlet_advancedSearch", "false")
	q.Set("_4_WAR_portalvbpqportlet_andOperator", "true")
	q.Set("_4_WAR_portalvbpqportlet_resetCur", "false")
	q.Set("_4_WAR_portalvbpqportlet_cur", strconv.Itoa(page))
	return q
}

// Listing mirrors listURL's Liferay parameters and bounded 200-row pages.
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
	if page < 1 || page > min(200, r.Budget.MaxPages) {
		return api.RequestPlan{}, fmt.Errorf("page exceeds frozen or legacy cap")
	}
	q := listingQuery(page, min(200, r.Budget.MaxItems))
	return p.finish(api.RequestPlan{Country: r.Country, SourceID: r.SourceID, WindowKey: r.WindowKey, Purpose: model.PurposeListing, URL: baseURL + listPath + "?" + q.Encode(), Method: "GET", Cursor: strconv.Itoa(page), PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget})
}

// PlanNumber mirrors SearchByNumber's bounded listing scan. Despite its comment,
// legacy Discover ignores the keyword argument and listURL has no number filter.
// Do not invent a portal query parameter. M1.9 must compare all decoded numbers
// and preserve ambiguity; no candidate or incomplete scan proves absence.
func (p *Planner) PlanNumber(r api.NumberRequest) (api.RequestPlan, error) {
	if err := r.Validate(); err != nil {
		return api.RequestPlan{}, err
	}
	if strings.TrimSpace(r.Number.Text) == "" {
		return api.RequestPlan{}, fmt.Errorf("number required")
	}
	q := listingQuery(1, min(200, r.Budget.MaxItems))
	return p.finish(api.RequestPlan{Country: r.Country, SourceID: r.SourceID, WindowKey: r.WindowKey, Purpose: model.PurposeNumber, URL: baseURL + listPath + "?" + q.Encode(), Method: "GET", Cursor: "1", PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget})
}

// NextNumberPage continues the same candidate scan without resetting allowance.
func (p *Planner) NextNumberPage(origin api.RequestPlan, parentCapture string) (api.RequestPlan, error) {
	if _, err := p.finish(origin); err != nil {
		return api.RequestPlan{}, err
	}
	page, err := strconv.Atoi(origin.Cursor)
	if err != nil || page < 1 || page >= min(200, origin.Budget.MaxPages) || origin.Purpose != model.PurposeNumber || parentCapture == "" {
		return api.RequestPlan{}, fmt.Errorf("invalid or capped number scan")
	}
	q := listingQuery(page+1, min(200, origin.Budget.MaxItems))
	origin.URL, origin.Cursor, origin.ParentCapture = baseURL+listPath+"?"+q.Encode(), strconv.Itoa(page+1), parentCapture
	return p.finish(origin)
}

// Detail mirrors canonicalDetailURL with a numeric ID encoded as a query value.
func (p *Planner) Detail(r api.DetailRequest) (api.RequestPlan, error) {
	if err := r.Validate(); err != nil {
		return api.RequestPlan{}, err
	}
	if r.ExternalID == "" || strings.Trim(r.ExternalID, "0123456789") != "" {
		return api.RequestPlan{}, fmt.Errorf("numeric portal ID required")
	}
	q := portalQuery()
	q.Set("_4_WAR_portalvbpqportlet_id", r.ExternalID)
	q.Set("_4_WAR_portalvbpqportlet_mvcPath", "/html/portlet/list/view_detail.jsp")
	return p.finish(api.RequestPlan{Country: r.Country, SourceID: r.SourceID, WindowKey: r.WindowKey, Purpose: model.PurposeDetail, URL: baseURL + listPath + "?" + q.Encode(), Method: "GET", ParentCapture: r.ListingCaptures[0].CaptureKey, PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget})
}

// Artifact retains every decoded attachment, including legacy DOC files.
func (p *Planner) Artifact(r api.ArtifactRequest) (api.RequestPlan, error) {
	if r.Artifact.Ordinal >= r.Budget.MaxItems || int64(len(r.Artifact.URL)) > r.Budget.MaxInputBytes || r.Artifact.Capture != nil && (r.Artifact.Capture.Country != r.Country || r.Artifact.Capture.SourceID != r.SourceID) {
		return api.RequestPlan{}, fmt.Errorf("artifact exceeds bounds or crosses source")
	}
	if err := r.Validate(); err != nil {
		return api.RequestPlan{}, err
	}
	return p.finish(api.RequestPlan{Country: r.Country, SourceID: r.SourceID, WindowKey: r.WindowKey, Purpose: model.PurposeArtifact, URL: r.Artifact.URL, Method: "GET", PolicyHash: r.PolicyHash, BudgetHash: r.BudgetHash, Budget: r.Budget})
}

// Artifacts retains the full decoded inventory, including the DOC links v1 skips.
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

// NextPage increments only the page value and preserves the frozen origin.
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

// FollowUp plans a decoded detail or attachment under its original allowance.
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
	if u.Scheme != "https" || u.Host != "sbv.hanoi.gov.vn" || strings.ContainsAny(u.Path, "\\") {
		return api.RequestPlan{}, fmt.Errorf("URL outside approved origin")
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." || strings.Contains(segment, "%") {
			return api.RequestPlan{}, fmt.Errorf("unsafe URL path")
		}
	}
	if plan.Purpose == model.PurposeArtifact {
		if !strings.HasPrefix(u.Path, "/documents/") || len(u.Path) <= len("/documents/") {
			return api.RequestPlan{}, fmt.Errorf("unapproved artifact endpoint")
		}
	} else {
		q := u.Query()
		if u.Path != listPath || q.Get("p_p_id") != "4_WAR_portalvbpqportlet" {
			return api.RequestPlan{}, fmt.Errorf("unapproved portal path")
		}
		if plan.Purpose == model.PurposeDetail {
			if len(q) != 9 || len(q["_4_WAR_portalvbpqportlet_id"]) != 1 || len(q["_4_WAR_portalvbpqportlet_mvcPath"]) != 1 || q.Get("_4_WAR_portalvbpqportlet_mvcPath") != "/html/portlet/list/view_detail.jsp" || q.Get("_4_WAR_portalvbpqportlet_id") == "" || strings.Trim(q.Get("_4_WAR_portalvbpqportlet_id"), "0123456789") != "" {
				return api.RequestPlan{}, fmt.Errorf("invalid detail parameters")
			}
			for key, values := range portalQuery() {
				if len(q[key]) != 1 || q.Get(key) != values[0] {
					return api.RequestPlan{}, fmt.Errorf("unapproved portal control")
				}
			}
		} else {
			page, err := strconv.Atoi(plan.Cursor)
			if err != nil || page < 1 || page > min(200, plan.Budget.MaxPages) || plan.URL != baseURL+listPath+"?"+listingQuery(page, min(200, plan.Budget.MaxItems)).Encode() {
				return api.RequestPlan{}, fmt.Errorf("invalid listing parameters")
			}
		}
	}
	plan.RequestKey = "pending"
	if err := plan.Validate(); err != nil {
		return api.RequestPlan{}, err
	}
	plan.RequestKey = fmt.Sprintf("%x", sha256.Sum256([]byte(plan.WindowKey+"\x00"+string(plan.Purpose)+"\x00"+plan.URL+"\x00"+string(plan.Body)+"\x00"+string(plan.PolicyHash)+"\x00"+string(plan.BudgetHash)+"\x00"+plan.ParentCapture)))
	return plan, nil
}
