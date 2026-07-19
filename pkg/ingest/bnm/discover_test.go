package bnm

import "testing"

func TestDecodeFileStem(t *testing.T) {
	tests := []struct{ in, want string }{
		{"PD+Management+of+Customer+Info", "PD Management of Customer Info"},
		{"pua_20120305_P.U.+%28A%29+70+-+PERATURAN", "pua_20120305_P.U. (A) 70 - PERATURAN"},
		{"1.7_MSB+%28Remittance+Business%29+Regulations+2012", "1.7_MSB (Remittance Business) Regulations 2012"},
		{"MCIPD_PD_2025", "MCIPD_PD_2025"},                                   // clean stem unchanged
		{"pd-AMLCFTCPF-TFS-FI-Feb2024_%2", "pd-AMLCFTCPF-TFS-FI-Feb2024_%2"}, // bad escape passes through
	}
	for _, tt := range tests {
		if got := decodeFileStem(tt.in); got != tt.want {
			t.Errorf("decodeFileStem(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
