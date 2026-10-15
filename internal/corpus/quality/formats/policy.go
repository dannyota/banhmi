package formats

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"danny.vn/banhmi/internal/corpus/model"
)

// DefaultLimits returns the proposed, unmeasured M1 bounds. Owner gate O7 must
// approve production values before deployment or real-document acceptance.
func DefaultLimits() Limits {
	return Limits{
		MaxBlobs:       1024,
		MaxRows:        10000,
		MaxJSONBytes:   4194304,
		MaxObjectBytes: 67108864,
		MaxElapsedMS:   60000,
	}
}

// Validate rejects unbounded limits and blob counts above the frozen ceiling.
func (l Limits) Validate() error {
	if l.MaxBlobs <= 0 || l.MaxBlobs > 1024 || l.MaxRows <= 0 || l.MaxJSONBytes <= 0 || l.MaxObjectBytes <= 0 || l.MaxElapsedMS <= 0 {
		return fmt.Errorf("acceptance limits must be positive; max_blobs must not exceed 1024")
	}
	return nil
}

// PolicyBundleCodec constructs the policy-bundle-v1 rule B codec. Limits are
// trusted decoding bounds, separate from the bounds carried by the policy.
func PolicyBundleCodec(limits Limits) Codec[PolicyBundle] {
	return Codec[PolicyBundle]{limits: limits, validate: validatePolicy, marshal: marshalPolicy, unmarshal: unmarshalPolicy}
}

func validatePolicy(v PolicyBundle) error {
	if v.SchemaVersion != "policy-bundle-v1" || v.Country == "" || v.ThresholdVersion == "" {
		return fmt.Errorf("invalid policy version or identity")
	}
	if err := v.AcceptanceLimits.Validate(); err != nil {
		return err
	}
	for _, profile := range [][]model.MetricRequirement{v.ArtifactProfile, v.RollupProfile} {
		if len(profile) == 0 {
			return fmt.Errorf("empty metric profile")
		}
		geometry, fidelity := false, false
		for i, requirement := range profile {
			if !requirement.Kind.Valid() || requirement.Threshold == "" || (i > 0 && profile[i-1].Kind >= requirement.Kind) {
				return fmt.Errorf("profile requires known, sorted unique checks and thresholds")
			}
			if requirement.Kind == model.MetricGeometry {
				geometry = requirement.Mandatory
			}
			if requirement.Kind == model.MetricFidelity {
				fidelity = requirement.Mandatory
			}
		}
		if !geometry || !fidelity {
			return fmt.Errorf("geometry and fidelity must be mandatory")
		}
	}
	return orderedStrings(v.GapReasonCodes)
}

func profileObject(profile []model.MetricRequirement) []map[string]any {
	values := make([]map[string]any, len(profile))
	for i, r := range profile {
		values[i] = map[string]any{
			"kind":                 r.Kind,
			"mandatory":            r.Mandatory,
			"allow_not_applicable": r.AllowNotApplicable,
			"threshold":            r.Threshold,
		}
	}
	return values
}

func marshalPolicy(v PolicyBundle) ([]byte, error) {
	// encoding/json sorts string map keys bytewise and escapes keys and strings
	// with Go's standard escaping. The tree contains only integers and no nulls.
	return json.Marshal(map[string]any{
		"schema_version":    v.SchemaVersion,
		"country":           v.Country,
		"threshold_version": v.ThresholdVersion,
		"artifact_profile":  profileObject(v.ArtifactProfile),
		"rollup_profile":    profileObject(v.RollupProfile),
		"acceptance_limits": map[string]int64{
			"max_blobs":        v.AcceptanceLimits.MaxBlobs,
			"max_rows":         v.AcceptanceLimits.MaxRows,
			"max_json_bytes":   v.AcceptanceLimits.MaxJSONBytes,
			"max_object_bytes": v.AcceptanceLimits.MaxObjectBytes,
			"max_elapsed_ms":   v.AcceptanceLimits.MaxElapsedMS,
		},
		"gap_reason_codes": v.GapReasonCodes,
	})
}

func object(data []byte, keys ...string) (map[string]json.RawMessage, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil || values == nil || len(values) != len(keys) {
		return nil, fmt.Errorf("policy object has missing or unknown keys")
	}
	for _, key := range keys {
		value, ok := values[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fmt.Errorf("missing or null policy key %s", key)
		}
	}
	return values, nil
}

func unmarshalPolicy(data []byte, v *PolicyBundle) error {
	values, err := object(data, "schema_version", "country", "threshold_version", "artifact_profile", "rollup_profile", "acceptance_limits", "gap_reason_codes")
	if err != nil {
		return err
	}
	for key, dst := range map[string]any{
		"schema_version":    &v.SchemaVersion,
		"country":           &v.Country,
		"threshold_version": &v.ThresholdVersion,
		"gap_reason_codes":  &v.GapReasonCodes,
	} {
		if err := json.Unmarshal(values[key], dst); err != nil {
			return fmt.Errorf("policy %s: %w", key, err)
		}
	}
	v.ArtifactProfile, err = readProfile(values["artifact_profile"])
	if err != nil {
		return err
	}
	v.RollupProfile, err = readProfile(values["rollup_profile"])
	if err != nil {
		return err
	}
	l, err := object(values["acceptance_limits"], "max_blobs", "max_rows", "max_json_bytes", "max_object_bytes", "max_elapsed_ms")
	if err != nil {
		return err
	}
	for key, dst := range map[string]*int64{
		"max_blobs":        &v.AcceptanceLimits.MaxBlobs,
		"max_rows":         &v.AcceptanceLimits.MaxRows,
		"max_json_bytes":   &v.AcceptanceLimits.MaxJSONBytes,
		"max_object_bytes": &v.AcceptanceLimits.MaxObjectBytes,
		"max_elapsed_ms":   &v.AcceptanceLimits.MaxElapsedMS,
	} {
		number := string(bytes.TrimSpace(l[key]))
		n, err := strconv.ParseInt(number, 10, 64)
		if err != nil || strconv.FormatInt(n, 10) != number {
			return fmt.Errorf("policy %s requires a canonical integer", key)
		}
		*dst = n
	}
	return nil
}

func readProfile(data []byte) ([]model.MetricRequirement, error) {
	var entries []json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil || entries == nil {
		return nil, fmt.Errorf("profile requires non-null array")
	}
	profile := make([]model.MetricRequirement, len(entries))
	for i, entry := range entries {
		values, err := object(entry, "kind", "mandatory", "allow_not_applicable", "threshold")
		if err != nil {
			return nil, err
		}
		for key, dst := range map[string]any{
			"kind":                 &profile[i].Kind,
			"mandatory":            &profile[i].Mandatory,
			"allow_not_applicable": &profile[i].AllowNotApplicable,
			"threshold":            &profile[i].Threshold,
		} {
			if err := json.Unmarshal(values[key], dst); err != nil {
				return nil, fmt.Errorf("profile %s: %w", key, err)
			}
		}
	}
	return profile, nil
}

// PolicyFromJSONB reads a JSONB representation without float conversion and
// returns its rule B bytes and digest. Unlike Codec.Decode, it permits JSONB key
// order and whitespace changes. Duplicate keys and noninteger numbers reject.
func PolicyFromJSONB(data []byte, limits Limits) (PolicyBundle, []byte, model.Digest, error) {
	var v PolicyBundle
	if err := limits.Validate(); err != nil {
		return v, nil, "", err
	}
	if int64(len(data)) > limits.MaxJSONBytes {
		return v, nil, "", fmt.Errorf("policy exceeds JSON byte bound")
	}
	if err := uniqueJSON(data, limits.MaxRows); err != nil {
		return v, nil, "", err
	}
	if err := unmarshalPolicy(data, &v); err != nil {
		return v, nil, "", err
	}
	canonical, digest, err := PolicyBundleCodec(limits).Encode(v)
	return v, canonical, digest, err
}

func trustedProfile(policy PolicyBundle, limits Limits, rollup bool) (model.QualityMetricProfile, error) {
	_, digest, err := PolicyBundleCodec(limits).Encode(policy)
	if err != nil {
		return model.QualityMetricProfile{}, err
	}
	metrics := policy.ArtifactProfile
	if rollup {
		metrics = policy.RollupProfile
	}
	return model.QualityMetricProfile{PolicyHash: digest, Metrics: append([]model.MetricRequirement(nil), metrics...)}, nil
}

// PolicyProfiles validates a policy and returns independent model profiles
// bound to its rule B digest. Report-supplied mandatory flags cannot select them.
func PolicyProfiles(policy PolicyBundle, limits Limits) (model.QualityMetricProfile, model.QualityMetricProfile, error) {
	artifact, err := trustedProfile(policy, limits, false)
	if err != nil {
		return model.QualityMetricProfile{}, model.QualityMetricProfile{}, err
	}
	rollup, err := trustedProfile(policy, limits, true)
	return artifact, rollup, err
}
