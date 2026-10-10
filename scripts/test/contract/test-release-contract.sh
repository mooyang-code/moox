#!/usr/bin/env bash
# 发布与构建工具的契约：构建脚本的目标与输出名、发布包内容、依赖固定版本、SCF 打包不带密钥、多平台矩阵。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
RELEASE="${ROOT}/scripts/release/release.sh"
BUILD="${ROOT}/scripts/build/build.sh"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

bash "${ROOT}/scripts/test/contract/test-build-factor.sh"
bash "${ROOT}/scripts/test/contract/test-build-factor-linux-contract.sh"

(cd "${ROOT}/packages/doctor" && go test -count=1 ./...)

# 发布包：全部二进制、moox.toml 示例、默认初始化数据、Doctor 报告格式与 CLI 配置。
for contract in \
  'packages/doctor/report.schema.json' \
  'modules/cli/config/cli.yaml' \
  'moox.toml.example' \
  'config/setup/.' \
  'set -euo pipefail' \
  'SKIP_WEB_ASSETS' \
  'github.com/rakyll/statik@v0.1.7'; do
  grep -Fq -- "${contract}" "${RELEASE}" || fail "release.sh 缺少 ${contract}"
done
for binary in moox-cli moox-admin moox-host-gateway moox-host-agent moox-access moox-egress-proxy moox-storage-primary \
  moox-storage-node moox-storage-view moox-strategy moox-trade moox-monitor moox-factor-engine; do
  grep -Fq "${binary}" "${RELEASE}" || fail "release.sh 没有打包 ${binary}"
done
if grep -Eq 'caddy|deploy-moox|storage-start|modules/[a-z-]+/bin' "${RELEASE}"; then
  fail "发布包不再包含旧部署脚本、Caddy 模板和按模块分目录的二进制"
fi

unfrozen="--no-""frozen-lockfile"
floating_statik="statik@""latest"
if rg -n "pnpm install .*${unfrozen}|${floating_statik}" "${ROOT}/scripts" "${ROOT}/web-host/Makefile"; then
  fail "发布工具中有未固定版本的依赖"
fi

# 构建脚本：目标名称与组件目录的二进制一致。
grep -q 'binary_name' "${BUILD}"
grep -q 'collector-scf)' "${BUILD}"
grep -q 'host-agent)' "${BUILD}"
grep -q 'host-gateway)' "${BUILD}"
grep -q 'build_go modules/strategy ./cmd/server moox-strategy' "${BUILD}"
grep -q 'build_go modules/strategy ./cmd/cli moox-strategy-cli' "${BUILD}"
grep -q 'build_go modules/hostagent ./cmd/server moox-host-agent 0' "${BUILD}"
if grep -q 'collector-market-data-scf' "${BUILD}"; then
  fail "旧的 SCF 构建目标仍在"
fi
grep -q 'for role in primary node view; do' "${BUILD}"
for binary in $(sed -n 's/^    binary: //p' "${ROOT}/packages/servicecatalog/catalog.yaml"); do
  # caddy 由 moox-cli 下载；存储的三个进程由 build_storage 按角色构建（moox-storage-<角色>）。
  case "${binary}" in caddy | moox-storage-*) continue ;; esac
  grep -q "${binary}" "${BUILD}" || fail "组件目录中的二进制 ${binary} 没有构建目标"
done

# SCF 打包：不读取、不嵌入存储凭据。
if grep -q 'MOOX_STORAGE_PRIMARY_AUTH_SECRET' "${ROOT}/scripts/build/build-collector-scf-package.sh"; then
  fail "SCF 打包不能读取或嵌入存储凭据"
fi
grep -q 'eventbus-ca.pem' "${ROOT}/scripts/build/build-collector-scf-package.sh"
grep -q 'credential assignment is not permitted' "${ROOT}/scripts/build/build-collector-scf-package.sh"

# 默认初始化数据。
for path in \
  'config/setup/metadata.yaml' \
  'config/setup/dataset-health-policy.yaml' \
  'config/setup/collection-tasks.yaml'; do
  test -f "${ROOT}/${path}" || fail "缺少默认初始化文件 ${path}"
done
test ! -e "${ROOT}/examples/monitor-pipelines.yaml"
test ! -e "${ROOT}/examples/metadata-quant-initial.seed.yaml"

bash -n "${BUILD}" "${RELEASE}" "${ROOT}/scripts/release/release-matrix.sh"
grep -q 'RELEASE_PLATFORMS' "${ROOT}/scripts/release/release-matrix.sh"
matrix_output="$(VERSION=test RELEASE_PLATFORMS=linux/amd64,darwin/arm64,windows/amd64 "${ROOT}/scripts/release/release-matrix.sh" --dry-run)"
grep -q 'release test (linux/amd64' <<<"${matrix_output}"
grep -q 'release test (darwin/arm64' <<<"${matrix_output}"
grep -q 'release test (windows/amd64' <<<"${matrix_output}"

echo 'release contract passed'
