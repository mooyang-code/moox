package command

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"gopkg.in/yaml.v3"
)

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
	transport, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	if err != nil {
		return fmt.Errorf("cannot verify running Collector blacklist; deploy and restart Collector first")
	}
	defer transport.Close()
	return verifyCollectorBlacklistRuntime(ctx, transport, snapshot.Manifest.Paths.Resolved().ControlRoot, cfg)
}

func verifyCollectorBlacklistRuntime(ctx context.Context, transport setupssh.Client, root string, cfg *setupconfig.SCFFetcherSpace) error {
	result, err := transport.Run(ctx, []string{"bash", "-lc", collectorBlacklistRuntimeScript, "moox-collector-blacklist-preflight", root}, nil)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("cannot verify live Collector process identity; deploy and restart Collector with the Space region blacklist first")
	}
	parts := strings.SplitN(result.Stdout, "\n", 3)
	if len(parts) != 3 {
		return fmt.Errorf("missing Collector runtime identity; deploy and restart Collector first")
	}
	return validateCollectorBlacklistRuntime(cfg, []byte(parts[2]), parts[0], parts[1])
}

func validateCollectorBlacklistRuntime(cfg *setupconfig.SCFFetcherSpace, raw []byte, configHash, executable string) error {
	if filepath.Base(executable) != "moox-collector" || configHash != fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) {
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

const collectorBlacklistRuntimeScript = `set -eu
root=$1
pid=$(cat "$root/run/collector.pid")
case "$pid" in ''|*[!0-9]*) exit 1 ;; esac
kill -0 "$pid"
exe=$(readlink "/proc/$pid/exe")
test "$exe" = "$root/bin/moox-collector"
hash=$(tr '\000' '\n' < "/proc/$pid/environ" | sed -n 's/^MOOX_CONFIG_HASH=//p')
test -n "$hash"
printf '%s\n%s\n' "$hash" "$exe"
cat "$root/collector/config/app.yaml"
kill -0 "$pid"
test "$(cat "$root/run/collector.pid")" = "$pid"
test "$(readlink "/proc/$pid/exe")" = "$exe"
`
