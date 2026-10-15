package intake

import (
	"errors"
	"fmt"
	"testing"

	"danny.vn/banhmi/internal/corpus/run"
)

func TestAdmissionRotationSerializesWithReserve(t *testing.T) {
	for _, reserveFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(reserveFirst), func(t *testing.T) {
			f := newAdmissionFixture(t, true)
			ctx := f.source.db.ctx
			firstConn := sourceAppConnection(t, f.source.db)
			secondConn := sourceAppConnection(t, f.source.db)
			gate := &admissionCommitStarter{TransactionStarter: firstConn, entered: make(chan struct{}), release: make(chan struct{}), commit: true}
			firstAdmission, err := NewDocumentAdmission(gate, "writer-a", f.adapter.observer)
			if err != nil {
				t.Fatal(err)
			}
			firstLedger, err := NewLedger(gate, "writer-a", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			secondAdmission, err := NewDocumentAdmission(secondConn, "writer-a", f.adapter.observer)
			if err != nil {
				t.Fatal(err)
			}
			secondLedger, err := NewLedger(secondConn, "writer-a", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			reserve := func(a *DocumentAdmission) error {
				r, err := a.Reserve(ctx, f.request)
				if err == nil && r.Receipt == nil {
					return errors.New("reservation was refused")
				}
				return err
			}
			rotate := func(l *Ledger) error {
				_, err := l.RotateAdmission(ctx, 2, "writer-a", true)
				return err
			}
			first := make(chan error, 1)
			go func() {
				if reserveFirst {
					first <- reserve(firstAdmission)
				} else {
					first <- rotate(firstLedger)
				}
			}()
			select {
			case <-gate.entered:
			case <-ctx.Done():
				t.Fatal("first operation did not reach commit barrier")
			}
			second := make(chan error, 1)
			go func() {
				if reserveFirst {
					second <- rotate(secondLedger)
				} else {
					second <- reserve(secondAdmission)
				}
			}()
			waitAdapterBlockedBy(t, ctx, f.source.db.conn, secondConn.PgConn().PID(), firstConn.PgConn().PID())
			close(gate.release)
			if err := <-first; err != nil {
				t.Fatal(err)
			}
			if err := <-second; reserveFirst && err != nil || !reserveFirst && !errors.Is(err, run.ErrStaleGrant) {
				t.Fatalf("rotation order: %v", err)
			}
			var slots int
			if err := f.source.app.QueryRow(ctx, `SELECT count(*) FROM ingest.document_admission_slot`).Scan(&slots); err != nil || slots != map[bool]int{true: 1, false: 0}[reserveFirst] {
				t.Fatal("rotation erased a committed reservation or admitted an obsolete epoch")
			}
			before := admissionRows(t, f)
			if _, err := f.adapter.Reserve(ctx, f.request); !errors.Is(err, run.ErrStaleGrant) {
				t.Fatal("old epoch replay retained authority")
			}
			requireAdmissionUnchanged(t, f, before)
		})
	}
}
