package command

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"gopkg.in/yaml.v3"
)

// disableCollectorTimerFleet disables every Timer-triggered market fetcher in
// the Space. Crypto is Scheduler/Invoke-owned; leaving a legacy Timer trigger
// enabled would execute the same collection range twice during rollout.
func disableCollectorTimerFleet(ctx context.Context, client *adminclient.Client, cfg *setupconfig.SCFFetcherSpace) error {
	if cfg == nil {
		return nil
	}
	nodes, err := client.ListCloudNodes(ctx, adminclient.CloudNodeListFilter{NodeType: "scf-event", BizType: "market_fetcher"})
	if err != nil {
		return fmt.Errorf("list Timer fleet for space %s: %w", cfg.SpaceID, err)
	}
	timers := make([]adminclient.CloudNode, 0)
	for _, node := range nodes {
		if node.IsDeleted || strings.TrimSpace(node.NodeID) == "" || !strings.EqualFold(node.TriggerType, "timer") {
			continue
		}
		timers = append(timers, node)
	}
	jobs, err := submitCollectorTimerRuntimeConfigs(ctx, client, collectorTimerDisablePatches(timers))
	if err != nil {
		return fmt.Errorf("disable Timer fleet for space %s: %w", cfg.SpaceID, err)
	}
	if err := waitCollectorBatches(ctx, client, jobs); err != nil {
		return fmt.Errorf("wait for Timer fleet shutdown for space %s: %w", cfg.SpaceID, err)
	}
	return nil
}

// The caller supplies its Space-scoped control client. Disabling the old
// fleet must finish before publishing or activating its replacement.
func disableCollectorBlacklistedTimers(ctx context.Context, client *adminclient.Client, cfg *setupconfig.SCFFetcherSpace) error {
	if cfg == nil || len(cfg.RegionBlacklist) == 0 {
		return nil
	}
	nodes, err := client.ListCloudNodes(ctx, adminclient.CloudNodeListFilter{NodeType: "scf-event", BizType: "market_fetcher"})
	if err != nil {
		return fmt.Errorf("list blacklisted Timer fleet for space %s: %w", cfg.SpaceID, err)
	}
	blocked := make([]adminclient.CloudNode, 0)
	for _, node := range nodes {
		if node.IsDeleted || strings.TrimSpace(node.NodeID) == "" || node.NodeType != "scf-event" || node.BizType != "market_fetcher" || !strings.EqualFold(node.TriggerType, "timer") || !cfg.IsRegionBlacklisted(node.Region) {
			continue
		}
		blocked = append(blocked, node)
	}
	jobs, err := submitCollectorTimerRuntimeConfigs(ctx, client, collectorTimerDisablePatches(blocked))
	if err != nil {
		return fmt.Errorf("disable blacklisted Timer fleet for space %s: %w", cfg.SpaceID, err)
	}
	if err := waitCollectorBatches(ctx, client, jobs); err != nil {
		return fmt.Errorf("wait for blacklisted Timer fleet shutdown for space %s: %w", cfg.SpaceID, err)
	}
	return nil
}

func preflightCollectorBlacklistRuntime(ctx context.Context, snapshot *setupconfig.Snapshot, cfg *setupconfig.SCFFetcherSpace) error {
	if cfg == nil {
		return nil
	}
	if snapshot == nil {
		return fmt.Errorf("deploy and restart Collector with the Space region blacklist before publishing SCFs")
	}
	host, err := collectorHost(snapshot.Manifest)
	if err != nil {
		return err
	}
	transport, err := dialSetupHost(ctx, host)
	if err != nil {
		return fmt.Errorf("连接主机 %s 校验行情采集服务的地域黑名单失败，请先部署并重启行情采集服务: %w", host.ID, err)
	}
	defer transport.Close()
	return verifyCollectorBlacklistRuntime(ctx, transport, host.Root, cfg)
}

func verifyCollectorBlacklistRuntime(ctx context.Context, transport setupssh.Client, root string, cfg *setupconfig.SCFFetcherSpace) error {
	result, err := transport.Run(ctx, []string{"bash", "-c", collectorBlacklistRuntimeScript, "moox-collector-blacklist-preflight", root}, nil)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("cannot verify live Collector process identity; deploy and restart Collector with the Space region blacklist first")
	}
	executable, raw, ok := strings.Cut(result.Stdout, "\n")
	if !ok {
		return fmt.Errorf("missing Collector runtime identity; deploy and restart Collector first")
	}
	return validateCollectorBlacklistRuntime(cfg, []byte(raw), executable)
}

// validateCollectorBlacklistRuntime 校验运行中的行情采集服务使用的配置与 moox.toml 的地域黑名单一致。进程必须
// 来自当前发布（发布目录不可变，所以发布中的 app.yaml 就是进程读到的配置）。
func validateCollectorBlacklistRuntime(cfg *setupconfig.SCFFetcherSpace, raw []byte, executable string) error {
	if filepath.Base(executable) != "moox-collector" {
		return fmt.Errorf("Collector process does not match its current app configuration; deploy and restart Collector before publishing SCFs")
	}
	var config struct {
		Blacklists map[string][]string `yaml:"scf_region_blacklists"`
	}
	if err := yaml.Unmarshal(raw, &config); err != nil {
		return fmt.Errorf("cannot validate Collector region blacklist configuration")
	}
	normalize := func(values []string) []string {
		result := make([]string, 0, len(values))
		for _, value := range values {
			result = append(result, strings.ToLower(strings.TrimSpace(value)))
		}
		slices.Sort(result)
		return slices.Compact(result)
	}
	if cfg == nil || !slices.Equal(normalize(config.Blacklists[cfg.SpaceID]), normalize(cfg.RegionBlacklist)) {
		return fmt.Errorf("Collector Space region blacklist differs from manifest; deploy and restart Collector before publishing SCFs")
	}
	return nil
}

// collectorBlacklistRuntimeScript 输出运行中行情采集服务的可执行文件和当前发布中的 app.yaml；进程不是当前发布
// 启动的时候失败。
const collectorBlacklistRuntimeScript = `set -eu
root=$1
pid=$(cat "$root/run/collector.pid")
case "$pid" in ''|*[!0-9]*) exit 1 ;; esac
kill -0 "$pid"
release=$(readlink -f "$root/current")
exe=$(readlink "/proc/$pid/exe")
test "$exe" = "$release/bin/moox-collector"
printf '%s\n' "$exe"
cat "$release/collector/config/app.yaml"
`

// collectorHost 返回部署了行情采集服务的主机。
func collectorHost(manifest setupconfig.Manifest) (setupconfig.Host, error) {
	hosts := manifest.HostsOf("collector")
	if len(hosts) == 0 {
		return setupconfig.Host{}, fmt.Errorf("moox.toml 的部署表中没有行情采集服务（collector）")
	}
	host, _ := manifest.Host(hosts[0])
	return host, nil
}
