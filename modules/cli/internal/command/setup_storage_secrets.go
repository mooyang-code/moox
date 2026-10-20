package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

// nativeStorageEnvironmentFile 是原生 storage 单元当前发布里 storage-primary 的私密运行环境，
// 由 unitinstall 在准备发布时写入（JSON 映射，0600），同时包含 Node、Primary、View 三个角色密钥。
const nativeStorageEnvironmentFile = "current/secrets/runtime-storage-primary.json"

// nativeStorageEnvironmentLimit 与 unitinstall 读取同一文件时的上限保持同一量级。
const nativeStorageEnvironmentLimit = 64 << 10

// storageRoleSecrets 是 Storage 三个角色的共享密钥，只在内存中用于派生 AppKey。
type storageRoleSecrets struct {
	Node    string
	Primary string
	View    string
}

func (storageRoleSecrets) String() string     { return "StorageRoleSecrets{已省略}" }
func (s storageRoleSecrets) GoString() string { return s.String() }

func nativeStorageEnvironmentPath(storageRoot string) string {
	return path.Join(storageRoot, nativeStorageEnvironmentFile)
}

// parseNativeStorageRoleSecrets 从原生 storage-primary 运行环境中取出三个角色密钥；错误信息不含任何密钥内容。
func parseNativeStorageRoleSecrets(raw []byte) (storageRoleSecrets, error) {
	var values map[string]string
	if len(raw) == 0 || len(raw) > nativeStorageEnvironmentLimit || json.Unmarshal(raw, &values) != nil {
		return storageRoleSecrets{}, errors.New("storage_role_secrets_invalid")
	}
	secrets := storageRoleSecrets{
		Node:    values["MOOX_STORAGE_NODE_AUTH_SECRET"],
		Primary: values["MOOX_STORAGE_PRIMARY_AUTH_SECRET"],
		View:    values["MOOX_STORAGE_VIEW_AUTH_SECRET"],
	}
	for _, value := range []string{secrets.Node, secrets.Primary, secrets.View} {
		// 与 unitdeploy 的约束一致：原生部署生成的角色密钥至少 32 字符。
		if len(value) < 32 || !storageSecretValuePattern.MatchString(value) {
			return storageRoleSecrets{}, errors.New("storage_role_secrets_invalid")
		}
	}
	return secrets, nil
}

// readRemoteStorageRoleSecrets 经 SSH 只读取原生 storage 单元的角色密钥。
func readRemoteStorageRoleSecrets(ctx context.Context, transport setupssh.Client, storageRoot string) (storageRoleSecrets, error) {
	raw, err := readRemotePrivateFile(ctx, transport, nativeStorageEnvironmentPath(storageRoot), nativeStorageEnvironmentLimit)
	if err != nil {
		return storageRoleSecrets{}, errors.New("storage_verification_auth_unavailable: 无法读取原生 Storage 单元的私密运行环境（" + nativeStorageEnvironmentFile + "）")
	}
	secrets, err := parseNativeStorageRoleSecrets(raw)
	clear(raw)
	if err != nil {
		return storageRoleSecrets{}, fmt.Errorf("storage_verification_auth_unavailable: %w", err)
	}
	return secrets, nil
}

// readRemotePrivateFile 读取远端部署用户私有的 0600 常规文件：拒绝链接、空文件和超限文件，
// 并在复制前后核对文件身份与摘要，避免读到写入中的内容。
func readRemotePrivateFile(ctx context.Context, transport setupssh.Client, path string, limit int) ([]byte, error) {
	if transport == nil || !filepath.IsAbs(path) || limit <= 0 {
		return nil, fmt.Errorf("remote secret unavailable")
	}
	result, err := transport.Run(ctx, []string{
		"sh", "-lc",
		`set -eu
path="$1"
limit="$2"
file_id() { stat -c '%d:%i:%s:%Y' "$1" 2>/dev/null || stat -f '%d:%i:%z:%m' "$1"; }
file_hash() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}';
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}
attempt=1
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT HUP INT TERM
while [ "$attempt" -le 3 ]; do
  [ -f "$path" ] && [ ! -L "$path" ] || exit 1
  mode=$(stat -c '%a' "$path" 2>/dev/null || stat -f '%Lp' "$path")
  [ "$mode" = 600 ] || exit 1
  size=$(wc -c <"$path")
  [ "$size" -gt 0 ] && [ "$size" -le "$limit" ] || exit 1
  before="$(file_id "$path"):$(file_hash "$path")"
  cp "$path" "$tmp"
  after="$(file_id "$path"):$(file_hash "$path")"
  if [ "$before" = "$after" ]; then cat "$tmp"; exit 0; fi
  attempt=$((attempt + 1))
done
exit 1`,
		"moox-read-private-file", path, fmt.Sprint(limit),
	}, nil)
	if err != nil || result.ExitCode != 0 || len(result.Stdout) == 0 || len(result.Stdout) > limit {
		return nil, fmt.Errorf("remote secret unavailable")
	}
	return []byte(result.Stdout), nil
}
