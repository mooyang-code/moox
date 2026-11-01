package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/bundle"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	setupvalidate "github.com/mooyang-code/moox/modules/cli/internal/setup/validate"
	"github.com/spf13/cobra"
)

type setupAdminBootstrap func(context.Context, *setupconfig.Snapshot, unitdeploy.DeployOptions) (unitdeploy.BootstrapResult, error)
type setupControlBootstrap func(context.Context, *setupconfig.Snapshot, unitdeploy.ControlOptions) (unitdeploy.BootstrapResult, error)

func newSetupBootstrapCommand(deps setupDeps) *cobra.Command {
	var file, source, stage string
	var options unitdeploy.DeployOptions
	var control unitdeploy.ControlOptions
	if deps.bootstrapAdmin == nil {
		deps.bootstrapAdmin = unitdeploy.BootstrapAdmin
	}
	if deps.bootstrapControl == nil {
		deps.bootstrapControl = unitdeploy.BootstrapControl
	}
	command := &cobra.Command{
		Use:   "bootstrap",
		Short: "通过已核验 SSH 初始化核心或完整控制服务",
		Long:  "--stage admin 初始化 Admin、EventBus、Host Gateway、Host Agent，保留全量拓扑并安装本机操作员身份；--stage control 在 Storage/出口依赖实际就绪后，用原始软件和凭据部署完整 control 部署及 Console Proxy。首次安装自动创建管理台内部 CA。纯 Go 和前端默认在本机构建；只有三个 Storage CGO 服务使用 compile_host。重试必须保留原 state-dir、原配置和原授权；完整舰队、云资源及正式验收继续由外层流程完成。",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if stage != "admin" && stage != "control" {
				return errors.New("bootstrap requires --stage admin or --stage control")
			}
			if stage == "admin" && control.Release != "" {
				return errors.New("--release upgrades the control stage only")
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
			var result unitdeploy.BootstrapResult
			if err := setupvalidate.CheckDeployments(snapshot.Manifest); err != nil {
				return err
			}
			// 升级只换程序，不占首次安装那么多空间，这里只拦首次安装。
			if control.Release == "" {
				if err := checkInstallDisk(command.Context(), deps, snapshot, snapshot.Manifest.ControlHost().Name); err != nil {
					return err
				}
			}
			if stage == "admin" {
				if err := checkAdminRoute(command.Context(), deps, snapshot, options); err != nil {
					return err
				}
				result, err = deps.bootstrapAdmin(command.Context(), snapshot, options)
			} else {
				control.DeployOptions = options
				result, err = deploySetupControl(command.Context(), deps, snapshot, control)
			}
			if err != nil {
				return err
			}
			return writeSetupJSON(command, result)
		},
	}
	command.Flags().StringVar(&file, "file", defaultSetupFile, "原仓库中的私有 moox.toml")
	command.Flags().StringVar(&stage, "stage", "", "执行阶段：admin/control")
	command.Flags().StringVar(&source, "source-dir", "", "源码与公共配置模板所在 worktree（默认当前目录）")
	command.Flags().StringVar(&options.BinaryDirectory, "binary-dir", "", "可选：本机预先构建的目标 Linux host/control 二进制目录")
	command.Flags().StringVar(&options.StateDirectory, "state-dir", "", "持久私有部署状态目录；重试必须使用原目录")
	command.Flags().StringVar(&options.OperatorDirectory, "operator-dir", "", "本机操作员身份目录（默认 ~/.config/moox）")
	command.Flags().StringVar(&control.Release, "release", "", "仅 --stage control：用该版本升级 control 上的 host 与 control 单元（会按顺序重启 control 上的服务）")
	return command
}

// deploySetupControl 部署 control：确认元数据和配置没问题，部署后装看护再写完成记录，setup install 共用。
func deploySetupControl(ctx context.Context, deps setupDeps, snapshot *setupconfig.Snapshot, control unitdeploy.ControlOptions) (unitdeploy.BootstrapResult, error) {
	var result unitdeploy.BootstrapResult
	// 已经装好过管理台的环境重跑这一步是修复，元数据早就激活了。
	local := unitdeploy.ReadLocalState(control.StateDirectory, control.OperatorDirectory, setupBinding(snapshot))
	if control.Release == "" && !local.InitStorage && !local.ControlReady {
		return result, errors.New("init_storage_required: 管理台的服务要等系统元数据激活才能就绪，先运行 setup init --stage storage；运行 setup validate 看下一步")
	}
	// 改通知、Collector 这些配置后可以重跑；主机、账号这类改了就不是原来那次安装。
	if control.Release == "" && !local.ControlReady && local.StaleBinding {
		return result, errors.New("deployment_config_changed: 主机、路径、管理员或版本和这次安装开始时不一致，运行 setup validate 看原配置的位置。换回原配置和原版本后重跑；要用新配置就彻底清空后重新安装")
	}
	result, err := deps.bootstrapControl(ctx, snapshot, control)
	if err != nil {
		return result, err
	}
	return result, finishSetupControl(ctx, deps, snapshot, control.DeployOptions, result)
}

// finishSetupControl 给控制机装好看护再写完成记录：控制机不走 deploy-host，这里不装就没人拉起崩溃的组件。
// 看护安装失败时不写记录，重跑这一步能补上。
func finishSetupControl(ctx context.Context, deps setupDeps, snapshot *setupconfig.Snapshot, options unitdeploy.DeployOptions, result unitdeploy.BootstrapResult) error {
	watchdog, err := deps.installWatchdog(ctx, snapshot, snapshot.Manifest.ControlHost(), false)
	if err != nil {
		return fmt.Errorf("install watchdog on %s: %w", snapshot.Manifest.ControlHost().Name, err)
	}
	fmt.Fprintf(options.Log, "watchdog installed on %s: %s\n", snapshot.Manifest.ControlHost().Name, watchdog)
	return unitdeploy.MarkControlReady(options.StateDirectory, result)
}

func resolveSetupUnitOptions(snapshot *setupconfig.Snapshot, source string, options *unitdeploy.DeployOptions) error {
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
	if err := loadSetupBundle(options); err != nil {
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

// loadSetupBundle 在源目录是解压后的产物包时读取清单：软件由目标机按清单下载，部署不再在本机构建。
func loadSetupBundle(options *unitdeploy.DeployOptions) error {
	manifest, ok, err := bundle.Load(options.RepositoryRoot)
	if err != nil || !ok {
		return err
	}
	if err := manifest.CheckCLI(Version, unitruntime.CatalogSHA256()); err != nil {
		return err
	}
	options.Bundle = &manifest
	return nil
}
