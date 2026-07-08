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

const eosTokenID = 151643

func (e *onnxEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	start := time.Now()
	batchSize := len(texts)

	// Tokenize all texts and find max length for padding.
	encoded := make([][]uint32, batchSize)
	maxLen := 0
	for i, text := range texts {
		ids32, _ := e.tk.Encode(text, true)
		if len(ids32) == 0 {
			return nil, fmt.Errorf("onnxembed: empty tokenization for text %d", i)
		}
		encoded[i] = ids32
		if len(ids32) > maxLen {
			maxLen = len(ids32)
		}
	}

	// Build padded [batchSize, maxLen] tensors.
	ids := make([]int64, batchSize*maxLen)
	mask := make([]int64, batchSize*maxLen)
	pos := make([]int64, batchSize*maxLen)

	for i, enc := range encoded {
		seqLen := len(enc)
		rowOffset := i * maxLen
		for j := 0; j < maxLen; j++ {
			if j < seqLen {
				ids[rowOffset+j] = int64(enc[j])
				mask[rowOffset+j] = 1
				pos[rowOffset+j] = int64(j)
			} else {
				ids[rowOffset+j] = eosTokenID // pad with EOS
				mask[rowOffset+j] = 0
				pos[rowOffset+j] = 0
			}
		}
	}

	vecs, err := e.runBatch(ctx, int64(batchSize), int64(maxLen), ids, mask, pos)
	if err != nil {
		return nil, err
	}
	slog.Debug("onnxembed: batch done", "texts", batchSize, "max_len", maxLen, "elapsed", time.Since(start))
	return vecs, nil
}

func (e *onnxEmbedder) runBatch(ctx context.Context, batchSize, seqLen int64, ids, mask, pos []int64) ([][]float32, error) {
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

	shape := []int64{batchSize, seqLen}
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

	// Empty KV cache: [batchSize, num_heads, 0, head_dim].
	kvShape := []int64{batchSize, int64(e.numKVHeads), 0, int64(e.headDim)}
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

	// Last-token pooling per text: find last real token via attention_mask.
	vecs := make([][]float32, batchSize)
	for i := int64(0); i < batchSize; i++ {
		lastPos := int64(0)
		for j := int64(0); j < seqLen; j++ {
			if mask[i*seqLen+j] == 1 {
				lastPos = j
			}
		}
		offset := int((i*seqLen + lastPos)) * e.dims
		vec := make([]float32, e.dims)
		copy(vec, data[offset:offset+e.dims])
		vecs[i] = l2(vec)
	}
	return vecs, nil
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
