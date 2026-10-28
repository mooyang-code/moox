package factorlib

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

// 仓库里的因子库与示例都必须能编译：factor_id、输出列与回看是对外契约。
func TestRepositoryLibrariesCompile(t *testing.T) {
	cases := map[string]map[string]int{
		"../../factors": {
			// 回看按窗口上限推导（参数可在范围内逐期调整）：ema 取 warmup 上限 50000，其余 120。
			"bias": 120, "bias_q": 239, "cci": 239, "min_max": 120, "quote_volume_mean": 120, "quote_volume_mean_q": 239,
			"volume_mean_q": 239, "zf_abs_mean": 240, "zf_mean_q": 239, "zf_std": 121, "zscore_abs_mean_q": 358,
			// 常用技术指标（common.yaml）：EMA、MACD、RSI 带指数衰减，回看按截断到起点权重低于 1e-9 的根数推导。
			"ma": 120, "ema": 50000, "macd": 362, "rsi": 2478, "roc": 121, "boll": 120, "atr": 121, "atr_pct": 121, "return_std": 121, "vwap": 120,
		},
		"../../../../examples/factors": {"bias": 120, "cci": 239, "ma_fast": 60, "ma_slow": 240, "channel": 120},
	}
	for dir, lookbacks := range cases {
		entries, err := LoadDir(dir)
		require.NoError(t, err, dir)
		require.Len(t, entries, len(lookbacks), dir)
		for _, entry := range entries {
			def, err := domain.NormalizeFactorDefinition(domain.FactorDef{FactorID: entry.FactorID, Name: entry.Name, Spec: entry.Spec})
			require.NoError(t, err, "%s/%s", dir, entry.FactorID)
			want, ok := lookbacks[entry.FactorID]
			require.True(t, ok, "%s 里有意外的因子 %s", dir, entry.FactorID)
			require.Equal(t, want, def.LookbackPeriods, "%s/%s 的回看", dir, entry.FactorID)
		}
	}
}

func TestLoadRejectsBadLibraries(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		return path
	}
	_, err := LoadFile(write("unknown.yaml", "- factor_id: A\n  name: A\n  spec: x\n  params_json: '{}'\n"))
	require.ErrorContains(t, err, "params_json")
	_, err = LoadFile(write("missing.yaml", "- factor_id: A\n  spec: x\n"))
	require.ErrorContains(t, err, "缺少")
	_, err = LoadFile(write("dup.yaml", "- {factor_id: A, name: A, spec: x}\n- {factor_id: A, name: B, spec: y}\n"))
	require.ErrorContains(t, err, "重复")
	_, err = LoadFile(write("multi.yaml", "- {factor_id: A, name: A, spec: x}\n---\n- {factor_id: B, name: B, spec: y}\n"))
	require.ErrorContains(t, err, "一个 YAML 文档")
	_, err = LoadFile(write("empty.yaml", ""))
	require.ErrorContains(t, err, "空文件")

	split := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(split, "a.yaml"), []byte("- {factor_id: A, name: A, spec: x}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(split, "b.yaml"), []byte("- {factor_id: A, name: A2, spec: y}\n"), 0o600))
	_, err = LoadDir(split)
	require.ErrorContains(t, err, "同时出现在")
	_, err = LoadDir(t.TempDir())
	require.ErrorContains(t, err, "没有 *.yaml")
}
