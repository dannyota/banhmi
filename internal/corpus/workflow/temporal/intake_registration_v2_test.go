package temporal

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/workflow"

	"danny.vn/banhmi/internal/corpus/run"
)

func TestIntakeV2ProposedBounds(t *testing.T) {
	c := ProposedIntakeConfigV2()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.ChildGroup != 16 || c.Processing != 32 || c.Open != 256 || c.ChildStarts != 100 || c.Pages != 20 || c.Reminder != 24*time.Hour || c.Expiry != 7*24*time.Hour {
		t.Fatal("proposed bounds changed")
	}
	for _, mutate := range []func(*IntakeConfigV2){
		func(c *IntakeConfigV2) { c.ChildGroup = 0 },
		func(c *IntakeConfigV2) { c.ChildGroup = c.PendingActivities + 1 },
		func(c *IntakeConfigV2) { c.Processing = c.Open + 1 },
		func(c *IntakeConfigV2) { c.Expiry = c.Reminder },
		func(c *IntakeConfigV2) { c.EnvelopeBytes = 2 << 20 },
		func(c *IntakeConfigV2) { c.TransactionBytes = 4 << 20 },
		func(c *IntakeConfigV2) { c.CommandOverheadBytes = 0 },
		func(c *IntakeConfigV2) { c.HistoryEvents = 10240 },
		func(c *IntakeConfigV2) { c.HistoryBytes = 10 << 20 },
		func(c *IntakeConfigV2) { c.Signals = 10000 },
		func(c *IntakeConfigV2) { c.HeartbeatTimeout = c.Heartbeat },
		func(c *IntakeConfigV2) { c.Sandbox.MemoryBytes = 0 },
	} {
		bad := c
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid bound accepted")
		}
	}
}

type workflowRegistryV2 struct {
	names map[string]bool
}

func (r *workflowRegistryV2) RegisterWorkflow(any) {}

func (r *workflowRegistryV2) RegisterWorkflowWithOptions(_ any, opts workflow.RegisterOptions) {
	r.names[opts.Name] = true
}

func TestIntakeV2RegistrationPreservesV1(t *testing.T) {
	r := &workflowRegistryV2{names: make(map[string]bool)}
	RegisterWorkflow(r)
	RegisterIntakeWorkflowsV2(r)
	for _, name := range []string{WorkflowNameV1, SourceSyncNameV2, DocumentIngestNameV2, ControlReconcileNameV2} {
		if !r.names[name] {
			t.Fatalf("missing registration %s", name)
		}
	}
}

func TestIntakeV2EnvelopeAndContinuationBounds(t *testing.T) {
	c := ProposedIntakeConfigV2()
	if intakePayloadV2(strings.Repeat("x", c.EnvelopeBytes-2), c) != nil || intakePayloadV2(strings.Repeat("x", c.EnvelopeBytes-1), c) == nil {
		t.Fatal("encoded envelope boundary not enforced")
	}
	s := intakeTestStateV2()
	s.SourceDeadline = time.Now().UTC()
	if s.Validate() == nil {
		t.Fatal("document accepted inherited source deadline")
	}
	s = intakeTestStateV2()
	f := intakeTestFrameV2(s, "progress", "resume")
	for i := 0; i <= c.References; i++ {
		f.Notifications = append(f.Notifications, IntakeNotificationV2{WorkflowID: s.WorkflowID, Receipt: *f.Receipt})
	}
	if validateFrameV2(f, s) == nil {
		t.Fatal("too many references accepted")
	}
	for _, mutate := range []func(*IntakeConfigV2){
		func(c *IntakeConfigV2) { c.HistoryEventReserve = c.HistoryEvents },
		func(c *IntakeConfigV2) { c.TransactionBytes = c.HistoryBytes },
		func(c *IntakeConfigV2) { c.AdmissionScanItems = 1001 },
		func(c *IntakeConfigV2) { c.SourceInFlight = c.SourceConnections+1 },
		func(c *IntakeConfigV2) { c.TemporalMonthlyAlertCents = 0 },
		func(c *IntakeConfigV2) { c.Notifications = int(^uint(0)>>1) },
	} {
		bad := c
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid configurable reserve accepted")
		}
	}
	bindings, err := IntakeQueueBindingsV2(c, "deployment", "image")
	if err != nil || len(bindings) != 7 {
		t.Fatalf("queue bindings: %v", err)
	}
	for _, binding := range bindings {
		if binding.Options.DeploymentOptions.Version.DeploymentName != "deployment" || binding.Options.DeploymentOptions.Version.BuildID != "image" {
			t.Fatal("queue left pinned deployment")
		}
	}
	for _, op := range []string{"decode", "detect", "convert", "normalize", "gate"} {
		if intakeQueueV2(op) != ConverterTaskQueueV2 {
			t.Fatal("byte-reading operation escaped converter queue")
		}
	}
	if intakeQueueV2("structure") != ParserTaskQueueV2 || intakeQueueV2("expiry") != DatabaseTaskQueueV2 {
		t.Fatal("incorrect block-only/database routing")
	}
	b, err := json.Marshal(IntakeCompletionV2{Outcome: "cancelled"})
	if err != nil || strings.Contains(string(b), "Receipt") {
		t.Fatal("cancellation invented result")
	}
	if run.DefaultDocumentAdmissionLimits().Processing != int32(c.Processing) || run.DefaultDocumentAdmissionLimits().Open != int32(c.Open) {
		t.Fatal("proposed admission defaults diverged")
	}
}

func TestIntakeV2MaximumCommandBudget(t *testing.T) {
	c := ProposedIntakeConfigV2()
	// Each child carries one complete envelope. Reserve metadata for every
	// command and the parent result, rather than counting payload bytes alone.
	worst := (c.ChildGroup + 1) * (c.EnvelopeBytes + c.CommandOverheadBytes)
	if worst >= c.TransactionBytes || c.TransactionBytes >= 4<<20 {
		t.Fatalf("combined commands %d exceed budget %d", worst, c.TransactionBytes)
	}
}

func TestIntakeV2StartPolicies(t *testing.T) {
	s := intakeTestStateV2()
	o, err := IntakeStartOptionsV2(s)
	if err != nil {
		t.Fatal(err)
	}
	if o.ID != s.WorkflowID || o.TaskQueue != CoordinatorTaskQueueV2 || o.WorkflowIDReusePolicy != enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE || o.WorkflowIDConflictPolicy != enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING {
		t.Fatal("unsafe duplicate start policy")
	}
}

func TestIntakeV2RejectsUnusableBootstrapHeadroom(t *testing.T) {
	c := ProposedIntakeConfigV2()
	for _, mutate := range []func(*IntakeConfigV2){
		func(c *IntakeConfigV2) { c.HistoryEvents, c.HistoryEventReserve = 2, 1 },
		func(c *IntakeConfigV2) { c.HistoryEventReserve = intakeBootstrapEventFloorV2-1 },
		func(c *IntakeConfigV2) { c.HistoryEvents = c.HistoryEventReserve+intakeBootstrapEventFloorV2-1 },
		func(c *IntakeConfigV2) { c.HistoryBytes = c.TransactionBytes+c.EnvelopeBytes+c.CommandOverheadBytes-1 },
	} {
		bad := c
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("unusable history configuration accepted")
		}
	}
	c.HistoryEvents = c.HistoryEventReserve+intakeBootstrapEventFloorV2
	c.HistoryBytes = c.TransactionBytes+c.EnvelopeBytes+c.CommandOverheadBytes
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestIntakeV2BootstrapBoundaryTerminatesWithoutContinueAsNew(t *testing.T) {
	for _, kind := range []run.WorkflowKind{run.SourceSync, run.DocumentIngest, run.ControlReconcile} {
		for _, boundary := range []string{"events", "bytes", "events_over", "bytes_over", "suggested"} {
			t.Run(string(kind)+"/"+boundary, func(t *testing.T) {
				s := intakeTestStateV2()
				var fn any = DocumentIngestV2
				if kind == run.SourceSync {
					s, fn = sourceTestStateV2(), SourceSyncV2
				} else if kind == run.ControlReconcile {
					s.Kind, s.WorkflowID, fn = kind, "corpus-control/vn", ControlReconcileV2
				}
				calls := 0
				env := intakeTestEnvironmentV2(s, func(IntakeCommandV2) (IntakeFrameV2, error) {
					calls++
					return intakeTestFrameV2(s, "progress", "resume"), nil
				})
				switch boundary {
				case "events":
					env.SetCurrentHistoryLength(s.Config.HistoryEvents-s.Config.HistoryEventReserve)
				case "events_over":
					env.SetCurrentHistoryLength(s.Config.HistoryEvents-s.Config.HistoryEventReserve+1)
				case "bytes":
					env.SetCurrentHistorySize(s.Config.HistoryBytes-s.Config.TransactionBytes)
				case "bytes_over":
					env.SetCurrentHistorySize(s.Config.HistoryBytes-s.Config.TransactionBytes+1)
				case "suggested":
					env.SetContinueAsNewSuggested(true)
				}
				env.ExecuteWorkflow(fn, s)
				err := env.GetWorkflowError()
				if err == nil || !strings.Contains(err.Error(), "intake_bootstrap_history_limit") || calls != 0 {
					t.Fatalf("bootstrap did not terminate: %v activity calls %d", err, calls)
				}
			})
		}
	}
}
