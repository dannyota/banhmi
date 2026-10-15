package residency

import (
	"encoding/json"
	"strings"

	"danny.vn/banhmi/internal/corpus/model"
)

// ByteIdentity binds bytes without retaining a source path in the report.
type ByteIdentity struct {
	SHA256   model.Digest
	ByteSize int64
}

// DetectorCount retains only a frozen detector ID and its aggregate count.
type DetectorCount struct {
	ID      string
	Matches int64
}

// ResidencyReport is a private result, never an orchestration payload.
type ResidencyReport struct {
	SchemaVersion  string
	Request        model.AssetRef
	Subject        ByteIdentity
	Policy         model.AssetRef
	Country        string
	SourceID       string
	OwnerKey       string
	Operation      string
	RuleVersion    string
	EvidenceHash   model.Digest
	Complete       bool
	Class          model.ResidencyClass
	ReasonCode     string
	Counts         []DetectorCount
	AdmissionProof *model.Digest
}

// ReportCodec constructs a closed production residency-report-v1 codec.
// A public report always fails, including one carrying a forged synthetic proof.
func ReportCodec(policy ResidencyPolicy, limits ResidencyLimits) (Codec[ResidencyReport], error) {
	return reportCodec(policy, limits, false)
}

func reportCodec(policy ResidencyPolicy, limits ResidencyLimits, positive bool) (Codec[ResidencyReport], error) {
	policyBytes, _, err := PolicyCodec(limits).Encode(policy)
	if err != nil {
		return Codec[ResidencyReport]{}, err
	}
	policy.Rules = append([]DetectorRule(nil), policy.Rules...)
	limits = stricter(limits, policy.Limits)
	// Fail construction when even a minimum closed report cannot fit.
	z := model.Digest(strings.Repeat("0", 64))
	minimal := ResidencyReport{SchemaVersion: "residency-report-v1",
		Request: model.AssetRef{Path: "requests/" + string(z), SHA256: z},
		Subject: ByteIdentity{SHA256: z}, Policy: hashRef("policies", policyBytes),
		Country: "vn", SourceID: "a", OwnerKey: "execution-" + strings.Repeat("0", 32),
		Operation: "capture_export", RuleVersion: policy.RuleVersion, EvidenceHash: z,
		Complete: true, Class: model.ResidencyUnclassified, ReasonCode: "NO_POSITIVE_PROOF",
		Counts: make([]DetectorCount, len(policy.Rules))}
	for i, rule := range policy.Rules {
		minimal.Counts[i].ID = rule.ID
	}
	data, _ := json.Marshal(minimal)
	if int64(len(data)) > limits.MaxReportBytes || int64(len(policy.Rules)) > limits.MaxItems {
		return Codec[ResidencyReport]{}, limitError
	}
	if err := dependencyBounds(minimal, data, limits); err != nil {
		return Codec[ResidencyReport]{}, err
	}
	return Codec[ResidencyReport]{limits: limits, maxBytes: limits.MaxReportBytes,
		validate: func(r ResidencyReport) error { return validateReport(r, policy, limits, positive) }}, nil
}

func validateReport(r ResidencyReport, p ResidencyPolicy, l ResidencyLimits, positive bool) error {
	if r.SchemaVersion != "residency-report-v1" || r.Request.Path != "requests/"+string(r.Request.SHA256) || r.Policy.Path != "policies/"+string(r.Policy.SHA256) || r.Country != "vn" || !sourceID.MatchString(r.SourceID) || model.ValidateEnvelopeKey(r.OwnerKey, "execution") != nil || (r.Operation != "capture_export" && r.Operation != "artifact_export") || r.RuleVersion != p.RuleVersion || r.Subject.ByteSize < 0 || len(r.Counts) != len(p.Rules) {
		return inputError
	}
	if r.Subject.ByteSize > l.MaxSubjectBytes || r.Request.ByteSize > l.MaxRequestBytes || r.Policy.ByteSize > l.MaxPolicyBytes {
		return limitError
	}
	policyBytes, err := marshalPolicy(p)
	if err != nil || r.Policy != hashRef("policies", policyBytes) {
		return bindingError
	}
	var total int64
	for i, count := range r.Counts {
		if count.ID != p.Rules[i].ID || count.Matches < 0 {
			return inputError
		}
		if count.Matches > l.MaxMatches-total {
			return limitError
		}
		total += count.Matches
	}
	if r.AdmissionProof != nil {
		if !positive || *r.AdmissionProof != proofDigest(r.Request.SHA256) || r.Class != model.ResidencyPublic || !r.Complete || total != 0 || r.ReasonCode != "SYNTHETIC_PROOF" {
			return bindingError
		}
		return nil
	}
	if total > 0 {
		if r.Class == model.ResidencyPersonal && r.ReasonCode == "SIGNAL_PRESENT" {
			return nil
		}
		return inputError
	}
	if r.Class != model.ResidencyUnclassified {
		return bindingError
	}
	if r.Complete && r.ReasonCode == "NO_POSITIVE_PROOF" {
		return nil
	}
	if !r.Complete && (r.ReasonCode == "INCOMPLETE_SURFACE" || r.ReasonCode == "UNSUPPORTED_ENCODING") {
		return nil
	}
	return inputError
}

func proofDigest(request model.Digest) model.Digest {
	data, _ := json.Marshal(struct {
		Version string
		Request model.Digest
	}{"synthetic-residency-proof-v1", request})
	return hash(data)
}

// ValidateReportBinding re-encodes all values and checks every request binding.
// Shape validation and this closed binding check cannot grant public admission.
func ValidateReportBinding(request ScreenRequest, report ResidencyReport, policy ResidencyPolicy) error {
	return validateReportBinding(request, report, policy, false)
}

func validateReportBinding(request ScreenRequest, report ResidencyReport, policy ResidencyPolicy, positive bool) error {
	l := policy.Limits
	policyBytes, _, err := PolicyCodec(l).Encode(policy)
	if err != nil {
		return err
	}
	requestBytes, _, err := RequestCodec(l).Encode(request)
	if err != nil {
		return err
	}
	codec, err := reportCodec(policy, l, positive)
	if err != nil {
		return err
	}
	if _, _, err := codec.Encode(report); err != nil {
		return err
	}
	evidence, _ := json.Marshal(request.Evidence)
	if report.Request != hashRef("requests", requestBytes) || report.Subject != (ByteIdentity{request.Subject.SHA256, request.Subject.ByteSize}) || report.Policy != request.Policy || request.Policy != hashRef("policies", policyBytes) || report.Country != request.Country || report.SourceID != request.SourceID || report.OwnerKey != request.OwnerKey || report.Operation != request.Operation || report.RuleVersion != policy.RuleVersion || report.EvidenceHash != hash(evidence) {
		return bindingError
	}
	return nil
}
