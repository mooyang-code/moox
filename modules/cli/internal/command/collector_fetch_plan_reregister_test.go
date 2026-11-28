package command

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 没有 --yes 时只预览、不连接网关：reregister 会清空补采作业，不能被误触发。
func TestFetchPlanReregisterOnlyPreviewsWithoutYes(t *testing.T) {
	gw := &rpcFakeGateway{response: `{"ret_info":{"code":0}}`}
	useRPCGateway(t, gw)

	out, _, err := runSpecCommand(t, specByPath(t, "collector", "fetch-plan", "reregister"), "--space-id", "crypto")
	require.NoError(t, err)
	assert.Zero(t, gw.calls)
	preview := parsePreview(t, out)
	assert.Equal(t, "ReregisterFetchPlan", preview["method"])

	_, _, err = runSpecCommand(t, specByPath(t, "collector", "fetch-plan", "reregister"), "--space-id", "crypto", "--yes")
	require.NoError(t, err)
	assert.Equal(t, 1, gw.calls)
	assert.Equal(t, "ReregisterFetchPlan", gw.method)
}

func TestFetchPlanSubcommandsAreRegistered(t *testing.T) {
	mountCommands(rootCmd)
	for _, name := range []string{"show", "reregister", "pause", "resume"} {
		found, rest, err := rootCmd.Find([]string{"collector", "fetch-plan", name})
		require.NoError(t, err, name)
		require.Empty(t, rest, name)
		require.Equal(t, name, found.Name())
		require.NotNil(t, found.Flags().Lookup("space-id"), name)
	}
	reregister, _, _ := rootCmd.Find([]string{"collector", "fetch-plan", "reregister"})
	require.NotNil(t, reregister.Flags().Lookup("yes"))
	pause, _, _ := rootCmd.Find([]string{"collector", "fetch-plan", "pause"})
	require.NotNil(t, pause.Flags().Lookup("wait"))
	var out bytes.Buffer
	require.Error(t, runFetchPlanPause(context.Background(), &out, fetchPlanPauseConfig{File: "/nonexistent/moox.toml"}, true, 0), "缺少 --space-id")
}

// collector fetch-plan show 只输出采集计划摘要（open、applied、functions），不输出 collector status 的数据集巡检与历史补齐详情；
// 且必须显式指定 --space-id，不默认回退到 crypto。
func TestFetchPlanShowOutputsPlanSummaryOnlyAndRequiresSpaceID(t *testing.T) {
	gw := &rpcFakeGateway{response: `{
		"ret_info": {"code": 0},
		"level": "ok",
		"reasons": ["healthy"],
		"open": {"id": 12, "status": "applying", "created_at": 1764288000000},
		"applied": {"id": 11, "status": "applied", "updated_at": 1764201600000},
		"functions": [{"name": "moox-fetch-plan-stockcn-1", "region": "ap-shanghai", "market": "stockcn", "cron": "0 */5 * * * *", "subject_count": 300, "datasets": ["ds1"], "state": "applied"}],
		"datasets": [{"dataset_id": "ds1", "level": "ok"}],
		"active_backfills": 2,
		"history_seeds": [{"dataset_id": "ds1", "status": "running"}]
	}`}
	useRPCGateway(t, gw)

	// 缺少 --space-id 必须报错，不调用网关。
	for _, path := range [][]string{
		{"collector", "status"},
		{"collector", "fetch-plan", "show"},
		{"collector", "fetch-plan", "resume"},
		{"collector", "fetch-plan", "reregister"},
		{"collector", "kline", "check"},
		{"collector", "kline", "backfill"},
		{"collector", "kline", "backfill-list"},
		{"collector", "kline", "backfill-status"},
		{"collector", "kline", "backfill-cancel"},
		{"collector", "kline", "backfill-retry"},
		{"collector", "kline", "absences-clear"},
		{"collector", "history-seed", "skip"},
	} {
		_, _, err := runSpecCommand(t, specByPath(t, path...))
		require.ErrorContains(t, err, "--space-id", "%v 缺少 --space-id 应报错", path)
	}
	assert.Zero(t, gw.calls)

	out, _, err := runSpecCommand(t, specByPath(t, "collector", "fetch-plan", "show"), "--space-id", "stockcn")
	require.NoError(t, err)
	assert.JSONEq(t, `{"space_id":"stockcn"}`, string(gw.body))

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	assert.ElementsMatch(t, []string{"open", "applied", "functions"}, []string{"open", "applied", "functions"})
	assert.Len(t, decoded, 3, "fetch-plan show 只应包含 open、applied、functions 三个字段")
	assert.NotContains(t, decoded, "datasets")
	assert.NotContains(t, decoded, "level")
	assert.NotContains(t, decoded, "history_seeds")

	openPlan := decoded["open"].(map[string]any)
	assert.EqualValues(t, 12, openPlan["id"])
	assert.Equal(t, "applying", openPlan["status"])
	appliedPlan := decoded["applied"].(map[string]any)
	assert.EqualValues(t, 11, appliedPlan["id"])
	fns := decoded["functions"].([]any)
	require.Len(t, fns, 1)
	fn := fns[0].(map[string]any)
	assert.Equal(t, "moox-fetch-plan-stockcn-1", fn["name"])
	assert.EqualValues(t, 300, fn["subjects"])
}
