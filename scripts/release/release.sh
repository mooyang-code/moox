#!/usr/bin/env bash
# 生成一个平台的发布包：全部二进制、moox.toml 示例、默认初始化数据和文档。
# 部署不使用发布包：moox-cli setup deploy-host 在仓库中按 moox.toml 渲染每台主机的发布（见 docs/部署与运维.md）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION="${VERSION:-dev}"
OS="${TARGET_GOOS:-${GOOS:-$(go env GOOS)}}"
ARCH="${TARGET_GOARCH:-${GOARCH:-$(go env GOARCH)}}"
RELEASE_ROOT="${ROOT}/release/moox-${VERSION}-${OS}-${ARCH}"
ARCHIVE="${RELEASE_ROOT}.tar.gz"

build_web_assets() {
  (
    cd "${ROOT}/web"
    CI=true pnpm install --frozen-lockfile --config.confirmModulesPurge=false
    pnpm run build:prod
  )
  (cd "${ROOT}/web-host" && go run github.com/rakyll/statik@v0.1.7 -src=../web/dist -dest=./internal)
}

if [[ "${SKIP_WEB_ASSETS:-0}" == "1" ]]; then
  echo "==> reuse existing web assets"
else
  build_web_assets
fi
TARGET_GOOS="${OS}" TARGET_GOARCH="${ARCH}" "${ROOT}/scripts/build/build.sh"

# 不连 Storage 校验默认元数据：发布机可能交叉编译，用本机构建的 moox-cli 执行。
validate_default_metadata() {
  local seed="${ROOT}/config/setup/metadata.yaml"
  [[ -s "${seed}" ]] || {
    echo "missing default metadata: ${seed}" >&2
    exit 1
  }
  (cd "${ROOT}" && go run ./modules/cli/cmd/moox-cli metadata apply --file "${seed}" --dry-run >/dev/null)
  grep -q 'data_node_id: storage-node-0' "${seed}"
  grep -q 'freq: 1m' "${seed}"
  for dataset in dataset_mooxsys_host_resource dataset_mooxsys_host_filesystem dataset_mooxsys_host_disk dataset_mooxsys_host_network; do
    grep -q "dataset_id: ${dataset}" "${seed}"
  done
}

validate_default_metadata

# 发布包中的二进制：全部组件和各模块的运维 CLI。主机采集器只有 Linux 版本。
binaries=(
  moox-cli moox-admin moox-admin-cli moox-host-gateway moox-host-gateway-cli moox-eventbus moox-web-host
  moox-cloudnode moox-cloudnode-cli moox-collector moox-collector-cli moox-factor-mgr moox-factor-mgr-cli
  moox-factor-engine moox-strategy moox-strategy-cli moox-trade moox-trade-cli moox-monitor moox-monitor-cli
  moox-storage-primary moox-storage-node moox-storage-view moox-storage-cli moox-access moox-egress-proxy
  moox-archive moox-archive-cli
)
if [[ "${OS}" == "linux" ]]; then
  binaries+=(moox-host-agent moox-host-agent-cli)
fi

rm -rf "${RELEASE_ROOT}"
mkdir -p "${RELEASE_ROOT}/bin" "${RELEASE_ROOT}/config/setup" "${RELEASE_ROOT}/config/doctor" "${RELEASE_ROOT}/docs"
for binary in "${binaries[@]}"; do
  name="${binary}"
  [[ "${OS}" == "windows" ]] && name="${binary}.exe"
  [[ -f "${ROOT}/bin/${name}" ]] || {
    echo "missing binary: bin/${name}" >&2
    exit 1
  }
  cp "${ROOT}/bin/${name}" "${RELEASE_ROOT}/bin/${name}"
done

cp "${ROOT}/moox.toml.example" "${RELEASE_ROOT}/config/moox.toml.example"
cp -R "${ROOT}/config/setup/." "${RELEASE_ROOT}/config/setup/"
cp "${ROOT}/modules/cli/config/cli.yaml" "${RELEASE_ROOT}/config/cli.yaml"
cp "${ROOT}/packages/doctor/report.schema.json" "${RELEASE_ROOT}/config/doctor/report.schema.json"
cp -R "${ROOT}/docs/." "${RELEASE_ROOT}/docs/"
cp "${ROOT}/README.md" "${RELEASE_ROOT}/README.md"

tar -C "${ROOT}/release" -czf "${ARCHIVE}" "$(basename "${RELEASE_ROOT}")"
echo "==> release package: ${ARCHIVE}"
