package model

import "fmt"

// ValidateKey checks portable opaque ledger-key syntax, without resolving it.
// Producers must use opaque keys, never titles, URLs or personal identifiers.
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("key is required")
	}
	for _, c := range key {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == ':' || c == '/' || c == '.') {
			return fmt.Errorf("key must contain portable identifier characters")
		}
	}
	return nil
}

type AcceptanceState string

const (
	Candidate        AcceptanceState = "candidate"
	Quarantined      AcceptanceState = "quarantined"
	Accepted         AcceptanceState = "accepted"
	AcceptedWithGaps AcceptanceState = "accepted_with_gaps"
	Excluded         AcceptanceState = "excluded"
)

func (s AcceptanceState) Valid() bool {
	return s == Candidate || s == Quarantined || s == Accepted || s == AcceptedWithGaps || s == Excluded
}

func (s AcceptanceState) BuildEligible() bool { return s == Accepted || s == AcceptedWithGaps }

type DecisionKind string

const (
	DecisionRetryConverter  DecisionKind = "retry_converter"
	DecisionAcceptOCR       DecisionKind = "accept_non_binding_ocr"
	DecisionAcceptGaps      DecisionKind = "accept_with_named_gaps"
	DecisionExclude         DecisionKind = "exclude_coverage_gap"
	DecisionAlternateSource DecisionKind = "attach_alternate_official_source"
)

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

func (k GapKind) Valid() bool {
	return k == GapSourceWindow || k == GapArtifact || k == GapDocumentRevision || k == GapProvision || k == GapPart
}

type GapRef struct {
	Kind       GapKind
	Key        string
	ReasonCode string
	ReportHash Digest
}

func (g GapRef) Validate() error {
	if !g.Kind.Valid() || g.Key == "" || g.ReasonCode == "" {
		return fmt.Errorf("gap requires a known scope, key and reason code")
	}
	return g.ReportHash.Validate()
}

// CaptureRef names immutable captured content and hashed transport metadata.
// Locators must be approved public URLs with no credentials or personal data.
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

func (c CaptureRef) Validate() error {
	if c.Country == "" || c.SourceID == "" || c.CaptureKey == "" || c.MediaType == "" {
		return fmt.Errorf("capture identity and media type are required")
	}
	if err := c.Asset.Validate(); err != nil {
		return err
	}
	if err := c.RequestHash.Validate(); err != nil {
		return err
	}
	if err := c.ResponseHash.Validate(); err != nil {
		return err
	}
	return c.TransportReceipt.Validate()
}

type ResidencyClass string

const (
	ResidencyPublic       ResidencyClass = "public_non_personal"
	ResidencyPersonal     ResidencyClass = "personal_data"
	ResidencyUnclassified ResidencyClass = "unclassified"
)

func (c ResidencyClass) Valid() bool {
	return c == ResidencyPublic || c == ResidencyPersonal || c == ResidencyUnclassified
}

// ResidencyAdmission never carries screened text. Only classified public,
// non-personal artifacts may enter workflow references or external processing.
type ResidencyAdmission struct {
	ArtifactHash Digest
	PolicyHash   Digest
	Class        ResidencyClass
	ReasonCode   string
	Report       AssetRef
}

func (a ResidencyAdmission) Admitted() bool { return a.Class == ResidencyPublic }

func (a ResidencyAdmission) Validate() error {
	if !a.Class.Valid() || a.ReasonCode == "" {
		return fmt.Errorf("residency requires classification and reason")
	}
	for _, d := range []Digest{a.ArtifactHash, a.PolicyHash} {
		if err := d.Validate(); err != nil {
			return err
		}
	}
	return a.Report.Validate()
}
