package model

import "testing"

func TestSourceStateVocabularies(t *testing.T) {
	sets := map[string][]interface{ Valid() bool }{
		"window":  {SourceWindowFrozen, SourceWindowProgressing, SourceWindowComplete, SourceWindowIncomplete, SourceWindowFailed},
		"page":    {DiscoveryPageRequested, DiscoveryPageCaptured, DiscoveryPageDecoded, DiscoveryPageAccepted, DiscoveryPageFailed},
		"request": {ArtifactRequestDue, ArtifactRequestLeased, ArtifactRequestFetched, ArtifactRequestVerified, ArtifactRequestFailed, ArtifactRequestExhausted, ArtifactRequestExpired},
		"role":    {ArtifactRoleMain, ArtifactRoleAppendix, ArtifactRoleOriginalScan},
		"owner":   {RetentionOwnerExecution, RetentionOwnerObservationRevision, RetentionOwnerDocumentRevision, RetentionOwnerBuild, RetentionOwnerRelease},
	}
	for name, values := range sets {
		for _, value := range values {
			if !value.Valid() {
				t.Errorf("%s rejects %v", name, value)
			}
		}
	}
	for _, value := range []interface{ Valid() bool }{
		SourceWindowState(""), SourceWindowState("outside"),
		DiscoveryPageState(""), DiscoveryPageState("outside"),
		ArtifactRequestState(""), ArtifactRequestState("outside"),
		ArtifactRole(""), ArtifactRole("outside"),
		RetentionOwnerKind(""), RetentionOwnerKind("outside"),
	} {
		if value.Valid() {
			t.Errorf("unknown value %T(%v) accepted", value, value)
		}
	}
}
