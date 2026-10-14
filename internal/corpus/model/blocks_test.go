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
