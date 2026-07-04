//go:build openvino

package ovembed

/*
#cgo LDFLAGS: -lopenvino_c
#include "openvino/c/openvino.h"
#include <stdlib.h>
// cgo cannot call variadic C functions; wrap compile in fixed-arg C functions.
// LATENCY hint + minimal resources: the query path serializes inference
// (low-QPS, one reused infer request), so extra streams/threads waste memory.
static ov_status_e ov_compile_cpu(const ov_core_t* core, const ov_model_t* model, ov_compiled_model_t** cm) {
    return ov_core_compile_model(core, model, "CPU", 6, cm,
        "PERFORMANCE_HINT", "LATENCY",
        "NUM_STREAMS", "1",
        "INFERENCE_NUM_THREADS", "1");
}
static ov_status_e ov_compile_gpu(const ov_core_t* core, const ov_model_t* model, ov_compiled_model_t** cm) {
    return ov_core_compile_model(core, model, "GPU", 2, cm,
        "PERFORMANCE_HINT", "LATENCY");
}
static ov_status_e ov_compile_auto(const ov_core_t* core, const ov_model_t* model, ov_compiled_model_t** cm) {
    return ov_core_compile_model(core, model, "AUTO", 2, cm,
        "PERFORMANCE_HINT", "LATENCY");
}
*/
import "C"

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	tok "github.com/daulet/tokenizers"

	"danny.vn/banhmi/pkg/rag/embed"
)

type ovEmbedder struct {
	mu       sync.Mutex // one infer request, serialized; the query path is low-QPS
	core     *C.ov_core_t
	compiled *C.ov_compiled_model_t
	req      *C.ov_infer_request_t
	tk       *tok.Tokenizer
	dims     int
	model    string
	// cached C strings for tensor names (freed never — process-lifetime singleton)
	nIn1, nIn2, nOut *C.char
}

// compileForDevice compiles the model for the requested device. "AUTO" tries GPU
// first and falls back to CPU if GPU compilation fails (e.g. no GPU driver).
// Returns the compiled model, the actual device used, and any error.
func compileForDevice(core *C.ov_core_t, model *C.ov_model_t, device string) (*C.ov_compiled_model_t, string, error) {
	var compiled *C.ov_compiled_model_t
	switch device {
	case "GPU":
		if err := ck(C.ov_compile_gpu(core, model, &compiled), "compile_model(GPU)"); err != nil {
			return nil, "", err
		}
		return compiled, "GPU", nil
	case "CPU":
		if err := ck(C.ov_compile_cpu(core, model, &compiled), "compile_model(CPU)"); err != nil {
			return nil, "", err
		}
		return compiled, "CPU", nil
	default:
		// AUTO: try GPU, fall back to CPU. OpenVINO's AUTO device can hang or
		// error when no GPU is present, so we do the fallback explicitly.
		if err := ck(C.ov_compile_gpu(core, model, &compiled), "compile_model(GPU)"); err == nil {
			slog.Info("ovembed: compiled on GPU")
			return compiled, "GPU", nil
		}
		slog.Info("ovembed: GPU unavailable, falling back to CPU")
		if err := ck(C.ov_compile_cpu(core, model, &compiled), "compile_model(CPU)"); err != nil {
			return nil, "", err
		}
		return compiled, "CPU", nil
	}
}

func ck(st C.ov_status_e, what string) error {
	if st != C.OK {
		return fmt.Errorf("ovembed: %s: ov_status=%d", what, int(st))
	}
	return nil
}

// New compiles the model and loads the tokenizer. Config.Device selects the
// inference device: "CPU", "GPU", or "AUTO" (default — try GPU, fall back to
// CPU). The OpenVINO Runtime shared libraries must be resolvable at load time.
func New(c Config) (embed.Embedder, error) {
	xml := C.CString(filepath.Join(c.ModelDir, "openvino_model.xml"))
	bin := C.CString(filepath.Join(c.ModelDir, "openvino_model.bin"))
	defer C.free(unsafe.Pointer(xml))
	defer C.free(unsafe.Pointer(bin))

	var core *C.ov_core_t
	if err := ck(C.ov_core_create(&core), "core_create"); err != nil {
		return nil, err
	}
	var model *C.ov_model_t
	if err := ck(C.ov_core_read_model(core, xml, bin, &model), "read_model"); err != nil {
		return nil, err
	}
	defer C.ov_model_free(model)
	compiled, _, err := compileForDevice(core, model, strings.ToUpper(strings.TrimSpace(c.Device)))
	if err != nil {
		return nil, err
	}
	var req *C.ov_infer_request_t
	if err := ck(C.ov_compiled_model_create_infer_request(compiled, &req), "create_infer_request"); err != nil {
		return nil, err
	}
	tkBytes, err := os.ReadFile(c.TokenizerPath)
	if err != nil {
		return nil, fmt.Errorf("ovembed: read tokenizer %s: %w", c.TokenizerPath, err)
	}
	// Truncate queries at embed.MaxQueryTokens — accuracy-neutral for real
	// queries, but it caps the reused infer request's activation shape.
	t, err := tok.FromBytesWithTruncation(tkBytes, uint32(embed.MaxQueryTokens), tok.TruncationDirectionRight)
	if err != nil {
		return nil, fmt.Errorf("ovembed: load tokenizer %s: %w", c.TokenizerPath, err)
	}
	dims := c.Dims
	if dims <= 0 {
		dims = 1024
	}
	model2 := c.Model
	if model2 == "" {
		model2 = "bge-m3"
	}
	return &ovEmbedder{
		core: core, compiled: compiled, req: req, tk: t, dims: dims, model: model2,
		nIn1: C.CString("input_ids"), nIn2: C.CString("attention_mask"), nOut: C.CString("sentence_embedding"),
	}, nil
}

func (e *ovEmbedder) Model() string { return e.model }
func (e *ovEmbedder) Dims() int     { return e.dims }

func (e *ovEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([][]float32, len(texts))
	for i, text := range texts {
		v, err := e.one(text)
		if err != nil {
			return nil, fmt.Errorf("ovembed: embed text %d: %w", i, err)
		}
		out[i] = v
	}
	return out, nil
}

func (e *ovEmbedder) one(text string) ([]float32, error) {
	ids32, _ := e.tk.Encode(text, true) // add special tokens (CLS … SEP)
	n := len(ids32)
	if n == 0 { // defensive: never pass a zero-length tensor to ONNX/OpenVINO
		return nil, fmt.Errorf("empty tokenization")
	}
	// Inputs in C memory so no Go pointer is retained by the tensor.
	idsC := C.malloc(C.size_t(n * 8))
	maskC := C.malloc(C.size_t(n * 8))
	defer C.free(idsC)
	defer C.free(maskC)
	ids := unsafe.Slice((*int64)(idsC), n)
	mask := unsafe.Slice((*int64)(maskC), n)
	for j, v := range ids32 {
		ids[j] = int64(v)
		mask[j] = 1
	}
	dims := []C.int64_t{1, C.int64_t(n)}
	var shape C.ov_shape_t
	if err := ck(C.ov_shape_create(2, &dims[0], &shape), "shape_create"); err != nil {
		return nil, err
	}
	defer C.ov_shape_free(&shape)

	var tIds, tMask, tOut *C.ov_tensor_t
	if err := ck(C.ov_tensor_create_from_host_ptr(C.I64, shape, idsC, &tIds), "tensor input_ids"); err != nil {
		return nil, err
	}
	defer C.ov_tensor_free(tIds)
	if err := ck(C.ov_tensor_create_from_host_ptr(C.I64, shape, maskC, &tMask), "tensor attention_mask"); err != nil {
		return nil, err
	}
	defer C.ov_tensor_free(tMask)
	if err := ck(C.ov_infer_request_set_tensor(e.req, e.nIn1, tIds), "set input_ids"); err != nil {
		return nil, err
	}
	if err := ck(C.ov_infer_request_set_tensor(e.req, e.nIn2, tMask), "set attention_mask"); err != nil {
		return nil, err
	}
	if err := ck(C.ov_infer_request_infer(e.req), "infer"); err != nil {
		return nil, err
	}
	if err := ck(C.ov_infer_request_get_tensor(e.req, e.nOut, &tOut), "get sentence_embedding"); err != nil {
		return nil, err
	}
	defer C.ov_tensor_free(tOut)
	var data unsafe.Pointer
	if err := ck(C.ov_tensor_data(tOut, &data), "tensor_data"); err != nil {
		return nil, err
	}
	src := unsafe.Slice((*float32)(data), e.dims)
	res := make([]float32, e.dims)
	copy(res, src)
	return l2(res), nil
}

func l2(v []float32) []float32 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	n := float32(math.Sqrt(s))
	if n == 0 {
		n = 1
	}
	for i := range v {
		v[i] /= n
	}
	return v
}
