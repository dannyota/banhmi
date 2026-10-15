package intake

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/run"
)

func TestAdmissionRawReceiptBounds(t *testing.T) {
	for _, tc := range []struct {
		n, limit int64
		want     bool
	}{
		{0, 1, true}, {2, 1, true}, {3, 1, false}, {0, 0, false}, {0, -1, false},
		{math.MaxInt32, math.MaxInt32, true}, {2 * math.MaxInt32, math.MaxInt32, true},
		{2*math.MaxInt32 + 1, math.MaxInt32, false}, {math.MaxInt64, math.MaxInt32, false},
		{0, math.MaxInt32 + 1, false}, {-1, 1, false},
	} {
		if got := admissionRawBound(tc.n, tc.limit); got != tc.want {
			t.Fatalf("bound(%d,%d)=%v", tc.n, tc.limit, got)
		}
	}
}

func TestAdmissionCanonicalReceiptBounds(t *testing.T) {
	type payload struct{ A, B string }
	want := payload{A: "a", B: "b"}
	canonical, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	limit := int64(len(canonical))
	raw := json.RawMessage(`{"A": "a", "B": "b"}`)
	if int64(len(raw)) <= limit {
		t.Fatal("fixture must exercise JSONB separator expansion")
	}
	var got payload
	if err := admissionDecode(raw, limit, &got); err != nil || got != want {
		t.Fatalf("boundary receipt failed: %v", err)
	}
	for _, bad := range []json.RawMessage{
		json.RawMessage(strings.Repeat(" ", int(2*limit+1))),
		json.RawMessage(`{"A":"aa","B":"b"}`), json.RawMessage(`{"A":`),
		json.RawMessage(`{"A":"a","B":"b","C":0}`), json.RawMessage(`{} {}`),
	} {
		if !errors.Is(admissionDecode(bad, limit, &got), run.ErrAdmissionIntegrity) {
			t.Fatal("invalid receipt accepted")
		}
	}
}

func TestAdmissionHashSeparatesFieldsAndRevisions(t *testing.T) {
	left := admissionPayload{Country: "vn", Method: "close", Target: "slot-00000000000000000000000000000001", Revision: 1, Reason: run.AdmissionAbandoned}
	right := left
	right.Revision++
	if admissionOperation(left) == admissionOperation(right) {
		t.Fatal("revision omitted from operation identity")
	}
	right = left
	right.Reason = run.AdmissionCancelled
	if admissionOperation(left) != admissionOperation(right) || admissionHash(left) == admissionHash(right) {
		t.Fatal("operation identity and payload equality are conflated")
	}
	if admissionHash(struct{ A, B string }{"ab", "c"}) == admissionHash(struct{ A, B string }{"a", "bc"}) {
		t.Fatal("hash fields lack length prefixes")
	}
}

func TestAdmissionCapacityClassification(t *testing.T) {
	for _, tc := range []struct {
		processing, open bool
		want             run.AdmissionRefusal
	}{{false, false, ""}, {true, false, run.AdmissionProcessingFull}, {false, true, run.AdmissionOpenFull}, {true, true, run.AdmissionBothFull}} {
		if got := admissionCapacityRefusal(tc.processing, tc.open); got != tc.want {
			t.Fatal("capacity refusal changed")
		}
	}
}
