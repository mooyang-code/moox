package ops

import (
	"math"
	"sort"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

// Aggregate 计算一个截面的统计量（聚合广播类截面算子）：in 是同一时刻、同一 series_tag、成员标的范围之内各行的输入
// （可能含 NaN）。返回值由 broadcast 写给截面的每一行，包括输入为 NaN 的行；输入为 NaN 的行不参与统计，没有有效样本时返回 NaN。
type Aggregate func(in Section) float64

// AggregateKernel 按节点的编译期常量（例如 cs_quantile 的 q）与参考标的（cs_ref）准备聚合内核；不是聚合广播类算子时返回 nil。
// 内核不保存跨调用的状态，可以被多个 goroutine 同时调用。
func AggregateKernel(op string, floats []float64, subject string) Aggregate {
	switch op {
	case "cs_quantile":
		q := floats[0]
		return sortedAggregate(func(sorted []float64) float64 { return quantileLinear(sorted, q) })
	case "cs_ref":
		return func(in Section) float64 {
			for i, s := range in.Subjects {
				if s == subject {
					return in.Args[0][i]
				}
			}
			return nan
		}
	}
	return aggregates[op]
}

// broadcast 把统计量写给截面的每一行。
func broadcast(aggregate Aggregate) Cross {
	return func(in Section, out []float64) string {
		value := aggregate(in)
		for i := range out {
			out[i] = value
		}
		return ""
	}
}

// validValues 返回非 NaN 的值（保持原顺序）。
func validValues(values []float64) []float64 {
	out := make([]float64, 0, len(values))
	for _, v := range values {
		if !math.IsNaN(v) {
			out = append(out, v)
		}
	}
	return out
}

// onValid 把统计函数包装成聚合内核：只在有效样本上计算，样本少于 minimum 个时为 NaN，±Inf 记为 NaN。
func onValid(minimum int, fn func(valid []float64) float64) Aggregate {
	return func(in Section) float64 {
		valid := validValues(in.Args[0])
		if len(valid) < minimum {
			return nan
		}
		return factorexpr.Finite(fn(valid))
	}
}

// sortedAggregate 在排好序的有效样本上计算（中位数、分位数）；−0 归一成 +0，与 sortedWindow 一致。
func sortedAggregate(fn func(sorted []float64) float64) Aggregate {
	return onValid(1, func(valid []float64) float64 {
		for i := range valid {
			valid[i] += 0
		}
		sort.Float64s(valid)
		return fn(valid)
	})
}

var aggregates = map[string]Aggregate{
	// 均值、和、标准差与时序算子共用同一套精确求和与两遍法。
	"cs_mean": onValid(1, meanValue),
	"cs_sum":  onValid(1, exactSum),
	"cs_std":  onValid(2, stdDev),
	// cs_count 的输入是布尔（1、0、NaN）：计真值个数。
	"cs_count":  onValid(1, countTrue),
	"cs_median": sortedAggregate(median),
	"cs_min":    onValid(1, windowMin),
	"cs_max":    onValid(1, windowMax),
}
