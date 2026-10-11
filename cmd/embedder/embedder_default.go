//go:build !onnx

package main

import (
	"errors"

	"danny.vn/banhmi/pkg/rag/embed"
)

func newEmbedder(int, int, int) (embed.Embedder, error) {
	return nil, errors.New("embedder requires the onnx build tag: go build -tags onnx")
}
