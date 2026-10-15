package intake

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/run"
)

func TestAdmissionBindAndCloseSerializeBothOrders(t *testing.T) {
	for _, bindFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(bindFirst), func(t *testing.T) {
			f := newAdmissionFixture(t, true)
			ctx := f.source.db.ctx
			reserved := f.reserve(t)
			if _, err := f.source.app.Exec(ctx, `UPDATE ingest.execution_lease SET expires_at=clock_timestamp()-interval '1 second' WHERE resource_kind='document_version'`); err != nil {
				t.Fatal(err)
			}
			fresh := f.request.Child
			fresh.Execution.Owner = "owner-recovered"
			lease, err := f.source.ledger.ReclaimLease(ctx, 1, fresh.Execution.Scope, fresh.Execution.Owner, time.Now().Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			fresh.Execution.AttemptFence = lease.AttemptFence
			if _, err := f.source.ledger.RegisterExecution(ctx, fresh, "recovered-admission-run", nil); err != nil {
				t.Fatal(err)
			}
			winnerConn := sourceAppConnection(t, f.source.db)
			loserConn := sourceAppConnection(t, f.source.db)
			gate := &admissionCommitStarter{TransactionStarter: winnerConn, entered: make(chan struct{}), release: make(chan struct{}), commit: true}
			winner, err := NewDocumentAdmission(gate, "writer-a", f.adapter.observer)
			if err != nil {
				t.Fatal(err)
			}
			loser, err := NewDocumentAdmission(loserConn, "writer-a", f.adapter.observer)
			if err != nil {
				t.Fatal(err)
			}
			bind := func(a *DocumentAdmission) error {
				_, err := a.Bind(ctx, run.AdmissionChange{Actor: fresh, SlotKey: reserved.SlotKey, ExpectedRevision: reserved.Revision})
				return err
			}
			stop := func(a *DocumentAdmission) error {
				_, err := a.Close(ctx, run.AdmissionCloseRequest{Change: admissionChange(f, reserved, true), Reason: run.AdmissionAbandoned})
				return err
			}
			first, second := stop, bind
			if bindFirst {
				first, second = bind, stop
			}
			won := make(chan error, 1)
			go func() { won <- first(winner) }()
			select {
			case <-gate.entered:
			case <-ctx.Done():
				t.Fatal("first transition did not reach commit barrier")
			}
			lost := make(chan error, 1)
			go func() { lost <- second(loser) }()
			waitAdapterBlockedBy(t, ctx, f.source.db.conn, loserConn.PgConn().PID(), winnerConn.PgConn().PID())
			close(gate.release)
			if err := <-won; err != nil {
				t.Fatal(err)
			}
			if err := <-lost; !errors.Is(err, run.ErrAdmissionConflict) {
				t.Fatalf("losing transition bypassed revision/state fence: %v", err)
			}
			slot := admissionCurrentSlot(t, f, reserved)
			want := "closing"
			if bindFirst {
				want = "processing"
			}
			if slot.State != want || slot.Revision != reserved.Revision+1 {
				t.Fatalf("race result: %+v", slot)
			}
			var cancelled bool
			if err := f.source.app.QueryRow(ctx, `SELECT cancelled_at IS NOT NULL FROM ingest.execution_lease WHERE resource_kind='document_version'`).Scan(&cancelled); err != nil || cancelled {
				t.Fatal("race cancelled the unrelated reclaimed grant")
			}
			before := admissionRows(t, f)
			if err := second(f.adapter); !errors.Is(err, run.ErrAdmissionConflict) {
				t.Fatal("losing transition retry succeeded")
			}
			requireAdmissionUnchanged(t, f, before)
		})
	}
}

func TestAdmissionAbandonmentRejectsAcknowledgementAndWrongActor(t *testing.T) {
	for _, name := range []string{"acknowledged", "revision", "source", "country"} {
		t.Run(name, func(t *testing.T) {
			f := newAdmissionFixture(t, true)
			ctx := f.source.db.ctx
			reserved := f.reserve(t)
			if _, err := f.source.app.Exec(ctx, `UPDATE ingest.execution_lease SET expires_at=clock_timestamp()-interval '1 second' WHERE resource_kind='document_version'`); err != nil {
				t.Fatal(err)
			}
			r := run.AdmissionCloseRequest{Change: admissionChange(f, reserved, true), Reason: run.AdmissionAbandoned}
			switch name {
			case "acknowledged":
				if _, err := f.source.app.Exec(ctx, `UPDATE ingest.delivery_record SET acknowledged_at=clock_timestamp() WHERE deduplication_key=$1`, f.request.DeliveryKey); err != nil {
					t.Fatal(err)
				}
			case "revision":
				r.Change.ExpectedRevision++
			case "source":
				r.Change.Actor = f.request.Actor
			case "country":
				r.Change.Actor.Execution.Country = "my"
			}
			before := admissionRows(t, f)
			if _, err := f.adapter.Close(context.WithoutCancel(ctx), r); err == nil {
				t.Fatal("unproved stop succeeded")
			}
			requireAdmissionUnchanged(t, f, before)
		})
	}
}
