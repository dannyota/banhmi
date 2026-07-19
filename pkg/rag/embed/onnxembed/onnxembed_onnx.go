//go:build onnx

package onnxembed

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"runtime"
	"sync"
	"time"

	tok "github.com/daulet/tokenizers"
	ort "github.com/microsoft/onnxruntime/go/onnxruntime"

	"danny.vn/banhmi/pkg/rag/embed"
)

var initOnce sync.Once
var initErr error

type onnxEmbedder struct {
	tkMu        sync.Mutex    // tokenizer FFI only — sub-ms, its C shim's concurrency is unaudited
	sem         chan struct{} // bounds concurrent sess.Run calls; weights are shared, activations are per-run
	tk          *tok.Tokenizer
	sess        *ort.Session
	dims        int
	model       string
	numKVLayers int
	numKVHeads  int
	headDim     int
	kvDtype     ort.TensorElementDataType // FP16 or FP32 — read from the model's KV cache inputs
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

	opts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("onnxembed: create session options: %w", err)
	}
	defer opts.Close()

	// Disable the CPU memory arena so ORT frees activation buffers after each
	// Run() instead of retaining them at high-water mark. On the query path
	// (short sequences, 2–3 QPS) the per-run malloc cost is negligible; the
	// savings are 200–400 MB of anonymous RSS that the arena would hold forever.
	if err := opts.DisableCpuMemArena(); err != nil {
		return nil, fmt.Errorf("onnxembed: disable CPU mem arena: %w", err)
	}
	slog.Info("onnxembed: CPU memory arena disabled")

	if c.CUDA {
		slog.Info("onnxembed: enabling CUDA execution provider")
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

	// Detect the KV cache dtype from the first past_key_values input. INT8
	// models dequantize to float32 internally, so their KV inputs are FP32;
	// FP16 models keep FP16 KV. Reading the model's own metadata avoids a
	// hardcoded assumption that broke when switching model quantization.
	kvDtype := ort.TensorElementDataTypeFloat16 // safe default for the FP16 model
	for _, in := range sess.Inputs() {
		if len(in.Name) > 16 && in.Name[:16] == "past_key_values." {
			kvDtype = in.DataType
			break
		}
	}
	slog.Info("onnxembed: model loaded", "outputs", len(outputNames), "inputs", len(sess.Inputs()), "kv_dtype", kvDtype)

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

	// Default concurrency: one run per core captures the parallelism a single
	// short-sequence run leaves idle; beyond core count runs only time-slice
	// the same cores while each holds its own activation memory.
	conc := c.Concurrency
	if conc <= 0 {
		conc = runtime.NumCPU()
		if conc < 2 {
			conc = 2
		}
	}

	return &onnxEmbedder{
		sem:         make(chan struct{}, conc),
		tk:          t,
		sess:        sess,
		kvDtype:     kvDtype,
		dims:        dims,
		model:       model,
		numKVLayers: kvLayers,
		numKVHeads:  kvHeads,
		headDim:     headDim,
	}, nil
}

const eosTokenID = 151643

func (e *onnxEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	start := time.Now()
	batchSize := len(texts)

	// Tokenize all texts and find max length for padding.
	e.tkMu.Lock()
	encoded := make([][]uint32, batchSize)
	maxLen := 0
	for i, text := range texts {
		ids32, _ := e.tk.Encode(text, true)
		if len(ids32) == 0 {
			e.tkMu.Unlock()
			return nil, fmt.Errorf("onnxembed: empty tokenization for text %d", i)
		}
		encoded[i] = ids32
		if len(ids32) > maxLen {
			maxLen = len(ids32)
		}
	}
	e.tkMu.Unlock()

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

	select {
	case e.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	vecs, err := e.runBatch(ctx, int64(batchSize), int64(maxLen), ids, mask, pos)
	<-e.sem
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
			t, err := ort.NewTensorFromBytes(e.kvDtype, kvShape, []byte{})
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
