package residency

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestResidencyPolicyCanonical(t *testing.T) {
	p := testPolicy()
	codec := PolicyCodec(ceiling)
	data, digest, err := codec.Encode(p)
	if err != nil || digest != hash(data) || bytes.HasSuffix(data, []byte("\n")) {
		t.Fatal("policy encode")
	}
	decoded, decodedDigest, err := codec.Decode(data)
	if err != nil || decodedDigest != digest || !reflect.DeepEqual(decoded, p) {
		t.Fatal("policy round-trip")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != 6 || !bytes.HasPrefix(data, []byte(`{"admission":"closed","country":"vn","limits":{`)) {
		t.Fatal("policy field-order")
	}
	var limits map[string]json.RawMessage
	if json.Unmarshal(fields["limits"], &limits) != nil || len(limits) != len(limitNames) {
		t.Fatal("policy limits-shape")
	}
	for _, name := range limitNames {
		if _, ok := limits[name]; !ok {
			t.Fatal("policy limits-name")
		}
	}
}

func TestResidencyPolicyRejectsMutations(t *testing.T) {
	for n, mutate := range []func(*ResidencyPolicy){
		func(p *ResidencyPolicy) { p.Admission = "public" },
		func(p *ResidencyPolicy) { p.Country = "my" },
		func(p *ResidencyPolicy) { p.RuleVersion = "residency-signals-v2" },
		func(p *ResidencyPolicy) { p.SchemaVersion = "other" },
		func(p *ResidencyPolicy) { p.Rules[0].Pattern = ".*" },
		func(p *ResidencyPolicy) { p.Rules[0], p.Rules[1] = p.Rules[1], p.Rules[0] },
		func(p *ResidencyPolicy) { p.Rules = p.Rules[:4] },
		func(p *ResidencyPolicy) { p.Rules = append(p.Rules, DetectorRule{"z", "x"}) },
		func(p *ResidencyPolicy) { p.Rules = nil },
	} {
		p := testPolicy()
		mutate(&p)
		data, digest, err := PolicyCodec(ceiling).Encode(p)
		if err == nil || data != nil || digest != "" {
			t.Fatalf("policy-%d rejection", n)
		}
	}
	p := testPolicy()
	data, _, err := PolicyCodec(ceiling).Encode(p)
	if err != nil {
		t.Fatal("policy-negative setup")
	}
	for n, invalid := range [][]byte{
		append(append([]byte(nil), data...), '\n'),
		bytes.Replace(data, []byte(`"closed"`), []byte(`null`), 1),
		bytes.Replace(data, []byte(`"country":"vn"`), []byte(`"country":"vn","country":"vn"`), 1),
		bytes.Replace(data, []byte(`"id":"email"`), []byte(`"id":"email","extra":"x"`), 1),
		bytes.Replace(data, []byte(`"max_rules":5`), []byte(`"max_rules":5.0`), 1),
		bytes.Replace(data, []byte(`"max_rules":5`), []byte(`"max_rules":5e0`), 1),
		bytes.Replace(data, []byte(`"max_rules":5`), []byte(`"max_rules":-0`), 1),
		bytes.Replace(data, []byte(`"max_rules":5`), []byte(`"max_rules":5,"max_rules":5`), 1),
		bytes.Replace(data, []byte(`"max_rules":5,`), nil, 1),
	} {
		value, digest, err := PolicyCodec(ceiling).Decode(invalid)
		if err == nil || !reflect.DeepEqual(value, ResidencyPolicy{}) || digest != "" {
			t.Fatalf("policy-bytes-%d rejection", n)
		}
	}
}

func TestResidencyEveryLimitDeclaration(t *testing.T) {
	for n := 0; n < reflect.TypeOf(ceiling).NumField(); n++ {
		for _, value := range []int64{0, -1, math.MaxInt64} {
			limits := ceiling
			reflect.ValueOf(&limits).Elem().Field(n).SetInt(value)
			if limits.Validate() != limitError {
				t.Fatalf("declaration-%d invalid", n)
			}
		}
		at := ceiling
		if at.Validate() != nil {
			t.Fatalf("declaration-%d limit", n)
		}
		over := at
		field := reflect.ValueOf(&over).Elem().Field(n)
		field.SetInt(field.Int() + 1)
		if over.Validate() != limitError {
			t.Fatalf("declaration-%d limit-plus-one", n)
		}
		policy := testPolicy()
		policy.Limits = over
		if _, _, err := PolicyCodec(ceiling).Encode(policy); err != limitError {
			t.Fatalf("declaration-%d policy", n)
		}
	}
}

func TestResidencyRuleAndPolicyOperationalBounds(t *testing.T) {
	p := testPolicy()
	data, _, err := PolicyCodec(ceiling).Encode(p)
	if err != nil {
		t.Fatal("policy-bound setup")
	}
	longest := int64(0)
	for _, rule := range p.Rules {
		longest = max(longest, int64(len(rule.Pattern)))
	}
	for n, configure := range []func(*ResidencyLimits){
		func(l *ResidencyLimits) { l.MaxPolicyBytes = int64(len(data)) },
		func(l *ResidencyLimits) { l.MaxPatternBytes = longest },
		func(l *ResidencyLimits) { l.MaxRules = 5 },
		func(l *ResidencyLimits) { l.MaxItems = 5 },
	} {
		at := ceiling
		configure(&at)
		if _, _, err := PolicyCodec(at).Encode(p); err != nil {
			t.Fatalf("policy-bound-%d limit", n)
		}
		over := at
		switch n {
		case 0:
			over.MaxPolicyBytes--
		case 1:
			over.MaxPatternBytes--
		case 2:
			over.MaxRules--
		case 3:
			over.MaxItems--
		}
		if _, _, err := PolicyCodec(over).Encode(p); err != limitError {
			t.Fatalf("policy-bound-%d limit-plus-one", n)
		}
	}
}

func TestResidencyCopiesPolicy(t *testing.T) {
	p := testPolicy()
	e, err := NewEngine(p, ceiling)
	if err != nil {
		t.Fatal("copy setup")
	}
	p.Rules[0].ID = strings.Repeat("z", 3)
	p.Limits.MaxMatches = 1
	if e.policy.Rules[0].ID != "email" || e.limits.MaxMatches != ceiling.MaxMatches || e.proof != nil {
		t.Fatal("copy isolation")
	}
}
