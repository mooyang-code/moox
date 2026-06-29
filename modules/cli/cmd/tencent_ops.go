package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/tencentcloud"
	"github.com/spf13/cobra"
)

type lighthouseFirewallAddOptions struct {
	SecretID        string
	SecretKey       string
	Region          string
	Endpoint        string
	InstanceID      string
	PublicIP        string
	Ports           string
	Protocol        string
	Cidr            string
	IPv6Cidr        string
	Action          string
	Description     string
	FirewallVersion int64
	DryRun          bool
}

var lighthouseFirewallAddFlags lighthouseFirewallAddOptions

var opsCmd = &cobra.Command{
	Use:   "ops",
	Short: "云资源运维工具",
}

var tencentOpsCmd = &cobra.Command{
	Use:   "tencent",
	Short: "腾讯云运维工具",
}

var lighthouseOpsCmd = &cobra.Command{
	Use:   "lighthouse",
	Short: "腾讯云轻量应用服务器运维工具",
}

var lighthouseFirewallCmd = &cobra.Command{
	Use:   "firewall",
	Short: "轻量应用服务器防火墙工具",
}

var lighthouseFirewallAddCmd = &cobra.Command{
	Use:   "add",
	Short: "添加轻量应用服务器防火墙规则",
	Long: `添加腾讯云轻量应用服务器防火墙规则。

示例：
  moox-cli ops tencent lighthouse firewall add --public-ip <lighthouse-public-ip> --ports 20201,20200,11000
  moox-cli ops tencent lighthouse firewall add --instance-id lhins-xxxx --ports 20201 --cidr 0.0.0.0/0 --description moox-storage
  moox-cli ops tencent lighthouse firewall add --instance-id lhins-xxxx --ports 20201,20200 --dry-run

提示：public-ip 可从云厂商控制台获取；MooX 服务部署地址以管理台“服务部署信息”页为准。`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLighthouseFirewallAdd(cmd, lighthouseFirewallAddFlags)
	},
}

func init() {
	rootCmd.AddCommand(opsCmd)
	opsCmd.AddCommand(tencentOpsCmd)
	tencentOpsCmd.AddCommand(lighthouseOpsCmd)
	lighthouseOpsCmd.AddCommand(lighthouseFirewallCmd)
	lighthouseFirewallCmd.AddCommand(lighthouseFirewallAddCmd)

	lighthouseFirewallAddCmd.Flags().StringVar(&lighthouseFirewallAddFlags.SecretID, "secret-id", "", "腾讯云 SecretId；默认读取 TENCENTCLOUD_SECRET_ID 或 TENCENT_SECRET_ID")
	lighthouseFirewallAddCmd.Flags().StringVar(&lighthouseFirewallAddFlags.SecretKey, "secret-key", "", "腾讯云 SecretKey；默认读取 TENCENTCLOUD_SECRET_KEY 或 TENCENT_SECRET_KEY")
	lighthouseFirewallAddCmd.Flags().StringVar(&lighthouseFirewallAddFlags.Region, "region", "ap-guangzhou", "腾讯云地域")
	lighthouseFirewallAddCmd.Flags().StringVar(&lighthouseFirewallAddFlags.Endpoint, "endpoint", "https://lighthouse.tencentcloudapi.com", "腾讯云 Lighthouse API endpoint")
	lighthouseFirewallAddCmd.Flags().StringVar(&lighthouseFirewallAddFlags.InstanceID, "instance-id", "", "轻量应用服务器实例 ID；与 --public-ip 二选一")
	lighthouseFirewallAddCmd.Flags().StringVar(&lighthouseFirewallAddFlags.PublicIP, "public-ip", "", "公网 IP；未传 --instance-id 时用 DescribeInstances 自动解析")
	lighthouseFirewallAddCmd.Flags().StringVar(&lighthouseFirewallAddFlags.Ports, "ports", "", "端口：ALL、单端口、逗号分隔端口或范围，如 20201,20200,11000")
	lighthouseFirewallAddCmd.Flags().StringVar(&lighthouseFirewallAddFlags.Protocol, "protocol", "TCP", "协议：TCP、UDP、ICMP、ICMPv6、ALL")
	lighthouseFirewallAddCmd.Flags().StringVar(&lighthouseFirewallAddFlags.Cidr, "cidr", "0.0.0.0/0", "IPv4 CIDR 或 IP；与 --ipv6-cidr 互斥")
	lighthouseFirewallAddCmd.Flags().StringVar(&lighthouseFirewallAddFlags.IPv6Cidr, "ipv6-cidr", "", "IPv6 CIDR 或 IP；与 --cidr 互斥")
	lighthouseFirewallAddCmd.Flags().StringVar(&lighthouseFirewallAddFlags.Action, "action", "ACCEPT", "动作：ACCEPT 或 DROP")
	lighthouseFirewallAddCmd.Flags().StringVar(&lighthouseFirewallAddFlags.Description, "description", "moox firewall rule", "防火墙规则描述，最长 64 字符")
	lighthouseFirewallAddCmd.Flags().Int64Var(&lighthouseFirewallAddFlags.FirewallVersion, "firewall-version", 0, "防火墙版本号；0 表示不传")
	lighthouseFirewallAddCmd.Flags().BoolVar(&lighthouseFirewallAddFlags.DryRun, "dry-run", false, "仅输出将要提交的规则，不调用腾讯云 API")
}

func runLighthouseFirewallAdd(cmd *cobra.Command, opts lighthouseFirewallAddOptions) error {
	opts.SecretID = firstNonEmpty(opts.SecretID, os.Getenv("TENCENTCLOUD_SECRET_ID"), os.Getenv("TENCENT_SECRET_ID"))
	opts.SecretKey = firstNonEmpty(opts.SecretKey, os.Getenv("TENCENTCLOUD_SECRET_KEY"), os.Getenv("TENCENT_SECRET_KEY"))
	opts.Region = firstNonEmpty(opts.Region, os.Getenv("TENCENTCLOUD_REGION"), "ap-guangzhou")

	if strings.TrimSpace(opts.InstanceID) == "" && strings.TrimSpace(opts.PublicIP) == "" {
		return fmt.Errorf("--instance-id or --public-ip is required")
	}
	if opts.DryRun {
		preview, err := buildFirewallAddPreview(opts)
		if err != nil {
			return err
		}
		return writeJSON(cmd, map[string]any{
			"dry_run":   true,
			"action":    "CreateFirewallRules",
			"region":    opts.Region,
			"endpoint":  opts.Endpoint,
			"public_ip": strings.TrimSpace(opts.PublicIP),
			"request":   preview,
		})
	}
	client, err := tencentcloud.NewClient(tencentcloud.ClientOptions{
		SecretID:  opts.SecretID,
		SecretKey: opts.SecretKey,
		Region:    opts.Region,
		Endpoint:  opts.Endpoint,
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	defer cancel()

	instanceID := strings.TrimSpace(opts.InstanceID)
	if instanceID == "" {
		instanceID, err = client.ResolveInstanceIDByPublicIP(ctx, opts.PublicIP)
		if err != nil {
			return err
		}
		opts.InstanceID = instanceID
	}
	req, err := tencentcloud.NewCreateFirewallRulesRequest(tencentcloud.CreateFirewallRulesOptions{
		InstanceID:      instanceID,
		Protocol:        opts.Protocol,
		Ports:           opts.Ports,
		CidrBlock:       opts.Cidr,
		IPv6CidrBlock:   opts.IPv6Cidr,
		Action:          opts.Action,
		Description:     opts.Description,
		FirewallVersion: opts.FirewallVersion,
	})
	if err != nil {
		return err
	}
	requestID, err := client.CreateFirewallRules(ctx, req)
	if err != nil {
		return err
	}
	return writeJSON(cmd, map[string]any{
		"status":      "created",
		"request_id":  requestID,
		"region":      opts.Region,
		"instance_id": instanceID,
		"public_ip":   strings.TrimSpace(opts.PublicIP),
		"rules":       req.FirewallRules,
	})
}

func buildFirewallAddPreview(opts lighthouseFirewallAddOptions) (map[string]any, error) {
	instanceID := strings.TrimSpace(opts.InstanceID)
	if instanceID == "" && strings.TrimSpace(opts.PublicIP) != "" {
		instanceID = "<resolve-from-public-ip>"
	}
	req, err := tencentcloud.NewCreateFirewallRulesRequest(tencentcloud.CreateFirewallRulesOptions{
		InstanceID:      instanceID,
		Protocol:        opts.Protocol,
		Ports:           opts.Ports,
		CidrBlock:       opts.Cidr,
		IPv6CidrBlock:   opts.IPv6Cidr,
		Action:          opts.Action,
		Description:     opts.Description,
		FirewallVersion: opts.FirewallVersion,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"instance_id":               req.InstanceID,
		"resolve_instance_by_ip":    strings.TrimSpace(opts.InstanceID) == "" && strings.TrimSpace(opts.PublicIP) != "",
		"create_firewall_rules_req": req,
	}, nil
}

func writeJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
