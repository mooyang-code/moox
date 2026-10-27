package ops

import (
	"math"
	"math/bits"
)

// exactSum 返回 values 精确和的正确舍入（半数取偶）。
//
// 结果只取决于参与求和的数的多重集合，与顺序无关：内容相同、顺序轮换的两个窗口（例如移出与移入的都是 0）
// 求出的和逐位相同，本该相等的值不会因为舍入而打破并列（ts_rank、cs_rank 依赖这一点）；同时它不依赖从哪里开始累加，
// 满足点纯性（设计 5.1）。
//
// 项数少时用 Shewchuk 部分和（与 Python 的 math.fsum 相同），项数多时用定点累加器。项数多、量级跨度大时
// （例如回看 5 万根的截断 EMA，最老的权重小到 1e−200 以下）部分和会长到上百个，Shewchuk 每项都要逐个合并；
// 累加器每项开销固定。两种实现给出的都是精确和的正确舍入，结果逐位相同。
//
// 参与求和的项只会是有限值或 NaN（表达式引擎把 ±Inf 记为 NaN），任一项为 NaN 时结果为 NaN。
// 不处理的输入（行情数据不会出现）：累加超出 float64 范围；精确和为 0 时累加器一律返回 +0。
func exactSum(values []float64) float64 {
	if len(values) <= shewchukMaxTerms || len(values) > accMaxTerms {
		var stack [8]float64
		partials := stack[:0]
		for _, x := range values {
			partials = addPartial(partials, x)
		}
		return roundPartials(partials)
	}
	var acc accumulator
	acc.addAll(len(values), func(i int) float64 { return values[i] })
	return acc.round()
}

// exactSumOf 对 fn(0), …, fn(n−1) 做精确求和，按 0…n−1 的顺序逐项求值一次。
func exactSumOf(n int, fn func(i int) float64) float64 {
	if n <= shewchukMaxTerms || n > accMaxTerms {
		var stack [8]float64
		partials := stack[:0]
		for k := 0; k < n; k++ {
			partials = addPartial(partials, fn(k))
		}
		return roundPartials(partials)
	}
	var acc accumulator
	acc.addAll(n, fn)
	return acc.round()
}

// shewchukMaxTerms 是改用定点累加器的项数门槛：累加器每次求和有固定的清零与舍入开销（约几百纳秒），
// 项数少时 Shewchuk 更快；项数多时累加器每项开销固定，不随量级跨度增长。
const shewchukMaxTerms = 32

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

// 定点累加器：把每个有限 float64 写成整数 M × 2^(E−1074)（M < 2^53，E ∈ [0, 2045]），按 32 位一段累加到 int64 数组里，
// 精确和就是这个大整数乘以 2^−1074；最后规约进位、取最高 53 位并按半数取偶舍入。每项只是几次整数加法。
const (
	accChunkBits = 32
	accChunkMask = 1<<accChunkBits - 1
	// accChunks 覆盖最高位 2045 + 84 = 2129 位（第 66 段）再加进位余量。
	accChunks = 68
	// accMaxTerms：每段每次最多加入不到 2^32，int64 在 2^31 次之内不会溢出；超过这个项数（实际不会出现）只用部分和。
	accMaxTerms = 1 << 30
)

type accumulator struct {
	chunks [accChunks]int64
	nan    bool
}

// addAll 依次加入 fn(0), …, fn(n−1)。逐项的加入写在循环体里（单独成函数超出内联预算，每项多一次调用）。
// 0 也按同样的方式加入（各段加 0），不单独判断。
func (a *accumulator) addAll(n int, fn func(i int) float64) {
	for k := 0; k < n; k++ {
		b := math.Float64bits(fn(k))
		e := int(b>>52) & 0x7ff
		if e == 0x7ff { // NaN 或 ±Inf（引擎里 ±Inf 已记为 NaN）
			a.nan = true
			continue
		}
		m := b & (1<<52 - 1)
		if e != 0 { // 正规数 M × 2^(e−1075) = M × 2^((e−1)−1074)；0 与次正规数是 M × 2^−1074
			m |= 1 << 52
			e--
		}
		hi, lo := bits.Mul64(m, 1<<uint(e&31)) // M << (e mod 32)，最多 84 位
		sign := -int64(b >> 63)                // 负数为 −1，正数为 0：(v ^ sign) − sign 即按符号取反
		c := a.chunks[e>>5 : e>>5+3 : e>>5+3]
		c[0] += (int64(lo&accChunkMask) ^ sign) - sign
		c[1] += (int64(lo>>accChunkBits) ^ sign) - sign
		c[2] += (int64(hi) ^ sign) - sign
	}
}

// normalize 把每段规约到 [0, 2^32)，进位（可正可负）推到更高段；最高段带符号。
func (a *accumulator) normalize() {
	for i := 0; i < accChunks-1; i++ {
		carry := a.chunks[i] >> accChunkBits
		a.chunks[i] -= carry << accChunkBits
		a.chunks[i+1] += carry
	}
}

func (a *accumulator) round() float64 {
	if a.nan {
		return math.NaN()
	}
	a.normalize()
	negative := a.chunks[accChunks-1] < 0
	if negative {
		for i := range a.chunks {
			a.chunks[i] = -a.chunks[i]
		}
		a.normalize()
	}
	top := -1
	for i := accChunks - 1; i >= 0; i-- {
		if a.chunks[i] != 0 {
			top = i
			break
		}
	}
	if top < 0 {
		return 0
	}
	chunk := func(i int) uint64 {
		if i < 0 {
			return 0
		}
		return uint64(a.chunks[i])
	}
	var result float64
	if top <= 1 {
		// 整数小于 2^64：float64(uint64) 按半数取偶舍入；不足 2^53 时精确，乘 2^−1074 也精确（落在次正规范围时同样可表示）。
		result = math.Ldexp(float64(chunk(1)<<accChunkBits|chunk(0)), -1074)
	} else {
		// 取最高四段组成 128 位窗口，窗口以下任何非零段都计入粘滞位。
		hi := chunk(top)<<accChunkBits | chunk(top-1)
		lo := chunk(top-2)<<accChunkBits | chunk(top-3)
		sticky := false
		for i := top - 4; i >= 0; i-- {
			if a.chunks[i] != 0 {
				sticky = true
				break
			}
		}
		length := 64 + bits.Len64(hi) // 窗口里有效位数（hi ≥ 2^32，至少 97 位）
		drop := uint(length - 54)     // 截到 54 位：53 位尾数加 1 位舍入位
		var kept uint64
		if drop >= 64 {
			kept = hi >> (drop - 64)
			if hi&(1<<(drop-64)-1) != 0 || lo != 0 {
				sticky = true
			}
		} else {
			kept = hi<<(64-drop) | lo>>drop
			if lo&(1<<drop-1) != 0 {
				sticky = true
			}
		}
		mantissa, guard := kept>>1, kept&1
		if guard == 1 && (sticky || mantissa&1 == 1) {
			mantissa++
		}
		// 尾数最低位在大整数里的位序：窗口起点（第 top−3 段）加上截掉的位数再加舍入位。
		lsb := (top-3)*accChunkBits + int(drop) + 1
		result = math.Ldexp(float64(mantissa), lsb-1074)
	}
	if negative {
		return -result
	}
	return result
}
