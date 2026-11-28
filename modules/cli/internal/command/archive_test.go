package command

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArchiveCreateSendsBackfillAndDirName(t *testing.T) {
	g := &rpcFakeGateway{response: `{"ret_info":{"code":0},"task":{"space_id":"crypto","dataset_id":"ds","backfill_from":"2026-10-15T00:00:00Z","purpose":"collect","dir_name":"binance-perp-kline-1m"}}`}
	useRPCGateway(t, g)
	out, _, err := runSpecCommand(t, specByPath(t, "archive", "tasks", "create"),
		"--space-id", "crypto", "--dataset-id", "ds", "--backfill-from", "2026-10-15T00:00:00Z", "--paused", "--dir-name", "binance-perp-kline-1m")
	require.NoError(t, err)
	assert.Equal(t, archiveMgrService, g.service)
	assert.Equal(t, "CreateArchiveTask", g.method)
	assert.True(t, g.closed)
	assert.JSONEq(t, `{"space_id":"crypto","dataset_id":"ds","backfill_from":"2026-10-15T00:00:00Z","paused":true,"dir_name":"binance-perp-kline-1m"}`, string(g.body))
	assert.Contains(t, out, `"backfill_from": "2026-10-15T00:00:00Z"`)
	assert.Contains(t, out, `"dir_name": "binance-perp-kline-1m"`)
}

func TestArchivePrepareAndRunsListOptions(t *testing.T) {
	g := &rpcFakeGateway{response: `{"ret_info":{"code":0},"purpose":"collect","suggested_dir_name":"binance-swap-kline-1m","dir_name_conflict":true}`}
	useRPCGateway(t, g)
	out, _, err := runSpecCommand(t, specByPath(t, "archive", "tasks", "prepare"),
		"--space-id", "crypto", "--dataset-id", "ds", "--dir-name", "binance-perp-kline-1m")
	require.NoError(t, err)
	assert.Equal(t, "PrepareArchiveTask", g.method)
	assert.JSONEq(t, `{"space_id":"crypto","dataset_id":"ds","dir_name":"binance-perp-kline-1m"}`, string(g.body))
	assert.Contains(t, out, `"suggested_dir_name": "binance-swap-kline-1m"`)
	assert.Contains(t, out, `"dir_name_conflict": true`)

	runs := &rpcFakeGateway{response: `{"ret_info":{"code":0},"runs":[]}`}
	useRPCGateway(t, runs)
	_, _, err = runSpecCommand(t, specByPath(t, "archive", "runs", "list"), "--include-follow")
	require.NoError(t, err)
	assert.Equal(t, "ListArchiveRuns", runs.method)
	var body map[string]any
	require.NoError(t, json.Unmarshal(runs.body, &body))
	assert.Equal(t, true, body["include_follow"])
	_, hasSpace := body["space_id"]
	assert.False(t, hasSpace, "未传 --space-id 时不限制空间")
}

func TestArchiveDestructiveCommandsPreviewWithoutYes(t *testing.T) {
	g := &rpcFakeGateway{response: `{"ret_info":{"code":0},"removed":[]}`}
	useRPCGateway(t, g)

	// clear-unrecoverable 缺必填参数时拒绝（含 --space-id）
	_, _, err := runSpecCommand(t, specByPath(t, "archive", "tasks", "clear-unrecoverable"), "--dataset-id", "ds", "--start", "2026-10-01T00:00:00Z", "--end", "2026-10-05T00:00:00Z")
	require.ErrorContains(t, err, "--space-id")
	_, _, err = runSpecCommand(t, specByPath(t, "archive", "tasks", "clear-unrecoverable"), "--space-id", "crypto", "--dataset-id", "ds")
	require.ErrorContains(t, err, "--start")
	assert.Zero(t, g.calls)

	// 无 --yes 只预览，不调用网关
	out, _, err := runSpecCommand(t, specByPath(t, "archive", "tasks", "clear-unrecoverable"),
		"--space-id", "crypto", "--dataset-id", "ds", "--start", "2026-10-01T00:00:00Z", "--end", "2026-10-05T00:00:00Z")
	require.NoError(t, err)
	assert.Zero(t, g.calls)
	preview := parsePreview(t, out)
	assert.Equal(t, "ClearArchiveUnrecoverable", preview["method"])

	// 加 --yes 才调用
	_, _, err = runSpecCommand(t, specByPath(t, "archive", "tasks", "clear-unrecoverable"),
		"--space-id", "crypto", "--dataset-id", "ds", "--start", "2026-10-01T00:00:00Z", "--end", "2026-10-05T00:00:00Z", "--yes")
	require.NoError(t, err)
	assert.Equal(t, 1, g.calls)
	assert.Equal(t, "ClearArchiveUnrecoverable", g.method)
	assert.JSONEq(t, `{"space_id":"crypto","dataset_id":"ds","start":"2026-10-01T00:00:00Z","end":"2026-10-05T00:00:00Z"}`, string(g.body))

	// delete 无 --yes 只预览
	out, _, err = runSpecCommand(t, specByPath(t, "archive", "tasks", "delete"), "--space-id", "crypto", "--dataset-id", "ds")
	require.NoError(t, err)
	assert.Equal(t, 1, g.calls)
	assert.Equal(t, "DeleteArchiveTask", parsePreview(t, out)["method"])
}

func TestArchiveCommandsValidateRequiredFlagsAndMapMethods(t *testing.T) {
	g := &rpcFakeGateway{response: `{"ret_info":{"code":0}}`}
	useRPCGateway(t, g)
	for name, tc := range map[string]struct {
		path []string
		args []string
	}{
		"create without space":    {[]string{"archive", "tasks", "create"}, []string{"--dataset-id", "ds"}},
		"create without dataset":  {[]string{"archive", "tasks", "create"}, []string{"--space-id", "crypto"}},
		"prepare without space":   {[]string{"archive", "tasks", "prepare"}, []string{"--dataset-id", "ds"}},
		"prepare without dataset": {[]string{"archive", "tasks", "prepare"}, []string{"--space-id", "crypto"}},
		"update without space":    {[]string{"archive", "tasks", "update"}, []string{"--dataset-id", "ds", "--status", "paused"}},
		"run without space":       {[]string{"archive", "tasks", "run"}, []string{"--dataset-id", "ds"}},
		"backfill without space":  {[]string{"archive", "tasks", "backfill"}, []string{"--dataset-id", "ds", "--from", "2026-10-16T00:00:00Z", "--to", "2026-10-17T00:00:00Z"}},
		"backfill without range":  {[]string{"archive", "tasks", "backfill"}, []string{"--space-id", "crypto", "--dataset-id", "ds"}},
		"run get without id":      {[]string{"archive", "runs", "get"}, nil},
		"run cancel without id":   {[]string{"archive", "runs", "cancel"}, nil},
		"partitions without space": {[]string{"archive", "partitions", "list"}, nil},
		"partition get no space":  {[]string{"archive", "partitions", "get"}, []string{"--dataset-id", "ds", "--partition-key", "2026-10-16"}},
		"partition get no key":    {[]string{"archive", "partitions", "get"}, []string{"--space-id", "crypto", "--dataset-id", "ds"}},
	} {
		_, _, err := runSpecCommand(t, specByPath(t, tc.path...), tc.args...)
		require.Error(t, err, name)
	}
	assert.Zero(t, g.calls, "参数不全时不应调用网关")

	for _, tc := range []struct {
		path   []string
		args   []string
		method string
	}{
		{[]string{"archive", "overview"}, nil, "GetArchiveOverview"},
		{[]string{"archive", "tasks", "list"}, []string{"--space-id", "crypto"}, "ListArchiveTasks"},
		{[]string{"archive", "tasks", "prepare"}, []string{"--space-id", "crypto", "--dataset-id", "ds"}, "PrepareArchiveTask"},
		{[]string{"archive", "tasks", "update"}, []string{"--space-id", "crypto", "--dataset-id", "ds", "--status", "paused"}, "UpdateArchiveTask"},
		{[]string{"archive", "tasks", "delete"}, []string{"--space-id", "crypto", "--dataset-id", "ds", "--yes"}, "DeleteArchiveTask"},
		{[]string{"archive", "tasks", "run"}, []string{"--space-id", "crypto", "--dataset-id", "ds"}, "RunArchiveTaskNow"},
		{[]string{"archive", "tasks", "backfill"}, []string{"--space-id", "crypto", "--dataset-id", "ds", "--from", "2026-10-16T00:00:00Z", "--to", "2026-10-17T00:00:00Z"}, "BackfillArchiveRange"},
		{[]string{"archive", "runs", "list"}, []string{"--status", "queued"}, "ListArchiveRuns"},
		{[]string{"archive", "runs", "get"}, []string{"--run-id", "run_1"}, "GetArchiveRun"},
		{[]string{"archive", "runs", "cancel"}, []string{"--run-id", "run_1"}, "CancelArchiveRun"},
		{[]string{"archive", "partitions", "list"}, []string{"--space-id", "crypto", "--from-month", "2026-10"}, "ListArchivePartitions"},
		{[]string{"archive", "partitions", "get"}, []string{"--space-id", "crypto", "--dataset-id", "ds", "--partition-key", "2026-10-16"}, "GetArchivePartition"},
	} {
		gw := &rpcFakeGateway{response: `{"ret_info":{"code":0}}`}
		useRPCGateway(t, gw)
		_, _, err := runSpecCommand(t, specByPath(t, tc.path...), tc.args...)
		require.NoError(t, err, tc.method)
		assert.Equal(t, archiveMgrService, gw.service)
		assert.Equal(t, tc.method, gw.method)
	}
}

func TestArchiveSurfacesManagerErrorsAndTreeMounted(t *testing.T) {
	g := &rpcFakeGateway{response: `{"ret_info":{"code":6,"msg":"数据集已有归档任务"}}`}
	useRPCGateway(t, g)
	out, _, err := runSpecCommand(t, specByPath(t, "archive", "tasks", "create"), "--space-id", "crypto", "--dataset-id", "ds")
	require.ErrorContains(t, err, "数据集已有归档任务")
	assert.Contains(t, out, "数据集已有归档任务", "业务失败时按统一约定先打印响应再非 0 退出")

	mountCommands(rootCmd)
	cmd, rest, err := rootCmd.Find([]string{"archive"})
	require.NoError(t, err)
	require.Empty(t, rest)
	var found []string
	for _, c := range cmd.Commands() {
		found = append(found, c.Name())
	}
	assert.ElementsMatch(t, []string{"overview", "tasks", "runs", "partitions"}, found)
}
