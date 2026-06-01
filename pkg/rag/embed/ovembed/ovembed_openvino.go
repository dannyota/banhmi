//go:build openvino

package ovembed

/*
#cgo LDFLAGS: -lopenvino_c
#include "openvino/c/openvino.h"
#include <stdlib.h>
// cgo cannot call variadic C functions; wrap compile with zero properties.
static ov_status_e ov_compile_cpu(const ov_core_t* core, const ov_model_t* model, ov_compiled_model_t** cm) {
    return ov_core_compile_model(core, model, "CPU", 0, cm);
}
*/
import "C"

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
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

func ck(st C.ov_status_e, what string) error {
	if st != C.OK {
		return fmt.Errorf("ovembed: %s: ov_status=%d", what, int(st))
	}
	return nil
}

// New compiles the model for CPU and loads the tokenizer. The OpenVINO Runtime
// shared libraries must be resolvable at load time (rpath or LD_LIBRARY_PATH).
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
	var compiled *C.ov_compiled_model_t
	if err := ck(C.ov_compile_cpu(core, model, &compiled), "compile_model"); err != nil {
		return nil, err
	}
	var req *C.ov_infer_request_t
	if err := ck(C.ov_compiled_model_create_infer_request(compiled, &req), "create_infer_request"); err != nil {
		return nil, err
	}
	t, err := tok.FromFile(c.TokenizerPath)
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
