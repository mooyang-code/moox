package domain

import "testing"

func TestAggregatePeriodFailureReportState(t *testing.T) {
	targets := []WriteTarget{{ID: "target-a"}, {ID: "target-b"}}
	tests := []struct {
		name    string
		results []PeriodFailureTargetResult
		want    PeriodFailureReportState
	}{
		{name: "empty", want: PeriodFailureReportPending},
		{name: "accepted", results: []PeriodFailureTargetResult{{WriteTargetID: "target-a", Disposition: "recorded"}, {WriteTargetID: "target-b", Disposition: "already_succeeded"}}, want: PeriodFailureReportAcknowledged},
		{name: "missed and empty", results: []PeriodFailureTargetResult{{WriteTargetID: "target-a", Disposition: "missed_deadline"}}, want: PeriodFailureReportPending},
		{name: "accepted and missed", results: []PeriodFailureTargetResult{{WriteTargetID: "target-a", Disposition: "recorded"}, {WriteTargetID: "target-b", Disposition: "missed_deadline"}}, want: PeriodFailureReportMissedDeadline},
		{name: "unknown", results: []PeriodFailureTargetResult{{WriteTargetID: "target-a", Disposition: "future_value"}, {WriteTargetID: "target-b", Disposition: "recorded"}}, want: PeriodFailureReportPending},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := AggregatePeriodFailureReportState(targets, test.results); got != test.want {
				t.Fatalf("AggregatePeriodFailureReportState() = %q, want %q", got, test.want)
			}
		})
	}
}
