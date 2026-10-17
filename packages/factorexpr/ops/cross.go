package ops

import (
	"math"
	"sort"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

// valid 返回非 NaN 的下标（保持原顺序）。
func valid(values []float64) []int {
	indexes := make([]int, 0, len(values))
	for i, v := range values {
		if !math.IsNaN(v) {
			indexes = append(indexes, i)
		}
	}
	return indexes
}

func fillNaN(out []float64) {
	for i := range out {
		out[i] = nan
	}
}

// crossMean 返回有效值的均值；全部相同时直接返回这个值。
func crossMean(values []float64, indexes []int) (float64, bool) {
	first := values[indexes[0]]
	constant := true
	for _, i := range indexes[1:] {
		if values[i] != first {
			constant = false
			break
		}
	}
	if constant {
		return first, true
	}
	return exactSumOf(len(indexes), func(k int) float64 { return values[indexes[k]] }) / float64(len(indexes)), false
}

var cross = map[string]Cross{
	// cs_rank：截面百分位（名次 / 有效样本数），并列取平均名次。
	"cs_rank": func(values, out []float64) {
		fillNaN(out)
		indexes := valid(values)
		if len(indexes) == 0 {
			return
		}
		sort.SliceStable(indexes, func(a, b int) bool { return values[indexes[a]] < values[indexes[b]] })
		count := float64(len(indexes))
		for start := 0; start < len(indexes); {
			end := start
			for end+1 < len(indexes) && values[indexes[end+1]] == values[indexes[start]] {
				end++
			}
			rank := (float64(start+1) + float64(end+1)) / 2
			for k := start; k <= end; k++ {
				out[indexes[k]] = rank / count
			}
			start = end + 1
		}
	},
	// cs_zscore：(x − mean) / std（ddof=1）；样本少于 2 或标准差为 0 时为 NaN。
	"cs_zscore": func(values, out []float64) {
		fillNaN(out)
		indexes := valid(values)
		if len(indexes) < 2 {
			return
		}
		m, constant := crossMean(values, indexes)
		if constant {
			return
		}
		ss := exactSumOf(len(indexes), func(k int) float64 { d := values[indexes[k]] - m; return d * d })
		sd := math.Sqrt(ss / float64(len(indexes)-1))
		if sd == 0 {
			return
		}
		for _, i := range indexes {
			out[i] = factorexpr.Finite((values[i] - m) / sd)
		}
	},
	// cs_demean：x − mean。
	"cs_demean": func(values, out []float64) {
		fillNaN(out)
		indexes := valid(values)
		if len(indexes) == 0 {
			return
		}
		m, _ := crossMean(values, indexes)
		for _, i := range indexes {
			out[i] = factorexpr.Finite(values[i] - m)
		}
	},
	// cs_scale：x / sum(|x|)；分母为 0 时为 NaN。
	"cs_scale": func(values, out []float64) {
		fillNaN(out)
		indexes := valid(values)
		if len(indexes) == 0 {
			return
		}
		total := exactSumOf(len(indexes), func(k int) float64 { return math.Abs(values[indexes[k]]) })
		if total == 0 || math.IsInf(total, 0) {
			return
		}
		for _, i := range indexes {
			out[i] = factorexpr.Finite(values[i] / total)
		}
	},
}
