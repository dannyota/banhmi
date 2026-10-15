package intake

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

// admissionPayload excludes the authorizing actor and mutable child grant.
// Declaration order is the digest order; these values never enter transport.
type admissionPayload struct {
	Method   string
	Country  string
	Target   string
	Revision int64
	Child    *run.IntakeWork
	Limits   *run.DocumentAdmissionLimits
	Reason   run.AdmissionTerminalReason
	Cause    string
}

type admissionStoredRequest struct {
	Payload admissionPayload
	Actor   run.AdmissionBinding
	Binding run.AdmissionBinding
}

type admissionStoredResponse struct {
	Operation        string
	PriorState       string
	Receipt          run.AdmissionReceipt
	LimitsRevision   int64
	Processing       int64
	Open             int64
	Observation      run.DocumentExecutionObservation
	LeaseDisposition string
	LostGrantCause   string
	ComparedLease    *admissionLeaseTuple `json:",omitempty"`
}

// This tuple is receipt audit data, never authority over a replacement grant.
type admissionLeaseTuple struct {
	Epoch int64
	Owner string
	Fence int64
}

func admissionComparedLease(lease intakedb.IngestExecutionLease) *admissionLeaseTuple {
	return &admissionLeaseTuple{Epoch: lease.AdmissionEpoch, Owner: lease.Owner, Fence: lease.AttemptFence}
}

func admissionHash(v any) string {
	h := sha256.New()
	admissionHashValue(h, reflect.ValueOf(v))
	return hex.EncodeToString(h.Sum(nil))
}

func admissionHashValue(h hash.Hash, v reflect.Value) {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			_, _ = h.Write([]byte{0})
			return
		}
		_, _ = h.Write([]byte{1})
		admissionHashValue(h, v.Elem())
		return
	}
	if v.Type() == reflect.TypeFor[time.Time]() {
		admissionHashInt(h, v.Interface().(time.Time).UTC().UnixMicro())
		return
	}
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			admissionHashValue(h, v.Field(i))
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			admissionHashValue(h, v.Index(i))
		}
	case reflect.String:
		admissionHashInt(h, int64(len(v.String())))
		_, _ = io.WriteString(h, v.String())
	case reflect.Int, reflect.Int32, reflect.Int64:
		admissionHashInt(h, v.Int())
	case reflect.Uint8:
		admissionHashInt(h, int64(v.Uint()))
	}
}

func admissionHashInt(h hash.Hash, n int64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(n))
	_, _ = h.Write(b[:])
}

func admissionOperation(p admissionPayload) string {
	return "sha256:" + admissionHash(struct {
		Domain, Country, Method, Target string
		Revision                        int64
	}{"country-admission-v1", p.Country, p.Method, p.Target, p.Revision})
}

func admissionRawBound(n, limit int64) bool {
	return n >= 0 && limit >= 1 && limit <= math.MaxInt32 && !(n > limit && n-limit > limit)
}

func admissionEncode(v any, limit int64) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode admission receipt: %w", err)
	}
	if limit < 1 || limit > math.MaxInt32 || int64(len(b)) > limit {
		return nil, run.ErrAdmissionIntegrity
	}
	return b, nil
}

func admissionDecode(raw json.RawMessage, limit int64, target any) error {
	trimmed := bytes.TrimSpace(raw)
	if !admissionRawBound(int64(len(raw)), limit) || len(trimmed) == 0 || trimmed[0] != '{' {
		return run.ErrAdmissionIntegrity
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return errors.Join(run.ErrAdmissionIntegrity, err)
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return run.ErrAdmissionIntegrity
	}
	_, err := admissionEncode(target, limit)
	return err
}

func admissionReplay(ctx context.Context, q intakedb.Querier, p admissionPayload) (*admissionStoredResponse, error) {
	r, err := q.FindDocumentAdmissionReceipt(ctx, admissionOperation(p))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	request, response, err := admissionRetainedReceipt(r)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(request.Payload, p) {
		return nil, run.ErrPayloadConflict
	}
	if r.SlotID != nil {
		slot, err := q.ReadDocumentAdmissionSlot(ctx, response.Receipt.SlotKey)
		if err != nil || slot.ID != *r.SlotID || response.Receipt.Revision > slot.Revision {
			return nil, errors.Join(run.ErrAdmissionIntegrity, err)
		}
	}
	return &response, nil
}

func admissionRetainedReceipt(r intakedb.IngestDocumentAdmissionReceipt) (admissionStoredRequest, admissionStoredResponse, error) {
	var request admissionStoredRequest
	var response admissionStoredResponse
	limit := int64(r.ReceiptJsonBytes)
	if err := admissionDecode(r.Request, limit, &request); err != nil {
		return request, response, err
	}
	if err := admissionDecode(r.Response, limit, &response); err != nil {
		return request, response, err
	}
	if r.LimitsRevision < 1 || r.Operation != response.Operation || r.OperationKey != admissionOperation(request.Payload) ||
		r.PayloadDigest != admissionHash(request.Payload) || response.Receipt.Key != r.ReceiptKey || response.LimitsRevision != r.LimitsRevision ||
		request.Actor.ExecutionID != r.ActorExecutionID || request.Actor.Epoch < 1 || request.Actor.Fence < 1 || request.Actor.Owner == "" ||
		response.Receipt.Revision < 1 || (r.SlotID == nil) != (response.Receipt.SlotKey == "") ||
		(r.SlotID != nil && (!response.Receipt.State.Valid() || request.Binding.ExecutionID < 1)) ||
		(r.SlotID == nil && (response.Receipt.State != "" || response.Receipt.Revision != r.LimitsRevision)) ||
		(request.Payload.Method != "reconcile" && r.Operation != request.Payload.Method) {
		return request, response, run.ErrAdmissionIntegrity
	}
	if response.LeaseDisposition != "" && (response.ComparedLease == nil || response.ComparedLease.Epoch < 1 || response.ComparedLease.Fence < 1 || response.ComparedLease.Owner == "") {
		return request, response, run.ErrAdmissionIntegrity
	}
	return request, response, nil
}

func admissionEffect(ctx context.Context, q intakedb.Querier, slot intakedb.IngestDocumentAdmissionSlot, operation string) (admissionStoredResponse, error) {
	r, err := q.FindAdmissionEffect(ctx, intakedb.FindAdmissionEffectParams{SlotID: &slot.ID, Operation: operation})
	if err != nil {
		return admissionStoredResponse{}, errors.Join(run.ErrAdmissionIntegrity, err)
	}
	_, response, err := admissionRetainedReceipt(r)
	return response, err
}

func admissionWriteReceipt(ctx context.Context, q intakedb.Querier, limit intakedb.IngestDocumentAdmissionLimit,
	p admissionPayload, actor run.AdmissionBinding, slot *intakedb.IngestDocumentAdmissionSlot, response admissionStoredResponse) (admissionStoredResponse, error) {
	var zero admissionStoredResponse
	if limit.ReceiptJsonBytes == nil || limit.Revision < 1 {
		return zero, run.ErrAdmissionUnconfigured
	}
	key, err := newLedgerKey("admission")
	if err != nil {
		return zero, err
	}
	request := admissionStoredRequest{Payload: p, Actor: actor}
	if response.Operation == "" {
		response.Operation = p.Method
	}
	response.Receipt.Key, response.LimitsRevision = key, limit.Revision
	var slotID *int64
	if slot != nil {
		slotID = &slot.ID
		request.Binding = admissionBinding(*slot)
		response.Receipt.SlotKey, response.Receipt.Revision, response.Receipt.State = slot.SlotKey, slot.Revision, run.AdmissionState(slot.State)
	}
	requestJSON, err := admissionEncode(request, int64(*limit.ReceiptJsonBytes))
	if err != nil {
		return zero, err
	}
	responseJSON, err := admissionEncode(response, int64(*limit.ReceiptJsonBytes))
	if err != nil {
		return zero, err
	}
	_, err = q.InsertDocumentAdmissionReceipt(ctx, intakedb.InsertDocumentAdmissionReceiptParams{
		ReceiptKey: key, OperationKey: admissionOperation(p), SlotID: slotID, ActorExecutionID: actor.ExecutionID,
		Operation: response.Operation, PayloadDigest: admissionHash(p), LimitsRevision: limit.Revision, ReceiptJsonBytes: *limit.ReceiptJsonBytes,
		Request: requestJSON, Response: responseJSON})
	if errors.Is(err, pgx.ErrNoRows) {
		// UNIQUE waiters must reread using a new READ COMMITTED snapshot.
		replayed, replayErr := admissionReplay(ctx, q, p)
		if replayErr != nil {
			return zero, replayErr
		}
		if replayed == nil {
			return zero, run.ErrAdmissionIntegrity
		}
		return *replayed, nil
	}
	if err != nil {
		return zero, err
	}
	return response, nil
}

func admissionBinding(s intakedb.IngestDocumentAdmissionSlot) run.AdmissionBinding {
	return run.AdmissionBinding{ExecutionID: s.CurrentExecutionID, Epoch: s.AdmissionEpoch, Fence: s.AttemptFence, Owner: s.LeaseOwner}
}

func admissionWorkBinding(w run.IntakeWork, id int64) run.AdmissionBinding {
	e := w.Execution
	return run.AdmissionBinding{ExecutionID: id, Epoch: e.AdmissionEpoch, Fence: e.AttemptFence, Owner: e.Owner}
}

func admissionChildPayload(w run.IntakeWork) *run.IntakeWork {
	w.Execution.Owner, w.Execution.AttemptFence, w.Execution.AdmissionEpoch = "", 0, 0
	return &w
}
