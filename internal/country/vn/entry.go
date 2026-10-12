package vn

import "danny.vn/banhmi/internal/core/country"

// Declare returns owned Vietnam inventory metadata without a runtime entry.
func Declare() country.Declaration {
	return country.Declaration{
		Code:       "vn",
		Descriptor: country.Descriptor{Manifest: AssetManifest()},
	}
}
