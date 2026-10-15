package intake

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

// Receipt keys have a fixed encoded length, so synthetic keys give an exact
// canonical size before the operation chooses its random receipt key.
func admissionBoundaryLimit(t *testing.T, request admissionStoredRequest, response admissionStoredResponse) int32 {
	t.Helper()
	response.Receipt.Key = "admission-00000000000000000000000000000000"
	return int32(max(len(admissionJSON(t, request)), len(admissionJSON(t, response))))
}

func admissionAssertBoundary(t *testing.T, f *admissionFixture, receipt run.AdmissionReceipt, limit int32) {
	t.Helper()
	var retained intakedb.IngestDocumentAdmissionReceipt
	if err := f.source.app.QueryRow(f.source.db.ctx, `SELECT request,response FROM ingest.document_admission_receipt WHERE receipt_key=$1`, receipt.Key).
		Scan(&retained.Request, &retained.Response); err != nil {
		t.Fatal(err)
	}
	var request admissionStoredRequest
	var response admissionStoredResponse
	if err := json.Unmarshal(retained.Request, &request); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(retained.Response, &response); err != nil {
		t.Fatal(err)
	}
	if got := admissionBoundaryLimit(t, request, response); got != limit || max(len(retained.Request), len(retained.Response)) <= int(limit) {
		t.Fatalf("boundary fixture: canonical=%d frozen=%d raw=%d", got, limit, max(len(retained.Request), len(retained.Response)))
	}
}

func TestAdmissionCommittedBoundaryConfigureSurvivesLowering(t *testing.T) {
	f := newAdmissionFixture(t, false)
	ctx := f.source.db.ctx
	var actorID int64
	if err := f.source.app.QueryRow(ctx, `SELECT id FROM ingest.execution WHERE workflow_id=$1`, f.control.Execution.WorkflowID).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	r := run.AdmissionConfigureRequest{Actor: f.control, Limits: run.DefaultDocumentAdmissionLimits()}
	r.Limits.Processing, r.Limits.Open = math.MaxInt32, math.MaxInt32
	for range 4 {
		p := admissionPayload{Method: "configure", Country: "vn", Target: "limits", Limits: &r.Limits}
		r.Limits.ReceiptJSONBytes = admissionBoundaryLimit(t,
			admissionStoredRequest{Payload: p, Actor: admissionWorkBinding(f.control, actorID)},
			admissionStoredResponse{Operation: "configure", LimitsRevision: 1, Receipt: run.AdmissionReceipt{Revision: 1}})
	}
	first, err := f.adapter.Configure(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	admissionAssertBoundary(t, f, first, r.Limits.ReceiptJSONBytes)
	before := admissionRows(t, f)
	if replay, err := f.adapter.Configure(ctx, r); err != nil || replay != first {
		t.Fatalf("initial boundary replay: %+v %v", replay, err)
	}
	requireAdmissionUnchanged(t, f, before)
	lowered := r
	lowered.ExpectedRevision = 1
	lowered.Limits.Processing, lowered.Limits.Open = 1, 1
	lowered.Limits.ReceiptJSONBytes--
	if _, err := f.adapter.Configure(ctx, lowered); err != nil {
		t.Fatal("lowering configuration could not retain its own receipt", err)
	}
	before = admissionRows(t, f)
	if replay, err := f.adapter.Configure(ctx, r); err != nil || replay != first {
		t.Fatalf("lowered ceiling invalidated initial receipt: %+v %v", replay, err)
	}
	requireAdmissionUnchanged(t, f, before)
}

func TestAdmissionCommittedBoundaryReserveAndCorruption(t *testing.T) {
	for _, corruption := range []string{"none", "raw", "canonical", "shape"} {
		t.Run(corruption, func(t *testing.T) {
			f := newAdmissionFixture(t, false)
			ctx := f.source.db.ctx
			var actorID, childID int64
			if err := f.source.app.QueryRow(ctx, `SELECT (SELECT id FROM ingest.execution WHERE workflow_id=$1),(SELECT id FROM ingest.execution WHERE workflow_id=$2)`, f.request.Actor.Execution.WorkflowID, f.request.Child.Execution.WorkflowID).
				Scan(&actorID, &childID); err != nil {
				t.Fatal(err)
			}
			p := admissionPayload{Method: "reserve", Country: "vn", Target: f.request.DeliveryKey, Child: admissionChildPayload(f.request.Child)}
			limit := admissionBoundaryLimit(t,
				admissionStoredRequest{Payload: p, Actor: admissionWorkBinding(f.request.Actor, actorID), Binding: admissionWorkBinding(f.request.Child, childID)},
				admissionStoredResponse{Operation: "reserve", LimitsRevision: 1, Processing: 1, Open: 1,
					Receipt: run.AdmissionReceipt{SlotKey: "slot-00000000000000000000000000000000", Revision: 1, State: run.AdmissionReserved}})
			limits := run.DefaultDocumentAdmissionLimits()
			limits.ReceiptJSONBytes = limit
			if _, err := f.adapter.Configure(ctx, run.AdmissionConfigureRequest{Actor: f.control, Limits: limits}); err != nil {
				t.Fatal(err)
			}
			first := f.reserve(t)
			admissionAssertBoundary(t, f, first, limit)
			before := admissionRows(t, f)
			if replay := f.reserve(t); replay != first {
				t.Fatal("boundary replay changed receipt")
			}
			requireAdmissionUnchanged(t, f, before)
			limits.ReceiptJSONBytes = limit - 1
			if _, err := f.adapter.Configure(ctx, run.AdmissionConfigureRequest{Actor: f.control, ExpectedRevision: 1, Limits: limits}); err != nil {
				t.Fatal(err)
			}
			if corruption != "none" {
				// The migration owner corrupts only this isolated synthetic receipt.
				if _, err := f.source.db.conn.Exec(ctx, `ALTER TABLE ingest.document_admission_receipt DISABLE TRIGGER USER`); err != nil {
					t.Fatal(err)
				}
				statement := `UPDATE ingest.document_admission_receipt SET receipt_json_bytes=1 WHERE receipt_key=$1`
				if corruption == "canonical" {
					statement = `UPDATE ingest.document_admission_receipt SET receipt_json_bytes=receipt_json_bytes-1 WHERE receipt_key=$1`
				} else if corruption == "shape" {
					statement = `UPDATE ingest.document_admission_receipt SET request='{}'::jsonb WHERE receipt_key=$1`
				}
				if _, err := f.source.db.conn.Exec(ctx, statement, first.Key); err != nil {
					t.Fatal(err)
				}
				if _, err := f.source.db.conn.Exec(ctx, `ALTER TABLE ingest.document_admission_receipt ENABLE TRIGGER USER`); err != nil {
					t.Fatal(err)
				}
			}
			before = admissionRows(t, f)
			replayed, err := f.adapter.Reserve(ctx, f.request)
			if corruption == "none" {
				if err != nil || replayed.Receipt == nil || *replayed.Receipt != first {
					t.Fatal("lowering invalidated ordinary receipt", err)
				}
			} else if !errors.Is(err, run.ErrAdmissionIntegrity) {
				t.Fatalf("corrupt %s receipt replayed: %v", corruption, err)
			}
			requireAdmissionUnchanged(t, f, before)
		})
	}
}
