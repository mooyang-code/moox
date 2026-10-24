// Package configinit 在本机起一个一次性录入页，让用户填写部署所需的信息和秘密，直接渲染出 moox.toml。
// 填写的值不经过 Agent：命令只把「已生成」的状态交回去。
package configinit

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
)

// exampleTemplate 是仓库根目录 moox.toml.example 的副本（测试保证两者一致）。
// 录入页只替换它里面的账号、云凭据、通知和主机拓扑，其余段落（保留期、因子、日志等）保持示例里的默认值。
//
//go:embed moox.toml.example
var exampleTemplate string

// Render 把请求渲染成一份完整的 moox.toml：先借助 env 识别服务器、检测因子计算机，
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
	}
	for _, edit := range edits {
		var err error
		if lines, err = setKey(lines, edit.section, edit.key, edit.value); err != nil {
			return nil, err
		}
	}
	lines, err := replaceTopology(lines, renderTopology(request))
	if err != nil {
		return nil, err
	}
	raw := []byte(strings.Join(lines, "\n"))
	if _, err := setupconfig.Parse(raw); err != nil {
		// 校验错误只提到字段名和规则，不含值；仍然只返回错误文本，不返回渲染结果。
		return nil, err
	}
	return raw, nil
}

// replaceTopology 把示例里从第一个 [hosts.*] 到 [egress_proxy] 之前的主机和放置段整体换成生成的拓扑。
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
	if request.factorRemote.ID != "" {
		out = append(out, "# 因子计算机：运行因子引擎。它只登记登录方式，不加入 MooX 部署拓扑。")
		out = append(out, renderHost(request.factorRemote)...)
		out = append(out, "")
	}
	if request.factorEngine != "" {
		out = append(out, "# 因子引擎所在的机器：local 表示运行 moox-cli 的这台电脑。", "[factor_engine]", "host = "+tomlString(request.factorEngine), "")
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
	out := []string{"[hosts." + host.ID + "]", "address = " + tomlString(host.Address)}
	if host.Region != "" {
		out = append(out, "region = "+tomlString(host.Region))
	}
	if host.Provider != "" {
		out = append(out, "provider = "+tomlString(host.Provider))
	}
	if host.TLSMode != "" {
		out = append(out, "tls_mode = "+tomlString(host.TLSMode))
	}
	return append(out, "ssh = { port = "+strconv.Itoa(host.SSHPort)+", username = "+tomlString(host.SSHUser)+", password = "+tomlString(host.SSHPassword)+" }")
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
