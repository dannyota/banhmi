// Package api owns per-artifact quality and residency admission contracts.
package api

import (
	"context"
	"fmt"

	"danny.vn/banhmi/internal/corpus/model"
)

// GateRequest supplies bounded decoded values and a trusted policy profile.
type GateRequest struct {
	Document model.DecodedDocument
	Parts    model.DecodedPartResult
	Profile  model.QualityMetricProfile
}

// GateResult binds a staged local report to its declared outcome.
type GateResult struct {
	Report  model.QualityReport
	Outcome model.QualityOutcome
}

// Validate checks outcome agreement and validates the report against trusted policy.
func (r GateResult) Validate(profile model.QualityMetricProfile) error {
	if r.Outcome != r.Report.Outcome {
		return fmt.Errorf("gate outcome differs from report")
	}
	if err := r.Report.Validate(profile); err != nil {
		return fmt.Errorf("quality report: %w", err)
	}
	return nil
}

// Gate evaluates every applicable per-artifact mandatory check and returns its
// report and outcome; unknown geometry/fidelity or failed integrity cannot pass,
// and neither a decision nor OCR confidence bypasses mandatory checks.
type Gate interface {
	// Evaluate computes metrics and outcome from verified local values and policy.
	Evaluate(GateRequest) (QualityEvaluation, error)
}

// ResidencyRequest is the local capture, screening-policy and budget manifest.
type ResidencyRequest struct {
	Artifact model.CaptureRef
	Policy   model.AssetRef
	Budget   model.ResourceBudget
}

// ResidencyScreen reads only within the in-country sandbox before workflow or
// external admission and returns classification references, never screened text;
// unclassified and personal-data-bearing artifacts fail admission.
type ResidencyScreen interface {
	// Screen classifies bounded bytes inside the local sandbox before export.
	Screen(context.Context, ResidencyRequestEnvelope) (model.ResidencyEnvelope, error)
}

// QualityEvaluation is the computed local result staged by the gate's consumer.
type QualityEvaluation struct {
	Metrics  []model.QualityMetric
	Findings []model.Finding
	Outcome  model.QualityOutcome
}

// ResidencyRequestEnvelope projects the local capture, policy and budget manifest.
type ResidencyRequestEnvelope struct {
	Key      string
	Manifest model.Digest
}

// Validate checks the residency request's ledger key and manifest digest.
func (e ResidencyRequestEnvelope) Validate() error {
	if err := model.ValidateEnvelope(e.Key, "screen", e.Manifest); err != nil {
		return fmt.Errorf("screen envelope: %w", err)
	}
	return nil
}

// MarshalJSON validates before serializing the projection.
func (e ResidencyRequestEnvelope) MarshalJSON() ([]byte, error) {
	encoded, err := model.MarshalEnvelope(e.Key, "screen", e.Manifest)
	if err != nil {
		return nil, fmt.Errorf("serialize screen envelope: %w", err)
	}
	return encoded, nil
}

// Validate checks decoded gate inputs before invoking a pure quality implementation.
// The consumer verifies Profile against the pinned policy bytes before this call.
func (r GateRequest) Validate() error {
	if err := r.Document.Validate(); err != nil {
		return fmt.Errorf("decoded document: %w", err)
	}
	if err := r.Parts.Validate(); err != nil {
		return fmt.Errorf("decoded parts: %w", err)
	}
	if err := r.Profile.Validate(); err != nil {
		return fmt.Errorf("trusted metric profile: %w", err)
	}
	return nil
}
