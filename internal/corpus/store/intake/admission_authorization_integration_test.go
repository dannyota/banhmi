package intake

import (
	"errors"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
)

func TestAdmissionResolveAndScanRejectWithoutWrites(t *testing.T) {
	for _, name := range []string{"writer", "epoch", "owner", "fence", "country", "expired", "cancelled", "unregistered", "inputs", "workflow", "foreign-document", "source"} {
		t.Run(name, func(t *testing.T) {
			f := newAdmissionFixture(t, true)
			f.reserve(t)
			ctx := f.source.db.ctx
			actor := f.control
			target := f.request.Child
			switch name {
			case "writer":
				f.adapter.ledger.writer = "writer-b"
			case "epoch":
				actor.Execution.AdmissionEpoch++
			case "owner":
				actor.Execution.Owner = "owner-b"
			case "fence":
				actor.Execution.AttemptFence++
			case "country":
				actor.Execution.Country = "my"
			case "expired", "cancelled":
				column, value := "expires_at", "clock_timestamp()-interval '1 second'"
				if name == "cancelled" {
					column, value = "cancelled_at", "clock_timestamp()"
				}
				if _, err := f.source.db.conn.Exec(ctx, "UPDATE ingest.execution_lease SET "+column+"="+value+" WHERE resource_kind='control'"); err != nil {
					t.Fatal(err)
				}
			case "unregistered":
				actor.Execution.WorkflowID = "corpus-control/unregistered"
			case "inputs":
				target.InputDigest = model.Digest(strings.Repeat("b", 64))
			case "workflow":
				target.Execution.Scope.Subject.DocumentVersion = "missing-version"
				target.Execution.WorkflowID = "document-ingest/vn/missing-version/attempt-a"
			case "foreign-document":
				actor = admissionSeedLaunch(t, f, 1, time.Now().Add(-time.Second)).Child
			case "source":
				actor = f.request.Actor
			}
			before := admissionRows(t, f)
			if _, err := f.adapter.Resolve(ctx, run.AdmissionResolveRequest{Actor: actor, DocumentWork: &target}); err == nil {
				t.Fatal("unauthorized resolution succeeded")
			}
			requireAdmissionUnchanged(t, f, before)
			// Target-only changes do not invalidate the control actor's scan.
			if name != "inputs" && name != "workflow" {
				if _, err := f.adapter.Scan(ctx, run.AdmissionScan{Actor: actor, Limit: 1}); err == nil {
					t.Fatal("unauthorized scan succeeded")
				}
				requireAdmissionUnchanged(t, f, before)
			}
		})
	}
}

func TestAdmissionResolutionAfterRebindIsNotMutationAuthority(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := f.source.db.ctx
	reserved := f.reserve(t)
	old, err := f.adapter.Resolve(ctx, run.AdmissionResolveRequest{Actor: f.request.Child, DocumentWork: &f.request.Child})
	if err != nil || old.Revision != reserved.Revision {
		t.Fatal(err)
	}
	bound, child := admissionLostBinding(t, f, reserved, f.request.Child)
	before := admissionRows(t, f)
	current, err := f.adapter.Resolve(ctx, run.AdmissionResolveRequest{Actor: child, DocumentWork: &child})
	if err != nil || current.Binding.Fence != child.Execution.AttemptFence || current.Revision != bound.Revision {
		t.Fatalf("fresh owner could not resolve rebound work: %+v %v", current, err)
	}
	if _, err := f.adapter.Bind(ctx, run.AdmissionChange{Actor: child, SlotKey: old.SlotKey, ExpectedRevision: old.Revision}); !errors.Is(err, run.ErrAdmissionConflict) {
		t.Fatal("old snapshot bypassed revision fence")
	}
	requireAdmissionUnchanged(t, f, before)
	items, err := f.adapter.Scan(ctx, run.AdmissionScan{Actor: f.control, Limit: 1})
	if err != nil || len(items) != 1 || items[0].Revision != bound.Revision {
		t.Fatalf("control sweep: %+v %v", items, err)
	}
	requireAdmissionUnchanged(t, f, before)
}

func TestAdmissionAdditionalReservationNegativesHaveNoWrites(t *testing.T) {
	for _, name := range []string{"future", "acknowledged", "untransferred-control", "child-owner", "child-fence", "oversized-initial-configure", "oversized-reserve"} {
		t.Run(name, func(t *testing.T) {
			f := newAdmissionFixture(t, name != "oversized-initial-configure")
			ctx := f.source.db.ctx
			r := f.request
			switch name {
			case "future":
				r = admissionSeedLaunch(t, f, 1, time.Now().Add(time.Hour))
			case "acknowledged":
				if _, err := f.source.app.Exec(ctx, `UPDATE ingest.delivery_record SET acknowledged_at=clock_timestamp() WHERE deduplication_key=$1`, r.DeliveryKey); err != nil {
					t.Fatal(err)
				}
			case "untransferred-control":
				r.Actor = f.control
			case "child-owner":
				r.Child.Execution.Owner = "owner-b"
			case "child-fence":
				r.Child.Execution.AttemptFence++
			case "oversized-reserve":
				limits := run.DefaultDocumentAdmissionLimits()
				limits.ReceiptJSONBytes = 512
				if _, err := f.adapter.Configure(ctx, run.AdmissionConfigureRequest{Actor: f.control, ExpectedRevision: 1, Limits: limits}); err != nil {
					t.Fatal(err)
				}
			}
			before := admissionRows(t, f)
			if name == "oversized-initial-configure" {
				limits := run.DefaultDocumentAdmissionLimits()
				limits.ReceiptJSONBytes = 1
				if _, err := f.adapter.Configure(ctx, run.AdmissionConfigureRequest{Actor: f.control, Limits: limits}); !errors.Is(err, run.ErrAdmissionIntegrity) {
					t.Fatal("oversized initial receipt was retained", err)
				}
			} else if _, err := f.adapter.Reserve(ctx, r); err == nil {
				t.Fatal("invalid reservation succeeded")
			}
			requireAdmissionUnchanged(t, f, before)
		})
	}
}
