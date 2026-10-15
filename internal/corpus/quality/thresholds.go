package quality

import "math/bits"

// threshold is a bounded rational, never a floating point approximation.
type threshold struct {
	qualification bool
	minimum       bool
	numerator     uint64
	denominator   uint64
}

func parseThreshold(s string) (threshold, bool) {
	if s == "qualification-required" {
		return threshold{qualification: true}, true
	}
	if len(s) < 5 || len(s) > 12 || (s[:4] != "min:" && s[:4] != "max:") {
		return threshold{}, false
	}
	t := threshold{minimum: s[:4] == "min:", denominator: 1}
	d := s[4:]
	if d == "0" || d == "1" {
		t.numerator = uint64(d[0] - '0')
		return t, true
	}
	if len(d) < 3 || len(d) > 8 || d[:2] != "0." || d[len(d)-1] == '0' {
		return threshold{}, false
	}
	for _, r := range d[2:] {
		if r < '0' || r > '9' {
			return threshold{}, false
		}
		t.numerator = t.numerator*10 + uint64(r-'0')
		t.denominator *= 10
	}
	return t, true
}

func (t threshold) passes(n, d int64) bool {
	if t.qualification || n < 0 || d <= 0 || n > d {
		return false
	}
	lh, ll := bits.Mul64(uint64(n), t.denominator)
	rh, rl := bits.Mul64(uint64(d), t.numerator)
	if t.minimum {
		return lh > rh || (lh == rh && ll >= rl)
	}
	return lh < rh || (lh == rh && ll <= rl)
}
