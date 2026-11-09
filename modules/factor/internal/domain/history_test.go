package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// cross_section_history：不写与写 current 的定义哈希相同（与第 0 期金样一致）；写 observed 时追加标记、哈希改变，
// 策略侧的成员契约据此报出 factor_changed。只改它的写法（注释、引号）不改变哈希。
func TestDefinitionHashCrossSectionHistory(t *testing.T) {
	plain := withSpec(t, "output: ts_mean(cs_rank(close), 5)\n", true)
	current := withSpec(t, "cross_section_history: current\noutput: ts_mean(cs_rank(close), 5)\n", true)
	observed := withSpec(t, "cross_section_history: observed\noutput: ts_mean(cs_rank(close), 5)\n", true)
	quoted := withSpec(t, "output: ts_mean(cs_rank(close), 5)\ncross_section_history: 'observed'  # 历史截面含已下架标的\n", true)

	require.Equal(t, DefinitionHash(plain), DefinitionHash(current))
	require.NotEqual(t, DefinitionHash(plain), DefinitionHash(observed))
	require.Equal(t, DefinitionHash(observed), DefinitionHash(quoted))
	require.False(t, plain.ObservedHistory())
	require.False(t, current.ObservedHistory())
	require.True(t, observed.ObservedHistory())
	// 没有编译结果（引擎目录里来自 JSON 快照的定义）时按 spec 解析。
	require.True(t, FactorDef{Spec: observed.Spec}.ObservedHistory())
	require.False(t, FactorDef{Spec: "output: nope(close)"}.ObservedHistory())
}

// 保存校验：只能写 current 或 observed；时序因子写 observed 没有意义，拒绝。
func TestCrossSectionHistoryValidation(t *testing.T) {
	for name, spec := range map[string]string{
		"未知取值":   "cross_section_history: all\noutput: cs_rank(close)\n",
		"不是字符串":  "cross_section_history: [observed]\noutput: cs_rank(close)\n",
		"空":      "cross_section_history:\noutput: cs_rank(close)\n",
		"时序因子":   "cross_section_history: observed\noutput: ts_mean(close, 5)\n",
		"大小写不同":  "cross_section_history: Observed\noutput: cs_rank(close)\n",
		"重复的键":   "cross_section_history: current\ncross_section_history: observed\noutput: cs_rank(close)\n",
		"数值":     "cross_section_history: 1\noutput: cs_rank(close)\n",
		"布尔":     "cross_section_history: true\noutput: cs_rank(close)\n",
		"写在 let": "let:\n  cross_section_history: close\noutput: cs_rank(cross_section_history)\n",
	} {
		_, err := NormalizeFactorDefinition(FactorDef{FactorID: "x", Name: "x", Spec: spec})
		if name == "写在 let" {
			require.NoError(t, err, "let 里同名的中间量与顶层键无关")
			continue
		}
		require.Error(t, err, name)
	}
}
