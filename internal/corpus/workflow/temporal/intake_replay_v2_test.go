package temporal

import "testing"

func TestIntakeV2ReplayerRetainsRegistrations(t *testing.T) {
	// Construction exercises the real SDK registration path. It does not
	// substitute for replaying a hosted history, which is an M1.14 gate.
	if NewIntakeReplayerV2() == nil {
		t.Fatal("missing SDK replayer")
	}
	if ReplayIntakeHistoryFileV2(nil, "") == nil {
		t.Fatal("missing captured history accepted")
	}
}
