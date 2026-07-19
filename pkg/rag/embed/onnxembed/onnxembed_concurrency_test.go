//go:build onnx

package onnxembed

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// vmHWM reads the process peak RSS (high-water mark) in MB from /proc.
func vmHWM(t *testing.T) float64 {
	t.Helper()
	f, err := os.Open("/proc/self/status")
	if err != nil {
		t.Fatalf("open /proc/self/status: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "VmHWM:") {
			kb, _ := strconv.Atoi(strings.Fields(sc.Text())[1])
			return float64(kb) / 1024
		}
	}
	t.Fatal("VmHWM not found")
	return 0
}

// TestConcurrentEmbedParity verifies that concurrent Embed calls are safe and
// produce the same vectors as sequential calls, and logs the measured
// activation-memory cost per additional in-flight run. Skips when the local
// model assets are absent (set BANHMI_ONNX_MODEL / BANHMI_ONNX_TOKENIZER /
// BANHMI_ONNX_LIB, e.g. via the Makefile ONNX_ENV).
func TestConcurrentEmbedParity(t *testing.T) {
	modelPath := os.Getenv("BANHMI_ONNX_MODEL")
	tokPath := os.Getenv("BANHMI_ONNX_TOKENIZER")
	if modelPath == "" || tokPath == "" {
		t.Skip("BANHMI_ONNX_MODEL / BANHMI_ONNX_TOKENIZER not set")
	}
	if _, err := os.Stat(modelPath); err != nil {
		t.Skipf("model not present: %v", err)
	}

	const parallel = 8
	e, err := New(Config{
		ModelPath:     modelPath,
		TokenizerPath: tokPath,
		LibPath:       os.Getenv("BANHMI_ONNX_LIB"),
		Concurrency:   parallel,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	query := "Ngân hàng thương mại phải đáp ứng yêu cầu bảo mật nào khi cung cấp dịch vụ " +
		"ngân hàng điện tử và mở tài khoản thanh toán bằng phương thức điện tử eKYC?"

	// Warm-up + reference vector.
	ref, err := e.Embed(ctx, []string{query})
	if err != nil {
		t.Fatalf("warm-up embed: %v", err)
	}

	// Phase 1: sequential — establishes single-run peak memory.
	seqStart := time.Now()
	for i := 0; i < parallel; i++ {
		if _, err := e.Embed(ctx, []string{query}); err != nil {
			t.Fatalf("sequential embed %d: %v", i, err)
		}
	}
	seqElapsed := time.Since(seqStart)
	hwmSeq := vmHWM(t)

	// Phase 2: concurrent — the extra high-water over phase 1 is the cost of
	// the additional in-flight runs.
	var wg sync.WaitGroup
	vecs := make([][][]float32, parallel)
	errs := make([]error, parallel)
	conStart := time.Now()
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			vecs[i], errs[i] = e.Embed(ctx, []string{query})
		}(i)
	}
	wg.Wait()
	conElapsed := time.Since(conStart)
	hwmCon := vmHWM(t)

	for i := 0; i < parallel; i++ {
		if errs[i] != nil {
			t.Fatalf("concurrent embed %d: %v", i, errs[i])
		}
		if len(vecs[i]) != 1 || len(vecs[i][0]) != len(ref[0]) {
			t.Fatalf("concurrent embed %d: wrong shape", i)
		}
		var maxDiff float64
		for j := range ref[0] {
			if d := math.Abs(float64(vecs[i][0][j] - ref[0][j])); d > maxDiff {
				maxDiff = d
			}
		}
		// FP16 CPU inference with varying thread interleaving: tiny reduction
		// -order drift is expected; anything larger means shared-state corruption.
		if maxDiff > 1e-2 {
			t.Errorf("concurrent embed %d: max element diff %.4g vs sequential", i, maxDiff)
		}
	}

	perRun := (hwmCon - hwmSeq) / float64(parallel-1)
	t.Log(fmt.Sprintf("sequential %d runs: %.1fs; concurrent %d runs: %.1fs", parallel, seqElapsed.Seconds(), parallel, conElapsed.Seconds()))
	t.Log(fmt.Sprintf("peak RSS after sequential: %.0f MB; after concurrent: %.0f MB; ≈%.0f MB per extra in-flight run", hwmSeq, hwmCon, perRun))
}
