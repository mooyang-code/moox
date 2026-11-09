package ops

import (
	"math"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

// ReasonResidSingular 是截面回归样本不足或设计矩阵奇异时计入失败指标的原因。
const ReasonResidSingular = "cs_resid_singular"

// epsilon 是 float64 的机器精度。
const epsilon = 0x1p-52

// residuals 是 cs_resid(y, x1…xk) 与 gs_resid(y, g, x1…xk) 的内核：在 y 与全部 x（gs_resid 还有 g）都有效的行上做最小二乘，
// 输出残差；其余行为 NaN。
//
// 数值做法（Frisch–Waugh–Lovell）：先把 y 与各 x 去均值——cs_resid 按整个截面去均值，相当于带截距项；gs_resid 按组去均值，
// 相当于每组一个截距（分组哑变量）——再对去均值后的 x 做 Householder QR，求不带截距的回归。结果与带截距（或哑变量）的
// 完整设计矩阵做最小二乘相同。gs_resid 不带 x 时就是组内去均值，与 gs_demean 逐位相同。
//
// 有效行数不超过参数个数（cs_resid 为 k+1，gs_resid 为 k+组数），或去均值后的设计矩阵奇异（某个 x 与截距、哑变量或其他 x
// 共线）时，整个截面为 NaN，返回 ReasonResidSingular。
func residuals(in Section, out []float64, byGroup bool) string {
	fillNaN(out)
	k := len(in.Args) - 1
	var rows []int
	for i := range in.Subjects {
		ok := !byGroup || !math.IsNaN(in.Group[i])
		for _, column := range in.Args {
			ok = ok && !math.IsNaN(column[i])
		}
		if ok {
			rows = append(rows, i)
		}
	}
	// 分组：cs_resid 只有一组。组内下标是 rows 里的位置。
	groups := [][]int{make([]int, len(rows))}
	for position := range rows {
		groups[0][position] = position
	}
	if byGroup {
		keys := make([]float64, len(rows))
		for position, i := range rows {
			keys[position] = in.Group[i]
		}
		var ok bool
		if groups, ok = groupsOf(keys); !ok {
			return ReasonTooManyGroups
		}
	}
	n := len(rows)
	if n == 0 {
		return ""
	}
	if n <= k+len(groups) {
		return ReasonResidSingular
	}
	// 去均值：centered[0] 是 y，centered[1..k] 是各 x。均值与 cs_demean 同一算法（全部相同时直接取这个值）。
	// references 是各 x 去均值之前（有效行上）的原列范数：奇异判定以它为参照。x 与截距或分组哑变量近似共线时
	// （例如由 1e6 和比它大 1 ulp 的值组成），去均值后剩下的只是舍入误差，按去均值后的范数作参照判不出来。
	centered := make([][]float64, k+1)
	references := make([]float64, k)
	for c, column := range in.Args {
		values := make([]float64, n)
		for position, i := range rows {
			values[position] = column[i]
		}
		demeaned := make([]float64, n)
		for _, members := range groups {
			m, _ := crossMean(values, members)
			for _, position := range members {
				demeaned[position] = values[position] - m
			}
		}
		centered[c] = demeaned
		if c > 0 {
			references[c-1] = euclidean(values)
		}
	}
	residual, ok := leastSquaresResidual(centered[0], centered[1:], references)
	if !ok {
		return ReasonResidSingular
	}
	for position, i := range rows {
		out[i] = factorexpr.Finite(residual[position])
	}
	return ""
}

// leastSquaresResidual 用 Householder QR 求 y 对 columns（各长 n，不含截距）的最小二乘残差 y − Xβ。references 是各列的参照范数
// （去均值之前的原列范数；为 nil 时用列本身的范数）：某一列在消去前面的列之后剩下的范数不超过 max(n, k)·ε·参照范数
// （或参照范数为 0）时判为奇异，ok 为假。去均值本身就是消去截距（哑变量）这一步，所以列本身的范数也要过同一个判定。
func leastSquaresResidual(y []float64, columns [][]float64, references []float64) ([]float64, bool) {
	n, k := len(y), len(columns)
	if k == 0 {
		return append([]float64(nil), y...), true
	}
	a := make([][]float64, k)
	norms := make([]float64, k)
	for j, column := range columns {
		a[j] = append([]float64(nil), column...)
		norms[j] = euclidean(column)
		if references != nil {
			norms[j] = references[j]
		}
	}
	qty := append([]float64(nil), y...)
	diagonal := make([]float64, k)
	tolerance := float64(max(n, k)) * epsilon
	v := make([]float64, n)
	for j := 0; j < k; j++ {
		sub := a[j][j:]
		norm := euclidean(sub)
		if norms[j] == 0 || norm <= tolerance*norms[j] {
			return nil, false
		}
		alpha := -norm
		if sub[0] < 0 {
			alpha = norm
		}
		w := v[:len(sub)]
		copy(w, sub)
		w[0] -= alpha
		vv := dot(w, w)
		reflect := func(target []float64) {
			scale := 2 * dot(w, target) / vv
			for i := range target {
				target[i] -= scale * w[i]
			}
		}
		for l := j + 1; l < k; l++ {
			reflect(a[l][j:])
		}
		reflect(qty[j:])
		diagonal[j] = alpha
	}
	// 回代 Rβ = (Qᵀy)[0:k]：R 的第 j 行是 diagonal[j] 与 a[l][j]（l > j）。
	beta := make([]float64, k)
	for j := k - 1; j >= 0; j-- {
		sum := qty[j]
		for l := j + 1; l < k; l++ {
			sum -= a[l][j] * beta[l]
		}
		beta[j] = sum / diagonal[j]
	}
	residual := make([]float64, n)
	for i := range y {
		fitted := 0.0
		for j, column := range columns {
			fitted += column[i] * beta[j]
		}
		residual[i] = y[i] - fitted
	}
	return residual, true
}

func dot(a, b []float64) float64 {
	sum := 0.0
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}

// euclidean 是向量的 2 范数，先按最大绝对值缩放，避免平方上溢或下溢。
func euclidean(values []float64) float64 {
	scale := 0.0
	for _, v := range values {
		scale = math.Max(scale, math.Abs(v))
	}
	if scale == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		d := v / scale
		sum += d * d
	}
	return scale * math.Sqrt(sum)
}
