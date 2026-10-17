package engine

import (
	"os"
	"slices"
	"testing"
)

func TestAutoWorkersScaleWithCPUs(t *testing.T) {
	for _, tc := range []struct{ cpus, python, read int }{{1, 4, 4}, {4, 4, 4}, {16, 4, 4}, {32, 8, 8}, {88, 16, 16}, {256, 16, 16}} {
		if got := autoPythonWorkers(tc.cpus); got != tc.python {
			t.Errorf("autoPythonWorkers(%d)=%d，期望 %d", tc.cpus, got, tc.python)
		}
		if got := autoReadWorkers(tc.cpus); got != tc.read {
			t.Errorf("autoReadWorkers(%d)=%d，期望 %d", tc.cpus, got, tc.read)
		}
	}
}

// 常驻服务与 run-once 启动的 Python worker 都以单线程 BLAS 运行。
func TestPythonProcessConfigUsesSingleThreadBLAS(t *testing.T) {
	for _, key := range []string{"OPENBLAS_NUM_THREADS", "OMP_NUM_THREADS", "MKL_NUM_THREADS"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	got := PythonConfig{Bin: "py", WorkerPath: "w.py", FactorsDir: "f"}.processConfig()
	for _, want := range []string{"OPENBLAS_NUM_THREADS=1", "OMP_NUM_THREADS=1", "MKL_NUM_THREADS=1"} {
		if !slices.Contains(got.Env, want) {
			t.Errorf("worker 环境缺少 %s：%v", want, got.Env)
		}
	}
	if got.PythonBin != "py" || got.WorkerPath != "w.py" || !slices.Equal(got.Args, []string{"--factors-dir", "f"}) {
		t.Errorf("worker 启动参数不符：%+v", got)
	}
}
