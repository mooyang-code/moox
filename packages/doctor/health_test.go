package doctor

import (
	"testing"
	"time"
)

func TestEvaluateModuleRunHealthMatchesSharedContract(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		signals ModuleRunSignals
		status  CheckStatus
		reason  string
	}{
		{
			name:    "never ran",
			signals: ModuleRunSignals{MaxLag: 5 * time.Minute},
			status:  StatusSkipped, reason: ModuleRunNoRun,
		},
		{
			name:    "succeeded after earlier error",
			signals: ModuleRunSignals{LastSuccess: now.Add(-time.Minute), LastError: now.Add(-10 * time.Minute), MaxLag: 5 * time.Minute},
			status:  StatusPass, reason: ModuleRunRecentSuccess,
		},
		{
			name:    "failed but within max lag of last success",
			signals: ModuleRunSignals{LastSuccess: now.Add(-3 * time.Minute), LastError: now.Add(-time.Minute), MaxLag: 10 * time.Minute},
			status:  StatusPass, reason: ModuleRunRecentSuccess,
		},
		{
			name:    "failed over max lag",
			signals: ModuleRunSignals{LastSuccess: now.Add(-time.Hour), LastError: now.Add(-10 * time.Minute), MaxLag: 10 * time.Minute},
			status:  StatusFail, reason: ModuleRunFailingOverMaxLag,
		},
		{
			name:    "never succeeded and failed within idle window",
			signals: ModuleRunSignals{LastError: now.Add(-50 * time.Minute), MaxLag: 2 * time.Minute},
			status:  StatusFail, reason: ModuleRunNeverSucceeded,
		},
		{
			name:    "idle after old error",
			signals: ModuleRunSignals{LastSuccess: now.Add(-3 * time.Hour), LastError: now.Add(-2 * time.Hour), MaxLag: 5 * time.Minute},
			status:  StatusPass, reason: ModuleRunIdleAfterError,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateModuleRunHealth(tc.signals, now)
			if got.Status != tc.status || got.Reason != tc.reason {
				t.Fatalf("got (%s, %s), want (%s, %s)", got.Status, got.Reason, tc.status, tc.reason)
			}
		})
	}
}

func TestComponentModuleMappingAndHealthStatusClassification(t *testing.T) {
	t.Parallel()
	if ComponentModule("factor-mgr") != "factor" || ComponentModule("strategy") != "strategy" {
		t.Fatal("unexpected ComponentModule mapping")
	}
	if ModuleComponent("factor") != "factor-mgr" || ModuleComponent("trade") != "trade" {
		t.Fatal("unexpected ModuleComponent mapping")
	}
	if HealthStatusSeverity("healthy") != "" || HealthStatusSeverity("disabled") != "" || HealthStatusSeverity("down") != "critical" || HealthStatusSeverity("degraded") != "warning" {
		t.Fatal("unexpected HealthStatusSeverity mapping")
	}
	if !(HealthStatusRank("down") > HealthStatusRank("degraded") && HealthStatusRank("degraded") > HealthStatusRank("unknown") && HealthStatusRank("unknown") > HealthStatusRank("healthy")) {
		t.Fatal("unexpected HealthStatusRank ordering")
	}
}
