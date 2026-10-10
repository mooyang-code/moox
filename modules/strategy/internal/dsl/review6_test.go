package dsl

import (
	"errors"
	"strings"
	"testing"
)

// rawCause 返回错误链最内层的原文。
func rawCause(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}

// YAML 语法错误给出行号与中文原因，解析器的英文原文只经 Unwrap 留给日志。
func TestYAMLErrorsAreChinese(t *testing.T) {
	_, err := Parse([]byte("name: x\nrules:\n  - id: r\n    type: rank: bad\n"))
	if err == nil || !strings.Contains(err.Error(), "不是合法的 YAML（第 4 行附近）") || strings.Contains(err.Error(), "mapping values") {
		t.Fatalf("YAML 错误应是带行号的中文说明：%v", err)
	}
	if raw := rawCause(err); raw == nil || !strings.Contains(raw.Error(), "yaml") {
		t.Fatalf("英文原文应留给日志：%v", raw)
	}
}

// 表达式语法错误给出行列与中文原因；未知的名称给出名称本身。
func TestExpressionErrorsAreChinese(t *testing.T) {
	_, err := Parse([]byte(`name: x
rules:
  - {id: r, type: rank, score: "close +", select: {top: 1}, weight: 1}
`))
	if err == nil || !strings.Contains(err.Error(), "语法错误（第 1 行第") || strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("表达式语法错误应是带位置的中文说明：%v", err)
	}
	if raw := rawCause(err); raw == nil || raw.Error() == err.Error() {
		t.Fatalf("表达式库的原文应留给日志：%v", raw)
	}
	described := exprError("编译失败", errors.New("unknown name foo (1:1)"), false)
	if described.Error() != "编译失败：未知的名称 foo" {
		t.Fatalf("未知名称应给出名称：%v", described)
	}
}
