package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const baseSpec = `# 动量
params: {window: 20}
let:
  r: close / delay(close, window) - 1
outputs:
  mom_{window}: r
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

// DefinitionHash 是计算契约的指纹：改窗口、改表达式、改输出名、改 allow_partial_universe 都要变（设计 9.3）。
func TestDefinitionHashChangesWithEveryContractField(t *testing.T) {
	want := DefinitionHash(baseFactor(t))
	require.NotEmpty(t, want)
	require.Equal(t, want, DefinitionHash(baseFactor(t)), "同一定义的指纹必须稳定")
	changes := map[string]string{
		"窗口":  strings.Replace(baseSpec, "window: 20", "window: 30", 1),
		"表达式": strings.Replace(baseSpec, "close / delay", "open / delay", 1),
		"输出名": strings.Replace(baseSpec, "mom_{window}", "momentum_{window}", 1),
	}
	for name, spec := range changes {
		require.NotEqual(t, want, DefinitionHash(withSpec(t, spec, false)), name)
	}
	cross := `outputs:
  r: cs_rank(close)
`
	require.NotEqual(t, DefinitionHash(withSpec(t, cross, false)), DefinitionHash(withSpec(t, cross, true)), "allow_partial_universe")
}

// 不影响结果的改动（标识、名称、时间戳、注释、格式、let 名）不改变指纹，但会改变 SpecHash。
func TestDefinitionHashIgnoresNonContractChanges(t *testing.T) {
	base := baseFactor(t)
	want := DefinitionHash(base)
	same := withSpec(t, `params:
  window: 20
let:
  ret: close/delay(close,window)-1   # 改了 let 名与格式
outputs:
  mom_{window}: ret
`, false)
	same.FactorID, same.Name = "other", "另一个名字"
	same.CreatedAt, same.UpdatedAt = time.Unix(1, 0), time.Unix(2, 0)
	require.Equal(t, want, DefinitionHash(same))
	require.NotEqual(t, base.SpecHash, same.SpecHash, "SpecHash 跟随原文")
	// 编译失败（expr 版本不一致）时按原文派生稳定、非空的指纹：周期标记与账本要求指纹非空。
	broken := FactorDef{Spec: "outputs: {x: nope(close)}"}
	require.NotEmpty(t, DefinitionHash(broken))
	require.Equal(t, DefinitionHash(broken), DefinitionHash(broken))
	require.NotEqual(t, DefinitionHash(broken), DefinitionHash(FactorDef{Spec: "outputs: {x: nope(open)}"}))
	require.Empty(t, DefinitionHash(FactorDef{}))
}

func TestSpecHashIsStableAndPrefixed(t *testing.T) {
	got := SpecHash("outputs: {x: close}\n")
	require.True(t, strings.HasPrefix(got, "sha256:"))
	require.Equal(t, got, SpecHash("outputs: {x: close}\n"))
	require.NotEqual(t, got, SpecHash("outputs: {x: open}\n"))
}
