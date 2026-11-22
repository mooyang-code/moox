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

// setupUpdateNote 是覆盖更新预览里固定的说明：不做的事和它们的代价。
const setupUpdateNote = "数据保留；不初始化、不启动空间：手工停用的任务和组件保持停用。新版本带来的业务空间、数据集、内置采集任务和云函数代码包不会随覆盖更新到位，" +
	"在 moox.toml 里新开了云函数采集、日志服务或换了云账号也不会生效，需要时再运行 setup init（只建不改）。" +
	"配置每次部署都从这个版本的模板重新渲染，手工调过的值要写在 [overrides] 里才会保留"

// setupUpdateMove 是换了主机的组件：旧主机上会停掉删除，新主机上从空数据开始，数据不迁移。
type setupUpdateMove struct {
	Component string `json:"component"`
	From      string `json:"from"`
	To        string `json:"to"`
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

// setupUpdateMoves 找出从一台主机挪到另一台的组件：在一台上要删掉、在另一台上要新装。
func setupUpdateMoves(hosts []setupUpdateHost) []setupUpdateMove {
	moves := []setupUpdateMove{}
	for _, from := range hosts {
		for _, id := range from.Remove {
			for _, to := range hosts {
				if to.Host != from.Host && slices.Contains(to.Install, id) {
					moves = append(moves, setupUpdateMove{Component: id, From: from.Host, To: to.Host})
				}
			}
		}
	}
	return moves
}

// setupUpdateMissingCore 返回还没装过的控制机与存储机：它们没装好时覆盖更新会新建 Admin 库与 CA、Collector 读不到元数据，应该接着做完安装。
func setupUpdateMissingCore(manifest *topology.Config, statuses []deploy.HostStatus) []string {
	var missing []string
	for _, host := range []string{manifest.ControlHost().Name, manifest.StorageHost().Name} {
		at := slices.IndexFunc(statuses, func(status deploy.HostStatus) bool { return status.Host == host })
		if (at < 0 || !statuses[at].Installed) && !slices.Contains(missing, host) {
			missing = append(missing, host)
		}
	}
	return missing
}

func newSetupUpdateCommand(deps setupDeps) *cobra.Command {
	var options setupInstallOptions
	cmd := &cobra.Command{
		Use:   "update",
		Short: "覆盖更新已装好的系统：按安装顺序把这个版本的程序和配置重新部署到全部主机，数据保留（默认只预览）",
		Long: `不带 --yes 只读预览：每台主机要更新、新装（moox.toml 里新加的组件或主机）、删掉（moox.toml 里去掉的组件）的组件，以及执行步骤。
带 --yes 按顺序执行：准备这台电脑的操作身份、放行防火墙、控制机先装 Admin、主机网关与 HostAgent，再装带 EventBus、Storage 的主机，
然后控制机其余组件和其余主机，统一等一次全部组件就绪，最后备份配置。每台主机只重启二进制或配置有变化的组件，用 setup stop 停掉的组件保持停用。
不初始化（不写业务空间、元数据、初始秘密和告警渠道）、不启动空间（手工停用的任务保持停用，不重建云函数），数据目录不动；
新版本带来的空间、数据集、内置任务和云函数代码包，以及 moox.toml 里新开的云函数采集、日志服务、换的云账号，要再运行 setup init 才到位。
组件换了主机时旧主机上的会停掉删除，新主机上从空数据开始，数据不迁移（预览的 moves 单列）。备份配置失败只提醒，不算更新失败。
控制机或存储机还没装过时拒绝执行：那是安装没做完，重跑 setup install。中途失败修好后重跑同一条命令，每一步都可以重做。`,
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
			if missing := setupUpdateMissingCore(snapshot.Manifest, status.Components); len(missing) != 0 {
				return fmt.Errorf("update_not_installed: 主机 %s 还没装过（控制机或存储机），这是上次安装没做完：重跑 setup install --space-id <空间>", strings.Join(missing, "、"))
			}
			steps := setupUpdateSteps(snapshot.Manifest)
			hosts := setupUpdateHosts(status.Components)
			if !options.yes {
				return writePreview(cmd, map[string]any{
					"action": "update", "route": status.Route, "hosts": hosts, "moves": setupUpdateMoves(hosts), "steps": steps,
					"manual_steps": status.ManualSteps, "warnings": status.Warnings, "notes": status.Notes, "note": setupUpdateNote,
				})
			}
			if len(status.ManualSteps) != 0 {
				return fmt.Errorf("manual_steps_required: 先做完这些再重跑：%s", strings.Join(status.ManualSteps, "；"))
			}
			started := time.Now()
			// 最后一步是备份配置：它用秘密文件里的管理员密码登录，用户在管理台改过密码时会失败，这不影响已经部署好的主机，只提醒。
			last := len(steps) - 1
			if err := runSetupSteps(cmd, deps, snapshot, options, steps[:last], len(steps), "update_step_failed"); err != nil {
				return err
			}
			result := map[string]any{"status": "updated"}
			fmt.Fprintf(cmd.ErrOrStderr(), "[%d/%d] %s\n", len(steps), len(steps), steps[last].Command)
			if err := executeSetupInstallStep(cmd, deps, snapshot, options, steps[last]); err != nil {
				result["warnings"] = []string{fmt.Sprintf("全部主机已更新，但备份配置失败：%v。之后可以单独运行 %s", err, steps[last].Command)}
			}
			result["seconds"] = time.Since(started).Round(time.Second).Seconds()
			return writeSetupJSON(cmd, result)
		},
	}
	cmd.Flags().StringVar(&options.file, "file", defaultSetupFile, "moox.toml 的路径（同目录要有 moox.secrets.toml）")
	cmd.Flags().BoolVar(&options.yes, "yes", false, "确认执行，不加只预览")
	options.source.flags(cmd)
	return cmd
}
