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
	}}, nil)

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

func TestCollectStructuredRelationCandidatesTrustsAGCLOM(t *testing.T) {
	// agclom P.U. subsidiary-legislation links come from the official json-subsid
	// endpoint, so they promote as structured relations typed subsidiary_legislation.
	refs, err := json.Marshal([]ingest.Relation{{
		Type:         "pua",
		TargetNumber: "P.U. (A) 61/2025",
		TargetTitle:  "Some Regulations 2025",
	}})
	if err != nil {
		t.Fatal(err)
	}
	docNumber := "Act 758"
	candidates := collectStructuredRelationCandidates(dbbronze.BronzeSourceDocument{
		Source:        "agclom",
		DocNumber:     &docNumber,
		DocNumberNorm: normalizeDocNumberForStorage(docNumber),
	}, []dbbronze.BronzeRawPayload{{
		Kind:    "references_json",
		Content: strPtr(string(refs)),
	}}, nil)

	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(candidates))
	}
	got := candidates[0]
	if got.relationType != "subsidiary_legislation" || got.operator != "pua" {
		t.Fatalf("type/operator = %q/%q, want subsidiary_legislation/pua", got.relationType, got.operator)
	}
	if got.evidenceKind != "structured_relation" || !got.promoted || got.confidence != 1 {
		t.Fatalf("kind/promoted/confidence = %q/%v/%v, want structured_relation/true/1", got.evidenceKind, got.promoted, got.confidence)
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
	}}, nil)

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

// idRelTypeMap mirrors the deploy/seed/relation_type.csv rows for bi/bpk: forward
// operators map to is_amending labels, reverse operators to non-amending *_by ones.
func idRelTypeMap() map[relationTypeKey]relationTypeConfig {
	return map[relationTypeKey]relationTypeConfig{
		{source: "bi", code: "Mengubah"}:                 {label: "amends", isAmending: true},
		{source: "bi", code: "Mencabut"}:                 {label: "revokes", isAmending: true},
		{source: "bpk", code: "Mengubah"}:                {label: "amends", isAmending: true},
		{source: "bpk", code: "Mencabut"}:                {label: "revokes", isAmending: true},
		{source: "bpk", code: "Mencabut sebagian"}:       {label: "partially_revokes", isAmending: true},
		{source: "bpk", code: "Diubah dengan"}:           {label: "amended_by", isAmending: false},
		{source: "bpk", code: "Dicabut dengan"}:          {label: "revoked_by", isAmending: false},
		{source: "bpk", code: "Dicabut sebagian dengan"}: {label: "partially_revoked_by", isAmending: false},
	}
}

func TestCollectStructuredRelationCandidatesPromotesConfigMappedForward(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		operator  string
		wantLabel string
	}{
		{"bi Mengubah", "bi", "Mengubah", "amends"},
		{"bi Mencabut", "bi", "Mencabut", "revokes"},
		{"bpk partial revoke", "bpk", "Mencabut sebagian", "partially_revokes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			refs, err := json.Marshal([]ingest.Relation{{
				Type:         tt.operator,
				TargetNumber: "PADG NO.11 TAHUN 2024",
				TargetID:     "9001",
			}})
			if err != nil {
				t.Fatal(err)
			}
			docNumber := "PADG Nomor 15 Tahun 2026"
			candidates := collectStructuredRelationCandidates(dbbronze.BronzeSourceDocument{
				Source:        tt.source,
				DocNumber:     &docNumber,
				DocNumberNorm: normalizeDocNumberForStorage(docNumber),
			}, []dbbronze.BronzeRawPayload{{
				Kind:    "references_json",
				Content: strPtr(string(refs)),
			}}, idRelTypeMap())

			if len(candidates) != 1 {
				t.Fatalf("len(candidates) = %d, want 1", len(candidates))
			}
			got := candidates[0]
			if got.relationType != tt.wantLabel || !got.promoted {
				t.Fatalf("type/promoted = %q/%v, want %q/true", got.relationType, got.promoted, tt.wantLabel)
			}
			if got.evidenceKind != "structured_relation" || got.sourceAuthority != "official_metadata" || got.confidence != 0.9 {
				t.Fatalf("kind/authority/confidence = %q/%q/%v, want structured_relation/official_metadata/0.9",
					got.evidenceKind, got.sourceAuthority, got.confidence)
			}
			if got.operator != tt.operator {
				t.Fatalf("operator = %q, want %q preserved", got.operator, tt.operator)
			}
		})
	}
}

func TestCollectStructuredRelationCandidatesKeepsReverseAndUnmappedWeak(t *testing.T) {
	// Reverse operators (this doc amended BY the target) and operators with no
	// config mapping must stay weak — the forward edge comes from the amender's
	// own page; promoting the reverse row would fabricate a mislabeled edge.
	for _, operator := range []string{"Diubah dengan", "Dicabut dengan", "Berlaku"} {
		refs, err := json.Marshal([]ingest.Relation{{
			Type:         operator,
			TargetNumber: "UU NO.11 TAHUN 2008",
			TargetID:     "9002",
		}})
		if err != nil {
			t.Fatal(err)
		}
		docNumber := "UU NO.19 TAHUN 2016"
		candidates := collectStructuredRelationCandidates(dbbronze.BronzeSourceDocument{
			Source:        "bpk",
			DocNumber:     &docNumber,
			DocNumberNorm: normalizeDocNumberForStorage(docNumber),
		}, []dbbronze.BronzeRawPayload{{
			Kind:    "references_json",
			Content: strPtr(string(refs)),
		}}, idRelTypeMap())

		if len(candidates) != 1 {
			t.Fatalf("%s: len(candidates) = %d, want 1", operator, len(candidates))
		}
		got := candidates[0]
		if got.relationType != "weak_relation" || got.promoted || got.confidence != 0.65 {
			t.Fatalf("%s: type/promoted/confidence = %q/%v/%v, want weak_relation/false/0.65",
				operator, got.relationType, got.promoted, got.confidence)
		}
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

// TestDocNumberMentionReMatchesNationalAssemblyLaws guards the regression that
// hid every Luật/Nghị quyết from text-derived relation evidence: the National
// Assembly suffix (QH14, QH15) carries no hyphen, so a pattern requiring one
// silently skipped repeal clauses such as Điều 44 of 116/2025/QH15.
func TestDocNumberMentionReMatchesNationalAssemblyLaws(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want []string
	}{
		{
			name: "law repealed by an effect article",
			text: "Luật An ninh mạng số 24/2018/QH14 hết hiệu lực kể từ ngày Luật này có hiệu lực thi hành.",
			want: []string{"24/2018/QH14"},
		},
		{
			name: "law with amending recital",
			text: "Luật An toàn thông tin mạng số 86/2015/QH13 đã được sửa đổi, bổ sung theo Luật số 35/2018/QH14",
			want: []string{"86/2015/QH13", "35/2018/QH14"},
		},
		{
			name: "ministerial circular still matches",
			text: "Thông tư số 22/2020/TT-BTTTT ngày 07 tháng 9 năm 2020 hết hiệu lực",
			want: []string{"22/2020/TT-BTTTT"},
		},
		{
			name: "consolidated document still matches",
			text: "văn bản hợp nhất 24/VBHN-NHNN",
			want: []string{"24/VBHN-NHNN"},
		},
		{
			name: "bare dates are not document numbers",
			text: "ngày 06/10/2011 và ngày 29/4/2011",
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := docNumberMentionRe.FindAllString(tc.text, -1)
			if len(got) != len(tc.want) {
				t.Fatalf("matches = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("match %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestDocNumberMentionReSpacedHyphens covers BOTH hyphen directions, which is
// where an earlier fix went wrong. Rejecting any whitespace before a hyphen
// killed the real spaced form ("266/QĐ - NH1"), destroying 3 resolved VN edges
// and truncating others into new phantoms. The rule that actually separates a
// real suffix from a centred page number is that the spaced form must continue
// with a LETTER.
func TestDocNumberMentionReSpacedHyphens(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want []string
	}{
		// Real numbers — every one of these must survive.
		{"tight hyphen", "Thông tư số 22/2020/TT-BTTTT ngày", []string{"22/2020/TT-BTTTT"}},
		{"space after hyphen (line break)", "Thông tư 01/2013/TT- NHNN bị bãi bỏ", []string{"01/2013/TT- NHNN"}},
		{"spaces both sides", "Quyết định số 266/QĐ - NH1 ngày 27/09/1996", []string{"266/QĐ - NH1"}},
		{"spaces both sides, decree", "Nghị định 60/2003/NĐ - CP quy định", []string{"60/2003/NĐ - CP"}},
		{"no hyphen at all", "Luật số 24/2018/QH14", []string{"24/2018/QH14"}},
		{"consolidated", "văn bản hợp nhất 24/VBHN-NHNN", []string{"24/VBHN-NHNN"}},

		// Page furniture — the spaced part is DIGIT-initial, so it must not match.
		{"page number then url", "2/OJK -143- www", nil},
		{"page number then body", "5/OJK -19- kantor pusat Bank di luar wilayah", nil},
		{"page number then pasal", "7/OJK -4- Pasal 3", nil},
		{"date arithmetic", "31/12/2021 - 0 - 1) 9 [ (9% - 8%)", nil},
		{"bare dates", "ngày 06/10/2011 và 29/4/2011", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := docNumberMentionRe.FindAllString(tc.text, -1)
			if len(got) != len(tc.want) {
				t.Fatalf("matches = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("match %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
