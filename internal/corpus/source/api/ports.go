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

// Continuer optionally plans listing continuations from bounded decoded values.
// A Planner without this capability has no continuation through this port.
// Core still screens every resulting request before capture.
type Continuer interface {
	// NextPage preserves the originating window, policy and exact resource budget.
	NextPage(ContinuationRequest) (RequestPlan, error)
}

// FormField is one decoded form value, never a raw body or remote reference.
// Names use only ASCII letters, digits, underscore and dollar sign.
type FormField struct {
	Name  string
	Value string
}

// ContinuationRequest retains a local listing origin and captured form state.
// Mode retains the discovery cadence; Fields and Target share the input byte
// allowance. Field names must be unique and use the closed FormField grammar.
type ContinuationRequest struct {
	Origin        RequestPlan
	WindowKey     string
	PolicyHash    model.Digest
	BudgetHash    model.Digest
	Budget        model.ResourceBudget
	Mode          DiscoveryMode
	ParentCapture model.CaptureRef
	Target        string
	Fields        []FormField
}

// Validate checks origin equality, capture provenance and bounded decoded values.
func (r ContinuationRequest) Validate() error {
	if err := r.Origin.ValidateOrigin(r.WindowKey, r.BudgetHash, r.Budget); err != nil {
		return fmt.Errorf("continuation budget: %w", err)
	}
	if r.PolicyHash != r.Origin.PolicyHash || r.Origin.Purpose != model.PurposeListing || !r.Mode.Valid() {
		return fmt.Errorf("invalid continuation policy, purpose or mode")
	}
	if err := r.ParentCapture.Validate(); err != nil {
		return fmt.Errorf("continuation capture: %w", err)
	}
	if r.ParentCapture.Country != r.Origin.Country || r.ParentCapture.SourceID != r.Origin.SourceID {
		return fmt.Errorf("continuation capture crosses country or source")
	}
	bound := identityInputBound{items: r.Budget.MaxItems, bytes: r.Budget.MaxInputBytes}
	if err := bound.collection(len(r.Fields)); err != nil {
		return err
	}
	if err := bound.strings(r.Target); err != nil {
		return err
	}
	if !formKey(r.Target) {
		return fmt.Errorf("invalid continuation target")
	}
	seen := make(map[string]bool, len(r.Fields))
	for _, field := range r.Fields {
		if err := bound.strings(field.Name, field.Value); err != nil {
			return err
		}
		if !formKey(field.Name) || seen[field.Name] {
			return fmt.Errorf("invalid or duplicate decoded form key")
		}
		seen[field.Name] = true
	}
	return nil
}

func formKey(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$') {
			return false
		}
	}
	return true
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

// validateResponseKind bounds trusted parser dispatch metadata. The empty value
// preserves sources with one detail response; country planners choose defaults.
func validateResponseKind(kind string) error {
	switch kind {
	case "", "metadata", "files", "tree", "diagram":
		return nil
	default:
		return fmt.Errorf("unknown expected response kind")
	}
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
	// ExpectedResponseKind is trusted planner dispatch metadata, never response data.
	ExpectedResponseKind string
	PolicyHash    model.Digest
	BudgetHash    model.Digest
	Budget        model.ResourceBudget
}

// Validate checks local request origin, method, locator, policy and byte/item bounds.
func (r RequestPlan) Validate() error {
	if err := validateResponseKind(r.ExpectedResponseKind); err != nil {
		return err
	}
	if r.ExpectedResponseKind != "" && r.Purpose != model.PurposeDetail {
		return fmt.Errorf("response kind requires detail purpose")
	}
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
	for _, value := range []string{r.URL, r.Cursor, r.ExpectedResponseKind} {
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
	// ExpectedResponseKind binds decoding to the originating trusted request plan.
	ExpectedResponseKind string
	PolicyHash      model.Digest
	WindowKey       string
	BudgetHash      model.Digest
	Budget          model.ResourceBudget
}

// Validate requires captured listing provenance and the original window/budget.
func (r DetailRequest) Validate() error {
	if err := validateResponseKind(r.ExpectedResponseKind); err != nil {
		return err
	}
	if int64(len(r.ExpectedResponseKind)) > r.Budget.MaxInputBytes {
		return fmt.Errorf("expected response kind exceeds frozen byte bound")
	}
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
	bound := identityInputBound{items: c.Limits.MaxItems, bytes: c.Limits.MaxBytes}
	return c.validate(&bound)
}

func (c IdentityContext) validate(bound *identityInputBound) error {
	if err := bound.collection(len(c.Fields)); err != nil {
		return err
	}
	if err := bound.strings(c.Title, c.Issuer, c.Type); err != nil {
		return err
	}
	if len(c.Fields) == 0 {
		return fmt.Errorf("context requires field provenance")
	}
	for i, span := range c.Fields {
		if err := bound.span(span); err != nil {
			return err
		}
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

// Validate checks one frozen allowance across number text, context and provenance.
func (i NumberInput) Validate() error {
	if err := i.Context.Limits.Validate(); err != nil {
		return fmt.Errorf("context limits: %w", err)
	}
	bound := identityInputBound{items: i.Context.Limits.MaxItems, bytes: i.Context.Limits.MaxBytes}
	if err := bound.collection(len(i.Sources)); err != nil {
		return err
	}
	if err := bound.strings(i.Text); err != nil {
		return err
	}
	if err := i.Context.validate(&bound); err != nil {
		return fmt.Errorf("context: %w", err)
	}
	if len(i.Sources) == 0 {
		return fmt.Errorf("number requires source provenance")
	}
	for n, span := range i.Sources {
		if err := bound.span(span); err != nil {
			return err
		}
		if err := span.Validate(); err != nil {
			return fmt.Errorf("number span %d: %w", n, err)
		}
	}
	return nil
}

// identityInputBound uses the decoded-rule subtraction accounting without
// allocating combined strings or provenance collections.
type identityInputBound struct {
	items int
	bytes int64
}

func (b *identityInputBound) collection(count int) error {
	if count > b.items {
		return fmt.Errorf("identity input exceeds frozen item bound")
	}
	b.items -= count
	return nil
}

func (b *identityInputBound) strings(values ...string) error {
	for _, value := range values {
		if int64(len(value)) > b.bytes {
			return fmt.Errorf("identity input exceeds frozen byte bound")
		}
		b.bytes -= int64(len(value))
		if !utf8.ValidString(value) {
			return fmt.Errorf("identity input requires valid UTF-8")
		}
	}
	return nil
}

func (b *identityInputBound) span(s model.Span) error {
	return b.strings(string(s.ArtifactHash), s.BlockID, string(s.Space), s.Object, s.Member, s.ReasonCode)
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
	// Limits freezes aggregate decoded strings and nested collection entries.
	Limits model.RuleLimits
}

// DecodedRelation retains computed native relation fields with source provenance.
type DecodedRelation struct {
	Kind    string
	Target  string
	Sources []model.Span
	// Limits freezes aggregate decoded strings and nested collection entries.
	Limits model.RuleLimits
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
	// Limits freezes aggregate decoded strings and nested collection entries.
	Limits model.RuleLimits
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
	// Limits freezes aggregate decoded strings and nested collection entries.
	Limits model.RuleLimits
	// Continuation is nil outside WebForms pagination; it contains no request body.
	Continuation *DecodedContinuation
}

// DecodedDetailResult returns computed detail values without storage operations.
// The sandbox bounds, screens and stages this value before projecting DetailEnvelope.
type DecodedDetailResult struct {
	Observation DecodedObservation
	Artifacts   []model.ArtifactInventoryEntry
	FollowUps   []model.FollowUpRequest
	Findings    []model.Finding
	// Limits freezes aggregate decoded strings and nested collection entries.
	Limits model.RuleLimits
}

// DecodedContinuation carries verified WebForms state to Continuer. It is absent
// for other sources and shares the page allowance, never a fresh allowance.
type DecodedContinuation struct {
	Target string
	Fields []FormField
}

// Validate uses the same field grammar, uniqueness and bounds as ContinuationRequest.
func (c DecodedContinuation) Validate(limits model.RuleLimits) error {
	if err := limits.Validate(); err != nil {
		return err
	}
	bound := identityInputBound{items: limits.MaxItems, bytes: limits.MaxBytes}
	return c.validate(&bound)
}

func (c DecodedContinuation) validate(b *identityInputBound) error {
	if err := b.collection(len(c.Fields)); err != nil {
		return err
	}
	if err := b.strings(c.Target); err != nil {
		return err
	}
	if !formKey(c.Target) {
		return fmt.Errorf("invalid continuation target")
	}
	seen := make(map[string]bool, len(c.Fields))
	for _, field := range c.Fields {
		if err := b.strings(field.Name, field.Value); err != nil {
			return err
		}
		if !formKey(field.Name) || seen[field.Name] {
			return fmt.Errorf("invalid or duplicate decoded form key")
		}
		seen[field.Name] = true
	}
	return nil
}

func sourceBound(limits model.RuleLimits) (identityInputBound, error) {
	if err := limits.Validate(); err != nil {
		return identityInputBound{}, err
	}
	return identityInputBound{items: limits.MaxItems, bytes: limits.MaxBytes}, nil
}

// Validate checks standalone field bounds and captured provenance.
func (f DecodedField) Validate() error {
	b, err := sourceBound(f.Limits)
	if err != nil {
		return err
	}
	return f.validate(&b)
}

func (f DecodedField) validate(b *identityInputBound) error {
	if err := b.collection(len(f.Sources)); err != nil {
		return err
	}
	if err := b.strings(f.Name, f.Value); err != nil {
		return err
	}
	if f.Name == "" || len(f.Sources) == 0 {
		return fmt.Errorf("decoded field requires name and provenance")
	}
	return b.spans(f.Sources)
}

// Validate checks standalone relation bounds and captured provenance.
func (r DecodedRelation) Validate() error {
	b, err := sourceBound(r.Limits)
	if err != nil {
		return err
	}
	return r.validate(&b)
}

func (r DecodedRelation) validate(b *identityInputBound) error {
	if err := b.collection(len(r.Sources)); err != nil {
		return err
	}
	if err := b.strings(r.Kind, r.Target); err != nil {
		return err
	}
	if r.Kind == "" || r.Target == "" || len(r.Sources) == 0 {
		return fmt.Errorf("decoded relation requires kind, target and provenance")
	}
	return b.spans(r.Sources)
}

func (b *identityInputBound) spans(spans []model.Span) error {
	for _, span := range spans {
		if err := b.span(span); err != nil {
			return err
		}
		if err := span.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (b *identityInputBound) asset(a model.AssetRef) error {
	if err := b.strings(a.Path, string(a.SHA256)); err != nil {
		return err
	}
	return a.Validate()
}

func (b *identityInputBound) capture(c model.CaptureRef) error {
	if err := b.strings(c.Country, c.SourceID, c.CaptureKey, c.MediaType, string(c.RequestHash), string(c.ResponseHash)); err != nil {
		return err
	}
	if err := b.asset(c.Asset); err != nil {
		return err
	}
	if err := b.asset(c.TransportReceipt); err != nil {
		return err
	}
	return c.Validate()
}

// Validate applies one frozen allowance across observation values and provenance.
// Nested Limits do not grant additional items or bytes.
func (o DecodedObservation) Validate() error {
	b, err := sourceBound(o.Limits)
	if err != nil {
		return err
	}
	return o.validate(&b)
}

func (o DecodedObservation) validate(b *identityInputBound) error {
	for _, count := range []int{len(o.Fields), len(o.Relations), len(o.SourceTree.Nodes)} {
		if err := b.collection(count); err != nil {
			return err
		}
	}
	if err := b.strings(o.SourceID, o.ExternalID, o.VersionKey); err != nil {
		return err
	}
	if o.SourceID == "" || o.ExternalID == "" || o.VersionKey == "" || o.Ordinal < 0 || o.SourceID != o.Capture.SourceID {
		return fmt.Errorf("invalid decoded observation identity")
	}
	if err := b.capture(o.Capture); err != nil {
		return err
	}
	for _, f := range o.Fields {
		if err := f.validate(b); err != nil {
			return err
		}
		if err := observationSpans(f.Sources, o.Capture); err != nil {
			return err
		}
	}
	for _, r := range o.Relations {
		if err := r.validate(b); err != nil {
			return err
		}
		if err := observationSpans(r.Sources, o.Capture); err != nil {
			return err
		}
	}
	for _, node := range o.SourceTree.Nodes {
		for _, count := range []int{len(node.BlockIDs), len(node.Sources)} {
			if err := b.collection(count); err != nil {
				return err
			}
		}
		if err := b.strings(node.ID, node.ParentID, node.Label); err != nil {
			return err
		}
		if node.ID == "" || len(node.Sources) == 0 {
			return fmt.Errorf("source node requires identity and provenance")
		}
		for _, id := range node.BlockIDs {
			if err := b.strings(id); err != nil {
				return err
			}
		}
		if err := b.spans(node.Sources); err != nil {
			return err
		}
		if err := observationSpans(node.Sources, o.Capture); err != nil {
			return err
		}
	}
	return nil
}

func observationSpans(spans []model.Span, capture model.CaptureRef) error {
	for _, s := range spans {
		if s.ArtifactHash != capture.Asset.SHA256 || s.Space != model.OriginalBytes || s.Range != nil && s.Range.End > capture.Asset.ByteSize {
			return fmt.Errorf("decoded provenance differs from mounted capture")
		}
	}
	return nil
}

func (b *identityInputBound) followUps(values []model.FollowUpRequest, capture model.CaptureRef, window string) error {
	if err := b.collection(len(values)); err != nil {
		return err
	}
	for _, f := range values {
		if err := b.strings(f.RequestKey, f.WindowKey, f.ParentCapture, string(f.Purpose), f.URL, string(f.PolicyHash), string(f.BudgetHash)); err != nil {
			return err
		}
		if err := f.Validate(); err != nil {
			return err
		}
		if f.ParentCapture != capture.CaptureKey || window != "" && f.WindowKey != window {
			return fmt.Errorf("decoded follow-up differs from origin")
		}
	}
	return nil
}

func (b *identityInputBound) findings(values []model.Finding) error {
	if err := b.collection(len(values)); err != nil {
		return err
	}
	for _, f := range values {
		for _, count := range []int{len(f.Spans), len(f.Evidence)} {
			if err := b.collection(count); err != nil {
				return err
			}
		}
		if err := b.strings(f.ReasonCode, string(f.Severity), string(f.Scope), f.ScopeKey); err != nil {
			return err
		}
		if err := b.spans(f.Spans); err != nil {
			return err
		}
		for _, a := range f.Evidence {
			if err := b.asset(a); err != nil {
				return err
			}
		}
		if err := f.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Validate applies one frozen allowance across the page, continuation and all
// nested values. Length checks precede traversal and each UTF-8 scan.
func (p DecodedDiscoveryPage) Validate() error {
	b, err := sourceBound(p.Limits)
	if err != nil {
		return err
	}
	if err := b.collection(len(p.Observations)); err != nil {
		return err
	}
	if err := b.strings(p.PageKey, p.Cursor.WindowKey, p.Cursor.SliceKey, p.Cursor.InitialCursor, p.Cursor.CommittedCursor, p.Cursor.NextCursor); err != nil {
		return err
	}
	if err := model.ValidateKey(p.PageKey); err != nil {
		return err
	}
	if err := b.capture(p.Capture); err != nil {
		return err
	}
	if err := p.Cursor.Validate(); err != nil {
		return err
	}
	for _, o := range p.Observations {
		if err := o.validate(&b); err != nil {
			return err
		}
		if o.Capture != p.Capture {
			return fmt.Errorf("decoded page observation differs from capture")
		}
	}
	if p.Continuation != nil {
		if err := p.Continuation.validate(&b); err != nil {
			return err
		}
	}
	if err := b.followUps(p.FollowUps, p.Capture, p.Cursor.WindowKey); err != nil {
		return err
	}
	return b.findings(p.Findings)
}

// Validate applies one frozen allowance across detail metadata, inventory,
// follow-ups, findings and every provenance string.
func (r DecodedDetailResult) Validate() error {
	b, err := sourceBound(r.Limits)
	if err != nil {
		return err
	}
	if err := b.collection(len(r.Artifacts)); err != nil {
		return err
	}
	if err := r.Observation.validate(&b); err != nil {
		return err
	}
	for _, a := range r.Artifacts {
		if err := b.strings(a.ArtifactKey, a.Role, a.URL, a.DeclaredFilename, a.Format); err != nil {
			return err
		}
		if a.Capture != nil {
			if err := b.capture(*a.Capture); err != nil {
				return err
			}
			if a.Capture.Country != r.Observation.Capture.Country || a.Capture.SourceID != r.Observation.SourceID {
				return fmt.Errorf("artifact crosses country or source")
			}
		}
		if err := a.Validate(); err != nil {
			return err
		}
	}
	if err := b.followUps(r.FollowUps, r.Observation.Capture, ""); err != nil {
		return err
	}
	return b.findings(r.Findings)
}
