//go:build !openvino

package ovembed

import (
	"errors"

	"danny.vn/banhmi/pkg/rag/embed"
)

// New reports that OpenVINO support was not compiled in. Build with `-tags openvino`
// (CGO + the OpenVINO Runtime) to enable the in-process embedder.
func New(Config) (embed.Embedder, error) {
	return nil, errors.New("ovembed: built without the 'openvino' build tag; rebuild with -tags openvino")
}
