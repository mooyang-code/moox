package factorexpr

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/expr-lang/expr/ast"
)

// scope 是编译环境：已编译的 let 按名称缓存，被多处引用时共享同一个节点。
type scope struct {
	params  map[string]Param
	lets    map[string]*Node
	letPos  map[string]int
	current int // 正在编译的 let 的位置；编译输出时为 len(lets)
	// memo 按语法树节点缓存编译结果。expr-lang 会让多个父节点共享同一个子节点（例如把 0 < e < 1 改写成两个比较、
	// 共用 e），不缓存的话嵌套几十层就会指数级重复编译。同一个语法节点只属于一条表达式，结果确定。
	memo map[ast.Node]*Node
}

// lower 把语法树编译成规范化的表达式节点：解析名称、检查类型、折叠常量、计算回看。
func lower(src *exprSource, node ast.Node, sc *scope) (*Node, error) {
	if cached, ok := sc.memo[node]; ok {
		return cached, nil
	}
	result, err := lowerNode(src, node, sc)
	if err != nil {
		return nil, err
	}
	sc.memo[node] = result
	return result, nil
}

func lowerNode(src *exprSource, node ast.Node, sc *scope) (*Node, error) {
	switch n := node.(type) {
	case *ast.IntegerNode:
		if n.Value > maxIntegerParam || n.Value < -maxIntegerParam {
			return nil, src.errAt(node, "整数 %d 超出范围", n.Value)
		}
		return newNumber(float64(n.Value), true), nil
	case *ast.FloatNode:
		if math.IsNaN(n.Value) || math.IsInf(n.Value, 0) {
			return nil, src.errAt(node, "数值必须是有限数")
		}
		return newNumber(n.Value, false), nil
	case *ast.BoolNode:
		return newBool(n.Value), nil
	case *ast.IdentifierNode:
		return resolveName(src, n, sc)
	case *ast.UnaryNode:
		child, err := lower(src, n.Node, sc)
		if err != nil {
			return nil, err
		}
		switch n.Operator {
		case "-":
			return applyOperator(src, node, "neg", []*Node{child}, []ast.Node{n.Node})
		case "+":
			if !child.Kind.isNumber() {
				return nil, src.errAt(node, "运算 + 需要数值，得到%s", child.Kind.describe())
			}
			return child, nil
		case "!", "not":
			return applyOperator(src, node, "not", []*Node{child}, []ast.Node{n.Node})
		}
		return nil, src.errAt(node, "不支持运算符 %s", n.Operator)
	case *ast.BinaryNode:
		if reason, rejected := rejectedOperators[n.Operator]; rejected {
			return nil, src.errAt(node, "%s", reason)
		}
		op, ok := binaryOperators[n.Operator]
		if !ok {
			return nil, src.errAt(node, "不支持运算符 %s", n.Operator)
		}
		left, err := lower(src, n.Left, sc)
		if err != nil {
			return nil, err
		}
		right, err := lower(src, n.Right, sc)
		if err != nil {
			return nil, err
		}
		return applyOperator(src, node, op, []*Node{left, right}, []ast.Node{n.Left, n.Right})
	case *ast.ConditionalNode:
		parts := []ast.Node{n.Cond, n.Exp1, n.Exp2}
		args := make([]*Node, 0, 3)
		for _, part := range parts {
			arg, err := lower(src, part, sc)
			if err != nil {
				return nil, err
			}
			args = append(args, arg)
		}
		fn, _ := LookupFunction("where")
		return apply(src, node, fn, args, parts, sc)
	case *ast.CallNode:
		return lowerCall(src, n, sc)
	}
	return nil, rejectNode(src, node)
}

func resolveName(src *exprSource, node *ast.IdentifierNode, sc *scope) (*Node, error) {
	name := node.Value
	if position, ok := sc.letPos[name]; ok {
		if position >= sc.current {
			return nil, src.errAt(node, "引用了在它之后（或它自身）定义的 let %s；let 只能引用前面定义的中间量", name)
		}
		return sc.lets[name], nil
	}
	if param, ok := sc.params[name]; ok {
		return newParam(param), nil
	}
	if isFunctionName(name) {
		return nil, src.errAt(node, "%s 是函数，需要写成 %s(...)", name, name)
	}
	if isReservedColumn(name) {
		return nil, src.errAt(node, "%s 是系统列，不能作为输入", name)
	}
	if !validColumnName(name) {
		return nil, src.errAt(node, "输入列名 %q 不合法：只能包含字母、数字和下划线，不能以数字开头，最长 %d 个字符", name, MaxIdentifierRunes)
	}
	return newColumn(name), nil
}

func lowerCall(src *exprSource, call *ast.CallNode, sc *scope) (*Node, error) {
	callee, ok := call.Callee.(*ast.IdentifierNode)
	if !ok {
		return nil, src.errAt(call, "只能调用函数名")
	}
	fn, ok := LookupFunction(callee.Value)
	if !ok {
		if _, isLet := sc.letPos[callee.Value]; isLet {
			return nil, src.errAt(call, "%s 是 let 中间量，不是函数", callee.Value)
		}
		if _, isParam := sc.params[callee.Value]; isParam {
			return nil, src.errAt(call, "%s 是参数，不是函数", callee.Value)
		}
		return nil, src.errAt(call, "未知的函数 %s", callee.Value)
	}
	required := 0
	for _, arg := range fn.Args {
		if !arg.Optional {
			required++
		}
	}
	if len(call.Arguments) < required || len(call.Arguments) > len(fn.Args) {
		if required == len(fn.Args) {
			return nil, src.errAt(call, "函数 %s 需要 %d 个参数，写了 %d 个", fn.Name, required, len(call.Arguments))
		}
		return nil, src.errAt(call, "函数 %s 需要 %d～%d 个参数，写了 %d 个", fn.Name, required, len(fn.Args), len(call.Arguments))
	}
	args := make([]*Node, 0, len(call.Arguments))
	for _, argument := range call.Arguments {
		arg, err := lower(src, argument, sc)
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
	}
	return apply(src, call, fn, args, call.Arguments, sc)
}

func applyOperator(src *exprSource, node ast.Node, op string, args []*Node, argNodes []ast.Node) (*Node, error) {
	fn, _ := LookupOperator(op)
	return apply(src, node, fn, args, argNodes, nil)
}

// apply 检查参数类型、提取常量与参数槽、折叠常量，并构造算子节点。argNodes 与 args 一一对应，用于报错定位。
// 窗口、位移、分位数等编译期位置只接受字面常量或单个参数名；参数的声明范围必须落在该位置允许的范围内，
// 这样每期提交的任何合法取值都不会越界，只需在提交时检查算子的联合约束（例如 sma 的 m < n）。
func apply(src *exprSource, node ast.Node, fn *Function, args []*Node, argNodes []ast.Node, sc *scope) (*Node, error) {
	label := "函数 " + fn.Name + " 的参数 "
	if fn.operator {
		label = "运算 " + operatorSymbols[fn.Name] + " 的参数 "
	}
	result := &Node{Op: fn.Name, Function: fn, size: 1}
	for i, spec := range fn.Args {
		if i >= len(args) {
			// 可选参数取默认值
			if spec.Kind == ArgConstant {
				result.Floats = append(result.Floats, spec.Default)
				result.FloatParams = append(result.FloatParams, "")
			} else {
				result.Ints = append(result.Ints, int(spec.Default))
				result.IntParams = append(result.IntParams, "")
			}
			continue
		}
		arg, at := args[i], argNodes[i]
		switch spec.Kind {
		case ArgNumber:
			if !arg.Kind.isNumber() {
				return nil, src.errAt(at, "%s%s 需要数值，得到%s", label, spec.Name, arg.Kind.describe())
			}
		case ArgSeries:
			if arg.Kind == KindNumberConst || arg.Scalar {
				return nil, src.errAt(at, "%s%s 必须依赖输入列，不能是常量或参数", label, spec.Name)
			}
			if arg.Kind != KindNumber {
				return nil, src.errAt(at, "%s%s 需要数值序列，得到%s", label, spec.Name, arg.Kind.describe())
			}
		case ArgBool:
			if !arg.Kind.isBool() {
				return nil, src.errAt(at, "%s%s 需要布尔值，得到%s；数值请先比较，例如 x > 0", label, spec.Name, arg.Kind.describe())
			}
		case ArgBoolSeries:
			if arg.Kind == KindBoolConst || arg.Scalar {
				return nil, src.errAt(at, "%s%s 必须依赖输入列，不能是常量或参数", label, spec.Name)
			}
			if arg.Kind != KindBool {
				return nil, src.errAt(at, "%s%s 需要布尔序列，得到%s", label, spec.Name, arg.Kind.describe())
			}
		case ArgWindow, ArgInteger:
			if arg.Op == OpParam {
				param := sc.params[arg.Param]
				if !param.IsInt() || param.Min < spec.Min || param.Max > spec.Max {
					return nil, src.errAt(at, "%s%s 使用参数 %s 时，参数必须是整数类型且范围在 %s～%s 内", label, spec.Name, param.Name,
						formatFloat(spec.Min), formatFloat(spec.Max))
				}
				result.Ints = append(result.Ints, int(param.Default))
				result.IntParams = append(result.IntParams, param.Name)
				continue
			}
			if arg.Kind != KindNumberConst || !arg.IsInt || arg.Value < spec.Min || arg.Value > spec.Max {
				return nil, src.errAt(at, "%s%s 必须是 %s～%s 的整数常量或单个整数参数", label, spec.Name, formatFloat(spec.Min), formatFloat(spec.Max))
			}
			result.Ints = append(result.Ints, int(arg.Value))
			result.IntParams = append(result.IntParams, "")
			continue
		case ArgConstant:
			if arg.Op == OpParam {
				param := sc.params[arg.Param]
				if param.Min < spec.Min || param.Max > spec.Max {
					return nil, src.errAt(at, "%s%s 使用参数 %s 时，参数范围必须在 %s～%s 内", label, spec.Name, param.Name,
						formatFloat(spec.Min), formatFloat(spec.Max))
				}
				result.Floats = append(result.Floats, param.Default)
				result.FloatParams = append(result.FloatParams, param.Name)
				continue
			}
			if arg.Kind != KindNumberConst || arg.Value < spec.Min || arg.Value > spec.Max {
				return nil, src.errAt(at, "%s%s 必须是 %s～%s 之间的常量或单个参数", label, spec.Name, formatFloat(spec.Min), formatFloat(spec.Max))
			}
			result.Floats = append(result.Floats, arg.Value)
			result.FloatParams = append(result.FloatParams, "")
			continue
		}
		result.Args = append(result.Args, arg)
	}
	if fn.validate != nil {
		if reason := fn.validate(result.Ints, result.Floats); reason != "" {
			return nil, src.errAt(node, "%s（按参数默认值）", reason)
		}
	}
	if fn.Category == CategoryElementwise {
		if folded, ok, err := fold(src, node, fn, result.Args); ok || err != nil {
			return folded, err
		}
	}
	if fn.Result == ResultBool {
		result.Kind = KindBool
	} else {
		result.Kind = KindNumber
	}
	result.cross = fn.Category == CategoryCrossSection
	// 逐元素算子的参数全是常量或参数时，结果也只由参数决定（每条序列一个值）。
	result.Scalar = fn.Category == CategoryElementwise
	childLookback := 0
	for _, arg := range result.Args {
		childLookback = max(childLookback, arg.Lookback)
		result.size = addSize(result.size, arg.size)
		result.cross = result.cross || arg.cross
		result.Scalar = result.Scalar && (arg.Kind.isConst() || arg.Scalar)
	}
	result.back, result.shift = worstBackShift(fn, result.Ints, result.IntParams, sc)
	result.Lookback = childLookback + result.back
	if result.Lookback > MaxLookback {
		return nil, src.errAt(node, "回看 %d 个周期（参数按声明范围取最坏情况），超过上限 %d", result.Lookback, MaxLookback)
	}
	return result, nil
}

// worstBackShift 返回参数在声明范围内取值时，算子需求区间的最坏扩展：back 取最大、shift 取最小。
// 每个参数槽只取范围的两个端点（各算子的 Back、Shift 对每个参数单调，例如 sma 的截断长度在 n 最大、m 最小时最长），
// 一个节点最多 3 个整数参数，枚举量很小。
func worstBackShift(fn *Function, ints []int, slots []string, sc *scope) (int, int) {
	var positions []int
	for i, name := range slots {
		if name != "" {
			positions = append(positions, i)
		}
	}
	if len(positions) == 0 {
		return fn.Back(ints), fn.Shift(ints)
	}
	corner := append([]int(nil), ints...)
	back, shift := -1, math.MaxInt
	for mask := 0; mask < 1<<len(positions); mask++ {
		for bit, position := range positions {
			param := sc.params[slots[position]]
			corner[position] = int(param.Min)
			if mask&(1<<bit) != 0 {
				corner[position] = int(param.Max)
			}
		}
		back, shift = max(back, fn.Back(corner)), min(shift, fn.Shift(corner))
	}
	return back, shift
}

// fold 折叠逐元素算子：参数全是常量时在编译期算出结果；where 的条件是常量时直接取对应分支。
func fold(src *exprSource, node ast.Node, fn *Function, args []*Node) (*Node, bool, error) {
	if fn.Name == "where" && args[0].Kind == KindBoolConst && !(args[1].Kind.isConst() && args[2].Kind.isConst()) {
		if args[0].Value != 0 {
			return args[1], true, nil
		}
		return args[2], true, nil
	}
	values := make([]float64, len(args))
	allInt := true
	for i, arg := range args {
		if !arg.Kind.isConst() {
			return nil, false, nil
		}
		values[i] = arg.Value
		allInt = allInt && arg.IsInt
	}
	value := Scalar(fn.Name)(values)
	if math.IsNaN(value) {
		return nil, false, src.errAt(node, "常量运算的结果不是有限数（例如除以 0、对负数取对数）")
	}
	if fn.Result == ResultBool {
		return newBool(value != 0), true, nil
	}
	isInt := allInt && integerPreserving[fn.Name] && value == math.Trunc(value) && math.Abs(value) <= maxIntegerParam
	if fn.Name == "where" {
		chosen := args[2]
		if args[0].Value != 0 {
			chosen = args[1]
		}
		isInt = chosen.IsInt
	}
	return newNumber(value, isInt), true, nil
}

// FactorType 是推导出的因子类型（设计 3.4）。
type FactorType string

const (
	FactorTypeTimeSeries   FactorType = "timeseries"
	FactorTypeCrossSection FactorType = "cross_section"
)

// Output 是一个输出列。
type Output struct {
	Name string
	Root *Node
}

// Factor 是编译后的因子：输出列的表达式树与推导出的元数据（设计 6.3）。
type Factor struct {
	Spec         *Spec
	Params       []Param
	Outputs      []Output
	InputColumns []string
	// Lookback 是参数在声明范围内取最坏值时的回看。
	Lookback int
	Type     FactorType
}

// Param 返回名为 name 的参数声明。
func (f *Factor) Param(name string) (Param, bool) {
	for _, param := range f.Params {
		if param.Name == name {
			return param, true
		}
	}
	return Param{}, false
}

// Defaults 返回全部参数的默认值。
func (f *Factor) Defaults() map[string]float64 {
	values := make(map[string]float64, len(f.Params))
	for _, param := range f.Params {
		values[param.Name] = param.Default
	}
	return values
}

// CheckParams 检查一组参数取值（一个标的的完整取值）：每个参数都有、合法，且满足算子的联合约束（例如 sma 的 m < n）。
func (f *Factor) CheckParams(values map[string]float64) error {
	for name := range values {
		if _, ok := f.Param(name); !ok {
			return fmt.Errorf("未声明的参数 %s", name)
		}
	}
	for _, param := range f.Params {
		value, ok := values[param.Name]
		if !ok {
			return fmt.Errorf("缺少参数 %s", param.Name)
		}
		if err := param.Check(value); err != nil {
			return err
		}
	}
	visited := map[*Node]bool{}
	var walk func(*Node) error
	walk = func(node *Node) error {
		if visited[node] {
			return nil
		}
		visited[node] = true
		for _, arg := range node.Args {
			if err := walk(arg); err != nil {
				return err
			}
		}
		if node.Function == nil || node.Function.validate == nil || !node.HasParamSlots() {
			return nil
		}
		ints, floats := node.Bind(values)
		if reason := node.Function.validate(ints, floats); reason != "" {
			return fmt.Errorf("%s（%s）", reason, node.Canonical())
		}
		return nil
	}
	for _, output := range f.Outputs {
		if err := walk(output.Root); err != nil {
			return err
		}
	}
	return nil
}

// Bind 返回节点的整数与数值参数在一组参数取值下的具体值；没有取值的参数槽用默认值。
func (n *Node) Bind(values map[string]float64) ([]int, []float64) {
	ints := append([]int(nil), n.Ints...)
	for i, name := range n.IntParams {
		if value, ok := values[name]; ok && name != "" {
			ints[i] = int(value)
		}
	}
	floats := append([]float64(nil), n.Floats...)
	for i, name := range n.FloatParams {
		if value, ok := values[name]; ok && name != "" {
			floats[i] = value
		}
	}
	return ints, floats
}

// Compile 解析并编译一份定义文本。
func Compile(text string) (*Factor, error) {
	spec, err := ParseSpec(text)
	if err != nil {
		return nil, err
	}
	return CompileSpec(spec)
}

// CompileSpec 编译已解析的定义：编译 let 与输出（参数编译成参数槽），推导输入列、回看与类型。
func CompileSpec(spec *Spec) (*Factor, error) {
	if len(spec.Outputs) > MaxOutputs {
		return nil, errorf("outputs", 0, "输出不能超过 %d 个", MaxOutputs)
	}
	sc := &scope{params: make(map[string]Param, len(spec.Params)), lets: make(map[string]*Node, len(spec.Lets)),
		letPos: make(map[string]int, len(spec.Lets)), memo: make(map[ast.Node]*Node)}
	for _, param := range spec.Params {
		sc.params[param.Name] = param
	}
	lets := make([]*parsedBinding, len(spec.Lets))
	for i, binding := range spec.Lets {
		var err error
		if lets[i], err = parseBinding("let."+binding.Name, binding); err != nil {
			return nil, err
		}
		sc.letPos[binding.Name] = i
	}
	for i, let := range lets {
		sc.current = i
		node, err := lower(let.src, let.root, sc)
		if err != nil {
			return nil, err
		}
		sc.lets[let.binding.Name] = node
	}
	sc.current = len(lets)

	factor := &Factor{Spec: spec, Params: append([]Param(nil), spec.Params...)}
	total := 0
	for _, binding := range spec.Outputs {
		path := "outputs." + binding.Name
		if !validColumnName(binding.Name) {
			return nil, errorf(path, binding.Line, "输出列名 %q 不合法：只能包含字母、数字和下划线，不能以数字开头，最长 %d 个字符",
				binding.Name, MaxIdentifierRunes)
		}
		if isReservedColumn(binding.Name) {
			return nil, errorf(path, binding.Line, "输出列名 %s 是系统列", binding.Name)
		}
		parsed, err := parseBinding(path, binding)
		if err != nil {
			return nil, err
		}
		root, err := lower(parsed.src, parsed.root, sc)
		if err != nil {
			return nil, err
		}
		switch {
		case root.Kind == KindNumberConst:
			return nil, parsed.src.errAt(parsed.root, "输出必须依赖输入列，不能是常量")
		case root.Kind == KindBool || root.Kind == KindBoolConst:
			return nil, parsed.src.errAt(parsed.root, "输出必须是数值；布尔值请用 where(c, 1, 0) 转换")
		case root.Scalar:
			return nil, parsed.src.errAt(parsed.root, "输出必须依赖输入列，不能只由参数决定")
		}
		total = addSize(total, root.size)
		if total > MaxFactorNodes {
			return nil, errorf("outputs", 0, "内联 let 之后的表达式节点超过 %d 个", MaxFactorNodes)
		}
		factor.Outputs = append(factor.Outputs, Output{Name: binding.Name, Root: root})
	}
	factor.derive()
	return factor, nil
}

func (f *Factor) derive() {
	columns := map[string]bool{}
	visited := map[*Node]bool{}
	var walk func(*Node)
	walk = func(node *Node) {
		if visited[node] {
			return
		}
		visited[node] = true
		if node.Op == OpColumn {
			columns[node.Column] = true
		}
		for _, arg := range node.Args {
			walk(arg)
		}
	}
	f.Lookback = 1
	f.Type = FactorTypeTimeSeries
	for _, output := range f.Outputs {
		walk(output.Root)
		f.Lookback = max(f.Lookback, output.Root.Lookback)
		if output.Root.cross {
			f.Type = FactorTypeCrossSection
		}
	}
	f.InputColumns = make([]string, 0, len(columns))
	for column := range columns {
		f.InputColumns = append(f.InputColumns, column)
	}
	sort.Strings(f.InputColumns)
}

// OutputNames 返回输出列名，保持定义中的顺序。
func (f *Factor) OutputNames() []string {
	names := make([]string, len(f.Outputs))
	for i, output := range f.Outputs {
		names[i] = output.Name
	}
	return names
}

// CanonicalText 返回规范程序文本：先逐行写出参数声明“$名称 类型 [min, max] = 默认值”，再按输出顺序逐行写出
// “输出名 = 规范表达式”（设计 9.3）。参数声明进入 definition_hash，每期的取值不进入。
// definition_hash 由它与 ExprVersion、allow_partial_universe 一起计算。
func (f *Factor) CanonicalText() string {
	var b strings.Builder
	for _, param := range f.Params {
		fmt.Fprintf(&b, "$%s %s [%s, %s] = %s\n", param.Name, param.Type, formatFloat(param.Min), formatFloat(param.Max),
			formatFloat(param.Default))
	}
	for _, output := range f.Outputs {
		b.WriteString(output.Name)
		b.WriteString(" = ")
		output.Root.writeCanonical(&b)
		b.WriteByte('\n')
	}
	return b.String()
}

// ExprVersion 是算子语义版本（设计 9.3）：任何算子的语义变化（包括数值行为的修正）都要递增，只做性能优化时不变。
const ExprVersion = "expr/2"

func (f *Factor) String() string {
	return fmt.Sprintf("factor(type=%s, lookback=%d, inputs=%s, outputs=%s)", f.Type, f.Lookback,
		strings.Join(f.InputColumns, ","), strconv.Quote(strings.Join(f.OutputNames(), ",")))
}
