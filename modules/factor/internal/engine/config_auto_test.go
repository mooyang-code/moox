package engine

import (
	"runtime"
	"testing"
)

// 表达式执行器的默认并行度是 min(4, 核数)，默认内存上限 4GiB（设计 7.2、7.5）。
func TestComputeDefaults(t *testing.T) {
	cfg := Default()
	if want := min(4, runtime.NumCPU()); cfg.Compute.Workers != want {
		t.Errorf("compute.workers=%d，期望 %d", cfg.Compute.Workers, want)
	}
	if cfg.Compute.MaxMemory != 4<<30 {
		t.Errorf("compute.max_memory=%d，期望 4GiB", cfg.Compute.MaxMemory)
	}
}
