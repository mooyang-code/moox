package command

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 破坏性命令不带 --yes 时只预览：不能发出任何写请求，预览是带 preview:true 的 JSON。

func parsePreview(t *testing.T, raw string) map[string]any {
	t.Helper()
	var preview map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &preview), raw)
	require.Equal(t, true, preview["preview"], raw)
	return preview
}

func TestSetupFirewallOnlyPreviewsWithoutYes(t *testing.T) {
	snapshot := setupSnapshot(t)
	called := 0
	deps := setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		ensureFirewall: func(context.Context, *setupconfig.Snapshot) (setupFirewallSummary, error) {
			called++
			return setupFirewallSummary{Status: "ready"}, nil
		},
	}
	run := func(args ...string) string {
		cmd := newSetupCommand(deps)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(append([]string{"firewall", "--file", "moox.toml"}, args...))
		require.NoError(t, cmd.Execute())
		return out.String()
	}
	preview := parsePreview(t, run())
	assert.Zero(t, called, "没有 --yes 不能放行端口")
	assert.Equal(t, "open_firewall_ports", preview["action"])
	assert.NotEmpty(t, preview["targets"])

	snapshot = setupSnapshot(t)
	assert.Contains(t, run("--yes"), `"status":"ready"`)
	assert.Equal(t, 1, called)
}

func TestSetupWatchdogRemoveOnlyPreviewsWithoutYes(t *testing.T) {
	snapshot := setupSnapshot(t)
	_, removes, out, err := runWatchdogCommand(t, snapshot, "--remove", "--host", "compute")
	require.NoError(t, err)
	assert.Empty(t, removes, "没有 --yes 不能移除守护")
	preview := parsePreview(t, out)
	assert.Equal(t, "remove_watchdog", preview["action"])
	assert.Equal(t, []any{"compute"}, preview["hosts"])
}

func TestSetupSyncDeploymentsOnlyPreviewsWithoutYes(t *testing.T) {
	snapshot := setupSnapshot(t)
	previous := openCommandGateway
	openCommandGateway = func(context.Context, string, *setupconfig.Snapshot) (commandGateway, error) {
		t.Fatal("预览不能连接网关")
		return nil, nil
	}
	t.Cleanup(func() { openCommandGateway = previous })
	cmd := newSetupCommand(setupDeps{load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"sync-deployments", "--file", "moox.toml", "--host", "control"})
	require.NoError(t, cmd.Execute())
	preview := parsePreview(t, out.String())
	assert.Equal(t, "sync_deployments", preview["action"])
	assert.Equal(t, "control", preview["host"])
}

func TestCollectorTaskDeleteOnlyPreviewsWithoutYes(t *testing.T) {
	gateway := &rpcFakeGateway{}
	useRPCGateway(t, gateway)
	collectorTaskDeleteFlagsValue.File, collectorTaskDeleteFlagsValue.SpaceID, collectorTaskDeleteFlagsValue.TaskID = "moox.toml", "crypto", "task-1"
	collectorTaskDeleteFlagsValue.Yes, collectorTaskDeleteFlagsValue.KeepResult = false, false
	t.Cleanup(func() {
		collectorTaskDeleteFlagsValue.File, collectorTaskDeleteFlagsValue.SpaceID, collectorTaskDeleteFlagsValue.TaskID = "", "", ""
	})
	var out bytes.Buffer
	collectorTaskDeleteCmd.SetOut(&out)
	collectorTaskDeleteCmd.SetErr(&bytes.Buffer{})
	collectorTaskDeleteCmd.SetContext(context.Background())
	err := collectorTaskDeleteCmd.RunE(collectorTaskDeleteCmd, nil)
	// 假网关的 Invoke 不填充响应，预览走到读取任务详情这一步即可证明没有发出删除请求。
	require.NoError(t, err)
	preview := parsePreview(t, out.String())
	assert.Equal(t, "delete_task", preview["action"])
	assert.Equal(t, "task-1", preview["task_id"])
	assert.Equal(t, true, preview["delete_result_data"])
	assert.Zero(t, gateway.calls)
}

func TestCollectorResultDeleteOnlyPreviewsWithoutYes(t *testing.T) {
	collectorResultDeleteFlagsValue.SpaceID = "crypto"
	collectorResultDeleteFlagsValue.DatasetIDs = []string{"d1"}
	collectorResultDeleteFlagsValue.ViewIDs = []string{"v1"}
	collectorResultDeleteFlagsValue.Yes = false
	t.Cleanup(func() {
		collectorResultDeleteFlagsValue.SpaceID, collectorResultDeleteFlagsValue.DatasetIDs, collectorResultDeleteFlagsValue.ViewIDs = "", nil, nil
	})
	var out bytes.Buffer
	collectorResultDeleteCmd.SetOut(&out)
	collectorResultDeleteCmd.SetErr(&bytes.Buffer{})
	collectorResultDeleteCmd.SetContext(context.Background())
	require.NoError(t, collectorResultDeleteCmd.RunE(collectorResultDeleteCmd, nil))
	preview := parsePreview(t, out.String())
	assert.Equal(t, []any{"d1"}, preview["datasets"])
	assert.Equal(t, []any{"v1"}, preview["views"])
}

func TestDestructiveCommandsNoLongerHaveLegacyConfirmFlags(t *testing.T) {
	for _, cmd := range []struct {
		name string
		flag func(string) bool
	}{
		{"collector task purge", func(n string) bool { return collectorTaskPurgeCmd.Flags().Lookup(n) != nil }},
		{"collector function delete", func(n string) bool { return collectorFunctionDeleteCmd.Flags().Lookup(n) != nil }},
		{"firewall add", func(n string) bool { return lighthouseFirewallAddCmd.Flags().Lookup(n) != nil }},
		{"firewall open", func(n string) bool { return lighthouseFirewallOpenCmd.Flags().Lookup(n) != nil }},
	} {
		assert.True(t, cmd.flag("yes"), cmd.name+" 要有 --yes")
		for _, legacy := range []string{"dry-run", "confirm", "apply"} {
			assert.False(t, cmd.flag(legacy), "%s 不应再有 --%s", cmd.name, legacy)
		}
	}
}
