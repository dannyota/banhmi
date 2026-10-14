package model

import "testing"

func TestIdentityVocabularies(t *testing.T) {
	for _, k := range []IdentityEntityKind{IdentityEntityDocument} {
		if !k.Valid() {
			t.Errorf("entity kind %q rejected", k)
		}
	}
	for _, k := range []IdentityAssertionKind{IdentityAssertionObserved, IdentityAssertionMerge, IdentityAssertionSplit, IdentityAssertionContinuity} {
		if !k.Valid() {
			t.Errorf("assertion kind %q rejected", k)
		}
	}
	for _, s := range []IdentityReviewState{IdentityReviewUnreviewed, IdentityReviewConfirmed, IdentityReviewRejected, IdentityReviewUnresolved} {
		if !s.Valid() {
			t.Errorf("review state %q rejected", s)
		}
	}
	for _, s := range []LegacyIdentityMappingState{LegacyIdentityResolved, LegacyIdentityAmbiguous, LegacyIdentityUnavailable} {
		if !s.Valid() {
			t.Errorf("mapping state %q rejected", s)
		}
	}
	for _, outsider := range []string{"", "outsider", "DOCUMENT"} {
		if IdentityEntityKind(outsider).Valid() || IdentityAssertionKind(outsider).Valid() || IdentityReviewState(outsider).Valid() || LegacyIdentityMappingState(outsider).Valid() {
			t.Errorf("identity vocabulary admitted %q", outsider)
		}
	}
}
