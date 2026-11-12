// Package configinit 在本机起一个一次性录入页，让用户填写部署所需的信息和秘密，直接渲染出 moox.toml。
// 填写的值不经过 Agent：命令只把「已生成」的状态交回去。
package configinit

import (
	"context"
	_ "embed"
	"fmt"
	"slices"
	"strconv"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
)

// exampleTemplate 是仓库根目录 moox.toml.example 的副本（测试保证两者一致）。
// 录入页只替换它里面的账号、云凭据、通知和主机拓扑，其余段落（保留期、因子、日志等）保持示例里的默认值。
//
//go:embed moox.toml.example
var exampleTemplate string

// Render 把请求渲染成一份完整的 moox.toml：先借助 env 识别服务器、检测因子机，
// 再用 CLI 自己的严格解码和校验确认结果可用。失败时返回的错误信息不含任何秘密值。
func Render(ctx context.Context, request *Request, env Environment) ([]byte, error) {
	if err := request.normalize(ctx, env); err != nil {
		return nil, err
	}
	lines := strings.Split(exampleTemplate, "\n")
	edits := []struct{ section, key, value string }{
		{"admin", "username", tomlString(request.Admin.Username)},
		{"admin", "password", tomlString(request.Admin.Password)},
		{"tencent_cloud", "secret_id", tomlString(request.Tencent.SecretID)},
		{"tencent_cloud", "secret_key", tomlString(request.Tencent.SecretKey)},
		{"tencent_cloud", "region", tomlString(request.region)},
		{"notification", "channel_type", tomlString(request.Notification.ChannelType)},
		{"notification", "webhook_url", tomlString(request.Notification.WebhookURL)},
		{"notification", "secret", tomlString(request.Notification.Secret)},
	}
	for _, edit := range edits {
		var err error
		if lines, err = setKey(lines, edit.section, edit.key, edit.value); err != nil {
			return nil, err
		}
	}
	plan := renderSCFPlan(request)
	lines, err := replaceTopology(lines, renderTopology(request))
	if err != nil {
		return nil, err
	}
	lines = replaceExchangeKeys(lines, request.ExchangeKeys)
	if plan != nil {
		if lines, err = setKey(lines, "scf_plan", "enabled", "true"); err != nil {
			return nil, err
		}
		lines = append(lines, plan...)
	}
	raw := []byte(strings.Join(lines, "\n"))
	manifest, err := setupconfig.Parse(raw)
	if err != nil {
		// 校验错误只提到字段名和规则，不含值；仍然只返回错误文本，不返回渲染结果。
		return nil, err
	}
	if manifest.ComponentHost("factor-engine") == "" {
		// 没有因子机：打开因子只会长期告警。
		if lines, err = setKey(lines, "factors", "enabled", "false"); err != nil {
			return nil, err
		}
		return []byte(strings.Join(lines, "\n")), nil
	}
	if manifest.ComponentHost("collector-stream") != "" {
		return raw, nil
	}
	// 有因子机、没有海外机：crypto 采集不起来，去掉读 crypto 数据的默认因子任务，保留其他空间（如 A 股）的任务。
	var crypto []string
	for _, task := range manifest.Factors.Tasks {
		if task.SpaceID == "crypto" {
			crypto = append(crypto, task.ID)
		}
	}
	if len(crypto) == 0 {
		return raw, nil
	}
	return removeFactorEntries(raw, crypto, nil, &MigrateReport{})
}

// replaceTopology 把示例里从第一个 [hosts.*] 到 [egress_proxy] 之前的主机和部署段整体换成生成的拓扑。
func replaceTopology(lines []string, topology []string) ([]string, error) {
	start, end := -1, -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if start < 0 && strings.HasPrefix(trimmed, "[hosts.") {
			start = i
		}
		if start >= 0 && trimmed == "[egress_proxy]" {
			end = i
			break
		}
	}
	if start < 0 || end < 0 {
		return nil, fmt.Errorf("示例配置里找不到主机拓扑段（[hosts.*] 到 [egress_proxy]），请同步模板")
	}
	out := append([]string(nil), lines[:start]...)
	out = append(out, topology...)
	out = append(out, "")
	return append(out, lines[end:]...), nil
}

func renderTopology(request *Request) []string {
	var out []string
	for _, host := range request.hosts {
		out = append(out, renderHost(host)...)
		out = append(out, "")
	}
	out = append(out, "# host-gateway 与 host-agent 由代码为每台部署主机自动补齐，无需手工列出。", "[deployments]")
	for _, host := range request.hosts {
		quoted := make([]string, len(host.Components))
		for i, id := range host.Components {
			quoted[i] = tomlString(id)
		}
		out = append(out, host.ID+" = ["+strings.Join(quoted, ", ")+"]")
	}
	return out
}

func renderHost(host Host) []string {
	out := []string{"[hosts." + host.ID + "]"}
	if label := setupconfig.DefaultHostLabel(host.ID); label != "" {
		out = append(out, "label = "+tomlString(label))
	}
	out = append(out, "address = "+tomlString(host.Address))
	if host.Outbound {
		out = append(out, `reachability = "outbound"`)
	}
	if host.Region != "" {
		out = append(out, "region = "+tomlString(host.Region))
	}
	if host.Provider != "" {
		out = append(out, "provider = "+tomlString(host.Provider))
	}
	if host.renderPrivateAddress {
		out = append(out, "private_address = "+tomlString(host.PrivateAddress))
	}
	return append(out, "ssh = { port = "+strconv.Itoa(host.SSHPort)+", username = "+tomlString(host.SSHUser)+", private_key_file = "+tomlString(sshKeyTOMLPath)+" }")
}

// scfAccountID、scfAccountName 是渲染进 [scf_plan.cloud_account] 的云函数账号登记；scfCredentialSecretID 是 Admin 保存
// 第一步那把腾讯云密钥用的密钥 ID（modules/admin/internal/service/setup 的 tencentSecretID），云函数账号只引用它。
const (
	scfAccountID          = "tencent-scf"
	scfAccountName        = "Tencent SCF"
	scfCredentialSecretID = "tencent-default"
)

// renderSCFPlan 生成 A 股（stockcn）定时采集云函数的 [scf_plan] 子表：A 股只能走云函数。函数绑定入口主机所在地域的 VPC 与子网，
// 经入口主机的私网地址访问它上面的 Access。入口主机优先选存储服务器：函数与 Storage 同地域，写入全程走内网，不经公网转发；
// 存储服务器不满足条件时，退而选其他带 Access 且有可用 VPC 的主机（如海外服务器），由它的 Access 转发到 Storage。
// 代码包放在管理台所在地域、按 AppId 命名的 COS 桶（首次上传时自动创建）。
// 没有可用的入口主机或拿不到 AppId 时返回 nil，[scf_plan] 保持关闭，A 股要以后补配。
func renderSCFPlan(request *Request) []string {
	if request.appID == "" || !tencent.IsSCFRegion(request.region) {
		return nil
	}
	entry := scfEntryHost(request.hosts)
	if entry == nil {
		return nil
	}
	// 只有生成云函数计划时才给入口主机写私网地址（函数经它访问 Access），其他主机一律不写。
	entry.renderPrivateAddress = true
	bucket := "moox-scf-" + strings.TrimPrefix(request.region, "ap-") + "-" + request.appID
	return []string{
		"",
		"# A 股定时采集云函数（由 config init 按第一步的密钥与入口主机的私有网络生成；入口优先选存储服务器，同地域走内网）。",
		"[scf_plan.cloud_account]",
		"account_id = " + tomlString(scfAccountID),
		"account_name = " + tomlString(scfAccountName),
		"credential_secret_id = " + tomlString(scfCredentialSecretID),
		"app_id = " + tomlString(request.appID),
		"cos_region = " + tomlString(request.region),
		"cos_bucket = " + tomlString(bucket),
		"",
		"[scf_plan.regions." + entry.Region + "]",
		"vpc_id = " + tomlString(entry.VpcID),
		"subnet_id = " + tomlString(entry.SubnetID),
		"access_host = " + tomlString(entry.ID),
		"",
		"[[scf_plan.spaces]]",
		`space_id = "stockcn"`,
		"[scf_plan.spaces.markets]",
		"equity = [" + tomlString(entry.Region) + "]",
		"",
	}
}

// scfEntryHost 选云函数的入口主机：先看存储服务器，再按顺序看其他带 Access 的主机，取第一台可用的。可用指：
// 有 VPC、子网与私网地址，所在地域支持云函数；并且同地域的其他主机都在同一个 VPC——写了 private_address 的主机，
// 同地域的其他主机（如管理台）会改走私网探测它，VPC 不同时私网不通。
func scfEntryHost(hosts []Host) *Host {
	usable := func(candidate *Host) bool {
		if !slices.Contains(candidate.Components, "access") || candidate.VpcID == "" || candidate.SubnetID == "" ||
			candidate.PrivateAddress == "" || !tencent.IsSCFRegion(candidate.Region) {
			return false
		}
		for _, host := range hosts {
			if host.ID != candidate.ID && host.Region == candidate.Region && host.VpcID != candidate.VpcID {
				return false
			}
		}
		return true
	}
	for i := range hosts {
		if slices.Contains(hosts[i].Components, "storage-primary") && usable(&hosts[i]) {
			return &hosts[i]
		}
	}
	for i := range hosts {
		if !slices.Contains(hosts[i].Components, "storage-primary") && usable(&hosts[i]) {
			return &hosts[i]
		}
	}
	return nil
}

// tomlString 渲染 TOML 基本字符串。normalize 已经拒绝了控制字符，这里仍按规范转义，作为第二道防线。
func tomlString(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
