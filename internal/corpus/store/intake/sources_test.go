package intake

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

func TestSourcesReceiptEncodingRetainsSourcePayload(t *testing.T) {
	payload := sourcePayload{run.IntakeApplyRequest{}, "page_decode", json.RawMessage(`{"PageKey":"page-a"}`), nil, "", ""}
	encoded, err := sourceJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded sourcePayload
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SourceOperation != payload.SourceOperation || !sameSourceJSON(decoded.SourcePayload, payload.SourcePayload) {
		t.Fatal("source receipt encoding omitted accounting evidence")
	}
}

func TestSourcesJSONComparisonPreservesIntegerPrecision(t *testing.T) {
	if sameSourceJSON([]byte(`{"size":9007199254740993}`), []byte(`{"size":9007199254740992}`)) {
		t.Fatal("different exact lengths collapsed during replay comparison")
	}
	if !sameSourceJSON([]byte(`{"capture":{"size":1,"hash":"a"}}`), []byte(`{"capture": {"hash": "a", "size": 1}}`)) {
		t.Fatal("PostgreSQL JSONB ordering changed immutable receipt comparison")
	}
}

func TestSourcesIssuedKeys(t *testing.T) {
	key, err := newLedgerKey("observation")
	if err != nil {
		t.Fatal(err)
	}
	if err := model.ValidateEnvelopeKey(key, "observation"); err != nil {
		t.Fatal(err)
	}
	first := sourceAttemptKey("request-a", 1)
	if err := model.ValidateEnvelopeKey(first, "request"); err != nil {
		t.Fatal(err)
	}
	if first != sourceAttemptKey("request-a", 1) || first == sourceAttemptKey("request-a", 2) || first == sourceAttemptKey("request-b", 1) {
		t.Fatal("source attempt identity is not stable and request-specific")
	}
}

func TestSourcesHostIdentitySharesPacing(t *testing.T) {
	for _, authority := range []string{"example.com", "EXAMPLE.COM.", "example.com:80", "example.com:443"} {
		key, err := sourceHostKey(authority)
		if err != nil || key != "example.com" {
			t.Fatalf("equivalent source authority did not share pacing: %v", err)
		}
	}
	for _, authority := range []string{"[2001:db8::1]", "[2001:0db8:0:0:0:0:0:1]:443"} {
		key, err := sourceHostKey(authority)
		if err != nil || key != "2001:db8::1" {
			t.Fatalf("equivalent IP authority did not share pacing: %v", err)
		}
	}
}

func TestSourcesReplayRequiresOperationPayloadAndDigest(t *testing.T) {
	raw := json.RawMessage(`{"PageKey":"page-a"}`)
	digest, err := sourceDigest(struct {
		Operation string
		Payload   json.RawMessage
	}{"page_decode", raw})
	if err != nil {
		t.Fatal(err)
	}
	work := run.IntakeWork{
		Execution: run.ScopedExecution{Country: "vn", WorkflowID: "source-sync/vn/source-a/window-a", WorkflowKind: run.SourceSync, AttemptKey: "attempt-a", AdmissionEpoch: 1, AttemptFence: 1, Owner: "owner-a", Scope: run.ExecutionScope{Kind: run.ScopeIntake, Subject: run.IntakeSubject{Kind: run.SubjectSourceWindow, SourceWindow: "window-a"}}},
		Stage:     run.Discover, Substep: "decode", Shard: "page-a", InputDigest: model.Digest(strings.Repeat("a", 64)), RecipeDigest: model.Digest(strings.Repeat("a", 64)), BudgetDigest: model.Digest(strings.Repeat("a", 64)), Budget: run.Budget{MaxItems: 10, MaxInputBytes: 1024, MaxPages: 10, MaxElapsed: time.Minute, MaxConcurrent: 1},
	}
	request := run.IntakeApplyRequest{Work: work, IdempotencyKey: run.IntakeIdempotencyKey(work), OutputDigest: model.Digest(digest), Outcome: run.OutcomeSucceeded}
	for _, test := range []struct {
		name    string
		payload sourcePayload
	}{
		{"absent", sourcePayload{IntakeApplyRequest: request}},
		{"operation", sourcePayload{request, "page_capture", raw, nil, "", ""}},
		{"payload", sourcePayload{request, "page_decode", json.RawMessage(`{"PageKey":"page-b"}`), nil, "", ""}},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := sourceJSON(test.payload)
			if err != nil {
				t.Fatal(err)
			}
			if err := classifySourcePayload(encoded, request, "page_decode", raw); !errors.Is(err, run.ErrPayloadConflict) {
				t.Fatalf("invalid source replay was not a payload conflict: %v", err)
			}
		})
	}
	encoded, err := sourceJSON(sourcePayload{request, "page_decode", raw, nil, "", ""})
	if err != nil {
		t.Fatal(err)
	}
	if err := classifySourcePayload(encoded, request, "page_decode", raw); err != nil {
		t.Fatal(err)
	}
	request.OutputDigest = model.Digest(strings.Repeat("b", 64))
	encoded, err = sourceJSON(sourcePayload{request, "page_decode", raw, nil, "", ""})
	if err != nil {
		t.Fatal(err)
	}
	if err := classifySourcePayload(encoded, request, "page_decode", raw); !errors.Is(err, run.ErrPayloadConflict) {
		t.Fatalf("source payload did not validate its digest: %v", err)
	}
}

// sourceCleanupProbeTx records cleanup context properties without database I/O.
type sourceCleanupProbeTx struct {
	pgx.Tx
	duration     time.Duration
	contextError error
}

// Rollback records the independent cleanup deadline supplied by the test wrapper.
func (tx *sourceCleanupProbeTx) Rollback(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if ok {
		tx.duration = time.Until(deadline)
	}
	tx.contextError = ctx.Err()
	return nil
}

func TestSourcesCleanupUsesIndependentFiveSecondDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	probe := &sourceCleanupProbeTx{}
	if err := (boundedAdapterTx{probe}).Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if probe.contextError != nil || probe.duration <= 4*time.Second || probe.duration > 5*time.Second {
		t.Fatal("cleanup inherited the expired work context or lost its five-second bound")
	}
}

type sourceConstructorProbe struct{ calls int }

// BeginTx detects accidental constructor I/O without opening a connection.
func (db *sourceConstructorProbe) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	db.calls++
	return nil, errors.New("constructor performed transaction I/O")
}

func TestSourcesConstructorRetainsOptionalTrustedContinuerWithoutIO(t *testing.T) {
	db := &sourceConstructorProbe{}
	configured, err := NewSources(db, "writer-a", nil, fixtureSourceContinuer{})
	if err != nil || configured.continuer == nil || db.calls != 0 {
		t.Fatalf("trusted continuer constructor performed I/O or lost its dependency: %v", err)
	}
	plain, err := NewSources(db, "writer-a", nil)
	if err != nil || plain.continuer != nil || db.calls != 0 {
		t.Fatalf("existing constructor contract changed: %v", err)
	}
	if _, err := NewSources(db, "writer-a", nil, fixtureSourceContinuer{}, fixtureSourceContinuer{}); err == nil || db.calls != 0 {
		t.Fatal("ambiguous trusted continuer configuration was accepted")
	}
}

func TestDiscoveryAttemptKeyBindsWindowPageAndClaimOrdinal(t *testing.T) {
	first := discoveryAttemptKey("window-a", "page-a", 1)
	if err := model.ValidateEnvelopeKey(first, "request"); err != nil {
		t.Fatal(err)
	}
	if first != discoveryAttemptKey("window-a", "page-a", 1) {
		t.Fatal("claim replay changed issued attempt key")
	}
	for _, changed := range []string{discoveryAttemptKey("window-b", "page-a", 1), discoveryAttemptKey("window-a", "page-b", 1), discoveryAttemptKey("window-a", "page-a", 2)} {
		if changed == first {
			t.Fatal("claim key omitted window, page or claim ordinal")
		}
	}
}

// sourceOrderProbe records issued inserts without database I/O.
type sourceOrderProbe struct {
	intakedb.Querier
	observations []intakedb.InsertObservationRevisionParams
	edges        []intakedb.AddBlobRetentionReferenceParams
	blobs        map[string]intakedb.BronzeBlob
}

func (q *sourceOrderProbe) InsertObservationRevision(_ context.Context, p intakedb.InsertObservationRevisionParams) (int64, error) {
	q.observations = append(q.observations, p)
	return 1, nil
}

func (q *sourceOrderProbe) ReadObservationRevision(_ context.Context, p intakedb.ReadObservationRevisionParams) (intakedb.BronzeObservationRevision, error) {
	for _, inserted := range q.observations {
		if inserted.SourceRecordID == p.SourceRecordID && inserted.ObservationDigest == p.ObservationDigest {
			return intakedb.BronzeObservationRevision{ObservationKey: inserted.ObservationKey, SourceRecordID: inserted.SourceRecordID, ObservationDigest: inserted.ObservationDigest, MetadataDigest: inserted.MetadataDigest, SourceTreeDigest: inserted.SourceTreeDigest, RelationsDigest: inserted.RelationsDigest, CaptureBlobID: inserted.CaptureBlobID, DocumentVersionKey: inserted.DocumentVersionKey}, nil
		}
	}
	return intakedb.BronzeObservationRevision{}, pgx.ErrNoRows
}

func (q *sourceOrderProbe) ReadSourceBlob(_ context.Context, hash string) (intakedb.BronzeBlob, error) {
	return q.blobs[hash], nil
}

func (q *sourceOrderProbe) AddBlobRetentionReference(_ context.Context, p intakedb.AddBlobRetentionReferenceParams) (int64, error) {
	q.edges = append(q.edges, p)
	return 1, nil
}

type sourceOrderTx struct {
	pgx.Tx
	gaps []string
}

func (*sourceOrderTx) QueryRow(context.Context, string, ...any) pgx.Row {
	return sourceOrderRow{}
}

type sourceOrderRow struct{}

func (sourceOrderRow) Scan(dest ...any) error {
	*dest[0].(*int64) = 1
	return nil
}

func (tx *sourceOrderTx) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	tx.gaps = append(tx.gaps, args[0].(string))
	return pgconn.CommandTag{}, nil
}

func TestDiscoveryObservationBatchOrdersUniqueTupleAndPreservesReceiptOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	params := []intakedb.InsertObservationRevisionParams{
		{SourceRecordID: 20, ObservationDigest: "a", ObservationKey: "observation-00000000000000000000000000000003"},
		{SourceRecordID: 3, ObservationDigest: "b", ObservationKey: "observation-00000000000000000000000000000002"},
		{SourceRecordID: 3, ObservationDigest: "a", ObservationKey: "observation-00000000000000000000000000000001"},
	}
	original := append([]intakedb.InsertObservationRevisionParams(nil), params...)
	q := &sourceOrderProbe{}
	revisions, err := insertObservationBatch(ctx, &sourceOrderTx{}, q, params)
	if err != nil {
		t.Fatal(err)
	}
	want := []intakedb.InsertObservationRevisionParams{params[2], params[1], params[0]}
	if !reflect.DeepEqual(q.observations, want) {
		t.Fatalf("issued observation inserts: got %v, want %v", q.observations, want)
	}
	if !reflect.DeepEqual(params, original) {
		t.Fatal("observation insert order changed decoded receipt parameters")
	}
	if len(revisions) != len(params) {
		t.Fatal("observation batch lost returned revisions")
	}
	for i, revision := range revisions {
		if revision.ObservationKey != params[i].ObservationKey {
			t.Fatal("returned revisions changed decoded receipt order")
		}
	}
}

func TestSourceCaptureRetentionPathsOrderFullUniqueTuple(t *testing.T) {
	for _, path := range []struct {
		name   string
		owners []string
	}{
		{"discovery", []string{"observation-00000000000000000000000000000002", "observation-00000000000000000000000000000001"}},
		{"detail", []string{"observation-00000000000000000000000000000001"}},
		{"artifact", []string{"observation-00000000000000000000000000000001"}},
		{"transport-only", nil},
	} {
		t.Run(path.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			capture := model.CaptureRef{Asset: model.AssetRef{SHA256: model.Digest(strings.Repeat("a", 64)), ByteSize: 10}, TransportReceipt: model.AssetRef{SHA256: model.Digest(strings.Repeat("b", 64)), ByteSize: 20}}
			// Hash order is the reverse of the existing blob ID order.
			q := &sourceOrderProbe{blobs: map[string]intakedb.BronzeBlob{
				string(capture.Asset.SHA256):            {ID: 20, ByteSize: 10},
				string(capture.TransportReceipt.SHA256): {ID: 3, ByteSize: 20},
			}}
			original := append([]string(nil), path.owners...)
			execution := "execution-00000000000000000000000000000001"
			if err := protectSourceCapture(ctx, &sourceOrderTx{}, q, capture, execution, path.owners); err != nil {
				t.Fatal(err)
			}
			var want []intakedb.AddBlobRetentionReferenceParams
			for _, id := range []int64{3, 20} {
				want = append(want, intakedb.AddBlobRetentionReferenceParams{ID: id, OwnerKind: "execution", OwnerKey: execution})
				for i := len(path.owners) - 1; i >= 0; i-- {
					want = append(want, intakedb.AddBlobRetentionReferenceParams{ID: id, OwnerKind: "observation_revision", OwnerKey: path.owners[i]})
				}
			}
			if !reflect.DeepEqual(q.edges, want) {
				t.Fatalf("issued retention inserts: got %v, want %v", q.edges, want)
			}
			if !reflect.DeepEqual(path.owners, original) {
				t.Fatal("retention inserts changed decoded owner order")
			}
		})
	}
}

func TestAcceptanceGapBatchOrdersAllocatedKeysAndPreservesScopeOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	gaps := []acceptanceGap{
		{"gap-c", model.GapRef{Kind: model.GapSourceWindow, Key: "window-c", ReasonCode: "missing", ReportHash: model.Digest(strings.Repeat("a", 64))}},
		{"gap-b", model.GapRef{Kind: model.GapSourceWindow, Key: "window-b", ReasonCode: "missing", ReportHash: model.Digest(strings.Repeat("a", 64))}},
		{"gap-a", model.GapRef{Kind: model.GapSourceWindow, Key: "window-a", ReasonCode: "missing", ReportHash: model.Digest(strings.Repeat("a", 64))}},
	}
	original := append([]acceptanceGap(nil), gaps...)
	tx := &sourceOrderTx{}
	if err := insertAcceptanceGaps(ctx, tx, 1, gaps); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tx.gaps, []string{"gap-a", "gap-b", "gap-c"}) {
		t.Fatalf("issued gap inserts: %v", tx.gaps)
	}
	if !reflect.DeepEqual(gaps, original) {
		t.Fatal("gap inserts changed scoped gap receipt order")
	}
}
