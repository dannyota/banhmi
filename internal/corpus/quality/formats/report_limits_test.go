package formats

import (
	"encoding/json"
	"testing"
)

func TestReportCallerAndPolicyLimits(t *testing.T) {
	defaults := DefaultLimits()
	policy := fixturePolicy(t)
	reportCodec, err := GateReportCodec(policy, defaults)
	if err != nil {
		t.Fatal(err)
	}
	rollupCodec, err := GateRollupCodec(policy, defaults)
	if err != nil {
		t.Fatal(err)
	}
	report := decodedFixture(t, "gate-report-complete", reportCodec)
	// Seven rows exceed the six rows in the policy itself, so a caller row
	// limit can reject the report after the constructor accepts the policy.
	for i := 0; i < 3; i++ {
		report.Artifact.Findings = append(report.Artifact.Findings, report.Artifact.Findings[0])
	}
	rollup := decodedFixture(t, "gate-rollup-complete", rollupCodec)
	for _, kind := range []string{"artifact", "rollup"} {
		for _, bound := range []string{"blobs", "rows", "json-bytes", "object-bytes"} {
			for _, smaller := range []string{"policy", "caller"} {
				t.Run(kind+"/"+bound+"/"+smaller, func(t *testing.T) {
					var value any = report
					blobs, rows, dependencyBytes := int64(4), int64(7), int64(11)
					if kind == "rollup" {
						value = rollup
						blobs = 3
						dependencyBytes = rollup.OutputManifest.ByteSize + rollup.ArtifactReports[0].Report.ByteSize
					}
					data, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					exact := int64(len(data))
					switch bound {
					case "blobs":
						exact = blobs
					case "rows":
						exact = rows
					case "object-bytes":
						exact += dependencyBytes
					}
					set := func(limits *Limits, n int64) {
						switch bound {
						case "blobs":
							limits.MaxBlobs = n
						case "rows":
							limits.MaxRows = n
						case "json-bytes":
							limits.MaxJSONBytes = n
						case "object-bytes":
							limits.MaxObjectBytes = n
						}
					}
					for _, n := range []int64{exact, exact - 1} {
						selected := fixturePolicy(t)
						caller := defaults
						if smaller == "policy" {
							set(&selected.AcceptanceLimits, n)
						} else {
							set(&caller, n)
						}
						_, hash, err := PolicyBundleCodec(defaults).Encode(selected)
						if err != nil {
							t.Fatal(err)
						}
						check := func(encoded []byte, encodeErr, decodeErr error) {
							t.Helper()
							if n == exact {
								if encodeErr != nil || decodeErr != nil || int64(len(encoded)) != int64(len(data)) {
									t.Fatal("exact report bound rejected or size changed", encodeErr, decodeErr)
								}
							} else if encodeErr == nil || decodeErr == nil {
								t.Fatal("smaller report bound ignored", encodeErr, decodeErr)
							}
						}
						if kind == "artifact" {
							v := report
							v.Artifact.PolicyHash = hash
							codec, err := GateReportCodec(selected, caller)
							if err != nil {
								t.Fatal("policy rejected before report bound could be tested", err)
							}
							wire, err := json.Marshal(v)
							if err != nil {
								t.Fatal(err)
							}
							encoded, _, encodeErr := codec.Encode(v)
							_, _, decodeErr := codec.Decode(wire)
							check(encoded, encodeErr, decodeErr)
						} else {
							v := rollup
							v.PolicyHash = hash
							codec, err := GateRollupCodec(selected, caller)
							if err != nil {
								t.Fatal("policy rejected before roll-up bound could be tested", err)
							}
							wire, err := json.Marshal(v)
							if err != nil {
								t.Fatal(err)
							}
							encoded, _, encodeErr := codec.Encode(v)
							_, _, decodeErr := codec.Decode(wire)
							check(encoded, encodeErr, decodeErr)
						}
					}
				})
			}
		}
	}
}
