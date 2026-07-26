package metrics

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordIngestUpdatesBoundedTelemetry(t *testing.T) {
	before := testutil.ToFloat64(ingestTotal.WithLabelValues("success"))
	recordIngest("success", time.Now().Add(-time.Second))
	if got := testutil.ToFloat64(ingestTotal.WithLabelValues("success")); got != before+1 {
		t.Fatalf("success total = %v", got)
	}
	if testutil.ToFloat64(ingestLastSuccess) <= 0 {
		t.Fatal("last success was not recorded")
	}
}

func TestEvaluatePipelineSignalsTruthTable(t *testing.T) {
	now := time.Unix(1000, 0)
	base := report.PipelineSignals{EnabledWorkloads: 1, PreviousInputWatermark: now.Add(-3 * time.Minute), InputWatermark: now.Add(-time.Minute), OutputWatermark: now.Add(-90 * time.Second), LagTolerance: 2 * time.Minute}
	cases := []struct {
		name, want string
		mutate     func(*report.PipelineSignals)
	}{
		{name: "no workload", want: "SKIPPED", mutate: func(s *report.PipelineSignals) { s.EnabledWorkloads = 0 }},
		{name: "input idle", want: "PASS", mutate: func(s *report.PipelineSignals) { s.InputWatermark = s.PreviousInputWatermark }},
		{name: "output stalled", want: "FAIL", mutate: func(s *report.PipelineSignals) { s.OutputWatermark = now.Add(-10 * time.Minute) }},
		{name: "legal empty", want: "PASS", mutate: func(s *report.PipelineSignals) { s.LegalEmptyOutput = true }},
		{name: "storage deferred", want: "SKIPPED", mutate: func(s *report.PipelineSignals) { s.CrossesStorageDeferred = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			signals := base
			tc.mutate(&signals)
			got := report.EvaluatePipelineSignals(signals, now)
			if got.Status != tc.want {
				t.Fatalf("verdict = %+v, want %s", got, tc.want)
			}
			if tc.name == "storage deferred" && got.Reason != "storage_observability_deferred" {
				t.Fatalf("verdict = %+v", got)
			}
		})
	}
}
