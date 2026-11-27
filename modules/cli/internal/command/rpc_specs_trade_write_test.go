package command

import "testing"

func TestIsPaperExecutionMode(t *testing.T) {
	for _, value := range []any{"EXECUTION_MODE_PAPER", "PAPER", "paper", "1", float64(1)} {
		if !isPaperExecutionMode(value) {
			t.Errorf("isPaperExecutionMode(%#v) = false, want true", value)
		}
	}
	for _, value := range []any{"EXECUTION_MODE_LIVE", "LIVE", "2", float64(2), "", nil, 0} {
		if isPaperExecutionMode(value) {
			t.Errorf("isPaperExecutionMode(%#v) = true, want false", value)
		}
	}
}
