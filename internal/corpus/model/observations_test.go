package model

import (
	"testing"
	"time"
)

func TestFollowUpAndCursor(t *testing.T) {
	r := FollowUpRequest{RequestKey: "request", WindowKey: "window", ParentCapture: "capture", Purpose: PurposeDetail, URL: "https://example.org/item/1", PolicyHash: contractAsset().SHA256,
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
