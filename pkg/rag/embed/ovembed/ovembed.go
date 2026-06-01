// Package ovembed is an in-process BGE-M3 query embedder backed by the OpenVINO
// Runtime — running the *exact same* INT8 model that OVMS serves for the index, so
// query vectors match the index (~0.9996 cosine vs OVMS, OVMS-equivalent ranking).
//
// It lets the Cloud Run MCP server embed queries itself — no OVMS server, no
// sidecar — a single self-contained binary. Bulk indexing still uses the local
// GPU OVMS path (input unchanged); this is the query path only.
//
// The real implementation is CGO over the official libopenvino_c.so (the stable
// OpenVINO 2.0 C API) plus a static HF tokenizer, compiled only under the
// `openvino` build tag so default builds stay CGO-free. Without the tag, New
// returns an error.
package ovembed

// Config locates the model assets. The model dir must contain the OpenVINO IR
// (openvino_model.xml + .bin) with input_ids/attention_mask inputs and a
// sentence_embedding output (the model's pooled+normalized dense head).
type Config struct {
	ModelDir      string // dir holding openvino_model.xml + openvino_model.bin
	TokenizerPath string // HF tokenizer.json (XLM-RoBERTa)
	Dims          int    // embedding dimension (1024 for BGE-M3)
	// Model is the name the embedder reports; it MUST match the indexed embeddings'
	// model name so query vectors search the right set.
	Model string
}
