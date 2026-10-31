package command

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/spf13/cobra"
)

// lighthouseFirewallOpenOptions 通过控制面云账户凭证开放防火墙端口的选项。
type lighthouseFirewallOpenOptions struct {
	File           string
	CloudAccountID string
	Provider       string
	PublicIP       string
	Ports          string
	Region         string
	Protocol       string
	Cidr           string
	Description    string
	Yes            bool
}

var lighthouseFirewallOpenFlags lighthouseFirewallOpenOptions

var lighthouseFirewallOpenCmd = &cobra.Command{
	Use:   "open",
	Short: "通过控制面云账户凭证开放轻量防火墙端口",
	Long: `用控制面登记的云账户凭证开放轻量防火墙端口。凭证在进程内取用，不会打印。默认只预览，加 --yes 才调用腾讯云 API。
控制面经 moox.toml 里的操作员 SSH 网关访问，不需要另外指定地址或密钥。

示例：
  moox-cli ops tencent lighthouse firewall open --public-ip <lighthouse-public-ip> --ports 11000,10080,20200
  moox-cli ops tencent lighthouse firewall open --cloud-account-id account_xxx --public-ip <lighthouse-public-ip> --ports 9527 --yes

提示：lighthouse-public-ip 可从云厂商控制台获取。`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLighthouseFirewallOpen(cmd, lighthouseFirewallOpenFlags)
	},
}

func init() {
	lighthouseFirewallCmd.AddCommand(lighthouseFirewallOpenCmd)
	f := lighthouseFirewallOpenCmd.Flags()
	f.StringVar(&lighthouseFirewallOpenFlags.File, "file", "./moox.toml", "可信 SSH 主机配置")
	f.StringVar(&lighthouseFirewallOpenFlags.CloudAccountID, "cloud-account-id", "", "云账户 ID；未指定时取控制面第一个有效账户")
	f.StringVar(&lighthouseFirewallOpenFlags.Provider, "provider", "", "按云厂商筛选账户（仅在未指定 --cloud-account-id 时生效）")
	f.StringVar(&lighthouseFirewallOpenFlags.PublicIP, "public-ip", "", "公网 IP（必填）")
	f.StringVar(&lighthouseFirewallOpenFlags.Ports, "ports", "", "端口：ALL、单端口、逗号分隔端口或范围（必填）")
	f.StringVar(&lighthouseFirewallOpenFlags.Region, "region", "ap-guangzhou", "腾讯云地域")
	f.StringVar(&lighthouseFirewallOpenFlags.Protocol, "protocol", "TCP", "协议：TCP、UDP、ICMP、ICMPv6、ALL")
	f.StringVar(&lighthouseFirewallOpenFlags.Cidr, "cidr", "0.0.0.0/0", "IPv4 CIDR 或 IP")
	f.StringVar(&lighthouseFirewallOpenFlags.Description, "description", "moox services", "防火墙规则描述，最长 64 字符")
	f.BoolVar(&lighthouseFirewallOpenFlags.Yes, "yes", false, "确认开放；不加只预览，不调用腾讯云 API")
}

func runLighthouseFirewallOpen(cmd *cobra.Command, opts lighthouseFirewallOpenOptions) error {
	if strings.TrimSpace(opts.PublicIP) == "" {
		return fmt.Errorf("--public-ip is required")
	}
	if strings.TrimSpace(opts.Ports) == "" {
		return fmt.Errorf("--ports is required")
	}

	gateway, gatewayErr := openCommandGateway(cmd.Context(), opts.File, nil)
	if gatewayErr != nil {
		return gatewayErr
	}
	defer gateway.Close()
	client := &adminclient.Client{Gateway: gateway}
	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	defer cancel()

	accountID := strings.TrimSpace(opts.CloudAccountID)
	accounts, err := client.ListCloudAccounts(ctx, opts.Provider)
	if err != nil {
		return fmt.Errorf("list cloud accounts: %w", err)
	}
	var selected *adminclient.CloudAccount
	for i := range accounts {
		if accounts[i].IsDeleted {
			continue
		}
		if accountID == "" || accounts[i].AccountID == accountID {
			selected = &accounts[i]
			accountID = accounts[i].AccountID
			break
		}
	}
	if selected == nil || selected.CredentialSecretID == "" {
		return fmt.Errorf("no valid cloud account found in control plane")
	}
	info, err := client.GetSecretValue(ctx, selected.CredentialSecretID)
	if err != nil {
		return fmt.Errorf("get cloud account credentials: %w", err)
	}
	if info.KeyID == "" || info.SecretValue == "" {
		return fmt.Errorf("cloud account %s returned empty credentials", accountID)
	}

	fmt.Fprintf(os.Stderr, "使用云账户 %s 的凭证开放防火墙: public_ip=%s ports=%s\n", accountID, opts.PublicIP, opts.Ports)

	addOpts := lighthouseFirewallAddOptions{
		SecretID:    info.KeyID,
		SecretKey:   info.SecretValue,
		Region:      opts.Region,
		Endpoint:    "https://lighthouse.tencentcloudapi.com",
		PublicIP:    opts.PublicIP,
		Ports:       opts.Ports,
		Protocol:    opts.Protocol,
		Cidr:        opts.Cidr,
		Description: opts.Description,
		DryRun:      !opts.Yes,
	}
	return runLighthouseFirewallAdd(cmd, addOpts)
}
