//go:build onnx

package main

import (
	"fmt"
	"log/slog"
	"os"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/rag/embed"
	"danny.vn/banhmi/pkg/rag/embed/onnxembed"
)

func newEmbedder() (embed.Embedder, error) {
	cuda := os.Getenv("BANHMI_ONNX_CUDA") == "1"
	modelPath := envOrDefault("BANHMI_ONNX_MODEL", "/models/qwen3-embedding/model_fp16.onnx")
	tokPath := envOrDefault("BANHMI_ONNX_TOKENIZER", "/models/qwen3-embedding/tokenizer.json")
	libPath := os.Getenv("BANHMI_ONNX_LIB")

	slog.Info("onnx embedder config",
		"model_path", modelPath,
		"tokenizer_path", tokPath,
		"lib_path", libPath,
		"cuda", cuda,
		"model_name", config.EmbedModel,
		"dims", config.EmbedDims)

	e, err := onnxembed.New(onnxembed.Config{
		ModelPath:     modelPath,
		TokenizerPath: tokPath,
		LibPath:       libPath,
		Dims:          config.EmbedDims,
		Model:         config.EmbedModel,
		CUDA:          cuda,
	})
	if err != nil {
		return nil, fmt.Errorf("onnx embedder: %w", err)
	}
	slog.Info("onnx embedder ready", "model", e.Model(), "dims", e.Dims())
	return e, nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
