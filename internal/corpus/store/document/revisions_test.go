package document

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestProvisionAnchorTransport(t *testing.T) {
	r, err := NewRevisions(&forbiddenTransactionStarter{}, "writer-a", adapterLimits())
	if err != nil {
		t.Fatal(err)
	}
	item := adapterProvision("article-1", 0)
	item.SourceAnchors = json.RawMessage(`[{"object":"\ud83d\ude00","page":1.0}]`)
	payload, err := r.provisionPayload(revisionKey, "vn", []ProvisionInput{item})
	if err != nil || !json.Valid(payload) {
		t.Fatalf("valid Unicode anchors rejected: %v", err)
	}
	var transport []provisionTransport
	if err := json.Unmarshal(payload, &transport); err != nil || len(transport) != 1 || transport[0].NativeCitationHex != "c49069e1bb81752031" {
		t.Fatalf("citation transport changed bytes: %v", err)
	}
	for _, raw := range []string{`[{"object":"\ud800"}]`, `[{"object":"\udfff"}]`, `[{"object":"\ud800\u0061"}]`, `[{"object":"\u0000"}]`} {
		item.SourceAnchors = json.RawMessage(raw)
		if _, err := r.provisionPayload(revisionKey, "vn", []ProvisionInput{item}); err == nil {
			t.Fatal("invalid Unicode anchor reached transport")
		}
	}
	if _, err := r.provisionPayload(revisionKey, "vn", nil); err == nil {
		t.Fatal("empty bulk input was accepted")
	}
	left, err := jsonValue(json.RawMessage(`[{"page":1,"offset":0.125}]`))
	if err != nil {
		t.Fatal(err)
	}
	right, err := jsonValue(json.RawMessage(`[{"offset":125e-3,"page":1.00}]`))
	if err != nil {
		t.Fatal(err)
	}
	// Numbers must compare as exact JSONB values, without float64 rounding.
	l, _ := json.Marshal(left)
	rightBytes, _ := json.Marshal(right)
	if string(l) != string(rightBytes) {
		t.Fatal("equivalent JSONB numbers produced different replay values")
	}
}

func TestRevisionsConstructorAndKeyGrammar(t *testing.T) {
	if _, err := NewRevisions(nil, "writer-a", adapterLimits()); err == nil {
		t.Fatal("missing transaction starter accepted")
	}
	if _, err := NewRevisions(&forbiddenTransactionStarter{}, "Writer A", adapterLimits()); err == nil {
		t.Fatal("invalid writer accepted")
	}
	limits := adapterLimits()
	limits.MaxCitationBytes = 0
	if _, err := NewRevisions(&forbiddenTransactionStarter{}, "writer-a", limits); err == nil {
		t.Fatal("unbounded citation input accepted")
	}
	for _, tc := range []struct {
		key      string
		revision bool
	}{
		{identityKey, true},
		{revisionKey, false},
		{"vn:doc:not-a-uuid", false},
		{identityKey + ":rev:short", true},
	} {
		if err := validateDocumentKey(tc.key, "vn", tc.revision); err == nil {
			t.Fatal("invalid key grammar accepted")
		}
	}
	var rejection *RejectionError
	if err := validateDocumentKey(identityKey, "my", false); !errors.As(err, &rejection) || rejection.Reason != "wrong_country" {
		t.Fatal("country rejection lost its type or reason")
	}
}

func TestProvisionPageValidation(t *testing.T) {
	for _, tc := range []struct {
		after, limit int32
	}{{-2, 1}, {-1, 0}, {-1, MaxProvisionPageItems + 1}} {
		starter := &forbiddenTransactionStarter{}
		r, err := NewRevisions(starter, "writer-a", adapterLimits())
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.ReadProvisionRevisions(context.Background(), adapterGrant(1), revisionKey, tc.after, tc.limit)
		var rejection *RejectionError
		if !errors.As(err, &rejection) || starter.calls != 0 {
			t.Fatalf("invalid page reached transaction: calls=%d err=%v", starter.calls, err)
		}
	}
}

func TestProvisionDuplicateAnchorValidation(t *testing.T) {
	for _, raw := range []string{`[{"a":"\u0000","a":"x"}]`, `[{"a":1e999999,"a":1}]`} {
		starter := &forbiddenTransactionStarter{}
		r, err := NewRevisions(starter, "writer-a", adapterLimits())
		if err != nil {
			t.Fatal(err)
		}
		item := adapterProvision("article-1", 0)
		item.SourceAnchors = json.RawMessage(raw)
		_, err = r.InsertProvisionRevisions(context.Background(), adapterGrant(1), revisionKey, []ProvisionInput{item})
		var rejection *RejectionError
		if !errors.As(err, &rejection) || starter.calls != 0 {
			t.Fatalf("duplicate invalid token reached transaction: calls=%d err=%v", starter.calls, err)
		}
	}
}
