package command

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResampleCommandsValidateRequiredFlagsAndBuildRequests(t *testing.T) {
	gw := &rpcFakeGateway{response: `{"ret_info":{"code":0}}`}
	useRPCGateway(t, gw)

	_, _, err := runSpecCommand(t, specByPath(t, "collector", "resample", "backfill"))
	require.ErrorContains(t, err, "--task-id")
	_, _, err = runSpecCommand(t, specByPath(t, "collector", "resample", "backfill"), "--task-id", "t")
	require.ErrorContains(t, err, "--from")
	_, _, err = runSpecCommand(t, specByPath(t, "collector", "resample", "backfill"), "--task-id", "t", "--from", "2026-10-01")
	require.ErrorContains(t, err, "--to")
	_, _, err = runSpecCommand(t, specByPath(t, "collector", "resample", "show"))
	require.ErrorContains(t, err, "--job-id")
	_, _, err = runSpecCommand(t, specByPath(t, "collector", "resample", "cancel"))
	require.ErrorContains(t, err, "--job-id")
	assert.Zero(t, gw.calls)

	// 日期简写自动规范化为 UTC RFC3339，不给 --request-id 时自动生成。
	_, _, err = runSpecCommand(t, specByPath(t, "collector", "resample", "backfill"),
		"--space-id", "crypto", "--task-id", "t1", "--from", "2026-10-01", "--to", "2026-10-02T08:00", "--subjects", "BTC-USDT,ETH-USDT")
	require.NoError(t, err)
	assert.Equal(t, "SubmitKlineResampleJob", gw.method)
	var body map[string]any
	require.NoError(t, json.Unmarshal(gw.body, &body))
	assert.Equal(t, "crypto", body["space_id"])
	assert.Equal(t, "t1", body["task_id"])
	assert.Equal(t, "2026-10-01T00:00:00Z", body["start"])
	assert.Equal(t, "2026-10-02T08:00:00Z", body["end"])
	assert.Equal(t, []any{"BTC-USDT", "ETH-USDT"}, body["subjects"])
	assert.NotEmpty(t, body["request_id"])

	_, _, err = runSpecCommand(t, specByPath(t, "collector", "resample", "jobs"), "--task-id", "t1", "--status", "running", "--limit", "10")
	require.NoError(t, err)
	assert.Equal(t, "ListKlineResampleJobs", gw.method)
	assert.JSONEq(t, `{"space_id":"crypto","task_id":"t1","status":"running","limit":10}`, string(gw.body))

	_, _, err = runSpecCommand(t, specByPath(t, "collector", "resample", "show"), "--job-id", "j1")
	require.NoError(t, err)
	assert.Equal(t, "GetKlineResampleJob", gw.method)
	assert.JSONEq(t, `{"space_id":"crypto","job_id":"j1"}`, string(gw.body))

	_, _, err = runSpecCommand(t, specByPath(t, "collector", "resample", "cancel"), "--job-id", "j1")
	require.NoError(t, err)
	assert.Equal(t, "CancelKlineResampleJob", gw.method)
	assert.JSONEq(t, `{"space_id":"crypto","job_id":"j1"}`, string(gw.body))
}
