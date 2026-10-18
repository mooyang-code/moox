package engine

import (
	"runtime"
	"testing"
)

func TestAutoReadWorkersScaleWithCPUs(t *testing.T) {
	for _, tc := range []struct{ cpus, read int }{{1, 4}, {4, 4}, {16, 4}, {32, 8}, {88, 16}, {256, 16}} {
		if got := autoReadWorkers(tc.cpus); got != tc.read {
			t.Errorf("autoReadWorkers(%d)=%d，期望 %d", tc.cpus, got, tc.read)
		}
	}
}

// 表达式执行器的默认并行度是 min(4, 核数)，默认内存上限 1GiB（设计 7.2、7.5）。
func TestComputeDefaults(t *testing.T) {
	cfg := Default()
	if want := min(4, runtime.NumCPU()); cfg.Compute.Workers != want {
		t.Errorf("compute.workers=%d，期望 %d", cfg.Compute.Workers, want)
	}
	if cfg.Compute.MaxMemory != 1<<30 {
		t.Errorf("compute.max_memory=%d，期望 1GiB", cfg.Compute.MaxMemory)
	}
}
