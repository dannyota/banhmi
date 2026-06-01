//go:build !onnx

package onnxembed

import (
	"errors"

	"danny.vn/banhmi/pkg/rag/embed"
)

// New reports that ONNX support was not compiled in. Build with `-tags onnx`
// (CGO + ONNX Runtime) to enable the in-process embedder.
func New(Config) (embed.Embedder, error) {
	return nil, errors.New("onnxembed: built without the 'onnx' build tag; rebuild with -tags onnx")
}
