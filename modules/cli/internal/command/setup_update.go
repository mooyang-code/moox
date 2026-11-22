package command

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
)

// updateStepKinds 是覆盖更新要做的步骤：安装步骤里去掉初始化、启动空间与验收，部署顺序和安装一致。
// 不跑 setup init（含 --stage storage）：它会按种子写回业务空间与元数据；不跑 setup start：它会重新启用手工停用的任务、重建云函数。
var updateStepKinds = []string{installOperator, installFirewall, installDeploy, installReady, installBackup}

// setupUpdateSteps 排出覆盖更新的顺序：和安装同样先装控制机的 Admin 与主机级组件、再装带 EventBus 与 Storage 的主机，
// 然后控制机其余组件和其余主机（外部程序所在的排最后），统一等一次就绪，最后备份配置。
func setupUpdateSteps(manifest *topology.Config) []setupInstallStep {
	return slices.DeleteFunc(setupInstallSteps(manifest, ""), func(step setupInstallStep) bool { return !slices.Contains(updateStepKinds, step.Kind) })
}

// setupUpdateHost 是覆盖更新预览里的一台主机：要更新、要新装、要删掉的组件。
type setupUpdateHost struct {
	Host    string   `json:"host"`
	Version string   `json:"version,omitempty"`
	Update  []string `json:"update"`
	Install []string `json:"install"`
	Remove  []string `json:"remove"`
}

func setupUpdateHosts(statuses []deploy.HostStatus) []setupUpdateHost {
	hosts := make([]setupUpdateHost, 0, len(statuses))
	for _, status := range statuses {
		host := setupUpdateHost{Host: status.Host, Version: status.Version, Update: []string{}, Install: []string{}, Remove: []string{}}
		for _, component := range status.Components {
			switch {
			case component.Extra:
				host.Remove = append(host.Remove, component.ID)
			case component.Deployed():
				host.Update = append(host.Update, component.ID)
			default:
				host.Install = append(host.Install, component.ID)
			}
		}
		hosts = append(hosts, host)
	}
	return hosts
}

func newSetupUpdateCommand(deps setupDeps) *cobra.Command {
	var options setupInstallOptions
	cmd := &cobra.Command{
		Use:   "update",
		Short: "覆盖更新已装好的系统：按安装顺序把这个版本的程序和配置重新部署到全部主机，数据保留（默认只预览）",
		Long: `不带 --yes 只读预览：每台主机要更新、新装（moox.toml 里新加的组件或主机）、删掉（moox.toml 里去掉的组件）的组件，以及执行步骤。
带 --yes 按顺序执行：准备这台电脑的操作身份、放行防火墙、控制机先装 Admin、主机网关与 HostAgent，再装带 EventBus、Storage 的主机，
然后控制机其余组件和其余主机，统一等一次全部组件就绪，最后备份配置。每台主机只重启二进制或配置有变化的组件，用 setup stop 停掉的组件保持停用。
不初始化（不写业务空间、元数据、初始秘密和告警渠道）、不启动空间（手工停用的任务保持停用，不重建云函数），数据目录不动。
目标机都没装过时改用 setup install。中途失败修好后重跑同一条命令，每一步都可以重做。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(options.file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if err := options.source.resolve(); err != nil {
				return err
			}
			status, err := setupDeploymentStatus(cmd.Context(), deps, snapshot)
			if err != nil {
				return err
			}
			if status.Route != deployscan.RouteInstalled {
				return errors.New("update_not_installed: 目标机都没装过，没有可以覆盖更新的部署；新安装用 setup install --space-id <空间>")
			}
			steps := setupUpdateSteps(snapshot.Manifest)
			if !options.yes {
				return writePreview(cmd, map[string]any{
					"action": "update", "route": status.Route, "hosts": setupUpdateHosts(status.Components), "steps": steps,
					"manual_steps": status.ManualSteps, "warnings": status.Warnings, "notes": status.Notes,
					"note": "数据保留；不初始化、不启动空间：手工停用的任务和组件保持停用。配置每次部署都从这个版本的模板重新渲染，手工调过的值要写在 [overrides] 里才会保留",
				})
			}
			if len(status.ManualSteps) != 0 {
				return fmt.Errorf("manual_steps_required: 先做完这些再重跑：%s", strings.Join(status.ManualSteps, "；"))
			}
			started := time.Now()
			if err := runSetupSteps(cmd, deps, snapshot, options, steps, "update_step_failed"); err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{"status": "updated", "seconds": time.Since(started).Round(time.Second).Seconds()})
		},
	}
	cmd.Flags().StringVar(&options.file, "file", defaultSetupFile, "moox.toml 的路径（同目录要有 moox.secrets.toml）")
	cmd.Flags().BoolVar(&options.yes, "yes", false, "确认执行，不加只预览")
	options.source.flags(cmd)
	return cmd
}
