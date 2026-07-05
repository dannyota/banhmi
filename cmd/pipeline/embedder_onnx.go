//go:build onnx

package main

import (
	"fmt"
	"os"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/rag/embed"
	"danny.vn/banhmi/pkg/rag/embed/onnxembed"
)

func newServeEmbedder() (embed.Embedder, error) {
	e, err := onnxembed.New(onnxembed.Config{
		ModelPath:     envOrDefault("BANHMI_ONNX_MODEL", "/models/bge-m3/model_quantized.onnx"),
		TokenizerPath: envOrDefault("BANHMI_ONNX_TOKENIZER", "/models/bge-m3/tokenizer.json"),
		LibPath:       os.Getenv("BANHMI_ONNX_LIB"),
		Dims:          config.EmbedDims,
		Model:         config.EmbedModel,
		CUDA:          os.Getenv("BANHMI_ONNX_CUDA") == "1",
	})
	if err != nil {
		return nil, fmt.Errorf("onnx embedder: %w", err)
	}
	return e, nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
