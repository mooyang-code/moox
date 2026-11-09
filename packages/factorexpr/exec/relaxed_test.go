package exec

import (
	"math"
	"testing"
)

// 两个序列参数的宽松窗口：x 的中途 NaN 落在 y 开始之前时，结果仍为 NaN，与单序列宽松算子的“有效值之后再出现 NaN 仍为 NaN”一致。
// x 在第 10 根中途缺失，y 从第 12 根才开始；窗口长度 8。t=15、16 的窗口里 x 先有效、再缺失：应为 NaN
// （旧实现只看两条序列都开始之后的部分，会在这里出值）。t=17 起第 10 根位于窗口左缘或已滑出，按开头 NaN 处理，照常出值。
func TestRelaxedPairRejectsInternalNaNBeforeOtherStarts(t *testing.T) {
	const length = 24
	closes, opens, late := make([]float64, length), make([]float64, length), make([]float64, length)
	for i := range closes {
		closes[i] = 1 + float64(i%5)*0.3 + float64(i)*0.01
		late[i] = math.NaN()
		if i >= 12 {
			late[i] = float64(i*i%7) + 0.5
		}
	}
	opens[10] = 2
	data := map[string][]float64{"close": closes, "open": opens, "late": late}
	for _, output := range []string{"ts_cov_relaxed(x, late, 8, 2)", "ts_corr_relaxed(x, late, 8, 2)", "ts_corr_relaxed(late, x, 8, 2)", "ts_mean_relaxed(x, 8, 2)"} {
		spec := "let:\n  x: where(open == 2, nan, close)\noutput: " + output + "\n"
		got, start := evalSpec(t, spec, data, length)
		if start > 15 {
			t.Fatalf("%s: start = %d", output, start)
		}
		for _, tm := range []int{15, 16} {
			if !math.IsNaN(got[tm]) {
				t.Errorf("%s t=%d: got %v, want NaN（x 在窗口里有中途 NaN）", output, tm, got[tm])
			}
		}
		for tm := 17; tm < length; tm++ {
			if math.IsNaN(got[tm]) {
				t.Errorf("%s t=%d: 中途 NaN 已在窗口左缘或滑出，不应为 NaN", output, tm)
			}
		}
	}
}
