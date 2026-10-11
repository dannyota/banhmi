package blob

import (
	"testing"
	"time"
)

func TestMetadataValidate(t *testing.T) {
	metadata := Metadata{
		SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ByteSize: 12, MediaType: "application/pdf", ObjectRef: "objects/01/23",
		CaptureSource: "official_source", CaptureRef: "source:document:1", CapturedAt: time.Now(),
	}
	if err := metadata.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestReferenceKindsMatchDurableSchema(t *testing.T) {
	for _, kind := range []ReferenceKind{
		ReferenceBuild, ReferenceRelease, ReferenceActive, ReferenceRollback,
		ReferencePinned, ReferenceInFlight, ReferenceBackup,
	} {
		if !kind.Valid() {
			t.Fatalf("reference kind %q is invalid", kind)
		}
	}
}
