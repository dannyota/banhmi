package temporal

import (
	"fmt"

	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// NewIntakeReplayerV2 retains v1 and adds all three intake definitions. Real
// replay proof requires histories captured from hosted Temporal executions.
func NewIntakeReplayerV2() worker.WorkflowReplayer {
	r := NewReplayer()
	r.RegisterWorkflowWithOptions(SourceSyncV2, workflow.RegisterOptions{Name: SourceSyncNameV2})
	r.RegisterWorkflowWithOptions(DocumentIngestV2, workflow.RegisterOptions{Name: DocumentIngestNameV2})
	r.RegisterWorkflowWithOptions(ControlReconcileV2, workflow.RegisterOptions{Name: ControlReconcileNameV2})
	return r
}

// ReplayIntakeHistoryFileV2 replays a captured history, never synthetic JSON.
// The caller supplies a hosted artifact path; this function performs no fetch.
func ReplayIntakeHistoryFileV2(logger log.Logger, path string) error {
	if path == "" {
		return fmt.Errorf("captured intake history path is required")
	}
	return NewIntakeReplayerV2().ReplayWorkflowHistoryFromJSONFile(logger, path)
}
