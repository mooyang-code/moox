package engine

import "testing"

func TestAutoWorkersScaleWithCPUs(t *testing.T) {
	for _, tc := range []struct{ cpus, python, read int }{{1, 4, 4}, {4, 4, 4}, {16, 12, 4}, {88, 64, 16}, {256, 64, 16}} {
		if got := autoPythonWorkers(tc.cpus); got != tc.python {
			t.Errorf("autoPythonWorkers(%d)=%d，期望 %d", tc.cpus, got, tc.python)
		}
		if got := autoReadWorkers(tc.cpus); got != tc.read {
			t.Errorf("autoReadWorkers(%d)=%d，期望 %d", tc.cpus, got, tc.read)
		}
	}
}
