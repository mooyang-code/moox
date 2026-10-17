package factorexpr

import (
	"errors"
	"fmt"
	"strings"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/conf"
	"github.com/expr-lang/expr/file"
	"github.com/expr-lang/expr/parser"
)

// exprSource 是一条表达式原文及其在定义中的位置，用来把语法树节点的偏移换算成行列。
type exprSource struct {
	path    string
	docLine int
	text    string
}

// parsedBinding 是解析好的一条 let 或输出表达式。
type parsedBinding struct {
	binding Binding
	src     *exprSource
	root    ast.Node
}

// parseBinding 用 expr-lang 解析表达式：禁用全部内置函数与 if 语法，所有函数调用都解析成 CallNode，交给本包解析名称。
func parseBinding(path string, binding Binding) (*parsedBinding, error) {
	src := &exprSource{path: path, docLine: binding.Line, text: binding.Source}
	config := conf.CreateNew()
	config.MaxNodes = MaxExpressionNodes
	config.DisableIfOperator = true
	for name := range config.Builtins {
		config.Disabled[name] = true
	}
	tree, err := parser.ParseWithConfig(binding.Source, config)
	if err != nil {
		return nil, src.syntaxError(err)
	}
	return &parsedBinding{binding: binding, src: src, root: tree.Node}, nil
}

// errAt 返回定位到节点的诊断。
func (s *exprSource) errAt(node ast.Node, format string, args ...any) *Error {
	line, column := s.position(node.Location().From)
	return &Error{Path: s.path, DocLine: s.docLine, Line: line, Column: column, Message: fmt.Sprintf(format, args...)}
}

// position 把按字符计的偏移换算成从 1 开始的行列。
func (s *exprSource) position(offset int) (int, int) {
	line, column, count := 1, 1, 0
	for _, r := range s.text {
		if count == offset {
			break
		}
		if r == '\n' {
			line++
			column = 1
		} else {
			column++
		}
		count++
	}
	return line, column
}

var exprHints = []struct{ raw, text string }{
	{"exceeds maximum allowed nodes", fmt.Sprintf("表达式过于复杂：超过 %d 个语法节点", MaxExpressionNodes)},
	{"unexpected token", "出现了意外的符号，或表达式不完整"},
	{"literal not terminated", "字符串没有闭合"},
	{"unclosed", "括号没有闭合"},
	{"unrecognized character", "出现了无法识别的字符"},
	{"bad number syntax", "数字写法无效"},
	{"invalid float literal", "数字写法无效或超出范围"},
	{"invalid integer literal", "数字写法无效或超出范围"},
	{"float literal is too large", "数字超出范围"},
	{"expected at least", "参数太少"},
}

func (s *exprSource) syntaxError(err error) *Error {
	result := &Error{Path: s.path, DocLine: s.docLine, cause: err}
	text := err.Error()
	var located *file.Error
	if errors.As(err, &located) {
		text = located.Message
		if located.Line > 0 {
			result.Line, result.Column = located.Line, located.Column+1
		}
	}
	result.Message = "语法错误：表达式写法不合法"
	for _, hint := range exprHints {
		if strings.Contains(text, hint.raw) {
			result.Message = "语法错误：" + hint.text
			break
		}
	}
	return result
}

// rejectNode 对白名单之外的语法给出中文原因（设计 4.1）。
func rejectNode(src *exprSource, node ast.Node) *Error {
	switch n := node.(type) {
	case *ast.StringNode:
		return src.errAt(node, "不允许使用字符串")
	case *ast.NilNode:
		return src.errAt(node, "不允许使用 nil")
	case *ast.BuiltinNode:
		return src.errAt(node, "不支持 %s：只能使用因子函数，例如 ts_sum、ts_count", n.Name)
	case *ast.MemberNode:
		return src.errAt(node, "不允许使用成员访问或下标")
	case *ast.SliceNode:
		return src.errAt(node, "不允许使用切片")
	case *ast.ArrayNode, *ast.MapNode, *ast.PairNode:
		return src.errAt(node, "不允许使用数组或字典字面量")
	case *ast.ChainNode:
		return src.errAt(node, "不允许使用可选链 ?.")
	case *ast.PredicateNode, *ast.PointerNode:
		return src.errAt(node, "不允许使用谓词闭包")
	case *ast.VariableDeclaratorNode, *ast.SequenceNode:
		return src.errAt(node, "不允许在表达式里写 let 或多条语句，中间量请写在 let 段")
	}
	return src.errAt(node, "不支持这种写法")
}

// binaryOperators 把表达式里的运算符映射到算子名。
var binaryOperators = map[string]string{
	"+": "add", "-": "sub", "*": "mul", "/": "div", "%": "mod", "**": "pow", "^": "pow",
	"==": "eq", "!=": "ne", "<": "lt", "<=": "le", ">": "gt", ">=": "ge",
	"&&": "and", "and": "and", "||": "or", "or": "or",
}

var rejectedOperators = map[string]string{
	"in":         "不允许使用 in",
	"..":         "不允许使用范围 ..",
	"??":         "不允许使用 ??",
	"|":          "不允许使用管道 |",
	"matches":    "不允许使用字符串运算 matches",
	"contains":   "不允许使用字符串运算 contains",
	"startsWith": "不允许使用字符串运算 startsWith",
	"endsWith":   "不允许使用字符串运算 endsWith",
}

// operatorSymbols 用于报错时还原用户写下的运算符。
var operatorSymbols = map[string]string{
	"add": "+", "sub": "-", "mul": "*", "div": "/", "mod": "%", "pow": "**", "neg": "-", "not": "!",
	"eq": "==", "ne": "!=", "lt": "<", "le": "<=", "gt": ">", "ge": ">=", "and": "&&", "or": "||",
}
