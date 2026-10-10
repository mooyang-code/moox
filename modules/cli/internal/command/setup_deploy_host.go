package command

import (
	"context"
	"errors"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	"github.com/spf13/cobra"
)

type setupHostDeploy func(context.Context, *setupconfig.Snapshot, unitdeploy.HostOptions) (unitdeploy.HostResult, error)

func newSetupDeployHostCommand(deps setupDeps) *cobra.Command {
	var file, source string
	var options unitdeploy.HostOptions
	if deps.deployHost == nil {
		deps.deployHost = unitdeploy.DeployHost
	}
	command := &cobra.Command{Use: "deploy-host", Short: "通过已核验 SSH 部署指定主机的 Host Gateway 与 Host Agent", Long: "先完成 bootstrap --stage core，再从运行中的 Admin 导出目标主机身份和 EventBus 客户端凭据。重试必须保留原 state-dir；重复执行修复缺失进程并保留暂停状态。结果 host-ready 仅表示两项主机服务就绪；host-paused 表示已安装但存在暂停组件。纯 Go 在本机编译。", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
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
		if err := resolveSetupUnitOptions(snapshot, source, &options.CoreOptions); err != nil {
			return err
		}
		options.Log = command.ErrOrStderr()
		result, err := deps.deployHost(command.Context(), snapshot, options)
		if err != nil {
			return err
		}
		return writeSetupJSON(command, result)
	}}
	command.Flags().StringVar(&file, "file", defaultSetupFile, "原仓库中的私有 moox.toml")
	command.Flags().StringVar(&options.HostID, "host", "", "主机 ID")
	command.Flags().StringVar(&source, "source-dir", "", "源码与公共配置模板所在 worktree（默认当前目录）")
	command.Flags().StringVar(&options.BinaryDirectory, "binary-dir", "", "可选：本机构建的目标 Linux host 二进制目录")
	command.Flags().StringVar(&options.StateDirectory, "state-dir", "", "bootstrap core 创建的持久私有状态目录")
	command.Flags().StringVar(&options.OperatorDirectory, "operator-dir", "", "本机操作员身份目录（默认 ~/.config/moox）")
	return command
}
