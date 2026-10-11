package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/collectorpackager"
	"github.com/spf13/cobra"
)

type collectorFunctionUploadOptions struct {
	collectorPackageOptions
	File           string
	SpaceID        string
	ZipPath        string
	PackageName    string
	CloudAccountID string
	Runtime        string
}

var collectorFunctionUploadFlags collectorFunctionUploadOptions

var collectorFunctionUploadCmd = &cobra.Command{
	Use:   "upload",
	Short: "构建（或使用现成的）SCF 代码包并只上传，不创建函数",
	Long: "采集计划函数由 Collector 的对账协程自动创建，只需要一个可用的代码包。本命令构建或读取代码包 zip、校验后上传到 CloudNode，" +
		"输出 package_id；把它填进 Collector 配置 scf_plan.package_id。",
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
	flags.StringVar(&collectorFunctionUploadFlags.SpaceID, "space-id", "", "空间 ID")
	flags.StringVar(&collectorFunctionUploadFlags.ZipPath, "zip", "", "已构建的 SCF 代码包 zip；留空则现场构建")
	flags.StringVar(&collectorFunctionUploadFlags.PackageName, "package-name", "moox-collector", "代码包名称")
	flags.StringVar(&collectorFunctionUploadFlags.CloudAccountID, "cloud-account-id", "", "云账号 ID")
	flags.StringVar(&collectorFunctionUploadFlags.Runtime, "runtime", "Go1", "SCF 运行时")
}

func runCollectorFunctionUpload(ctx context.Context, out io.Writer, opts collectorFunctionUploadOptions) error {
	if opts.SpaceID == "" || opts.CloudAccountID == "" {
		return errors.New("--space-id and --cloud-account-id are required")
	}
	zipPath := opts.ZipPath
	if zipPath == "" {
		opts.collectorPackageOptions.SpaceID = opts.SpaceID
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
	client := &adminclient.Client{Gateway: gateway, SpaceID: opts.SpaceID}
	response, err := client.UploadPackage(ctx, adminclient.UploadPackageRequest{
		PackageName: opts.PackageName, Version: defaultFlag(opts.Version, "dev"), Runtime: opts.Runtime,
		PackageType: adminclient.ResolvePackageType("data_collector"), BizType: "market_fetcher",
		CloudAccountID: opts.CloudAccountID, OriginalFilename: filepath.Base(zipPath),
	}, data)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(map[string]any{"status": "uploaded", "package_id": response.PackageID, "zip": zipPath, "bytes": len(data), "message": fmt.Sprintf("请把 scf_plan.package_id 设为 %s", response.PackageID)})
}
