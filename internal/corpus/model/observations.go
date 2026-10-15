package model

import (
	"fmt"
	"net/url"
	"time"
	"unicode/utf8"
)

// ResourceBudget is a neutral envelope bound; follow-ups consume the original
// window's remaining budget, never a fresh allowance.
type ResourceBudget struct {
	MaxItems      int
	MaxInputBytes int64
	MaxPages      int
	MaxElapsed    time.Duration
	MaxConcurrent int
}

// Validate requires a positive bound for every resource dimension.
func (b ResourceBudget) Validate() error {
	if b.MaxItems <= 0 || b.MaxInputBytes <= 0 || b.MaxPages <= 0 || b.MaxElapsed <= 0 || b.MaxConcurrent <= 0 {
		return fmt.Errorf("every budget dimension must be positive")
	}
	return nil
}

// RequestPurpose names the supported source request purposes.
type RequestPurpose string

const (
	// PurposeListing requests a source listing.
	PurposeListing RequestPurpose = "listing"
	// PurposeDetail requests document metadata.
	PurposeDetail RequestPurpose = "detail"
	// PurposeArtifact requests an original artifact.
	PurposeArtifact RequestPurpose = "artifact"
	// PurposeNumber requests exact-number discovery.
	PurposeNumber RequestPurpose = "number"
)

// Valid reports membership in the closed contract vocabulary.
func (p RequestPurpose) Valid() bool {
	return p == PurposeListing || p == PurposeDetail || p == PurposeArtifact || p == PurposeNumber
}

// FollowUpRequest is a worker-local plan with a screened locator.
// Only FollowUpEnvelope may cross the scheduling boundary.
type FollowUpRequest struct {
	RequestKey    string
	WindowKey     string
	ParentCapture string
	Purpose       RequestPurpose
	URL           string
	Ordinal       int
	PolicyHash    Digest
	BudgetHash    Digest
	Budget        ResourceBudget
}

// Validate checks local origin, purpose, locator and frozen policy/budget identity.
func (r FollowUpRequest) Validate() error {
	if r.RequestKey == "" || r.WindowKey == "" || r.ParentCapture == "" || !r.Purpose.Valid() || r.Ordinal < 0 {
		return fmt.Errorf("invalid follow-up identity")
	}
	if err := ValidatePublicURL(r.URL); err != nil {
		return fmt.Errorf("public URL: %w", err)
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

// ValidatePublicURL checks syntax only, without DNS or network access. Core
// policy must also approve origins, redirects and URL fields for personal data.
func ValidatePublicURL(locator string) error {
	u, err := url.Parse(locator)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("public URL requires HTTP(S), host and no credentials or fragment")
	}
	return nil
}

// CursorPosition proposes progress only for a completely accounted window slice.
// Incomplete pages may retain facts but cannot advance CommittedCursor.
type CursorPosition struct {
	WindowKey       string
	SliceKey        string
	InitialCursor   string
	CommittedCursor string
	NextCursor      string
	Complete        bool
	Ordinal         int
}

// Validate rejects progress advancement for an incomplete slice.
func (c CursorPosition) Validate() error {
	if c.WindowKey == "" || c.SliceKey == "" || c.Ordinal < 0 {
		return fmt.Errorf("cursor requires window, slice and nonnegative ordinal")
	}
	if !c.Complete && c.NextCursor != c.CommittedCursor {
		return fmt.Errorf("incomplete coverage cannot advance cursor")
	}
	return nil
}

// Observation is a screened local manifest value, including native external IDs.
// Only ObservationEnvelope enters scheduling; native IDs never become ledger keys.
type Observation struct {
	SourceID        string
	ExternalID      string
	VersionKey      string
	ObservationHash Digest
	MetadataHash    Digest
	SourceTreeHash  Digest
	RelationsHash   Digest
	Capture         CaptureRef
	Ordinal         int
	FieldSpans      []Span
}

// Validate checks local source/capture association, hashes, spans and ordinal.
func (o Observation) Validate() error {
	if o.SourceID == "" || o.ExternalID == "" || o.VersionKey == "" || o.Ordinal < 0 || o.SourceID != o.Capture.SourceID {
		return fmt.Errorf("invalid observation identity")
	}
	for name, d := range map[string]Digest{"observation hash": o.ObservationHash, "metadata hash": o.MetadataHash, "source tree hash": o.SourceTreeHash, "relations hash": o.RelationsHash} {
		if err := d.Validate(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	for _, s := range o.FieldSpans {
		if err := s.Validate(); err != nil {
			return fmt.Errorf("source span: %w", err)
		}
	}
	if err := o.Capture.Validate(); err != nil {
		return fmt.Errorf("o.Capture: %w", err)
	}
	return nil
}

// MaxArtifactValueBytes bounds all declared artifact strings together.
const MaxArtifactValueBytes = 64 * 1024

// ArtifactFormatValid checks admitted conversion formats and preserved unsupported
// presentations. Empty remains valid for existing callers with no declaration.
func ArtifactFormatValid(value string) bool {
	switch value {
	case "", "unknown", "doc", "docx", "html", "pdf", "png", "jpeg", "tiff", "xlsx", "ppt", "pptx", "odp":
		return true
	default:
		return false
	}
}

// ArtifactInventoryEntry is a local expected-artifact record with its screened locator.
type ArtifactInventoryEntry struct {
	ArtifactKey string
	Role        string
	URL         string
	Ordinal     int
	Required    bool
	Capture     *CaptureRef
	// DeclaredFilename is source text, never a path to open. Empty means absent.
	DeclaredFilename string
	// Format is a declared hint, never sniffed proof. Empty means absent.
	Format string
}

// Validate checks local artifact identity, screened locator and optional capture.
func (a ArtifactInventoryEntry) Validate() error {
	remaining := int64(MaxArtifactValueBytes)
	for _, value := range []string{a.ArtifactKey, a.Role, a.URL, a.DeclaredFilename, a.Format} {
		if int64(len(value)) > remaining {
			return fmt.Errorf("artifact values exceed byte bound")
		}
		remaining -= int64(len(value))
		if !utf8.ValidString(value) {
			return fmt.Errorf("artifact values require UTF-8")
		}
	}
	if err := ValidateKey(a.ArtifactKey); err != nil {
		return fmt.Errorf("artifact key: %w", err)
	}
	if !ArtifactFormatValid(a.Format) {
		return fmt.Errorf("invalid declared artifact format")
	}
	if a.ArtifactKey == "" || a.Role == "" || a.Ordinal < 0 {
		return fmt.Errorf("invalid artifact inventory identity")
	}
	if err := ValidatePublicURL(a.URL); err != nil {
		return fmt.Errorf("public URL: %w", err)
	}
	if a.Capture != nil {
		if err := a.Capture.Validate(); err != nil {
			return fmt.Errorf("a.Capture: %w", err)
		}
		return nil
	}
	return nil
}

// ListingPageResult is a local listing manifest with coverage and staged observations.
type ListingPageResult struct {
	PageKey      string
	Capture      CaptureRef
	Observations []Observation
	FollowUps    []FollowUpRequest
	Cursor       CursorPosition
	Findings     []Finding
}

// DetailResult is a local detail manifest with artifacts and follow-up plans.
type DetailResult struct {
	Observation Observation
	Artifacts   []ArtifactInventoryEntry
	FollowUps   []FollowUpRequest
	Findings    []Finding
}

// Validate checks observations, coverage and follow-up window associations.
func (p ListingPageResult) Validate() error {
	if err := ValidateKey(p.PageKey); err != nil {
		return fmt.Errorf("ledger key: %w", err)
	}
	if err := p.Capture.Validate(); err != nil {
		return fmt.Errorf("Capture: %w", err)
	}
	if err := p.Cursor.Validate(); err != nil {
		return fmt.Errorf("Cursor: %w", err)
	}
	for _, o := range p.Observations {
		if err := o.Validate(); err != nil {
			return fmt.Errorf("observation: %w", err)
		}
		if o.SourceID != p.Capture.SourceID || o.Capture.Country != p.Capture.Country {
			return fmt.Errorf("listing observation crosses country or source")
		}
	}
	for _, f := range p.FollowUps {
		if err := f.Validate(); err != nil {
			return fmt.Errorf("finding: %w", err)
		}
		if f.WindowKey != p.Cursor.WindowKey {
			return fmt.Errorf("follow-up crosses source window")
		}
	}
	if err := validateFindings(p.Findings); err != nil {
		return fmt.Errorf("findings: %w", err)
	}
	return nil
}

// Validate checks local observations, artifact associations and follow-ups.
func (r DetailResult) Validate() error {
	if err := r.Observation.Validate(); err != nil {
		return fmt.Errorf("Observation: %w", err)
	}
	for _, a := range r.Artifacts {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("asset reference: %w", err)
		}
		if a.Capture != nil && (a.Capture.SourceID != r.Observation.SourceID || a.Capture.Country != r.Observation.Capture.Country) {
			return fmt.Errorf("artifact crosses country or source")
		}
	}
	for _, f := range r.FollowUps {
		if err := f.Validate(); err != nil {
			return fmt.Errorf("finding: %w", err)
		}
	}
	if err := validateFindings(r.Findings); err != nil {
		return fmt.Errorf("findings: %w", err)
	}
	return nil
}

// ValidateOrigin rejects follow-ups that change the window or reset its budget.
// Consumers compare against the frozen ledger manifest, not follow-up assertions.
func (r FollowUpRequest) ValidateOrigin(window string, budgetHash Digest, budget ResourceBudget) error {
	if err := r.Validate(); err != nil {
		return fmt.Errorf("follow-up: %w", err)
	}
	if r.WindowKey != window || r.BudgetHash != budgetHash || r.Budget != budget {
		return fmt.Errorf("follow-up must retain originating window and frozen budget")
	}
	return nil
}
