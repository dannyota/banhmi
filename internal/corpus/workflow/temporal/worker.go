package temporal

import (
	"fmt"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// StartOptions supplies the deterministic workflow identity and coordinator
// queue used by triggers. A lost start response can be reconciled by ID.
func StartOptions(state State) (client.StartWorkflowOptions, error) {
	if err := state.Validate(); err != nil {
		return client.StartWorkflowOptions{}, err
	}
	if err := state.validateNewStart(); err != nil {
		return client.StartWorkflowOptions{}, err
	}
	return client.StartWorkflowOptions{
		ID: WorkflowID(state.Country, state.BuildID),
		TaskQueue: CoordinatorTaskQueue,
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
		WorkflowExecutionErrorWhenAlreadyStarted: false,
	}, nil
}

// RegisterWorkflow registers the versioned workflow name used by starts,
// Continue-As-New, tests, and replay.
func RegisterWorkflow(registry worker.WorkflowRegistry) {
	registry.RegisterWorkflowWithOptions(CorpusRefresh, workflow.RegisterOptions{Name: WorkflowNameV1})
}

// RegisterActivities registers every activity on a registry intended for a
// single all-capability test worker. Production composition should use
// NewWorkerSet so stage work remains on fixed bounded queues.
func RegisterActivities(registry worker.ActivityRegistry, activities *Activities) {
	registerPrepare(registry, activities)
	registerDirect(registry, activities)
	registerExternal(registry, activities)
	registerActivation(registry, activities)
}

// WorkerSet contains the fixed coordinator and stage workers for the pilot.
type WorkerSet struct {
	Coordinator worker.Worker
	Network     worker.Worker
	CPU         worker.Worker
	Database    worker.Worker
	External    worker.Worker
	Exclusive   worker.Worker
}

// NewWorkerSet creates fixed queues and slot limits. It registers source only;
// callers still own lifecycle, connectivity, deployment, and durable adapters.
func NewWorkerSet(temporalClient client.Client, activities *Activities) (*WorkerSet, error) {
	if temporalClient == nil {
		return nil, fmt.Errorf("Temporal client is required")
	}
	if err := activities.validate(); err != nil {
		return nil, err
	}
	set := &WorkerSet{
		Coordinator: worker.New(temporalClient, CoordinatorTaskQueue, worker.Options{
			MaxConcurrentWorkflowTaskExecutionSize: 2,
		}),
		Network: worker.New(temporalClient, NetworkTaskQueue, worker.Options{
			MaxConcurrentActivityExecutionSize: 8,
		}),
		CPU: worker.New(temporalClient, CPUTaskQueue, worker.Options{
			MaxConcurrentActivityExecutionSize: 2,
		}),
		Database: worker.New(temporalClient, DatabaseTaskQueue, worker.Options{
			MaxConcurrentActivityExecutionSize: 4,
		}),
		External: worker.New(temporalClient, ExternalTaskQueue, worker.Options{
			MaxConcurrentActivityExecutionSize: 4,
		}),
		Exclusive: worker.New(temporalClient, ExclusiveTaskQueue, worker.Options{
			MaxConcurrentActivityExecutionSize: 1,
		}),
	}
	RegisterWorkflow(set.Coordinator)
	registerPrepare(set.Network, activities)
	registerDirect(set.Network, activities)
	registerPrepare(set.CPU, activities)
	registerDirect(set.CPU, activities)
	registerPrepare(set.Database, activities)
	registerDirect(set.Database, activities)
	registerPrepare(set.External, activities)
	registerExternal(set.External, activities)
	registerPrepare(set.Exclusive, activities)
	registerDirect(set.Exclusive, activities)
	registerActivation(set.Exclusive, activities)
	return set, nil
}

func (s *WorkerSet) All() []worker.Worker {
	if s == nil {
		return nil
	}
	return []worker.Worker{s.Coordinator, s.Network, s.CPU, s.Database, s.External, s.Exclusive}
}

func registerPrepare(registry worker.ActivityRegistry, activities *Activities) {
	registry.RegisterActivityWithOptions(activities.PrepareStage,
		activity.RegisterOptions{Name: PrepareStageActivityName})
}

func registerDirect(registry worker.ActivityRegistry, activities *Activities) {
	registry.RegisterActivityWithOptions(activities.ExecuteStage,
		activity.RegisterOptions{Name: ExecuteStageActivityName})
}

func registerExternal(registry worker.ActivityRegistry, activities *Activities) {
	registry.RegisterActivityWithOptions(activities.PrepareExternal,
		activity.RegisterOptions{Name: PrepareExternalActivityName})
	registry.RegisterActivityWithOptions(activities.SubmitExternal,
		activity.RegisterOptions{Name: SubmitExternalActivityName})
	registry.RegisterActivityWithOptions(activities.CheckExternal,
		activity.RegisterOptions{Name: CheckExternalActivityName})
	registry.RegisterActivityWithOptions(activities.ApplyExternal,
		activity.RegisterOptions{Name: ApplyExternalActivityName})
	registry.RegisterActivityWithOptions(activities.CleanupExternal,
		activity.RegisterOptions{Name: CleanupExternalActivityName})
}

func registerActivation(registry worker.ActivityRegistry, activities *Activities) {
	registry.RegisterActivityWithOptions(activities.Activate,
		activity.RegisterOptions{Name: ActivateActivityName})
}
