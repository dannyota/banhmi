package temporal

import (
	"fmt"

	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// NewReplayer returns a real SDK replayer with the stable workflow name.
func NewReplayer() worker.WorkflowReplayer {
	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflowWithOptions(CorpusRefresh, workflow.RegisterOptions{Name: WorkflowNameV1})
	return replayer
}

// ReplayHistoryFile accepts a history captured from a hosted Temporal run.
// The repository does not synthesize history JSON as proof of compatibility.
func ReplayHistoryFile(logger log.Logger, path string) error {
	if path == "" {
		return fmt.Errorf("history path is required")
	}
	return NewReplayer().ReplayWorkflowHistoryFromJSONFile(logger, path)
}
