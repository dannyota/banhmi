//go:build !onnx

package main

import (
	"errors"

	"danny.vn/banhmi/pkg/rag/embed"
)

func newEmbedder() (embed.Embedder, error) {
	return nil, errors.New("embedder requires the onnx build tag: go build -tags onnx")
}
