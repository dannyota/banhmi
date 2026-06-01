package mcp

import "testing"

func TestValidityWarning(t *testing.T) {
	tests := []struct {
		name          string
		issuedDate    string
		effectiveFrom string
		wantWarn      bool
	}{
		{
			name:          "effective before issued is impossible (TT 77/2025/TT-NHNN)",
			issuedDate:    "2025-12-31",
			effectiveFrom: "2025-03-01",
			wantWarn:      true,
		},
		{
			name:          "effective after issued is normal",
			issuedDate:    "2025-12-31",
			effectiveFrom: "2026-03-01",
			wantWarn:      false,
		},
		{
			name:          "same day is valid",
			issuedDate:    "2024-07-01",
			effectiveFrom: "2024-07-01",
			wantWarn:      false,
		},
		{
			name:          "missing effective date does not warn",
			issuedDate:    "2025-12-31",
			effectiveFrom: "",
			wantWarn:      false,
		},
		{
			name:          "missing issued date does not warn",
			issuedDate:    "",
			effectiveFrom: "2026-03-01",
			wantWarn:      false,
		},
		{
			name:          "unparseable dates do not warn",
			issuedDate:    "31/12/2025",
			effectiveFrom: "01/03/2025",
			wantWarn:      false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := validityWarning(tc.issuedDate, tc.effectiveFrom)
			if (got != "") != tc.wantWarn {
				t.Fatalf("validityWarning(%q, %q) = %q; wantWarn=%v", tc.issuedDate, tc.effectiveFrom, got, tc.wantWarn)
			}
		})
	}
}
