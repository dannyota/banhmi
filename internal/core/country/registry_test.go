package country

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/model"
)

func registryDeclaration(t *testing.T, code Code) Declaration {
	t.Helper()
	m := Manifest{
		Code:            code,
		ModuleVersion:   "m0-foundation-1",
		ContractVersion: "1",
		CorpusLanguage:  "vi",
		BindingLanguage: "vi",
		Timezone:        "Asia/Ho_Chi_Minh",
		OCRHints:        []string{"vi"},
		Sources:         []SourceDescriptor{{ID: "vbpl", Package: "vn/sources/vbpl", CorpusLanguage: "vi", BindingLanguage: "vi"}},
		Capabilities:    []Capability{{Name: "grammar", MissingReason: "Not implemented"}},
		Assets:          []model.AssetRef{{Path: "data/seed.csv", SHA256: model.Digest(strings.Repeat("a", 64)), ByteSize: 1}},
	}
	digest, err := m.ContentDigest()
	if err != nil {
		t.Fatal(err)
	}
	m.Digest = digest
	return Declaration{Code: code, Descriptor: Descriptor{Manifest: m}}
}

func requireAbsent(t *testing.T, code Code, err error) {
	t.Helper()
	if !errors.Is(err, ErrCountryAbsent) {
		t.Fatalf("expected absence for %q: %v", code, err)
	}
	var countryError *CountryError
	if !errors.As(err, &countryError) || countryError.Code != code {
		t.Fatalf("expected rejected code %q: %v", code, err)
	}
}

func TestRegistryAdmissionRejectsInventory(t *testing.T) {
	r, err := NewRegistry([]Declaration{registryDeclaration(t, "vn")})
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []Code{"", "vn", "my", "id", "sg", "th", "kh", "unknown", "VN"} {
		wantCode := code
		if wantCode == "" {
			wantCode = "vn"
		}
		t.Run(string(code), func(t *testing.T) {
			entry, err := r.Lookup(code)
			requireAbsent(t, wantCode, err)
			if !reflect.DeepEqual(entry, Entry{}) {
				t.Fatal("Lookup returned an entry despite absence")
			}
			entry, err = r.RequireEnabled(code)
			requireAbsent(t, wantCode, err)
			if !reflect.DeepEqual(entry, Entry{}) {
				t.Fatal("RequireEnabled returned an entry despite absence")
			}
		})
	}
	if len(r.Entries()) != 0 {
		t.Fatal("inventory declaration became a runtime entry")
	}
}

func TestRegistryRejectsEveryRuntimeHeader(t *testing.T) {
	for _, state := range []State{Enabled, MigratedDisabled, Absent, "", "forged"} {
		t.Run(string(state), func(t *testing.T) {
			declaration := registryDeclaration(t, "vn")
			declaration.Entry = &Entry{Code: "vn", State: state, Manifest: declaration.Descriptor.Manifest.Clone()}
			r, err := NewRegistry([]Declaration{declaration})
			if r != nil || !errors.Is(err, ErrEntryIncomplete) || errors.Is(err, ErrCountryAbsent) {
				t.Fatalf("supplied runtime header did not fail construction: %v", err)
			}
			var countryError *CountryError
			if !errors.As(err, &countryError) || countryError.Code != "vn" {
				t.Fatalf("construction lost rejected code: %v", err)
			}
		})
	}
	declaration := registryDeclaration(t, "vn")
	declaration.Entry = &Entry{}
	declaration.Descriptor = Descriptor{}
	if _, err := NewRegistry([]Declaration{declaration}); !errors.Is(err, ErrEntryIncomplete) {
		t.Fatalf("malformed non-nil entry bypassed incomplete-entry denial: %v", err)
	}
}

func TestRegistryValidatesDeclarations(t *testing.T) {
	for _, code := range []Code{"vn", "my", "id", "sg", "th", "kh"} {
		r, err := NewRegistry([]Declaration{registryDeclaration(t, code)})
		if err != nil {
			t.Fatalf("supported declaration %q: %v", code, err)
		}
		_, err = r.Lookup(code)
		requireAbsent(t, code, err)
	}
	cases := []struct {
		name   string
		change func(*Declaration)
	}{
		{"unsupported code", func(d *Declaration) {
			d.Code = "us"
			d.Descriptor.Manifest.Code = "us"
		}},
		{"empty code", func(d *Declaration) { d.Code = "" }},
		{"mismatched code", func(d *Declaration) { d.Descriptor.Manifest.Code = "my" }},
		{"invalid manifest", func(d *Declaration) { d.Descriptor.Manifest.ModuleVersion = "" }},
		{"corrupt manifest digest", func(d *Declaration) { d.Descriptor.Manifest.Digest = model.Digest(strings.Repeat("b", 64)) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := registryDeclaration(t, "vn")
			tc.change(&d)
			r, err := NewRegistry([]Declaration{d})
			if r != nil || err == nil {
				t.Fatal("accepted invalid declaration")
			}
		})
	}
	d := registryDeclaration(t, "vn")
	if r, err := NewRegistry([]Declaration{d, d}); r != nil || err == nil {
		t.Fatal("accepted duplicate declaration")
	}
}

func TestRegistrySnapshotsInputAndFreezeCannotEnable(t *testing.T) {
	input := []Declaration{registryDeclaration(t, "vn")}
	want := input[0].Descriptor.Manifest.Clone()
	r, err := NewRegistry(input)
	if err != nil {
		t.Fatal(err)
	}
	// The public API exposes no descriptor. Inspect the retained snapshot to
	// catch slice aliasing even though runtime admission remains closed.
	snapshot := r.(*registry)
	input[0].Descriptor.Manifest.OCRHints[0] = "en"
	input[0].Descriptor.Manifest.Sources[0].ID = "changed"
	input[0].Descriptor.Manifest.Capabilities[0].MissingReason = "changed"
	input[0].Descriptor.Manifest.Assets[0].ByteSize = 100
	input[0].Code = "my"
	input[0].Entry = &Entry{Code: "vn", State: Enabled}
	if !reflect.DeepEqual(snapshot.declarations[0].Descriptor.Manifest, want) || snapshot.declarations[0].Code != "vn" || snapshot.declarations[0].Entry != nil {
		t.Fatal("input mutation changed construction snapshot")
	}
	for i := 0; i < 2; i++ {
		if err := r.Freeze(); err != nil {
			t.Fatalf("Freeze %d: %v", i, err)
		}
		_, err := r.RequireEnabled("")
		requireAbsent(t, "vn", err)
	}
	input[0].Descriptor.Manifest.Assets[0].Path = "data/changed.csv"
	if !reflect.DeepEqual(snapshot.declarations[0].Descriptor.Manifest, want) {
		t.Fatal("input mutation changed frozen snapshot")
	}
	entries := r.Entries()
	entries = append(entries, Entry{Code: "vn", State: Enabled})
	if len(entries) != 1 || len(r.Entries()) != 0 {
		t.Fatal("caller output mutation changed registry entries")
	}
}

func TestReaderSurfaceExcludesFreeze(t *testing.T) {
	reader := reflect.TypeFor[Reader]()
	if reader.NumMethod() != 3 {
		t.Fatalf("Reader exposes %d methods", reader.NumMethod())
	}
	if _, ok := reader.MethodByName("Freeze"); ok {
		t.Fatal("Reader exposes construction Freeze")
	}
	for _, name := range []string{"Lookup", "Entries", "RequireEnabled"} {
		if _, ok := reader.MethodByName(name); !ok {
			t.Fatalf("Reader lacks %s", name)
		}
	}
}

func TestEmptyRegistryRemainsAbsent(t *testing.T) {
	r, err := NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Lookup("")
	requireAbsent(t, "vn", err)
	if err := r.Freeze(); err != nil || len(r.Entries()) != 0 {
		t.Fatalf("empty registry changed on Freeze: %v", err)
	}
}
