package command

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/hostpolicy"
	"github.com/stretchr/testify/require"
)

func runWatchdogCommand(t *testing.T, snapshot *setupconfig.Snapshot, args ...string) ([]string, []bool, string, error) {
	t.Helper()
	var hosts []string
	var removes []bool
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		installWatchdog: func(_ context.Context, _ *setupconfig.Snapshot, host setupconfig.Host, remove bool) (json.RawMessage, error) {
			hosts = append(hosts, host.Name)
			removes = append(removes, remove)
			return json.RawMessage(`{"seed":"written"}`), nil
		},
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs(append([]string{"watchdog", "--file", "moox.toml"}, args...))
	err := cmd.Execute()
	return hosts, removes, output.String(), err
}

// 默认处理全部主机；--host 只处理指定主机；--remove 不要求告警地址。
func TestSetupWatchdogCommandTargetsHosts(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	snapshot.Manifest.Notification = setupconfig.Notification{ChannelType: "wecom", WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test"}

	hosts, removes, output, err := runWatchdogCommand(t, snapshot)
	require.NoError(t, err)
	require.Len(t, hosts, len(snapshot.Manifest.Hosts()))
	require.NotContains(t, removes, true)
	require.Contains(t, output, `"status":"ready"`)

	// 命令结束时会清掉快照里的密钥（clearSetupSecrets），每次调用前重新设置。
	snapshot.Manifest.Notification = setupconfig.Notification{ChannelType: "wecom", WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test"}
	hosts, _, _, err = runWatchdogCommand(t, snapshot, "--host", "compute")
	require.NoError(t, err)
	require.Equal(t, []string{"compute"}, hosts)

	snapshot.Manifest.Notification = setupconfig.Notification{}
	_, _, _, err = runWatchdogCommand(t, snapshot)
	require.ErrorContains(t, err, "notification.webhook_url is required")
	_, removes, _, err = runWatchdogCommand(t, snapshot, "--remove", "--host", "compute")
	require.NoError(t, err)
	require.Equal(t, []bool{true}, removes)
}

func TestWatchdogSeedUsesNotificationChannel(t *testing.T) {
	t.Parallel()
	manifest := setupconfig.Manifest{Notification: setupconfig.Notification{WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test"}}
	raw, err := watchdogSeed(manifest)
	require.NoError(t, err)
	var seed hostpolicy.Policy
	require.NoError(t, json.Unmarshal(raw, &seed))
	require.Equal(t, hostpolicy.SourceSeed, seed.Source)
	require.Equal(t, []hostpolicy.Channel{{Type: "wecom", WebhookURL: manifest.Notification.WebhookURL}}, seed.NotificationChannels)
}
