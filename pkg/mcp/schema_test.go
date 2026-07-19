package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"danny.vn/banhmi/pkg/rag/retrieve"
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
