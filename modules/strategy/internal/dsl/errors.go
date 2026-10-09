package dsl

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/expr-lang/expr/file"
)

// describedError 是换成中文说明的第三方错误：Error 只给说明（位置与常见原因），YAML 与表达式库的英文原文经 Unwrap
// 留给日志。
type describedError struct {
	message string
	cause   error
}

func (e *describedError) Error() string { return e.message }
func (e *describedError) Unwrap() error { return e.cause }

var yamlLinePattern = regexp.MustCompile(`line (\d+)`)

// yamlHints 是 YAML 解析器常见报错的中文说明，按出现顺序匹配第一条。
var yamlHints = []struct{ raw, text string }{
	{"mapping values are not allowed", "这里不能出现键值对，请检查缩进与冒号"},
	{"block sequence entries are not allowed", "这里不能出现列表项，请检查缩进"},
	{"did not find expected key", "缺少预期的键，请检查缩进"},
	{"did not find expected '-' indicator", "列表项的缩进不一致"},
	{"could not find expected ':'", "缺少冒号"},
	{"did not find expected node content", "缺少预期的值"},
	{"found character that cannot start any token", "出现了不能作为开头的字符（例如用 Tab 缩进）"},
	{"found unexpected end of stream", "文本意外结束（引号或括号没有闭合）"},
	{"found unknown escape character", "字符串里有无法识别的转义字符"},
	{"unknown anchor", "引用了未定义的锚点"},
	{"found undefined tag handle", "使用了未定义的标签"},
}

// yamlSyntaxError 把 YAML 解析错误换成中文说明：给出行号与常见原因。
func yamlSyntaxError(err error) error {
	raw := err.Error()
	message := "策略 DSL 不是合法的 YAML"
	if match := yamlLinePattern.FindStringSubmatch(raw); match != nil {
		message += "（第 " + match[1] + " 行附近）"
	}
	for _, hint := range yamlHints {
		if strings.Contains(raw, hint.raw) {
			message += "：" + hint.text
			break
		}
	}
	return &describedError{message: message, cause: err}
}

var unknownNamePattern = regexp.MustCompile(`unknown name ([A-Za-z_][A-Za-z0-9_]*)`)

// exprHints 是表达式库常见报错的中文说明，按出现顺序匹配第一条。
var exprHints = []struct{ raw, text string }{
	{"unexpected token", "出现了意外的符号或表达式不完整"},
	{"literal not terminated", "字符串没有闭合"},
	{"unclosed", "括号或引号没有闭合"},
	{"mismatched types", "运算两边的类型不匹配"},
	{"invalid operation", "运算的操作数类型不对"},
	{"expected bool", "结果应为布尔值"},
	{"expected float64", "结果应为数值"},
	{"not enough arguments", "参数太少"},
	{"too many arguments", "参数太多"},
	{"cannot use", "类型不匹配"},
	{"unknown func", "使用了不支持的函数"},
}

// exprError 把表达式库的错误换成中文说明。what 是错误类别（“语法错误”或“编译失败”）；withPosition 为 true 时给出
// 行列（只对用户原文的解析错误有意义，编译的是改写后的表达式，位置对不上原文）。
func exprError(what string, err error, withPosition bool) error {
	text := err.Error()
	position := ""
	var located *file.Error
	if errors.As(err, &located) {
		text = located.Message
		if withPosition && located.Line > 0 {
			position = fmt.Sprintf("（第 %d 行第 %d 列）", located.Line, located.Column+1)
		}
	}
	message := what + position
	if match := unknownNamePattern.FindStringSubmatch(text); match != nil {
		return &describedError{message: message + "：未知的名称 " + match[1], cause: err}
	}
	for _, hint := range exprHints {
		if strings.Contains(text, hint.raw) {
			return &describedError{message: message + "：" + hint.text, cause: err}
		}
	}
	return &describedError{message: message, cause: err}
}
