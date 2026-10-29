package factorudf

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

var updateDigests = flag.Bool("update-digests", false, "重写 testdata/digests.json")

// 示例模块的一致性测试输出摘要固定在 testdata/digests.json 里。摘要只取决于模块的行为，不取决于 wasm 的字节：
// CI（amd64）与开发机（arm64）都与同一份预期值比较，跨架构的确定性因此成为回归门禁；
// 重新构建示例（make udf-testdata）后行为不变，摘要也不变，行为变了才需要有意识地用 -update-digests 更新。
func TestPinnedConformanceDigests(t *testing.T) {
	path := filepath.Join("testdata", "digests.json")
	got := map[string]string{}
	rt := newTestRuntime(t, Options{})
	for _, name := range []string{"ts_mean", "ema_trunc", "pair_relaxed", "faulty"} {
		m, err := rt.Load(context.Background(), testWasm(t, name))
		require.NoError(t, err)
		var fx FixtureSet
		if name != "faulty" {
			fx = loadFixtures(t, name)
		}
		rep, err := RunConformance(context.Background(), rt, m, fx, ConformanceOptions{})
		require.NoError(t, err)
		require.True(t, rep.Passed, "%s：%+v", name, rep.Failures())
		got[name] = rep.Digest
	}
	if *updateDigests {
		raw, err := json.MarshalIndent(got, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(raw, '\n'), 0o644))
		return
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "没有 %s：运行 go test -run TestPinnedConformanceDigests -update-digests 生成", path)
	var want map[string]string
	require.NoError(t, json.Unmarshal(raw, &want))
	require.Equal(t, want, got, "摘要变了：模块行为、一致性测试的数据生成或参数采样改变了；确认是有意的再用 -update-digests 更新")
}
