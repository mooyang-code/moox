package ops

import (
	"math"
	"math/rand"
	"testing"
)

// 大窗口的中位数、分位数：第一个窗口一次性排序，常数输入不再是 O(w²)。
func TestSortedWindowLargeConstant(t *testing.T) {
	x := make([]float64, 5000)
	for i := range x {
		x[i] = 3.5
	}
	out := make([]float64, 1)
	RollingKernel("ts_median")([]int{5000}, nil)(out, 4999, []Series{{Data: x}})
	if out[0] != 3.5 {
		t.Fatalf("median = %v", out[0])
	}
	x[10] = nan
	RollingKernel("ts_quantile")([]int{5000}, []float64{0.9})(out, 4999, []Series{{Data: x}})
	if !math.IsNaN(out[0]) {
		t.Fatalf("quantile with NaN in window = %v", out[0])
	}
}

func BenchmarkMedianLargeWindowSinglePoint(b *testing.B) {
	x := make([]float64, 1440)
	for i := range x {
		x[i] = 100
	}
	out := make([]float64, 1)
	kernel := RollingKernel("ts_median")([]int{1440}, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		kernel(out, 1439, []Series{{Data: x}})
	}
}

func BenchmarkMedianLargeWindowChunk(b *testing.B) {
	rng := rand.New(rand.NewSource(1))
	x := make([]float64, 1440+500)
	for i := range x {
		x[i] = 100 + rng.NormFloat64()
	}
	out := make([]float64, 500)
	kernel := RollingKernel("ts_median")([]int{1440}, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		kernel(out, 1439, []Series{{Data: x}})
	}
}
