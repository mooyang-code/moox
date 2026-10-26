package ops

import (
	"math"
	"sort"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

func allEqual(w []float64) bool {
	for _, v := range w[1:] {
		if v != w[0] {
			return false
		}
	}
	return true
}

// mean 返回均值；窗口内的值全部相同时直接返回这个值（与 pandas 一致），避免舍入让常数窗口的均值偏离原值。
func mean(w []float64) (float64, bool) {
	if allEqual(w) {
		return w[0], true
	}
	return exactSum(w) / float64(len(w)), false
}

// variance 是样本方差（ddof=1），两遍法；常数窗口精确为 0。
func variance(w []float64) float64 {
	m, constant := mean(w)
	if constant {
		return 0
	}
	ss := exactSumOf(len(w), func(i int) float64 { d := w[i] - m; return d * d })
	return ss / float64(len(w)-1)
}

func windowMin(w []float64) float64 {
	result := w[0]
	for _, v := range w[1:] {
		if v < result {
			result = v
		}
	}
	return result
}

func windowMax(w []float64) float64 {
	result := w[0]
	for _, v := range w[1:] {
		if v > result {
			result = v
		}
	}
	return result
}

// rankLast 是最新一个值在窗口内的百分位（pandas rank(pct=True, method="average")）：(小于它的个数 + (等于它的个数 + 1) / 2) / n，
// 等于它的个数包括它自己。
func rankLast(w []float64) float64 {
	current := w[len(w)-1]
	less, equal := 0, 0
	for _, v := range w {
		switch {
		case v < current:
			less++
		case v == current:
			equal++
		}
	}
	return (float64(less) + float64(equal+1)/2) / float64(len(w))
}

// centralMoments 返回二、三、四阶离差和（两遍法）；常数窗口返回 constant=true。
func centralMoments(w []float64) (m2, m3, m4 float64, constant bool) {
	m, constant := mean(w)
	if constant {
		return 0, 0, 0, true
	}
	n := len(w)
	m2 = exactSumOf(n, func(i int) float64 { d := w[i] - m; return d * d })
	m3 = exactSumOf(n, func(i int) float64 { d := w[i] - m; return d * d * d })
	m4 = exactSumOf(n, func(i int) float64 { d := w[i] - m; return d * d * d * d })
	return m2, m3, m4, false
}

// skew 是 pandas 的无偏偏度：sqrt(n(n−1)) · C / ((n−2) · B^1.5)，B、C 为二、三阶中心矩。
func skew(w []float64) float64 {
	m2, m3, _, constant := centralMoments(w)
	if constant || m2 == 0 {
		return nan
	}
	n := float64(len(w))
	b, c := m2/n, m3/n
	r := math.Sqrt(b)
	return math.Sqrt(n*(n-1)) * c / ((n - 2) * r * r * r)
}

// kurt 是 pandas 的无偏超额峰度：((n² − 1) · D / B² − 3(n − 1)²) / ((n − 2)(n − 3))。
func kurt(w []float64) float64 {
	m2, _, m4, constant := centralMoments(w)
	if constant || m2 == 0 {
		return nan
	}
	n := float64(len(w))
	b, d := m2/n, m4/n
	return ((n*n-1)*d/(b*b) - 3*(n-1)*(n-1)) / ((n - 2) * (n - 3))
}

// argExtreme 返回极值距今的根数（0 为最新一根），并列取最早的一根（n − 1 − np.argmax）。
func argExtreme(better func(a, b float64) bool) func(w []float64) float64 {
	return func(w []float64) float64 {
		best := 0
		for i, v := range w[1:] {
			if better(v, w[best]) {
				best = i + 1
			}
		}
		return float64(len(w) - 1 - best)
	}
}

// decayLinear 是线性衰减加权平均：最新一根权重为 n，最老一根为 1。
func decayLinear(n int) func(w []float64) float64 {
	total := float64(n) * float64(n+1) / 2
	return func(w []float64) float64 {
		return exactSumOf(n, func(i int) float64 { return float64(i+1) * w[i] }) / total
	}
}

type fit struct {
	slope, rsquare, resi float64
}

// regression 对时间序号 1..n 做最小二乘（Qlib 口径）：斜率、R² 与最新一根的残差。y 为常数时斜率、残差为 0，R² 为 NaN。
func regression(w []float64) fit {
	n := len(w)
	ym, constant := mean(w)
	xm := float64(n+1) / 2
	sxx := float64(n) * (float64(n)*float64(n) - 1) / 12
	if constant {
		return fit{slope: 0, rsquare: nan, resi: 0}
	}
	sxy := exactSumOf(n, func(i int) float64 { return (float64(i+1) - xm) * (w[i] - ym) })
	syy := exactSumOf(n, func(i int) float64 { d := w[i] - ym; return d * d })
	slope := sxy / sxx
	result := fit{slope: slope, resi: w[n-1] - (ym + slope*(float64(n)-xm))}
	if syy == 0 {
		result.rsquare = nan
	} else {
		result.rsquare = sxy * sxy / (sxx * syy)
	}
	return result
}

// covariance 是样本协方差（ddof=1），两遍法。
func covariance(x, y []float64) float64 {
	mx, _ := mean(x)
	my, _ := mean(y)
	sxy := exactSumOf(len(x), func(i int) float64 { return (x[i] - mx) * (y[i] - my) })
	return sxy / float64(len(x)-1)
}

// correlation 是皮尔逊相关系数；任一方为常数时为 NaN。
func correlation(x, y []float64) float64 {
	mx, cx := mean(x)
	my, cy := mean(y)
	if cx || cy {
		return nan
	}
	n := len(x)
	sxy := exactSumOf(n, func(i int) float64 { return (x[i] - mx) * (y[i] - my) })
	sxx := exactSumOf(n, func(i int) float64 { d := x[i] - mx; return d * d })
	syy := exactSumOf(n, func(i int) float64 { d := y[i] - my; return d * d })
	if sxx == 0 || syy == 0 {
		return nan
	}
	return sxy / math.Sqrt(sxx*syy)
}

// quantileLinear 是排好序的窗口的 q 分位数，线性插值（pandas interpolation="linear"）。
func quantileLinear(sorted []float64, q float64) float64 {
	position := q * float64(len(sorted)-1)
	lo := int(math.Floor(position))
	hi := int(math.Ceil(position))
	low, high := sorted[lo], sorted[hi]
	return low + (high-low)*(position-float64(lo))
}

// median 是排好序的窗口的中位数；偶数个时取中间两个的平均（pandas rolling median 的写法）。
func median(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// sortedWindow 维护窗口内非 NaN 值的有序多重集合，随输出点逐个移入、移出。分位数只取决于窗口内值的多重集合，
// 与从哪里开始维护无关，所以仍满足点纯性；窗口含 NaN 时输出 NaN。
func sortedWindow(n int, fn func(sorted []float64) float64) SeriesKernel {
	return func(out []float64, outStart int, in []Series) {
		x := in[0]
		sorted := make([]float64, 0, n)
		nans := 0
		add := func(v float64) {
			if math.IsNaN(v) {
				nans++
				return
			}
			v += 0 // 把 −0 归一成 +0，移出时按值查找才不会删错
			i := sort.SearchFloat64s(sorted, v)
			sorted = append(sorted, 0)
			copy(sorted[i+1:], sorted[i:])
			sorted[i] = v
		}
		remove := func(v float64) {
			if math.IsNaN(v) {
				nans--
				return
			}
			v += 0
			i := sort.SearchFloat64s(sorted, v)
			sorted = append(sorted[:i], sorted[i+1:]...)
		}
		// 第一个窗口一次性收集、排序（O(w log w)）；逐个插入在常数输入时每次都插在最前面，是 O(w²)。
		for t := outStart - n + 1; t <= outStart; t++ {
			if v := x.At(t); math.IsNaN(v) {
				nans++
			} else {
				sorted = append(sorted, v+0)
			}
		}
		sort.Float64s(sorted)
		for i := range out {
			if i > 0 {
				t := outStart + i
				remove(x.At(t - n))
				add(x.At(t))
			}
			if nans > 0 {
				out[i] = nan
				continue
			}
			out[i] = factorexpr.Finite(fn(sorted))
		}
	}
}

// truncatedSmoothing 是截断到 k 根的指数平滑（设计第 5 节）：
// y_t = (1−α)^(K−1)·x_{t−K+1} + Σ_{j=0}^{K−2} α(1−α)^j·x_{t−j}，等价于在 [t−K+1, t] 上以 x_{t−K+1} 为初值递推。
// 权重在准备内核时算好，求和从最老到最新、固定顺序。
func truncatedSmoothing(alpha float64, k int) SeriesKernel {
	weights := make([]float64, k) // weights[i] 对应窗口第 i 个值（0 为最老）
	decay := 1.0
	for j := 0; j < k-1; j++ { // j = 0 是最新一根
		weights[k-1-j] = alpha * decay
		decay *= 1 - alpha
	}
	weights[0] = decay // (1−α)^(K−1)
	return windowed(k, func(w []float64) float64 {
		return exactSumOf(k, func(i int) float64 { return weights[i] * w[i] })
	})
}
