package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupdeploy "github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/release"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// configHost 是 moox-cli config 读取主机上当前配置用到的 SSH 能力。
type configHost interface {
	Run(ctx context.Context, argv []string, stdin io.Reader) (setupssh.Result, error)
	Close() error
}

type configDeps struct {
	load func(string) (*setupconfig.Snapshot, error)
	dial func(context.Context, setupconfig.Host) (configHost, error)
	// render 渲染一台主机的发布。
	render func(setupconfig.Manifest, string) (release.Plan, error)
	// deploy 以复用二进制的方式重新部署一台主机上的几个组件。
	deploy func(ctx context.Context, snapshot *setupconfig.Snapshot, file, hostID string, components []string, out io.Writer) error
}

// configTarget 是由 moox.toml 派生的一组配置：一台主机上几个组件的配置文件，变更后重新部署这些组件。
type configTarget struct {
	ID     string
	HostID string
	Root   string
	// Files 是发布目录中的配置文件（相对路径）。
	Files      []string
	Components []string
	Format     string // "json" 或 "yaml"
	// shrinks 列出新配置缩短的保留期。
	shrinks func(current, rendered []byte) []string
}

// configTargetPlan 是一个目标的对比结果。
type configTargetPlan struct {
	ID          string   `json:"id"`
	Host        string   `json:"host"`
	Files       []string `json:"files"`
	Status      string   `json:"status"`
	ChangedKeys []string `json:"changed_keys,omitempty"`
	Redeploy    []string `json:"redeploy,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
	Shrinks     []string `json:"retention_shrinks,omitempty"`
}

const (
	configStatusUnchanged   = "unchanged"
	configStatusChanged     = "changed"
	configStatusNotDeployed = "not_deployed"
)

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
		render: func(manifest setupconfig.Manifest, hostID string) (release.Plan, error) {
			root, err := os.Getwd()
			if err != nil {
				return release.Plan{}, err
			}
			return release.Render(manifest, hostID, release.Options{RepositoryRoot: root})
		},
		deploy: func(ctx context.Context, snapshot *setupconfig.Snapshot, file, hostID string, components []string, out io.Writer) error {
			deployer, closeDeployer, err := defaultOpenSetupDeployer(snapshot, file, out, false)
			if err != nil {
				return err
			}
			defer closeDeployer()
			_, err = deployer.Deploy(ctx, hostID, setupdeploy.Options{Components: components, ReuseBinaries: true})
			return err
		},
	}
}

func newConfigCommand(deps configDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "把 moox.toml 的变更发布到各主机的组件配置",
		Long: `config plan 按 moox.toml 渲染各主机上由它派生的配置，与主机当前发布中的文件对比，不做任何修改；
config publish 以复用二进制的方式重新部署配置有变更的组件（生成新的发布，启动失败时自动切回原发布）。`,
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
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			plans, warnings, err := planConfig(cmd.Context(), deps, snapshot, only)
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, configPlanOutput(plans, warnings))
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "moox.toml 路径")
	cmd.Flags().StringSliceVar(&only, "only", nil, "只处理指定目标（storage-policy、collector-runtime）")
	return cmd
}

func newConfigPublishCommand(deps configDeps) *cobra.Command {
	var file string
	var only []string
	var yes, allowShrink bool
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "重新部署配置有变更的组件并验证",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes {
				return errors.New("config publish 会重新部署并重启组件；请先用 moox-cli config plan 核对，再加 --yes")
			}
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			plans, warnings, err := planConfig(cmd.Context(), deps, snapshot, only)
			if err != nil {
				return err
			}
			if !allowShrink {
				for _, plan := range plans {
					if len(plan.Shrinks) > 0 {
						return fmt.Errorf("%s 缩短了保留期（%s）；超出新保留期的数据会在下次清理时删除，确认后加 --allow-retention-shrink", plan.ID, strings.Join(plan.Shrinks, "; "))
					}
				}
			}
			published := []string{}
			for _, plan := range plans {
				if plan.Status != configStatusChanged {
					continue
				}
				if err = deps.deploy(cmd.Context(), snapshot, file, plan.Host, plan.Redeploy, cmd.ErrOrStderr()); err != nil {
					err = fmt.Errorf("%s: 重新部署主机 %s 上的 %s: %w", plan.ID, plan.Host, strings.Join(plan.Redeploy, "、"), err)
					break
				}
				published = append(published, plan.ID+"@"+plan.Host)
			}
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
	cmd.Flags().StringSliceVar(&only, "only", nil, "只发布指定目标（storage-policy、collector-runtime）")
	cmd.Flags().BoolVar(&yes, "yes", false, "确认重新部署并重启受影响的组件")
	cmd.Flags().BoolVar(&allowShrink, "allow-retention-shrink", false, "允许缩短保留期（会删除超出新保留期的数据）")
	return cmd
}

func configPlanOutput(plans []configTargetPlan, warnings []string) map[string]any {
	changed := 0
	for _, plan := range plans {
		if plan.Status == configStatusChanged {
			changed++
		}
	}
	return map[string]any{"targets": plans, "changed": changed, "warnings": warnings}
}

// configTargets 列出由 moox.toml 派生、可以单独发布的配置：每台存储主机上的存储策略，以及行情采集服务的运行配置。
func configTargets(manifest setupconfig.Manifest) []configTarget {
	var targets []configTarget
	for _, hostID := range manifest.HostsOf("storage-primary") {
		host, _ := manifest.Host(hostID)
		target := configTarget{ID: "storage-policy", HostID: hostID, Root: host.Root, Format: "json", shrinks: retentionShrinks}
		for _, component := range []string{"storage-primary", "storage-view"} {
			if manifest.HasComponent(hostID, component) {
				target.Components = append(target.Components, component)
				target.Files = append(target.Files, component+"/config/storage-policy.json")
			}
		}
		targets = append(targets, target)
	}
	for _, hostID := range manifest.HostsOf("collector") {
		host, _ := manifest.Host(hostID)
		targets = append(targets, configTarget{
			ID: "collector-runtime", HostID: hostID, Root: host.Root, Format: "yaml",
			Files: []string{"collector/config/app.yaml"}, Components: []string{"collector"},
		})
	}
	return targets
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
	found := map[string]bool{}
	for _, target := range targets {
		if wanted[target.ID] {
			selected = append(selected, target)
			found[target.ID] = true
		}
	}
	for id := range wanted {
		if !found[id] {
			return nil, fmt.Errorf("未知的配置目标 %q（可选 storage-policy、collector-runtime）", id)
		}
	}
	return selected, nil
}

func planConfig(ctx context.Context, deps configDeps, snapshot *setupconfig.Snapshot, only []string) ([]configTargetPlan, []string, error) {
	targets, err := selectConfigTargets(configTargets(snapshot.Manifest), only)
	if err != nil {
		return nil, nil, err
	}
	_, coverage := snapshot.Manifest.StoragePolicy().Coverage()
	hosts := map[string]configHost{}
	rendered := map[string]release.Plan{}
	defer func() {
		for _, host := range hosts {
			_ = host.Close()
		}
	}()
	plans := make([]configTargetPlan, 0, len(targets))
	for _, target := range targets {
		host, ok := hosts[target.HostID]
		if !ok {
			definition, _ := snapshot.Manifest.Host(target.HostID)
			host, err = deps.dial(ctx, definition)
			if err != nil {
				return nil, nil, fmt.Errorf("连接主机 %s（%s）: %w", target.HostID, target.ID, err)
			}
			hosts[target.HostID] = host
		}
		plan, ok := rendered[target.HostID]
		if !ok {
			if plan, err = deps.render(snapshot.Manifest, target.HostID); err != nil {
				return nil, nil, fmt.Errorf("渲染主机 %s 的发布: %w", target.HostID, err)
			}
			rendered[target.HostID] = plan
		}
		result, err := planConfigTarget(ctx, host, target, plan)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", target.ID, err)
		}
		plans = append(plans, result)
	}
	return plans, coverage, nil
}

// planConfigTarget 对比一个目标在主机当前发布中的文件与按 moox.toml 渲染的文件。
func planConfigTarget(ctx context.Context, host configHost, target configTarget, rendered release.Plan) (configTargetPlan, error) {
	plan := configTargetPlan{ID: target.ID, Host: target.HostID, Files: target.Files, Status: configStatusUnchanged}
	keys := map[string]bool{}
	for _, file := range target.Files {
		next, ok := renderedFile(rendered, file)
		if !ok {
			return plan, fmt.Errorf("发布中没有 %s", file)
		}
		current, err := readConfigFile(ctx, host, target.Root+"/current/"+file)
		if err != nil {
			return plan, err
		}
		if current == nil {
			plan.Status = configStatusNotDeployed
			plan.Warnings = []string{"主机当前发布中没有 " + file + "，请先用 moox-cli setup deploy-host 部署"}
			return plan, nil
		}
		if string(current) == string(next) {
			continue
		}
		plan.Status = configStatusChanged
		changed, err := changedConfigKeys(target.Format, current, next)
		if err != nil {
			return plan, err
		}
		for _, key := range changed {
			keys[key] = true
		}
		if target.shrinks != nil && len(plan.Shrinks) == 0 {
			plan.Shrinks = target.shrinks(current, next)
		}
	}
	if plan.Status == configStatusChanged {
		plan.ChangedKeys = sortedBoolKeys(keys)
		plan.Redeploy = append([]string(nil), target.Components...)
	}
	return plan, nil
}

func renderedFile(plan release.Plan, path string) ([]byte, bool) {
	for _, file := range plan.Files {
		if file.Path == path {
			return file.Data, true
		}
	}
	return nil, false
}

// readConfigFile 读取主机上的文件；文件不存在时返回 nil。
func readConfigFile(ctx context.Context, host configHost, file string) ([]byte, error) {
	result, err := host.Run(ctx, []string{"sh", "-c", `if [ -f "$1" ]; then cat "$1"; else exit 3; fi`, "moox-config-read", file}, nil)
	// 退出码非零时同时返回结果和错误；只有没有退出码的错误才是连接失败。
	if err != nil && result.ExitCode == 0 {
		return nil, fmt.Errorf("读取 %s: %w", file, err)
	}
	switch result.ExitCode {
	case 0:
		return []byte(result.Stdout), nil
	case 3:
		return nil, nil
	default:
		return nil, fmt.Errorf("读取 %s: 退出码 %d: %s", file, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
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
