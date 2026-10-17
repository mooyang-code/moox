package ops

import "math"

// exactSum 返回 values 精确和的正确舍入（Shewchuk 算法，与 Python 的 math.fsum 相同）。
//
// 结果只取决于参与求和的数的多重集合，与顺序无关：内容相同、顺序轮换的两个窗口（例如移出与移入的都是 0）
// 求出的和逐位相同，本该相等的值不会因为舍入而打破并列（ts_rank、cs_rank 依赖这一点）；同时它不依赖从哪里开始累加，
// 满足点纯性（设计 5.1）。
func exactSum(values []float64) float64 {
	var stack [8]float64
	partials := stack[:0]
	for _, x := range values {
		partials = addPartial(partials, x)
	}
	return roundPartials(partials)
}

// exactSumOf 对 fn(0), …, fn(n−1) 做精确求和。
func exactSumOf(n int, fn func(i int) float64) float64 {
	var stack [8]float64
	partials := stack[:0]
	for k := 0; k < n; k++ {
		partials = addPartial(partials, fn(k))
	}
	return roundPartials(partials)
}

// addPartial 把 x 并入部分和：partials 互不重叠、按绝对值递增，它们的精确和等于已加入的所有数的精确和。
func addPartial(partials []float64, x float64) []float64 {
	i := 0
	for _, y := range partials {
		if math.Abs(x) < math.Abs(y) {
			x, y = y, x
		}
		hi := x + y
		lo := y - (hi - x)
		if lo != 0 {
			partials[i] = lo
			i++
		}
		x = hi
	}
	return append(partials[:i], x)
}

// roundPartials 把部分和合成正确舍入的结果（CPython math_fsum 的末段，含“半数取偶”的修正）。
func roundPartials(partials []float64) float64 {
	n := len(partials)
	if n == 0 {
		return 0
	}
	n--
	hi := partials[n]
	lo := 0.0
	for n > 0 {
		x := hi
		n--
		y := partials[n]
		hi = x + y
		lo = y - (hi - x)
		if lo != 0 {
			break
		}
	}
	if n > 0 && ((lo < 0 && partials[n-1] < 0) || (lo > 0 && partials[n-1] > 0)) {
		y := lo * 2
		x := hi + y
		if y == x-hi {
			hi = x
		}
	}
	return hi
}
