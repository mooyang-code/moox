package command

import (
	"bytes"
	"context"
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
