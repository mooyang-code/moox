package factorexpr

import (
	"fmt"
	"sort"
	"strings"

	"github.com/expr-lang/expr/ast"
)

// 自定义算子（wasm UDF，设计 4）在 DSL 里写成 udf.<名称>(<序列输入>..., <参数>...)，是时序算子：
// 只依赖窗口、回看由元数据的公式决定。factorexpr 不加载也不执行 wasm，只按调用方传入的解析环境（名称 → 版本哈希与元数据）
// 编译；执行经 exec.UDFHost 交给宿主（packages/factorudf）。

// UDFParam 是自定义算子的一个标量参数（元数据声明顺序）。
type UDFParam struct {
	Name string
	// Int 为真是整数参数，否则是数值参数。
	Int      bool
	Min, Max float64
}

// UDF 是一个已解析到具体版本的自定义算子：版本哈希决定它的身份，进入规范程序文本与 definition_hash。
type UDF struct {
	Name string
	// Hash 是版本（wasm 字节的 sha256，十六进制小写）。
	Hash   string
	Inputs []string
	Params []UDFParam
	// 窗口长度 L = LookbackA * 参数 LookbackParam + LookbackB；LookbackParam 为空时 L = LookbackB。
	LookbackA     int64
	LookbackB     int64
	LookbackParam string
	// Relaxed、MinValid 是空值规则（只供说明与调用方使用，规则由模块自己强制）。
	Relaxed  bool
	MinValid int
	Doc      string
	Example  string
}

// UDFEnv 是编译时的自定义算子解析环境：名称 → 版本与元数据。由调用方提供（管理端按启用版本或请求级覆盖解析），
// factorexpr 自己不查找“当前版本”。
type UDFEnv interface {
	LookupUDF(name string) (UDF, bool)
	ListUDFs() []UDF
}

// UDFs 是一个固定的解析环境。
type UDFs map[string]UDF

// LookupUDF 返回名称对应的版本。
func (e UDFs) LookupUDF(name string) (UDF, bool) {
	u, ok := e[name]
	return u, ok
}

// ListUDFs 按名称排序返回全部。
func (e UDFs) ListUDFs() []UDF {
	out := make([]UDF, 0, len(e))
	for _, u := range e {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// UDFRef 是因子定义引用的一个自定义算子版本。
type UDFRef struct {
	Name string
	Hash string
}

// CallName 是节点算子名里的前缀部分：udf.<名称>。
func (u UDF) CallName() string { return "udf." + u.Name }

// NodeOp 是节点的算子名：udf.<名称>@<版本哈希>，也是规范程序文本里的写法。
func (u UDF) NodeOp() string { return u.CallName() + "@" + u.Hash }

// Lookback 返回整数参数取值为 value 时的窗口长度 L（不检查溢出：参数范围在编译时已限制）。
func (u UDF) lookbackAt(ints []int, intPos int) int {
	if u.LookbackParam == "" {
		return int(u.LookbackB)
	}
	return int(u.LookbackA)*ints[intPos] + int(u.LookbackB)
}

// Window 按整数参数值（Function 里 Ints 的顺序）返回窗口长度 L。
func (u UDF) Window(ints []int) int {
	pos := 0
	for _, p := range u.Params {
		if !p.Int {
			continue
		}
		if p.Name == u.LookbackParam {
			return u.lookbackAt(ints, pos)
		}
		pos++
	}
	return u.lookbackAt(ints, 0)
}

// ParamSlots 返回元数据参数在 Node/ProgramNode 的 Ints、Floats 里的位置：第 i 个参数是整数参数时 ints[i] 是它在 Ints 里的下标，
// 否则 floats[i] 是它在 Floats 里的下标。
func (u UDF) ParamSlots() (isInt []bool, index []int) {
	isInt, index = make([]bool, len(u.Params)), make([]int, len(u.Params))
	ints, floats := 0, 0
	for i, p := range u.Params {
		isInt[i] = p.Int
		if p.Int {
			index[i] = ints
			ints++
		} else {
			index[i] = floats
			floats++
		}
	}
	return isInt, index
}

// udfFunction 把一个自定义算子变成注册表里的函数（只存在于本次编译的环境里）。
func udfFunction(u UDF) *Function {
	args := make([]ArgSpec, 0, len(u.Inputs)+len(u.Params))
	for _, name := range u.Inputs {
		args = append(args, ArgSpec{Name: name, Kind: ArgSeries})
	}
	for _, p := range u.Params {
		kind := ArgConstant
		if p.Int {
			kind = ArgInteger
		}
		args = append(args, ArgSpec{Name: p.Name, Kind: kind, Min: p.Min, Max: p.Max})
	}
	lookback := fmt.Sprintf("L(输入)+L−1，窗口长度 L = %s", describeUDFLookback(u))
	udf := u
	return &Function{
		Name: u.CallName(), Category: CategoryTimeSeries, Args: args, Result: ResultNumber,
		Lookback: lookback, Doc: u.Doc, Example: u.Example, udf: &udf,
		back: func(ints []int) int { return max(udf.Window(ints)-1, 0) },
	}
}

func describeUDFLookback(u UDF) string {
	switch {
	case u.LookbackParam == "":
		return fmt.Sprintf("%d", u.LookbackB)
	case u.LookbackA == 1 && u.LookbackB == 0:
		return u.LookbackParam
	case u.LookbackB == 0:
		return fmt.Sprintf("%d*%s", u.LookbackA, u.LookbackParam)
	case u.LookbackA == 1:
		return fmt.Sprintf("%s+%d", u.LookbackParam, u.LookbackB)
	}
	return fmt.Sprintf("%d*%s+%d", u.LookbackA, u.LookbackParam, u.LookbackB)
}

// UDF 返回自定义算子的定义；内置算子返回 nil。
func (f *Function) UDF() *UDF {
	if f == nil {
		return nil
	}
	return f.udf
}

// udfCallee 识别 udf.<名称> 形式的调用对象：成员访问，左侧是标识符 udf，右侧是名称。其余成员访问仍然拒绝。
func udfCallee(callee ast.Node) (name string, ok bool) {
	member, isMember := callee.(*ast.MemberNode)
	if !isMember || member.Optional {
		return "", false
	}
	base, isIdent := member.Node.(*ast.IdentifierNode)
	if !isIdent || base.Value != "udf" {
		return "", false
	}
	property, isString := member.Property.(*ast.StringNode)
	if !isString {
		return "", false
	}
	return property.Value, true
}

// lowerUDFCall 编译 udf.<名称>(...) 调用：按解析环境找到版本，构造函数后走与内置算子相同的参数检查。
func lowerUDFCall(src *exprSource, call *ast.CallNode, name string, sc *scope) (*Node, error) {
	if sc.udfs == nil {
		return nil, src.errAt(call, "没有可用的自定义算子：这里的编译环境不支持 udf.%s", name)
	}
	fn := sc.udfFns[name]
	if fn == nil {
		def, ok := sc.udfs.LookupUDF(name)
		if !ok {
			return nil, src.errAt(call, "未知的自定义算子 udf.%s%s", name, availableUDFs(sc.udfs))
		}
		if def.Hash == "" {
			return nil, src.errAt(call, "自定义算子 udf.%s 没有启用的版本", name)
		}
		fn = udfFunction(def)
		sc.udfFns[name] = fn
	}
	if len(call.Arguments) != len(fn.Args) {
		return nil, src.errAt(call, "自定义算子 %s 需要 %d 个参数（序列输入 %s；参数 %s），写了 %d 个",
			fn.Name, len(fn.Args), strings.Join(fn.udf.Inputs, "、"), udfParamNames(fn.udf), len(call.Arguments))
	}
	args := make([]*Node, 0, len(call.Arguments))
	for _, argument := range call.Arguments {
		arg, err := lower(src, argument, sc)
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
	}
	node, err := apply(src, call, fn, args, call.Arguments, sc)
	if err != nil {
		return nil, err
	}
	node.Op = fn.udf.NodeOp()
	return node, nil
}

func udfParamNames(u *UDF) string {
	if len(u.Params) == 0 {
		return "无"
	}
	names := make([]string, len(u.Params))
	for i, p := range u.Params {
		names[i] = p.Name
	}
	return strings.Join(names, "、")
}

func availableUDFs(env UDFEnv) string {
	list := env.ListUDFs()
	if len(list) == 0 {
		return "：当前没有已启用的自定义算子"
	}
	names := make([]string, len(list))
	for i, u := range list {
		names[i] = "udf." + u.Name
	}
	return "；可用：" + strings.Join(names, "、")
}
