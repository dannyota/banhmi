// Package api owns per-artifact quality and residency admission contracts.
package api

import (
	"context"
	"fmt"

	"danny.vn/banhmi/internal/corpus/model"
)

type GateRequest struct {
	Document model.ConvertedDocument
	Parts    model.PartResult
	Policy   model.AssetRef
}

type GateResult struct {
	Report  model.QualityReport
	Outcome model.QualityOutcome
}

func (r GateResult) Validate() error {
	if r.Outcome != r.Report.Outcome {
		return fmt.Errorf("gate outcome differs from report")
	}
	return r.Report.Validate()
}

// Gate evaluates every applicable per-artifact mandatory check and returns its
// report and outcome; unknown geometry/fidelity or failed integrity cannot pass,
// and neither a decision nor OCR confidence bypasses mandatory checks.
type Gate interface {
	Evaluate(GateRequest) (GateResult, error)
}

type ResidencyRequest struct {
	Artifact model.CaptureRef
	Policy   model.AssetRef
	Budget   model.ResourceBudget
}

// ResidencyScreen reads only within the in-country sandbox before workflow or
// external admission and returns classification references, never screened text;
// unclassified and personal-data-bearing artifacts fail admission.
type ResidencyScreen interface {
	Screen(context.Context, ResidencyRequest) (model.ResidencyAdmission, error)
}
