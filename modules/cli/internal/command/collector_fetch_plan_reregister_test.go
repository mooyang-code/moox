package command

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 没有 --yes 或 --space-id 时在连接网关之前就拒绝：该命令会清空补采作业，不能被误触发。
func TestFetchPlanReregisterRequiresSpaceAndConfirmation(t *testing.T) {
	var out bytes.Buffer
	err := runFetchPlanReregister(context.Background(), &out, klineRangeFlags{File: "/nonexistent/moox.toml"}, true)
	require.ErrorContains(t, err, "--space-id")

	err = runFetchPlanReregister(context.Background(), &out, klineRangeFlags{File: "/nonexistent/moox.toml", SpaceID: "crypto"}, false)
	require.ErrorContains(t, err, "--yes")
	require.Empty(t, out.String())
}

func TestFetchPlanReregisterIsRegisteredUnderFetchPlan(t *testing.T) {
	found, _, err := collectorCmd.Find([]string{"fetch-plan", "reregister"})
	require.NoError(t, err)
	require.Equal(t, "reregister", found.Name())
	require.NotNil(t, found.Flags().Lookup("yes"))
	require.NotNil(t, found.Flags().Lookup("space-id"))
}

func TestFetchPlanPauseAndResumeAreRegisteredUnderFetchPlan(t *testing.T) {
	for _, name := range []string{"pause", "resume"} {
		found, _, err := collectorCmd.Find([]string{"fetch-plan", name})
		require.NoError(t, err)
		require.Equal(t, name, found.Name())
		require.NotNil(t, found.Flags().Lookup("space-id"))
	}
	pause, _, _ := collectorCmd.Find([]string{"fetch-plan", "pause"})
	require.NotNil(t, pause.Flags().Lookup("wait"))
	var out bytes.Buffer
	require.Error(t, runFetchPlanPause(context.Background(), &out, klineRangeFlags{File: "/nonexistent/moox.toml"}, true, 0), "缺少 --space-id")
}
