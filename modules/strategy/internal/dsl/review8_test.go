package dsl

import (
	"strings"
	"testing"
)

// matches 的正则只能是字符串字面量：用数据（例如 instrument_id）当正则，标的 ID 里的括号会让整期在求值时失败；字面量的
// 正则在保存时就校验，写错时给出中文原因。
func TestMatchesRequiresLiteralPattern(t *testing.T) {
	_, err := Parse([]byte(`name: x
rules:
  - {id: r, type: signal, pool: [BTC-USDT], entry: "instrument_id matches instrument_id", exit: "close < 1", weight: 1}
`))
	if err == nil || !strings.Contains(err.Error(), "matches 右边只能是字符串字面量") {
		t.Fatalf("非字面量的正则应拒绝：%v", err)
	}
	_, err = Parse([]byte(`name: x
rules:
  - {id: r, type: signal, pool: [BTC-USDT], entry: "instrument_id matches \"((\"", exit: "close < 1", weight: 1}
`))
	if err == nil || !strings.Contains(err.Error(), "正则表达式无效") || strings.Contains(err.Error(), "missing") {
		t.Fatalf("写错的正则应在保存时给出中文原因：%v", err)
	}
}
