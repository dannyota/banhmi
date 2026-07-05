//go:build onnx

package main

import (
	"fmt"
	"os"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/rag/embed"
	"danny.vn/banhmi/pkg/rag/embed/onnxembed"
)

func newLocalEmbedder(cfg *config.Config) (embed.Embedder, error) {
	if cfg.EmbedEngine() == "onnx" {
		c := onnxembed.Config{
			ModelPath:     envOr("BANHMI_ONNX_MODEL", "/models/bge-m3/model_quantized.onnx"),
			TokenizerPath: envOr("BANHMI_ONNX_TOKENIZER", "/models/bge-m3/tokenizer.json"),
			LibPath:       os.Getenv("BANHMI_ONNX_LIB"),
			Dims:          config.EmbedDims,
			Model:         config.EmbedModel,
		}
		e, err := onnxembed.New(c)
		if err != nil {
			return nil, fmt.Errorf("onnx embedder: %w", err)
		}
		return e, nil
	}
	return embed.New(cfg.EmbedEndpoint(), config.EmbedModel, config.EmbedDims, ""), nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
