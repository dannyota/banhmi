package model

import (
	"strings"
	"testing"
)

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
	if BlockKind("unknown").Valid() || CoordinateSpace("unknown").Valid() || EditKind("unknown").Valid() || Authority("unknown-value").Valid() {
		t.Fatal("accepted unknown block vocabulary")
	}
	if !AuthorityUnknown.Valid() {
		t.Fatal("rejected unknown authority")
	}
}

func TestDecodedRuleValueBounds(t *testing.T) {
	hash := contractAsset().SHA256
	span := Span{ArtifactHash: hash, Space: RawText, Range: &ByteRange{Start: 0, End: 1}}
	limits := RuleLimits{MaxItems: 2, MaxBytes: 1000}
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

func TestDecodedInputsCountSpanBytes(t *testing.T) {
	hash := contractAsset().SHA256
	span := Span{ArtifactHash: hash, Space: RawText, Range: &ByteRange{End: 1}, Member: strings.Repeat("x", 1<<20)}
	if err := span.Validate(); err != nil {
		t.Fatal(err)
	}
	limits := RuleLimits{MaxItems: 10, MaxBytes: 1000}
	for name, input := range map[string]interface{ Validate() error }{
		"text":     TextInput{Text: "x", Sources: []Span{span}, Limits: limits},
		"document": DecodedDocument{OriginalHash: hash, CandidateDigest: hash, Limits: limits, Blocks: []DecodedBlock{{ID: "b", Kind: BlockParagraph, Language: "vi", Authority: AuthorityUnknown, SourceSpans: []Span{span}}}},
		"tree":     SourceTree{Limits: limits, Nodes: []SourceNode{{ID: "n", Sources: []Span{span}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := input.Validate(); err == nil || !strings.Contains(err.Error(), "byte bound") {
				t.Fatalf("oversized provenance accepted: %v", err)
			}
		})
	}
	// Even one byte of text cannot admit oversized provenance under a tiny limit.
	input := TextInput{Text: "x", Sources: []Span{span}, Limits: RuleLimits{MaxItems: 1, MaxBytes: 1}}
	if input.Validate() == nil {
		t.Fatal("span member escaped aggregate byte allowance")
	}
}

func TestDecodedInputsAggregateBounds(t *testing.T) {
	hash := contractAsset().SHA256
	span := Span{ArtifactHash: hash, Space: RawText, Range: &ByteRange{End: 1}}
	tree := SourceTree{Limits: RuleLimits{MaxItems: 6, MaxBytes: 1000}, Nodes: []SourceNode{
		{ID: "a", BlockIDs: []string{"a"}, Sources: []Span{span}},
		{ID: "b", BlockIDs: []string{"b"}, Sources: []Span{span}},
	}}
	if err := tree.Validate(); err != nil {
		t.Fatal(err)
	}
	tree.Limits.MaxItems--
	if err := tree.Validate(); err == nil || !strings.Contains(err.Error(), "item bound") {
		t.Fatalf("aggregate tree collections escaped bound: %v", err)
	}
	doc := DecodedDocument{OriginalHash: hash, CandidateDigest: hash, Limits: RuleLimits{MaxItems: 2, MaxBytes: 1000}, Blocks: []DecodedBlock{{ID: "a", Kind: BlockParagraph, Language: "vi", Authority: AuthorityUnknown, SourceSpans: []Span{span}}}}
	if err := doc.Validate(); err != nil {
		t.Fatal(err)
	}
	doc.Limits.MaxItems--
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "item bound") {
		t.Fatalf("blocks and spans did not share allowance: %v", err)
	}
	input := TextInput{Text: "x", Sources: []Span{span}, Limits: RuleLimits{MaxItems: 1, MaxBytes: int64(1 + len(hash) + len(RawText))}}
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	input.Limits.MaxBytes--
	if input.Validate() == nil {
		t.Fatal("text and span bytes did not share allowance")
	}
}

func TestDecodedInputsCheckLengthsBeforeUTF8(t *testing.T) {
	oversized := strings.Repeat("x", 1<<20) + "\xff"
	hash := contractAsset().SHA256
	span := Span{ArtifactHash: hash, Space: RawText, Range: &ByteRange{End: 1}}
	limits := RuleLimits{MaxItems: 10, MaxBytes: 1000}
	for name, input := range map[string]interface{ Validate() error }{
		"text":     TextInput{Text: oversized, Sources: []Span{span}, Limits: limits},
		"document": DecodedDocument{OriginalHash: hash, CandidateDigest: hash, Limits: limits, Blocks: []DecodedBlock{{ID: "b", Kind: BlockParagraph, RawText: oversized, Language: "vi", Authority: AuthorityUnknown, SourceSpans: []Span{span}}}},
		"tree":     SourceTree{Limits: limits, Nodes: []SourceNode{{ID: "n", Label: oversized, Sources: []Span{span}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := input.Validate(); err == nil || !strings.Contains(err.Error(), "byte bound") || strings.Contains(err.Error(), "UTF-8") {
				t.Fatalf("length was not checked before encoding: %v", err)
			}
		})
	}
}
