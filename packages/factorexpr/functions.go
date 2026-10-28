package factorexpr

import "strings"

// ArgDoc 是函数参数的说明。Min、Max 只对编译期常量参数有意义。
type ArgDoc struct {
	Name     string
	Kind     string // number、series、bool、bool_series、window、integer、constant
	Min      float64
	Max      float64
	Optional bool
	Default  float64
}

// FunctionDoc 是一个函数的说明，供控制台的函数参考与补全使用（ListFactorFunctions）。
type FunctionDoc struct {
	Name      string
	Category  string // elementwise、timeseries、cross_section
	Signature string // 例如 ts_quantile(x, n, q)、pct_change(x, k=1)
	Result    string // number 或 bool
	Args      []ArgDoc
	Lookback  string
	Doc       string
	Example   string
}

var argKindNames = map[ArgKind]string{
	ArgNumber: "number", ArgSeries: "series", ArgBool: "bool", ArgBoolSeries: "bool_series",
	ArgWindow: "window", ArgInteger: "integer", ArgConstant: "constant",
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
		doc := FunctionDoc{Name: fn.Name, Category: fn.Category.String(), Lookback: fn.Lookback, Doc: fn.Doc, Example: fn.Example,
			Result: "number"}
		if fn.Result == ResultBool {
			doc.Result = "bool"
		}
		parts := make([]string, 0, len(fn.Args))
		for _, arg := range fn.Args {
			doc.Args = append(doc.Args, ArgDoc{Name: arg.Name, Kind: argKindNames[arg.Kind], Min: arg.Min, Max: arg.Max,
				Optional: arg.Optional, Default: arg.Default})
			if arg.Optional {
				parts = append(parts, arg.Name+"="+formatFloat(arg.Default))
			} else {
				parts = append(parts, arg.Name)
			}
		}
		doc.Signature = fn.Name + "(" + strings.Join(parts, ", ") + ")"
		docs = append(docs, doc)
	}
	return docs
}
