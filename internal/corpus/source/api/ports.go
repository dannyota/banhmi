// Package api owns pure source extension and reference-only capture contracts.
package api

import (
	"context"
	"fmt"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
)

// Planner deterministically plans bounded listing, detail and artifact requests
// without I/O; core policy validates every initial and follow-up locator.
type Planner interface {
	Listing(DiscoverRequest) (RequestPlan, error)
	Detail(DetailRequest) (RequestPlan, error)
	Artifact(ArtifactRequest) (RequestPlan, error)
}

// Decoder runs only inside the sandbox on verified captured references, with
// no network client, credentials, persistence or remote URI resolution.
type Decoder interface {
	Listing(CapturedInput, DiscoverRequest) (DiscoveryPage, error)
	Detail(CapturedInput, DetailRequest) (DetailResult, error)
}

// Factory binds lazy pure constructors; construction never performs I/O.
type Factory struct {
	SourceID   string
	NewPlanner func() Planner
	NewDecoder func() Decoder
}

func (f Factory) Validate() error {
	if f.SourceID == "" || f.NewPlanner == nil || f.NewDecoder == nil {
		return fmt.Errorf("source factory requires identity and both constructors")
	}
	return nil
}

// NumberSearcher plans exact-number discovery without I/O; results must recheck
// equality and preserve ambiguity rather than selecting an arbitrary candidate.
type NumberSearcher interface {
	PlanNumber(NumberRequest) (RequestPlan, error)
}

// Fetcher executes core-approved requests under shared source policy, stages
// bounded content and returns immutable capture references without parsing it.
type Fetcher interface {
	Capture(context.Context, FetchRequest, SourcePolicy) (model.CaptureRef, error)
}

// DecoderSandbox verifies and mounts captures before dispatching the pinned
// decoder; it validates reference-only results before they leave isolation.
type DecoderSandbox interface {
	Listing(context.Context, DecoderRef, model.CaptureRef, DiscoverRequest) (DiscoveryPage, error)
	Detail(context.Context, DecoderRef, model.CaptureRef, DetailRequest) (DetailResult, error)
}

// IdentityRules interprets captured metadata deterministically without I/O;
// uncertain identity produces findings and never invented source fields.
type IdentityRules interface {
	Classify(model.Observation) (DocumentType, []model.Finding)
	ParseNumber(model.AssetRef, IdentityContext) (NumberMatch, []model.Finding)
}

type RequestPlan struct {
	Country       string
	SourceID      string
	RequestKey    string
	WindowKey     string
	Purpose       model.RequestPurpose
	URL           string
	Method        string
	Headers       *model.AssetRef
	Body          *model.AssetRef
	ParentCapture string
	Cursor        string
	PolicyHash    model.Digest
	Budget        model.ResourceBudget
}

func (r RequestPlan) Validate() error {
	if r.Country == "" || r.SourceID == "" || r.RequestKey == "" || r.WindowKey == "" || !r.Purpose.Valid() || (r.Method != "GET" && r.Method != "POST") {
		return fmt.Errorf("invalid request identity, purpose or method")
	}
	if err := model.ValidatePublicURL(r.URL); err != nil {
		return err
	}
	if err := r.PolicyHash.Validate(); err != nil {
		return err
	}
	for _, a := range []*model.AssetRef{r.Headers, r.Body} {
		if a != nil {
			if err := a.Validate(); err != nil {
				return err
			}
		}
	}
	return r.Budget.Validate()
}

type FetchRequest = RequestPlan
type CapturedInput struct {
	Capture model.CaptureRef
}

func (i CapturedInput) Validate() error { return i.Capture.Validate() }

type DiscoveryPage = model.ListingPageResult
type DetailResult = model.DetailResult

type DecoderRef struct {
	Country     string
	SourceID    string
	Version     string
	ImageDigest model.Digest
}

func (r DecoderRef) Validate() error {
	for _, key := range []string{r.Country, r.SourceID, r.Version} {
		if err := model.ValidateKey(key); err != nil {
			return err
		}
	}
	return r.ImageDigest.Validate()
}

// SourcePolicy is a hash-pinned, credential-free effective policy manifest.
// The core resolves origin/redirect, pacing, retry and sweep rules from it.
type SourcePolicy struct {
	Manifest model.AssetRef
	Digest   model.Digest
}

func (p SourcePolicy) Validate() error {
	if err := p.Manifest.Validate(); err != nil {
		return err
	}
	if p.Digest != p.Manifest.SHA256 {
		return fmt.Errorf("policy digest must match manifest bytes")
	}
	return nil
}

type DiscoveryMode string

const (
	DiscoveryIncremental DiscoveryMode = "incremental"
	DiscoveryFullSweep   DiscoveryMode = "full_sweep"
	DiscoveryBackfill    DiscoveryMode = "backfill"
)

func (m DiscoveryMode) Valid() bool {
	return m == DiscoveryIncremental || m == DiscoveryFullSweep || m == DiscoveryBackfill
}

type DiscoverRequest struct {
	Country    string
	SourceID   string
	WindowKey  string
	SliceKey   string
	Cursor     string
	Mode       DiscoveryMode
	Start      time.Time
	Cutoff     time.Time
	PolicyHash model.Digest
	Budget     model.ResourceBudget
}

func (r DiscoverRequest) Validate() error {
	if r.Country == "" || r.SourceID == "" || r.WindowKey == "" || r.SliceKey == "" || !r.Mode.Valid() || r.Start.IsZero() || !r.Cutoff.After(r.Start) {
		return fmt.Errorf("invalid discovery identity or frozen window")
	}
	if err := r.PolicyHash.Validate(); err != nil {
		return err
	}
	return r.Budget.Validate()
}

type DetailRequest struct {
	Country         string
	SourceID        string
	ExternalID      string
	ListingCaptures []model.CaptureRef
	PolicyHash      model.Digest
	Budget          model.ResourceBudget
}

func (r DetailRequest) Validate() error {
	for _, key := range []string{r.Country, r.SourceID, r.ExternalID} {
		if err := model.ValidateKey(key); err != nil {
			return err
		}
	}
	if len(r.ListingCaptures) == 0 {
		return fmt.Errorf("detail requires captured listing provenance")
	}
	for _, c := range r.ListingCaptures {
		if err := c.Validate(); err != nil {
			return err
		}
		if c.Country != r.Country || c.SourceID != r.SourceID {
			return fmt.Errorf("listing capture crosses country or source")
		}
	}
	if err := r.PolicyHash.Validate(); err != nil {
		return err
	}
	return r.Budget.Validate()
}

type ArtifactRequest struct {
	Country           string
	SourceID          string
	Artifact          model.ArtifactInventoryEntry
	ParentObservation string
	PolicyHash        model.Digest
	Budget            model.ResourceBudget
}

func (r ArtifactRequest) Validate() error {
	for _, key := range []string{r.Country, r.SourceID, r.ParentObservation} {
		if err := model.ValidateKey(key); err != nil {
			return err
		}
	}
	if err := r.Artifact.Validate(); err != nil {
		return err
	}
	if err := r.PolicyHash.Validate(); err != nil {
		return err
	}
	return r.Budget.Validate()
}

// Number text and title/issuer context stay in captured assets, not envelopes.
type NumberRequest struct {
	Country    string
	SourceID   string
	Number     model.AssetRef
	Context    IdentityContext
	PolicyHash model.Digest
	Budget     model.ResourceBudget
}

func (r NumberRequest) Validate() error {
	for _, key := range []string{r.Country, r.SourceID} {
		if err := model.ValidateKey(key); err != nil {
			return err
		}
	}
	for _, a := range []model.AssetRef{r.Number, r.Context.Metadata} {
		if err := a.Validate(); err != nil {
			return err
		}
	}
	if err := r.PolicyHash.Validate(); err != nil {
		return err
	}
	return r.Budget.Validate()
}

type DocumentType struct {
	Key      string
	Evidence model.AssetRef
}

type IdentityContext struct {
	Metadata model.AssetRef
}

type NumberMatchKind string

const (
	NumberNoMatch   NumberMatchKind = "no_match"
	NumberExact     NumberMatchKind = "exact"
	NumberAmbiguous NumberMatchKind = "ambiguous"
)

func (k NumberMatchKind) Valid() bool {
	return k == NumberNoMatch || k == NumberExact || k == NumberAmbiguous
}

type NumberMatch struct {
	Candidates model.AssetRef
	Kind       NumberMatchKind
}

func (m NumberMatch) Validate() error {
	if !m.Kind.Valid() {
		return fmt.Errorf("unknown number match kind")
	}
	return m.Candidates.Validate()
}
