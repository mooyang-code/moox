package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/hostpolicy"
	"github.com/spf13/cobra"
)

// newSetupWatchdogCommand 在各主机安装主机级守护：部署用户 crontab 里每分钟一次的 `moox-runtime watch`，
// 并用 moox.toml 的 [notification] 写入主机策略种子（HostAgent 之后会用 Monitor 的渠道覆盖它）。
// 种子含告警推送地址，经 SSH 标准输入传给目标机，不出现在命令行参数里。
func newSetupWatchdogCommand(deps setupDeps) *cobra.Command {
	var file string
	var hosts []string
	var remove, yes bool
	cmd := &cobra.Command{Use: "watchdog", Short: "在各主机安装（或移除）主机级守护与告警策略种子", RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		targets, err := watchdogTargets(snapshot.Manifest, hosts)
		if err != nil {
			return err
		}
		if remove && !yes {
			names := make([]string, 0, len(targets))
			for _, host := range targets {
				names = append(names, host.Name)
			}
			return writePreview(cmd, map[string]any{"action": "remove_watchdog", "hosts": names})
		}
		results := map[string]json.RawMessage{}
		for _, host := range targets {
			result, err := deps.installWatchdog(cmd.Context(), snapshot, host, remove)
			if err != nil {
				return fmt.Errorf("host %s: %w", host.Name, err)
			}
			results[host.Name] = result
		}
		if err := snapshot.VerifyUnchanged(); err != nil {
			return fmt.Errorf("config_changed")
		}
		return writeSetupJSON(cmd, map[string]any{"status": "ready", "hosts": results})
	}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringSliceVar(&hosts, "host", nil, "只处理这些主机（默认全部主机）")
	cmd.Flags().BoolVar(&remove, "remove", false, "移除守护的 crontab 条目（默认只预览，需要 --yes）")
	cmd.Flags().BoolVar(&yes, "yes", false, "确认移除；只对 --remove 有意义")
	return cmd
}

func watchdogTargets(manifest setupconfig.Manifest, names []string) ([]setupconfig.Host, error) {
	if len(names) == 0 {
		return manifest.Hosts(), nil
	}
	var targets []setupconfig.Host
	for _, name := range names {
		host, err := findSetupHost(manifest, name)
		if err != nil {
			return nil, err
		}
		targets = append(targets, host)
	}
	return targets, nil
}

// watchdogSeed 用 moox.toml 的 [notification] 生成主机策略种子；没配置 webhook 时种子里没有渠道，等 Monitor 同步。
func watchdogSeed(manifest setupconfig.Manifest) ([]byte, error) {
	channel := strings.TrimSpace(manifest.Notification.ChannelType)
	if channel == "" {
		channel = "wecom"
	}
	seed := hostpolicy.Policy{Source: hostpolicy.SourceSeed, FetchedAt: time.Now().UTC()}
	if url := strings.TrimSpace(manifest.Notification.WebhookURL); url != "" {
		seed.NotificationChannels = []hostpolicy.Channel{{Type: channel, WebhookURL: url}}
	}
	if err := seed.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(seed)
}

func defaultSetupInstallWatchdog(ctx context.Context, snapshot *setupconfig.Snapshot, host setupconfig.Host, remove bool) (json.RawMessage, error) {
	root := snapshot.Manifest.Paths.Resolved().DeployRoot
	runtime := path.Join(root, "host", "current", "bin", "moox-runtime")
	argv := []string{runtime, "install-watchdog", "--root", root}
	var stdin io.Reader
	if remove {
		argv = append(argv, "--remove")
	} else {
		seed, err := watchdogSeed(snapshot.Manifest)
		if err != nil {
			return nil, err
		}
		argv = append(argv, "--seed-policy", "-")
		stdin = bytes.NewReader(seed)
	}
	transport, err := dialSetupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	defer transport.Close()
	output, err := transport.Run(ctx, argv, stdin)
	if err != nil {
		return nil, fmt.Errorf("install-watchdog failed: %w: %s", err, strings.TrimSpace(output.Stderr))
	}
	raw := []byte(strings.TrimSpace(output.Stdout))
	if !json.Valid(raw) {
		return nil, fmt.Errorf("install-watchdog returned invalid output")
	}
	return json.RawMessage(raw), nil
}
