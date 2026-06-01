package vbpl

import (
	"encoding/json"
	"strings"
	"testing"
)

// sampleDetailData mirrors the GET /doc/{id} `data` object shape: a few mapped
// fields, the stable issuer `organization` code, and the bulky inline HTML body
// that must not be duplicated into the preserved raw metadata.
const sampleDetailData = `{
  "docNum": "52/2024/NĐ-CP",
  "organization": {"code": "62", "name": "Chính phủ"},
  "effStatus": {"code": "CCHL", "name": "Chưa có hiệu lực"},
  "documentContent": {"content": "<html><body>... 130KB of body ...</body></html>"},
  "documentIssues": [{"personName": "Lê Minh Khái", "jobTitleName": "Phó Thủ tướng"}],
  "references": [{"referenceType": 3, "targetDocument": {"docNum": "46/2010/QH12"}}]
}`

func TestDetailRawMetaStripsBodyKeepsMetadata(t *testing.T) {
	raw := detailRawMeta(json.RawMessage(sampleDetailData))
	if raw == nil {
		t.Fatal("detailRawMeta returned nil for valid input")
	}
	s := string(raw)
	if strings.Contains(s, "documentContent") || strings.Contains(s, "130KB of body") {
		t.Errorf("detailRawMeta must drop the bulky documentContent body, got: %s", s)
	}
	for _, want := range []string{"organization", "docNum", "effStatus", "documentIssues", "references"} {
		if !strings.Contains(s, want) {
			t.Errorf("detailRawMeta dropped %q; it must preserve unmapped fields for later mining", want)
		}
	}
}

func TestDetailRawMetaNilOnBadInput(t *testing.T) {
	if got := detailRawMeta(json.RawMessage(`not json`)); got != nil {
		t.Errorf("detailRawMeta(bad) = %q, want nil (never fail a fetch over preservation)", got)
	}
	if got := detailRawMeta(nil); got != nil {
		t.Errorf("detailRawMeta(nil) = %q, want nil", got)
	}
}

func TestDetailDataIssuerCode(t *testing.T) {
	var d detailData
	if err := json.Unmarshal([]byte(sampleDetailData), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.Organization.Code != "62" {
		t.Errorf("Organization.Code = %q, want 62 (stable issuer identity)", d.Organization.Code)
	}
}
