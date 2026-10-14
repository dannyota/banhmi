package model

import (
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func contractAsset() AssetRef {
	return AssetRef{Path: "artifacts/value", SHA256: Digest(strings.Repeat("a", 64)), ByteSize: 1}
}

func contractCapture() CaptureRef {
	a := contractAsset()
	return CaptureRef{Country: "vn", SourceID: "source", CaptureKey: "capture", Asset: a, MediaType: "text/html", RequestHash: a.SHA256, ResponseHash: a.SHA256, TransportReceipt: a}
}

func TestAcceptanceAndClosedIntakeKinds(t *testing.T) {
	for _, s := range []AcceptanceState{Candidate, Quarantined, Accepted, AcceptedWithGaps, Excluded} {
		if !s.Valid() || s.BuildEligible() != (s == Accepted || s == AcceptedWithGaps) {
			t.Fatalf("invalid eligibility for %s", s)
		}
	}
	if AcceptanceState("unknown").Valid() || AcceptanceState("unknown").BuildEligible() || DecisionKind("unknown").Valid() || GapKind("unknown").Valid() || ResidencyClass("unknown").Valid() {
		t.Fatal("unknown intake kind accepted")
	}
	for _, k := range []DecisionKind{DecisionRetryConverter, DecisionAcceptOCR, DecisionAcceptGaps, DecisionExclude, DecisionAlternateSource} {
		if !k.Valid() {
			t.Fatal(k)
		}
	}
	a := contractAsset()
	for _, c := range []ResidencyClass{ResidencyPublic, ResidencyPersonal, ResidencyUnclassified} {
		r := ResidencyAdmission{ArtifactHash: a.SHA256, PolicyHash: a.SHA256, Class: c, ReasonCode: "CLASSIFIED", Report: a}
		if r.Validate() != nil || r.Admitted() != (c == ResidencyPublic) {
			t.Fatal("incorrect residency admission")
		}
	}
	if err := contractCapture().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestContractValidatorsHaveNoIOImports(t *testing.T) {
	allowed := map[string]bool{"fmt": true, "net/url": true, "time": true, "unicode/utf8": true}
	for _, name := range []string{"intake.go", "observations.go", "blocks.go", "conversion.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil || !allowed[p] {
				t.Fatalf("I/O-capable dependency %s in %s", imp.Path.Value, name)
			}
		}
	}
}

func TestIntakeEnvelopesContainNoRawByteFields(t *testing.T) {
	seen := make(map[reflect.Type]bool)
	var inspect func(reflect.Type)
	inspect = func(typ reflect.Type) {
		if seen[typ] {
			return
		}
		seen[typ] = true
		switch typ.Kind() {
		case reflect.Interface:
			t.Fatalf("untyped payload interface in %s", typ)
		case reflect.Slice, reflect.Array:
			if typ.Elem().Kind() == reflect.Uint8 {
				t.Fatalf("raw byte payload in %s", typ)
			}
			inspect(typ.Elem())
		case reflect.Pointer:
			inspect(typ.Elem())
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				inspect(typ.Field(i).Type)
			}
		}
	}
	for _, typ := range []reflect.Type{reflect.TypeFor[CaptureRef](), reflect.TypeFor[Observation](), reflect.TypeFor[FollowUpRequest](), reflect.TypeFor[ListingPageResult](), reflect.TypeFor[DetailResult](), reflect.TypeFor[ResidencyAdmission]()} {
		inspect(typ)
	}
}
