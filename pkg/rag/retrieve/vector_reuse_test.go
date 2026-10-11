package retrieve

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/base/jurisdiction"
	"danny.vn/banhmi/pkg/rag/embed"
)

type recordingEmbedder struct {
	mu     sync.Mutex
	inputs []string
	err    error
}

func (e *recordingEmbedder) Model() string { return "test/model" }
func (e *recordingEmbedder) Dims() int     { return 1 }
func (e *recordingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.inputs = append(e.inputs, texts...)
	if e.err != nil {
		return nil, e.err
	}
	out := make([][]float32, len(texts))
	for i, text := range texts {
		out[i] = []float32{float32(len(text))}
	}
	return out, nil
}

func (e *recordingEmbedder) recorded() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.inputs...)
}

func TestEmbedQueryFormatsTransformedDenseQueryOnce(t *testing.T) {
	emb := &recordingEmbedder{}
	r := New(nil, emb, config.Default().Retrieve, nil,
		WithJurisdiction(jurisdiction.For("vn")),
		WithDiacriticDict(map[string]string{"ngan": "ngân", "hang": "hàng"}),
	).(*hybridRetriever)
	denseQuery, sparseQuery := r.prepareQueries("ngan hang TPPU")
	if denseQuery != "ngân hàng tppu" {
		t.Fatalf("dense query = %q", denseQuery)
	}
	if sparseQuery != "ngan hang TPPU" {
		t.Fatalf("sparse query = %q; lexical input must remain independent", sparseQuery)
	}

	vec, err := r.embedQuery(context.Background(), denseQuery)
	if err != nil {
		t.Fatalf("embedQuery: %v", err)
	}
	wantInput := embed.FormatQuery(denseQuery)
	if got := emb.recorded(); len(got) != 1 || got[0] != wantInput {
		t.Fatalf("embed inputs = %q, want [%q]", got, wantInput)
	}
	if got := vec.Slice(); len(got) != 1 || got[0] != float32(len(wantInput)) {
		t.Fatalf("vector = %v, want transformed-query marker", got)
	}

	idEmb := &recordingEmbedder{}
	idRetriever := New(nil, idEmb, config.Default().Retrieve, nil,
		WithJurisdiction(jurisdiction.For("id")),
		WithAbbreviationDict(map[string]string{"tppu": "tindak pidana pencucian uang"}),
	).(*hybridRetriever)
	if len(idRetriever.diacriticDict) != 0 {
		t.Fatalf("Indonesian diacritic dictionary = %v, want empty", idRetriever.diacriticDict)
	}
	if expansion, ok := r.abbreviationDict["tppu"]; ok {
		t.Fatalf("Vietnam abbreviation dictionary contains Indonesian expansion %q", expansion)
	}
	idDenseQuery, idSparseQuery := idRetriever.prepareQueries("aturan TPPU")
	wantExpanded := "aturan TPPU (tindak pidana pencucian uang)"
	if idDenseQuery != wantExpanded {
		t.Fatalf("Indonesian dense query = %q, want %q", idDenseQuery, wantExpanded)
	}
	if idSparseQuery != wantExpanded {
		t.Fatalf("Indonesian sparse query = %q, want %q", idSparseQuery, wantExpanded)
	}
	idVec, err := idRetriever.embedQuery(context.Background(), idDenseQuery)
	if err != nil {
		t.Fatalf("Indonesian embedQuery: %v", err)
	}
	idWantInput := embed.FormatQuery(idDenseQuery)
	if got := idEmb.recorded(); len(got) != 1 || got[0] != idWantInput {
		t.Fatalf("Indonesian embed inputs = %q, want [%q]", got, idWantInput)
	}
	if got := idVec.Slice(); len(got) != 1 || got[0] != float32(len(idWantInput)) {
		t.Fatalf("Indonesian vector = %v, want expanded-query marker", got)
	}

	denseQuery, sparseQuery = r.prepareQueries("ngan hang TPPU")
	if denseQuery != "ngân hàng tppu" || sparseQuery != "ngan hang TPPU" {
		t.Fatalf("Vietnam queries = (%q, %q), want only Vietnam diacritic restoration", denseQuery, sparseQuery)
	}
	idDenseQuery, idSparseQuery = idRetriever.prepareQueries("ngan hang TPPU")
	wantExpanded = "ngan hang TPPU (tindak pidana pencucian uang)"
	if idDenseQuery != wantExpanded || idSparseQuery != wantExpanded {
		t.Fatalf("Indonesian queries = (%q, %q), want (%q, %q)", idDenseQuery, idSparseQuery, wantExpanded, wantExpanded)
	}
}

func TestEmbedQueryConcurrentRequestsKeepVectorsIsolated(t *testing.T) {
	emb := &recordingEmbedder{}
	r := &hybridRetriever{embedder: emb}
	queries := []string{"a", "a much longer query"}
	vectors := make([]float32, len(queries))
	errs := make([]error, len(queries))

	var wg sync.WaitGroup
	for i, query := range queries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			vec, err := r.embedQuery(context.Background(), query)
			errs[i] = err
			if err == nil {
				vectors[i] = vec.Slice()[0]
			}
		}()
	}
	wg.Wait()

	for i, query := range queries {
		if errs[i] != nil {
			t.Fatalf("embedQuery(%q): %v", query, errs[i])
		}
		want := float32(len(embed.FormatQuery(query)))
		if vectors[i] != want {
			t.Fatalf("vector[%d] = %v, want %v", i, vectors[i], want)
		}
	}
}

func TestEmbedQueryPropagatesRequiredVectorError(t *testing.T) {
	want := errors.New("inference unavailable")
	r := &hybridRetriever{embedder: &recordingEmbedder{err: want}}
	_, err := r.embedQuery(context.Background(), "query")
	if !errors.Is(err, want) {
		t.Fatalf("embedQuery error = %v, want %v", err, want)
	}

	r.cfg = config.Default().Retrieve
	_, err = r.SearchEvidence(context.Background(), "banking query", SearchOpts{Mode: ModeVector})
	if !errors.Is(err, want) {
		t.Fatalf("SearchEvidence error = %v, want required primary embed error %v", err, want)
	}
}

func TestValidateSearchRejectsLimitsBeforeEmbedding(t *testing.T) {
	emb := &recordingEmbedder{}
	cfg := config.Default().Retrieve
	r := &hybridRetriever{embedder: emb, cfg: cfg}

	tests := []struct {
		name string
		q    string
		opts SearchOpts
	}{
		{name: "query bytes", q: fmt.Sprintf("%0*d", cfg.MaxQueryBytes+1, 0)},
		{name: "results", q: "banking", opts: SearchOpts{TopK: cfg.MaxResults + 1}},
		{name: "related", q: "banking", opts: SearchOpts{RelatedK: cfg.MaxRelated + 1}},
		{name: "combined filters", q: "banking", opts: SearchOpts{Issuer: make([]string, cfg.MaxFilterValues), DocType: []string{"law"}}},
		{name: "filter bytes", q: "banking", opts: SearchOpts{Issuer: []string{fmt.Sprintf("%0*d", cfg.MaxFilterBytes+1, 0)}}},
		{name: "as_of bytes", q: "banking", opts: SearchOpts{AsOf: fmt.Sprintf("%0*d", cfg.MaxFilterBytes+1, 0)}},
		{name: "issued_from bytes", q: "banking", opts: SearchOpts{IssuedFrom: fmt.Sprintf("%0*d", cfg.MaxFilterBytes+1, 0)}},
		{name: "issued_to bytes", q: "banking", opts: SearchOpts{IssuedTo: fmt.Sprintf("%0*d", cfg.MaxFilterBytes+1, 0)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.SearchEvidence(context.Background(), tc.q, tc.opts); err == nil {
				t.Fatal("SearchEvidence succeeded, want limit error")
			}
		})
	}
	if got := emb.recorded(); len(got) != 0 {
		t.Fatalf("embed inputs = %q, want none", got)
	}
}

func TestValidateSearchAcceptsExactLimitsAndZeroDefaults(t *testing.T) {
	cfg := config.Default().Retrieve
	r := &hybridRetriever{cfg: cfg}
	opts := SearchOpts{
		TopK:       cfg.MaxResults,
		RelatedK:   cfg.MaxRelated,
		Issuer:     make([]string, cfg.MaxFilterValues-1),
		DocType:    []string{fmt.Sprintf("%0*d", cfg.MaxFilterBytes, 0)},
		AsOf:       fmt.Sprintf("%0*d", cfg.MaxFilterBytes, 0),
		IssuedFrom: fmt.Sprintf("%0*d", cfg.MaxFilterBytes, 0),
		IssuedTo:   fmt.Sprintf("%0*d", cfg.MaxFilterBytes, 0),
	}
	if err := r.validateSearch(fmt.Sprintf("%0*d", cfg.MaxQueryBytes, 0), opts); err != nil {
		t.Fatalf("validateSearch exact limits: %v", err)
	}
	if err := r.validateSearch("banking", SearchOpts{}); err != nil {
		t.Fatalf("validateSearch zero defaults: %v", err)
	}
}

func TestValidateSearchChecksEffectiveTopKBeforeAccess(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.RetrieveConfig
		opts    SearchOpts
		wantErr bool
	}{
		{
			name:    "configured default exceeds cap",
			cfg:     config.RetrieveConfig{ReadLimits: config.ReadLimits{MaxResults: 3}, TopK: 8},
			wantErr: true,
		},
		{
			name:    "compiled default exceeds cap",
			cfg:     config.RetrieveConfig{ReadLimits: config.ReadLimits{MaxResults: 3}},
			wantErr: true,
		},
		{
			name: "explicit value within cap overrides configured default",
			cfg:  config.RetrieveConfig{ReadLimits: config.ReadLimits{MaxResults: 3}, TopK: 8},
			opts: SearchOpts{TopK: 3},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			emb := &recordingEmbedder{}
			r := &hybridRetriever{embedder: emb, cfg: tc.cfg}
			var err error
			if tc.wantErr {
				_, err = r.SearchEvidence(context.Background(), "banking", tc.opts)
			} else {
				err = r.validateSearch("banking", tc.opts)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateSearch error = %v, wantErr %v", err, tc.wantErr)
			}
			if got := emb.recorded(); len(got) != 0 {
				t.Fatalf("embed inputs = %q, want none", got)
			}
		})
	}
}

func TestValidateSearchRejectsNegativeCounts(t *testing.T) {
	r := &hybridRetriever{cfg: config.Default().Retrieve}
	for name, opts := range map[string]SearchOpts{
		"top_k":     {TopK: -1},
		"vector_k":  {VectorK: -1},
		"bm25_k":    {BM25K: -1},
		"rrf_k":     {RRFK: -1},
		"doc_cap":   {DocCap: -1},
		"related_k": {RelatedK: -1},
	} {
		t.Run(name, func(t *testing.T) {
			if err := r.validateSearch("banking", opts); err == nil {
				t.Fatal("validateSearch succeeded, want negative-count error")
			}
		})
	}
}

func TestResolveRejectsCandidateArithmeticOverflow(t *testing.T) {
	cfg := config.Default().Retrieve
	cfg.HNSWCandidateMultiplier = 2
	r := &hybridRetriever{cfg: cfg}
	_, err := r.resolve(SearchOpts{VectorK: math.MaxInt})
	if err == nil {
		t.Fatal("resolve succeeded, want HNSW candidate overflow error")
	}
}

func TestBM25ModeDoesNotRequireQueryVector(t *testing.T) {
	r := &hybridRetriever{embedder: &recordingEmbedder{}}
	res, err := r.resolve(SearchOpts{Mode: ModeBM25})
	if err != nil {
		t.Fatalf("resolve BM25: %v", err)
	}
	if res.mode != ModeBM25 {
		t.Fatalf("mode = %q, want %q", res.mode, ModeBM25)
	}
}
