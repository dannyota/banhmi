package formats

import (
	"encoding/json"
	"fmt"
	"strings"

	"danny.vn/banhmi/internal/corpus/model"
)

// CandidateInputCodec constructs the candidate-input-v1 rule A codec.
func CandidateInputCodec(limits Limits) Codec[CandidateInput] {
	return Codec[CandidateInput]{limits: limits, validate: validateInput}
}

func validateInput(v CandidateInput) error {
	if v.SchemaVersion != "candidate-input-v1" || v.Country == "" || v.Language == "" || v.ObservationKey == "" || v.ThresholdVersion == "" || len(v.Artifacts) == 0 {
		return fmt.Errorf("candidate input requires version, identity and selected artifacts")
	}
	if err := model.ValidateKey(v.DocumentVersionKey); err != nil {
		return err
	}
	if !strings.HasPrefix(v.ProcessingExecutionKey, "execution-") || len(v.ProcessingExecutionKey) != 42 {
		return fmt.Errorf("invalid processing execution key")
	}
	for _, ch := range v.ProcessingExecutionKey[10:] {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return fmt.Errorf("invalid processing execution key")
		}
	}
	seen := make(map[string]bool)
	for i, artifact := range v.Artifacts {
		if artifact.ArtifactKey == "" || artifact.Role == "" || artifact.ObservationOrdinal < 0 || seen[artifact.ArtifactKey] {
			return fmt.Errorf("invalid or duplicate selected artifact")
		}
		seen[artifact.ArtifactKey] = true
		if err := artifact.Capture.Validate(); err != nil {
			return err
		}
		if artifact.Capture.Country != v.Country || (i > 0 && !artifactLess(v.Artifacts[i-1], artifact)) {
			return fmt.Errorf("artifact country or selection order mismatch")
		}
		if artifact.Format == "html" && v.HTMLSelector != "body" && (!strings.HasPrefix(v.HTMLSelector, "#") || len(v.HTMLSelector) < 2) {
			return fmt.Errorf("HTML requires body or exact id selector")
		}
	}
	switch v.Mode {
	case "first_pass":
		if v.Predecessor != nil {
			return fmt.Errorf("first pass has no predecessor")
		}
	case "reprocess":
		if v.Predecessor == nil || v.Predecessor.AcceptanceWatermark < 0 {
			return fmt.Errorf("reprocessing requires a predecessor and nonnegative watermark")
		}
		if err := model.ValidateKey(v.Predecessor.DocumentVersionKey); err != nil {
			return err
		}
		if len(v.Predecessor.RevisionKey) != 112 {
			return fmt.Errorf("predecessor requires a full revision key")
		}
		if err := model.ValidateRevisionKey(v.Predecessor.RevisionKey, v.Country); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown candidate mode")
	}
	return nil
}

func artifactLess(a, b CandidateArtifact) bool {
	if a.Role != b.Role {
		return a.Role < b.Role
	}
	if a.ObservationOrdinal != b.ObservationOrdinal {
		return a.ObservationOrdinal < b.ObservationOrdinal
	}
	return a.ArtifactKey < b.ArtifactKey
}

// CandidateOutputCodec constructs the candidate-output-v1 rule A codec. It
// also recomputes TextHash from the ordered normalized-text hash array.
func CandidateOutputCodec(limits Limits) Codec[CandidateOutput] {
	return Codec[CandidateOutput]{limits: limits, validate: func(v CandidateOutput) error {
		if v.SchemaVersion != "candidate-output-v1" || len(v.Artifacts) == 0 {
			return fmt.Errorf("invalid candidate output version or empty conversions")
		}
		for i, artifact := range v.Artifacts {
			if artifact.Ordinal != i {
				return fmt.Errorf("output ordinals must cover input in ascending order")
			}
		}
		if TextHash(v.Artifacts) != v.TextHash {
			return fmt.Errorf("candidate text hash mismatch")
		}
		return nil
	}}
}

// TextHash hashes the rule A array of normalized-text digests in artifact order.
func TextHash(artifacts []OutputArtifact) model.Digest {
	hashes := make([]model.Digest, len(artifacts))
	for i, artifact := range artifacts {
		hashes[i] = artifact.NormalizedText.SHA256
	}
	data, _ := json.Marshal(hashes) // A slice of strings cannot fail to marshal.
	return Hash(data)
}

// ValidateCandidateBinding compares decoded input/output bindings without
// rewriting logical paths. Exact document and sandbox membership is checked by
// the consumer against the retained M1.12 objects.
func ValidateCandidateBinding(input CandidateInput, output CandidateOutput, limits Limits) error {
	_, candidate, err := CandidateInputCodec(limits).Encode(input)
	if err != nil {
		return err
	}
	if _, _, err := CandidateOutputCodec(limits).Encode(output); err != nil {
		return err
	}
	if output.CandidateDigest != candidate || output.NormalizerManifestDigest != input.NormalizerManifest.SHA256 || output.GatePolicyDigest != input.GatePolicyDigest || len(output.Artifacts) != len(input.Artifacts) {
		return fmt.Errorf("candidate output binding mismatch")
	}
	for i, artifact := range output.Artifacts {
		if artifact.OriginalHash != input.Artifacts[i].Capture.Asset.SHA256 {
			return fmt.Errorf("candidate original hash mismatch")
		}
	}
	return nil
}
