package command

import (
	"context"
	"errors"
	"fmt"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/spf13/cobra"
)

type setupHostDeploy func(context.Context, *setupconfig.Snapshot, unitdeploy.HostOptions) (unitdeploy.HostResult, error)

func newSetupDeployHostCommand(deps setupDeps) *cobra.Command {
	var file, source string
	var options unitdeploy.HostOptions
	if deps.deployHost == nil {
		deps.deployHost = unitdeploy.DeployHost
	}
	command := &cobra.Command{Use: "deploy-host", Short: "通过已核验 SSH 部署指定主机的 Host Gateway 与 Host Agent", Long: "先完成 bootstrap --stage admin，再从运行中的 Admin 导出目标主机身份和 EventBus 客户端凭据。重试必须保留原 state-dir；重复执行修复缺失进程并保留暂停状态。结果 host-ready 仅表示两项主机服务就绪；host-paused 表示已安装但存在暂停组件。纯 Go 在本机编译。", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		if options.HostID == "" {
			return errors.New("deploy-host requires --host")
		}
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		if err := snapshot.VerifyUnchanged(); err != nil {
			return errors.New("config_changed")
		}
		if err := resolveSetupUnitOptions(snapshot, source, &options.DeployOptions); err != nil {
			return err
		}
		options.Log = command.ErrOrStderr()
		if options.Release == "" {
			if err := checkInstallDisk(command.Context(), deps, snapshot, options.HostID); err != nil {
				return err
			}
		}
		result, err := deploySetupHost(command.Context(), deps, snapshot, options)
		if err != nil {
			return err
		}
		return writeSetupJSON(command, result)
	}}
	command.Flags().StringVar(&file, "file", defaultSetupFile, "原仓库中的私有 moox.toml")
	command.Flags().StringVar(&options.HostID, "host", "", "主机 ID")
	command.Flags().StringVar(&source, "source-dir", "", "源码与公共配置模板所在 worktree（默认当前目录）")
	command.Flags().StringVar(&options.BinaryDirectory, "binary-dir", "", "可选：本机构建的目标 Linux host 二进制目录")
	command.Flags().StringVar(&options.StateDirectory, "state-dir", "", "bootstrap admin 创建的持久私有状态目录")
	command.Flags().StringVar(&options.OperatorDirectory, "operator-dir", "", "本机操作员身份目录（默认 ~/.config/moox）")
	command.Flags().StringVar(&options.Release, "release", "", "可选：setup release build 产出的版本 ID；为空时修复首次安装的发布")
	return command
}

// deploySetupHost 部署一台主机的主机服务：只能出站的主机先确认控制机端口，装好后安装看护再记完成，setup install 共用。
func deploySetupHost(ctx context.Context, deps setupDeps, snapshot *setupconfig.Snapshot, options unitdeploy.HostOptions) (unitdeploy.HostResult, error) {
	var result unitdeploy.HostResult
	// 只能出站的主机经 control 反向连接接入：先经腾讯云接口确认 control 的端口已放行，再部署。
	if snapshot.Manifest.HostByID(options.HostID).Reachability == servicecatalog.ReachabilityOutbound && deps.ensureRelayFirewall != nil {
		firewall, err := deps.ensureRelayFirewall(ctx, snapshot)
		if err != nil {
			return result, fmt.Errorf("prepare control firewall for outbound host %s: %w", options.HostID, err)
		}
		fmt.Fprintf(options.Log, "control firewall ready for outbound host %s: created=%d already_open=%d\n", options.HostID, firewall.Created, firewall.AlreadyOpen)
	}
	result, err := deps.deployHost(ctx, snapshot, options)
	if err != nil {
		return result, err
	}
	// 主机服务就绪后安装主机级守护（crontab 与告警策略种子）；重复执行是幂等的。
	if deps.installWatchdog != nil {
		host, err := findSetupHost(snapshot.Manifest, options.HostID)
		if err != nil {
			return result, err
		}
		watchdog, err := deps.installWatchdog(ctx, snapshot, host, false)
		if err != nil {
			return result, fmt.Errorf("install watchdog on %s: %w", options.HostID, err)
		}
		fmt.Fprintf(options.Log, "watchdog installed on %s: %s\n", options.HostID, watchdog)
	}
	// 看护也装好才算这台主机完成，否则 next_steps 会跳过它，这台主机就没有自愈了。
	if err := unitdeploy.MarkHostReady(options.StateDirectory, options.HostID); err != nil {
		return result, err
	}
	return result, nil
}
