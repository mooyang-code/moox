package command

import (
	"fmt"
	"os"
	"path/filepath"

	setupdeploy "github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	"github.com/spf13/cobra"
)

func newSetupPackageCommand() *cobra.Command {
	var options setupdeploy.UnitPackageOptions
	command := &cobra.Command{
		Use:   "package",
		Short: "按组件边界打包已编译的部署制品",
		Long:  "打包 host、control、storage、access、egress-proxy 或 trade。仅收集已编译的 Linux 制品与版本库中的配置模板，不读取 moox.toml 或运行密钥；配置渲染和身份材料由部署流程注入。",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			root := options.RepositoryRoot
			if root == "" {
				var err error
				root, err = os.Getwd()
				if err != nil {
					return err
				}
			}
			options.RepositoryRoot = root
			if options.BinaryDirectory == "" {
				options.BinaryDirectory = filepath.Join(root, "bin")
			}
			result, err := setupdeploy.PackageUnit(command.Context(), options)
			if err != nil {
				return fmt.Errorf("package deployment unit: %w", err)
			}
			return writeSetupJSON(command, result)
		},
	}
	command.Flags().StringVar(&options.RepositoryRoot, "repo-root", "", "配置模板所在的 Git worktree（默认当前目录）")
	command.Flags().StringVar(&options.Profile, "profile", "", "部署包类型：host/control/storage/access/egress-proxy/trade")
	command.Flags().StringVar(&options.BinaryDirectory, "binary-dir", "", "已编译制品目录（默认 ./bin）")
	command.Flags().StringVar(&options.Output, "output", "", "输出 .tar.gz 文件")
	command.Flags().StringVar(&options.GOOS, "goos", "linux", "目标操作系统")
	command.Flags().StringVar(&options.GOARCH, "goarch", "amd64", "目标架构：amd64/arm64")
	_ = command.MarkFlagRequired("profile")
	_ = command.MarkFlagRequired("output")
	command.AddCommand(&cobra.Command{
		Use:   "inspect ARCHIVE",
		Short: "校验部署包的组件边界、平台、文件与摘要",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			result, err := setupdeploy.InspectUnitPackage(command.Context(), args[0])
			if err != nil {
				return fmt.Errorf("inspect deployment unit: %w", err)
			}
			return writeSetupJSON(command, result)
		},
	})
	return command
}
