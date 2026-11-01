#!/usr/bin/env bash
# 构建一个对外发布版本的全部产物（维护者执行；用户直接下载这些产物，不需要编译）。
#
#   moox-cli_<版本>_<系统>_<架构>.tar.gz（Windows 为 .zip）  五个平台的命令行
#   moox-bundle_<版本>_linux_amd64.tar.gz                    部署产物包：该提交的仓库文件快照 + 预编译的 linux/amd64 服务器二进制
#   moox-unit-<单元>_<版本>_linux_amd64.tar.gz               10 个部署单元包，目标机按需下载
#   moox-collector-scf_<版本>_linux_amd64                    云函数程序
#   SHA256SUMS
#
# 用法：scripts/release/build-release.sh v0.2.0 [输出目录，默认 dist/<版本>]
# 要求：工作区干净；本机有固定版本的 Go 与正在运行的 Docker（Storage 需要 CGO）。前端用仓库里已打包的内嵌资源。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
die() { echo "build-release: $*" >&2; exit 1; }

VERSION="${1:-}"
[[ "${VERSION}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "版本号要写成 vX.Y.Z"
OUT="${2:-${ROOT}/dist/${VERSION}}"
[[ -z "$(git -C "${ROOT}" status --porcelain)" ]] || die "工作区有未提交的改动"
COMMIT="$(git -C "${ROOT}" rev-parse HEAD)"

go_version="$(tr -d '[:space:]' < "${ROOT}/.go-version")"
if [[ -d "${HOME}/.local/go${go_version}/bin" ]]; then
  export PATH="${HOME}/.local/go${go_version}/bin:${PATH}"
fi

# 发布产物去掉符号表，体积约减半。
export GO_STRIP=1

mkdir -p "${OUT}"
OUT="$(cd "${OUT}" && pwd)"
[[ -z "$(ls -A "${OUT}")" ]] || die "输出目录 ${OUT} 不是空的"
STAGE="$(mktemp -d "${TMPDIR:-/tmp}/moox-release.XXXXXX")"
trap 'rm -rf "${STAGE}"' EXIT

# 1. 服务器二进制
BUNDLE="moox-${VERSION}"
mkdir -p "${STAGE}/${BUNDLE}"
git -C "${ROOT}" archive "${COMMIT}" | tar -x -C "${STAGE}/${BUNDLE}"

server_targets=(admin host-gateway eventbus console-proxy egress-proxy web-host cloudfunction collector collector-stream collector-scf
  factor-mgr factor-engine importer replay archive-mgr archive-worker strategy strategy-cli trade trade-cli monitor monitor-cli
  hostagent access storage-cli storage)
VERSION="${VERSION}" GIT_COMMIT="${COMMIT:0:8}" \
  bash "${ROOT}/scripts/build/build-linux-local.sh" --out "${STAGE}/${BUNDLE}/bin/linux-amd64" "${server_targets[@]}"

# 2. 各平台的 moox-cli
platforms=(darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64)
for platform in "${platforms[@]}"; do
  os="${platform%/*}" arch="${platform#*/}"
  dir="${STAGE}/cli-${os}-${arch}"
  TARGET_GOOS="${os}" TARGET_GOARCH="${arch}" CGO_ENABLED=0 BIN_DIR="${dir}" VERSION="${VERSION}" GIT_COMMIT="${COMMIT:0:8}" \
    bash "${ROOT}/scripts/build/build.sh" cli >&2
  name="moox-cli_${VERSION}_${os}_${arch}"
  if [[ "${os}" == "windows" ]]; then
    (cd "${dir}" && zip -q "${OUT}/${name}.zip" moox-cli.exe)
  else
    tar -C "${dir}" -czf "${OUT}/${name}.tar.gz" moox-cli
  fi
done

# 3. 部署单元包：用本机的 moox-cli 打包，文件排序、时间戳为 0，同一提交结果相同
BINARIES="${STAGE}/${BUNDLE}/bin/linux-amd64"
LOCAL_CLI="${STAGE}/cli-$(go env GOOS)-$(go env GOARCH)/moox-cli"
units=(host control eventbus storage access egress-proxy trade collector-stream factor-engine archive-worker)
mkdir -p "${STAGE}/unit-results"
for unit in "${units[@]}"; do
  "${LOCAL_CLI}" setup package --repo-root "${ROOT}" --profile "${unit}" --binary-dir "${BINARIES}" --goos linux --goarch amd64 \
    --output "${OUT}/moox-unit-${unit}_${VERSION}_linux_amd64.tar.gz" > "${STAGE}/unit-results/${unit}.json"
done
cp "${BINARIES}/moox-collector-scf" "${OUT}/moox-collector-scf_${VERSION}_linux_amd64"

# 4. 清单和产物包。产物包里也放一份 linux/amd64 的 moox-cli，方便直接在 Linux 操作机上使用。
git -C "${ROOT}" ls-files -z | python3 -I "${ROOT}/scripts/release/write-bundle-manifest.py" "${VERSION}" "${COMMIT}" \
  "${STAGE}/unit-results" "${OUT}/moox-collector-scf_${VERSION}_linux_amd64" "${STAGE}/${BUNDLE}/moox-bundle.json"
cp "${STAGE}/cli-linux-amd64/moox-cli" "${BINARIES}/moox-cli"
tar -C "${STAGE}" -czf "${OUT}/moox-bundle_${VERSION}_linux_amd64.tar.gz" "${BUNDLE}"

(cd "${OUT}" && shasum -a 256 ./* | sed 's#  \./#  #' > SHA256SUMS)
echo "产物在 ${OUT}："
ls -lh "${OUT}"
