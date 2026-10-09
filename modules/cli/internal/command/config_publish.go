package command

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// configHost is the SSH surface `moox-cli config` needs on a host.
type configHost interface {
	Run(ctx context.Context, argv []string, stdin io.Reader) (setupssh.Result, error)
	Upload(ctx context.Context, src io.Reader, size int64, dst string, mode fs.FileMode) error
	Close() error
}

type configDeps struct {
	load func(string) (*setupconfig.Snapshot, error)
	dial func(context.Context, setupconfig.Host) (configHost, error)
}

// configTarget is one host file derived from moox.toml.
type configTarget struct {
	ID       string
	Host     setupconfig.Host
	Root     string
	Path     string
	Format   string // "json" or "yaml"
	Services []string
	// render returns the new file from the current one; current is nil when
	// the file is missing on the host.
	render func(current []byte) ([]byte, error)
	// shrinks lists retention periods the new file shortens.
	shrinks func(current, rendered []byte) []string
}

// configTargetPlan is the plan of one target.
type configTargetPlan struct {
	ID          string   `json:"id"`
	Host        string   `json:"host"`
	Path        string   `json:"path"`
	Status      string   `json:"status"`
	ChangedKeys []string `json:"changed_keys,omitempty"`
	Restart     []string `json:"restart,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
	Shrinks     []string `json:"retention_shrinks,omitempty"`

	target   configTarget
	rendered []byte
}

const (
	configStatusUnchanged   = "unchanged"
	configStatusChanged     = "changed"
	configStatusMissing     = "missing"
	configStatusNotDeployed = "not_deployed"
)

// errConfigNotDeployed marks a target whose module is not deployed on its
// host; it is reported and skipped rather than created.
var errConfigNotDeployed = errors.New("module is not deployed on the host")

func init() {
	rootCmd.AddCommand(newConfigCommand(defaultConfigDeps()))
}

func defaultConfigDeps() configDeps {
	setup := defaultSetupDeps()
	return configDeps{
		load: setup.load,
		dial: func(ctx context.Context, host setupconfig.Host) (configHost, error) {
			return dialSetupHost(ctx, host)
		},
	}
}

func newConfigCommand(deps configDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "把 moox.toml 的变更发布到各主机的模块配置",
		Long: `config plan 渲染由 moox.toml 派生的各主机配置文件并与主机上的当前文件对比，不做任何修改；
config publish 写入有变更的文件、重启受影响的服务并等待就绪，失败时恢复原文件。`,
		SilenceUsage: true,
	}
	cmd.AddCommand(newConfigPlanCommand(deps), newConfigPublishCommand(deps))
	return cmd
}

func newConfigPlanCommand(deps configDeps) *cobra.Command {
	var file string
	var only []string
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "渲染并对比由 moox.toml 派生的配置，不做任何修改",
		RunE: func(cmd *cobra.Command, _ []string) error {
			plans, warnings, err := planConfig(cmd.Context(), deps, file, only)
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, configPlanOutput(plans, warnings))
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "moox.toml 路径")
	cmd.Flags().StringSliceVar(&only, "only", nil, "只处理指定目标（storage-policy、collector-runtime、trade-dns）")
	return cmd
}

func newConfigPublishCommand(deps configDeps) *cobra.Command {
	var file string
	var only []string
	var yes, allowShrink bool
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "写入有变更的配置、重启受影响的服务并验证",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes {
				return errors.New("config publish changes hosts and restarts services; review `moox-cli config plan` and pass --yes")
			}
			plans, warnings, err := planConfig(cmd.Context(), deps, file, only)
			if err != nil {
				return err
			}
			if !allowShrink {
				for _, plan := range plans {
					if len(plan.Shrinks) > 0 {
						return fmt.Errorf("%s shortens retention (%s); rows older than the new retention are deleted at the next cleanup; pass --allow-retention-shrink to publish", plan.ID, strings.Join(plan.Shrinks, "; "))
					}
				}
			}
			published, err := publishConfig(cmd.Context(), deps, plans)
			output := configPlanOutput(plans, warnings)
			output["published"] = published
			if err != nil {
				output["error"] = err.Error()
				_ = writeSetupJSON(cmd, output)
				return err
			}
			return writeSetupJSON(cmd, output)
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "moox.toml 路径")
	cmd.Flags().StringSliceVar(&only, "only", nil, "只发布指定目标（storage-policy、collector-runtime、trade-dns）")
	cmd.Flags().BoolVar(&yes, "yes", false, "确认写入并重启受影响的服务")
	cmd.Flags().BoolVar(&allowShrink, "allow-retention-shrink", false, "允许缩短保留期（会删除超出新保留期的数据）")
	return cmd
}

func configPlanOutput(plans []configTargetPlan, warnings []string) map[string]any {
	changed := 0
	for _, plan := range plans {
		if plan.Status == configStatusChanged || plan.Status == configStatusMissing {
			changed++
		}
	}
	return map[string]any{"targets": plans, "changed": changed, "warnings": warnings}
}

// configTargets is the publish target registry. Every host file derived
// from moox.toml is listed here; deployment renders the same files.
func configTargets(snapshot *setupconfig.Snapshot) ([]configTarget, error) {
	manifest := snapshot.Manifest
	paths := manifest.Paths.Resolved()
	// Without a dedicated storage_host, Storage runs on the control host.
	storageHost := manifest.ControlHost
	if manifest.HasStorageHost() {
		host, err := resolveStorageDeploymentHost(manifest, "")
		if err != nil {
			return nil, fmt.Errorf("resolve Storage host: %w", err)
		}
		storageHost = host
	}
	policy := manifest.StoragePolicy()
	targets := []configTarget{{
		ID: "storage-policy", Host: storageHost, Root: paths.StorageRoot,
		Path: path.Join(paths.StorageRoot, "config", "storage-policy.json"), Format: "json",
		Services: []string{"storage-primary", "storage-view"},
		render: func([]byte) ([]byte, error) {
			return policy.Encode()
		},
		shrinks: retentionShrinks,
	}, {
		ID: "collector-runtime", Host: manifest.ControlHost, Root: paths.ControlRoot,
		Path: path.Join(paths.ControlRoot, "collector", "config", "app.yaml"), Format: "yaml",
		Services: []string{"collector"},
		render: func(current []byte) ([]byte, error) {
			if current == nil {
				return nil, errConfigNotDeployed
			}
			return setupconfig.RenderCollectorRuntimeConfig(snapshot, current)
		},
	}}
	if hostID := manifest.PlacementHost("egress-proxy"); hostID != "" {
		host, err := findSetupHost(manifest, hostID)
		if err != nil {
			return nil, err
		}
		targets = append(targets, configTarget{
			ID: "egress-policy", Host: host, Root: paths.ControlRoot,
			Path: path.Join(paths.ControlRoot, "egress-proxy", "config", "app.yaml"), Format: "yaml",
			Services: []string{"egress-proxy"},
			render: func(current []byte) ([]byte, error) {
				if current == nil {
					return nil, errConfigNotDeployed
				}
				return setupconfig.RenderEgressConfig(snapshot, current)
			},
		})
	}
	return targets, nil
}

func selectConfigTargets(targets []configTarget, only []string) ([]configTarget, error) {
	if len(only) == 0 {
		return targets, nil
	}
	wanted := make(map[string]bool, len(only))
	for _, id := range only {
		wanted[strings.TrimSpace(id)] = true
	}
	var selected []configTarget
	for _, target := range targets {
		if wanted[target.ID] {
			selected = append(selected, target)
			delete(wanted, target.ID)
		}
	}
	for id := range wanted {
		return nil, fmt.Errorf("unknown config target %q", id)
	}
	return selected, nil
}

func planConfig(ctx context.Context, deps configDeps, file string, only []string) ([]configTargetPlan, []string, error) {
	snapshot, err := deps.load(file)
	if err != nil {
		return nil, nil, err
	}
	defer clearSetupSecrets(snapshot)
	targets, err := configTargets(snapshot)
	if err != nil {
		return nil, nil, err
	}
	if targets, err = selectConfigTargets(targets, only); err != nil {
		return nil, nil, err
	}
	_, coverage := snapshot.Manifest.StoragePolicy().Coverage()
	hosts := map[string]configHost{}
	defer func() {
		for _, host := range hosts {
			_ = host.Close()
		}
	}()
	plans := make([]configTargetPlan, 0, len(targets))
	for _, target := range targets {
		host, ok := hosts[target.Host.Name]
		if !ok {
			host, err = deps.dial(ctx, target.Host)
			if err != nil {
				return nil, nil, fmt.Errorf("connect to %s for %s: %w", target.Host.Name, target.ID, err)
			}
			hosts[target.Host.Name] = host
		}
		plan, err := planConfigTarget(ctx, host, target)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", target.ID, err)
		}
		plans = append(plans, plan)
	}
	return plans, coverage, nil
}

func planConfigTarget(ctx context.Context, host configHost, target configTarget) (configTargetPlan, error) {
	plan := configTargetPlan{ID: target.ID, Host: target.Host.Name, Path: target.Path, target: target}
	current, published, err := readConfigFile(ctx, host, target.Path)
	if err != nil {
		return plan, err
	}
	rendered, err := target.render(current)
	if errors.Is(err, errConfigNotDeployed) {
		plan.Status = configStatusNotDeployed
		plan.Warnings = []string{"the module is not deployed on this host; deploy it first, config publish only updates deployed files"}
		return plan, nil
	}
	if err != nil {
		return plan, err
	}
	plan.rendered = rendered
	switch {
	case current == nil:
		plan.Status = configStatusMissing
	case bytes.Equal(current, rendered):
		plan.Status = configStatusUnchanged
	default:
		plan.Status = configStatusChanged
	}
	if plan.Status == configStatusUnchanged {
		return plan, nil
	}
	plan.Restart = append([]string(nil), target.Services...)
	if current != nil {
		plan.ChangedKeys, err = changedConfigKeys(target.Format, current, rendered)
		if err != nil {
			return plan, err
		}
		if published != "" && published != sha256Hex(current) {
			plan.Warnings = append(plan.Warnings, "the file on the host was edited after the last publish; publishing replaces those edits")
		}
		if target.shrinks != nil {
			plan.Shrinks = target.shrinks(current, rendered)
		}
	}
	return plan, nil
}

// readConfigFile returns the file and the hash recorded by the last
// publish; the file is nil when it does not exist.
func readConfigFile(ctx context.Context, host configHost, file string) ([]byte, string, error) {
	result, err := host.Run(ctx, []string{"sh", "-c", `if [ -f "$1" ]; then cat "$1"; else exit 3; fi`, "moox-config-read", file}, nil)
	// A non-zero exit returns both the result and an error; only an error
	// without an exit status is a transport failure.
	if err != nil && result.ExitCode == 0 {
		return nil, "", fmt.Errorf("read %s: %w", file, err)
	}
	switch result.ExitCode {
	case 0:
	case 3:
		return nil, "", nil
	default:
		return nil, "", fmt.Errorf("read %s: exit %d: %s", file, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	published, err := host.Run(ctx, []string{"sh", "-c", `cat "$1.sha256" 2>/dev/null || true`, "moox-config-read", file}, nil)
	if err != nil {
		return nil, "", fmt.Errorf("read %s.sha256: %w", file, err)
	}
	return []byte(result.Stdout), strings.TrimSpace(published.Stdout), nil
}

// configPublishScript replaces one file under the deployment maintenance
// lock, restarts the services and waits for them (start.sh probes
// readiness). On failure it restores the previous file and restarts again.
const configPublishScript = `set -euo pipefail
root=$1 file=$2 tmp=$3
shift 3
exec 9>"${root}.maintenance.lock"
flock -w 120 9 || { echo "deployment maintenance lock is busy" >&2; exit 75; }
if [ -f "$file" ]; then
  cp -p "$file" "$file.last"
  chmod --reference="$file" "$tmp"
fi
mv -f "$tmp" "$file"
sha256sum "$file" | cut -d' ' -f1 > "$file.sha256"
restart() {
  for service in "$@"; do
    "$root/restart.sh" "$service" 8>&- 9>&- || return 1
  done
}
if ! restart "$@"; then
  echo "restart failed; restoring the previous file" >&2
  if [ -f "$file.last" ]; then
    cp -p "$file.last" "$file"
    sha256sum "$file" | cut -d' ' -f1 > "$file.sha256"
  else
    rm -f "$file" "$file.sha256"
  fi
  restart "$@" || true
  exit 70
fi
`

// publishConfig publishes the changed targets in registry order and stops
// at the first failure; targets already published stay published.
func publishConfig(ctx context.Context, deps configDeps, plans []configTargetPlan) ([]string, error) {
	var published []string
	hosts := map[string]configHost{}
	defer func() {
		for _, host := range hosts {
			_ = host.Close()
		}
	}()
	for _, plan := range plans {
		if plan.Status == configStatusUnchanged || plan.Status == configStatusNotDeployed {
			continue
		}
		host, ok := hosts[plan.target.Host.Name]
		if !ok {
			var err error
			host, err = deps.dial(ctx, plan.target.Host)
			if err != nil {
				return published, fmt.Errorf("connect to %s for %s: %w", plan.target.Host.Name, plan.ID, err)
			}
			hosts[plan.target.Host.Name] = host
		}
		tmp := plan.Path + ".tmp"
		if err := host.Upload(ctx, bytes.NewReader(plan.rendered), int64(len(plan.rendered)), tmp, 0o600); err != nil {
			return published, fmt.Errorf("%s: upload %s: %w", plan.ID, tmp, err)
		}
		argv := append([]string{"bash", "-c", configPublishScript, "moox-config-publish", plan.target.Root, plan.Path, tmp}, plan.target.Services...)
		result, err := host.Run(ctx, argv, nil)
		if err != nil && result.ExitCode == 0 {
			return published, fmt.Errorf("%s: publish: %w", plan.ID, err)
		}
		if result.ExitCode != 0 {
			return published, fmt.Errorf("%s: publish failed (exit %d), previous file restored: %s", plan.ID, result.ExitCode, strings.TrimSpace(result.Stderr))
		}
		published = append(published, plan.ID)
	}
	return published, nil
}

// retentionShrinks lists every (space, frequency) whose retention the new
// policy shortens. Spaces are compared through the defaults so moving a
// value between a space and the defaults is judged by its effect.
func retentionShrinks(current, rendered []byte) []string {
	before, err := storagepolicy.Parse(current)
	if err != nil {
		// An unreadable policy on the host cannot be compared; treat every
		// finite retention as a possible shrink.
		before = storagepolicy.Policy{}
	}
	after, err := storagepolicy.Parse(rendered)
	if err != nil {
		return nil
	}
	spaces := map[string]bool{"": true}
	for spaceID := range before.Retention.Spaces {
		spaces[spaceID] = true
	}
	for spaceID := range after.Retention.Spaces {
		spaces[spaceID] = true
	}
	var shrinks []string
	for _, spaceID := range sortedBoolKeys(spaces) {
		for _, freq := range frequencypkg.Strings() {
			newPeriod, _, err := after.Retention.Resolve(spaceID, freq)
			if err != nil || newPeriod.Forever {
				continue
			}
			oldPeriod, _, err := before.Retention.Resolve(spaceID, freq)
			if err == nil && !oldPeriod.Forever && oldPeriod.Duration <= newPeriod.Duration {
				continue
			}
			where := "defaults"
			if spaceID != "" {
				where = spaceID
			}
			from := "unknown"
			if err == nil {
				from = oldPeriod.String()
			}
			shrinks = append(shrinks, fmt.Sprintf("%s %s: %s -> %s", where, freq, from, newPeriod))
		}
	}
	return shrinks
}

// changedConfigKeys returns the dotted keys whose values differ. Values are
// never reported, so a file holding secrets reveals only key names.
func changedConfigKeys(format string, current, rendered []byte) ([]string, error) {
	decode := func(raw []byte) (any, error) {
		var value any
		var err error
		if format == "json" {
			err = json.Unmarshal(raw, &value)
		} else {
			err = yaml.Unmarshal(raw, &value)
		}
		return value, err
	}
	before, err := decode(current)
	if err != nil {
		// The host file does not parse; every key of the new file changes.
		before = nil
	}
	after, err := decode(rendered)
	if err != nil {
		return nil, fmt.Errorf("rendered file does not parse: %w", err)
	}
	left, right := map[string]string{}, map[string]string{}
	flattenConfig("", before, left)
	flattenConfig("", after, right)
	keys := map[string]bool{}
	for key, value := range right {
		if left[key] != value {
			keys[key] = true
		}
	}
	for key := range left {
		if _, ok := right[key]; !ok {
			keys[key] = true
		}
	}
	return sortedBoolKeys(keys), nil
}

func flattenConfig(prefix string, value any, out map[string]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			flattenConfig(joinConfigKey(prefix, key), child, out)
		}
	case map[any]any:
		for key, child := range typed {
			flattenConfig(joinConfigKey(prefix, fmt.Sprint(key)), child, out)
		}
	default:
		encoded, _ := json.Marshal(typed)
		out[prefix] = string(encoded)
	}
}

func joinConfigKey(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func sortedBoolKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
