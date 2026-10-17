// Package factorexpr 是因子表达式引擎：解析 YAML 定义（params / let / outputs），把 BRAIN 风格的表达式编译成
// 带类型的表达式树，推导输入列、回看与因子类型，并把一个因子集的全部因子合并成去重后的计算图。
// 设计见 docs/计划/因子表达式引擎详细设计.md。
package factorexpr

import (
	"fmt"
	"strings"
)

// Error 是编译诊断。Path 指出出错的位置（例如 outputs.cci、let.tp、params.window），DocLine 是它在定义文本中的行号，
// Line、Column 是表达式内部的行列（从 1 开始；0 表示不适用）。
type Error struct {
	Path    string
	DocLine int
	Line    int
	Column  int
	Message string
	cause   error
}

func (e *Error) Error() string {
	var b strings.Builder
	if e.Path != "" {
		b.WriteString(e.Path)
		var where []string
		if e.DocLine > 0 {
			where = append(where, fmt.Sprintf("定义第 %d 行", e.DocLine))
		}
		if e.Line > 0 {
			where = append(where, fmt.Sprintf("表达式第 %d 行第 %d 列", e.Line, e.Column))
		}
		if len(where) > 0 {
			b.WriteString("（" + strings.Join(where, "，") + "）")
		}
		b.WriteString("：")
	} else if e.DocLine > 0 {
		b.WriteString(fmt.Sprintf("定义第 %d 行：", e.DocLine))
	}
	b.WriteString(e.Message)
	return b.String()
}

// Unwrap 返回第三方库的原始错误（英文原文只留给日志）。
func (e *Error) Unwrap() error { return e.cause }

func errorf(path string, docLine int, format string, args ...any) *Error {
	return &Error{Path: path, DocLine: docLine, Message: fmt.Sprintf(format, args...)}
}
