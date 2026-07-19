// Package onnxembed is an in-process Qwen3-Embedding query embedder backed by
// ONNX Runtime via github.com/microsoft/onnxruntime/go.
//
// It exists so the MCP server can embed queries itself — no sidecar — yielding a
// single self-contained binary. It is the QUERY-time embedder only; bulk indexing
// uses the GPU path (see pkg/rag/embed and docs/design/RAG.md).
//
// Qwen3-Embedding is a decoder model: inputs are input_ids + attention_mask +
// position_ids + 28×2 empty KV cache tensors; the output is last_hidden_state
// [1, seq, 1024]. Pooling is last-token (the EOS position), then L2 normalize.
//
// The real implementation is CGO (ONNX Runtime + a static HF tokenizer) and is
// compiled only under the `onnx` build tag, so default builds stay CGO-free. Build
// the server image with `-tags onnx`; without it, New returns an error.
package onnxembed

// Config locates the model assets and the ONNX Runtime shared library. Paths are
// supplied by the caller (env-driven in pkg/app) so the same code works locally
// and in the image.
type Config struct {
	ModelPath     string // Qwen3-Embedding .onnx (decoder with KV cache inputs)
	TokenizerPath string // HF tokenizer.json (Qwen3 BPE)
	LibPath       string // libonnxruntime.so; empty = default search
	Dims          int    // embedding dimension (1024 for Qwen3-Embedding-0.6B)
	Model         string // name reported by Model(); must match indexed embeddings
	CUDA          bool   // use CUDA execution provider (requires GPU + libonnxruntime-gpu.so)
	NumKVLayers   int    // number of KV cache layer pairs (default 28 for Qwen3-0.6B)
	NumKVHeads    int    // number of KV attention heads (default 8 for Qwen3-0.6B)
	HeadDim       int    // per-head dimension (default 128 for Qwen3-0.6B)
	Concurrency   int    // max concurrent ORT runs on the shared session (0 = NumCPU, min 2)
}
