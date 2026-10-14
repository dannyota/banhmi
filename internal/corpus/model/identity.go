package model

// IdentityEntityKind names entities admitted to the identity catalog. Additional
// kinds need their own reviewed key grammar before this vocabulary expands.
type IdentityEntityKind string

const IdentityEntityDocument IdentityEntityKind = "document"

// Valid reports membership in the closed identity vocabulary.
func (k IdentityEntityKind) Valid() bool {
	return k == IdentityEntityDocument
}

// IdentityAssertionKind records an observation or an explicit identity decision.
type IdentityAssertionKind string

const (
	IdentityAssertionObserved   IdentityAssertionKind = "observed"
	IdentityAssertionMerge      IdentityAssertionKind = "merge"
	IdentityAssertionSplit      IdentityAssertionKind = "split"
	IdentityAssertionContinuity IdentityAssertionKind = "continuity"
)

// Valid reports membership in the closed assertion vocabulary.
func (k IdentityAssertionKind) Valid() bool {
	return k == IdentityAssertionObserved || k == IdentityAssertionMerge || k == IdentityAssertionSplit || k == IdentityAssertionContinuity
}

// IdentityReviewState records immutable evidence assessment. Reassessment appends
// an assertion instead of changing an earlier row.
type IdentityReviewState string

const (
	IdentityReviewUnreviewed IdentityReviewState = "unreviewed"
	IdentityReviewConfirmed  IdentityReviewState = "confirmed"
	IdentityReviewRejected   IdentityReviewState = "rejected"
	IdentityReviewUnresolved IdentityReviewState = "unresolved"
)

// Valid reports membership in the closed review vocabulary.
func (s IdentityReviewState) Valid() bool {
	return s == IdentityReviewUnreviewed || s == IdentityReviewConfirmed || s == IdentityReviewRejected || s == IdentityReviewUnresolved
}

// LegacyIdentityMappingState preserves uncertainty in imported identity evidence.
type LegacyIdentityMappingState string

const (
	LegacyIdentityResolved    LegacyIdentityMappingState = "resolved"
	LegacyIdentityAmbiguous   LegacyIdentityMappingState = "ambiguous"
	LegacyIdentityUnavailable LegacyIdentityMappingState = "unavailable"
)

// Valid reports membership in the closed mapping vocabulary.
func (s LegacyIdentityMappingState) Valid() bool {
	return s == LegacyIdentityResolved || s == LegacyIdentityAmbiguous || s == LegacyIdentityUnavailable
}
