package deploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/release"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// credentialRequest 是一台主机这次部署需要从 control 取的密钥与证书。
type credentialRequest struct {
	// CallerKeys 是调用方签名密钥（moox-admin-cli keys ensure，已有就复用）。
	CallerKeys []release.CallerKey
	// HostCertificate 为 true 时重新签发这台主机的主机网关证书。
	HostCertificate bool
	// Principals 为 true 时导出外部调用方的密钥集（部署了外部接入的主机）。
	Principals bool
	// SharedSecrets、EventBusFiles 从 control 的 secrets/、secrets/eventbus/ 复制。
	SharedSecrets, EventBusFiles []string
}

// credentialRequestFor 汇总选中组件需要的密钥与证书。目标是 control 时，共享密钥和消息总线凭据已经在本机，不用复制。
func credentialRequestFor(plan release.Plan, selected []release.Component) credentialRequest {
	var request credentialRequest
	keys := map[string]release.CallerKey{}
	shared, eventbus := map[string]bool{}, map[string]bool{}
	for _, component := range selected {
		for _, key := range component.CallerKeys {
			keys[key.File] = key
		}
		if component.ID == "host-gateway" {
			request.HostCertificate = true
		}
		if component.ID == "access" {
			request.Principals = true
		}
		for _, name := range component.SharedSecrets {
			shared[name] = true
		}
		for _, name := range component.EventBusFiles {
			eventbus[name] = true
		}
	}
	for _, file := range sortedKeys(keys) {
		request.CallerKeys = append(request.CallerKeys, keys[file])
	}
	if plan.HostID != servicecatalog.ControlHostID {
		request.SharedSecrets = sortedSet(shared)
		request.EventBusFiles = sortedSet(eventbus)
	}
	return request
}

func sortedKeys[V any](values map[string]V) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// credentialScript 生成在 control 上执行的脚本：用 moox-admin-cli 导出密钥和证书，复制共享密钥，打包后以 base64 输出。
func credentialScript(host setupconfig.Host, request credentialRequest) string {
	var b strings.Builder
	b.WriteString(`set -eu
root="$1"
cli="$root/current/bin/moox-admin-cli"
db="$root/data/admin/admin.db"
key="$root/secrets/admin-encryption-key"
pki="$root/secrets/pki"
[ -x "$cli" ] || { echo "control 上还没有安装 moox-admin-cli，请先执行 moox-cli setup bootstrap" >&2; exit 1; }
out="$(mktemp -d)"
trap 'rm -rf "$out"' EXIT
mkdir -p "$out/secrets/eventbus" "$out/certs"
copy() {
  [ -s "$1" ] || { echo "control 上缺少 $1，请先部署 control" >&2; exit 1; }
  cp "$1" "$2"
}
`)
	for _, key := range request.CallerKeys {
		fmt.Fprintf(&b, "\"$cli\" keys ensure --db-path \"$db\" --encryption-key-file \"$key\" --caller %s --out \"$out/secrets/%s\" >/dev/null\n",
			shellQuote(key.Identity), key.File)
	}
	if request.HostCertificate {
		fmt.Fprintf(&b, "\"$cli\" pki issue --pki-dir \"$pki\" --host %s --address %s", shellQuote(host.ID), shellQuote(host.Address))
		if host.PrivateAddress != "" {
			fmt.Fprintf(&b, " --address %s", shellQuote(host.PrivateAddress))
		}
		b.WriteString(" --out-dir \"$out/certs/host-gateway\" >/dev/null\n")
	}
	b.WriteString("\"$cli\" pki export-ca --pki-dir \"$pki\" --out \"$out/certs/moox-ca.crt\" >/dev/null\n")
	if request.Principals {
		b.WriteString("\"$cli\" keys export-principals --db-path \"$db\" --encryption-key-file \"$key\" --out \"$out/secrets/access-principals.json\" >/dev/null\n")
	}
	for _, name := range request.SharedSecrets {
		fmt.Fprintf(&b, "copy \"$root/secrets/%s\" \"$out/secrets/%s\"\n", name, name)
	}
	for _, name := range request.EventBusFiles {
		fmt.Fprintf(&b, "copy \"$root/secrets/eventbus/%s\" \"$out/secrets/eventbus/%s\"\n", name, name)
	}
	b.WriteString("tar -C \"$out\" -czf - . | base64\n")
	return b.String()
}

// fetchCredentials 在 control 上导出密钥与证书，返回要装进目标主机的文件（路径以 secrets/ 或 certs/ 开头）。
func fetchCredentials(ctx context.Context, control setupssh.Client, controlRoot string, host setupconfig.Host, request credentialRequest) ([]release.File, error) {
	result, err := control.Run(ctx, []string{"bash", "-c", credentialScript(host, request), "moox-credentials", controlRoot}, nil)
	if err != nil {
		return nil, fmt.Errorf("在 control 上导出密钥与证书: %w: %s", err, strings.TrimSpace(result.Stderr))
	}
	raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(result.Stdout), ""))
	if err != nil {
		return nil, fmt.Errorf("解析 control 导出的密钥: %w", err)
	}
	return untarFiles(raw)
}

func untarFiles(raw []byte) ([]release.File, error) {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("解析 control 导出的密钥: %w", err)
	}
	reader := tar.NewReader(gz)
	var files []release.File
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("解析 control 导出的密钥: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Clean(strings.TrimPrefix(header.Name, "./"))
		if strings.HasPrefix(name, "..") || !(strings.HasPrefix(name, "secrets/") || strings.HasPrefix(name, "certs/")) {
			return nil, fmt.Errorf("control 导出的文件路径无效：%s", header.Name)
		}
		data, err := io.ReadAll(io.LimitReader(reader, maxCredentialFileBytes+1))
		if err != nil {
			return nil, err
		}
		// 超过上限时直接报错：静默截断会让一份被截掉的密钥或证书被当成完整文件装到主机上。
		if len(data) > maxCredentialFileBytes {
			return nil, fmt.Errorf("control 导出的文件 %s 超过 %d 字节", header.Name, maxCredentialFileBytes)
		}
		files = append(files, release.File{Path: name, Mode: 0o600, Data: data})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// notificationFile 是告警推送渠道的密钥文件，按 moox.toml 生成。
func notificationFile(manifest setupconfig.Manifest) release.File {
	data := fmt.Sprintf("MOOX_NOTIFICATION_CHANNEL_TYPE=%s\nMOOX_NOTIFICATION_WEBHOOK_URL=%s\n",
		manifest.Notification.ChannelType, manifest.Notification.WebhookURL)
	return release.File{Path: "secrets/notification.env", Mode: 0o600, Data: []byte(data)}
}

// maxCredentialFileBytes 是单个密钥或证书文件的大小上限。
const maxCredentialFileBytes = 1 << 20

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
