package factorexpr

import (
	"math"
	"sort"
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
)

// ArgSpec 描述一个参数。Min、Max 只对编译期常量参数有效（闭区间）。
type ArgSpec struct {
	Name     string
	Kind     ArgKind
	Min      float64
	Max      float64
	Optional bool
	Default  float64
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
	// back 返回在子节点回看之上额外需要的行数（同时决定需求区间向前扩展多少），shift 返回需求区间整体向前平移的行数
	// （只有 delay 不为 0）。参数是整数常量参数，按 Args 中出现的顺序排列。
	back  func(ints []int) int
	shift func(ints []int) int
	// validate 做参数之间的联合校验（例如 sma 的 m < n）。
	validate func(ints []int, floats []float64) string
	operator bool
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
func emaBack(ints []int) int    { return RecursiveSpan(EMAAlpha(ints[0])) - 1 }
func smaBack(ints []int) int    { return RecursiveSpan(SMAAlpha(ints[0], ints[1])) - 1 }

var (
	argX      = ArgSpec{Name: "x", Kind: ArgNumber}
	argSeries = ArgSpec{Name: "x", Kind: ArgSeries}
)

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

func cross(name, doc, example string) *Function {
	return &Function{Name: name, Category: CategoryCrossSection, Args: []ArgSpec{argSeries}, Result: ResultNumber,
		Lookback: "L(x)", Doc: doc, Example: example}
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

	// 时序：递归平滑（截断到 K 根）
	{Name: "ema", Category: CategoryTimeSeries, Args: []ArgSpec{argSeries, window(1)}, Result: ResultNumber,
		Lookback: "L(x)+K−1，K 为让起点权重衰减到 1e−9 以下的根数",
		Doc:      "指数移动平均，α = 2/(n+1)，截断到最近 K 根（ewm(span=n, adjust=False)）", Example: "ema(close, 20)", back: emaBack},
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

	// 截面
	cross("cs_rank", "截面百分位（名次 / 有效样本数，并列取平均名次）；按 (data_time, series_tag) 分组，排除 NaN", "cs_rank(ts_mean(volume, 20))"),
	cross("cs_zscore", "截面标准化 (x − mean) / std（ddof=1）；样本少于 2 或标准差为 0 时为 NaN", "cs_zscore(pct_change(close, 20))"),
	cross("cs_demean", "截面去均值 x − mean", "cs_demean(pct_change(close))"),
	cross("cs_scale", "截面缩放 x / sum(|x|)；分母为 0 时为 NaN", "cs_scale(cs_demean(x))"),
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
