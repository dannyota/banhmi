package sec

import (
	"golang.org/x/text/encoding/charmap"
)

// decodeCP874 converts a TIS-620/CP874 (Windows-874) byte slice to UTF-8.
// The SEC NRS portal serves HTML in this legacy Thai encoding.
func decodeCP874(b []byte) (string, error) {
	decoded, err := charmap.Windows874.NewDecoder().Bytes(b)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}
