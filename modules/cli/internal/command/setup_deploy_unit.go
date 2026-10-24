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
	command := &cobra.Command{Use: "deploy-unit", Short: "通过已核验 SSH 部署业务单元", Long: "先完成核心初始化及目标主机部署，再安装该主机已登记的 access、egress-proxy、trade、collector-stream、storage、factor-engine 或 archive-worker 单元（collector-stream 首次安装也走这里，之后的二进制升级可用 native-release.sh install）。factor-engine 与 archive-worker 是外部程序：软件包随版本制品交付，由 systemd 用户服务托管（目标机需开启 linger），主机或部署在 Admin 里已停用时只安装文件、不启动；归档根目录取 moox.toml 的 [paths] archive_root。纯 Go 默认在本机构建；Storage 必须通过 --binary-dir 使用已构建的 Linux CGO 制品（setup build-linux 在本机 Docker 里构建）。重试复用原始软件、身份和配置，保留暂停状态；结果仅表示所选单元，不代表完整系统初始化成功。", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
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
	command.Flags().StringVar(&options.Profile, "profile", "", "业务单元：access/egress-proxy/trade/collector-stream/storage/factor-engine/archive-worker")
	command.Flags().StringVar(&source, "source-dir", "", "源码与公共配置模板所在 worktree（默认当前目录）")
	command.Flags().StringVar(&options.BinaryDirectory, "binary-dir", "", "可选：目标 Linux 预构建目录；Storage 必须填写")
	command.Flags().StringVar(&options.StateDirectory, "state-dir", "", "bootstrap core 创建的持久私有状态目录")
	command.Flags().StringVar(&options.OperatorDirectory, "operator-dir", "", "本机操作员身份目录（默认 ~/.config/moox）")
	command.Flags().StringVar(&options.Release, "release", "", "可选：setup release build 产出的版本 ID；为空时修复首次安装的发布")
	return command
}
