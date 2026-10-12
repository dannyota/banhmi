package model_test

import (
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/model"
)

func TestDigestValidation(t *testing.T) {
	for _, value := range []string{"", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		if err := model.Digest(value).Validate(); err == nil {
			t.Errorf("Validate(%q) accepted invalid digest", value)
		}
	}
	if err := model.Digest("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef").Validate(); err != nil {
		t.Fatalf("valid digest: %v", err)
	}
}

func TestAssetPathValidation(t *testing.T) {
	valid := model.AssetRef{Path: "data/seeds/điều.csv", SHA256: model.Digest(strings.Repeat("0", 64))}
	if err := valid.Validate(); err != nil {
		t.Fatalf("zero-byte portable asset: %v", err)
	}
	for _, path := range []string{"", ".", "..", "../data.csv", "data/../seed.csv", "data/./seed.csv", "/data.csv", "data//seed.csv", "data/", `data\seed.csv`, `C:\data.csv`, "C:data.csv", "data/C:/seed.csv", "data/\x00.csv", "data/\xff.csv"} {
		t.Run(path, func(t *testing.T) {
			asset := valid
			asset.Path = path
			if err := asset.Validate(); err == nil {
				t.Fatalf("accepted invalid path %q", path)
			}
		})
	}
	negative := valid
	negative.ByteSize = -1
	if err := negative.Validate(); err == nil {
		t.Fatal("accepted negative size")
	}
	badHash := valid
	badHash.SHA256 = ""
	if err := badHash.Validate(); err == nil {
		t.Fatal("accepted missing asset hash")
	}
}
