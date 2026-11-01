package command

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/bundle"
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
	build := &cobra.Command{Use: "build", Short: "在本机构建并打包一个版本的全部纯 Go 单元", Long: "要求工作区干净；host、control、access、egress-proxy、trade 在本机关闭 CGO 构建，Storage 需用 --storage-binary-dir 提供已构建的 CGO 二进制（setup build-linux 在本机 Docker 里构建）（含 storage CLI）。同一版本 ID 已存在且来源提交一致时直接复用。", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		if options.ReleaseID == "" {
			return errors.New("release build requires --release")
		}
		var admin unitdeploy.DeployOptions
		admin.StateDirectory = options.StateDirectory
		if admin.StateDirectory == "" {
			// 未指定状态目录时与部署命令一样按 moox.toml 推导默认位置。
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if err := resolveSetupUnitOptions(snapshot, source, &admin); err != nil {
				return err
			}
		}
		var err error
		if options.StateDirectory, err = filepath.Abs(admin.StateDirectory); err != nil {
			return err
		}
		if source == "" {
			source = "."
		}
		if options.RepositoryRoot, err = filepath.Abs(source); err != nil {
			return err
		}
		if _, ok, err := bundle.Load(options.RepositoryRoot); err != nil {
			return err
		} else if ok && options.BinaryDirectory == "" {
			return errors.New("产物包里没有服务器程序，用 setup release fetch 登记版本；要用自己的程序就指定 --binary-dir")
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
	release.AddCommand(build, newSetupReleaseFetchCommand(deps))
	return release
}

// newSetupReleaseFetchCommand 用当前产物包登记一个版本。发布 ID 和软件版本分开：
// 同一份软件可以用新 ID 再发一次，让保留期或出口配置生效。
func newSetupReleaseFetchCommand(deps setupDeps) *cobra.Command {
	var file, source, id string
	var options unitdeploy.DeployOptions
	fetch := &cobra.Command{Use: "fetch", Short: "用当前产物包登记一个版本，目标机部署时下载", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		if id == "" {
			return errors.New("release fetch requires --release")
		}
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		if err := resolveSetupUnitOptions(snapshot, source, &options); err != nil {
			return err
		}
		if options.Bundle == nil {
			return errors.New("setup release fetch 要在产物包目录里运行；源码目录用 setup release build")
		}
		result, err := unitdeploy.FetchRelease(options.StateDirectory, id, options.Bundle)
		if err != nil {
			return err
		}
		return writeSetupJSON(command, map[string]any{"status": "ready", "release": result.ID, "source_commit": result.SourceCommit, "software": options.Bundle.Release})
	}}
	fetch.Flags().StringVar(&file, "file", defaultSetupFile, "原仓库中的私有 moox.toml（只用于推导默认状态目录）")
	fetch.Flags().StringVar(&id, "release", "", "版本 ID（小写字母、数字、点与短横线，最长 40）")
	fetch.Flags().StringVar(&source, "source-dir", "", "产物包目录（默认当前目录）")
	fetch.Flags().StringVar(&options.StateDirectory, "state-dir", "", "持久私有部署状态目录（与 bootstrap 相同）")
	return fetch
}
