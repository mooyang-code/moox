package metrics

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordIngestUpdatesBoundedTelemetry(t *testing.T) {
	before := testutil.ToFloat64(ingestTotal.WithLabelValues("success"))
	recordIngest(nil, "success", time.Now().Add(-time.Second))
	if got := testutil.ToFloat64(ingestTotal.WithLabelValues("success")); got != before+1 {
		t.Fatalf("success total = %v", got)
	}
	if testutil.ToFloat64(ingestLastSuccess) <= 0 {
		t.Fatal("last success was not recorded")
	}
}

func TestEvaluateModuleHealthTruthTable(t *testing.T) {
	now := time.Unix(1000, 0)
	base := report.ModuleHealthSignals{EnabledWorkloads: 1, PreviousInputWatermark: now.Add(-3 * time.Minute), InputWatermark: now.Add(-time.Minute), OutputWatermark: now.Add(-90 * time.Second), MaxLag: 2 * time.Minute}
	cases := []struct {
		name, want string
		mutate     func(*report.ModuleHealthSignals)
	}{
		{name: "no workload", want: "SKIPPED", mutate: func(s *report.ModuleHealthSignals) { s.EnabledWorkloads = 0 }},
		{name: "input idle", want: "PASS", mutate: func(s *report.ModuleHealthSignals) { s.InputWatermark = s.PreviousInputWatermark }},
		{name: "output stalled", want: "FAIL", mutate: func(s *report.ModuleHealthSignals) { s.OutputWatermark = now.Add(-10 * time.Minute) }},
		{name: "legal empty", want: "PASS", mutate: func(s *report.ModuleHealthSignals) { s.LegalEmptyOutput = true }},
		{name: "storage deferred", want: "SKIPPED", mutate: func(s *report.ModuleHealthSignals) { s.ObservabilityDeferred = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			signals := base
			tc.mutate(&signals)
			got := report.EvaluateModuleHealth(signals, now)
			if got.Status != tc.want {
				t.Fatalf("verdict = %+v, want %s", got, tc.want)
			}
			if tc.name == "storage deferred" && got.Reason != "storage_observability_deferred" {
				t.Fatalf("verdict = %+v", got)
			}
		})
	}
}
