package kagglebatch

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	kaggle "danny.vn/kaggle"
)

// fakeKaggle is a stand-in Kaggle API + file server driven by httptest. It
// answers the RPCs EmbedAll calls (upload, dataset status, kernel push/status,
// output listing) and serves the signed vectors file.
type fakeKaggle struct {
	t    *testing.T
	dims int

	// vectors maps input index -> embedding, returned in vectors.jsonl. The
	// server intentionally emits lines out of input order to prove EmbedAll
	// realigns by index.
	vectors map[int][]float32

	// kernelStatuses is the sequence of kernel statuses returned by successive
	// GetKernelSessionStatus calls; the last value repeats once exhausted.
	kernelStatuses []string
	statusCall     int

	// datasetExists controls whether CreateDatasetVersion (newDataset=false)
	// succeeds; when false it returns a 404 so EmbedAll falls back to create.
	datasetExists bool

	versionCalled       bool
	createCalled        bool
	deleteCalled        bool
	datasetDeleteCalled bool

	srv  *httptest.Server // API server
	file *httptest.Server // signed file-download server
}

func newFakeKaggle(t *testing.T, dims int, vectors map[int][]float32, kernelStatuses []string, datasetExists bool) *fakeKaggle {
	t.Helper()
	f := &fakeKaggle{
		t:              t,
		dims:           dims,
		vectors:        vectors,
		kernelStatuses: kernelStatuses,
		datasetExists:  datasetExists,
	}
	f.file = httptest.NewServer(http.HandlerFunc(f.serveFile))
	f.srv = httptest.NewServer(http.HandlerFunc(f.serveAPI))
	t.Cleanup(func() {
		f.srv.Close()
		f.file.Close()
	})
	return f
}

// serveFile serves the vectors.jsonl bytes and the kernel log for signed URLs.
func (f *fakeKaggle) serveFile(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/vectors.jsonl.gz":
		// Emit gzipped lines in reverse index order to exercise realignment +
		// the gzip decode path.
		var raw bytes.Buffer
		for i := len(f.vectors) - 1; i >= 0; i-- {
			line, _ := json.Marshal(vectorRow{Index: i, Embedding: f.vectors[i]})
			raw.Write(line)
			raw.WriteByte('\n')
		}
		gz := gzip.NewWriter(w)
		_, _ = gz.Write(raw.Bytes())
		_ = gz.Close()
	case "/run.log":
		_, _ = w.Write([]byte("Traceback: kernel blew up\n"))
	case "/upload":
		// Signed blob upload target; accept the PUT body.
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

// serveAPI dispatches the Kaggle JSON RPCs by path suffix.
func (f *fakeKaggle) serveAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/UploadDatasetFile"):
		// Return a signed create URL + token; the client PUTs bytes there.
		_, _ = fmt.Fprintf(w, `{"token":"tok-1","createUrl":"%s/upload"}`, f.file.URL)

	case strings.HasSuffix(path, "/CreateDatasetVersion"):
		f.versionCalled = true
		if !f.datasetExists {
			// Mimic a not-found so EmbedAll falls back to CreateDataset.
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":404,"message":"dataset not found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ref":"owner/banhmi-embed-input"}`))

	case strings.HasSuffix(path, "/CreateDataset"):
		f.createCalled = true
		_, _ = w.Write([]byte(`{"ref":"owner/banhmi-embed-input"}`))

	case strings.HasSuffix(path, "/GetDatasetStatus"):
		_, _ = w.Write([]byte(`{"status":"READY"}`))

	case strings.HasSuffix(path, "/SaveKernel"):
		_, _ = w.Write([]byte(`{"ref":"owner/banhmi-embed-run","versionNumber":1}`))

	case strings.HasSuffix(path, "/GetKernelSessionStatus"):
		status := f.kernelStatuses[len(f.kernelStatuses)-1]
		if f.statusCall < len(f.kernelStatuses) {
			status = f.kernelStatuses[f.statusCall]
		}
		f.statusCall++
		_, _ = fmt.Fprintf(w, `{"status":%q}`, status)

	case strings.HasSuffix(path, "/ListKernelSessionOutput"):
		_, _ = fmt.Fprintf(w, `{"files":[{"url":"%s/vectors.jsonl.gz","fileName":"vectors.jsonl.gz"},{"url":"%s/run.log","fileName":"run.log"}],"log":"captured log line","nextPageToken":""}`, f.file.URL, f.file.URL)

	case strings.HasSuffix(path, "/DeleteKernel"):
		f.deleteCalled = true
		_, _ = w.Write([]byte(`{}`))

	case strings.HasSuffix(path, "/DeleteDataset"):
		f.datasetDeleteCalled = true
		_, _ = w.Write([]byte(`{}`))

	default:
		f.t.Errorf("unexpected API path %q", path)
		http.NotFound(w, r)
	}
}

// embedderFor builds a BatchEmbedder whose transport points at the fake server,
// with near-zero poll intervals so the test does not actually wait.
func (f *fakeKaggle) embedderFor(t *testing.T) *BatchEmbedder {
	t.Helper()
	client, err := kaggle.New(
		kaggle.WithToken("x"),
		kaggle.WithEndpoint(f.srv.URL),
		kaggle.WithHTTPClient(f.srv.Client()),
	)
	if err != nil {
		t.Fatalf("new kaggle client: %v", err)
	}
	b, err := newWithClient(Options{Owner: "owner", Accelerator: "NvidiaTeslaT4", Dims: f.dims}, nil, client)
	if err != nil {
		t.Fatalf("newWithClient: %v", err)
	}
	b.datasetPollInterval = time.Millisecond
	b.kernelPollInterval = time.Millisecond
	return b
}

func vec(dims int, v float32) []float32 {
	out := make([]float32, dims)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestEmbedAllReturnsOrderedVectors(t *testing.T) {
	dims := 4
	vectors := map[int][]float32{
		0: vec(dims, 0.0),
		1: vec(dims, 0.1),
		2: vec(dims, 0.2),
	}
	f := newFakeKaggle(t, dims, vectors, []string{"RUNNING", "COMPLETE"}, true)
	b := f.embedderFor(t)

	got, err := b.EmbedAll(context.Background(), []string{"alpha", "beta", "gamma"})
	if err != nil {
		t.Fatalf("EmbedAll: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d vectors, want 3", len(got))
	}
	for i := range got {
		if len(got[i]) != dims {
			t.Errorf("vector %d has %d dims, want %d", i, len(got[i]), dims)
		}
		if got[i][0] != vectors[i][0] {
			t.Errorf("vector %d = %v, want index-aligned %v", i, got[i], vectors[i])
		}
	}
	if !f.createCalled {
		t.Error("expected a CreateDataset (each run uses a fresh, unique slug)")
	}
	if f.versionCalled {
		t.Error("did not expect CreateDatasetVersion (unique slug ⇒ always create)")
	}
	if !f.deleteCalled {
		t.Error("expected the embed kernel to be auto-deleted on success")
	}
	if !f.datasetDeleteCalled {
		t.Error("expected the input dataset to be auto-deleted on success")
	}
}

func TestEmbedAllSurfacesKernelError(t *testing.T) {
	dims := 2
	vectors := map[int][]float32{0: vec(dims, 1)}
	f := newFakeKaggle(t, dims, vectors, []string{"RUNNING", "ERROR"}, true)
	b := f.embedderFor(t)

	_, err := b.EmbedAll(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("expected an error when the kernel fails")
	}
	if !strings.Contains(err.Error(), "kernel failed") {
		t.Errorf("error = %q, want it to mention kernel failure", err)
	}
	// The error should fold in a tail of the downloaded log.
	if !strings.Contains(err.Error(), "kernel blew up") {
		t.Errorf("error = %q, want it to include the kernel log tail", err)
	}
}

func TestEmbedAllDimMismatch(t *testing.T) {
	// Server returns 4-d vectors but the embedder expects 8.
	vectors := map[int][]float32{0: vec(4, 1)}
	f := newFakeKaggle(t, 4, vectors, []string{"COMPLETE"}, true)
	client, err := kaggle.New(kaggle.WithToken("x"), kaggle.WithEndpoint(f.srv.URL), kaggle.WithHTTPClient(f.srv.Client()))
	if err != nil {
		t.Fatalf("new kaggle client: %v", err)
	}
	b, err := newWithClient(Options{Owner: "owner", Dims: 8}, nil, client)
	if err != nil {
		t.Fatalf("newWithClient: %v", err)
	}
	b.datasetPollInterval = time.Millisecond
	b.kernelPollInterval = time.Millisecond

	if _, err := b.EmbedAll(context.Background(), []string{"x"}); err == nil || !strings.Contains(err.Error(), "dims") {
		t.Fatalf("expected a dims-mismatch error, got %v", err)
	}
}

func TestEmbedAllEmpty(t *testing.T) {
	f := newFakeKaggle(t, 4, map[int][]float32{}, []string{"COMPLETE"}, true)
	b := f.embedderFor(t)
	got, err := b.EmbedAll(context.Background(), nil)
	if err != nil {
		t.Fatalf("EmbedAll(nil): %v", err)
	}
	if got != nil {
		t.Errorf("EmbedAll(nil) = %v, want nil", got)
	}
}

func TestNewValidatesOptions(t *testing.T) {
	client, err := kaggle.New(kaggle.WithToken("x"), kaggle.WithEndpoint("http://example.invalid"))
	if err != nil {
		t.Fatalf("new kaggle client: %v", err)
	}
	if _, err := newWithClient(Options{Dims: 1024}, nil, client); err != nil {
		t.Errorf("Owner is optional (auto-derived from the token); got error: %v", err)
	}
	if _, err := newWithClient(Options{Owner: "owner"}, nil, client); err == nil {
		t.Error("expected error when Dims is not positive")
	}
}

func TestWriteAndParseRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.jsonl")
	texts := []string{"first text", "second\twith tab", "third 漢字"}
	if err := writeInputJSONL(path, texts); err != nil {
		t.Fatalf("writeInputJSONL: %v", err)
	}

	data, err := os.ReadFile(path)
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

	// Build a matching vectors file and parse it back.
	vpath := filepath.Join(dir, outputFileName)
	var b strings.Builder
	for i := range texts {
		l, _ := json.Marshal(vectorRow{Index: i, Embedding: vec(3, float32(i))})
		b.Write(l)
		b.WriteByte('\n')
	}
	var gzbuf bytes.Buffer
	gzw := gzip.NewWriter(&gzbuf)
	_, _ = gzw.Write([]byte(b.String()))
	_ = gzw.Close()
	if err := os.WriteFile(vpath, gzbuf.Bytes(), 0o644); err != nil {
		t.Fatalf("write vectors: %v", err)
	}
	got, err := parseVectorsJSONL(vpath, len(texts), 3)
	if err != nil {
		t.Fatalf("parseVectorsJSONL: %v", err)
	}
	if len(got) != len(texts) || got[2][0] != 2 {
		t.Errorf("parsed = %v", got)
	}
}

func TestParseVectorsJSONLErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(content string) string {
		p := filepath.Join(dir, "v.jsonl")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		return p
	}

	tests := []struct {
		name    string
		content string
		n       int
		dims    int
		wantSub string
	}{
		{"missing index", `{"index":0,"embedding":[1,2]}`, 2, 2, "missing vector for index 1"},
		{"duplicate index", "{\"index\":0,\"embedding\":[1,2]}\n{\"index\":0,\"embedding\":[3,4]}", 2, 2, "duplicate vector"},
		{"out of range", `{"index":5,"embedding":[1,2]}`, 2, 2, "out of range"},
		{"wrong dims", `{"index":0,"embedding":[1,2,3]}`, 1, 2, "dims"},
		{"bad json", `{not json}`, 1, 2, "parse vectors line"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseVectorsJSONL(write(tc.content), tc.n, tc.dims)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantSub)
			}
		})
	}
}
