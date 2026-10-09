package command

import (
	"context"
	"fmt"
	"strings"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

// ensureControlPrincipalKey 在 control 上用 moox-admin-cli 确保外部调用方 principal 的签名密钥存在（已有就复用），
// 返回密钥文件内容（CallerKey JSON）。root 是 control 的部署根目录。
func ensureControlPrincipalKey(ctx context.Context, transport setupssh.Client, root, principal string) ([]byte, error) {
	result, err := transport.Run(ctx, []string{"bash", "-c", `set -eu
root="$1"
out="$(mktemp)"
trap 'rm -f "$out"' EXIT
"$root/current/bin/moox-admin-cli" keys ensure --db-path "$root/data/admin/admin.db" \
  --encryption-key-file "$root/secrets/admin-encryption-key" --caller "$2" --principal --out "$out" >/dev/null
cat "$out"`, "moox-principal-key", root, principal}, nil)
	if err != nil {
		return nil, fmt.Errorf("在 control 上导出 %s 的签名密钥: %w: %s", principal, err, strings.TrimSpace(result.Stderr))
	}
	if len(result.Stdout) == 0 || len(result.Stdout) > 4096 {
		return nil, fmt.Errorf("control 返回的 %s 签名密钥无效", principal)
	}
	return []byte(result.Stdout), nil
}

// readControlFile 读取 control 上的一个文件（绝对路径）。
func readControlFile(ctx context.Context, transport setupssh.Client, path string) ([]byte, error) {
	result, err := transport.Run(ctx, []string{"cat", "--", path}, nil)
	if err != nil || len(result.Stdout) == 0 || len(result.Stdout) > 1<<20 {
		return nil, fmt.Errorf("control 上的文件 %s 不存在或为空", path)
	}
	return []byte(result.Stdout), nil
}
