// Package residency implements private, bounded residency byte contracts.
package residency

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"

	"danny.vn/banhmi/internal/corpus/model"
)

// ErrorKind is a payload-free private processing failure class.
type ErrorKind string

// Error returns only the fixed failure class, without an underlying cause.
func (e ErrorKind) Error() string { return string(e) }

const (
	inputError     ErrorKind = "INPUT"
	policyError    ErrorKind = "POLICY"
	bindingError   ErrorKind = "BINDING"
	limitError     ErrorKind = "LIMIT"
	cancelledError ErrorKind = "CANCELLED"
	deadlineError  ErrorKind = "DEADLINE"
)

// ResidencyLimits bounds private formats, retained closures and inspection work.
type ResidencyLimits struct {
	MaxPolicyBytes  int64
	MaxRequestBytes int64
	MaxReportBytes  int64
	MaxSubjectBytes int64
	MaxDecodedBytes int64
	MaxObjects      int64
	MaxItems        int64
	MaxEvidence     int64
	MaxRules        int64
	MaxPatternBytes int64
	MaxMatches      int64
	MaxElapsedMS    int64
}

var ceiling = ResidencyLimits{16384, 65536, 4096, 8388608, 67108864, 128, 4096, 16, 5, 512, 1024, 5000}

var limitNames = [...]string{
	"max_policy_bytes", "max_request_bytes", "max_report_bytes", "max_subject_bytes",
	"max_decoded_bytes", "max_objects", "max_items", "max_evidence", "max_rules",
	"max_pattern_bytes", "max_matches", "max_elapsed_ms",
}

// Validate rejects nonpositive or enlarged synthetic declarations.
func (l ResidencyLimits) Validate() error {
	v, maximum := reflect.ValueOf(l), reflect.ValueOf(ceiling)
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).Int() <= 0 || v.Field(i).Int() > maximum.Field(i).Int() {
			return limitError
		}
	}
	return nil
}

func stricter(a, b ResidencyLimits) ResidencyLimits {
	v, other := reflect.ValueOf(&a).Elem(), reflect.ValueOf(b)
	for i := 0; i < v.NumField(); i++ {
		v.Field(i).SetInt(min(v.Field(i).Int(), other.Field(i).Int()))
	}
	return a
}

// DetectorRule identifies one frozen RE2 blocking signal.
type DetectorRule struct {
	ID      string
	Pattern string
}

// ResidencyPolicy pins the closed admission mode and complete detector set.
type ResidencyPolicy struct {
	SchemaVersion string
	Country       string
	RuleVersion   string
	Admission     string
	Limits        ResidencyLimits
	Rules         []DetectorRule
}

func frozenRules() []DetectorRule {
	return []DetectorRule{
		{"email", "[A-Za-z0-9.!#$%&'*+/=?^_\\x60{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+"},
		{"id12", `[0-9]{12}`},
		{"id9", `[0-9]{9}`},
		{"phone", `(?:\+84[ .-]?[0-9](?:[ .-]?[0-9]){8}|0[0-9](?:[ .-]?[0-9]){8})`},
		{"tax", `(?i:ma[ \t]+so[ \t]+thue|mã[ \t]+số[ \t]+thuế|mst)[ \t]*[:=]?[ \t]*[0-9]{10}(?:-[0-9]{3})?`},
	}
}

// PolicyCodec constructs the residency-policy-v1 rule B codec.
func PolicyCodec(limits ResidencyLimits) Codec[ResidencyPolicy] {
	return Codec[ResidencyPolicy]{limits: limits, maxBytes: limits.MaxPolicyBytes,
		validate: func(p ResidencyPolicy) error { return validatePolicy(p, limits) },
		marshal: marshalPolicy, unmarshal: unmarshalPolicy}
}

func validatePolicy(p ResidencyPolicy, trusted ResidencyLimits) error {
	if p.Limits.Validate() != nil {
		return limitError
	}
	l := stricter(p.Limits, trusted)
	if int64(len(p.Rules)) > l.MaxRules || int64(len(p.Rules)) > l.MaxItems {
		return limitError
	}
	for _, rule := range p.Rules {
		if int64(len(rule.Pattern)) > l.MaxPatternBytes {
			return limitError
		}
	}
	if p.SchemaVersion != "residency-policy-v1" || p.Country != "vn" || p.RuleVersion != "residency-signals-v1" || p.Admission != "closed" || !reflect.DeepEqual(p.Rules, frozenRules()) {
		return policyError
	}
	data, err := marshalPolicy(p)
	if err != nil || int64(len(data)) > l.MaxPolicyBytes {
		return limitError
	}
	return nil
}

func marshalPolicy(p ResidencyPolicy) ([]byte, error) {
	l := make(map[string]int64, len(limitNames))
	v := reflect.ValueOf(p.Limits)
	for i, name := range limitNames {
		l[name] = v.Field(i).Int()
	}
	rules := make([]map[string]string, len(p.Rules))
	for i, r := range p.Rules {
		rules[i] = map[string]string{"id": r.ID, "pattern": r.Pattern}
	}
	return json.Marshal(map[string]any{"schema_version": p.SchemaVersion, "country": p.Country,
		"rule_version": p.RuleVersion, "admission": p.Admission, "limits": l, "rules": rules})
}

func unmarshalPolicy(data []byte, p *ResidencyPolicy) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return inputError
	}
	for name, target := range map[string]*string{"schema_version": &p.SchemaVersion,
		"country": &p.Country, "rule_version": &p.RuleVersion, "admission": &p.Admission} {
		if json.Unmarshal(fields[name], target) != nil {
			return inputError
		}
	}
	var limits map[string]json.RawMessage
	if json.Unmarshal(fields["limits"], &limits) != nil {
		return inputError
	}
	v := reflect.ValueOf(&p.Limits).Elem()
	for i, name := range limitNames {
		var n int64
		if err := json.Unmarshal(limits[name], &n); err != nil {
			return decodeError(err)
		}
		v.Field(i).SetInt(n)
	}
	var rules []map[string]string
	if json.Unmarshal(fields["rules"], &rules) != nil {
		return inputError
	}
	p.Rules = make([]DetectorRule, len(rules))
	for i, rule := range rules {
		p.Rules[i] = DetectorRule{rule["id"], rule["pattern"]}
	}
	return nil
}

func decodeError(err error) ErrorKind {
	if kind, ok := err.(ErrorKind); ok {
		return kind
	}
	if mismatch, ok := err.(*json.UnmarshalTypeError); ok && (mismatch.Type.Kind() == reflect.Int || mismatch.Type.Kind() == reflect.Int64) && strings.HasPrefix(mismatch.Value, "number ") {
		number := strings.TrimPrefix(mismatch.Value, "number ")
		if !strings.ContainsAny(number, ".eE") {
			if _, err := strconv.ParseInt(number, 10, 64); err != nil {
				return limitError
			}
		}
	}
	return inputError
}

func hashRef(prefix string, data []byte) model.AssetRef {
	h := hash(data)
	return model.AssetRef{Path: prefix + "/" + string(h), SHA256: h, ByteSize: int64(len(data))}
}
