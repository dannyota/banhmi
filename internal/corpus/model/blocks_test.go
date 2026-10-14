package model

import "testing"

func TestRangesAndSpanCoordinates(t *testing.T) {
	if err := (ByteRange{Start: 0, End: 1}).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, r := range []ByteRange{{Start: -1, End: 1}, {Start: 0, End: 0}, {Start: 2, End: 1}} {
		if r.Validate() == nil {
			t.Fatal("accepted invalid half-open range")
		}
	}
	s := Span{ArtifactHash: contractAsset().SHA256, Space: OriginalBytes, ReasonCode: "BYTE_MAPPING_UNAVAILABLE", Member: "word/document.xml"}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.Member = ""
	if s.Validate() == nil {
		t.Fatal("accepted missing mapping without anchor")
	}
	s.ArtifactHash = "bad"
	if s.Validate() == nil {
		t.Fatal("accepted invalid span digest")
	}
	if BlockKind("unknown").Valid() || CoordinateSpace("unknown").Valid() || EditKind("unknown").Valid() || Authority("unknown").Valid() {
		t.Fatal("accepted unknown block vocabulary")
	}
}

func TestDecodedRuleValueBounds(t *testing.T) {
	hash := contractAsset().SHA256
	span := Span{ArtifactHash: hash, Space: RawText, Range: &ByteRange{Start: 0, End: 1}}
	limits := RuleLimits{MaxItems: 2, MaxBytes: 100}
	document := DecodedDocument{OriginalHash: hash, CandidateDigest: hash, Limits: limits, Blocks: []DecodedBlock{{ID: "block", Kind: BlockParagraph, RawText: "x", NormalizedText: "x", Language: "vi", Authority: AuthorityBinding, SourceSpans: []Span{span}}}}
	if err := document.Validate(); err != nil {
		t.Fatal(err)
	}
	document.Limits.MaxBytes = 1
	if document.Validate() == nil {
		t.Fatal("decoded document ignored frozen byte bound")
	}
	tree := SourceTree{Limits: limits, Nodes: []SourceNode{{ID: "node", Label: "label", Sources: []Span{span}}}}
	if err := tree.Validate(); err != nil {
		t.Fatal(err)
	}
	tree.Limits.MaxBytes = 1
	if tree.Validate() == nil {
		t.Fatal("decoded tree ignored frozen byte bound")
	}
	input := TextInput{Text: "x", Sources: []Span{span}, Limits: limits}
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	input.Sources = nil
	if input.Validate() == nil {
		t.Fatal("normalizer input omitted provenance")
	}
}
