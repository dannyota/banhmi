package intake

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
)

func TestSourcesReceiptEncodingRetainsSourcePayload(t *testing.T) {
	payload := sourcePayload{run.IntakeApplyRequest{}, "page_decode", json.RawMessage(`{"PageKey":"page-a"}`)}
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
		Stage: run.Discover, Substep: "decode", Shard: "page-a", InputDigest: model.Digest(strings.Repeat("a", 64)), RecipeDigest: model.Digest(strings.Repeat("a", 64)), BudgetDigest: model.Digest(strings.Repeat("a", 64)), Budget: run.Budget{MaxItems: 10, MaxInputBytes: 1024, MaxPages: 10, MaxElapsed: time.Minute, MaxConcurrent: 1},
	}
	request := run.IntakeApplyRequest{Work: work, IdempotencyKey: run.IntakeIdempotencyKey(work), OutputDigest: model.Digest(digest), Outcome: run.OutcomeSucceeded}
	for _, test := range []struct {
		name    string
		payload sourcePayload
	}{
		{"absent", sourcePayload{IntakeApplyRequest: request}},
		{"operation", sourcePayload{request, "page_capture", raw}},
		{"payload", sourcePayload{request, "page_decode", json.RawMessage(`{"PageKey":"page-b"}`)}},
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
	encoded, err := sourceJSON(sourcePayload{request, "page_decode", raw})
	if err != nil {
		t.Fatal(err)
	}
	if err := classifySourcePayload(encoded, request, "page_decode", raw); err != nil {
		t.Fatal(err)
	}
	request.OutputDigest = model.Digest(strings.Repeat("b", 64))
	encoded, err = sourceJSON(sourcePayload{request, "page_decode", raw})
	if err != nil {
		t.Fatal(err)
	}
	if err := classifySourcePayload(encoded, request, "page_decode", raw); !errors.Is(err, run.ErrPayloadConflict) {
		t.Fatalf("source payload did not validate its digest: %v", err)
	}
}
