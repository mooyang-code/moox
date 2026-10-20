#!/usr/bin/env bash
# 把 .go-version 固定的 Go 安装到 ~/.local/go<版本>（构建脚本与 CI 都优先用这个位置），不改动系统里已有的 Go。
# 支持 macOS、Linux、WSL 和 Windows 的 Git Bash（amd64 / arm64）。下载官方安装包并校验 SHA-256（来自官方发布清单）。
#
# 用法：install-go.sh
# 环境变量：
#   MOOX_GO_DL_BASE   下载地址前缀，默认 https://golang.google.cn/dl（Go 官方的国内镜像，海外也能用；也可设 https://go.dev/dl）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION="$(tr -d '[:space:]' < "${ROOT}/.go-version")"
BASE="${MOOX_GO_DL_BASE:-https://golang.google.cn/dl}"
DEST="${HOME}/.local/go${VERSION}"

die() { echo "install-go: $*" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) os=darwin; ext=tar.gz ;;
  Linux) os=linux; ext=tar.gz ;;
  MINGW*|MSYS*|CYGWIN*) os=windows; ext=zip ;;
  *) die "不支持的系统 $(uname -s)" ;;
esac
case "$(uname -m)" in
  x86_64|amd64|AMD64) arch=amd64 ;;
  arm64|aarch64|ARM64) arch=arm64 ;;
  *) die "不支持的架构 $(uname -m)" ;;
esac
file="go${VERSION}.${os}-${arch}.${ext}"

if [[ -x "${DEST}/bin/go" || -x "${DEST}/bin/go.exe" ]]; then
  have="$(GOTOOLCHAIN=local "${DEST}/bin/go" env GOVERSION 2>/dev/null || true)"
  if [[ "${have}" == "go${VERSION}" ]]; then echo "已安装：${DEST}（${have}）"; exit 0; fi
  die "${DEST} 已存在但版本是 ${have:-未知}，不是 go${VERSION}：确认后删除该目录再重试"
fi
[[ ! -e "${DEST}" ]] || die "${DEST} 已存在但不是完整的 Go 安装：确认后删除该目录再重试"

command -v curl >/dev/null 2>&1 || die "需要 curl"
command -v python3 >/dev/null 2>&1 || command -v jq >/dev/null 2>&1 || die "需要 python3 或 jq 来读取官方发布清单"

work="$(mktemp -d "${TMPDIR:-/tmp}/moox-install-go.XXXXXX")"
trap 'rm -rf "${work}"' EXIT

echo "==> 读取官方发布清单，取 ${file} 的 SHA-256"
curl -fsSL "${BASE}/?mode=json&include=all" -o "${work}/releases.json"
if command -v python3 >/dev/null 2>&1; then
  sha="$(python3 -I - "${work}/releases.json" "${VERSION}" "${file}" <<'PY'
import json, sys
path, version, name = sys.argv[1:4]
for release in json.load(open(path)):
    if release["version"] == "go" + version:
        for item in release["files"]:
            if item["filename"] == name:
                print(item["sha256"])
PY
)"
else
  sha="$(jq -r --arg v "go${VERSION}" --arg f "${file}" '.[] | select(.version == $v) | .files[] | select(.filename == $f) | .sha256' "${work}/releases.json")"
fi
[[ "${sha}" =~ ^[0-9a-f]{64}$ ]] || die "官方发布清单里没有 ${file}（Go ${VERSION} 在 ${os}/${arch} 上没有官方安装包？）"

echo "==> 下载 ${BASE}/${file}"
curl -fL --retry 3 -o "${work}/${file}" "${BASE}/${file}"
if command -v sha256sum >/dev/null 2>&1; then actual="$(sha256sum "${work}/${file}" | cut -d' ' -f1)"; else actual="$(shasum -a 256 "${work}/${file}" | cut -d' ' -f1)"; fi
[[ "${actual}" == "${sha}" ]] || die "SHA-256 不一致：期望 ${sha}，实际 ${actual}；已放弃安装"

echo "==> 解压到 ${DEST}"
mkdir -p "${work}/x" "${HOME}/.local"
if [[ "${ext}" == zip ]]; then
  command -v unzip >/dev/null 2>&1 || die "Windows 的 Git Bash 需要 unzip（或改在 WSL 里运行）"
  unzip -q "${work}/${file}" -d "${work}/x"
else
  tar -xzf "${work}/${file}" -C "${work}/x"
fi
mv "${work}/x/go" "${DEST}"

have="$(GOTOOLCHAIN=local "${DEST}/bin/go" env GOVERSION)"
[[ "${have}" == "go${VERSION}" ]] || die "安装后版本不对：${have}"
echo "已安装：${DEST}（${have}）"
echo "构建脚本会自动优先使用它；如需在当前终端直接使用：export PATH=\"${DEST}/bin:\$PATH\""
