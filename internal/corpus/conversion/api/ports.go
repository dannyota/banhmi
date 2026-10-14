// Package api owns sandbox conversion and pure block-processing contracts.
package api

import (
	"context"

	"danny.vn/banhmi/internal/corpus/model"
)

type ConvertRequest struct {
	Artifact model.CaptureRef
	Recipe   model.AssetRef
	Budget   model.ResourceBudget
}

// Converter opens captured artifacts only inside the sandbox and stages a
// versioned block document plus report; it exposes no outside-sandbox byte API.
type Converter interface {
	Convert(context.Context, ConvertRequest) (model.ConvertedDocument, model.QualityReport, error)
}

// Normalizer transforms verified blocks without I/O and returns hash-bound
// text, offset maps and reversible edits, never bare replacement text.
type Normalizer interface {
	Normalize(model.ConvertedDocument) (model.MappedText, []model.Finding, error)
}

// StructureBuilder assigns exactly one owner per retained block using all six
// part outputs and captured source structure, without reparsing raw bytes or I/O.
type StructureBuilder interface {
	BuildStructure(model.ConvertedDocument, model.PartResult, model.AssetRef) (model.BodyTree, []model.Finding, error)
}

// OutputValidator verifies staged hashes, bounds, schema, transform chains and
// provenance before acceptance; untrusted content reads stay inside the sandbox.
type OutputValidator interface {
	ValidateOutput(context.Context, model.ConvertedDocument) (model.QualityReport, error)
}

// BodyRules applies deterministic country grammar to validated blocks and all
// part outputs without I/O; unresolved ownership remains a blocking finding.
type BodyRules interface {
	Structure(model.ConvertedDocument, model.PartResult, model.AssetRef) (model.BodyTree, []model.Finding)
}

// LanguageRules deterministically normalizes verified text with maps/edits and
// inspects blocks without I/O; NFC is an output contract, not a validator rewrite.
type LanguageRules interface {
	Normalize(model.ConvertedDocument) (model.MappedText, []model.Finding)
	Inspect(model.ConvertedDocument) ([]model.QualityMetric, []model.Finding)
}
