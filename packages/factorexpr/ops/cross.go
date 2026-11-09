package ops

import (
	"math"
	"sort"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

// Section 是一个截面的输入：同一时刻、同一 series_tag、成员标的范围之内的各行，按标的排序。
type Section struct {
	// Args 与算子的序列参数一一对应（不含分组键），每个都与 Subjects 等长，可能含 NaN。
	Args [][]float64
	// Group 是分组键（gs_*、gx_*、gs_resid 的 g）；算子没有分组键时为 nil。
	Group []float64
	// Subjects 是各行的标的。
	Subjects []string
}

// Cross 计算一个截面，结果写入与 Subjects 等长的 out。
//   - 逐行变换类：输入为 NaN 的行不参与计算，输出也为 NaN；
//   - 聚合广播类（第 1 期的 Aggregate 经 broadcast 包装）：统计量写给每一行，包括输入为 NaN 的行。
//
// 返回非空串表示这个截面整体没有算出（out 已全部写成 NaN），值是计入失败指标的原因，例如 cs_resid_singular。
// 内核不保存跨调用的状态，可以被多个 goroutine 同时调用。
type Cross func(in Section, out []float64) string

// CrossKernel 按节点的编译期常量（例如 cs_quantile 的 q）与参考标的（cs_ref）准备截面内核；不是截面算子时返回 nil。
func CrossKernel(op string, floats []float64, subject string) Cross {
	if kernel, ok := cross[op]; ok {
		return kernel
	}
	if kernel, ok := grouped[op]; ok {
		return kernel
	}
	if aggregate := AggregateKernel(op, floats, subject); aggregate != nil {
		return broadcast(aggregate)
	}
	return nil
}

// rowwise 把单输入的逐行变换包装成截面内核。
func rowwise(fn func(values, out []float64)) Cross {
	return func(in Section, out []float64) string {
		fn(in.Args[0], out)
		return ""
	}
}

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
	"cs_rank":   rowwise(crossRank),
	"cs_zscore": rowwise(crossZscore),
	"cs_demean": rowwise(crossDemean),
	"cs_scale":  rowwise(crossScale),
	"cs_resid": func(in Section, out []float64) string {
		return residuals(in, out, false)
	},
}

// cs_rank：截面百分位（名次 / 有效样本数），并列取平均名次。
func crossRank(values, out []float64) {
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
}

// cs_zscore：(x − mean) / std（ddof=1）；样本少于 2 或标准差为 0 时为 NaN。
func crossZscore(values, out []float64) {
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
}

// cs_demean：x − mean。
func crossDemean(values, out []float64) {
	fillNaN(out)
	indexes := valid(values)
	if len(indexes) == 0 {
		return
	}
	m, _ := crossMean(values, indexes)
	for _, i := range indexes {
		out[i] = factorexpr.Finite(values[i] - m)
	}
}

// cs_scale：x / sum(|x|)；分母为 0 时为 NaN。
func crossScale(values, out []float64) {
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
}
