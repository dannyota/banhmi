package country_test

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"danny.vn/banhmi/internal/core/country"
	"danny.vn/banhmi/internal/corpus/model"
)

func manifestFixture() country.Manifest {
	sources := []country.SourceDescriptor{
		{ID: "vbpl", Package: "vn/sources/vbpl", CorpusLanguage: "vi", BindingLanguage: "vi"},
		{ID: "congbao", Package: "vn/sources/congbao", CorpusLanguage: "vi", BindingLanguage: "vi"},
	}
	capabilities := []country.Capability{
		{Name: "guide", MissingReason: "Chưa có nội dung"},
		{Name: "assets", Version: "1"},
	}
	assets := []model.AssetRef{
		{Path: "data/z.csv", SHA256: model.Digest(strings.Repeat("b", 64)), ByteSize: 2},
		{Path: "data/a.csv", SHA256: model.Digest(strings.Repeat("a", 64)), ByteSize: 0},
	}
	return country.Manifest{
		Code:            "vn",
		ModuleVersion:   "m0-foundation-1",
		ContractVersion: "1",
		CorpusLanguage:  "vi",
		BindingLanguage: "vi",
		Timezone:        "Asia/Ho_Chi_Minh",
		OCRHints:        []string{"vi", "en"},
		Sources:         sources,
		Capabilities:    capabilities,
		Assets:          assets,
	}
}

func digestOf(t *testing.T, manifest country.Manifest) model.Digest {
	t.Helper()
	digest, err := manifest.ContentDigest()
	if err != nil {
		t.Fatalf("ContentDigest: %v", err)
	}
	return digest
}

func TestManifestDigestStable(t *testing.T) {
	m := manifestFixture()
	before := m.Clone()
	// This literal pins encoding v1 without calling the production encoder.
	encoded := `{"Code":"vn","ModuleVersion":"m0-foundation-1","ContractVersion":"1","CorpusLanguage":"vi","BindingLanguage":"vi","Timezone":"Asia/Ho_Chi_Minh","OCRHints":["en","vi"],"Sources":[{"ID":"congbao","Package":"vn/sources/congbao","CorpusLanguage":"vi","BindingLanguage":"vi"},{"ID":"vbpl","Package":"vn/sources/vbpl","CorpusLanguage":"vi","BindingLanguage":"vi"}],"Capabilities":[{"Name":"assets","Version":"1","MissingReason":""},{"Name":"guide","Version":"","MissingReason":"Chưa có nội dung"}],"Assets":[{"Path":"data/a.csv","SHA256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ByteSize":0},{"Path":"data/z.csv","SHA256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","ByteSize":2}]}`
	want := model.Digest(fmt.Sprintf("%x", sha256.Sum256([]byte(encoded))))
	got := digestOf(t, m)
	if got != want {
		t.Fatalf("encoding v1 digest = %s, want %s", got, want)
	}
	if !reflect.DeepEqual(m, before) {
		t.Fatal("ContentDigest mutated input")
	}
	m.OCRHints[0], m.OCRHints[1] = m.OCRHints[1], m.OCRHints[0]
	m.Sources[0], m.Sources[1] = m.Sources[1], m.Sources[0]
	m.Capabilities[0], m.Capabilities[1] = m.Capabilities[1], m.Capabilities[0]
	m.Assets[0], m.Assets[1] = m.Assets[1], m.Assets[0]
	if reordered := digestOf(t, m); reordered != want {
		t.Fatalf("reordering changed digest: %s", reordered)
	}
	m.Digest = got
	if err := m.Validate(); err != nil {
		t.Fatalf("matching supplied digest: %v", err)
	}
	if digestOf(t, m) != got {
		t.Fatal("supplied digest entered its own content hash")
	}
	m.Digest = model.Digest(strings.Repeat("0", 64))
	if err := m.Validate(); err == nil {
		t.Fatal("accepted altered supplied digest")
	}
	if digestOf(t, m) != got {
		t.Fatal("ContentDigest did not omit altered supplied digest")
	}
	for _, field := range []string{"OCRHints", "Sources", "Capabilities", "Assets"} {
		t.Run("nil equals empty "+field, func(t *testing.T) {
			a, b := manifestFixture(), manifestFixture()
			switch field {
			case "OCRHints":
				a.OCRHints, b.OCRHints = nil, []string{}
			case "Sources":
				a.Sources, b.Sources = nil, []country.SourceDescriptor{}
			case "Capabilities":
				a.Capabilities, b.Capabilities = nil, []country.Capability{}
			case "Assets":
				a.Assets, b.Assets = nil, []model.AssetRef{}
			}
			if digestOf(t, a) != digestOf(t, b) {
				t.Fatal("nil and empty lists have different digests")
			}
		})
	}
}

func TestManifestDigestChangesWithContent(t *testing.T) {
	original := manifestFixture()
	wantDifferent := digestOf(t, original)
	cases := []struct {
		name   string
		change func(*country.Manifest)
	}{
		{"code", func(m *country.Manifest) { m.Code = "kh" }},
		{"module version", func(m *country.Manifest) { m.ModuleVersion = "2" }},
		{"contract version", func(m *country.Manifest) { m.ContractVersion = "2" }},
		{"corpus language", func(m *country.Manifest) { m.CorpusLanguage = "en" }},
		{"binding language", func(m *country.Manifest) { m.BindingLanguage = "km" }},
		{"timezone", func(m *country.Manifest) { m.Timezone = "UTC" }},
		{"OCR hint", func(m *country.Manifest) { m.OCRHints[0] = "km" }},
		{"source ID", func(m *country.Manifest) { m.Sources[0].ID = "other" }},
		{"source package", func(m *country.Manifest) { m.Sources[0].Package = "vn/sources/other" }},
		{"source corpus language", func(m *country.Manifest) { m.Sources[0].CorpusLanguage = "en" }},
		{"source binding language", func(m *country.Manifest) { m.Sources[0].BindingLanguage = "km" }},
		{"capability name", func(m *country.Manifest) { m.Capabilities[0].Name = "locales" }},
		{"capability version", func(m *country.Manifest) { m.Capabilities[1].Version = "2" }},
		{"missing reason", func(m *country.Manifest) { m.Capabilities[0].MissingReason = "Chưa duyệt" }},
		{"asset path", func(m *country.Manifest) { m.Assets[0].Path = "data/other.csv" }},
		{"asset hash", func(m *country.Manifest) { m.Assets[0].SHA256 = model.Digest(strings.Repeat("c", 64)) }},
		{"asset size", func(m *country.Manifest) { m.Assets[0].ByteSize++ }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := original.Clone()
			tc.change(&m)
			if got := digestOf(t, m); got == wantDifferent {
				t.Fatal("content change did not change digest")
			}
		})
	}
}

func TestManifestCloneIndependent(t *testing.T) {
	original := manifestFixture()
	before := manifestFixture()
	clone := original.Clone()
	clone.OCRHints[0] = "km"
	clone.Sources[0].ID = "other"
	clone.Capabilities[0].MissingReason = "other"
	clone.Assets[0].Path = "data/other.csv"
	if !reflect.DeepEqual(original, before) {
		t.Fatal("mutating clone changed original slices")
	}
	original.OCRHints[1] = "th"
	original.Sources[1].ID = "changed"
	original.Capabilities[1].Version = "changed"
	original.Assets[1].ByteSize = 100
	if clone.OCRHints[1] != "en" || clone.Sources[1].ID != "congbao" || clone.Capabilities[1].Version != "1" || clone.Assets[1].ByteSize != 0 {
		t.Fatal("mutating original changed clone slices")
	}
}

func TestManifestValidation(t *testing.T) {
	if err := manifestFixture().Validate(); err != nil {
		t.Fatalf("pending capabilities are valid declaration metadata: %v", err)
	}
	cases := []struct {
		name   string
		change func(*country.Manifest)
	}{
		{"empty code", func(m *country.Manifest) { m.Code = "" }},
		{"empty module version", func(m *country.Manifest) { m.ModuleVersion = "" }},
		{"empty contract version", func(m *country.Manifest) { m.ContractVersion = "" }},
		{"empty corpus language", func(m *country.Manifest) { m.CorpusLanguage = "" }},
		{"empty binding language", func(m *country.Manifest) { m.BindingLanguage = "" }},
		{"empty timezone", func(m *country.Manifest) { m.Timezone = "" }},
		{"blank module version", func(m *country.Manifest) { m.ModuleVersion = " \t" }},
		{"empty source ID", func(m *country.Manifest) { m.Sources[0].ID = "" }},
		{"empty source package", func(m *country.Manifest) { m.Sources[0].Package = "" }},
		{"empty source corpus language", func(m *country.Manifest) { m.Sources[0].CorpusLanguage = "" }},
		{"empty source binding language", func(m *country.Manifest) { m.Sources[0].BindingLanguage = "" }},
		{"duplicate source", func(m *country.Manifest) { m.Sources = append(m.Sources, m.Sources[0]) }},
		{"empty capability name", func(m *country.Manifest) { m.Capabilities[0].Name = "" }},
		{"duplicate capability", func(m *country.Manifest) { m.Capabilities = append(m.Capabilities, m.Capabilities[0]) }},
		{"unexplained missing capability", func(m *country.Manifest) { m.Capabilities[0].MissingReason = "" }},
		{"blank missing reason", func(m *country.Manifest) { m.Capabilities[0].MissingReason = " " }},
		{"implemented and missing capability", func(m *country.Manifest) { m.Capabilities[0].Version = "1" }},
		{"empty OCR hint", func(m *country.Manifest) { m.OCRHints[0] = "" }},
		{"duplicate OCR hint", func(m *country.Manifest) { m.OCRHints = append(m.OCRHints, m.OCRHints[0]) }},
		{"duplicate asset", func(m *country.Manifest) { m.Assets = append(m.Assets, m.Assets[0]) }},
		{"invalid asset", func(m *country.Manifest) { m.Assets[0].Path = "../data.csv" }},
		{"invalid asset hash", func(m *country.Manifest) { m.Assets[0].SHA256 = "bad" }},
		{"negative asset size", func(m *country.Manifest) { m.Assets[0].ByteSize = -1 }},
		{"invalid UTF-8 identity", func(m *country.Manifest) { m.ModuleVersion = "\xff" }},
		{"invalid UTF-8 source", func(m *country.Manifest) { m.Sources[0].Package = "\xff" }},
		{"invalid UTF-8 reason", func(m *country.Manifest) { m.Capabilities[0].MissingReason = "\xff" }},
		{"invalid UTF-8 OCR hint", func(m *country.Manifest) { m.OCRHints[0] = "\xff" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := manifestFixture()
			tc.change(&m)
			if err := m.Validate(); err == nil {
				t.Fatal("Validate accepted invalid metadata")
			}
			if _, err := m.ContentDigest(); err == nil {
				t.Fatal("ContentDigest accepted invalid metadata")
			}
		})
	}
	for _, supplied := range []model.Digest{"bad", model.Digest(strings.Repeat("A", 64))} {
		m := manifestFixture()
		m.Digest = supplied
		if err := m.Validate(); err == nil {
			t.Fatalf("accepted malformed supplied digest %q", supplied)
		}
	}
}
