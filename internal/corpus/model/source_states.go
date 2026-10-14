package model

// SourceWindowState records complete-window accounting progress.
type SourceWindowState string

const (
	SourceWindowFrozen      SourceWindowState = "frozen"
	SourceWindowProgressing SourceWindowState = "progressing"
	SourceWindowComplete    SourceWindowState = "complete"
	SourceWindowIncomplete  SourceWindowState = "incomplete"
	SourceWindowFailed      SourceWindowState = "failed"
)

// Valid reports membership in the closed contract vocabulary.
func (s SourceWindowState) Valid() bool {
	return s == SourceWindowFrozen || s == SourceWindowProgressing || s == SourceWindowComplete || s == SourceWindowIncomplete || s == SourceWindowFailed
}

// DiscoveryPageState records planning, staged receipts and page acceptance.
type DiscoveryPageState string

const (
	DiscoveryPageRequested DiscoveryPageState = "requested"
	DiscoveryPageCaptured  DiscoveryPageState = "captured"
	DiscoveryPageDecoded   DiscoveryPageState = "decoded"
	DiscoveryPageAccepted  DiscoveryPageState = "accepted"
	DiscoveryPageFailed    DiscoveryPageState = "failed"
)

// Valid reports membership in the closed contract vocabulary.
func (s DiscoveryPageState) Valid() bool {
	return s == DiscoveryPageRequested || s == DiscoveryPageCaptured || s == DiscoveryPageDecoded || s == DiscoveryPageAccepted || s == DiscoveryPageFailed
}

// ArtifactRequestState records transport eligibility and capture outcomes.
type ArtifactRequestState string

const (
	ArtifactRequestDue       ArtifactRequestState = "due"
	ArtifactRequestLeased    ArtifactRequestState = "leased"
	ArtifactRequestFetched   ArtifactRequestState = "fetched"
	ArtifactRequestVerified  ArtifactRequestState = "verified"
	ArtifactRequestFailed    ArtifactRequestState = "failed"
	ArtifactRequestExhausted ArtifactRequestState = "exhausted"
	ArtifactRequestExpired   ArtifactRequestState = "expired"
)

// Valid reports membership in the closed contract vocabulary.
func (s ArtifactRequestState) Valid() bool {
	return s == ArtifactRequestDue || s == ArtifactRequestLeased || s == ArtifactRequestFetched || s == ArtifactRequestVerified || s == ArtifactRequestFailed || s == ArtifactRequestExhausted || s == ArtifactRequestExpired
}

// ArtifactRole preserves the source-presented role of an original artifact.
type ArtifactRole string

const (
	ArtifactRoleMain         ArtifactRole = "main"
	ArtifactRoleAppendix     ArtifactRole = "appendix"
	ArtifactRoleOriginalScan ArtifactRole = "original_scan"
)

// Valid reports membership in the closed contract vocabulary.
func (r ArtifactRole) Valid() bool {
	return r == ArtifactRoleMain || r == ArtifactRoleAppendix || r == ArtifactRoleOriginalScan
}

// RetentionOwnerKind selects the stable-key grammar of a blob dependency.
type RetentionOwnerKind string

const (
	RetentionOwnerExecution           RetentionOwnerKind = "execution"
	RetentionOwnerObservationRevision RetentionOwnerKind = "observation_revision"
	RetentionOwnerDocumentRevision    RetentionOwnerKind = "document_revision"
	RetentionOwnerBuild               RetentionOwnerKind = "build"
	RetentionOwnerRelease             RetentionOwnerKind = "release"
)

// Valid reports membership in the closed contract vocabulary.
func (k RetentionOwnerKind) Valid() bool {
	return k == RetentionOwnerExecution || k == RetentionOwnerObservationRevision || k == RetentionOwnerDocumentRevision || k == RetentionOwnerBuild || k == RetentionOwnerRelease
}

// DiscoveryClaimState records ownership of one discovery fetch slot.
type DiscoveryClaimState string

const (
	DiscoveryClaimIssued   DiscoveryClaimState = "issued"
	DiscoveryClaimSettled  DiscoveryClaimState = "settled"
	DiscoveryClaimReleased DiscoveryClaimState = "released"
)

// Valid reports membership in the closed contract vocabulary.
func (s DiscoveryClaimState) Valid() bool {
	return s == DiscoveryClaimIssued || s == DiscoveryClaimSettled || s == DiscoveryClaimReleased
}
