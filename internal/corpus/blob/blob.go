// Package blob defines immutable content metadata and durable retention ports.
// It provides no in-memory production catalog or object-store substitute.
package blob

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"time"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Metadata struct {
	SHA256        string
	ByteSize      int64
	MediaType     string
	ObjectRef     string
	CaptureSource string
	CaptureRef    string
	CapturedAt    time.Time
}

func (m Metadata) Validate() error {
	switch {
	case !sha256Pattern.MatchString(m.SHA256):
		return fmt.Errorf("SHA-256 must contain 64 lowercase hexadecimal characters")
	case m.ByteSize < 0:
		return fmt.Errorf("byte size must not be negative")
	case m.MediaType == "":
		return fmt.Errorf("media type is required")
	case m.ObjectRef == "":
		return fmt.Errorf("object reference is required")
	case m.CaptureSource == "":
		return fmt.Errorf("capture source is required")
	case m.CaptureRef == "":
		return fmt.Errorf("capture reference is required")
	case m.CapturedAt.IsZero():
		return fmt.Errorf("captured time is required")
	default:
		return nil
	}
}

type ReferenceKind string

const (
	ReferenceBuild    ReferenceKind = "build"
	ReferenceRelease  ReferenceKind = "release"
	ReferenceActive   ReferenceKind = "active"
	ReferenceRollback ReferenceKind = "rollback"
	ReferencePinned   ReferenceKind = "pinned"
	ReferenceInFlight ReferenceKind = "in_flight"
	ReferenceBackup   ReferenceKind = "backup"
)

func (k ReferenceKind) Valid() bool {
	switch k {
	case ReferenceBuild, ReferenceRelease, ReferenceActive, ReferenceRollback,
		ReferencePinned, ReferenceInFlight, ReferenceBackup:
		return true
	default:
		return false
	}
}

type Reference struct {
	BlobSHA256 string
	Kind       ReferenceKind
	Key        string
	ExpiresAt  *time.Time
}

func (r Reference) Validate() error {
	if !sha256Pattern.MatchString(r.BlobSHA256) {
		return fmt.Errorf("blob SHA-256 must contain 64 lowercase hexadecimal characters")
	}
	if !r.Kind.Valid() {
		return fmt.Errorf("unknown blob reference kind %q", r.Kind)
	}
	if r.Key == "" {
		return fmt.Errorf("blob reference key is required")
	}
	return nil
}

type Catalog interface {
	Register(context.Context, Metadata) (Metadata, error)
	BySHA256(context.Context, string) (Metadata, error)
	AddReference(context.Context, Reference) error
	RemoveReference(context.Context, Reference) error
	References(context.Context, string, time.Time) ([]Reference, error)
}

type Object struct {
	ObjectRef string
	ByteSize  int64
	SHA256    string
}

// Store addresses objects only by catalog-issued references. Callers never
// turn release IDs or request parameters into storage locations.
type Store interface {
	Put(context.Context, Metadata, io.Reader) (Object, error)
	Open(context.Context, string) (io.ReadCloser, error)
	Stat(context.Context, string) (Object, error)
	Delete(context.Context, string) error
}
