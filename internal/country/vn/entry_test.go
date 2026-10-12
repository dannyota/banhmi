package vn_test

import (
	"reflect"
	"testing"

	"danny.vn/banhmi/internal/core/country"
	"danny.vn/banhmi/internal/country/vn"
)

func TestDeclareUsesFrozenAssetsWithoutRuntimeEntry(t *testing.T) {
	d := vn.Declare()
	if d.Code != "vn" || d.Entry != nil {
		t.Fatal("Vietnam declaration claimed a runtime entry")
	}
	if !reflect.DeepEqual(d.Descriptor.Manifest, vn.AssetManifest()) {
		t.Fatal("declaration diverged from frozen asset metadata")
	}
	if err := d.Descriptor.Manifest.Validate(); err != nil {
		t.Fatalf("declaration manifest: %v", err)
	}
}

func TestDeclareReturnsIndependentMetadata(t *testing.T) {
	want := vn.AssetManifest()
	d := vn.Declare()
	d.Code = "my"
	d.Entry = &country.Entry{Code: "vn", State: country.Enabled}
	m := &d.Descriptor.Manifest
	m.ModuleVersion = "changed"
	m.OCRHints[0] = "en"
	m.Sources[0].ID = "changed"
	m.Capabilities[0].Version = "changed"
	m.Assets[0].ByteSize++
	got := vn.Declare()
	if got.Code != "vn" || got.Entry != nil || !reflect.DeepEqual(got.Descriptor.Manifest, want) {
		t.Fatal("caller mutation changed a later Vietnam declaration")
	}
	if !reflect.DeepEqual(vn.AssetManifest(), want) {
		t.Fatal("caller mutation changed frozen asset metadata")
	}
}
