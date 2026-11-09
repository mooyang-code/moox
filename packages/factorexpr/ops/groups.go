package ops

import (
	"math"
	"sort"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

// MaxGroups 是一个截面里分组键不同取值的个数上限；超出时整个截面输出 NaN，原因记 ReasonTooManyGroups。
const MaxGroups = 1000

// ReasonTooManyGroups 是截面的组数超过 MaxGroups 时计入失败指标的原因。
const ReasonTooManyGroups = "gs_too_many_groups"

// groupsOf 按分组键把截面的行分组：g 为 NaN 的行不属于任何组；−0 与 0 是同一组。组按第一次出现的行排列，组内的行保持
// 原顺序（截面已按标的排序），结果与 map 的遍历顺序无关。组数超过 MaxGroups 时 ok 为假。
func groupsOf(keys []float64) (groups [][]int, ok bool) {
	index := make(map[uint64]int)
	for i, g := range keys {
		if math.IsNaN(g) {
			continue
		}
		bits := math.Float64bits(g + 0)
		k, seen := index[bits]
		if !seen {
			if len(groups) == MaxGroups {
				return nil, false
			}
			k = len(groups)
			index[bits] = k
			groups = append(groups, nil)
		}
		groups[k] = append(groups[k], i)
	}
	return groups, true
}

// subsection 取出一组行组成的子截面（不含分组键），复用 into 的缓冲区。
func subsection(in Section, rows []int, into Section) Section {
	if cap(into.Args) < len(in.Args) {
		into.Args = make([][]float64, len(in.Args))
	}
	into.Args = into.Args[:len(in.Args)]
	for k, values := range in.Args {
		column := into.Args[k][:0]
		for _, i := range rows {
			column = append(column, values[i])
		}
		into.Args[k] = column
	}
	into.Subjects = into.Subjects[:0]
	for _, i := range rows {
		into.Subjects = append(into.Subjects, in.Subjects[i])
	}
	into.Group = nil
	return into
}

func resize(buffer []float64, n int) []float64 {
	if cap(buffer) < n {
		return make([]float64, n)
	}
	return buffer[:n]
}

// perGroup 把截面内核按组执行：每组是一个子截面，结果写回组内各行；g 为 NaN 的行（不属于任何组）为 NaN。
// 逐行变换类的内核得到组内变换，聚合广播类的内核得到组内统计量并广播回组内每一行（包括输入为 NaN 的行）。
func perGroup(base Cross) Cross {
	return func(in Section, out []float64) string {
		fillNaN(out)
		groups, ok := groupsOf(in.Group)
		if !ok {
			return ReasonTooManyGroups
		}
		var sub Section
		var buffer []float64
		for _, rows := range groups {
			sub = subsection(in, rows, sub)
			buffer = resize(buffer, len(rows))
			if reason := base(sub, buffer); reason != "" {
				// 组内内核都不报告告警（只有截面回归会报告，它不按组执行）。
				continue
			}
			for k, i := range rows {
				out[i] = buffer[k]
			}
		}
		return ""
	}
}

// acrossGroups 先取每组 v 的有效均值作为该组的值（组内全为 NaN 的组没有值、不参与），再用 base 在组之间排名或标准化，
// 结果广播回组内每一行。
func acrossGroups(base func(values, out []float64)) Cross {
	return func(in Section, out []float64) string {
		fillNaN(out)
		groups, ok := groupsOf(in.Group)
		if !ok {
			return ReasonTooManyGroups
		}
		v := in.Args[0]
		values := make([]float64, len(groups))
		for k, rows := range groups {
			var indexes []int
			for _, i := range rows {
				if !math.IsNaN(v[i]) {
					indexes = append(indexes, i)
				}
			}
			values[k] = nan
			if len(indexes) > 0 {
				values[k], _ = crossMean(v, indexes)
			}
		}
		scores := make([]float64, len(groups))
		base(values, scores)
		for k, rows := range groups {
			for _, i := range rows {
				out[i] = scores[k]
			}
		}
		return ""
	}
}

// weightedMean 是 Σw·x / Σw，只用 x、w 都有效且 w > 0 的行；没有这样的行时为 NaN。
func weightedMean(in Section) float64 {
	x, w := in.Args[0], in.Args[1]
	var indexes []int
	for i := range x {
		if !math.IsNaN(x[i]) && !math.IsNaN(w[i]) && w[i] > 0 {
			indexes = append(indexes, i)
		}
	}
	if len(indexes) == 0 {
		return nan
	}
	numerator := exactSumOf(len(indexes), func(k int) float64 { return w[indexes[k]] * x[indexes[k]] })
	denominator := exactSumOf(len(indexes), func(k int) float64 { return w[indexes[k]] })
	return factorexpr.Finite(numerator / denominator)
}

// cumulativeShare 按 x 降序排列（并列保持原顺序，即按标的），输出累计到本行（含本行）的 x 之和占总和的比例；
// 总和为 0 时全为 NaN。累计和按这个固定顺序逐项相加，总和就是最后一项的累计和，所以排在最后的一行恰好是 1。
func cumulativeShare(values, out []float64) {
	fillNaN(out)
	indexes := valid(values)
	if len(indexes) == 0 {
		return
	}
	sort.SliceStable(indexes, func(a, b int) bool { return values[indexes[a]] > values[indexes[b]] })
	running := make([]float64, len(indexes))
	total := 0.0
	for k, i := range indexes {
		total += values[i]
		running[k] = total
	}
	if total == 0 || math.IsInf(total, 0) || math.IsNaN(total) {
		return
	}
	for k, i := range indexes {
		out[i] = factorexpr.Finite(running[k] / total)
	}
}

// grouped 是带分组键的截面内核（gs_*、gx_*）。
var grouped = map[string]Cross{
	"gs_rank":     perGroup(rowwise(crossRank)),
	"gs_demean":   perGroup(rowwise(crossDemean)),
	"gs_zscore":   perGroup(rowwise(crossZscore)),
	"gs_cumshare": perGroup(rowwise(cumulativeShare)),
	"gs_mean":     perGroup(broadcast(aggregates["cs_mean"])),
	"gs_sum":      perGroup(broadcast(aggregates["cs_sum"])),
	"gs_count":    perGroup(broadcast(aggregates["cs_count"])),
	"gs_median":   perGroup(broadcast(aggregates["cs_median"])),
	"gs_wmean":    perGroup(broadcast(weightedMean)),
	"gx_rank":     acrossGroups(crossRank),
	"gx_zscore":   acrossGroups(crossZscore),
	"gs_resid": func(in Section, out []float64) string {
		return residuals(in, out, true)
	},
}
