package report

import (
	"fmt"
	"time"
)

type PipelineVerdict struct{ Status, Reason string }

type PipelineSignals struct {
	EnabledWorkloads       int
	InputWatermark         time.Time
	OutputWatermark        time.Time
	PreviousInputWatermark time.Time
	LagTolerance           time.Duration
	LegalEmptyOutput       bool
	CrossesStorageDeferred bool
}

// EvaluatePipelineSignals is the shared Monitor and Doctor truth table.
func EvaluatePipelineSignals(signals PipelineSignals, now time.Time) PipelineVerdict {
	if signals.CrossesStorageDeferred {
		return PipelineVerdict{Status: "SKIPPED", Reason: "storage_observability_deferred"}
	}
	if signals.EnabledWorkloads == 0 {
		return PipelineVerdict{Status: "SKIPPED", Reason: "no_enabled_workload"}
	}
	if signals.InputWatermark.IsZero() {
		return PipelineVerdict{Status: "UNKNOWN", Reason: "input_observation_missing"}
	}
	if !signals.PreviousInputWatermark.IsZero() && !signals.InputWatermark.After(signals.PreviousInputWatermark) {
		return PipelineVerdict{Status: "PASS", Reason: "input_idle"}
	}
	if signals.LegalEmptyOutput {
		return PipelineVerdict{Status: "PASS", Reason: "legal_empty_output"}
	}
	if signals.LagTolerance <= 0 {
		return PipelineVerdict{Status: "UNKNOWN", Reason: "invalid_lag_tolerance"}
	}
	if signals.OutputWatermark.IsZero() || signals.InputWatermark.Sub(signals.OutputWatermark) > signals.LagTolerance || now.Sub(signals.OutputWatermark) > signals.LagTolerance {
		return PipelineVerdict{Status: "FAIL", Reason: fmt.Sprintf("output_stalled_over_%s", signals.LagTolerance)}
	}
	return PipelineVerdict{Status: "PASS", Reason: "within_lag_tolerance"}
}
