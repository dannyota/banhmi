// Package catalog assembles the compiled inventory without runtime effects.
package catalog

import (
	"danny.vn/banhmi/internal/core/country"
	"danny.vn/banhmi/internal/country/vn"
)

// Declarations returns owned inventory metadata. M0 has no runtime entries.
func Declarations() []country.Declaration {
	return []country.Declaration{vn.Declare()}
}
