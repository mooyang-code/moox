package command

import (
	"context"
	"errors"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	"github.com/spf13/cobra"
)

type setupUnitDeploy func(context.Context, *setupconfig.Snapshot, unitdeploy.UnitOptions) (unitdeploy.UnitResult, error)

func newSetupDeployUnitCommand(deps setupDeps) *cobra.Command {
	var file, source string
	var options unitdeploy.UnitOptions
	if deps.deployUnit == nil {
		deps.deployUnit = unitdeploy.DeployUnit
	}
	command := &cobra.Command{Use: "deploy-unit", Short: "通过已核验 SSH 部署业务单元", Long: "先完成核心初始化及目标主机部署，再安装该主机已登记的 access、egress-proxy、trade 或 storage 单元。纯 Go 默认在本机构建；Storage 必须通过 --binary-dir 使用编译机已构建的 Linux CGO 制品。重试复用原始软件、身份和配置，保留暂停状态；结果仅表示所选单元，不代表完整系统初始化成功。", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		if options.HostID == "" || options.Profile == "" || options.Profile == "host" {
			return errors.New("deploy-unit requires --host and a business --profile")
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
		result, err := deps.deployUnit(command.Context(), snapshot, options)
		if err != nil {
			return err
		}
		return writeSetupJSON(command, result)
	}}
	command.Flags().StringVar(&file, "file", defaultSetupFile, "原仓库中的私有 moox.toml")
	command.Flags().StringVar(&options.HostID, "host", "", "已登记主机 ID")
	command.Flags().StringVar(&options.Profile, "profile", "", "业务单元：access/egress-proxy/trade/storage")
	command.Flags().StringVar(&source, "source-dir", "", "源码与公共配置模板所在 worktree（默认当前目录）")
	command.Flags().StringVar(&options.BinaryDirectory, "binary-dir", "", "可选：目标 Linux 预构建目录；Storage 必须填写")
	command.Flags().StringVar(&options.StateDirectory, "state-dir", "", "bootstrap core 创建的持久私有状态目录")
	command.Flags().StringVar(&options.OperatorDirectory, "operator-dir", "", "本机操作员身份目录（默认 ~/.config/moox）")
	return command
}
