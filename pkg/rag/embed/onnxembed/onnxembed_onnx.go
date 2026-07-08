//go:build onnx

package onnxembed

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sync"
	"time"

	tok "github.com/daulet/tokenizers"
	ort "github.com/microsoft/onnxruntime/go/onnxruntime"

	"danny.vn/banhmi/pkg/rag/embed"
)

var initOnce sync.Once
var initErr error

type onnxEmbedder struct {
	mu          sync.Mutex
	tk          *tok.Tokenizer
	sess        *ort.Session
	dims        int
	model       string
	numKVLayers int
	numKVHeads  int
	headDim     int
}

func (e *onnxEmbedder) Model() string { return e.model }
func (e *onnxEmbedder) Dims() int     { return e.dims }

func New(c Config) (embed.Embedder, error) {
	initOnce.Do(func() {
		slog.Info("onnxembed: initializing ORT", "lib", c.LibPath)
		if c.LibPath != "" {
			ort.SetSharedLibraryPath(c.LibPath)
		}
		initErr = ort.Init()
	})
	if initErr != nil {
		return nil, fmt.Errorf("onnxembed: init ONNX Runtime: %w", initErr)
	}
	slog.Info("onnxembed: ORT initialized")
	tkBytes, err := os.ReadFile(c.TokenizerPath)
	if err != nil {
		return nil, fmt.Errorf("onnxembed: read tokenizer %s: %w", c.TokenizerPath, err)
	}
	t, err := tok.FromBytesWithTruncation(tkBytes, uint32(embed.MaxQueryTokens), tok.TruncationDirectionRight)
	if err != nil {
		return nil, fmt.Errorf("onnxembed: load tokenizer %s: %w", c.TokenizerPath, err)
	}

	var opts *ort.SessionOptions
	if c.CUDA {
		slog.Info("onnxembed: enabling CUDA execution provider")
		opts, err = ort.NewSessionOptions()
		if err != nil {
			return nil, fmt.Errorf("onnxembed: create session options: %w", err)
		}
		defer opts.Close()
		if err := opts.AppendExecutionProvider("CUDAExecutionProvider", nil); err != nil {
			slog.Error("onnxembed: CUDA provider failed, falling back to CPU", "err", err)
		} else {
			slog.Info("onnxembed: CUDA provider registered")
		}
	} else {
		slog.Info("onnxembed: CUDA disabled, using CPU")
	}

	slog.Info("onnxembed: loading model", "path", c.ModelPath)
	sess, err := ort.NewSession(c.ModelPath, opts)
	if err != nil {
		return nil, fmt.Errorf("onnxembed: open model %s: %w", c.ModelPath, err)
	}

	outputNames := make([]string, len(sess.Outputs()))
	for i, o := range sess.Outputs() {
		outputNames[i] = o.Name
	}
	slog.Info("onnxembed: model outputs", "names", outputNames, "inputs", len(sess.Inputs()))

	dims := c.Dims
	if dims <= 0 {
		dims = 1024
	}
	model := c.Model
	if model == "" {
		model = "qwen3-embedding-0.6b"
	}
	kvLayers := c.NumKVLayers
	if kvLayers <= 0 {
		kvLayers = 28
	}
	kvHeads := c.NumKVHeads
	if kvHeads <= 0 {
		kvHeads = 8
	}
	headDim := c.HeadDim
	if headDim <= 0 {
		headDim = 128
	}

	return &onnxEmbedder{
		tk:          t,
		sess:        sess,
		dims:        dims,
		model:       model,
		numKVLayers: kvLayers,
		numKVHeads:  kvHeads,
		headDim:     headDim,
	}, nil
}

func (e *onnxEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	start := time.Now()
	out := make([][]float32, len(texts))
	for i, text := range texts {
		ids32, _ := e.tk.Encode(text, true)
		n := len(ids32)
		if n == 0 {
			return nil, fmt.Errorf("onnxembed: empty tokenization for text %d", i)
		}
		ids := make([]int64, n)
		mask := make([]int64, n)
		pos := make([]int64, n)
		for j, v := range ids32 {
			ids[j] = int64(v)
			mask[j] = 1
			pos[j] = int64(j)
		}
		vec, err := e.run(ctx, ids, mask, pos)
		if err != nil {
			return nil, fmt.Errorf("onnxembed: embed text %d: %w", i, err)
		}
		out[i] = vec
	}
	slog.Debug("onnxembed: batch done", "texts", len(texts), "elapsed", time.Since(start))
	return out, nil
}

func (e *onnxEmbedder) run(ctx context.Context, ids, mask, pos []int64) ([]float32, error) {
	seqLen := int64(len(ids))
	inputs := make(map[string]*ort.Tensor, 3+e.numKVLayers*2)
	var toClose []*ort.Tensor

	cleanup := func() {
		for _, t := range toClose {
			t.Close()
		}
	}

	addTensor := func(name string, shape []int64, data []int64) error {
		t, err := ort.CreateTensor(shape, data)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		toClose = append(toClose, t)
		inputs[name] = t
		return nil
	}

	shape := []int64{1, seqLen}
	if err := addTensor("input_ids", shape, ids); err != nil {
		cleanup()
		return nil, err
	}
	if err := addTensor("attention_mask", shape, mask); err != nil {
		cleanup()
		return nil, err
	}
	if err := addTensor("position_ids", shape, pos); err != nil {
		cleanup()
		return nil, err
	}

	// Empty KV cache tensors: [1, num_heads, 0, head_dim], dtype float16.
	kvShape := []int64{1, int64(e.numKVHeads), 0, int64(e.headDim)}
	for i := 0; i < e.numKVLayers; i++ {
		for _, role := range []string{"key", "value"} {
			name := fmt.Sprintf("past_key_values.%d.%s", i, role)
			t, err := ort.NewTensorFromBytes(ort.TensorElementDataTypeFloat16, kvShape, []byte{})
			if err != nil {
				cleanup()
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			toClose = append(toClose, t)
			inputs[name] = t
		}
	}

	results, err := e.sess.Run(ctx, inputs, []string{"last_hidden_state"})
	cleanup()
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, r := range results {
			r.Close()
		}
	}()

	out, ok := results["last_hidden_state"]
	if !ok {
		return nil, fmt.Errorf("last_hidden_state not in output")
	}

	data, err := ort.TensorData[float32](out)
	if err != nil {
		return nil, err
	}

	// Last-token pooling: take the hidden state at the last position (EOS token).
	lastOffset := int(seqLen-1) * e.dims
	vec := make([]float32, e.dims)
	copy(vec, data[lastOffset:lastOffset+e.dims])
	return l2(vec), nil
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
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x / n
	}
	return out
}
