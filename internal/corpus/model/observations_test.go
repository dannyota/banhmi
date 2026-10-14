package model

import (
	"testing"
	"time"
)

func TestFollowUpAndCursor(t *testing.T) {
	r := FollowUpRequest{RequestKey: "request", WindowKey: "window", ParentCapture: "capture", Purpose: PurposeDetail, URL: "https://example.org/item/1", PolicyHash: contractAsset().SHA256, BudgetHash: contractAsset().SHA256,
		Budget: ResourceBudget{MaxItems: 1, MaxInputBytes: 10, MaxPages: 1, MaxElapsed: time.Second, MaxConcurrent: 1}}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	r.Budget.MaxInputBytes = 0
	if r.Validate() == nil || RequestPurpose("unknown").Valid() {
		t.Fatal("accepted unbounded follow-up or unknown purpose")
	}
	for _, u := range []string{"file:///tmp/a", "https://user:password@example.org", "https://example.org/#fragment", "relative"} {
		if ValidatePublicURL(u) == nil {
			t.Fatalf("accepted unsafe URL %s", u)
		}
	}
	c := CursorPosition{WindowKey: "window", SliceKey: "slice", CommittedCursor: "1", NextCursor: "2"}
	if c.Validate() == nil {
		t.Fatal("incomplete cursor advanced")
	}
	c.Complete = true
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestFollowUpRetainsFrozenOrigin(t *testing.T) {
	hash := contractAsset().SHA256
	budget := ResourceBudget{MaxItems: 1, MaxInputBytes: 10, MaxPages: 1, MaxElapsed: time.Second, MaxConcurrent: 1}
	r := FollowUpRequest{RequestKey: "request", WindowKey: "window", ParentCapture: "capture", Purpose: PurposeDetail, URL: "https://example.invalid/item", PolicyHash: hash, BudgetHash: hash, Budget: budget}
	if err := r.ValidateOrigin("window", hash, budget); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*FollowUpRequest){
		func(r *FollowUpRequest) { r.WindowKey = "other" },
		func(r *FollowUpRequest) { r.BudgetHash = "" },
		func(r *FollowUpRequest) { r.Budget.MaxItems++ },
	} {
		changed := r
		mutate(&changed)
		if changed.ValidateOrigin("window", hash, budget) == nil {
			t.Fatal("follow-up reset frozen origin")
		}
	}
}

func TestArtifactDeclaredValues(t *testing.T) {
	entry := ArtifactInventoryEntry{ArtifactKey: "artifact", Role: "main", URL: "https://example.org/file", DeclaredFilename: "synthetic.doc", Format: "doc"}
	if err := entry.Validate(); err != nil {
		t.Fatal(err)
	}
	entry.Format = "exe"
	if entry.Validate() == nil {
		t.Fatal("unknown format vocabulary accepted")
	}
	entry.Format = "unknown"
	entry.DeclaredFilename = string(make([]byte, MaxArtifactValueBytes+1))
	if entry.Validate() == nil {
		t.Fatal("oversized filename accepted")
	}
}

func TestDeclaredArtifactFormatsAndBounds(t *testing.T) {
	for _, format := range []string{"", "unknown", "doc", "docx", "html", "pdf", "png", "jpeg", "tiff", "xlsx", "ppt", "pptx", "odp"} {
		entry := ArtifactInventoryEntry{ArtifactKey: "artifact", Role: "main", URL: "https://example.org/file", Format: format}
		if err := entry.Validate(); err != nil {
			t.Fatalf("declared format %s: %v", format, err)
		}
	}
	entry := ArtifactInventoryEntry{ArtifactKey: "artifact", Role: "main", URL: "https://example.org/file", Format: "unknown"}
	for _, filename := range []string{"\xff", string(make([]byte, MaxArtifactValueBytes+1)) + "\xff"} {
		entry.DeclaredFilename = filename
		if entry.Validate() == nil {
			t.Fatal("invalid declared filename accepted")
		}
	}
}

func TestArtifactDeclaredValuesAreLocal(t *testing.T) {
	// New declaration fields remain simple local values. Existing transport
	// sentinels in intake_test.go guard every reference-only envelope.
	entry := ArtifactInventoryEntry{ArtifactKey: "artifact", Role: "main", URL: "https://example.org/file", DeclaredFilename: "synthetic.person@example.invalid", Format: "unknown"}
	if err := entry.Validate(); err != nil {
		t.Fatal(err)
	}
	entry.ArtifactKey = entry.DeclaredFilename
	if entry.Validate() == nil {
		t.Fatal("declared native text became a ledger key")
	}
}
