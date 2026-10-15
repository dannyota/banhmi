package intake

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"danny.vn/banhmi/internal/corpus/run"
)

func TestAdmissionCancelledControlCannotReplayWorkerLossAfterPreflight(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := f.source.db.ctx
	reserved := f.reserve(t)
	if _, err := f.source.app.Exec(ctx, `UPDATE ingest.execution_lease SET expires_at=clock_timestamp()-interval '1 second' WHERE resource_kind='document_version'`); err != nil {
		t.Fatal(err)
	}
	observing, resume := make(chan struct{}), make(chan struct{})
	conn := sourceAppConnection(t, f.source.db)
	adapter, err := NewDocumentAdmission(conn, "writer-a", admissionObserverFunc(func(ctx context.Context, workflow string) (run.DocumentExecutionObservation, error) {
		close(observing)
		select {
		case <-resume:
			return run.DocumentExecutionObservation{WorkflowID: workflow, Status: run.DocumentExecutionUnknown}, nil
		case <-ctx.Done():
			return run.DocumentExecutionObservation{}, ctx.Err()
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	change := admissionChange(f, reserved, true)
	go func() {
		_, err := adapter.Reconcile(ctx, change)
		done <- err
	}()
	select {
	case <-observing:
	case <-ctx.Done():
		t.Fatal("reconciler did not finish preflight")
	}
	// A separate transaction commits exactly the receipt the first caller
	// did not see. Cancellation happens before that caller enters change().
	if loss, err := f.adapter.Reconcile(ctx, change); err != nil || loss.State != run.AdmissionOpenOnly {
		t.Fatalf("competing worker-loss transition: %+v %v", loss, err)
	}
	if _, err := f.source.app.Exec(ctx, `UPDATE ingest.execution_lease SET cancelled_at=clock_timestamp() WHERE resource_kind='control'`); err != nil {
		t.Fatal(err)
	}
	before := admissionRows(t, f)
	close(resume)
	select {
	case err := <-done:
		if !errors.Is(err, run.ErrStaleGrant) {
			t.Fatal("cancelled control replayed a newly committed worker-loss receipt", err)
		}
	case <-ctx.Done():
		t.Fatal("replay did not finish")
	}
	requireAdmissionUnchanged(t, f, before)
	if _, err := f.adapter.Reconcile(ctx, change); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatal("cancelled control replayed worker loss during preflight", err)
	}
	requireAdmissionUnchanged(t, f, before)
}

func TestAdmissionSupersededCloseRetainsComparedGrantAfterLaterReclaim(t *testing.T) {
	for _, method := range []string{"failure", "abandonment"} {
		t.Run(method, func(t *testing.T) {
			f := newAdmissionFixture(t, true)
			ctx := f.source.db.ctx
			reserved := f.reserve(t)
			replacement := admissionReclaimDocument(t, f, "same")
			change := admissionChange(f, reserved, true)
			closeRequest := run.AdmissionCloseRequest{Change: change, Reason: run.AdmissionAbandoned}
			var receipt run.AdmissionReceipt
			var err error
			if method == "failure" {
				f.status = run.DocumentExecutionTerminal
				receipt, err = f.adapter.Reconcile(ctx, change)
			} else {
				receipt, err = f.adapter.Close(ctx, closeRequest)
			}
			if err != nil || receipt.State != run.AdmissionClosing {
				t.Fatalf("superseded close: %+v %v", receipt, err)
			}
			var cancelled bool
			if err := f.source.app.QueryRow(ctx, `SELECT cancelled_at IS NOT NULL FROM ingest.execution_lease WHERE resource_kind='document_version'`).Scan(&cancelled); err != nil || cancelled {
				t.Fatal("close cancelled the compared replacement grant")
			}
			later := admissionReclaimDocument(t, f, "same")
			if later.Execution.AttemptFence <= replacement.Execution.AttemptFence {
				t.Fatal("fixture did not reclaim the replacement grant")
			}
			before := admissionRows(t, f)
			var raw json.RawMessage
			if err := f.source.app.QueryRow(ctx, `SELECT response FROM ingest.document_admission_receipt WHERE receipt_key=$1`, receipt.Key).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var retained admissionStoredResponse
			if err := json.Unmarshal(raw, &retained); err != nil {
				t.Fatal(err)
			}
			want := admissionLeaseTuple{Epoch: replacement.Execution.AdmissionEpoch, Owner: replacement.Execution.Owner, Fence: replacement.Execution.AttemptFence}
			if retained.LeaseDisposition != "superseded" || retained.ComparedLease == nil || *retained.ComparedLease != want {
				t.Fatalf("receipt lost the grant actually compared: %+v", retained)
			}
			var replay run.AdmissionReceipt
			if method == "failure" {
				replay, err = f.adapter.Reconcile(ctx, change)
			} else {
				replay, err = f.adapter.Close(ctx, closeRequest)
			}
			if err != nil || replay != receipt {
				t.Fatal("later reclaim changed the close receipt", err)
			}
			if err := f.source.app.QueryRow(ctx, `SELECT cancelled_at IS NOT NULL FROM ingest.execution_lease WHERE resource_kind='document_version'`).Scan(&cancelled); err != nil || cancelled {
				t.Fatal("close replay cancelled the later grant")
			}
			requireAdmissionUnchanged(t, f, before)
		})
	}
}
