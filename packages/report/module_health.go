package report

import (
	"fmt"
	"time"
)

type ModuleHealthVerdict struct{ Status, Reason string }

type ModuleHealthSignals struct {
	EnabledWorkloads       int
	InputWatermark         time.Time
	OutputWatermark        time.Time
	PreviousInputWatermark time.Time
	MaxLag                 time.Duration
	LegalEmptyOutput       bool
	ObservabilityDeferred  bool
}

// EvaluateModuleHealth is the shared Monitor and Doctor truth table.
func EvaluateModuleHealth(signals ModuleHealthSignals, now time.Time) ModuleHealthVerdict {
	if signals.ObservabilityDeferred {
		return ModuleHealthVerdict{Status: "SKIPPED", Reason: "storage_observability_deferred"}
	}
	if signals.EnabledWorkloads == 0 {
		return ModuleHealthVerdict{Status: "SKIPPED", Reason: "no_enabled_workload"}
	}
	if signals.InputWatermark.IsZero() {
		return ModuleHealthVerdict{Status: "UNKNOWN", Reason: "input_observation_missing"}
	}
	if !signals.PreviousInputWatermark.IsZero() &&
		!signals.InputWatermark.After(signals.PreviousInputWatermark) {
		return ModuleHealthVerdict{Status: "PASS", Reason: "input_idle"}
	}
	if signals.LegalEmptyOutput {
		return ModuleHealthVerdict{Status: "PASS", Reason: "legal_empty_output"}
	}
	if signals.MaxLag <= 0 {
		return ModuleHealthVerdict{Status: "UNKNOWN", Reason: "invalid_max_lag"}
	}
	if signals.OutputWatermark.IsZero() ||
		signals.InputWatermark.Sub(signals.OutputWatermark) > signals.MaxLag ||
		now.Sub(signals.OutputWatermark) > signals.MaxLag {
		return ModuleHealthVerdict{
			Status: "FAIL",
			Reason: fmt.Sprintf("output_stalled_over_%s", signals.MaxLag),
		}
	}
	return ModuleHealthVerdict{Status: "PASS", Reason: "within_max_lag"}
}
