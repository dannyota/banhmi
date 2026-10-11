package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"danny.vn/banhmi/pkg/base/config"
)

func TestQueryEndpointOverrideUsesDedicatedRoute(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": config.EmbedModel,
			"data":  []map[string]any{{"index": 0, "embedding": make([]float32, config.EmbedDims)}},
		})
	}))
	defer server.Close()
	t.Setenv("BANHMI_EMBED_QUERY", "")
	t.Setenv("BANHMI_EMBED_QUERY_ENDPOINT", server.URL+"/query")
	embedder, err := NewQueryEmbedder(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := embedder.Embed(t.Context(), []string{"query"}); err != nil {
		t.Fatal(err)
	}
	if path != "/query/embeddings" {
		t.Fatalf("request path = %q, want /query/embeddings", path)
	}
}

func TestQueryONNXAdmissionConfigUsesFiniteDefaults(t *testing.T) {
	t.Setenv("BANHMI_EMBED_CONCURRENCY", "")
	t.Setenv("BANHMI_EMBED_MAX_QUEUED", "")
	concurrency, maxQueued, err := queryONNXAdmissionConfig()
	if err != nil {
		t.Fatal(err)
	}
	if concurrency < 2 || maxQueued != concurrency {
		t.Fatalf("concurrency=%d maxQueued=%d, want finite matching defaults", concurrency, maxQueued)
	}
}

func TestQueryONNXAdmissionConfigOverrides(t *testing.T) {
	t.Setenv("BANHMI_EMBED_CONCURRENCY", "3")
	t.Setenv("BANHMI_EMBED_MAX_QUEUED", "5")
	concurrency, maxQueued, err := queryONNXAdmissionConfig()
	if err != nil {
		t.Fatal(err)
	}
	if concurrency != 3 || maxQueued != 5 {
		t.Fatalf("concurrency=%d maxQueued=%d, want 3 and 5", concurrency, maxQueued)
	}
}

func TestQueryONNXAdmissionConfigRejectsInvalidExplicitValues(t *testing.T) {
	for _, tc := range []struct {
		key   string
		value string
	}{
		{key: "BANHMI_EMBED_CONCURRENCY", value: "0"},
		{key: "BANHMI_EMBED_CONCURRENCY", value: "many"},
		{key: "BANHMI_EMBED_MAX_QUEUED", value: "-1"},
		{key: "BANHMI_EMBED_MAX_QUEUED", value: "many"},
	} {
		t.Run(tc.key+"_"+tc.value, func(t *testing.T) {
			t.Setenv("BANHMI_EMBED_CONCURRENCY", "")
			t.Setenv("BANHMI_EMBED_MAX_QUEUED", "")
			t.Setenv(tc.key, tc.value)
			if _, _, err := queryONNXAdmissionConfig(); err == nil {
				t.Fatalf("queryONNXAdmissionConfig accepted %s=%q", tc.key, tc.value)
			}
		})
	}
}
