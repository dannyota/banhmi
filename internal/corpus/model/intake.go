package model

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ValidateKey checks portable opaque ledger-key syntax, without resolving it.
// Producers must use opaque keys, never titles, URLs or personal identifiers.
func ValidateKey(key string) error {
	if len(key) == 0 || len(key) > 128 || !((key[0] >= 'a' && key[0] <= 'z') || (key[0] >= '0' && key[0] <= '9')) {
		return fmt.Errorf("key requires 1 to 128 lowercase identifier characters")
	}
	for _, c := range key {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return fmt.Errorf("key requires lowercase identifier characters")
		}
	}
	return nil
}

// AcceptanceState records candidate admission and build eligibility.
type AcceptanceState string

const (
	Candidate        AcceptanceState = "candidate"
	Quarantined      AcceptanceState = "quarantined"
	Accepted         AcceptanceState = "accepted"
	AcceptedWithGaps AcceptanceState = "accepted_with_gaps"
	Excluded         AcceptanceState = "excluded"
)

// Valid reports membership in the closed contract vocabulary.
func (s AcceptanceState) Valid() bool {
	return s == Candidate || s == Quarantined || s == Accepted || s == AcceptedWithGaps || s == Excluded
}

// BuildEligible reports whether this state permits release-build membership.
func (s AcceptanceState) BuildEligible() bool {
	return s == Accepted || s == AcceptedWithGaps
}

// DecisionKind names a supported review action.
type DecisionKind string

const (
	DecisionRetryConverter  DecisionKind = "retry_converter"
	DecisionAcceptOCR       DecisionKind = "accept_non_binding_ocr"
	DecisionAcceptGaps      DecisionKind = "accept_with_named_gaps"
	DecisionExclude         DecisionKind = "exclude_coverage_gap"
	DecisionAlternateSource DecisionKind = "attach_alternate_official_source"
)

// Valid reports membership in the closed contract vocabulary.
func (k DecisionKind) Valid() bool {
	return k == DecisionRetryConverter || k == DecisionAcceptOCR || k == DecisionAcceptGaps || k == DecisionExclude || k == DecisionAlternateSource
}

// GapKind names scope, including expected missing items without database rows.
type GapKind string

const (
	GapSourceWindow     GapKind = "source_window"
	GapArtifact         GapKind = "artifact"
	GapDocumentRevision GapKind = "document_revision"
	GapProvision        GapKind = "provision"
	GapPart             GapKind = "part"
)

// Valid reports membership in the closed contract vocabulary.
func (k GapKind) Valid() bool {
	return k == GapSourceWindow || k == GapArtifact || k == GapDocumentRevision || k == GapProvision || k == GapPart
}

// GapRef binds a machine reason and report hash to an exact ledger scope.
type GapRef struct {
	Kind       GapKind
	Key        string
	ReasonCode string
	ReportHash Digest
}

// Validate requires a known gap scope, key, reason and pinned report hash.
func (g GapRef) Validate() error {
	if !g.Kind.Valid() || g.Key == "" || g.ReasonCode == "" {
		return fmt.Errorf("gap requires a known scope, key and reason code")
	}
	if err := g.ReportHash.Validate(); err != nil {
		return fmt.Errorf("g.ReportHash: %w", err)
	}
	return nil
}

// CaptureRef is a local manifest value with immutable content and transport metadata.
// Only CaptureEnvelope crosses transport; storage paths remain local.
type CaptureRef struct {
	Country          string
	SourceID         string
	CaptureKey       string
	Asset            AssetRef
	MediaType        string
	RequestHash      Digest
	ResponseHash     Digest
	TransportReceipt AssetRef
}

// Validate checks local capture identity, content hashes and transport receipt.
func (c CaptureRef) Validate() error {
	if c.Country == "" || c.SourceID == "" || c.CaptureKey == "" || c.MediaType == "" {
		return fmt.Errorf("capture identity and media type are required")
	}
	if err := c.Asset.Validate(); err != nil {
		return fmt.Errorf("Asset: %w", err)
	}
	if err := c.RequestHash.Validate(); err != nil {
		return fmt.Errorf("RequestHash: %w", err)
	}
	if err := c.ResponseHash.Validate(); err != nil {
		return fmt.Errorf("ResponseHash: %w", err)
	}
	if err := c.TransportReceipt.Validate(); err != nil {
		return fmt.Errorf("c.TransportReceipt: %w", err)
	}
	return nil
}

// ResidencyClass names a screening outcome for local content.
type ResidencyClass string

const (
	ResidencyPublic       ResidencyClass = "public_non_personal"
	ResidencyPersonal     ResidencyClass = "personal_data"
	ResidencyUnclassified ResidencyClass = "unclassified"
)

// Valid reports membership in the closed contract vocabulary.
func (c ResidencyClass) Valid() bool {
	return c == ResidencyPublic || c == ResidencyPersonal || c == ResidencyUnclassified
}

// ResidencyAdmission is a local screening receipt without screened text.
// Only classified public, non-personal artifacts may enter external processing.
// ResidencyEnvelope carries the scheduling projection.
type ResidencyAdmission struct {
	ArtifactHash Digest
	PolicyHash   Digest
	Class        ResidencyClass
	ReasonCode   string
	Report       AssetRef
}

// Admitted validates the Admitted contract.
func (a ResidencyAdmission) Admitted() bool {
	return a.Class == ResidencyPublic
}

// Validate checks classification, reason, artifact/policy hashes and report.
func (a ResidencyAdmission) Validate() error {
	if !a.Class.Valid() || a.ReasonCode == "" {
		return fmt.Errorf("residency requires classification and reason")
	}
	for name, d := range map[string]Digest{"artifact hash": a.ArtifactHash, "policy hash": a.PolicyHash} {
		if err := d.Validate(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if err := a.Report.Validate(); err != nil {
		return fmt.Errorf("a.Report: %w", err)
	}
	return nil
}

// ValidateEnvelopeKey requires a ledger-issued kind prefix and 128-bit hex key.
// Syntax cannot prove issuance; the receiving ledger must resolve the key.
// No source identifier, cursor, URL, path or personal identifier is a ledger key.
func ValidateEnvelopeKey(key, kind string) error {
	switch kind {
	case "capture", "observation", "request", "page", "detail", "conversion", "quality", "residency", "execution", "result", "decoder", "convert", "screen":
	default:
		return fmt.Errorf("unknown envelope key kind")
	}
	prefix := kind + "-"
	if !strings.HasPrefix(key, prefix) || len(key) != len(prefix)+32 {
		return fmt.Errorf("envelope key requires kind prefix and 32 lowercase hexadecimal characters")
	}
	for _, c := range key[len(prefix):] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return fmt.Errorf("envelope key requires lowercase hexadecimal characters")
		}
	}
	return nil
}

// ValidateEnvelope validates a transport projection without resolving its manifest.
// Manifest bytes, screening and ledger issuance are verified in the owning sandbox.
func ValidateEnvelope(key, kind string, manifest Digest) error {
	if err := ValidateEnvelopeKey(key, kind); err != nil {
		return fmt.Errorf("ledger key: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("manifest digest: %w", err)
	}
	return nil
}

// MarshalEnvelope validates before serialization, so error output contains no input.
func MarshalEnvelope(key, kind string, manifest Digest) ([]byte, error) {
	if err := ValidateEnvelope(key, kind, manifest); err != nil {
		return nil, fmt.Errorf("serialize envelope: %w", err)
	}
	return json.Marshal(struct {
		Key      string
		Manifest Digest
	}{key, manifest})
}

// CaptureEnvelope projects a screened capture manifest into transport.
// The local manifest retains all text, paths, URLs, cursors and capture metadata.
type CaptureEnvelope struct {
	Key      string
	Manifest Digest
}

// Validate checks the ledger key grammar and pinned manifest digest.
func (e CaptureEnvelope) Validate() error {
	if err := ValidateEnvelope(e.Key, "capture", e.Manifest); err != nil {
		return fmt.Errorf("capture envelope: %w", err)
	}
	return nil
}

// MarshalJSON rejects unsafe projections before serialization.
func (e CaptureEnvelope) MarshalJSON() ([]byte, error) {
	encoded, err := MarshalEnvelope(e.Key, "capture", e.Manifest)
	if err != nil {
		return nil, fmt.Errorf("serialize capture envelope: %w", err)
	}
	return encoded, nil
}

// ObservationEnvelope projects a screened observation manifest into transport.
// The local manifest retains all text, paths, URLs, cursors and capture metadata.
type ObservationEnvelope struct {
	Key      string
	Manifest Digest
}

// Validate checks the ledger key grammar and pinned manifest digest.
func (e ObservationEnvelope) Validate() error {
	if err := ValidateEnvelope(e.Key, "observation", e.Manifest); err != nil {
		return fmt.Errorf("observation envelope: %w", err)
	}
	return nil
}

// MarshalJSON rejects unsafe projections before serialization.
func (e ObservationEnvelope) MarshalJSON() ([]byte, error) {
	encoded, err := MarshalEnvelope(e.Key, "observation", e.Manifest)
	if err != nil {
		return nil, fmt.Errorf("serialize observation envelope: %w", err)
	}
	return encoded, nil
}

// FollowUpEnvelope projects a screened follow-up manifest into transport.
// The local manifest retains all text, paths, URLs, cursors and capture metadata.
type FollowUpEnvelope struct {
	Key      string
	Manifest Digest
}

// Validate checks the ledger key grammar and pinned manifest digest.
func (e FollowUpEnvelope) Validate() error {
	if err := ValidateEnvelope(e.Key, "request", e.Manifest); err != nil {
		return fmt.Errorf("request envelope: %w", err)
	}
	return nil
}

// MarshalJSON rejects unsafe projections before serialization.
func (e FollowUpEnvelope) MarshalJSON() ([]byte, error) {
	encoded, err := MarshalEnvelope(e.Key, "request", e.Manifest)
	if err != nil {
		return nil, fmt.Errorf("serialize request envelope: %w", err)
	}
	return encoded, nil
}

// ListingPageEnvelope projects a screened listing-page manifest into transport.
// The local manifest retains all text, paths, URLs, cursors and capture metadata.
type ListingPageEnvelope struct {
	Key      string
	Manifest Digest
}

// Validate checks the ledger key grammar and pinned manifest digest.
func (e ListingPageEnvelope) Validate() error {
	if err := ValidateEnvelope(e.Key, "page", e.Manifest); err != nil {
		return fmt.Errorf("page envelope: %w", err)
	}
	return nil
}

// MarshalJSON rejects unsafe projections before serialization.
func (e ListingPageEnvelope) MarshalJSON() ([]byte, error) {
	encoded, err := MarshalEnvelope(e.Key, "page", e.Manifest)
	if err != nil {
		return nil, fmt.Errorf("serialize page envelope: %w", err)
	}
	return encoded, nil
}

// DetailEnvelope projects a screened detail-result manifest into transport.
// The local manifest retains all text, paths, URLs, cursors and capture metadata.
type DetailEnvelope struct {
	Key      string
	Manifest Digest
}

// Validate checks the ledger key grammar and pinned manifest digest.
func (e DetailEnvelope) Validate() error {
	if err := ValidateEnvelope(e.Key, "detail", e.Manifest); err != nil {
		return fmt.Errorf("detail envelope: %w", err)
	}
	return nil
}

// MarshalJSON rejects unsafe projections before serialization.
func (e DetailEnvelope) MarshalJSON() ([]byte, error) {
	encoded, err := MarshalEnvelope(e.Key, "detail", e.Manifest)
	if err != nil {
		return nil, fmt.Errorf("serialize detail envelope: %w", err)
	}
	return encoded, nil
}

// ConversionEnvelope projects a screened conversion manifest into transport.
// The local manifest retains all text, paths, URLs, cursors and capture metadata.
type ConversionEnvelope struct {
	Key      string
	Manifest Digest
}

// Validate checks the ledger key grammar and pinned manifest digest.
func (e ConversionEnvelope) Validate() error {
	if err := ValidateEnvelope(e.Key, "conversion", e.Manifest); err != nil {
		return fmt.Errorf("conversion envelope: %w", err)
	}
	return nil
}

// MarshalJSON rejects unsafe projections before serialization.
func (e ConversionEnvelope) MarshalJSON() ([]byte, error) {
	encoded, err := MarshalEnvelope(e.Key, "conversion", e.Manifest)
	if err != nil {
		return nil, fmt.Errorf("serialize conversion envelope: %w", err)
	}
	return encoded, nil
}

// QualityEnvelope projects a screened quality report into transport.
// The local manifest retains all text, paths, URLs, cursors and capture metadata.
type QualityEnvelope struct {
	Key      string
	Manifest Digest
}

// Validate checks the ledger key grammar and pinned manifest digest.
func (e QualityEnvelope) Validate() error {
	if err := ValidateEnvelope(e.Key, "quality", e.Manifest); err != nil {
		return fmt.Errorf("quality envelope: %w", err)
	}
	return nil
}

// MarshalJSON rejects unsafe projections before serialization.
func (e QualityEnvelope) MarshalJSON() ([]byte, error) {
	encoded, err := MarshalEnvelope(e.Key, "quality", e.Manifest)
	if err != nil {
		return nil, fmt.Errorf("serialize quality envelope: %w", err)
	}
	return encoded, nil
}

// ResidencyEnvelope projects a local residency-screening receipt into transport.
// The local manifest retains all text, paths, URLs, cursors and capture metadata.
type ResidencyEnvelope struct {
	Key      string
	Manifest Digest
}

// Validate checks the ledger key grammar and pinned manifest digest.
func (e ResidencyEnvelope) Validate() error {
	if err := ValidateEnvelope(e.Key, "residency", e.Manifest); err != nil {
		return fmt.Errorf("residency envelope: %w", err)
	}
	return nil
}

// MarshalJSON rejects unsafe projections before serialization.
func (e ResidencyEnvelope) MarshalJSON() ([]byte, error) {
	encoded, err := MarshalEnvelope(e.Key, "residency", e.Manifest)
	if err != nil {
		return nil, fmt.Errorf("serialize residency envelope: %w", err)
	}
	return encoded, nil
}
