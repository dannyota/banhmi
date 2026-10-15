package intake

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

func TestAdmissionWaitingTelemetryContainsOnlyAggregateLabels(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	admissionWaiting(context.Background(), "vn", run.AdmissionOpenFull, intakedb.ReadAdmissionQueueHeadsRow{
		Launch: "private-delivery", Resume: "private-slot", LaunchWaiting: 2, ResumeWaiting: 3})
	var event map[string]any
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event["msg"] != "country_admission_waiting" || event["country"] != "vn" || event["refusal"] != "open_full" || event["count"] != float64(5) {
		t.Fatalf("aggregate telemetry: %+v", event)
	}
	for label := range event {
		switch label {
		case "time", "level", "msg", "country", "refusal", "count":
		default:
			t.Fatalf("unexpected admission telemetry label %q", label)
		}
	}
	if bytes.Contains(output.Bytes(), []byte("private-")) {
		t.Fatal("telemetry exposed a queued item")
	}
}
