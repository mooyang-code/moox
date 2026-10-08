package dsl

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
	"github.com/expr-lang/expr/vm"
)

// Stage 是表达式所在的阶段，决定返回类型与可用标识符。
type Stage string

const (
	StageFilter      Stage = "filter"
	StageScore       Stage = "score"
	StageSelectWhere Stage = "select.where"
	StageFilterAfter Stage = "filter_after"
	StageEntry       Stage = "entry"
	StageExit        Stage = "exit"
	// stageInner 是 rank/zscore 的参数表达式：数值，可继续嵌套截面函数。
	stageInner Stage = "inner"
)

const (
	normalizerRank   = "rank"
	normalizerZScore = "zscore"
	normalizerPrefix = "__norm_"

	identifierBars       = "bars"
	identifierScore      = "score"
	identifierInstrument = "instrument_id"

	maxExpressionNodes = 256
	// maxExpressionLength 限制单条表达式的字符数，同时限制了括号嵌套深度，避免递归解析耗尽栈。
	maxExpressionLength = 1024
)

// Normalizer 是一次截面预计算：先对样本内每行求 Inner，再把 rank 或 zscore 的结果写入 Variable。
type Normalizer struct {
	Variable string
	Kind     string
	Inner    *Expression
}

// Expression 是一条已分析（可选已编译）的表达式。
type Expression struct {
	Source          string
	Stage           Stage
	Rewritten       string
	Program         *vm.Program
	Normalizers     []*Normalizer
	Columns         []string // 直接引用的当期列（不含截面函数参数内部的引用）
	PreviousColumns []string // 通过 bars[-1] 引用的上一根列
	UsesScore       bool
	Numeric         bool
}

// AllColumns 返回包括截面函数参数在内的全部当期列。
func (e *Expression) AllColumns() []string {
	set := make(map[string]struct{})
	e.collectColumns(set, false)
	return sortedKeys(set)
}

// AllPreviousColumns 返回包括截面函数参数在内的全部上一根列。
func (e *Expression) AllPreviousColumns() []string {
	set := make(map[string]struct{})
	e.collectColumns(set, true)
	return sortedKeys(set)
}

func (e *Expression) collectColumns(set map[string]struct{}, previous bool) {
	if e == nil {
		return
	}
	source := e.Columns
	if previous {
		source = e.PreviousColumns
	}
	for _, column := range source {
		set[column] = struct{}{}
	}
	for _, normalizer := range e.Normalizers {
		normalizer.Inner.collectColumns(set, previous)
	}
}

// Run 对一行环境求值。env 由引擎构造：列名 → float64，bars → []map[string]float64{当期, 上一根}，
// score、instrument_id 与各 Normalizer.Variable。
func (e *Expression) Run(env map[string]any) (any, error) {
	if e == nil || e.Program == nil {
		return nil, errors.New("表达式尚未编译")
	}
	return expr.Run(e.Program, env)
}

// Analyze 解析并校验一条表达式，不编译。用于保存 DSL 时的语法检查。
func Analyze(source string, stage Stage) (*Expression, error) {
	return analyze(source, stage)
}

// CompileExpression 分析并编译一条表达式。columns 是绑定 View 的全部列，引用不存在的列会报错。
func CompileExpression(source string, stage Stage, columns map[string]struct{}) (*Expression, error) {
	expression, err := analyze(source, stage)
	if err != nil {
		return nil, err
	}
	if err := compileAnalyzed(expression, columns); err != nil {
		return nil, err
	}
	return expression, nil
}

func analyze(source string, stage Stage) (*Expression, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, errors.New("表达式为空")
	}
	if len(source) > maxExpressionLength {
		return nil, fmt.Errorf("表达式超过 %d 个字符", maxExpressionLength)
	}
	tree, err := parser.Parse(source)
	if err != nil {
		return nil, fmt.Errorf("语法错误：%w", err)
	}
	expression := &Expression{Source: source, Stage: stage, Numeric: stage == StageScore || stage == stageInner}
	w := &walker{stage: stage, expression: expression, columns: map[string]struct{}{}, previous: map[string]struct{}{}}
	if err := w.visit(&tree.Node); err != nil {
		return nil, err
	}
	if w.barsIdentifiers != w.barsAccesses {
		return nil, errors.New("bars 必须以 bars[0].列名 或 bars[-1].列名 的形式使用")
	}
	expression.Columns = sortedKeys(w.columns)
	expression.PreviousColumns = sortedKeys(w.previous)
	expression.Rewritten = tree.Node.String()
	for _, spec := range w.normalizers {
		inner, err := analyze(spec.innerSource, stageInner)
		if err != nil {
			return nil, fmt.Errorf("%s 的参数无效：%w", spec.kind, err)
		}
		expression.Normalizers = append(expression.Normalizers, &Normalizer{Variable: spec.variable, Kind: spec.kind, Inner: inner})
	}
	return expression, nil
}

func compileAnalyzed(expression *Expression, columns map[string]struct{}) error {
	for _, column := range expression.Columns {
		if _, ok := columns[column]; !ok {
			return fmt.Errorf("列 %q 不存在于绑定的 View", column)
		}
	}
	for _, column := range expression.PreviousColumns {
		if _, ok := columns[column]; !ok {
			return fmt.Errorf("列 %q 不存在于绑定的 View", column)
		}
	}
	for _, normalizer := range expression.Normalizers {
		if err := compileAnalyzed(normalizer.Inner, columns); err != nil {
			return err
		}
	}
	env := make(map[string]any, len(columns)+len(expression.Normalizers)+3)
	for column := range columns {
		env[column] = float64(0)
	}
	env[identifierBars] = []map[string]float64{}
	env[identifierScore] = float64(0)
	env[identifierInstrument] = ""
	for _, normalizer := range expression.Normalizers {
		env[normalizer.Variable] = float64(0)
	}
	options := []expr.Option{expr.Env(env), expr.DisableAllBuiltins(), expr.MaxNodes(maxExpressionNodes)}
	if expression.Numeric {
		options = append(options, expr.AsFloat64())
	} else {
		options = append(options, expr.AsBool())
	}
	program, err := expr.Compile(expression.Rewritten, options...)
	if err != nil {
		return fmt.Errorf("编译失败：%w", err)
	}
	expression.Program = program
	return nil
}

type normalizerSpec struct {
	variable    string
	kind        string
	innerSource string
}

// walker 以先序遍历检查节点类型、收集依赖，并就地改写 AST：
// rank(x) / zscore(x) 改写为预计算变量，bars[-1] 改写为 bars[1]（环境中第二个元素）。
type walker struct {
	stage           Stage
	expression      *Expression
	columns         map[string]struct{}
	previous        map[string]struct{}
	normalizers     []normalizerSpec
	barsIdentifiers int
	barsAccesses    int
}

func (w *walker) visit(node *ast.Node) error {
	switch n := (*node).(type) {
	case *ast.IdentifierNode:
		return w.identifier(n)
	case *ast.IntegerNode, *ast.FloatNode, *ast.BoolNode, *ast.StringNode:
		return nil
	case *ast.UnaryNode:
		return w.visit(&n.Node)
	case *ast.BinaryNode:
		if err := w.visit(&n.Left); err != nil {
			return err
		}
		return w.visit(&n.Right)
	case *ast.ConditionalNode:
		for _, child := range []*ast.Node{&n.Cond, &n.Exp1, &n.Exp2} {
			if err := w.visit(child); err != nil {
				return err
			}
		}
		return nil
	case *ast.MemberNode:
		return w.member(n)
	case *ast.CallNode:
		return w.call(node, n)
	case *ast.BuiltinNode:
		return fmt.Errorf("不允许使用内置函数 %s", n.Name)
	case *ast.NilNode:
		return errors.New("不允许使用 nil")
	case *ast.ChainNode:
		return errors.New("不允许使用可选链 ?.")
	case *ast.SliceNode:
		return errors.New("不允许使用切片")
	case *ast.ArrayNode, *ast.MapNode, *ast.PairNode:
		return errors.New("不允许使用数组或字典字面量")
	case *ast.PredicateNode, *ast.PointerNode:
		return errors.New("不允许使用谓词闭包")
	case *ast.VariableDeclaratorNode, *ast.SequenceNode:
		return errors.New("不允许使用 let 或多条语句")
	default:
		return fmt.Errorf("不支持的表达式节点 %T", *node)
	}
}

func (w *walker) identifier(n *ast.IdentifierNode) error {
	switch n.Value {
	case identifierBars:
		w.barsIdentifiers++
		return nil
	case identifierScore:
		if w.stage != StageSelectWhere && w.stage != StageFilterAfter {
			return fmt.Errorf("score 只能在 select.where 和 filter_after 中使用，不能在 %s 中使用", w.stage)
		}
		w.expression.UsesScore = true
		return nil
	case identifierInstrument:
		return nil
	}
	if strings.HasPrefix(n.Value, normalizerPrefix) || strings.HasPrefix(n.Value, "__") {
		return fmt.Errorf("标识符 %q 是保留名称", n.Value)
	}
	if n.Value == normalizerRank || n.Value == normalizerZScore {
		return fmt.Errorf("%s 必须作为函数调用使用，例如 %s(close)", n.Value, n.Value)
	}
	w.columns[n.Value] = struct{}{}
	return nil
}

func (w *walker) member(n *ast.MemberNode) error {
	inner, ok := n.Node.(*ast.MemberNode)
	if !ok {
		if isBarsIdentifier(n.Node) {
			return errors.New("bars 必须以 bars[0].列名 或 bars[-1].列名 的形式使用")
		}
		return errors.New("不允许使用属性访问，只能引用列名或 bars[0].列名、bars[-1].列名")
	}
	if !isBarsIdentifier(inner.Node) {
		return errors.New("不允许使用属性访问，只能引用列名或 bars[0].列名、bars[-1].列名")
	}
	offset, err := barOffset(inner.Property)
	if err != nil {
		return err
	}
	field, ok := memberName(n.Property)
	if !ok {
		return errors.New("bars[...] 之后必须跟列名")
	}
	if field == identifierScore || field == identifierInstrument || strings.HasPrefix(field, "__") {
		return fmt.Errorf("bars 不能访问 %q", field)
	}
	w.barsIdentifiers++
	w.barsAccesses++
	if offset == 0 {
		w.columns[field] = struct{}{}
	} else {
		w.previous[field] = struct{}{}
		// 环境中 bars 是 [当期, 上一根]，把 -1 改写为 1。
		ast.Patch(&inner.Property, &ast.IntegerNode{Value: 1})
	}
	return nil
}

func (w *walker) call(node *ast.Node, n *ast.CallNode) error {
	callee, ok := n.Callee.(*ast.IdentifierNode)
	if !ok {
		return errors.New("不允许调用方法")
	}
	if callee.Value != normalizerRank && callee.Value != normalizerZScore {
		return fmt.Errorf("不允许调用函数 %s，只支持 rank 和 zscore", callee.Value)
	}
	if w.stage != StageScore && w.stage != stageInner {
		return fmt.Errorf("%s 只能在 score 中使用", callee.Value)
	}
	if len(n.Arguments) != 1 {
		return fmt.Errorf("%s 需要且只需要一个参数", callee.Value)
	}
	spec := normalizerSpec{variable: fmt.Sprintf("%s%d", normalizerPrefix, len(w.normalizers)), kind: callee.Value, innerSource: n.Arguments[0].String()}
	w.normalizers = append(w.normalizers, spec)
	ast.Patch(node, &ast.IdentifierNode{Value: spec.variable})
	return nil
}

func isBarsIdentifier(node ast.Node) bool {
	identifier, ok := node.(*ast.IdentifierNode)
	return ok && identifier.Value == identifierBars
}

func barOffset(node ast.Node) (int, error) {
	switch n := node.(type) {
	case *ast.IntegerNode:
		if n.Value == 0 {
			return 0, nil
		}
		if n.Value == -1 {
			return -1, nil
		}
	case *ast.UnaryNode:
		if n.Operator == "-" {
			if value, ok := n.Node.(*ast.IntegerNode); ok && value.Value == 1 {
				return -1, nil
			}
		}
	}
	return 0, errors.New("bars 的下标只能是常量 0 或 -1")
}

func memberName(node ast.Node) (string, bool) {
	switch property := node.(type) {
	case *ast.IdentifierNode:
		return property.Value, property.Value != ""
	case *ast.StringNode:
		return property.Value, property.Value != ""
	default:
		return "", false
	}
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
