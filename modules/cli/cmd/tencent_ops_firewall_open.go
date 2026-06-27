package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// lighthouseFirewallOpenOptions 通过控制面云账户凭证开放防火墙端口的选项。
type lighthouseFirewallOpenOptions struct {
	ControlURL       string
	ServiceAccessKey string
	ServiceSecretKey string
	CloudAccountID   string
	Provider         string
	PublicIP         string
	Ports            string
	Region           string
	Protocol         string
	Cidr             string
	Description      string
	DryRun           bool
}

var lighthouseFirewallOpenFlags lighthouseFirewallOpenOptions

var lighthouseFirewallOpenCmd = &cobra.Command{
	Use:   "open",
	Short: "通过控制面云账户凭证开放轻量防火墙端口",
	Long: `通过控制面后台 API 获取云账户明文凭证（reveal），自动调用 firewall add 开放端口。
替代独立工具 admin/cmd/open-lighthouse-firewall：凭证不再从本地 SQLite DB 解密，
而是走 /api/service/cloudnode/* 的 HMAC 签名鉴权从控制面获取。

示例：
  moox-cli ops tencent lighthouse firewall open \
    --control-url http://106.53.107.122:11000 \
    --service-access-key moox-service --service-secret-key moox-service-secret-change-me \
    --public-ip 106.53.107.122 --ports 11000,10080,20200,20201,20202

  moox-cli ops tencent lighthouse firewall open \
    --control-url http://106.53.107.122:11000 \
    --service-access-key moox-service --service-secret-key <secret> \
    --cloud-account-id account_xxx --public-ip 106.53.107.122 --ports 9527`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLighthouseFirewallOpen(cmd, lighthouseFirewallOpenFlags)
	},
}

func init() {
	lighthouseFirewallCmd.AddCommand(lighthouseFirewallOpenCmd)
	f := lighthouseFirewallOpenCmd.Flags()
	f.StringVar(&lighthouseFirewallOpenFlags.ControlURL, "control-url", "", "控制面地址，形如 http://ip:port（必填）")
	f.StringVar(&lighthouseFirewallOpenFlags.ServiceAccessKey, "service-access-key", "", "后台服务签名 access_key（与控制面 gateway.yaml 一致）")
	f.StringVar(&lighthouseFirewallOpenFlags.ServiceSecretKey, "service-secret-key", "", "后台服务签名 secret_key（与控制面 gateway.yaml 一致）")
	f.StringVar(&lighthouseFirewallOpenFlags.CloudAccountID, "cloud-account-id", "", "云账户 ID；未指定时取控制面第一个有效账户")
	f.StringVar(&lighthouseFirewallOpenFlags.Provider, "provider", "", "按云厂商筛选账户（仅在未指定 --cloud-account-id 时生效）")
	f.StringVar(&lighthouseFirewallOpenFlags.PublicIP, "public-ip", "", "公网 IP（必填）")
	f.StringVar(&lighthouseFirewallOpenFlags.Ports, "ports", "", "端口：ALL、单端口、逗号分隔端口或范围（必填）")
	f.StringVar(&lighthouseFirewallOpenFlags.Region, "region", "ap-guangzhou", "腾讯云地域")
	f.StringVar(&lighthouseFirewallOpenFlags.Protocol, "protocol", "TCP", "协议：TCP、UDP、ICMP、ICMPv6、ALL")
	f.StringVar(&lighthouseFirewallOpenFlags.Cidr, "cidr", "0.0.0.0/0", "IPv4 CIDR 或 IP")
	f.StringVar(&lighthouseFirewallOpenFlags.Description, "description", "moox services", "防火墙规则描述，最长 64 字符")
	f.BoolVar(&lighthouseFirewallOpenFlags.DryRun, "dry-run", false, "仅打印将使用的账户与规则，不调用腾讯云 API")
}

func runLighthouseFirewallOpen(cmd *cobra.Command, opts lighthouseFirewallOpenOptions) error {
	if strings.TrimSpace(opts.ControlURL) == "" {
		return fmt.Errorf("--control-url is required")
	}
	if strings.TrimSpace(opts.PublicIP) == "" {
		return fmt.Errorf("--public-ip is required")
	}
	if strings.TrimSpace(opts.Ports) == "" {
		return fmt.Errorf("--ports is required")
	}
	if opts.ServiceAccessKey == "" || opts.ServiceSecretKey == "" {
		return fmt.Errorf("--service-access-key and --service-secret-key are required")
	}

	client := newCollectorAdminClient(opts.ControlURL, "", opts.ServiceAccessKey, opts.ServiceSecretKey)
	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	defer cancel()

	accountID := strings.TrimSpace(opts.CloudAccountID)
	if accountID == "" {
		accounts, err := client.ListCloudAccounts(ctx, opts.Provider)
		if err != nil {
			return fmt.Errorf("list cloud accounts: %w", err)
		}
		for _, a := range accounts {
			if a.Invalid == 0 && a.AccountID != "" {
				accountID = a.AccountID
				break
			}
		}
		if accountID == "" {
			return fmt.Errorf("no valid cloud account found in control plane; pass --cloud-account-id explicitly")
		}
	}

	info, err := client.GetCOSAccountInfo(ctx, accountID)
	if err != nil {
		return fmt.Errorf("get cloud account credentials: %w", err)
	}
	if info.SecretID == "" || info.SecretKey == "" {
		return fmt.Errorf("cloud account %s returned empty credentials", accountID)
	}

	fmt.Fprintf(os.Stderr, "使用云账户 %s 的凭证开放防火墙: public_ip=%s ports=%s\n", accountID, opts.PublicIP, opts.Ports)

	addOpts := lighthouseFirewallAddOptions{
		SecretID:    info.SecretID,
		SecretKey:   info.SecretKey,
		Region:      opts.Region,
		Endpoint:    "https://lighthouse.tencentcloudapi.com",
		PublicIP:    opts.PublicIP,
		Ports:       opts.Ports,
		Protocol:    opts.Protocol,
		Cidr:        opts.Cidr,
		Description: opts.Description,
		DryRun:      opts.DryRun,
	}
	return runLighthouseFirewallAdd(cmd, addOpts)
}
