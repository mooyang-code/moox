package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	"github.com/spf13/cobra"
)

type setupCoreBootstrap func(context.Context, *setupconfig.Snapshot, unitdeploy.CoreOptions) (unitdeploy.CoreResult, error)
type setupControlBootstrap func(context.Context, *setupconfig.Snapshot, unitdeploy.ControlOptions) (unitdeploy.CoreResult, error)

func newSetupBootstrapCommand(deps setupDeps) *cobra.Command {
	var file, source, stage string
	var options unitdeploy.CoreOptions
	var control unitdeploy.ControlOptions
	if deps.bootstrapCore == nil {
		deps.bootstrapCore = unitdeploy.BootstrapCore
	}
	if deps.bootstrapControl == nil {
		deps.bootstrapControl = unitdeploy.BootstrapControl
	}
	command := &cobra.Command{
		Use:   "bootstrap",
		Short: "通过已核验 SSH 初始化核心或完整控制服务",
		Long:  "--stage core 初始化 Admin、EventBus、Host Gateway、Host Agent，保留全量拓扑并安装本机操作员身份；--stage control 在 Storage/出口依赖实际就绪后，用原始软件和凭据部署完整 control 放置及 Console Proxy。Factor 必须使用已经准备好 pandas/numpy 的目标 Python。首次 internal CA 须显式创建或提供关闭后的可信导入目录。纯 Go 和前端默认在本机构建；只有三个 Storage CGO 服务使用 compile_host。重试必须保留原 state-dir、原配置和原授权；完整舰队、云资源及正式验收继续由外层流程完成。",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if stage != "core" && stage != "control" {
				return errors.New("bootstrap requires --stage core or --stage control")
			}
			if stage == "core" && (control.ProxyCA.Create || control.ProxyCA.ImportDirectory != "" || control.FactorPython != "") {
				return errors.New("proxy authorization and Factor Python belong to --stage control")
			}
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if err := snapshot.VerifyUnchanged(); err != nil {
				return errors.New("config_changed")
			}
			if err := resolveSetupUnitOptions(snapshot, source, &options); err != nil {
				return err
			}
			options.Log = command.ErrOrStderr()
			var result unitdeploy.CoreResult
			if stage == "core" {
				result, err = deps.bootstrapCore(command.Context(), snapshot, options)
			} else {
				control.CoreOptions = options
				result, err = deps.bootstrapControl(command.Context(), snapshot, control)
			}
			if err != nil {
				return err
			}
			return writeSetupJSON(command, result)
		},
	}
	command.Flags().StringVar(&file, "file", defaultSetupFile, "原仓库中的私有 moox.toml")
	command.Flags().StringVar(&stage, "stage", "", "执行阶段：core/control")
	command.Flags().StringVar(&source, "source-dir", "", "源码与公共配置模板所在 worktree（默认当前目录）")
	command.Flags().StringVar(&options.BinaryDirectory, "binary-dir", "", "可选：本机预先构建的目标 Linux host/control 二进制目录")
	command.Flags().StringVar(&options.StateDirectory, "state-dir", "", "持久私有部署状态目录；重试必须使用原目录")
	command.Flags().StringVar(&options.OperatorDirectory, "operator-dir", "", "本机操作员身份目录（默认 ~/.config/moox）")
	command.Flags().BoolVar(&control.ProxyCA.Create, "create-proxy-ca", false, "首次 internal 代理安装的一次性 CA 创建授权")
	command.Flags().StringVar(&control.ProxyCA.ImportDirectory, "proxy-ca-import-dir", "", "目标机上已关闭并核验原信任证明的规范代理状态快照")
	command.Flags().StringVar(&control.FactorPython, "factor-python", "", "目标机已准备 pandas/numpy 的 Python 绝对路径（默认 python3）")
	return command
}

func resolveSetupUnitOptions(snapshot *setupconfig.Snapshot, source string, options *unitdeploy.CoreOptions) error {
	var err error
	if source == "" {
		source, err = os.Getwd()
	}
	if err != nil {
		return err
	}
	options.RepositoryRoot, err = filepath.Abs(source)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if options.StateDirectory == "" {
		binding := snapshot.Manifest.ControlHost().Name + "\x00" + snapshot.Manifest.ControlHost().Address + "\x00" + snapshot.Manifest.Paths.DeployRoot
		digest := sha256.Sum256([]byte(binding))
		options.StateDirectory = filepath.Join(home, ".local", "state", "moox", "setup", hex.EncodeToString(digest[:16]))
	}
	if options.OperatorDirectory == "" {
		options.OperatorDirectory = filepath.Join(home, ".config", "moox")
	}
	options.StateDirectory, err = filepath.Abs(options.StateDirectory)
	if err != nil {
		return err
	}
	options.OperatorDirectory, err = filepath.Abs(options.OperatorDirectory)
	if err != nil {
		return err
	}
	if options.BinaryDirectory != "" {
		options.BinaryDirectory, err = filepath.Abs(options.BinaryDirectory)
		if err != nil {
			return err
		}
	}
	return nil
}
