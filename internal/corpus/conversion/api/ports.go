// Package api owns sandbox conversion and pure block-processing contracts.
package api

import (
	"context"
	"fmt"

	"danny.vn/banhmi/internal/corpus/model"
)

// ConvertRequest is the local artifact, recipe and frozen-budget manifest.
type ConvertRequest struct {
	Artifact model.CaptureRef
	Recipe   model.AssetRef
	Budget   model.ResourceBudget
}

// Converter opens captured artifacts only inside the sandbox and stages a
// versioned block document plus report; it exposes no outside-sandbox byte API.
type Converter interface {
	// Convert resolves the local manifest inside the sandbox and stages its outputs.
	Convert(context.Context, ConvertEnvelope) (model.ConversionEnvelope, model.QualityEnvelope, error)
}

// Normalizer transforms bounded decoded text without I/O and returns computed
// text, offset maps and reversible edits to the consumer responsible for staging.
type Normalizer interface {
	// Normalize returns computed text, reversible edits and provenance for staging.
	Normalize(model.TextInput) (model.MappedValue, []model.Finding, error)
}

// StructureBuilder assigns exactly one owner per retained block using all six
// part outputs and captured source structure, without reparsing raw bytes or I/O.
type StructureBuilder interface {
	// BuildStructure returns computed ownership from decoded blocks, parts and source nodes.
	BuildStructure(model.DecodedDocument, model.DecodedPartResult, model.SourceTree) (model.BodyValue, []model.Finding, error)
}

// OutputValidator verifies staged hashes, bounds, schema, transform chains and
// provenance before acceptance; untrusted content reads stay inside the sandbox.
type OutputValidator interface {
	// ValidateOutput verifies staged conversion bytes and provenance inside isolation.
	ValidateOutput(context.Context, model.ConversionEnvelope) (model.QualityEnvelope, error)
}

// BodyRules applies deterministic country grammar to validated blocks and all
// part outputs without I/O; unresolved ownership remains a blocking finding.
type BodyRules interface {
	// Structure applies country grammar to decoded blocks, all six part outputs and tree nodes.
	Structure(model.DecodedDocument, model.DecodedPartResult, model.SourceTree) (model.BodyValue, []model.Finding)
}

// LanguageRules returns computed decoded text with maps/edits and
// inspects blocks without I/O; NFC is an output contract, not a validator rewrite.
type LanguageRules interface {
	// Normalize returns computed text, reversible edits and provenance for staging.
	Normalize(model.TextInput) (model.MappedValue, []model.Finding)
	// Inspect measures decoded block values without resolving references.
	Inspect(model.DecodedDocument) ([]model.QualityMetric, []model.Finding)
}

// ConvertEnvelope projects the local artifact, recipe and frozen-budget manifest.
// The sandbox resolves and verifies this manifest before opening artifact bytes.
type ConvertEnvelope struct {
	Key      string
	Manifest model.Digest
}

// Validate checks the conversion request ledger key and manifest digest.
func (e ConvertEnvelope) Validate() error {
	if err := model.ValidateEnvelope(e.Key, "convert", e.Manifest); err != nil {
		return fmt.Errorf("convert envelope: %w", err)
	}
	return nil
}

// MarshalJSON validates before serializing the projection.
func (e ConvertEnvelope) MarshalJSON() ([]byte, error) {
	encoded, err := model.MarshalEnvelope(e.Key, "convert", e.Manifest)
	if err != nil {
		return nil, fmt.Errorf("serialize convert envelope: %w", err)
	}
	return encoded, nil
}
