package command

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/clsprepare"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/spf13/cobra"
)

type clsBootstrapOptions struct {
	SecretID      string
	SecretKey     string
	Region        string
	Endpoint      string
	LogsetName    string
	TopicName     string
	RetentionDays int64
	Partitions    int64
	DryRun        bool
}

var clsBootstrapFlags clsBootstrapOptions

var clsOpsCmd = &cobra.Command{
	Use:   "cls",
	Short: "腾讯云日志服务运维工具",
}

var clsBootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "幂等开通 CLS 并创建 MooX 日志集、主题和索引",
	Long: `幂等初始化腾讯云日志服务。

执行顺序为 GetClsService、OpenClsService（如未开通）、Describe/CreateLogset、
Describe/CreateTopic、CreateIndex。未显式传密钥时，使用腾讯云 SDK 默认凭证链：
环境变量、凭证文件、CVM 实例角色。命令输出不包含密钥。`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCLSBootstrap(cmd, clsBootstrapFlags)
	},
}

func newCLSResolveCommand() *cobra.Command {
	opts := clsBootstrapOptions{
		Region:     "ap-guangzhou",
		Endpoint:   "cls.tencentcloudapi.com",
		LogsetName: "moox",
		TopicName:  "moox-application",
	}
	cmd := &cobra.Command{
		Use:   "resolve",
		Short: "查询既有 MooX CLS 资源，不创建或修改资源",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCLSResolve(cmd, opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.SecretID, "secret-id", "", "腾讯云 SecretId；默认使用 SDK 凭证链")
	f.StringVar(&opts.SecretKey, "secret-key", "", "腾讯云 SecretKey；默认使用 SDK 凭证链")
	f.StringVar(&opts.Region, "region", opts.Region, "CLS 地域")
	f.StringVar(&opts.Endpoint, "endpoint", opts.Endpoint, "CLS API endpoint")
	f.StringVar(&opts.LogsetName, "logset-name", opts.LogsetName, "日志集名称")
	f.StringVar(&opts.TopicName, "topic-name", opts.TopicName, "日志主题名称")
	return cmd
}

func init() {
	tencentOpsCmd.AddCommand(clsOpsCmd)
	clsOpsCmd.AddCommand(clsBootstrapCmd)
	clsOpsCmd.AddCommand(newCLSResolveCommand())
	clsOpsCmd.AddCommand(newCLSPrepareCommand())
	f := clsBootstrapCmd.Flags()
	f.StringVar(&clsBootstrapFlags.SecretID, "secret-id", "", "腾讯云 SecretId；默认使用 SDK 凭证链")
	f.StringVar(&clsBootstrapFlags.SecretKey, "secret-key", "", "腾讯云 SecretKey；默认使用 SDK 凭证链")
	f.StringVar(&clsBootstrapFlags.Region, "region", "ap-guangzhou", "CLS 地域")
	f.StringVar(&clsBootstrapFlags.Endpoint, "endpoint", "cls.tencentcloudapi.com", "CLS API endpoint")
	f.StringVar(&clsBootstrapFlags.LogsetName, "logset-name", "moox", "日志集名称")
	f.StringVar(&clsBootstrapFlags.TopicName, "topic-name", "moox-application", "日志主题名称")
	f.Int64Var(&clsBootstrapFlags.RetentionDays, "retention-days", clsprepare.RetentionDays, "日志保留天数（默认 2 天；1-3600，3640 表示永久）")
	f.Int64Var(&clsBootstrapFlags.Partitions, "partitions", clsprepare.Partitions, "初始分区数（默认 1；1-10）")
	f.BoolVar(&clsBootstrapFlags.DryRun, "dry-run", false, "仅输出初始化计划，不访问凭证或调用云 API")
}

func runCLSResolve(cmd *cobra.Command, opts clsBootstrapOptions) error {
	opts.SecretID = firstNonEmpty(opts.SecretID, os.Getenv("TENCENTCLOUD_SECRET_ID"), os.Getenv("TENCENT_SECRET_ID"))
	opts.SecretKey = firstNonEmpty(opts.SecretKey, os.Getenv("TENCENTCLOUD_SECRET_KEY"), os.Getenv("TENCENT_SECRET_KEY"))
	opts.Region = firstNonEmpty(opts.Region, os.Getenv("TENCENTCLOUD_REGION"), "ap-guangzhou")
	api, err := tencent.NewCLSSDKAPI(tencent.CLSSDKOptions{
		SecretID: opts.SecretID, SecretKey: opts.SecretKey, Region: opts.Region, Endpoint: opts.Endpoint,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	resources, err := tencent.ResolveExistingCLS(ctx, api, opts.LogsetName, opts.TopicName)
	if err != nil {
		return err
	}
	return writeJSON(cmd, map[string]any{
		"status": "resolved", "region": opts.Region, "resources": resources,
	})
}

func runCLSBootstrap(cmd *cobra.Command, opts clsBootstrapOptions) error {
	opts.SecretID = firstNonEmpty(opts.SecretID, os.Getenv("TENCENTCLOUD_SECRET_ID"), os.Getenv("TENCENT_SECRET_ID"))
	opts.SecretKey = firstNonEmpty(opts.SecretKey, os.Getenv("TENCENTCLOUD_SECRET_KEY"), os.Getenv("TENCENT_SECRET_KEY"))
	opts.Region = firstNonEmpty(opts.Region, os.Getenv("TENCENTCLOUD_REGION"), "ap-guangzhou")
	opts.LogsetName = strings.TrimSpace(opts.LogsetName)
	opts.TopicName = strings.TrimSpace(opts.TopicName)
	if opts.LogsetName == "" || opts.TopicName == "" {
		return fmt.Errorf("--logset-name and --topic-name are required")
	}
	if opts.RetentionDays < 1 || (opts.RetentionDays > 3600 && opts.RetentionDays != 3640) {
		return fmt.Errorf("--retention-days must be 1..3600 or 3640")
	}
	if opts.Partitions < 1 || opts.Partitions > 10 {
		return fmt.Errorf("--partitions must be 1..10")
	}
	credentialSource := "provider_chain"
	if opts.SecretID != "" || opts.SecretKey != "" {
		credentialSource = "explicit_or_environment"
	}
	if opts.DryRun {
		return writeJSON(cmd, map[string]any{
			"dry_run": true,
			"actions": []map[string]any{
				{"action": "GetClsService"},
				{"action": "OpenClsService", "when": "service is not open"},
				{"action": "DescribeLogsets/CreateLogset", "logset_name": opts.LogsetName},
				{"action": "DescribeTopics/CreateTopic", "topic_name": opts.TopicName, "retention_days": opts.RetentionDays, "partitions": opts.Partitions},
				{"action": "CreateIndex", "when": "topic index is disabled"},
			},
			"region": opts.Region, "credential_source": credentialSource,
		})
	}

	api, err := tencent.NewCLSSDKAPI(tencent.CLSSDKOptions{
		SecretID: opts.SecretID, SecretKey: opts.SecretKey,
		Region: opts.Region, Endpoint: opts.Endpoint,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Second)
	defer cancel()
	result, err := tencent.BootstrapCLS(ctx, api, tencent.CLSBootstrapOptions{
		LogsetName: opts.LogsetName, TopicName: opts.TopicName,
		RetentionDays: opts.RetentionDays, Partitions: opts.Partitions,
	})
	if err != nil {
		return err
	}
	return writeJSON(cmd, map[string]any{
		"status": "configured", "region": opts.Region,
		"credential_source": credentialSource,
		"resources":         result,
		"writer": map[string]any{
			"topic_id":       result.TopicID,
			"host":           clsIngestHost(opts.Region),
			"secret_id_env":  "TENCENTCLOUD_SECRET_ID",
			"secret_key_env": "TENCENTCLOUD_SECRET_KEY",
		},
	})
}

func clsIngestHost(region string) string {
	return strings.TrimSpace(region) + ".cls.tencentyun.com"
}
