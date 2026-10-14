// Package api owns pure source extension and reference-only capture contracts.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
	"unicode/utf8"

	"danny.vn/banhmi/internal/corpus/model"
)

// Planner deterministically plans bounded listing, detail and artifact requests
// without I/O; core policy validates every initial and follow-up locator.
type Planner interface {
	// Listing plans or decodes one bounded local listing slice.
	Listing(DiscoverRequest) (RequestPlan, error)
	// Detail plans or decodes one bounded local detail request.
	Detail(DetailRequest) (RequestPlan, error)
	// Artifact plans a bounded local artifact capture.
	Artifact(ArtifactRequest) (RequestPlan, error)
}

// Decoder runs only inside the sandbox on verified mounted capture bytes, with
// no network client, credentials, persistence or remote URI resolution.
type Decoder interface {
	// Listing plans or decodes one bounded local listing slice.
	Listing(CapturedInput, DiscoverRequest) (DecodedDiscoveryPage, error)
	// Detail plans or decodes one bounded local detail request.
	Detail(CapturedInput, DetailRequest) (DecodedDetailResult, error)
}

// Factory binds lazy pure constructors; construction never performs I/O.
type Factory struct {
	SourceID   string
	NewPlanner func() Planner
	NewDecoder func() Decoder
}

// Validate requires source identity and both constructors without invoking them.
func (f Factory) Validate() error {
	if f.SourceID == "" || f.NewPlanner == nil || f.NewDecoder == nil {
		return fmt.Errorf("source factory requires identity and both constructors")
	}
	return nil
}

// NumberSearcher plans exact-number discovery without I/O; results must recheck
// equality and preserve ambiguity rather than selecting an arbitrary candidate.
type NumberSearcher interface {
	// PlanNumber plans exact-number discovery from decoded local values.
	PlanNumber(NumberRequest) (RequestPlan, error)
}

// Fetcher executes core-approved requests under shared source policy, stages
// bounded content and returns immutable capture references without parsing it.
type Fetcher interface {
	// Capture resolves a screened request manifest and stages a bounded capture.
	Capture(context.Context, RequestEnvelope) (model.CaptureEnvelope, error)
}

// DecoderSandbox verifies and mounts captures before dispatching the pinned
// decoder; it validates reference-only results before they leave isolation.
type DecoderSandbox interface {
	// Listing plans or decodes one bounded local listing slice.
	Listing(context.Context, DecoderEnvelope, model.CaptureEnvelope, RequestEnvelope) (model.ListingPageEnvelope, error)
	// Detail plans or decodes one bounded local detail request.
	Detail(context.Context, DecoderEnvelope, model.CaptureEnvelope, RequestEnvelope) (model.DetailEnvelope, error)
}

// IdentityRules interprets captured metadata deterministically without I/O;
// uncertain identity produces findings and never invented source fields.
type IdentityRules interface {
	// Classify interprets decoded field values without resolving references.
	Classify(IdentityContext) (DocumentType, []model.Finding)
	// ParseNumber parses bounded decoded number/context values without I/O.
	ParseNumber(NumberInput) (NumberMatch, []model.Finding)
}

// RequestPlan remains worker-local; only RequestEnvelope enters transport.
type RequestPlan struct {
	Country       string
	SourceID      string
	RequestKey    string
	WindowKey     string
	Purpose       model.RequestPurpose
	URL           string
	Method        string
	Headers       []RequestHeader
	Body          []byte
	ParentCapture string
	Cursor        string
	PolicyHash    model.Digest
	BudgetHash    model.Digest
	Budget        model.ResourceBudget
}

// Validate checks local request origin, method, locator, policy and byte/item bounds.
func (r RequestPlan) Validate() error {
	if r.Country == "" || r.SourceID == "" || r.RequestKey == "" || r.WindowKey == "" || !r.Purpose.Valid() || (r.Method != "GET" && r.Method != "POST") {
		return fmt.Errorf("invalid request identity, purpose or method")
	}
	if err := model.ValidatePublicURL(r.URL); err != nil {
		return fmt.Errorf("public URL: %w", err)
	}
	if err := r.BudgetHash.Validate(); err != nil {
		return fmt.Errorf("frozen budget hash: %w", err)
	}
	if err := r.PolicyHash.Validate(); err != nil {
		return fmt.Errorf("policy hash: %w", err)
	}
	if len(r.Headers) > r.Budget.MaxItems {
		return fmt.Errorf("request headers exceed frozen item bound")
	}
	remaining := r.Budget.MaxInputBytes
	for _, value := range []string{r.URL, r.Cursor} {
		if int64(len(value)) > remaining {
			return fmt.Errorf("request values exceed frozen byte bound")
		}
		remaining -= int64(len(value))
	}
	for _, header := range r.Headers {
		for _, value := range []string{header.Name, header.Value} {
			if int64(len(value)) > remaining {
				return fmt.Errorf("request headers exceed frozen byte bound")
			}
			remaining -= int64(len(value))
		}
	}
	if int64(len(r.Body)) > remaining {
		return fmt.Errorf("request body exceeds frozen byte bound")
	}
	if err := r.Budget.Validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	return nil
}

// FetchRequest is a worker-local request plan executed by core capture.
type FetchRequest = RequestPlan
// CapturedInput is verified bounded content mounted by the sandbox, never transport.
type CapturedInput struct {
	Capture model.CaptureRef
	Content []byte
	Budget  model.ResourceBudget
}

// Validate verifies mounted bytes against the pinned capture digest and frozen bound.
func (i CapturedInput) Validate() error {
	if err := i.Capture.Validate(); err != nil {
		return fmt.Errorf("capture: %w", err)
	}
	if err := i.Budget.Validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	if int64(len(i.Content)) > i.Budget.MaxInputBytes || int64(len(i.Content)) != i.Capture.Asset.ByteSize {
		return fmt.Errorf("capture bytes differ from reference or exceed frozen bound")
	}
	hash := sha256.Sum256(i.Content)
	if model.Digest(hex.EncodeToString(hash[:])) != i.Capture.Asset.SHA256 {
		return fmt.Errorf("capture content digest mismatch")
	}
	return nil
}

// DiscoveryPage is a worker-local decoded listing result.
type DiscoveryPage = model.ListingPageResult
// DetailResult is a local detail manifest with artifacts and follow-up plans.
type DetailResult = model.DetailResult

// DecoderRef pins a local decoder version and sandbox image.
type DecoderRef struct {
	Country     string
	SourceID    string
	Version     string
	ImageDigest model.Digest
}

// Validate checks local country/source/version keys and the pinned image digest.
func (r DecoderRef) Validate() error {
	for name, key := range map[string]string{"country": r.Country, "source": r.SourceID, "version": r.Version} {
		if err := model.ValidateKey(key); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if err := r.ImageDigest.Validate(); err != nil {
		return fmt.Errorf("decoder image digest: %w", err)
	}
	return nil
}

// SourcePolicy is a hash-pinned, credential-free effective policy manifest.
// The core resolves origin/redirect, pacing, retry and sweep rules from it.
type SourcePolicy struct {
	Manifest model.AssetRef
	Digest   model.Digest
}

// Validate requires the policy digest to equal its immutable manifest hash.
func (p SourcePolicy) Validate() error {
	if err := p.Manifest.Validate(); err != nil {
		return fmt.Errorf("policy manifest: %w", err)
	}
	if p.Digest != p.Manifest.SHA256 {
		return fmt.Errorf("policy digest must match manifest bytes")
	}
	return nil
}

// DiscoveryMode names the supported discovery window modes.
type DiscoveryMode string

const (
	// DiscoveryIncremental resumes incremental discovery.
	DiscoveryIncremental DiscoveryMode = "incremental"
	// DiscoveryFullSweep covers the full configured source window.
	DiscoveryFullSweep   DiscoveryMode = "full_sweep"
	// DiscoveryBackfill fills configured historical coverage.
	DiscoveryBackfill    DiscoveryMode = "backfill"
)

// Valid reports membership in the closed contract vocabulary.
func (m DiscoveryMode) Valid() bool {
	return m == DiscoveryIncremental || m == DiscoveryFullSweep || m == DiscoveryBackfill
}

// DiscoverRequest freezes a local source window, cursor, policy and budget.
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
	BudgetHash model.Digest
	Budget     model.ResourceBudget
}

// Validate checks the fixed discovery window, origin and frozen policy/budget.
func (r DiscoverRequest) Validate() error {
	if r.Country == "" || r.SourceID == "" || r.WindowKey == "" || r.SliceKey == "" || !r.Mode.Valid() || r.Start.IsZero() || !r.Cutoff.After(r.Start) {
		return fmt.Errorf("invalid discovery identity or frozen window")
	}
	if err := r.BudgetHash.Validate(); err != nil {
		return fmt.Errorf("frozen budget hash: %w", err)
	}
	if err := r.PolicyHash.Validate(); err != nil {
		return fmt.Errorf("policy hash: %w", err)
	}
	if err := r.Budget.Validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	return nil
}

// DetailRequest retains native identity and the originating window and budget.
type DetailRequest struct {
	Country         string
	SourceID        string
	ExternalID      string
	ListingCaptures []model.CaptureRef
	PolicyHash      model.Digest
	WindowKey       string
	BudgetHash      model.Digest
	Budget          model.ResourceBudget
}

// Validate requires captured listing provenance and the original window/budget.
func (r DetailRequest) Validate() error {
	for name, key := range map[string]string{"country": r.Country, "source": r.SourceID, "window": r.WindowKey} {
		if err := model.ValidateKey(key); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if len(r.ListingCaptures) == 0 {
		return fmt.Errorf("detail requires captured listing provenance")
	}
	for _, c := range r.ListingCaptures {
		if err := c.Validate(); err != nil {
			return fmt.Errorf("capture: %w", err)
		}
		if c.Country != r.Country || c.SourceID != r.SourceID {
			return fmt.Errorf("listing capture crosses country or source")
		}
	}
	if err := r.BudgetHash.Validate(); err != nil {
		return fmt.Errorf("frozen budget hash: %w", err)
	}
	if err := r.PolicyHash.Validate(); err != nil {
		return fmt.Errorf("policy hash: %w", err)
	}
	if err := r.Budget.Validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	return nil
}

// ArtifactRequest retains the parent observation and originating window and budget.
type ArtifactRequest struct {
	Country           string
	SourceID          string
	Artifact          model.ArtifactInventoryEntry
	ParentObservation string
	PolicyHash        model.Digest
	WindowKey         string
	BudgetHash        model.Digest
	Budget            model.ResourceBudget
}

// Validate checks the local artifact and original window/budget identity.
func (r ArtifactRequest) Validate() error {
	for name, key := range map[string]string{"country": r.Country, "source": r.SourceID, "parent observation": r.ParentObservation, "window": r.WindowKey} {
		if err := model.ValidateKey(key); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if err := r.Artifact.Validate(); err != nil {
		return fmt.Errorf("artifact: %w", err)
	}
	if err := r.BudgetHash.Validate(); err != nil {
		return fmt.Errorf("frozen budget hash: %w", err)
	}
	if err := r.PolicyHash.Validate(); err != nil {
		return fmt.Errorf("policy hash: %w", err)
	}
	if err := r.Budget.Validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	return nil
}

// NumberRequest supplies bounded decoded number/context values for local planning.
// Only RequestEnvelope crosses transport; native text remains inside Vietnam.
type NumberRequest struct {
	Country    string
	SourceID   string
	WindowKey  string
	Number     NumberInput
	PolicyHash model.Digest
	BudgetHash model.Digest
	Budget     model.ResourceBudget
}

// Validate checks decoded number input and original window/policy/budget identity.
func (r NumberRequest) Validate() error {
	for name, key := range map[string]string{"country": r.Country, "source": r.SourceID, "window": r.WindowKey} {
		if err := model.ValidateKey(key); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if err := r.Number.Validate(); err != nil {
		return fmt.Errorf("number input: %w", err)
	}
	if err := r.BudgetHash.Validate(); err != nil {
		return fmt.Errorf("frozen budget hash: %w", err)
	}
	if err := r.PolicyHash.Validate(); err != nil {
		return fmt.Errorf("policy hash: %w", err)
	}
	if err := r.Budget.Validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	return nil
}

// DocumentType returns computed classification and exact field provenance.
type DocumentType struct {
	Key      string
	Evidence []model.Span
}

// IdentityContext supplies decoded source fields and provenance to pure rules.
type IdentityContext struct {
	Title  string
	Issuer string
	Type   string
	Fields []model.Span
	Limits model.RuleLimits
}

// Validate checks decoded context bounds and exact source anchors.
func (c IdentityContext) Validate() error {
	if err := c.Limits.Validate(); err != nil {
		return fmt.Errorf("context limits: %w", err)
	}
	if len(c.Fields) == 0 || len(c.Fields) > c.Limits.MaxItems {
		return fmt.Errorf("context requires bounded field provenance")
	}
	remaining := c.Limits.MaxBytes
	for _, value := range []string{c.Title, c.Issuer, c.Type} {
		if !utf8.ValidString(value) || int64(len(value)) > remaining {
			return fmt.Errorf("context exceeds byte bound or has invalid UTF-8")
		}
		remaining -= int64(len(value))
	}
	for i, span := range c.Fields {
		if err := span.Validate(); err != nil {
			return fmt.Errorf("context field %d: %w", i, err)
		}
	}
	return nil
}

// NumberInput supplies verified decoded number bytes and context to pure rules.
type NumberInput struct {
	Text    string
	Sources []model.Span
	Context IdentityContext
}

// Validate checks the frozen number byte bound and provenance before parsing.
func (i NumberInput) Validate() error {
	if err := i.Context.Validate(); err != nil {
		return fmt.Errorf("context: %w", err)
	}
	if !utf8.ValidString(i.Text) || int64(len(i.Text)) > i.Context.Limits.MaxBytes || len(i.Sources) == 0 || len(i.Sources) > i.Context.Limits.MaxItems {
		return fmt.Errorf("number requires bounded decoded text and provenance")
	}
	for n, span := range i.Sources {
		if err := span.Validate(); err != nil {
			return fmt.Errorf("number span %d: %w", n, err)
		}
	}
	return nil
}

// NumberMatchKind distinguishes absent, exact and ambiguous local number matches.
type NumberMatchKind string

const (
	// NumberNoMatch records no exact-number candidate.
	NumberNoMatch   NumberMatchKind = "no_match"
	// NumberExact records an exact-number match.
	NumberExact     NumberMatchKind = "exact"
	// NumberAmbiguous records unresolved exact-number candidates.
	NumberAmbiguous NumberMatchKind = "ambiguous"
)

// Valid reports membership in the closed contract vocabulary.
func (k NumberMatchKind) Valid() bool {
	return k == NumberNoMatch || k == NumberExact || k == NumberAmbiguous
}

// NumberMatch returns computed candidates for the caller responsible for staging.
type NumberMatch struct {
	Candidates []NumberCandidate
	Kind       NumberMatchKind
}

// Validate checks the match kind and each computed candidate source span.
func (m NumberMatch) Validate() error {
	if !m.Kind.Valid() {
		return fmt.Errorf("unknown number match kind")
	}
	for i, candidate := range m.Candidates {
		if err := candidate.Source.Validate(); err != nil {
			return fmt.Errorf("candidate %d provenance: %w", i, err)
		}
	}
	return nil
}

// NumberCandidate returns computed exact number and namespace with source provenance.
// The owning consumer bounds, validates and stages candidates before transport.
type NumberCandidate struct {
	Exact         string
	NormalizedKey string
	Namespace     string
	Source        model.Span
}

// RequestEnvelope projects a screened local manifest for transport.
type RequestEnvelope struct {
	Key      string
	Manifest model.Digest
}

// Validate checks the kind-specific ledger key and digest.
func (e RequestEnvelope) Validate() error {
	if err := model.ValidateEnvelope(e.Key, "request", e.Manifest); err != nil {
		return fmt.Errorf("request envelope: %w", err)
	}
	return nil
}

// MarshalJSON validates before serializing any projection.
func (e RequestEnvelope) MarshalJSON() ([]byte, error) {
	encoded, err := model.MarshalEnvelope(e.Key, "request", e.Manifest)
	if err != nil {
		return nil, fmt.Errorf("serialize request envelope: %w", err)
	}
	return encoded, nil
}

// DecoderEnvelope projects a screened local manifest for transport.
type DecoderEnvelope struct {
	Key      string
	Manifest model.Digest
}

// Validate checks the kind-specific ledger key and digest.
func (e DecoderEnvelope) Validate() error {
	if err := model.ValidateEnvelope(e.Key, "decoder", e.Manifest); err != nil {
		return fmt.Errorf("decoder envelope: %w", err)
	}
	return nil
}

// MarshalJSON validates before serializing any projection.
func (e DecoderEnvelope) MarshalJSON() ([]byte, error) {
	encoded, err := model.MarshalEnvelope(e.Key, "decoder", e.Manifest)
	if err != nil {
		return nil, fmt.Errorf("serialize decoder envelope: %w", err)
	}
	return encoded, nil
}

// ValidateOrigin rejects planners that move work to another window or reset budgets.
// Consumers call it against the originating request before staging any plan.
func (r RequestPlan) ValidateOrigin(window string, budgetHash model.Digest, budget model.ResourceBudget) error {
	if err := r.Validate(); err != nil {
		return fmt.Errorf("request plan: %w", err)
	}
	if r.WindowKey != window || r.BudgetHash != budgetHash || r.Budget != budget {
		return fmt.Errorf("request plan must retain originating window and frozen budget")
	}
	return nil
}

// RequestHeader is a computed worker-local header, screened by core before capture.
// Country planners receive no credentials and must never generate secret headers.
type RequestHeader struct {
	Name  string
	Value string
}

// DecodedField preserves native field values and their exact source spans locally.
type DecodedField struct {
	Name    string
	Value   string
	Sources []model.Span
}

// DecodedRelation retains computed native relation fields with source provenance.
type DecodedRelation struct {
	Kind    string
	Target  string
	Sources []model.Span
}

// DecodedObservation returns values to the sandbox component that stages manifests.
// Native external IDs, metadata, trees and relations remain inside Vietnam.
type DecodedObservation struct {
	SourceID   string
	ExternalID string
	VersionKey string
	Fields     []DecodedField
	SourceTree model.SourceTree
	Relations  []DecodedRelation
	Capture    model.CaptureRef
	Ordinal    int
}

// DecodedDiscoveryPage returns computed listing values without storage operations.
// The sandbox bounds, screens and stages this value before projecting ListingPageEnvelope.
type DecodedDiscoveryPage struct {
	PageKey      string
	Capture      model.CaptureRef
	Observations []DecodedObservation
	FollowUps    []model.FollowUpRequest
	Cursor       model.CursorPosition
	Findings     []model.Finding
}

// DecodedDetailResult returns computed detail values without storage operations.
// The sandbox bounds, screens and stages this value before projecting DetailEnvelope.
type DecodedDetailResult struct {
	Observation DecodedObservation
	Artifacts   []model.ArtifactInventoryEntry
	FollowUps   []model.FollowUpRequest
	Findings    []model.Finding
}
