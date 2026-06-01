//go:build onnx

package onnxembed

import (
	"context"
	"fmt"
	"math"
	"sync"

	tok "github.com/daulet/tokenizers"
	ort "github.com/yalue/onnxruntime_go"

	"danny.vn/banhmi/pkg/rag/embed"
)

// initOnce guards the process-global ONNX Runtime environment.
var initOnce sync.Once
var initErr error

type onnxEmbedder struct {
	mu    sync.Mutex // ORT Run is serialized; the query path is low-QPS
	tk    *tok.Tokenizer
	sess  *ort.DynamicAdvancedSession
	dims  int
	model string
}

func (e *onnxEmbedder) Model() string { return e.model }
func (e *onnxEmbedder) Dims() int     { return e.dims }

// New loads the tokenizer + model and returns an in-process embedder. The model
// must expose input_ids/attention_mask inputs and a dense_vecs output (the
// pre-pooled, pre-normalized BGE-M3 dense vector).
func New(c Config) (embed.Embedder, error) {
	initOnce.Do(func() {
		if c.LibPath != "" {
			ort.SetSharedLibraryPath(c.LibPath)
		}
		initErr = ort.InitializeEnvironment()
	})
	if initErr != nil {
		return nil, fmt.Errorf("onnxembed: init ONNX Runtime: %w", initErr)
	}
	t, err := tok.FromFile(c.TokenizerPath)
	if err != nil {
		return nil, fmt.Errorf("onnxembed: load tokenizer %s: %w", c.TokenizerPath, err)
	}
	sess, err := ort.NewDynamicAdvancedSession(c.ModelPath,
		[]string{"input_ids", "attention_mask"}, []string{"dense_vecs"}, nil)
	if err != nil {
		return nil, fmt.Errorf("onnxembed: open model %s: %w", c.ModelPath, err)
	}
	dims := c.Dims
	if dims <= 0 {
		dims = 1024
	}
	model := c.Model
	if model == "" {
		model = "bge-m3"
	}
	return &onnxEmbedder{tk: t, sess: sess, dims: dims, model: model}, nil
}

// Embed returns one L2-normalized vector per input text. The query path embeds a
// single text at a time, so each text is run individually (no padding).
func (e *onnxEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	out := make([][]float32, len(texts))
	for i, text := range texts {
		ids32, _ := e.tk.Encode(text, true) // add special tokens (<s> … </s>)
		n := len(ids32)
		if n == 0 { // defensive: never feed a zero-length tensor to ONNX Runtime
			return nil, fmt.Errorf("onnxembed: empty tokenization for text %d", i)
		}
		ids := make([]int64, n)
		mask := make([]int64, n)
		for j, v := range ids32 {
			ids[j] = int64(v)
			mask[j] = 1
		}
		vec, err := e.run(ids, mask)
		if err != nil {
			return nil, fmt.Errorf("onnxembed: embed text %d: %w", i, err)
		}
		out[i] = vec
	}
	return out, nil
}

func (e *onnxEmbedder) run(ids, mask []int64) ([]float32, error) {
	shape := ort.NewShape(1, int64(len(ids)))
	tin, err := ort.NewTensor(shape, ids)
	if err != nil {
		return nil, err
	}
	defer tin.Destroy()
	tmask, err := ort.NewTensor(shape, mask)
	if err != nil {
		return nil, err
	}
	defer tmask.Destroy()
	res, err := ort.NewEmptyTensor[float32](ort.NewShape(1, int64(e.dims)))
	if err != nil {
		return nil, err
	}
	defer res.Destroy()
	if err := e.sess.Run([]ort.Value{tin, tmask}, []ort.Value{res}); err != nil {
		return nil, err
	}
	return l2(res.GetData()), nil
}

// l2 returns an L2-normalized copy (the model already normalizes, but we guard
// against drift and match the OVMS path's normalized output).
func l2(v []float32) []float32 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	n := float32(math.Sqrt(s))
	if n == 0 {
		n = 1
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x / n
	}
	return out
}
