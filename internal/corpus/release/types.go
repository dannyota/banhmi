// Package release defines corpus publication, activation, pin, and retention
// contracts without binding them to a database or serving transport.
package release

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	ErrIncompleteManifest   = errors.New("release manifest is incomplete")
	ErrCoverageMismatch    = errors.New("release coverage does not match chunk coverage")
	ErrUnacceptedGaps      = errors.New("release contains unaccepted gaps")
	ErrIncompatibleRuntime = errors.New("release is incompatible with the serving runtime")
	ErrSelectionConflict   = errors.New("release selection changed")
	ErrWrongCountry        = errors.New("release belongs to another country")
	ErrPoolDraining        = errors.New("release pool is draining")
	ErrPinLost             = errors.New("durable release pin was lost")
	ErrRetentionConflict   = errors.New("release has a conflicting retention claim")
	ErrWrongBuild          = errors.New("release belongs to another build")
)

const (
	EmbeddingDimensions = 1024
	LexicalDimensions   = 1048576
)

type State string

const (
	StateCandidate State = "candidate"
	StateValidated State = "validated"
	StateReady     State = "ready"
	StateActive    State = "active"
	StateRetired   State = "retired"
	StateFailed    State = "failed"
)

func (s State) available() bool {
	return s == StateReady || s == StateActive || s == StateRetired
}

// Manifest binds every identity and coverage count needed to reproduce and
// serve one immutable evidence release.
type Manifest struct {
	Country                              string
	CorpusHash                           string
	SourceManifestDigest                 string
	BlobSetDigest                        string
	SchemaMigrationDigest                string
	CitationRecipeDigest                 string
	ChunkingRecipeDigest                 string
	EmbeddingModelDigest                 string
	EmbeddingTokenizerDigest             string
	EmbeddingPreprocessingDigest         string
	EmbeddingDimensions                  int
	LexicalRevision                      string
	LexicalRevisionDigest                string
	LexicalRecipeDigest                  string
	LexicalTokenizerDigest               string
	LexicalPreprocessingDigest           string
	LexicalDimensions                    int
	TaxonomyReleaseID                    int64
	TaxonomyContentDigest                string
	ConfigDigest                         string
	EvaluationArtifactRef                string
	EvaluationArtifactDigest             string
	CompatibleServerImageDigest          string
	CompatibleEmbedderImageDigest        string
	SourceObservationCount               int64
	DocumentRevisionCount                int64
	ProvisionRevisionCount               int64
	ChunkCount                           int64
	CitationCount                        int64
	ProvenanceCount                      int64
	DenseVectorCount                     int64
	LexicalChunkCount                    int64
	GapCount                             int64
	AcceptedGapCount                     int64
	GapAcceptanceRef                     string
}

type Release struct {
	ID              string
	BuildID         uuid.UUID
	Country         string
	SnapshotRef     string
	State           State
	Manifest        Manifest
	ValidatedAt     time.Time
	ReadyAt         time.Time
	ValidationError string
}

func (r Release) Validate() error {
	if r.ID == "" || r.BuildID == uuid.Nil || !validCountry(r.Country) || r.SnapshotRef == "" {
		return fmt.Errorf("%w: release identity is required", ErrIncompleteManifest)
	}
	if r.Manifest.Country != r.Country {
		return fmt.Errorf("%w: release country %q, manifest country %q", ErrWrongCountry, r.Country, r.Manifest.Country)
	}
	if !r.State.available() {
		return &UnavailableError{Country: r.Country, ReleaseID: r.ID, Reason: UnavailableNotReady}
	}
	if r.ValidatedAt.IsZero() || r.ReadyAt.IsZero() || r.ValidationError != "" {
		return &UnavailableError{Country: r.Country, ReleaseID: r.ID, Reason: UnavailableNotReady}
	}
	return r.Manifest.Validate()
}

type BundleIdentity struct {
	Country                      string
	CorpusHash                   string
	SourceManifestDigest         string
	BlobSetDigest                string
	SchemaMigrationDigest        string
	CitationRecipeDigest         string
	ChunkingRecipeDigest         string
	EmbeddingModelDigest         string
	EmbeddingTokenizerDigest     string
	EmbeddingPreprocessingDigest string
	EmbeddingDimensions          int
	LexicalRevision              string
	LexicalRevisionDigest        string
	LexicalRecipeDigest          string
	LexicalTokenizerDigest       string
	LexicalPreprocessingDigest   string
	LexicalDimensions            int
	TaxonomyReleaseID            int64
	TaxonomyContentDigest        string
	ConfigDigest                 string
	EvaluationArtifactRef        string
	EvaluationArtifactDigest     string
}

type RuntimeIdentity struct {
	Bundle              BundleIdentity
	Coverage            Coverage
	ServerImageDigest   string
	EmbedderImageDigest string
}

type Coverage struct {
	SourceObservationCount  int64
	DocumentRevisionCount   int64
	ProvisionRevisionCount  int64
	ChunkCount              int64
	CitationCount           int64
	ProvenanceCount         int64
	DenseVectorCount        int64
	LexicalChunkCount       int64
	GapCount                int64
	AcceptedGapCount        int64
	GapAcceptanceRef        string
}

type Selection struct {
	Country           string
	Version           int64
	CurrentReleaseID  string
	PreviousReleaseID string
}

type ActivationChange struct {
	Country         string
	ReleaseID       string
	ExpectedVersion int64
	ActivatedBy     string
	ActivatedAt     time.Time
}

type ReadinessRecord struct {
	Country         string
	ReleaseID       string
	State           State
	ValidatedAt     time.Time
	ReadyAt         time.Time
	ValidationError string
}

type Catalog interface {
	Release(context.Context, string, string) (Release, error)
	SetReadiness(context.Context, ReadinessRecord) (Release, error)
	Selection(context.Context, string) (Selection, error)
	// CompareAndSet atomically rejects a target with a live or finalized
	// retention claim before changing selection or active and rollback holds.
	CompareAndSet(context.Context, ActivationChange) (Selection, error)
}

type Readiness interface {
	Check(context.Context, Release) (RuntimeIdentity, error)
}

type PoolDrainer interface {
	Drain(country, releaseID string) error
}

type UnavailableReason string

const (
	UnavailableUnknown  UnavailableReason = "unknown"
	UnavailableExpired  UnavailableReason = "expired"
	UnavailableNotReady UnavailableReason = "not_ready"
)

type UnavailableError struct {
	Country   string
	ReleaseID string
	Reason    UnavailableReason
}

func (e *UnavailableError) Error() string {
	return fmt.Sprintf("release %q is %s for country %q", e.ReleaseID, e.Reason, e.Country)
}

type DurablePin struct {
	Country   string
	ReleaseID string
	Key       string
	ExpiresAt time.Time
}

type DurableLease interface {
	Pin() DurablePin
	// Context is canceled when renewal fails or the durable hold is otherwise
	// lost. Resolver operations bind every evidence call to this context.
	Context() context.Context
	// Close removes the durable in-flight hold. Implementations renew the hold
	// until Close begins so retention cannot expire a live request.
	Close() error
}

type PinStore interface {
	// Acquire atomically rejects expired, wrong-country, unavailable, and
	// retention-claimed releases before creating the in-flight hold.
	Acquire(context.Context, string, string, time.Duration) (DurableLease, error)
}

type HoldKind string

const (
	HoldActive   HoldKind = "active"
	HoldRollback HoldKind = "rollback"
	HoldNamed    HoldKind = "pinned"
	HoldInFlight HoldKind = "in_flight"
	HoldBackup   HoldKind = "backup"
)

func (k HoldKind) Valid() bool {
	switch k {
	case HoldActive, HoldRollback, HoldNamed, HoldInFlight, HoldBackup:
		return true
	default:
		return false
	}
}

type Hold struct {
	Country   string
	ReleaseID string
	Kind      HoldKind
	Key       string
	ExpiresAt *time.Time
}

func (h Hold) Validate() error {
	if !validCountry(h.Country) || h.ReleaseID == "" || h.Key == "" {
		return fmt.Errorf("hold country, release ID, and key are required")
	}
	if !h.Kind.Valid() {
		return fmt.Errorf("unknown release hold kind %q", h.Kind)
	}
	if (h.Kind == HoldNamed || h.Kind == HoldInFlight) && h.ExpiresAt == nil {
		return fmt.Errorf("release hold kind %q requires expiry", h.Kind)
	}
	return nil
}

func validCountry(country string) bool {
	return len(country) == 2 && country[0] >= 'a' && country[0] <= 'z' &&
		country[1] >= 'a' && country[1] <= 'z'
}

type RetentionClaim struct {
	Release   Release
	ClaimKey  string
	Owner     string
	Fence     int64
	ClaimedAt time.Time
	ExpiresAt time.Time
}

type RetentionRequest struct {
	Country  string
	Owner    string
	BatchKey string
	Limit    int
	Now      time.Time
	TTL      time.Duration
}

type RetentionStore interface {
	// ClaimEligible atomically excludes active, rollback, named, in-flight, and
	// backup-held releases and marks each returned release unavailable to
	// PinStore.Acquire. Expiry permits recovery from an abandoned collector.
	ClaimEligible(context.Context, RetentionRequest) ([]RetentionClaim, error)
	RenewClaim(context.Context, RetentionClaim, time.Time, time.Duration) (RetentionClaim, error)
	ReleaseClaim(context.Context, RetentionClaim) error
	// FinalizeClaim rechecks selection and every protected hold under the claim
	// fence before it makes deletion final.
	FinalizeClaim(context.Context, RetentionClaim, time.Time) error
}

type HoldStore interface {
	Add(context.Context, Hold) error
	Remove(context.Context, Hold) error
	List(context.Context, string, string, time.Time) ([]Hold, error)
}
