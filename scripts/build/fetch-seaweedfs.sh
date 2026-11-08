#!/usr/bin/env bash
# 取固定版本的 SeaweedFS weed 二进制放进输出目录：缓存在仓库外，校验 sha256 不符就删掉并失败。
# 用法：fetch-seaweedfs.sh <amd64|arm64> <输出目录>
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SUMS="${MOOX_SEAWEEDFS_SUMS:-${ROOT}/scripts/build/seaweedfs-sha256.txt}"
BASE="${MOOX_SEAWEEDFS_DL_BASE:-https://github.com/seaweedfs/seaweedfs/releases/download}"
CACHE="${MOOX_SEAWEEDFS_CACHE:-${HOME}/.cache/moox/seaweedfs}"

die() {
  printf '[fetch-seaweedfs] 错误：%s\n' "$*" >&2
  exit 1
}

[[ $# -eq 2 ]] || die "用法：fetch-seaweedfs.sh <amd64|arm64> <输出目录>"
arch="$1"
out="$2"
[[ "${arch}" == amd64 || "${arch}" == arm64 ]] || die "只支持 amd64、arm64：${arch}"
file="linux_${arch}.tar.gz"
read -r version sha < <(awk -v f="${file}" '$1 !~ /^#/ && $2 == f {print $1, $3}' "${SUMS}")
[[ -n "${version:-}" && "${sha:-}" =~ ^[0-9a-f]{64}$ ]] || die "${SUMS} 里没有 ${file} 的版本与校验和"

sha_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

dir="${CACHE}/${version}"
tarball="${dir}/${file}"
mkdir -p "${dir}" "${out}"
if [[ ! -s "${tarball}" || "$(sha_of "${tarball}")" != "${sha}" ]]; then
  rm -f "${tarball}"
  echo "==> 下载 SeaweedFS ${version} ${file}" >&2
  curl -fL --retry 3 -o "${tarball}.part" "${BASE}/${version}/${file}"
  actual="$(sha_of "${tarball}.part")"
  if [[ "${actual}" != "${sha}" ]]; then
    rm -f "${tarball}.part"
    die "${file} 的 SHA-256 不一致：期望 ${sha}，实际 ${actual}"
  fi
  mv "${tarball}.part" "${tarball}"
fi

work="$(mktemp -d "${TMPDIR:-/tmp}/moox-seaweedfs.XXXXXX")"
trap 'rm -rf "${work}"' EXIT
tar -xzf "${tarball}" -C "${work}" weed
install -m 0755 "${work}/weed" "${out}/weed"
echo "==> weed ${version} (linux/${arch}) -> ${out}/weed" >&2
