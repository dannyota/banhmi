//go:build !onnx

package main

import (
	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/rag/embed"
)

func newLocalEmbedder(cfg *config.Config) (embed.Embedder, error) {
	return embed.New(cfg.EmbedEndpoint(), config.EmbedModel, config.EmbedDims, ""), nil
}
