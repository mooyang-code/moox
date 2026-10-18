package command

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	"github.com/spf13/cobra"
)

type setupReleaseBuild func(context.Context, unitdeploy.ReleaseOptions) (unitdeploy.Release, error)

// newSetupReleaseCommand 管理版本制品：setup release build 在本机构建并打包全部纯 Go 单元，保存在私有部署状态目录的 releases/<ID>/，
// 之后 deploy-host / deploy-unit / bootstrap --stage control 用 --release 选择它升级已安装单元或部署新主机。
func newSetupReleaseCommand(deps setupDeps) *cobra.Command {
	release := &cobra.Command{Use: "release", Short: "构建与管理版本制品（用于升级已安装单元）"}
	var file, source string
	var options unitdeploy.ReleaseOptions
	if deps.buildRelease == nil {
		deps.buildRelease = unitdeploy.BuildRelease
	}
	build := &cobra.Command{Use: "build", Short: "在本机构建并打包一个版本的全部纯 Go 单元", Long: "要求工作区干净；host、control、access、egress-proxy、trade 在本机关闭 CGO 构建，Storage 需用 --storage-binary-dir 提供编译机构建的 CGO 二进制（含 storage CLI）。同一版本 ID 已存在且来源提交一致时直接复用。", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		if options.ReleaseID == "" {
			return errors.New("release build requires --release")
		}
		var core unitdeploy.CoreOptions
		core.StateDirectory = options.StateDirectory
		if core.StateDirectory == "" {
			// 未指定状态目录时与部署命令一样按 moox.toml 推导默认位置。
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if err := resolveSetupUnitOptions(snapshot, source, &core); err != nil {
				return err
			}
		}
		var err error
		if options.StateDirectory, err = filepath.Abs(core.StateDirectory); err != nil {
			return err
		}
		if source == "" {
			source = "."
		}
		if options.RepositoryRoot, err = filepath.Abs(source); err != nil {
			return err
		}
		for _, directory := range []*string{&options.BinaryDirectory, &options.StorageBinaryDirectory} {
			if *directory != "" {
				if *directory, err = filepath.Abs(*directory); err != nil {
					return err
				}
			}
		}
		options.Log = command.ErrOrStderr()
		result, err := deps.buildRelease(command.Context(), options)
		if err != nil {
			return err
		}
		packages := map[string]string{}
		for profile, item := range result.Packages {
			packages[profile] = item.SHA256
		}
		return writeSetupJSON(command, map[string]any{"status": "ready", "release": result.ID, "source_commit": result.SourceCommit, "architecture": result.Architecture, "packages": packages})
	}}
	build.Flags().StringVar(&file, "file", defaultSetupFile, "原仓库中的私有 moox.toml（只用于推导默认状态目录）")
	build.Flags().StringVar(&options.ReleaseID, "release", "", "版本 ID（小写字母、数字、点与短横线，最长 40）")
	build.Flags().StringVar(&options.Architecture, "arch", "amd64", "目标 Linux 架构：amd64/arm64")
	build.Flags().StringVar(&source, "source-dir", "", "源码 worktree（默认当前目录）")
	build.Flags().StringVar(&options.BinaryDirectory, "binary-dir", "", "可选：已为目标平台构建的纯 Go 二进制目录")
	build.Flags().StringVar(&options.StorageBinaryDirectory, "storage-binary-dir", "", "可选：Storage CGO 二进制与 storage CLI 目录")
	build.Flags().StringVar(&options.StateDirectory, "state-dir", "", "持久私有部署状态目录（与 bootstrap 相同）")
	release.AddCommand(build)
	return release
}
