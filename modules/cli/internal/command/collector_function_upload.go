package command

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/collectorpackager"
	"github.com/mooyang-code/moox/modules/cli/internal/rpcclient"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/spf13/cobra"
)

type collectorFunctionUploadOptions struct {
	collectorPackageOptions
	File        string
	ZipPath     string
	PackageName string
}

// collectorFunctionRuntime 是采集函数的运行时：Collector 建函数固定用 Go1（见 modules/collector/internal/cloud）。
const collectorFunctionRuntime = "Go1"

var collectorFunctionUploadFlags collectorFunctionUploadOptions

var collectorFunctionUploadCmd = &cobra.Command{
	Use:   "upload",
	Short: "构建（或使用现成的）SCF 代码包并只上传，不创建函数",
	Long: "采集计划函数由 Collector 的对账协程自动创建，只需要一个可用的代码包。代码包全局共享（各空间使用同一个 Linux 二进制）。本命令构建或读取代码包 zip、校验后经 Collector 上传到 COS。" +
		"--package-name 需与 moox.toml 里 scf_plan 的 package_name 一致（默认 moox-collector-plan）：Collector 总是使用该名称的最新可用版本，" +
		"上传后落后的函数会自动升级，不需要改配置或重启。每个包名只保留最新 3 个版本。",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runCollectorFunctionUpload(cmd.Context(), cmd.OutOrStdout(), collectorFunctionUploadFlags)
	},
}

func init() {
	collectorFunctionCmd.AddCommand(collectorFunctionUploadCmd)
	addCollectorPackageFlags(collectorFunctionUploadCmd, &collectorFunctionUploadFlags.collectorPackageOptions)
	flags := collectorFunctionUploadCmd.Flags()
	flags.StringVar(&collectorFunctionUploadFlags.File, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
	flags.StringVar(&collectorFunctionUploadFlags.ZipPath, "zip", "", "已构建的 SCF 代码包 zip；留空则现场构建")
	flags.StringVar(&collectorFunctionUploadFlags.PackageName, "package-name", "", "代码包名称；默认 moox-collector-plan，与 scf_plan 的默认值一致")
}

func runCollectorFunctionUpload(ctx context.Context, out io.Writer, opts collectorFunctionUploadOptions) error {
	zipPath := opts.ZipPath
	if zipPath == "" {
		result, err := packageCollectorFunction(ctx, opts.collectorPackageOptions)
		if err != nil {
			return err
		}
		zipPath = result.Path
	}
	if err := collectorpackager.ValidateSCFPackageZip(zipPath); err != nil {
		return err
	}
	data, err := os.ReadFile(zipPath)
	if err != nil {
		return err
	}
	gateway, err := openCommandGateway(ctx, opts.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	client := &rpcclient.Client{Gateway: gateway}
	packageName := defaultFlag(strings.TrimSpace(opts.PackageName), topology.DefaultSCFPackageName)
	pkg, err := client.UploadPackage(ctx, &collectorpb.InitPackageUploadReq{
		PackageName: packageName, Version: defaultFlag(opts.Version, "dev"), Runtime: collectorFunctionRuntime,
		Filename: filepath.Base(zipPath),
	}, data)
	if err != nil {
		return err
	}
	return printJSON(out, map[string]any{"status": pkg.GetStatus(), "package_id": pkg.GetPackageId(), "package_name": packageName, "zip": zipPath, "bytes": len(data),
		"message": "Collector 按包名自动使用最新可用版本，落后的函数会自动升级，无需修改配置"})
}
