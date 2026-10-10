package engine

import (
	"errors"
	"testing"
)

// 分数的运行错误写成中文原因，表达式库的英文原文不进入解释明细。
func TestScoreErrorReasonIsChinese(t *testing.T) {
	for raw, want := range map[string]string{
		"error parsing regexp: missing closing ) (1:1)": "score_error:求值出错（正则表达式无效）",
		"something unexpected happened":                 "score_error:求值出错",
	} {
		if got := scoreErrorReason(errors.New(raw)); got != want {
			t.Fatalf("%q 应写成 %q：%q", raw, want, got)
		}
	}
}
