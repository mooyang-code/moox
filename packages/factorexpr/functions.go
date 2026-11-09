package factorexpr

import (
	"strconv"
	"strings"
)

// ArgDoc 是函数参数的说明。Min、Max 只对编译期常量参数有意义。
type ArgDoc struct {
	Name     string
	Kind     string // number、series、bool、bool_series、window、integer、constant、subject、group
	Min      float64
	Max      float64
	Optional bool
	Default  float64
	// Variadic 大于 0 表示变参（只出现在最后一个参数上）：可以重复写 1～Variadic 次，可选时也可以不写；没有默认值。
	Variadic int
}

// FunctionDoc 是一个函数的说明，供控制台的函数参考与补全使用（ListFactorFunctions）。
type FunctionDoc struct {
	Name     string
	Category string // elementwise、timeseries、cross_section
	// Broadcast 为真表示聚合广播类截面函数（Category 仍是 cross_section）。
	Broadcast bool
	Signature string // 例如 ts_quantile(x, n, q)、pct_change(x, k=1)
	Result    string // number 或 bool
	Args      []ArgDoc
	Lookback  string
	Doc       string
	Example   string
}

// variadicSignature 是变参在签名里的写法：必填时为 x1[, x2 … x8]，可选时为 [x1 … x8]。
func variadicSignature(arg ArgSpec, most int) string {
	last := arg.Name + strconv.Itoa(most)
	if arg.Optional {
		return "[" + arg.Name + "1 … " + last + "]"
	}
	return arg.Name + "1[, " + arg.Name + "2 … " + last + "]"
}

var argKindNames = map[ArgKind]string{
	ArgNumber: "number", ArgSeries: "series", ArgBool: "bool", ArgBoolSeries: "bool_series",
	ArgWindow: "window", ArgInteger: "integer", ArgConstant: "constant", ArgSubject: "subject", ArgGroup: "group",
}

// FunctionDocs 返回全部可调用函数的说明，顺序与 Functions 相同。
func FunctionDocs() []FunctionDoc { return FunctionDocsWith(nil) }

// FunctionDocsWith 在内置函数之后附上环境里的自定义算子：类别是 timeseries，签名带 udf. 前缀。
func FunctionDocsWith(env UDFEnv) []FunctionDoc {
	list := Functions()
	if env != nil {
		for _, u := range env.ListUDFs() {
			if u.Hash != "" {
				list = append(list, udfFunction(u))
			}
		}
	}
	docs := make([]FunctionDoc, 0, len(list))
	for _, fn := range list {
		doc := FunctionDoc{Name: fn.Name, Category: fn.Category.String(), Broadcast: fn.Broadcast, Lookback: fn.Lookback, Doc: fn.Doc,
			Example: fn.Example, Result: "number"}
		if fn.Result == ResultBool {
			doc.Result = "bool"
		}
		parts := make([]string, 0, len(fn.Args))
		for i, arg := range fn.Args {
			argDoc := ArgDoc{Name: arg.Name, Kind: argKindNames[arg.Kind], Min: arg.Min, Max: arg.Max, Optional: arg.Optional, Default: arg.Default}
			if fn.Variadic > 0 && i == len(fn.Args)-1 {
				argDoc.Variadic, argDoc.Default = fn.Variadic, 0
				doc.Args = append(doc.Args, argDoc)
				parts = append(parts, variadicSignature(arg, fn.Variadic))
				continue
			}
			doc.Args = append(doc.Args, argDoc)
			if arg.Optional {
				parts = append(parts, arg.Name+"="+formatFloat(arg.Default))
			} else {
				parts = append(parts, arg.Name)
			}
		}
		doc.Signature = fn.Name + "(" + strings.Join(parts, ", ") + ")"
		if fn.Variadic > 0 && fn.Args[len(fn.Args)-1].Optional {
			// 可选的变参写成 gs_resid(y, g[, x1 … x8])：方括号紧跟前一个参数。
			doc.Signature = strings.Replace(doc.Signature, ", [", "[, ", 1)
		}
		docs = append(docs, doc)
	}
	return docs
}
