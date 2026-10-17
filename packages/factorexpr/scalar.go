package factorexpr

import "math"

// 标量语义（设计 4.4）：任一输入为 NaN 结果为 NaN；产生 ±Inf 时记为 NaN；布尔值用 1、0 表示，NaN 表示未知。
// 常量折叠与逐元素内核共用这些函数，保证编译期与运行期的结果一致。

var nan = math.NaN()

// Finite 把 ±Inf 记为 NaN。
func Finite(value float64) float64 {
	if math.IsInf(value, 0) {
		return nan
	}
	return value
}

func boolValue(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

// ScalarFunc 是逐元素算子的标量实现。
type ScalarFunc func(args []float64) float64

func compare(test func(a, b float64) bool) ScalarFunc {
	return func(args []float64) float64 {
		if math.IsNaN(args[0]) || math.IsNaN(args[1]) {
			return nan
		}
		return boolValue(test(args[0], args[1]))
	}
}

func unary(fn func(float64) float64) ScalarFunc {
	return func(args []float64) float64 {
		if math.IsNaN(args[0]) {
			return nan
		}
		return Finite(fn(args[0]))
	}
}

func binary(fn func(a, b float64) float64) ScalarFunc {
	return func(args []float64) float64 {
		if math.IsNaN(args[0]) || math.IsNaN(args[1]) {
			return nan
		}
		return Finite(fn(args[0], args[1]))
	}
}

var scalarFuncs = map[string]ScalarFunc{
	"add": binary(func(a, b float64) float64 { return a + b }),
	"sub": binary(func(a, b float64) float64 { return a - b }),
	"mul": binary(func(a, b float64) float64 { return a * b }),
	"div": binary(func(a, b float64) float64 {
		if b == 0 {
			return nan
		}
		return a / b
	}),
	"mod": binary(func(a, b float64) float64 {
		if b == 0 {
			return nan
		}
		return math.Mod(a, b)
	}),
	"pow":   binary(math.Pow),
	"power": binary(math.Pow),
	"neg":   unary(func(a float64) float64 { return -a }),
	"abs":   unary(math.Abs),
	"sign": unary(func(a float64) float64 {
		switch {
		case a > 0:
			return 1
		case a < 0:
			return -1
		}
		return 0
	}),
	"sqrt": unary(func(a float64) float64 {
		if a < 0 {
			return nan
		}
		return math.Sqrt(a)
	}),
	"exp": unary(math.Exp),
	"log": unary(func(a float64) float64 {
		if a <= 0 {
			return nan
		}
		return math.Log(a)
	}),
	"signed_power": binary(func(a, e float64) float64 {
		if a == 0 {
			return 0
		}
		magnitude := math.Pow(math.Abs(a), e)
		if a < 0 {
			return -magnitude
		}
		return magnitude
	}),
	"max": binary(math.Max),
	"min": binary(math.Min),
	"clip": func(args []float64) float64 {
		x, lo, hi := args[0], args[1], args[2]
		if math.IsNaN(x) || math.IsNaN(lo) || math.IsNaN(hi) || lo > hi {
			return nan
		}
		return math.Min(math.Max(x, lo), hi)
	},
	"where": func(args []float64) float64 {
		switch {
		case math.IsNaN(args[0]):
			return nan
		case args[0] != 0:
			return args[1]
		}
		return args[2]
	},
	"fill_nan": func(args []float64) float64 {
		if math.IsNaN(args[0]) {
			return args[1]
		}
		return args[0]
	},
	"is_nan": func(args []float64) float64 { return boolValue(math.IsNaN(args[0])) },
	"not": func(args []float64) float64 {
		if math.IsNaN(args[0]) {
			return nan
		}
		return boolValue(args[0] == 0)
	},
	"eq": compare(func(a, b float64) bool { return a == b }),
	"ne": compare(func(a, b float64) bool { return a != b }),
	"lt": compare(func(a, b float64) bool { return a < b }),
	"le": compare(func(a, b float64) bool { return a <= b }),
	"gt": compare(func(a, b float64) bool { return a > b }),
	"ge": compare(func(a, b float64) bool { return a >= b }),
	// 严格的三值逻辑（设计 4.4 第 4 条）：任一操作数为 NaN，结果就是 NaN，不做短路。
	"and": compare(func(a, b float64) bool { return a != 0 && b != 0 }),
	"or":  compare(func(a, b float64) bool { return a != 0 || b != 0 }),
}

// Scalar 返回逐元素算子的标量实现；不是逐元素算子时返回 nil。
func Scalar(op string) ScalarFunc { return scalarFuncs[op] }

// integerPreserving 是整数输入时结果仍为整数的算子，用于判断折叠后的常量能否作为窗口参数。
var integerPreserving = map[string]bool{
	"add": true, "sub": true, "mul": true, "mod": true, "neg": true, "abs": true, "max": true, "min": true, "sign": true,
}
