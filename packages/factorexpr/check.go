package factorexpr

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/expr-lang/expr/ast"
)

// scope 是一组参数取值下的编译环境：已编译的 let 按名称缓存，被多处引用时共享同一个节点。
type scope struct {
	combo   combination
	listed  []string // 列表参数名，用于在报错里写明是哪组参数取值
	lets    map[string]*Node
	letPos  map[string]int
	current int // 正在编译的 let 的位置；编译输出时为 len(lets)
	// memo 按语法树节点缓存编译结果。expr-lang 会让多个父节点共享同一个子节点（例如把 0 < e < 1 改写成两个比较、
	// 共用 e），不缓存的话嵌套几十层就会指数级重复编译。同一个语法节点只属于一条表达式，在同一组参数取值下结果确定。
	memo map[ast.Node]*Node
}

// describe 返回这组参数取值的说明，例如 “window=5”；没有列表参数时为空。
func (s *scope) describe() string {
	if len(s.listed) == 0 {
		return ""
	}
	parts := make([]string, 0, len(s.listed))
	for _, name := range s.listed {
		parts = append(parts, name+"="+formatFloat(s.combo[name].Value))
	}
	return strings.Join(parts, "，")
}

func (s *scope) annotate(err error) error {
	if described := s.describe(); described != "" {
		if e, ok := err.(*Error); ok {
			e.Message += "（参数 " + described + "）"
		}
	}
	return err
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
		return apply(src, node, fn, args, parts)
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
	if number, ok := sc.combo[name]; ok {
		return newNumber(number.Value, number.IsInt), nil
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
		if _, isParam := sc.combo[callee.Value]; isParam {
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
	return apply(src, call, fn, args, call.Arguments)
}

func applyOperator(src *exprSource, node ast.Node, op string, args []*Node, argNodes []ast.Node) (*Node, error) {
	fn, _ := LookupOperator(op)
	return apply(src, node, fn, args, argNodes)
}

// apply 检查参数类型、提取常量参数、折叠常量，并构造算子节点。argNodes 与 args 一一对应，用于报错定位。
func apply(src *exprSource, node ast.Node, fn *Function, args []*Node, argNodes []ast.Node) (*Node, error) {
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
			} else {
				result.Ints = append(result.Ints, int(spec.Default))
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
			if arg.Kind == KindNumberConst {
				return nil, src.errAt(at, "%s%s 必须依赖输入列，不能是常量", label, spec.Name)
			}
			if arg.Kind != KindNumber {
				return nil, src.errAt(at, "%s%s 需要数值序列，得到%s", label, spec.Name, arg.Kind.describe())
			}
		case ArgBool:
			if !arg.Kind.isBool() {
				return nil, src.errAt(at, "%s%s 需要布尔值，得到%s；数值请先比较，例如 x > 0", label, spec.Name, arg.Kind.describe())
			}
		case ArgBoolSeries:
			if arg.Kind == KindBoolConst {
				return nil, src.errAt(at, "%s%s 必须依赖输入列，不能是常量", label, spec.Name)
			}
			if arg.Kind != KindBool {
				return nil, src.errAt(at, "%s%s 需要布尔序列，得到%s", label, spec.Name, arg.Kind.describe())
			}
		case ArgWindow, ArgInteger:
			if arg.Kind != KindNumberConst || !arg.IsInt || arg.Value < spec.Min || arg.Value > spec.Max {
				return nil, src.errAt(at, "%s%s 必须是 %s～%s 的整数常量", label, spec.Name, formatFloat(spec.Min), formatFloat(spec.Max))
			}
			result.Ints = append(result.Ints, int(arg.Value))
			continue
		case ArgConstant:
			if arg.Kind != KindNumberConst || arg.Value < spec.Min || arg.Value > spec.Max {
				return nil, src.errAt(at, "%s%s 必须是 %s～%s 之间的常量", label, spec.Name, formatFloat(spec.Min), formatFloat(spec.Max))
			}
			result.Floats = append(result.Floats, arg.Value)
			continue
		}
		result.Args = append(result.Args, arg)
	}
	if fn.validate != nil {
		if reason := fn.validate(result.Ints, result.Floats); reason != "" {
			return nil, src.errAt(node, "%s", reason)
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
	childLookback := 0
	for _, arg := range result.Args {
		childLookback = max(childLookback, arg.Lookback)
		result.size = addSize(result.size, arg.size)
		result.cross = result.cross || arg.cross
	}
	result.Lookback = childLookback + fn.Back(result.Ints)
	if result.Lookback > MaxLookback {
		return nil, src.errAt(node, "回看 %d 个周期，超过上限 %d", result.Lookback, MaxLookback)
	}
	return result, nil
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

// Output 是展开后的一个输出列。
type Output struct {
	Name string
	Root *Node
}

// Factor 是编译后的因子：输出列的表达式树与推导出的元数据（设计 6.3）。
type Factor struct {
	Spec         *Spec
	Outputs      []Output
	InputColumns []string
	Lookback     int
	Type         FactorType
}

// Compile 解析并编译一份定义文本。
func Compile(text string) (*Factor, error) {
	spec, err := ParseSpec(text)
	if err != nil {
		return nil, err
	}
	return CompileSpec(spec)
}

// CompileSpec 编译已解析的定义：展开参数，按每组参数取值编译 let 与输出，推导输入列、回看与类型。
func CompileSpec(spec *Spec) (*Factor, error) {
	if err := checkExpansionSize(spec); err != nil {
		return nil, err
	}
	combos := combinations(spec)
	expanded, err := expandOutputs(spec, combos)
	if err != nil {
		return nil, err
	}
	lets := make([]*parsedBinding, len(spec.Lets))
	letPos := make(map[string]int, len(spec.Lets))
	for i, binding := range spec.Lets {
		if lets[i], err = parseBinding("let."+binding.Name, binding); err != nil {
			return nil, err
		}
		letPos[binding.Name] = i
	}
	outputs := make(map[string]*parsedBinding, len(spec.Outputs))
	for _, binding := range spec.Outputs {
		if outputs[binding.Name], err = parseBinding("outputs."+binding.Name, binding); err != nil {
			return nil, err
		}
	}
	var listed []string
	for _, param := range spec.Params {
		if param.List {
			listed = append(listed, param.Name)
		}
	}
	scopes := make([]*scope, len(combos))
	scopeFor := func(index int) (*scope, error) {
		if scopes[index] != nil {
			return scopes[index], nil
		}
		sc := &scope{combo: combos[index], listed: listed, lets: make(map[string]*Node, len(lets)), letPos: letPos,
			memo: make(map[ast.Node]*Node)}
		for i, let := range lets {
			sc.current = i
			node, err := lower(let.src, let.root, sc)
			if err != nil {
				return nil, sc.annotate(err)
			}
			sc.lets[let.binding.Name] = node
		}
		sc.current = len(lets)
		scopes[index] = sc
		return sc, nil
	}

	factor := &Factor{Spec: spec}
	total := 0
	for _, output := range expanded {
		sc, err := scopeFor(output.combo)
		if err != nil {
			return nil, err
		}
		parsed := outputs[output.binding.Name]
		root, err := lower(parsed.src, parsed.root, sc)
		if err != nil {
			return nil, sc.annotate(err)
		}
		switch root.Kind {
		case KindNumberConst:
			return nil, sc.annotate(parsed.src.errAt(parsed.root, "输出必须依赖输入列，不能是常量"))
		case KindBool, KindBoolConst:
			return nil, sc.annotate(parsed.src.errAt(parsed.root, "输出必须是数值；布尔值请用 where(c, 1, 0) 转换"))
		}
		total = addSize(total, root.size)
		if total > MaxFactorNodes {
			return nil, errorf("outputs", 0, "展开参数、内联 let 之后的表达式节点超过 %d 个", MaxFactorNodes)
		}
		factor.Outputs = append(factor.Outputs, Output{Name: output.name, Root: root})
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

// OutputNames 返回展开后的输出列名，保持定义中的顺序。
func (f *Factor) OutputNames() []string {
	names := make([]string, len(f.Outputs))
	for i, output := range f.Outputs {
		names[i] = output.Name
	}
	return names
}

// CanonicalText 返回规范程序文本：按输出顺序逐行写出“输出名 = 规范表达式”（设计 9.3）。
// definition_hash 由它与 ExprVersion、allow_partial_universe 一起计算。
func (f *Factor) CanonicalText() string {
	var b strings.Builder
	for _, output := range f.Outputs {
		b.WriteString(output.Name)
		b.WriteString(" = ")
		output.Root.writeCanonical(&b)
		b.WriteByte('\n')
	}
	return b.String()
}

// ExprVersion 是算子语义版本（设计 9.3）：任何算子的语义变化（包括数值行为的修正）都要递增，只做性能优化时不变。
const ExprVersion = "expr/1"

func (f *Factor) String() string {
	return fmt.Sprintf("factor(type=%s, lookback=%d, inputs=%s, outputs=%s)", f.Type, f.Lookback,
		strings.Join(f.InputColumns, ","), strconv.Quote(strings.Join(f.OutputNames(), ",")))
}
