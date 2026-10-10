package engine

import (
	"errors"
	"testing"
)

// 分数的运行错误写成中文原因，表达式库的英文原文不进入解释明细。
func TestScoreErrorReasonIsChinese(t *testing.T) {
	for raw, want := range map[string]string{
		"runtime error: integer divide by zero (1:3)":  "score_error:除以零",
		"index out of range: 3 (array length 1) (1:1)": "score_error:下标越界",
		"something unexpected happened":                "score_error:求值出错",
	} {
		if got := scoreErrorReason(errors.New(raw)); got != want {
			t.Fatalf("%q 应写成 %q：%q", raw, want, got)
		}
	}
}
