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

func newSetupBootstrapCommand(deps setupDeps) *cobra.Command {
	var file, source, stage string
	var options unitdeploy.CoreOptions
	if deps.bootstrapCore == nil {
		deps.bootstrapCore = unitdeploy.BootstrapCore
	}
	command := &cobra.Command{
		Use:   "bootstrap",
		Short: "通过已核验 SSH 初始化控制主机的核心服务",
		Long:  "核心阶段初始化 Admin、EventBus、Host Gateway、Host Agent，保留全量拓扑，并在本机安装操作员签名身份。必须指定 --stage core；结果 core-ready 仅表示核心阶段完成，其余服务、Console Proxy、云资源与正式验收仍由后续部署阶段完成。纯 Go 和前端默认在本机构建；只有 Storage CGO 构建使用 compile_host。重试必须保留原 state-dir，其中保存私有凭据和原始制品。",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if stage != "core" {
				return errors.New("bootstrap currently requires --stage core")
			}
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if err := snapshot.VerifyUnchanged(); err != nil {
				return errors.New("config_changed")
			}
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
			options.Log = command.ErrOrStderr()
			result, err := deps.bootstrapCore(command.Context(), snapshot, options)
			if err != nil {
				return err
			}
			return writeSetupJSON(command, result)
		},
	}
	command.Flags().StringVar(&file, "file", defaultSetupFile, "原仓库中的私有 moox.toml")
	command.Flags().StringVar(&stage, "stage", "", "当前可执行阶段：core")
	command.Flags().StringVar(&source, "source-dir", "", "源码与公共配置模板所在 worktree（默认当前目录）")
	command.Flags().StringVar(&options.BinaryDirectory, "binary-dir", "", "可选：本机预先构建的目标 Linux host/control 二进制目录")
	command.Flags().StringVar(&options.StateDirectory, "state-dir", "", "持久私有部署状态目录；重试必须使用原目录")
	command.Flags().StringVar(&options.OperatorDirectory, "operator-dir", "", "本机操作员身份目录（默认 ~/.config/moox）")
	return command
}
