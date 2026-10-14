package model

import (
	"fmt"
	"net/url"
	"time"
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

func (b ResourceBudget) Validate() error {
	if b.MaxItems <= 0 || b.MaxInputBytes <= 0 || b.MaxPages <= 0 || b.MaxElapsed <= 0 || b.MaxConcurrent <= 0 {
		return fmt.Errorf("every budget dimension must be positive")
	}
	return nil
}

type RequestPurpose string

const (
	PurposeListing  RequestPurpose = "listing"
	PurposeDetail   RequestPurpose = "detail"
	PurposeArtifact RequestPurpose = "artifact"
	PurposeNumber   RequestPurpose = "number"
)

func (p RequestPurpose) Valid() bool {
	return p == PurposeListing || p == PurposeDetail || p == PurposeArtifact || p == PurposeNumber
}

// FollowUpRequest carries an approved locator, not content or arbitrary headers.
type FollowUpRequest struct {
	RequestKey    string
	WindowKey     string
	ParentCapture string
	Purpose       RequestPurpose
	URL           string
	Ordinal       int
	PolicyHash    Digest
	Budget        ResourceBudget
}

func (r FollowUpRequest) Validate() error {
	if r.RequestKey == "" || r.WindowKey == "" || r.ParentCapture == "" || !r.Purpose.Valid() || r.Ordinal < 0 {
		return fmt.Errorf("invalid follow-up identity")
	}
	if err := ValidatePublicURL(r.URL); err != nil {
		return err
	}
	if err := r.PolicyHash.Validate(); err != nil {
		return err
	}
	return r.Budget.Validate()
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

func (c CursorPosition) Validate() error {
	if c.WindowKey == "" || c.SliceKey == "" || c.Ordinal < 0 {
		return fmt.Errorf("cursor requires window, slice and nonnegative ordinal")
	}
	if !c.Complete && c.NextCursor != c.CommittedCursor {
		return fmt.Errorf("incomplete coverage cannot advance cursor")
	}
	return nil
}

// Observation stores captured metadata, trees and relations by hash, never
// titles, field text, raw bytes or personal data in the scheduling envelope.
type Observation struct {
	SourceID         string
	ExternalID       string
	VersionKey       string
	ObservationHash  Digest
	MetadataHash     Digest
	SourceTreeHash   Digest
	RelationsHash    Digest
	Capture          CaptureRef
	Ordinal          int
	FieldSpans       []Span
}

func (o Observation) Validate() error {
	if o.SourceID == "" || o.ExternalID == "" || o.VersionKey == "" || o.Ordinal < 0 || o.SourceID != o.Capture.SourceID {
		return fmt.Errorf("invalid observation identity")
	}
	for _, d := range []Digest{o.ObservationHash, o.MetadataHash, o.SourceTreeHash, o.RelationsHash} {
		if err := d.Validate(); err != nil {
			return err
		}
	}
	for _, s := range o.FieldSpans {
		if err := s.Validate(); err != nil {
			return err
		}
	}
	return o.Capture.Validate()
}

type ArtifactInventoryEntry struct {
	ArtifactKey string
	Role        string
	URL         string
	Ordinal     int
	Required    bool
	Capture     *CaptureRef
}

func (a ArtifactInventoryEntry) Validate() error {
	if a.ArtifactKey == "" || a.Role == "" || a.Ordinal < 0 {
		return fmt.Errorf("invalid artifact inventory identity")
	}
	if err := ValidatePublicURL(a.URL); err != nil {
		return err
	}
	if a.Capture != nil {
		return a.Capture.Validate()
	}
	return nil
}

type ListingPageResult struct {
	PageKey      string
	Capture      CaptureRef
	Observations []Observation
	FollowUps    []FollowUpRequest
	Cursor       CursorPosition
	Findings     []Finding
}

type DetailResult struct {
	Observation Observation
	Artifacts   []ArtifactInventoryEntry
	FollowUps   []FollowUpRequest
	Findings    []Finding
}

func (p ListingPageResult) Validate() error {
	if err := ValidateKey(p.PageKey); err != nil {
		return err
	}
	if err := p.Capture.Validate(); err != nil {
		return err
	}
	if err := p.Cursor.Validate(); err != nil {
		return err
	}
	for _, o := range p.Observations {
		if err := o.Validate(); err != nil {
			return err
		}
		if o.SourceID != p.Capture.SourceID || o.Capture.Country != p.Capture.Country {
			return fmt.Errorf("listing observation crosses country or source")
		}
	}
	for _, f := range p.FollowUps {
		if err := f.Validate(); err != nil {
			return err
		}
		if f.WindowKey != p.Cursor.WindowKey {
			return fmt.Errorf("follow-up crosses source window")
		}
	}
	return validateFindings(p.Findings)
}

func (r DetailResult) Validate() error {
	if err := r.Observation.Validate(); err != nil {
		return err
	}
	for _, a := range r.Artifacts {
		if err := a.Validate(); err != nil {
			return err
		}
		if a.Capture != nil && (a.Capture.SourceID != r.Observation.SourceID || a.Capture.Country != r.Observation.Capture.Country) {
			return fmt.Errorf("artifact crosses country or source")
		}
	}
	for _, f := range r.FollowUps {
		if err := f.Validate(); err != nil {
			return err
		}
	}
	return validateFindings(r.Findings)
}
