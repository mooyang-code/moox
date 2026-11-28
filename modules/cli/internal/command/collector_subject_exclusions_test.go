package command

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectorSubjectExclusionCommands(t *testing.T) {
	gw := &rpcFakeGateway{responses: map[string]string{
		"SetSubjectExclusions":   `{"ret_info":{"code":0},"exclusions":[{"subject_id":"BTC-USDT","market_type":"swap","reason":"公告下架"}]}`,
		"ClearSubjectExclusions": `{"ret_info":{"code":0},"removed":2}`,
		"ListSubjectExclusions":  `{"ret_info":{"code":0},"exclusions":[{"subject_id":"BTC-USDT","reason":"公告下架"}]}`,
	}}
	useRPCGateway(t, gw)

	_, _, err := runSpecCommand(t, specByPath(t, "collector", "subjects", "exclude"), "--subjects", "BTC-USDT", "--reason", "x")
	require.ErrorContains(t, err, "--space-id")
	_, _, err = runSpecCommand(t, specByPath(t, "collector", "subjects", "unexclude"), "--subjects", "BTC-USDT")
	require.ErrorContains(t, err, "--space-id")
	_, _, err = runSpecCommand(t, specByPath(t, "collector", "subjects", "exclusions"))
	require.ErrorContains(t, err, "--space-id")
	_, _, err = runSpecCommand(t, specByPath(t, "collector", "subjects", "exclude"), "--space-id", "crypto", "--subjects", "BTC-USDT")
	require.ErrorContains(t, err, "--reason")
	_, _, err = runSpecCommand(t, specByPath(t, "collector", "subjects", "exclude"), "--space-id", "crypto", "--reason", "x")
	require.ErrorContains(t, err, "--subjects")
	_, _, err = runSpecCommand(t, specByPath(t, "collector", "subjects", "exclude"), "--space-id", "crypto", "--subjects", " , ", "--reason", "x")
	require.ErrorContains(t, err, "--subjects")
	_, _, err = runSpecCommand(t, specByPath(t, "collector", "subjects", "exclude"), "--space-id", "crypto", "--subjects", "BTC-USDT", "--reason", "x", "--expires-in", "-1h")
	require.ErrorContains(t, err, "--expires-in")
	assert.Zero(t, gw.calls)

	before := time.Now()
	out, _, err := runSpecCommand(t, specByPath(t, "collector", "subjects", "exclude"),
		"--space-id", "crypto", "--subjects", "BTC-USDT", "--market", "swap", "--reason", "公告下架", "--expires-in", "72h")
	require.NoError(t, err)
	var req map[string]any
	require.NoError(t, json.Unmarshal(gw.bodies["SetSubjectExclusions"], &req))
	assert.Equal(t, []any{"BTC-USDT"}, req["subject_ids"])
	assert.Equal(t, "swap", req["market_type"])
	assert.Contains(t, req["operator"], "moox-cli:")
	expiresAt, ok := req["expires_at"].(float64)
	require.True(t, ok)
	require.WithinDuration(t, before.Add(72*time.Hour), time.UnixMilli(int64(expiresAt)), 2*time.Second)
	assert.Contains(t, out, `"market_type": "swap"`)

	out, _, err = runSpecCommand(t, specByPath(t, "collector", "subjects", "unexclude"),
		"--space-id", "crypto", "--subjects", "BTC-USDT,ETH-USDT")
	require.NoError(t, err)
	assert.Contains(t, out, `"removed": 2`)

	out, _, err = runSpecCommand(t, specByPath(t, "collector", "subjects", "exclusions"), "--space-id", "crypto")
	require.NoError(t, err)
	assert.Contains(t, out, `"subject_id": "BTC-USDT"`)
	assert.Equal(t, []string{"SetSubjectExclusions", "ClearSubjectExclusions", "ListSubjectExclusions"}, gw.methods)
}
