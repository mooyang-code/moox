package factorexpr

import (
	"math"
	"sort"
	"strconv"
)

// Category 是算子的类别，决定计算方式：逐元素、按序列滚动、按截面。
type Category int

const (
	CategoryElementwise Category = iota + 1
	CategoryTimeSeries
	CategoryCrossSection
)

func (c Category) String() string {
	switch c {
	case CategoryElementwise:
		return "elementwise"
	case CategoryTimeSeries:
		return "timeseries"
	case CategoryCrossSection:
		return "cross_section"
	}
	return "unknown"
}

// ArgKind 是参数要求的类型。
type ArgKind int

const (
	// ArgNumber 是数值序列或数值常量（常量按标量广播）。
	ArgNumber ArgKind = iota + 1
	// ArgSeries 是数值序列，必须依赖输入列。
	ArgSeries
	// ArgBool 是布尔序列或布尔常量。
	ArgBool
	// ArgBoolSeries 是布尔序列，必须依赖输入列。
	ArgBoolSeries
	// ArgWindow 是窗口长度：编译期整数常量。
	ArgWindow
	// ArgInteger 是位移、平滑参数等：编译期整数常量。
	ArgInteger
	// ArgConstant 是分位数等：编译期数值常量。
	ArgConstant
	// ArgSubject 是标的 ID：只能写字符串字面量（cs_ref 的参考标的），表达式里其他位置都不允许字符串。
	ArgSubject
	// ArgGroup 是分组键：数值序列，必须依赖输入列（例如行业代码列）。截面里按它的取值分组，NaN 的行不属于任何组。
	ArgGroup
)

// ArgSpec 描述一个参数。Min、Max 只对编译期常量参数有效（闭区间）。
type ArgSpec struct {
	Name     string
	Kind     ArgKind
	Min      float64
	Max      float64
	Optional bool
	Default  float64
	// LiteralOnly 为真表示这个编译期常量位置只收字面量，不收参数（截面算子的常量要对整个截面相同）。
	LiteralOnly bool
}

// Function 是注册表中的一个算子（函数或运算符）。
type Function struct {
	Name     string
	Category Category
	Args     []ArgSpec
	// Result 是结果类型：KindNumber 或 KindBool。
	Result KindResult
	// Lookback 是回看规则的说明，例如 “L(x)+n−1”。
	Lookback string
	Doc      string
	Example  string
	// Variadic 大于 0 表示变参：最后一个参数可以重复 1～Variadic 次（它是可选参数时也可以不写），各次的名字是参数名加序号。
	Variadic int
	// Broadcast 为真表示聚合广播类截面算子（Category 仍是 CategoryCrossSection）：每个截面算出一个统计量，
	// 写给截面里的每一行，包括输入为 NaN 的行；其余截面算子是逐行变换类，输入为 NaN 的行输出 NaN。
	Broadcast bool
	// back 返回在子节点回看之上额外需要的行数（同时决定需求区间向前扩展多少），shift 返回需求区间整体向前平移的行数
	// （只有 delay 不为 0）。参数是整数常量参数，按 Args 中出现的顺序排列。
	back  func(ints []int) int
	shift func(ints []int) int
	// validate 做参数之间的联合校验（例如 sma 的 m < n）。
	validate func(ints []int, floats []float64) string
	operator bool
	// udf 非空表示这是自定义算子（只存在于一次编译的环境里，不在全局注册表中）。
	udf *UDF
}

// KindResult 是算子结果的类型。
type KindResult int

const (
	ResultNumber KindResult = iota + 1
	ResultBool
)

// Back 返回整数常量参数为 ints 时，该算子在子节点回看之上额外需要的行数。
func (f *Function) Back(ints []int) int {
	if f == nil || f.back == nil {
		return 0
	}
	return f.back(ints)
}

// Shift 返回需求区间整体向前平移的行数。
func (f *Function) Shift(ints []int) int {
	if f == nil || f.shift == nil {
		return 0
	}
	return f.shift(ints)
}

// Validate 做参数之间的联合校验，返回不满足时的原因；没有联合约束或满足时返回空串。
func (f *Function) Validate(ints []int, floats []float64) string {
	if f == nil || f.validate == nil {
		return ""
	}
	return f.validate(ints, floats)
}

// ArgCount 返回参数个数的下限与上限。
func (f *Function) ArgCount() (int, int) {
	required := 0
	for _, arg := range f.Args {
		if !arg.Optional {
			required++
		}
	}
	if f.Variadic > 0 {
		return required, len(f.Args) - 1 + f.Variadic
	}
	return required, len(f.Args)
}

// argSpecs 返回写了 count 个参数时每个位置的参数说明：变参的最后一个参数按次数展开，名字加序号（x1、x2…）。
// 不是变参时就是 Args。
func (f *Function) argSpecs(count int) []ArgSpec {
	if f.Variadic == 0 {
		return f.Args
	}
	specs := append([]ArgSpec(nil), f.Args[:len(f.Args)-1]...)
	last := f.Args[len(f.Args)-1]
	for i := 1; len(specs) < count; i++ {
		spec := last
		spec.Name = last.Name + strconv.Itoa(i)
		spec.Optional = false
		specs = append(specs, spec)
	}
	return specs
}

// GroupArg 返回分组键在节点序列参数（ProgramNode.Args）中的位置；没有分组键时为 −1。
// 截面算子的序列参数都写在编译期常量与标的字面量之前，所以它就是分组键之前的参数个数。
func (f *Function) GroupArg() int {
	if f == nil {
		return -1
	}
	for i, arg := range f.Args {
		if arg.Kind == ArgGroup {
			return i
		}
	}
	return -1
}

// Operator 报告它是否为运算符（只能通过运算符书写，不能按名字调用）。
func (f *Function) Operator() bool { return f != nil && f.operator }

const (
	// recursiveTolerance 是递归算子截断时被截掉部分的权重上限。
	recursiveTolerance = 1e-9
)

// RecursiveSpan 返回平滑系数为 alpha 的递归算子截断后的窗口长度 K：让起点值的权重衰减到 1e−9 以下所需的根数。
func RecursiveSpan(alpha float64) int {
	if alpha >= 1 {
		return 1
	}
	return int(math.Ceil(math.Log(recursiveTolerance) / math.Log(1-alpha)))
}

// EMAAlpha 返回 ema(x, n) 的平滑系数。
func EMAAlpha(n int) float64 { return 2 / float64(n+1) }

// SMAAlpha 返回 sma(x, n, m) 的平滑系数。
func SMAAlpha(n, m int) float64 { return float64(m) / float64(n) }

func windowBack(ints []int) int { return ints[0] - 1 }
func shiftBack(ints []int) int  { return ints[0] }
func emaBack(ints []int) int {
	if ints[1] > 0 {
		return ints[1] - 1
	}
	return RecursiveSpan(EMAAlpha(ints[0])) - 1
}
func smaBack(ints []int) int { return RecursiveSpan(SMAAlpha(ints[0], ints[1])) - 1 }

var (
	argX      = ArgSpec{Name: "x", Kind: ArgNumber}
	argSeries = ArgSpec{Name: "x", Kind: ArgSeries}
	argGroup  = ArgSpec{Name: "g", Kind: ArgGroup}
)

// chipWindow 是筹码分布算子的窗口：每个输出点都要重算整个窗口，所以上限比通用窗口小得多。
var chipWindow = ArgSpec{Name: "n", Kind: ArgWindow, Min: 20, Max: 500}

// chipSeries 是筹码分布算子的五个序列参数。
func chipSeries() []ArgSpec {
	return []ArgSpec{{Name: "open", Kind: ArgSeries}, {Name: "high", Kind: ArgSeries}, {Name: "low", Kind: ArgSeries},
		{Name: "close", Kind: ArgSeries}, {Name: "turnover", Kind: ArgSeries}}
}

func window(min int) ArgSpec {
	return ArgSpec{Name: "n", Kind: ArgWindow, Min: float64(min), Max: MaxWindow}
}

func elementwise(name string, args []ArgSpec, result KindResult, doc, example string) *Function {
	return &Function{Name: name, Category: CategoryElementwise, Args: args, Result: result,
		Lookback: "各参数回看的最大值", Doc: doc, Example: example}
}

func rolling(name string, args []ArgSpec, doc, example string) *Function {
	return &Function{Name: name, Category: CategoryTimeSeries, Args: args, Result: ResultNumber,
		Lookback: "L(x)+n−1", Doc: doc, Example: example, back: func(ints []int) int { return ints[len(ints)-1] - 1 }}
}

// relaxedWindow 登记 (x, n, m) 形式的宽松窗口算子：回看按 n 推导，m 是序列开头最少的有效值个数。
func relaxedWindow(name string, minimum int, doc, example string) *Function {
	return relaxed(name, []ArgSpec{argSeries}, minimum, nil, doc, example)
}

// relaxed 登记宽松窗口算子，参数依次是 series、窗口 n、consts（例如 ts_quantile_relaxed 的 q）与 m。m 的下限沿用严格版本
// 对 n 的下限（minimum），且不能大于 n。整数参数依次为 [n, m]。
func relaxed(name string, series []ArgSpec, minimum int, consts []ArgSpec, doc, example string) *Function {
	args := append(append([]ArgSpec(nil), series...), window(minimum))
	args = append(append(args, consts...), ArgSpec{Name: "m", Kind: ArgInteger, Min: float64(minimum), Max: MaxWindow})
	return &Function{Name: name, Category: CategoryTimeSeries, Args: args,
		Result: ResultNumber, Lookback: "L(x)+n−1", Doc: doc, Example: example,
		back: func(ints []int) int { return ints[0] - 1 },
		validate: func(ints []int, _ []float64) string {
			if ints[1] > ints[0] {
				return name + " 的 m 不能大于 n"
			}
			return ""
		}}
}

func cross(name, doc, example string) *Function {
	return &Function{Name: name, Category: CategoryCrossSection, Args: []ArgSpec{argSeries}, Result: ResultNumber,
		Lookback: "L(x)", Doc: doc, Example: example}
}

// withGroup 在截面算子的参数末尾加上分组键 g。
func (f *Function) withGroup() *Function {
	f.Args = append(append([]ArgSpec(nil), f.Args...), argGroup)
	f.Lookback = crossLookback(f.Args)
	return f
}

// crossLookback 是截面算子的回看说明：只有一个序列参数时为 L(x)，多个时（含分组键）取各参数回看的最大值。
func crossLookback(args []ArgSpec) string {
	series := 0
	for _, arg := range args {
		if arg.Kind == ArgSeries || arg.Kind == ArgBoolSeries || arg.Kind == ArgGroup {
			series++
		}
	}
	if series > 1 {
		return "各参数回看的最大值"
	}
	return "L(x)"
}

// aggregate 登记聚合广播类截面算子：有效样本（非 NaN）上算出一个统计量，广播到同一截面（同一 data_time、同一 series_tag、
// 成员标的范围之内）的每一行；没有有效样本时整个截面为 NaN。
func aggregate(name string, args []ArgSpec, doc, example string) *Function {
	return &Function{Name: name, Category: CategoryCrossSection, Args: args, Result: ResultNumber, Broadcast: true,
		Lookback: crossLookback(args), Doc: doc, Example: example}
}

var functionList = []*Function{
	// 逐元素
	elementwise("abs", []ArgSpec{argX}, ResultNumber, "绝对值", "abs(close - open)"),
	elementwise("sign", []ArgSpec{argX}, ResultNumber, "符号：正数为 1，负数为 −1，0 为 0", "sign(delta(close, 1))"),
	elementwise("sqrt", []ArgSpec{argX}, ResultNumber, "平方根；x < 0 时为 NaN", "sqrt(volume)"),
	elementwise("exp", []ArgSpec{argX}, ResultNumber, "自然指数", "exp(x)"),
	elementwise("log", []ArgSpec{argX}, ResultNumber, "自然对数；x ≤ 0 时为 NaN", "log(close)"),
	elementwise("power", []ArgSpec{argX, {Name: "y", Kind: ArgNumber}}, ResultNumber,
		"x 的 y 次方；底数为负且指数不是整数时为 NaN", "power(close, 2)"),
	elementwise("signed_power", []ArgSpec{argX, {Name: "e", Kind: ArgNumber}}, ResultNumber,
		"sign(x) · |x|^e", "signed_power(pct_change(close), 0.5)"),
	elementwise("max", []ArgSpec{{Name: "a", Kind: ArgNumber}, {Name: "b", Kind: ArgNumber}}, ResultNumber,
		"二元取大；任一为 NaN 时为 NaN", "max(high - low, abs(close - open))"),
	elementwise("min", []ArgSpec{{Name: "a", Kind: ArgNumber}, {Name: "b", Kind: ArgNumber}}, ResultNumber,
		"二元取小；任一为 NaN 时为 NaN", "min(open, close)"),
	elementwise("clip", []ArgSpec{argX, {Name: "lo", Kind: ArgNumber}, {Name: "hi", Kind: ArgNumber}}, ResultNumber,
		"截断到 [lo, hi]；lo > hi 时为 NaN", "clip(ts_zscore(close, 20), -3, 3)"),
	elementwise("where", []ArgSpec{{Name: "c", Kind: ArgBool}, {Name: "a", Kind: ArgNumber}, {Name: "b", Kind: ArgNumber}}, ResultNumber,
		"条件选择：c 为真取 a，为假取 b；c 为 NaN 时为 NaN。等价于 c ? a : b", "where(close > open, 1, 0)"),
	elementwise("fill_nan", []ArgSpec{argX, {Name: "v", Kind: ArgNumber}}, ResultNumber,
		"x 为 NaN 时取 v", "fill_nan(pct_change(close), 0)"),
	elementwise("is_nan", []ArgSpec{argX}, ResultBool, "x 是否为 NaN（结果不会是 NaN）", "where(is_nan(x), 0, x)"),
	elementwise("floor", []ArgSpec{argX}, ResultNumber, "向下取整", "floor(close)"),
	elementwise("ceil", []ArgSpec{argX}, ResultNumber, "向上取整", "ceil(volume / 100)"),
	elementwise("round", []ArgSpec{argX}, ResultNumber, "四舍五入到整数，0.5 远离零取整（2.5 → 3，−2.5 → −3）", "round(close)"),

	// 时序：位移
	{Name: "delay", Category: CategoryTimeSeries, Args: []ArgSpec{argSeries, {Name: "k", Kind: ArgInteger, Min: 1, Max: MaxWindow}},
		Result: ResultNumber, Lookback: "L(x)+k", Doc: "k 期前的值", Example: "delay(close, 1)", back: shiftBack, shift: shiftBack},
	{Name: "delta", Category: CategoryTimeSeries, Args: []ArgSpec{argSeries, {Name: "k", Kind: ArgInteger, Min: 1, Max: MaxWindow}},
		Result: ResultNumber, Lookback: "L(x)+k", Doc: "x − delay(x, k)", Example: "delta(close, 5)", back: shiftBack},
	{Name: "pct_change", Category: CategoryTimeSeries,
		Args:   []ArgSpec{argSeries, {Name: "k", Kind: ArgInteger, Min: 1, Max: MaxWindow, Optional: true, Default: 1}},
		Result: ResultNumber, Lookback: "L(x)+k", Doc: "x / delay(x, k) − 1；分母为 0 时为 NaN", Example: "pct_change(close)", back: shiftBack},

	// 时序：滚动窗口
	rolling("ts_sum", []ArgSpec{argSeries, window(1)}, "窗口内求和", "ts_sum(volume, 20)"),
	rolling("ts_mean", []ArgSpec{argSeries, window(1)}, "窗口内均值", "ts_mean(close, 20)"),
	rolling("ts_min", []ArgSpec{argSeries, window(1)}, "窗口内最小值", "ts_min(low, 20)"),
	rolling("ts_max", []ArgSpec{argSeries, window(1)}, "窗口内最大值", "ts_max(high, 20)"),
	rolling("ts_product", []ArgSpec{argSeries, window(1)}, "窗口内连乘", "ts_product(1 + pct_change(close), 5)"),
	rolling("ts_std", []ArgSpec{argSeries, window(2)}, "窗口内样本标准差（ddof=1）", "ts_std(close, 20)"),
	rolling("ts_var", []ArgSpec{argSeries, window(2)}, "窗口内样本方差（ddof=1）", "ts_var(close, 20)"),
	rolling("ts_zscore", []ArgSpec{argSeries, window(2)}, "(x − ts_mean) / ts_std；标准差为 0 时为 NaN", "ts_zscore(close, 20)"),
	rolling("ts_rank", []ArgSpec{argSeries, window(1)}, "当期值在窗口内的百分位，并列取平均名次（pandas rank(pct=True)）", "ts_rank(close, 20)"),
	{Name: "ts_quantile", Category: CategoryTimeSeries,
		Args:   []ArgSpec{argSeries, window(1), {Name: "q", Kind: ArgConstant, Min: 0, Max: 1}},
		Result: ResultNumber, Lookback: "L(x)+n−1", Doc: "窗口内 q 分位数，线性插值", Example: "ts_quantile(close, 20, 0.9)", back: windowBack},
	rolling("ts_median", []ArgSpec{argSeries, window(1)}, "窗口内中位数", "ts_median(volume, 20)"),
	rolling("ts_skew", []ArgSpec{argSeries, window(3)}, "窗口内无偏偏度", "ts_skew(pct_change(close), 20)"),
	rolling("ts_kurt", []ArgSpec{argSeries, window(4)}, "窗口内无偏超额峰度", "ts_kurt(pct_change(close), 20)"),
	rolling("ts_argmax", []ArgSpec{argSeries, window(1)}, "窗口内最大值距今的根数（0 为当期，并列取最早的一根）", "ts_argmax(high, 20)"),
	rolling("ts_argmin", []ArgSpec{argSeries, window(1)}, "窗口内最小值距今的根数（0 为当期，并列取最早的一根）", "ts_argmin(low, 20)"),
	rolling("ts_corr", []ArgSpec{{Name: "x", Kind: ArgSeries}, {Name: "y", Kind: ArgSeries}, window(2)},
		"窗口内相关系数；任一方标准差为 0 时为 NaN", "ts_corr(close, volume, 20)"),
	rolling("ts_cov", []ArgSpec{{Name: "x", Kind: ArgSeries}, {Name: "y", Kind: ArgSeries}, window(2)},
		"窗口内样本协方差（ddof=1）", "ts_cov(close, volume, 20)"),
	rolling("ts_decay_linear", []ArgSpec{argSeries, window(1)}, "线性衰减加权平均：最新一根权重为 n，最老一根为 1", "ts_decay_linear(close, 10)"),
	rolling("ts_count", []ArgSpec{{Name: "c", Kind: ArgBoolSeries}, window(1)}, "窗口内条件为真的个数", "ts_count(close > open, 20)"),
	rolling("ts_slope", []ArgSpec{{Name: "y", Kind: ArgSeries}, window(2)}, "对时间序号 1..n 做最小二乘的斜率", "ts_slope(close, 20)"),
	rolling("ts_rsquare", []ArgSpec{{Name: "y", Kind: ArgSeries}, window(2)}, "对时间序号 1..n 做最小二乘的 R²", "ts_rsquare(close, 20)"),
	rolling("ts_resi", []ArgSpec{{Name: "y", Kind: ArgSeries}, window(2)}, "对时间序号 1..n 做最小二乘后当期的残差", "ts_resi(close, 20)"),

	// 时序：窗口内事件记忆（只在最近 n 根里回看，窗口里没有事件时为 NaN）
	rolling("ts_last_valid", []ArgSpec{argSeries, window(1)},
		"最近 n 根（含当根）里最后一个非 NaN 的 x；没有为 NaN。只有最近一次有效值落在窗口内时，才等于无限期前向填充",
		"fill_nan(ts_last_valid(where(close > open, 1, where(close < open, 0, nan)), 50), 0)"),
	rolling("ts_bars_since", []ArgSpec{{Name: "c", Kind: ArgBoolSeries}, window(1)},
		"最近 n 根里 c 最后一次为真距今的根数，当根为真记 0；c 为 NaN 不算为真；没有为 NaN", "ts_bars_since(close > ts_max(high, 20) * 0.99, 100)"),
	rolling("ts_value_when", []ArgSpec{{Name: "c", Kind: ArgBoolSeries}, {Name: "x", Kind: ArgSeries}, window(1)},
		"最近 n 根里 c 最后一次为真那一根的 x；没有为 NaN", "ts_value_when(volume > ts_mean(volume, 20) * 3, close, 100)"),

	// 时序：筹码分布（用最近 n 根的开高低收与换手率推算持仓成本的分布，价格要用复权价）
	{Name: "chip_profit", Category: CategoryTimeSeries, Args: append(chipSeries(), chipWindow),
		Result: ResultNumber, Lookback: "各参数回看的最大值+n−1", back: windowBack,
		Doc:     "筹码分布的获利比例（0～1）：最近 n 根推算出的持仓里，成本不高于当根收盘价的占比。turnover 是换手率（百分比）；价格用复权价；窗口内价格没有波动或全程没有换手时为 NaN",
		Example: "chip_profit(open, high, low, close, turnover_rate, 120)"},
	{Name: "chip_cost", Category: CategoryTimeSeries,
		Args:   append(chipSeries(), chipWindow, ArgSpec{Name: "q", Kind: ArgConstant, Min: 0.01, Max: 0.99}),
		Result: ResultNumber, Lookback: "各参数回看的最大值+n−1", back: windowBack,
		Doc:     "筹码分布的分位成本价：从低价往上累计到全部筹码的 q 时的价格，q=0.5 是平均成本。参数与口径同 chip_profit，结果与输入价格同一复权口径",
		Example: "chip_cost(open, high, low, close, turnover_rate, 120, 0.5)"},

	// 时序：递归平滑（截断到 K 根）
	{Name: "ema", Category: CategoryTimeSeries,
		Args:   []ArgSpec{argSeries, window(1), {Name: "k", Kind: ArgInteger, Min: 0, Max: MaxWindow, Optional: true, Default: 0}},
		Result: ResultNumber, Lookback: "L(x)+K−1，K 为 k；k 不写或为 0 时为让起点权重衰减到 1e−9 以下的根数",
		Doc:     "指数移动平均，α = 2/(n+1)，截断到最近 K 根（ewm(span=n, adjust=False)），窗口里最老的一根收盘价作为递推的初始值；可选的 k 显式指定预热总根数 K（含起点）：ema(close, 20, 50000) 是 EMA20 用 5 万根预热；k 不写或为 0 时由 n 推出；n 远大于 k 时起点权重不可忽略，值是近似的",
		Example: "ema(close, 20)", back: emaBack},
	{Name: "sma", Category: CategoryTimeSeries,
		Args:   []ArgSpec{argSeries, {Name: "n", Kind: ArgInteger, Min: 2, Max: MaxWindow}, {Name: "m", Kind: ArgInteger, Min: 1, Max: MaxWindow}},
		Result: ResultNumber, Lookback: "L(x)+K−1，K 为让起点权重衰减到 1e−9 以下的根数",
		Doc: "国君 SMA：y = (m·x + (n−m)·y₋₁) / n，截断到最近 K 根", Example: "sma(close, 20, 1)", back: smaBack,
		validate: func(ints []int, _ []float64) string {
			if ints[1] >= ints[0] {
				return "sma 的 m 必须小于 n"
			}
			return ""
		}},

	// 时序：宽松窗口（序列开头窗口没攒满也出值；对应 pandas 的 min_periods 与 ewm 从首根递推；中途出现 NaN 时仍输出 NaN）。
	// 只用于与外部因子在上市初期逐值对齐：开头的值只用很少的数据算出，噪声大，新写的因子优先用严格算子。
	{Name: "ema_relaxed", Category: CategoryTimeSeries,
		Args:   []ArgSpec{argSeries, window(1), {Name: "k", Kind: ArgInteger, Min: 0, Max: MaxWindow, Optional: true, Default: 0}},
		Result: ResultNumber, Lookback: "同 ema：L(x)+K−1",
		Doc:     "同 ema(x, n, k)；窗口开头是 NaN（序列还没开始）时，从第一个有效值开始递推，相当于 pandas ewm(span=n, adjust=False) 从序列首根算起；有效值之后再出现 NaN 时为 NaN。宽松窗口算子只用于与外部因子逐值对齐，新写因子优先用 ema",
		Example: "ema_relaxed(close, 20)", back: emaBack},
	relaxedWindow("ts_mean_relaxed", 1, "窗口内均值；窗口开头是 NaN（序列还没开始）时，在不少于 m 个有效值上计算，相当于 pandas rolling(n, min_periods=m)；有效值之后再出现 NaN 时为 NaN。宽松窗口算子只用于与外部因子逐值对齐，开头的值噪声大，新写因子优先用 ts_mean", "ts_mean_relaxed(close, 20, 1)"),
	relaxedWindow("ts_std_relaxed", 2, "窗口内样本标准差（ddof=1）；序列开头的宽松窗口规则同 ts_mean_relaxed，m ≥ 2", "ts_std_relaxed(close, 20, 2)"),
	relaxedWindow("ts_min_relaxed", 1, "窗口内最小值；序列开头的宽松窗口规则同 ts_mean_relaxed", "ts_min_relaxed(low, 20, 1)"),
	relaxedWindow("ts_max_relaxed", 1, "窗口内最大值；序列开头的宽松窗口规则同 ts_mean_relaxed", "ts_max_relaxed(high, 20, 1)"),
	relaxedWindow("ts_sum_relaxed", 1, "窗口内求和；序列开头的宽松窗口规则同 ts_mean_relaxed", "ts_sum_relaxed(volume, 20, 1)"),
	relaxed("ts_count_relaxed", []ArgSpec{{Name: "c", Kind: ArgBoolSeries}}, 1, nil,
		"窗口内条件为真的个数；序列开头的宽松窗口规则同 ts_mean_relaxed", "ts_count_relaxed(close > open, 20, 5)"),
	relaxedWindow("ts_median_relaxed", 1, "窗口内中位数；序列开头的宽松窗口规则同 ts_mean_relaxed", "ts_median_relaxed(volume, 20, 1)"),
	relaxed("ts_quantile_relaxed", []ArgSpec{argSeries}, 1, []ArgSpec{{Name: "q", Kind: ArgConstant, Min: 0, Max: 1}},
		"窗口内 q 分位数，线性插值；序列开头的宽松窗口规则同 ts_mean_relaxed", "ts_quantile_relaxed(close, 20, 0.9, 5)"),
	relaxedWindow("ts_rank_relaxed", 1, "当期值在窗口内的百分位，分母是窗口内的有效个数（pandas rolling(n, min_periods=m).rank(pct=True)）；序列开头的宽松窗口规则同 ts_mean_relaxed",
		"ts_rank_relaxed(close, 20, 5)"),
	relaxedWindow("ts_var_relaxed", 2, "窗口内样本方差（ddof=1）；序列开头的宽松窗口规则同 ts_mean_relaxed，m ≥ 2", "ts_var_relaxed(close, 20, 2)"),
	relaxedWindow("ts_zscore_relaxed", 2, "(x − ts_mean) / ts_std；序列开头的宽松窗口规则同 ts_mean_relaxed，m ≥ 2", "ts_zscore_relaxed(close, 20, 5)"),
	relaxedWindow("ts_skew_relaxed", 3, "窗口内无偏偏度；序列开头的宽松窗口规则同 ts_mean_relaxed，m ≥ 3", "ts_skew_relaxed(pct_change(close), 20, 5)"),
	relaxedWindow("ts_kurt_relaxed", 4, "窗口内无偏超额峰度；序列开头的宽松窗口规则同 ts_mean_relaxed，m ≥ 4", "ts_kurt_relaxed(pct_change(close), 20, 5)"),
	relaxedWindow("ts_argmax_relaxed", 1, "窗口内最大值距今的根数（0 为当期，并列取最早的一根）；序列开头的宽松窗口规则同 ts_mean_relaxed", "ts_argmax_relaxed(high, 20, 1)"),
	relaxedWindow("ts_argmin_relaxed", 1, "窗口内最小值距今的根数（0 为当期，并列取最早的一根）；序列开头的宽松窗口规则同 ts_mean_relaxed", "ts_argmin_relaxed(low, 20, 1)"),
	relaxedWindow("ts_product_relaxed", 1, "窗口内连乘；序列开头的宽松窗口规则同 ts_mean_relaxed", "ts_product_relaxed(1 + pct_change(close), 5, 1)"),
	relaxed("ts_corr_relaxed", []ArgSpec{{Name: "x", Kind: ArgSeries}, {Name: "y", Kind: ArgSeries}}, 2, nil,
		"窗口内相关系数；任一条序列在窗口里有效值之后又出现 NaN（中途缺失）时为 NaN，否则从两条序列都开始的位置起取共同的有效尾段，有效对数 ≥ m 才出值，m ≥ 2", "ts_corr_relaxed(close, volume, 20, 5)"),
	relaxed("ts_cov_relaxed", []ArgSpec{{Name: "x", Kind: ArgSeries}, {Name: "y", Kind: ArgSeries}}, 2, nil,
		"窗口内样本协方差（ddof=1）；共同有效尾段的规则同 ts_corr_relaxed，m ≥ 2", "ts_cov_relaxed(close, volume, 20, 5)"),

	// 截面
	cross("cs_rank", "截面百分位（名次 / 有效样本数，并列取平均名次）；按 (data_time, series_tag) 分组，排除 NaN", "cs_rank(ts_mean(volume, 20))"),
	cross("cs_zscore", "截面标准化 (x − mean) / std（ddof=1）；样本少于 2 或标准差为 0 时为 NaN", "cs_zscore(pct_change(close, 20))"),
	cross("cs_demean", "截面去均值 x − mean", "cs_demean(pct_change(close))"),
	cross("cs_scale", "截面缩放 x / sum(|x|)；分母为 0 时为 NaN", "cs_scale(cs_demean(x))"),

	// 截面：聚合广播
	aggregate("cs_mean", []ArgSpec{argSeries}, "截面均值，广播到截面每一行（含输入为 NaN 的行）；没有有效样本时为 NaN", "cs_mean(pct_change(close))"),
	aggregate("cs_sum", []ArgSpec{argSeries}, "截面求和，广播到截面每一行；没有有效样本时为 NaN", "cs_sum(volume)"),
	aggregate("cs_count", []ArgSpec{{Name: "c", Kind: ArgBoolSeries}}, "截面里条件为真的个数，c 为 NaN 的行不计；广播到截面每一行；c 全为 NaN 时为 NaN",
		"cs_count(close > open)"),
	aggregate("cs_std", []ArgSpec{argSeries}, "截面样本标准差（ddof=1），广播到截面每一行；有效样本少于 2 时为 NaN", "cs_std(pct_change(close))"),
	aggregate("cs_median", []ArgSpec{argSeries}, "截面中位数，广播到截面每一行；没有有效样本时为 NaN", "abs(close - cs_median(close))"),
	aggregate("cs_quantile", []ArgSpec{argSeries, {Name: "q", Kind: ArgConstant, Min: 0, Max: 1, LiteralOnly: true}},
		"截面 q 分位数，线性插值（与 ts_quantile 同口径），广播到截面每一行；q 只能写 0～1 的字面量，不能用参数", "cs_quantile(volume, 0.9)"),
	aggregate("cs_min", []ArgSpec{argSeries}, "截面最小值，广播到截面每一行；没有有效样本时为 NaN", "cs_min(close)"),
	aggregate("cs_max", []ArgSpec{argSeries}, "截面最大值，广播到截面每一行；没有有效样本时为 NaN", "cs_max(close)"),
	aggregate("cs_ref", []ArgSpec{argSeries, {Name: "subject", Kind: ArgSubject}},
		"同一截面里参考标的的 x，广播到截面每一行；参考标的本期不在截面里或 x 为 NaN 时整个截面为 NaN。标的写成字符串字面量，加成员和启用任务时校验它在任务的标的范围内",
		`pct_change(close) - pct_change(cs_ref(close, "BTC-USDT"))`),
	aggregate("cs_corr", []ArgSpec{{Name: "x", Kind: ArgSeries}, {Name: "y", Kind: ArgSeries}},
		"x、y 都有效的行上的截面皮尔逊相关系数，广播到截面每一行；有效对数少于 3 或任一方方差为 0 时为 NaN。秩相关写成 cs_corr(cs_rank(x), cs_rank(y))",
		"cs_corr(delay(cs_rank(f), 5), cs_rank(pct_change(close, 5)))"),

	// 分组截面：按分组键 g（数值列，例如行业代码）在每个截面内分组，g 为 NaN 的行不属于任何组、输出 NaN；
	// 一个截面的组数超过 1000 时整个截面为 NaN，计入失败指标（gs_too_many_groups）。
	cross("gs_rank", "组内百分位（与 cs_rank 同口径）", "gs_rank(pct_change(close, 20), industry)").withGroup(),
	cross("gs_demean", "组内去均值 x − 组内均值", "gs_demean(pct_change(close), industry)").withGroup(),
	cross("gs_zscore", "组内标准化（ddof=1）；组内样本少于 2 或标准差为 0 时为 NaN", "gs_zscore(pct_change(close, 20), industry)").withGroup(),
	cross("gs_cumshare", "组内按 x 降序排列（并列按标的 ID），累计到本行（含本行）的 x 之和占组内总和的比例；x 应为非负量，组内有负数或总和为 0 时整组为 NaN",
		"gs_cumshare(amount, industry)").withGroup(),
	aggregate("gs_mean", []ArgSpec{argSeries, argGroup}, "组内均值，广播到组内每一行（含输入为 NaN 的行）；组内没有有效样本时为 NaN", "x - gs_mean(x, industry)"),
	aggregate("gs_sum", []ArgSpec{argSeries, argGroup}, "组内求和，广播到组内每一行；组内没有有效样本时为 NaN", "amount / gs_sum(amount, industry)"),
	aggregate("gs_count", []ArgSpec{{Name: "c", Kind: ArgBoolSeries}, argGroup}, "组内条件为真的个数，c 为 NaN 的行不计；广播到组内每一行；组内 c 全为 NaN 时为 NaN",
		"gs_count(close > open, industry)"),
	aggregate("gs_median", []ArgSpec{argSeries, argGroup}, "组内中位数，广播到组内每一行；组内没有有效样本时为 NaN", "x - gs_median(x, industry)"),
	aggregate("gs_wmean", []ArgSpec{argSeries, {Name: "w", Kind: ArgSeries}, argGroup},
		"组内加权均值 Σw·x / Σw，w ≤ 0 或为 NaN、x 为 NaN 的行不参与；广播到组内每一行；没有可用的行时为 NaN", "gs_wmean(pct_change(close, 20), mcap, industry)"),
	aggregate("gx_rank", []ArgSpec{{Name: "v", Kind: ArgSeries}, argGroup},
		"组间百分位：每组取 v 的组内均值作为该组的值（v 通常是 gs_* 的结果，组内为常数；组内全为 NaN 的组不参与），在组之间按 cs_rank 的口径排名，再广播回组内每一行",
		"gx_rank(gs_mean(pct_change(close, 20), industry), industry)"),
	aggregate("gx_zscore", []ArgSpec{{Name: "v", Kind: ArgSeries}, argGroup},
		"组间标准化：每组的值同 gx_rank，在组之间按 cs_zscore 的口径标准化（ddof=1，少于 2 组或标准差为 0 时为 NaN），再广播回组内每一行",
		"gx_zscore(gs_mean(pct_change(close, 20), industry), industry)"),

	// 截面回归残差（逐行变换）
	{Name: "cs_resid", Category: CategoryCrossSection, Args: []ArgSpec{{Name: "y", Kind: ArgSeries}, {Name: "x", Kind: ArgSeries}}, Variadic: 8,
		Result: ResultNumber, Lookback: "各参数回看的最大值",
		Doc:     "每个截面上 y 对常数项与 x1…xk（1～8 个）做最小二乘，输出残差；任一变量为 NaN 的行不参与、输出 NaN。有效行数不超过 k+1 或设计矩阵奇异时整个截面为 NaN，计入失败指标（cs_resid_singular）",
		Example: "-cs_resid(log(mcap), a, b) / log(mcap)"},
	{Name: "gs_resid", Category: CategoryCrossSection, Args: []ArgSpec{{Name: "y", Kind: ArgSeries}, argGroup, {Name: "x", Kind: ArgSeries, Optional: true}},
		Variadic: 8, Result: ResultNumber, Lookback: "各参数回看的最大值",
		Doc:     "同 cs_resid，但截距换成分组哑变量（每组一个截距，g 为 NaN 的行输出 NaN）：先在组内对 y 与各 x 去均值，再做不带截距的回归；不给 x 时等于 gs_demean(y, g)。有效行数不超过 k+组数或奇异时整个截面为 NaN，计入失败指标（cs_resid_singular）",
		Example: "gs_resid(pct_change(close, 20), industry, log(mcap))"},
}

// 运算符：只能用运算符写，不能按名字调用。
var operatorList = []*Function{
	elementwise("add", []ArgSpec{{Name: "a", Kind: ArgNumber}, {Name: "b", Kind: ArgNumber}}, ResultNumber, "a + b", ""),
	elementwise("sub", []ArgSpec{{Name: "a", Kind: ArgNumber}, {Name: "b", Kind: ArgNumber}}, ResultNumber, "a - b", ""),
	elementwise("mul", []ArgSpec{{Name: "a", Kind: ArgNumber}, {Name: "b", Kind: ArgNumber}}, ResultNumber, "a * b", ""),
	elementwise("div", []ArgSpec{{Name: "a", Kind: ArgNumber}, {Name: "b", Kind: ArgNumber}}, ResultNumber, "a / b；除以 0 为 NaN", ""),
	elementwise("mod", []ArgSpec{{Name: "a", Kind: ArgNumber}, {Name: "b", Kind: ArgNumber}}, ResultNumber, "a % b；除以 0 为 NaN", ""),
	elementwise("pow", []ArgSpec{{Name: "a", Kind: ArgNumber}, {Name: "b", Kind: ArgNumber}}, ResultNumber, "a ** b", ""),
	elementwise("neg", []ArgSpec{{Name: "a", Kind: ArgNumber}}, ResultNumber, "-a", ""),
	elementwise("not", []ArgSpec{{Name: "a", Kind: ArgBool}}, ResultBool, "!a", ""),
	elementwise("eq", []ArgSpec{{Name: "a", Kind: ArgNumber}, {Name: "b", Kind: ArgNumber}}, ResultBool, "a == b", ""),
	elementwise("ne", []ArgSpec{{Name: "a", Kind: ArgNumber}, {Name: "b", Kind: ArgNumber}}, ResultBool, "a != b", ""),
	elementwise("lt", []ArgSpec{{Name: "a", Kind: ArgNumber}, {Name: "b", Kind: ArgNumber}}, ResultBool, "a < b", ""),
	elementwise("le", []ArgSpec{{Name: "a", Kind: ArgNumber}, {Name: "b", Kind: ArgNumber}}, ResultBool, "a <= b", ""),
	elementwise("gt", []ArgSpec{{Name: "a", Kind: ArgNumber}, {Name: "b", Kind: ArgNumber}}, ResultBool, "a > b", ""),
	elementwise("ge", []ArgSpec{{Name: "a", Kind: ArgNumber}, {Name: "b", Kind: ArgNumber}}, ResultBool, "a >= b", ""),
	elementwise("and", []ArgSpec{{Name: "a", Kind: ArgBool}, {Name: "b", Kind: ArgBool}}, ResultBool, "a && b", ""),
	elementwise("or", []ArgSpec{{Name: "a", Kind: ArgBool}, {Name: "b", Kind: ArgBool}}, ResultBool, "a || b", ""),
}

var (
	functions = indexFunctions(functionList, false)
	operators = indexFunctions(operatorList, true)
)

func indexFunctions(list []*Function, operator bool) map[string]*Function {
	index := make(map[string]*Function, len(list))
	for _, fn := range list {
		if _, duplicate := index[fn.Name]; duplicate {
			panic("factorexpr: duplicate function " + fn.Name)
		}
		fn.operator = operator
		if fn.Category == CategoryTimeSeries && fn.back == nil {
			panic("factorexpr: time series function without lookback rule " + fn.Name)
		}
		index[fn.Name] = fn
	}
	return index
}

// LookupFunction 返回可以按名字调用的函数。
func LookupFunction(name string) (*Function, bool) {
	fn, ok := functions[name]
	return fn, ok
}

// LookupOperator 返回运算符对应的算子（add、sub、gt 等内部名称）。
func LookupOperator(name string) (*Function, bool) {
	fn, ok := operators[name]
	return fn, ok
}

// Functions 返回全部可调用函数，按类别、名称排序，供控制台的函数参考使用。
func Functions() []*Function {
	out := append([]*Function(nil), functionList...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func isFunctionName(name string) bool {
	_, ok := functions[name]
	return ok
}
