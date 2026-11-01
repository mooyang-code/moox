package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"

	"github.com/spf13/cobra"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// setupRestartTarget 是一次重启要动的组件。Directory 是单元目录，外部程序由 systemd 托管，Directory 是部署根。
type setupRestartTarget struct {
	Host      setupconfig.Host
	Component string
	Directory string
	External  bool
}

func newSetupRestartCommand(deps setupDeps) *cobra.Command {
	var file, host, component string
	var yes bool
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "重启主机上的单个组件（默认只预览，--yes 才重启）",
		Long:  "只重启一个组件，不换程序和配置。普通组件走维护锁，和看护不冲突；因子引擎、归档 worker 走 systemd 并等健康检查通过。",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			target, err := setupRestartLocate(snapshot.Manifest, host, component)
			if err != nil {
				return err
			}
			if !yes {
				return writePreview(cmd, map[string]any{"action": "restart_component", "host": target.Host.Name, "component": component, "directory": target.Directory})
			}
			if deps.restartComponent == nil {
				return errors.New("setup restart dependencies are unavailable")
			}
			status, err := deps.restartComponent(cmd.Context(), target)
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{"host": target.Host.Name, "component": component, "status": status})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&host, "host", "", "主机 ID")
	cmd.Flags().StringVar(&component, "component", "", "组件 ID，比如 monitor、collector-stream")
	cmd.Flags().BoolVar(&yes, "yes", false, "确认重启")
	_ = cmd.MarkFlagRequired("host")
	_ = cmd.MarkFlagRequired("component")
	return cmd
}

// setupRestartLocate 校验主机和组件，算出单元目录。校验写法和 setup logs 一致。
func setupRestartLocate(manifest setupconfig.Manifest, hostID, component string) (setupRestartTarget, error) {
	host := manifest.HostByID(hostID)
	if host.Name == "" {
		return setupRestartTarget{}, fmt.Errorf("moox.toml 里没有主机 %q", hostID)
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return setupRestartTarget{}, err
	}
	entry, found := catalog.Component(component)
	if !found {
		return setupRestartTarget{}, fmt.Errorf("服务目录里没有组件 %q", component)
	}
	if entry.Scope != "host" && !slices.Contains(manifest.Deployments[hostID], component) {
		return setupRestartTarget{}, fmt.Errorf("组件 %s 没有部署在主机 %s 上", component, hostID)
	}
	paths := manifest.Paths.Resolved()
	if _, external := unitdeploy.ExternalProgramUnit(component); external {
		return setupRestartTarget{Host: host, Component: component, Directory: paths.DeployRoot, External: true}, nil
	}
	profile, ok := unitpackage.ProfileOf(component)
	if !ok {
		return setupRestartTarget{}, fmt.Errorf("组件 %s 不属于任何部署单元，不能单独重启", component)
	}
	directory := path.Join(paths.DeployRoot, profile)
	switch profile {
	case "control":
		directory = paths.ControlRoot
	case "storage":
		directory = paths.StorageRoot
	}
	return setupRestartTarget{Host: host, Component: component, Directory: directory}, nil
}

func defaultSetupRestartComponent(ctx context.Context, target setupRestartTarget) (json.RawMessage, error) {
	client, err := dialSetupHost(ctx, target.Host)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	if target.External {
		if err := unitdeploy.StartExternalService(ctx, client, target.Component, target.Directory); err != nil {
			return nil, err
		}
		return json.RawMessage(`"running"`), nil
	}
	current := path.Join(target.Directory, "current")
	output, err := client.Run(ctx, []string{path.Join(current, "restart.sh"), "--components", target.Component}, nil)
	if err != nil || output.ExitCode != 0 {
		return nil, fmt.Errorf("restart_failed: 重启 %s 失败，用 setup logs --host %s --component %s 查原因", target.Component, target.Host.Name, target.Component)
	}
	output, err = client.Run(ctx, []string{path.Join(current, "status.sh"), "--components", target.Component}, nil)
	if err != nil || output.ExitCode != 0 || !json.Valid([]byte(output.Stdout)) {
		return nil, fmt.Errorf("restart_unverified: %s 已重启，但读不到状态，用 setup logs --host %s --component %s 确认", target.Component, target.Host.Name, target.Component)
	}
	return json.RawMessage(output.Stdout), nil
}
