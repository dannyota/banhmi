package intake

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

func TestAdmissionUnacceptedDeliveryCannotBlockFIFO(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := f.source.db.ctx
	var childReceipt string
	if err := f.source.app.QueryRow(ctx, `SELECT execution_key FROM ingest.execution WHERE workflow_id=$1`, f.request.Child.Execution.WorkflowID).Scan(&childReceipt); err != nil {
		t.Fatal(err)
	}
	unaccepted := "request-00000000000000000000000000000900"
	if _, err := f.source.ledger.RecordDelivery(ctx, f.request.Child, intakedb.InsertDeliveryRecordParams{DeduplicationKey: unaccepted,
		DeliveryKind: "launch", RecipientWorkflowID: f.request.Child.Execution.WorkflowID, ReceiptKind: "execution", ReceiptKey: childReceipt,
		ExpectedEpoch: 1, ExpectedHash: string(f.request.Child.InputDigest), DueAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	before := admissionRows(t, f)
	r := f.request
	r.DeliveryKey = unaccepted
	if _, err := f.adapter.Reserve(ctx, r); err == nil {
		t.Fatal("delivery without accepted provenance was authorized")
	}
	requireAdmissionUnchanged(t, f, before)
	heads, err := intakedb.New(f.source.app).ReadAdmissionQueueHeads(ctx)
	if err != nil || heads.Launch != f.request.DeliveryKey || heads.LaunchWaiting != 1 || heads.ResumeWaiting != 0 {
		t.Fatalf("unaccepted delivery entered queue telemetry: %+v %v", heads, err)
	}
	f.reserve(t)
}

func TestAdmissionSameSourceWindowsPreserveExactLaunchProvenance(t *testing.T) {
	for _, mismatch := range []string{"request", "source", "external", "version"} {
		t.Run(mismatch, func(t *testing.T) {
			f := newAdmissionFixture(t, true)
			ctx := f.source.db.ctx
			other := admissionSeedLaunch(t, f, 1, time.Now().Add(-time.Second))
			q := intakedb.New(f.source.app)
			for _, request := range []run.AdmissionReserveRequest{f.request, other} {
				launch, err := q.ResolveAdmissionLaunch(ctx, request.DeliveryKey)
				if err != nil || launch.WindowKey != request.Actor.Execution.Scope.Subject.LeaseKey() ||
					launch.SourceID != "source-a" || launch.ExternalID != "native-"+request.Child.Execution.Scope.Subject.LeaseKey() {
					t.Fatal("same-source windows mixed committed child provenance", launch, err)
				}
			}
			original, err := q.ResolveAdmissionLaunch(ctx, f.request.DeliveryKey)
			if err != nil {
				t.Fatal(err)
			}
			bad := run.AdmissionReserveRequest{Actor: other.Actor, Child: f.request.Child,
				DeliveryKey: "request-00000000000000000000000000000902"}
			due := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
			if _, err := f.source.ledger.RecordDelivery(ctx, bad.Child, intakedb.InsertDeliveryRecordParams{DeduplicationKey: bad.DeliveryKey,
				DeliveryKind: "launch", RecipientWorkflowID: bad.Child.Execution.WorkflowID, ReceiptKind: "execution", ReceiptKey: original.ExecutionKey,
				ExpectedEpoch: bad.Child.Execution.AdmissionEpoch, ExpectedHash: string(bad.Child.InputDigest), DueAt: due}); err != nil {
				t.Fatal(err)
			}
			var resultID, pageID int64
			var payload json.RawMessage
			if err := f.source.app.QueryRow(ctx, `SELECT r.id,p.id,r.payload FROM ingest.source_child_binding b
JOIN ingest.execution_result r ON r.id=b.acceptance_result_id JOIN ingest.discovery_page p ON p.acceptance_result_id=r.id
WHERE b.workflow_id=$1`, other.Child.Execution.WorkflowID).Scan(&resultID, &pageID, &payload); err != nil {
				t.Fatal(err)
			}
			var retained sourcePayload
			var accepted DiscoveryAcceptance
			if err := json.Unmarshal(payload, &retained); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(retained.SourcePayload, &accepted); err != nil {
				t.Fatal(err)
			}
			// This later page belongs to the other window. Possessing the exact
			// delivery and registered child cannot replace the original binding.
			accepted.Page.PageKey = "page-00000000000000000000000000000902"
			accepted.Ordinal = 1
			accepted.Page.FollowUps[0].RequestKey = bad.DeliveryKey
			accepted.Page.Observations[0].ExternalID = "native-" + bad.Child.Execution.Scope.Subject.LeaseKey()
			accepted.Page.Observations[0].VersionKey = bad.Child.Execution.Scope.Subject.LeaseKey()
			accepted.Launches = []SourceLaunch{{RequestKey: bad.DeliveryKey, Work: bad.Child, DueAt: due}}
			switch mismatch {
			case "request":
				accepted.Launches[0].RequestKey = "request-00000000000000000000000000000903"
			case "source":
				accepted.Page.Observations[0].SourceID = "source-b"
			case "external":
				accepted.Page.Observations[0].ExternalID = "native-" + other.Child.Execution.Scope.Subject.LeaseKey()
			case "version":
				accepted.Page.Observations[0].VersionKey = other.Child.Execution.Scope.Subject.LeaseKey()
			}
			retained.Work = sourceWork(other.Actor, "accept", accepted.Page.PageKey)
			retained.IdempotencyKey = run.IntakeIdempotencyKey(retained.Work)
			retained.SourcePayload = admissionJSON(t, accepted)
			cloned := admissionClone(t, f, "execution_result", resultID, map[string]any{
				"result_key": "result-00000000000000000000000000000902", "idempotency_key": retained.IdempotencyKey,
				"shard": accepted.Page.PageKey, "payload": retained})
			admissionClone(t, f, "discovery_page", pageID, map[string]any{
				"page_key": accepted.Page.PageKey, "ordinal": 1, "acceptance_result_id": cloned})
			before := admissionRows(t, f)
			heads, err := q.ReadAdmissionQueueHeads(ctx)
			if err != nil || heads.Launch != f.request.DeliveryKey || heads.LaunchWaiting != 2 || heads.ResumeWaiting != 0 {
				t.Fatal("mismatched page entered the same-source launch queue", heads, err)
			}
			if _, err := f.adapter.Reserve(ctx, bad); !errors.Is(err, run.ErrStaleGrant) {
				t.Fatal("mismatched page authorized a same-source child", err)
			}
			requireAdmissionUnchanged(t, f, before)
			f.reserve(t)
			admissionUse(t, f, other)
		})
	}
}

func TestAdmissionLaterAcceptedPageReusesFirstChildBinding(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := f.source.db.ctx
	var originalResult, originalPage, firstBinding int64
	var payload json.RawMessage
	if err := f.source.app.QueryRow(ctx, `SELECT r.id,p.id,b.acceptance_result_id,r.payload FROM ingest.source_child_binding b
JOIN ingest.execution_result r ON r.id=b.acceptance_result_id JOIN ingest.discovery_page p ON p.acceptance_result_id=r.id
WHERE b.workflow_id=$1`, f.request.Child.Execution.WorkflowID).Scan(&originalResult, &originalPage, &firstBinding, &payload); err != nil {
		t.Fatal(err)
	}
	var retained sourcePayload
	var accepted DiscoveryAcceptance
	if err := json.Unmarshal(payload, &retained); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(retained.SourcePayload, &accepted); err != nil {
		t.Fatal(err)
	}
	// A second accepted page keeps the same source record and child, while
	// committing its own delivery. The immutable binding retains the first ID.
	later := "request-00000000000000000000000000000901"
	accepted.Page.PageKey = "page-00000000000000000000000000000901"
	accepted.Ordinal = 1
	accepted.Page.FollowUps[0].RequestKey = later
	accepted.Launches[0].RequestKey = later
	retained.Work = sourceWork(f.request.Actor, "accept", accepted.Page.PageKey)
	retained.IdempotencyKey = run.IntakeIdempotencyKey(retained.Work)
	retained.SourcePayload = admissionJSON(t, accepted)
	resultID := admissionClone(t, f, "execution_result", originalResult, map[string]any{
		"result_key": "result-00000000000000000000000000000901", "idempotency_key": retained.IdempotencyKey,
		"shard": accepted.Page.PageKey, "payload": retained})
	admissionClone(t, f, "discovery_page", originalPage, map[string]any{
		"page_key": accepted.Page.PageKey, "ordinal": 1, "acceptance_result_id": resultID})
	var delivery intakedb.IngestDeliveryRecord
	delivery, err := intakedb.New(f.source.app).LockAdmissionDelivery(ctx, f.request.DeliveryKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.source.ledger.RecordDelivery(ctx, f.request.Child, intakedb.InsertDeliveryRecordParams{DeduplicationKey: later,
		DeliveryKind: "launch", RecipientWorkflowID: delivery.RecipientWorkflowID, ReceiptKind: "execution", ReceiptKey: delivery.ReceiptKey,
		ExpectedEpoch: delivery.ExpectedEpoch, ExpectedHash: delivery.ExpectedHash, DueAt: accepted.Launches[0].DueAt}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.source.sources.AcknowledgeLaunch(ctx, sourceWork(f.request.Actor, "ack-first", f.request.DeliveryKey),
		SourceLaunchAcknowledgement{WindowKey: "window-a", RequestKey: f.request.DeliveryKey, Child: f.request.Child, ExecutionKey: delivery.ReceiptKey}); err != nil {
		t.Fatal(err)
	}
	f.request.DeliveryKey = later
	f.reserve(t)
	var retainedBinding int64
	if err := f.source.app.QueryRow(ctx, `SELECT acceptance_result_id FROM ingest.source_child_binding WHERE workflow_id=$1`, f.request.Child.Execution.WorkflowID).Scan(&retainedBinding); err != nil || retainedBinding != firstBinding {
		t.Fatal("admission changed the first accepted child binding")
	}
}

func TestAdmissionSourceTransferPreservesHeadAndTurn(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := f.source.db.ctx
	later := admissionSeedLaunch(t, f, 1, time.Now().Add(-time.Second))
	if _, err := f.source.sources.TransferLaunch(ctx, sourceWork(f.request.Actor, "transfer", f.request.DeliveryKey),
		SourceLaunchTransfer{RequestKey: f.request.DeliveryKey, RecipientWorkflowID: f.control.Execution.WorkflowID}); err != nil {
		t.Fatal(err)
	}
	before := admissionRows(t, f)
	if _, err := f.adapter.Reserve(ctx, f.request); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatal("source retained transferred launch authority")
	}
	if refused, err := f.adapter.Reserve(ctx, later); err != nil || refused.Refusal != run.AdmissionNotTurn {
		t.Fatal("transfer allowed later launch to overtake head")
	}
	requireAdmissionUnchanged(t, f, before)
	restarted, err := NewDocumentAdmission(f.source.app, "writer-a", f.adapter.observer)
	if err != nil {
		t.Fatal(err)
	}
	f.adapter = restarted
	r := f.request
	r.Actor = f.control
	admissionUse(t, f, r)
	var turn string
	if err := f.source.app.QueryRow(ctx, `SELECT next_queue FROM ingest.document_admission_limit`).Scan(&turn); err != nil || turn != "launch" {
		t.Fatal("transfer, restart or uncontested admission reset the turn")
	}
}

func TestAdmissionReserveReplayUnderReclaimedSourceGrant(t *testing.T) {
	f := newAdmissionFixture(t, true)
	first := f.reserve(t)
	f.source.reclaimWindow(t, 1)
	f.request.Actor = f.source.window
	before := admissionRows(t, f)
	if replay := f.reserve(t); replay != first {
		t.Fatal("fresh source grant changed the original reservation receipt")
	}
	requireAdmissionUnchanged(t, f, before)
}
