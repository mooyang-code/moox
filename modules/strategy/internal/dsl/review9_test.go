package dsl

import (
	"errors"
	"strings"
	"testing"

	"github.com/expr-lang/expr/ast"
)

// 在编译时能通过、求值时却必然出错的运算符，保存时就拒绝并说明原因：?? 的左边是不会为空的列值；范围 .. 与 in 会为每一行
// 分配整段整数数组（范围过大时直接超出内存预算）。
func TestUnsupportedOperatorsAreRejectedAtSave(t *testing.T) {
	for source, want := range map[string]string{
		"close ?? true":          "不允许使用 ??",
		"instrument_id ?? close": "不允许使用 ??",
		"close in 1..9":          "不允许使用范围",
		"close not in 1..9":      "不允许使用范围",
		"close in 1..9999999999": "不允许使用范围",
	} {
		if _, err := Analyze(source, StageFilter); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q 应在保存时被拒绝（%s）：%v", source, want, err)
		}
	}
	// 与其他运算符混用 ?? 是库的语法错误，同样给出中文说明。
	if _, err := Analyze("close ?? 1 > 0", StageFilter); err == nil || !strings.Contains(err.Error(), "不允许使用 ??") {
		t.Fatalf("?? 与其他运算符混用应说明不允许使用 ??：%v", err)
	}
}

// matches 的正则字面量在保存时就校验写法，给出中文原因与写错的正则；写对的正则通过。
func TestMatchesRegexpIsCheckedAtSave(t *testing.T) {
	for pattern, want := range map[string]string{
		`"(("`:  "缺少右括号",
		`"a)"`:  "多了一个右括号",
		`"[a"`:  "缺少右方括号",
		`"*a"`:  "前面没有可重复的内容",
		`"a\\"`: "末尾多了一个反斜杠",
	} {
		_, err := Analyze("instrument_id matches "+pattern, StageFilter)
		if err == nil || !strings.Contains(err.Error(), "matches 的正则表达式无效") || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "missing") {
			t.Fatalf("正则 %s 应在保存时给出中文原因（%s）：%v", pattern, want, err)
		}
		var described *describedError
		if !errors.As(err, &described) || described.Unwrap() == nil {
			t.Fatalf("库的英文原文应留在 Unwrap 里供日志使用：%v", err)
		}
	}
	if _, err := Analyze(`instrument_id matches "^BTC-.*USDT$"`, StageFilter); err != nil {
		t.Fatalf("写对的正则应通过：%v", err)
	}
}

// 三元表达式的两个分支类型必须一致：库编译能通过，却只在走到类型不符的分支的行上求值失败，整期结果时好时坏。
func TestConditionalBranchesMustAgree(t *testing.T) {
	columns := map[string]struct{}{"close": {}}
	for _, tc := range []struct {
		source string
		stage  Stage
	}{
		{"close > 1 ? 1 : true", StageFilter},
		{"close > 1 ? true : 1", StageFilter},
		{"close > 1 ? \"a\" : 2", StageScore},
		{"true ? 1 : true", StageFilter},
		{"(close > 1 ? close : \"a\") == 1", StageFilter},
		{"close > 1 ? (close > 2 ? 1 : true) : 3", StageScore},
	} {
		if _, err := CompileExpression(tc.source, tc.stage, columns); err == nil || !strings.Contains(err.Error(), "三元表达式") {
			t.Fatalf("%q 的分支类型不一致应在编译时被拒绝：%v", tc.source, err)
		}
	}
	for _, tc := range []struct {
		source string
		stage  Stage
	}{
		{"close > 1 ? true : false", StageFilter},
		{"close > 1 ? close : 2", StageScore},
		{"close > 1 ? 1 : 2.5", StageScore},
		{"close > 1 ? (close > 2 ? 1 : 2) : 3", StageScore},
		{"(close > 1 ? \"a\" : \"b\") == \"a\"", StageFilter},
	} {
		if _, err := CompileExpression(tc.source, tc.stage, columns); err != nil {
			t.Fatalf("%q 的分支类型一致应通过：%v", tc.source, err)
		}
	}
}

// 此前只剩类别的报错补了原因：三元条件不是布尔、数字写法无效或超出范围；没有匹配到具体提示时给通用说明。
func TestExpressionErrorsCarryReasons(t *testing.T) {
	columns := map[string]struct{}{"close": {}}
	if _, err := CompileExpression("close ? 1 : 2", StageFilter, columns); err == nil || !strings.Contains(err.Error(), "条件必须是布尔值") {
		t.Fatalf("三元条件不是布尔值应说明原因：%v", err)
	}
	for _, source := range []string{"close > 1e400", "close > 99999999999999999999", "close > 0x"} {
		if _, err := Analyze(source, StageFilter); err == nil || !strings.Contains(err.Error(), "数字写法无效") {
			t.Fatalf("%q 应说明数字写法无效：%v", source, err)
		}
	}
	if err := exprError("语法错误", errors.New("something unexpected"), false); err.Error() != "语法错误：表达式写法不合法" {
		t.Fatalf("没有匹配的提示时应给通用说明：%v", err)
	}
	if err := exprError("编译失败", errors.New("something unexpected"), false); err.Error() != "编译失败：表达式的运算符或操作数类型不合法" {
		t.Fatalf("没有匹配的提示时应给通用说明：%v", err)
	}
}

// 两个分支的类型都无法确定时也拒绝（无法证明一致），而不是因为“都是未知”而相等就放行；正则错误不是语法错误类型时给通用说明。
func TestUnknownBranchTypesAndRegexpReasonFallback(t *testing.T) {
	var node ast.Node = &ast.ConditionalNode{Cond: &ast.BoolNode{Value: true}, Exp1: &ast.NilNode{}, Exp2: &ast.NilNode{}}
	if err := checkConditionals(node); err == nil || !strings.Contains(err.Error(), "三元表达式") {
		t.Fatalf("两个分支类型都未知时应拒绝：%v", err)
	}
	if got := regexpReason(errors.New("not a syntax error")); got != "写法不合法" {
		t.Fatalf("不是语法错误类型时应给通用说明：%q", got)
	}
}
