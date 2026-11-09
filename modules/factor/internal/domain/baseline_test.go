package domain

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/factorlib"
	"github.com/stretchr/testify/require"
)

// 基线金样（第 0 期）：因子库 modules/factor/factors/*.yaml 里全部定义的 definition_hash。
// 金样由当前代码生成，之后的改动在不写新字段时必须让每个哈希逐位不变。默认只比对；
// 有意改变计算契约时用 UPDATE_BASELINE=1 重新生成，并在 PR 里说明原因。

const baselineHashesPath = "testdata/baseline/definition_hashes.json"

// baselineUpdate 报告本次是否重新生成基线金样。
func baselineUpdate() bool { return os.Getenv("UPDATE_BASELINE") != "" }

// baselineLibraryDefinitions 按管理端导入因子库的同一条路径（factorlib 读取 → NormalizeFactorDefinition）得到库里的全部定义。
func baselineLibraryDefinitions(t *testing.T) []FactorDef {
	t.Helper()
	entries, err := factorlib.LoadDir(filepath.Join("..", "..", "factors"))
	require.NoError(t, err)
	defs := make([]FactorDef, 0, len(entries))
	for _, entry := range entries {
		def, err := NormalizeFactorDefinition(FactorDef{
			FactorID: entry.FactorID, Name: entry.Name, CategoryID: entry.CategoryID, Spec: entry.Spec, AllowPartialUniverse: entry.AllowPartialUniverse,
		})
		require.NoError(t, err, "因子 %s 编译失败", entry.FactorID)
		defs = append(defs, def)
	}
	return defs
}

func TestBaselineDefinitionHashes(t *testing.T) {
	defs := baselineLibraryDefinitions(t)
	require.GreaterOrEqual(t, len(defs), 20, "因子库应包含全部定义")
	got := make(map[string]string, len(defs))
	for _, def := range defs {
		hash := DefinitionHash(def)
		require.True(t, strings.HasPrefix(hash, "sha256:"), "因子 %s 的指纹格式不对：%q", def.FactorID, hash)
		require.Equal(t, hash, DefinitionHash(def), "因子 %s 的指纹不稳定", def.FactorID)
		got[def.FactorID] = hash
	}
	raw, err := json.MarshalIndent(got, "", "  ")
	require.NoError(t, err)
	raw = append(raw, '\n')
	if baselineUpdate() {
		require.NoError(t, os.MkdirAll(filepath.Dir(baselineHashesPath), 0o755))
		require.NoError(t, os.WriteFile(baselineHashesPath, raw, 0o644))
		return
	}
	stored, err := os.ReadFile(baselineHashesPath)
	require.NoError(t, err, "没有金样 %s：用 UPDATE_BASELINE=1 生成", baselineHashesPath)
	var want map[string]string
	require.NoError(t, json.Unmarshal(stored, &want))
	ids := make([]string, 0, len(want)+len(got))
	for id := range want {
		ids = append(ids, id)
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		require.Equal(t, want[id], got[id], "因子 %s 的 definition_hash 与基线金样不一致（空串表示一侧没有这个因子）", id)
	}
	require.True(t, bytes.Equal(stored, raw), "金样 %s 与当前输出的字节不一致", baselineHashesPath)
}
