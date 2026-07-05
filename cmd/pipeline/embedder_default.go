//go:build !onnx

package main

import (
	"errors"

	"danny.vn/banhmi/pkg/rag/embed"
)

func newServeEmbedder() (embed.Embedder, error) {
	return nil, errors.New("serve-embed requires the onnx build tag: go build -tags onnx")
}
