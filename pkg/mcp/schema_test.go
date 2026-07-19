package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"danny.vn/banhmi/pkg/rag/retrieve"
	"strings"
)

// TestListTools_SingleTypedParams guards the wire shape of every tool's input
// schema: each property must carry one concrete JSON type, never a
// ["null", X] union. The Go SDK infers unions for pointer and slice fields,
// and strict MCP clients (ChatGPT's plugin scanner) read those as untyped.
func TestListTools_SingleTypedParams(t *testing.T) {
	cs := connect(t, &fakeSearcher{hits: []retrieve.Hit{sampleHit()}})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	for _, tool := range res.Tools {
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal %s input schema: %v", tool.Name, err)
		}
		var schema struct {
			Properties map[string]struct {
				Type any `json:"type"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("unmarshal %s input schema: %v", tool.Name, err)
		}
		for name, prop := range schema.Properties {
			if _, ok := prop.Type.(string); !ok {
				t.Errorf("%s.%s: type = %v, want a single string type", tool.Name, name, prop.Type)
			}
		}
	}
}

// issuerCorpus wraps fakeCorpus with issuer metadata for the filter-hint tests.
type issuerCorpus struct {
	fakeCorpus
	issuers []string
}

func (c issuerCorpus) Issuers(context.Context) ([]string, error) { return c.issuers, nil }

// TestIssuerHint: the search schema's issuer description carries the corpus's
// real issuer vocabulary — or an omit-this-filter warning when the corpus has
// no issuer metadata — so agents stop guessing strings that zero out results.
func TestIssuerHint(t *testing.T) {
	issuerDesc := func(c CorpusReader) string {
		schema := annotateIssuerHint(inputSchemaFor[searchInput](), c, slog.New(slog.DiscardHandler))
		b, err := json.Marshal(schema)
		if err != nil {
			t.Fatalf("marshal schema: %v", err)
		}
		var raw struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatalf("unmarshal schema: %v", err)
		}
		return raw.Properties["issuer"].Description
	}

	withValues := issuerDesc(issuerCorpus{issuers: []string{"Bank Negara Malaysia", "Securities Commission Malaysia"}})
	if !strings.Contains(withValues, "Issuer values in this corpus include: Bank Negara Malaysia | Securities Commission Malaysia") {
		t.Errorf("issuer hint missing vocabulary: %q", withValues)
	}

	empty := issuerDesc(issuerCorpus{})
	if !strings.Contains(empty, "no issuer metadata") {
		t.Errorf("empty-corpus hint missing warning: %q", empty)
	}

	// A corpus without the capability keeps the base description untouched.
	plain := issuerDesc(fakeCorpus{})
	if strings.Contains(plain, "Issuer values") || strings.Contains(plain, "no issuer metadata") {
		t.Errorf("non-lister corpus must not get a hint: %q", plain)
	}
}
