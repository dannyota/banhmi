package intake

import (
	"context"
	"errors"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

func TestSourcesThirdExpiryRejectsMissingClaimMarkers(t *testing.T) {
	epoch, fence := int64(1), int64(1)
	for _, markers := range []struct {
		name         string
		epoch, fence *int64
	}{
		{name: "both"},
		{name: "epoch", fence: &fence},
		{name: "fence", epoch: &epoch},
	} {
		t.Run(markers.name, func(t *testing.T) {
			r := intakedb.IngestArtifactRequest{State: "leased", InfrastructureAttempts: 3, ClaimedEpoch: markers.epoch, ClaimedFence: markers.fence}
			grant := run.ScopedExecution{AdmissionEpoch: 2, AttemptFence: 2}
			// A nil transaction also proves rejection precedes any write. The
			// schema prevents creating leased rows with missing markers in SQL.
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := closeSourceRequest(ctx, nil, grant, r, model.ArtifactRequestExpired, "infrastructure_retry_exhausted")
			if !errors.Is(err, run.ErrStaleGrant) {
				t.Fatalf("missing %s marker allowed closure: %v", markers.name, err)
			}
		})
	}
}
