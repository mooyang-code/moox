package ssh

import (
	"context"
	"fmt"
	"path/filepath"
)

// ReadPrivateFile 读取远端部署用户私有的 0600 常规文件：拒绝链接、空文件和超限文件，
// 并在复制前后核对文件身份与摘要，避免读到写入中的内容。
func ReadPrivateFile(ctx context.Context, transport Client, path string, limit int) ([]byte, error) {
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
