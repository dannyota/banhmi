package mcp

import (
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"danny.vn/banhmi/internal/evidence/api"
)

func TestStartupMetadataLanguages(t *testing.T) {
	cases := []struct {
		country string
		indexed string
		binding string
	}{
		{country: "vn", indexed: "Vietnamese", binding: "Vietnamese"},
		{country: "my", indexed: "English", binding: "English"},
		{country: "id", indexed: "Indonesian", binding: "Indonesian"},
		{country: "sg", indexed: "English", binding: "English"},
		{country: "th", indexed: "Thai", binding: "Thai"},
		{country: "kh", indexed: "English", binding: "Khmer"},
	}
	for _, tc := range cases {
		t.Run(tc.country, func(t *testing.T) {
			got := StartupMetadataFor(tc.country)
			if got.Country != tc.country || got.IndexedLanguage != tc.indexed || got.BindingLanguage != tc.binding {
				t.Fatalf("metadata = %+v, want country=%q indexed=%q binding=%q", got, tc.country, tc.indexed, tc.binding)
			}
		})
	}
}

func TestStartupMetadataPreservesKnownEmptyIssuerWarning(t *testing.T) {
	metadata := api.StartupMetadata{IssuerVocabulary: make([]string, 0)}
	schema := annotateStartupHints(inputSchemaFor[searchInput](), metadata).(*jsonschema.Schema)
	description := schema.Properties["issuer"].Description
	if !strings.Contains(description, "omit this filter") {
		t.Fatalf("issuer description = %q, want known-empty warning", description)
	}
}
