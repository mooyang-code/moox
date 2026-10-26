package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const baseSpec = `# 动量
params: {window: {type: int, min: 1, max: 60, default: 20}}
let:
  r: close / delay(close, window) - 1
output: r
`

func baseFactor(t *testing.T) FactorDef {
	t.Helper()
	def, err := NormalizeFactorDefinition(FactorDef{FactorID: "mom", Name: "动量", Spec: baseSpec})
	require.NoError(t, err)
	return def
}

func withSpec(t *testing.T, spec string, partial bool) FactorDef {
	t.Helper()
	def, err := NormalizeFactorDefinition(FactorDef{FactorID: "mom", Name: "动量", Spec: spec, AllowPartialUniverse: partial})
	require.NoError(t, err)
	return def
}

// DefinitionHash 是计算契约的指纹：改参数声明、改表达式、改输出名、改 allow_partial_universe 都要变（设计 9.3）。
func TestDefinitionHashChangesWithEveryContractField(t *testing.T) {
	want := DefinitionHash(baseFactor(t))
	require.NotEmpty(t, want)
	require.Equal(t, want, DefinitionHash(baseFactor(t)), "同一定义的指纹必须稳定")
	changes := map[string]string{
		"默认窗口": strings.Replace(baseSpec, "default: 20", "default: 30", 1),
		"窗口范围": strings.Replace(baseSpec, "max: 60", "max: 90", 1),
		"表达式":  strings.Replace(baseSpec, "close / delay", "open / delay", 1),
	}
	for name, spec := range changes {
		require.NotEqual(t, want, DefinitionHash(withSpec(t, spec, false)), name)
	}
	cross := `output: cs_rank(close)
`
	require.NotEqual(t, DefinitionHash(withSpec(t, cross, false)), DefinitionHash(withSpec(t, cross, true)), "allow_partial_universe")
}

// 不影响结果的改动（标识、名称、分类、时间戳、注释、格式、let 名）不改变指纹，但会改变 SpecHash。
func TestDefinitionHashIgnoresNonContractChanges(t *testing.T) {
	base := baseFactor(t)
	want := DefinitionHash(base)
	same := withSpec(t, `params:
  window: {type: int, min: 1, max: 60, default: 20}
let:
  ret: close/delay(close,window)-1   # 改了 let 名与格式
output: ret
`, false)
	same.FactorID, same.Name, same.CategoryID = "other", "另一个名字", "momentum"
	same.CreatedAt, same.UpdatedAt = time.Unix(1, 0), time.Unix(2, 0)
	require.Equal(t, want, DefinitionHash(same))
	require.NotEqual(t, base.SpecHash, same.SpecHash, "SpecHash 跟随原文")
	// 编译失败（expr 版本不一致）时按原文派生稳定、非空的指纹：周期标记与账本要求指纹非空。
	broken := FactorDef{Spec: "output: nope(close)"}
	require.NotEmpty(t, DefinitionHash(broken))
	require.Equal(t, DefinitionHash(broken), DefinitionHash(broken))
	require.NotEqual(t, DefinitionHash(broken), DefinitionHash(FactorDef{Spec: "output: nope(open)"}))
	require.Empty(t, DefinitionHash(FactorDef{}))
}

func TestSpecHashIsStableAndPrefixed(t *testing.T) {
	got := SpecHash("output: close\n")
	require.True(t, strings.HasPrefix(got, "sha256:"))
	require.Equal(t, got, SpecHash("output: close\n"))
	require.NotEqual(t, got, SpecHash("output: open\n"))
}
