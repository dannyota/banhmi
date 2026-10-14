package intake

import (
	"encoding/json"
	"testing"

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
