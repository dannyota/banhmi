//go:build onnx

package onnxembed

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sync"

	tok "github.com/daulet/tokenizers"
	ort "github.com/yalue/onnxruntime_go"

	"danny.vn/banhmi/pkg/rag/embed"
)

// initOnce guards the process-global ONNX Runtime environment.
var initOnce sync.Once
var initErr error

type onnxEmbedder struct {
	mu         sync.Mutex // ORT Run is serialized; the query path is low-QPS
	tk         *tok.Tokenizer
	sess       *ort.DynamicAdvancedSession
	dims       int
	model      string
	tokenLevel bool // true = last_hidden_state [1,seq,dims]; false = dense_vecs [1,dims]
}

func (e *onnxEmbedder) Model() string { return e.model }
func (e *onnxEmbedder) Dims() int     { return e.dims }

// New loads the tokenizer + model and returns an in-process embedder. The model
// must expose input_ids/attention_mask inputs and a last_hidden_state output
// (token-level embeddings); CLS pooling + L2 normalization happen in Go.
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
	tkBytes, err := os.ReadFile(c.TokenizerPath)
	if err != nil {
		return nil, fmt.Errorf("onnxembed: read tokenizer %s: %w", c.TokenizerPath, err)
	}
	t, err := tok.FromBytesWithTruncation(tkBytes, uint32(embed.MaxQueryTokens), tok.TruncationDirectionRight)
	if err != nil {
		return nil, fmt.Errorf("onnxembed: load tokenizer %s: %w", c.TokenizerPath, err)
	}
	var sessOpts *ort.SessionOptions
	if c.CUDA {
		sessOpts, err = ort.NewSessionOptions()
		if err != nil {
			return nil, fmt.Errorf("onnxembed: create session options: %w", err)
		}
		defer sessOpts.Destroy()
		cudaOpts, cerr := ort.NewCUDAProviderOptions()
		if cerr != nil {
			return nil, fmt.Errorf("onnxembed: create CUDA provider: %w", cerr)
		}
		defer cudaOpts.Destroy()
		if cerr := sessOpts.AppendExecutionProviderCUDA(cudaOpts); cerr != nil {
			return nil, fmt.Errorf("onnxembed: append CUDA provider: %w", cerr)
		}
	}
	_, outputs, ioErr := ort.GetInputOutputInfo(c.ModelPath)
	if ioErr != nil {
		return nil, fmt.Errorf("onnxembed: inspect model %s: %w", c.ModelPath, ioErr)
	}
	outputNames := make([]string, len(outputs))
	for i, o := range outputs {
		outputNames[i] = o.Name
	}
	if len(outputNames) == 0 {
		return nil, fmt.Errorf("onnxembed: model %s has no outputs", c.ModelPath)
	}
	slog.Info("onnxembed: model outputs", "names", outputNames, "using", outputNames[0])
	sess, err := ort.NewDynamicAdvancedSession(c.ModelPath,
		[]string{"input_ids", "attention_mask"}, outputNames[:1], sessOpts)
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
	return &onnxEmbedder{tk: t, sess: sess, dims: dims, model: model, tokenLevel: outputNames[0] == "last_hidden_state"}, nil
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
	seqLen := int64(len(ids))
	inShape := ort.NewShape(1, seqLen)
	tin, err := ort.NewTensor(inShape, ids)
	if err != nil {
		return nil, err
	}
	defer tin.Destroy()
	tmask, err := ort.NewTensor(inShape, mask)
	if err != nil {
		return nil, err
	}
	defer tmask.Destroy()

	var res *ort.Tensor[float32]
	if e.tokenLevel {
		// last_hidden_state output: [1, seq_len, dims] — needs CLS pooling.
		res, err = ort.NewEmptyTensor[float32](ort.NewShape(1, seqLen, int64(e.dims)))
	} else {
		// dense_vecs output: [1, dims] — already pooled.
		res, err = ort.NewEmptyTensor[float32](ort.NewShape(1, int64(e.dims)))
	}
	if err != nil {
		return nil, err
	}
	defer res.Destroy()
	if err := e.sess.Run([]ort.Value{tin, tmask}, []ort.Value{res}); err != nil {
		return nil, err
	}
	data := res.GetData()
	vec := make([]float32, e.dims)
	copy(vec, data[:e.dims])
	return l2(vec), nil
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
