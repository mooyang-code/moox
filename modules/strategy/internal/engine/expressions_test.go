package engine

import (
	"math"
	"testing"
)

// zscore 与样本的量级无关：极大或极小的有限值不能因为中间量溢出或下溢而全部变成 0。
func TestZScoreIsScaleInvariant(t *testing.T) {
	ids := []string{"A", "B", "C"}
	for _, scale := range []float64{1, 1e160, 1e300, 1e-160} {
		got := zScore(ids, map[string]float64{"A": 1 * scale, "B": 2 * scale, "C": 3 * scale})
		if math.Abs(got["A"]+math.Sqrt(1.5)) > 1e-9 || math.Abs(got["B"]) > 1e-9 || math.Abs(got["C"]-math.Sqrt(1.5)) > 1e-9 {
			t.Fatalf("量级 %g 下 zscore 不符：%v", scale, got)
		}
	}
	flat := zScore(ids, map[string]float64{"A": 1e160, "B": 1e160, "C": 1e160})
	if flat["A"] != 0 || flat["B"] != 0 || flat["C"] != 0 {
		t.Fatalf("零方差应为 0：%v", flat)
	}
	invalid := zScore(ids, map[string]float64{"A": math.Inf(1), "B": 1, "C": 2})
	if !math.IsNaN(invalid["B"]) {
		t.Fatalf("含非有限值的样本不能产生有限分数：%v", invalid)
	}
}
