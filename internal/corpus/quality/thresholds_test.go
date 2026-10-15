package quality

import (
	"math"
	"strings"
	"testing"
)

func TestDiagnosticsThresholdGrammar(t *testing.T) {
	for _, spelling := range []string{"qualification-required", "min:0", "max:0", "min:1", "max:1", "min:0.1", "max:0.000001", "min:0.999999", "max:0.123456"} {
		if _, ok := parseThreshold(spelling); !ok {
			t.Fatal("canonical threshold rejected")
		}
	}
	for _, spelling := range []string{"", "min:", "min:-0", "min:+1", "min:01", "min:0.", "min:.1", "min:1.0", "min:0.10", "min:0.000000", "max:0.1234567", "min:2", "min:1e0", " min:1", "max:1 ", "MIN:1", "qualification-required ", "min:0.é", "min:" + strings.Repeat("9", 100000)} {
		if _, ok := parseThreshold(spelling); ok {
			t.Fatal("noncanonical threshold accepted")
		}
	}
}

func TestDiagnosticsIntegerThresholdBoundaries(t *testing.T) {
	minimum, _ := parseThreshold("min:0.5")
	maximum, _ := parseThreshold("max:0.5")
	for _, c := range []struct {
		n, d     int64
		min, max bool
	}{
		{49, 100, false, true}, {50, 100, true, true}, {51, 100, true, false},
		{math.MaxInt64 / 2, math.MaxInt64, false, true},
		{math.MaxInt64/2 + 1, math.MaxInt64, true, false},
		{math.MaxInt64 - 1, math.MaxInt64 - 1, true, false},
		{0, 0, false, false}, {-1, 1, false, false}, {2, 1, false, false},
	} {
		if minimum.passes(c.n, c.d) != c.min || maximum.passes(c.n, c.d) != c.max {
			t.Fatal("integer threshold boundary mismatch")
		}
	}
	qualification, _ := parseThreshold("qualification-required")
	if qualification.passes(1, 1) {
		t.Fatal("qualification-required measured a pass")
	}
}
