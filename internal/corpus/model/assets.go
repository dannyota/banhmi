// Package model defines neutral corpus values without runtime dependencies.
package model

import (
	"fmt"
	"io/fs"
	"strings"
)

// Digest is exactly 64 lowercase hexadecimal SHA-256 characters.
type Digest string

// AssetRef identifies immutable bytes at a portable relative path.
type AssetRef struct {
	Path     string
	SHA256   Digest
	ByteSize int64
}

func (d Digest) Validate() error {
	if len(d) != 64 {
		return fmt.Errorf("digest must contain 64 lowercase hexadecimal characters")
	}
	for _, c := range d {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return fmt.Errorf("digest must contain 64 lowercase hexadecimal characters")
		}
	}
	return nil
}

func (a AssetRef) Validate() error {
	if a.Path == "." || !fs.ValidPath(a.Path) || strings.ContainsAny(a.Path, "\\:") {
		return fmt.Errorf("asset path must be slash-separated and relative without traversal or drive names")
	}
	for _, c := range a.Path {
		if c < 0x20 || c == 0x7f {
			return fmt.Errorf("asset path must not contain control characters")
		}
	}
	if err := a.SHA256.Validate(); err != nil {
		return fmt.Errorf("asset digest: %w", err)
	}
	if a.ByteSize < 0 {
		return fmt.Errorf("asset byte size must not be negative")
	}
	return nil
}
