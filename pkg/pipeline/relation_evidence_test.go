package pipeline

import (
	"encoding/json"
	"testing"

	"danny.vn/banhmi/pkg/ingest"
	dbbronze "danny.vn/banhmi/pkg/store/bronze"
)

func TestCollectStructuredRelationCandidatesTrustsVBPL(t *testing.T) {
	refs, err := json.Marshal([]ingest.Relation{{
		Type:         "amends_supplements",
		TypeRaw:      10,
		TargetNumber: "40/2024/TT-NHNN",
		TargetID:     "171000",
		TargetTitle:  "Thong tu so 40/2024/TT-NHNN",
	}})
	if err != nil {
		t.Fatal(err)
	}

	docNumber := "22/2026/TT-NHNN"
	candidates := collectStructuredRelationCandidates(dbbronze.BronzeSourceDocument{
		Source:        "vbpl",
		DocNumber:     &docNumber,
		DocNumberNorm: normalizeDocNumberForStorage(docNumber),
	}, []dbbronze.BronzeRawPayload{{
		Kind:    "references_json",
		Content: strPtr(string(refs)),
	}})

	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(candidates))
	}
	got := candidates[0]
	if got.targetNumber != "40/2024/TT-NHNN" || got.relationType != "amends_supplements" {
		t.Fatalf("candidate target/type = %q/%q", got.targetNumber, got.relationType)
	}
	if got.evidenceKind != "structured_relation" || got.sourceAuthority != "official_structured" || !got.promoted {
		t.Fatalf("kind/authority/promoted = %q/%q/%v", got.evidenceKind, got.sourceAuthority, got.promoted)
	}
	if got.relationTypeRaw == nil || *got.relationTypeRaw != 10 || got.confidence != 1 {
		t.Fatalf("raw/confidence = %v/%v, want 10/1", got.relationTypeRaw, got.confidence)
	}
}

func TestRelationTargetRefKeyUsesSourceTargetID(t *testing.T) {
	first := relationCandidate{
		source:       "vbpl",
		targetID:     "12898",
		targetNumber: "04/2007/QH12",
	}
	second := first
	second.targetID = "25400"

	if got := relationTargetRefKey(first); got != "vbpl:12898" {
		t.Fatalf("first ref key = %q, want vbpl:12898", got)
	}
	if got := relationTargetRefKey(second); got != "vbpl:25400" {
		t.Fatalf("second ref key = %q, want vbpl:25400", got)
	}
	if relationEvidenceKey(first) == relationEvidenceKey(second) {
		t.Fatal("same doc number with different VBPL target IDs must not share relation evidence key")
	}
}

func TestRelationTargetRefKeyFallsBackToDocNumber(t *testing.T) {
	candidate := relationCandidate{
		source:       "vbpl",
		targetNumber: " 04 / 2007 / QH12 ",
	}

	if got := relationTargetRefKey(candidate); got != "04/2007/QH12" {
		t.Fatalf("ref key = %q, want normalized doc number", got)
	}
}

func TestCollectStructuredRelationCandidatesWeakensNonVBPL(t *testing.T) {
	refs, err := json.Marshal([]ingest.Relation{{
		Type:         "abrogates",
		TypeRaw:      1,
		TargetNumber: "2345/QĐ-NHNN",
	}})
	if err != nil {
		t.Fatal(err)
	}

	docNumber := "2872/QĐ-NHNN"
	candidates := collectStructuredRelationCandidates(dbbronze.BronzeSourceDocument{
		Source:        "sbv_hanoi",
		DocNumber:     &docNumber,
		DocNumberNorm: normalizeDocNumberForStorage(docNumber),
	}, []dbbronze.BronzeRawPayload{{
		Kind:    "references_json",
		Content: strPtr(string(refs)),
	}})

	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(candidates))
	}
	got := candidates[0]
	if got.relationType != "weak_relation" || got.evidenceKind != "weak_relation" || got.promoted {
		t.Fatalf("kind/type/promoted = %q/%q/%v, want weak_relation/weak_relation/false", got.evidenceKind, got.relationType, got.promoted)
	}
	if got.operator != "abrogates" {
		t.Fatalf("operator = %q, want abrogates", got.operator)
	}
}

func TestCollectTextRelationCandidatesUsesWeakTitleAndSectionContext(t *testing.T) {
	doc := relationTextDoc{
		documentID:      6,
		currentNumber:   "77/2025/TT-NHNN",
		currentNorm:     normalizeDocNumberForStorage("77/2025/TT-NHNN"),
		title:           "Thông tư số 77/2025/TT-NHNN Sửa đổi, bổ sung một số điều của Thông tư số 50/2024/TT-NHNN",
		source:          "vbpl",
		sourceAuthority: "official_tree",
	}
	sections := []relationTextSection{{
		id:           101,
		citationPath: "dieu-4/khoan-1",
		content:      "Sửa đổi, bổ sung điểm c khoản 3 Điều 7 như sau: ...",
	}, {
		id:           102,
		citationPath: "dieu-5/khoan-1",
		content:      "Bổ sung khoản 1a vào sau khoản 1 Điều 8 như sau: ...",
	}, {
		id:           103,
		citationPath: "dieu-2",
		heading:      "Bổ sung khoản 11 Điều 2",
		content:      "“11. Khách hàng tổ chức mới là tổ chức mới đăng ký thành lập...”",
	}}

	candidates := collectTextRelationCandidates(doc, sections)
	byCitation := map[string]relationCandidate{}
	for _, candidate := range candidates {
		byCitation[candidate.citation] = candidate
	}
	for _, citation := range []string{"title", "dieu-4/khoan-1", "dieu-5/khoan-1", "dieu-2"} {
		got, ok := byCitation[citation]
		if !ok {
			t.Fatalf("missing candidate %s", citation)
		}
		if got.targetNumber != "50/2024/TT-NHNN" || got.relationType != "weak_relation" || got.evidenceKind != "weak_relation" || got.promoted {
			t.Fatalf("%s target/type/kind/promoted = %q/%q/%q/%v", citation, got.targetNumber, got.relationType, got.evidenceKind, got.promoted)
		}
	}
	if byCitation["title"].operator != "sửa đổi, bổ sung" {
		t.Fatalf("title operator = %q, want sua doi, bo sung", byCitation["title"].operator)
	}
	if byCitation["dieu-5/khoan-1"].operator != "bổ sung" || byCitation["dieu-2"].operator != "bổ sung" {
		t.Fatalf("supplement operators = %q/%q, want bo sung", byCitation["dieu-5/khoan-1"].operator, byCitation["dieu-2"].operator)
	}
}

func TestCollectTextRelationCandidatesFallbackRepealIsWeak(t *testing.T) {
	doc := relationTextDoc{
		documentID:      149,
		currentNumber:   "2872/QĐ-NHNN",
		currentNorm:     normalizeDocNumberForStorage("2872/QĐ-NHNN"),
		title:           "Quyết định về việc bãi bỏ quyết định số 2345/QĐ-NHNN ngày 18/12/2023",
		source:          "sbv_hanoi",
		sourceAuthority: "official_metadata",
	}

	candidates := collectTextRelationCandidates(doc, nil)
	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(candidates))
	}
	got := candidates[0]
	if got.targetNumber != "2345/QĐ-NHNN" || got.relationType != "weak_relation" || got.evidenceKind != "weak_relation" || got.promoted {
		t.Fatalf("candidate = target %q type %q kind %q promoted %v", got.targetNumber, got.relationType, got.evidenceKind, got.promoted)
	}
	if got.operator != "bãi bỏ" {
		t.Fatalf("operator = %q, want bai bo", got.operator)
	}
}

func TestCollectTextRelationCandidatesKeepsAllTargetsWeak(t *testing.T) {
	doc := relationTextDoc{
		documentID:      200,
		currentNumber:   "9999/QĐ-NHNN",
		currentNorm:     normalizeDocNumberForStorage("9999/QĐ-NHNN"),
		title:           "Quyết định kiểm thử",
		source:          "sbv_hanoi",
		sourceAuthority: "official_text",
	}
	sections := []relationTextSection{{
		id:           201,
		citationPath: "dieu-1",
		content:      "Căn cứ Quyết định số 1111/QĐ-NHNN. Bãi bỏ Quyết định số 2222/QĐ-NHNN và Quyết định số 3333/QĐ-NHNN.",
	}}

	candidates := collectTextRelationCandidates(doc, sections)
	byTarget := map[string]relationCandidate{}
	for _, candidate := range candidates {
		byTarget[candidate.targetNumber] = candidate
	}
	want := map[string]string{
		"1111/QĐ-NHNN": "căn cứ",
		"2222/QĐ-NHNN": "bãi bỏ",
		"3333/QĐ-NHNN": "bãi bỏ",
	}
	for target, operator := range want {
		got, ok := byTarget[target]
		if !ok {
			t.Fatalf("missing target %s", target)
		}
		if got.relationType != "weak_relation" || got.evidenceKind != "weak_relation" || got.promoted || got.operator != operator {
			t.Fatalf("%s = type %q kind %q promoted %v operator %q", target, got.relationType, got.evidenceKind, got.promoted, got.operator)
		}
	}
}
