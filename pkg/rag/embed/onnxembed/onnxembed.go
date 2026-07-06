// Package onnxembed is an in-process BGE-M3 query embedder backed by ONNX Runtime.
//
// It exists so the Cloud Run MCP server can embed queries itself — no OVMS,
// no sidecar — yielding a single self-contained binary. It is the QUERY-time
// embedder only; bulk indexing uses the GPU path (see pkg/rag/embed and
// docs/design/RAG.md).
//
// The model outputs token-level hidden states (last_hidden_state); CLS pooling
// and L2 normalization happen in Go.
//
// The real implementation is CGO (ONNX Runtime + a static HF tokenizer) and is
// compiled only under the `onnx` build tag, so default builds stay CGO-free. Build
// the server image with `-tags onnx`; without it, New returns an error.
package onnxembed

// Config locates the model assets and the ONNX Runtime shared library. Paths are
// supplied by the caller (env-driven in pkg/app) so the same code works locally
// and in the image.
type Config struct {
	ModelPath     string // BGE-M3 .onnx (inputs: input_ids, attention_mask; output: last_hidden_state)
	TokenizerPath string // HF tokenizer.json (XLM-RoBERTa)
	LibPath       string // libonnxruntime.so; empty = onnxruntime_go default search
	Dims          int    // embedding dimension (1024 for BGE-M3)
	// Model is the name the embedder reports. It MUST match the indexed embeddings'
	// model name so query vectors search the right set (the index was built with the
	// OVMS BGE-M3 INT8 model; this ONNX runtime embeds the same model ~0.98 cosine).
	Model string
	CUDA  bool // use CUDA execution provider (requires GPU + libonnxruntime-gpu.so)
}
