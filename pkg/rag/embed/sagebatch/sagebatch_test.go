package sagebatch

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func vec(dims int, v float32) []float32 {
	out := make([]float32, dims)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestInputWriterRoundTrip(t *testing.T) {
	iw := newInputWriter()
	texts := []string{"first text", "second\twith tab", "third 漢字"}
	for _, text := range texts {
		if err := iw.Write(text); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := iw.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if iw.count != len(texts) {
		t.Fatalf("count = %d, want %d", iw.count, len(texts))
	}

	// Decompress and verify each line.
	gz, err := gzip.NewReader(bytes.NewReader(iw.buf.Bytes()))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	data, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != len(texts) {
		t.Fatalf("wrote %d lines, want %d", len(lines), len(texts))
	}
	for i, line := range lines {
		var row inputRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if row.Index != i || row.Text != texts[i] {
			t.Errorf("line %d = %+v, want index %d text %q", i, row, i, texts[i])
		}
	}
}

func TestStreamParseVectors(t *testing.T) {
	dims := 4
	n := 3
	vectors := map[int][]float32{
		0: vec(dims, 0.0),
		1: vec(dims, 0.1),
		2: vec(dims, 0.2),
	}

	// Build gzipped JSONL in reverse order to exercise realignment.
	var raw bytes.Buffer
	for i := n - 1; i >= 0; i-- {
		line, _ := json.Marshal(vectorRow{Index: i, Embedding: vectors[i]})
		raw.Write(line)
		raw.WriteByte('\n')
	}
	var gzbuf bytes.Buffer
	gzw := gzip.NewWriter(&gzbuf)
	if _, err := gzw.Write(raw.Bytes()); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	got := make([][]float32, n)
	err := streamParseVectors(bytes.NewReader(gzbuf.Bytes()), n, dims, func(index int, v []float32) error {
		got[index] = v
		return nil
	})
	if err != nil {
		t.Fatalf("streamParseVectors: %v", err)
	}
	for i := range got {
		if len(got[i]) != dims {
			t.Errorf("vector %d has %d dims, want %d", i, len(got[i]), dims)
		}
		if got[i][0] != vectors[i][0] {
			t.Errorf("vector %d[0] = %v, want %v", i, got[i][0], vectors[i][0])
		}
	}
}

func TestStreamParseVectorsErrors(t *testing.T) {
	gzipData := func(content string) []byte {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, _ = gz.Write([]byte(content))
		_ = gz.Close()
		return buf.Bytes()
	}

	tests := []struct {
		name    string
		data    []byte
		n       int
		dims    int
		wantSub string
	}{
		{"missing index", gzipData(`{"index":0,"embedding":[1,2]}`), 2, 2, "missing vector for index 1"},
		{
			"duplicate index",
			gzipData("{\"index\":0,\"embedding\":[1,2]}\n{\"index\":0,\"embedding\":[3,4]}"),
			2, 2, "duplicate vector",
		},
		{"out of range", gzipData(`{"index":5,"embedding":[1,2]}`), 2, 2, "out of range"},
		{"wrong dims", gzipData(`{"index":0,"embedding":[1,2,3]}`), 1, 2, "dims"},
		{"bad json", gzipData(`{not json}`), 1, 2, "parse vectors line"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := streamParseVectors(bytes.NewReader(tc.data), tc.n, tc.dims, func(int, []float32) error {
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestValidateOpts(t *testing.T) {
	good := Options{
		Bucket:  "test-bucket",
		RoleARN: "arn:aws:iam::123456789012:role/test",
		Region:  "us-east-1",
		Dims:    1024,
	}

	if err := validateOpts(good); err != nil {
		t.Fatalf("valid opts returned error: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*Options)
		wantSub string
	}{
		{"missing bucket", func(o *Options) { o.Bucket = "" }, "Bucket"},
		{"missing role", func(o *Options) { o.RoleARN = "" }, "RoleARN"},
		{"missing region", func(o *Options) { o.Region = "" }, "Region"},
		{"zero dims", func(o *Options) { o.Dims = 0 }, "Dims"},
		{"negative dims", func(o *Options) { o.Dims = -1 }, "Dims"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := good
			tc.mutate(&o)
			err := validateOpts(o)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestS3PathConstruction(t *testing.T) {
	// Verify the S3 key pattern used in EmbedStream.
	runID := "banhmi-embed-1234567890"
	inputKey := "input/" + runID + "/" + inputFileName
	scriptKey := "input/" + runID + "/" + scriptFileName
	outputPrefix := "output/" + runID

	wantInput := "input/banhmi-embed-1234567890/input.jsonl.gz"
	wantScript := "input/banhmi-embed-1234567890/embed.py"
	wantOutputPrefix := "output/banhmi-embed-1234567890"

	if inputKey != wantInput {
		t.Errorf("inputKey = %q, want %q", inputKey, wantInput)
	}
	if scriptKey != wantScript {
		t.Errorf("scriptKey = %q, want %q", scriptKey, wantScript)
	}
	if outputPrefix != wantOutputPrefix {
		t.Errorf("outputPrefix = %q, want %q", outputPrefix, wantOutputPrefix)
	}
}

func TestEmbedScriptEmbedded(t *testing.T) {
	if embedScript == "" {
		t.Fatal("embedScript is empty — embed_script.py not embedded")
	}
	if !strings.Contains(embedScript, "BAAI/bge-m3") {
		t.Error("embedScript does not reference the BGE-M3 model")
	}
	if !strings.Contains(embedScript, "vectors.jsonl.gz") {
		t.Error("embedScript does not reference the output file name")
	}
}
