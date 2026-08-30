#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
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
TARGET_GOOS="${OS}" TARGET_GOARCH="${ARCH}" "${ROOT}/scripts/build.sh"

validate_default_metadata() {
  local seed="${ROOT}/examples/setup/default/metadata.yaml"
  [[ -s "${seed}" ]] || {
    echo "missing default metadata: ${seed}" >&2
    exit 1
  }
  # Validate the release contract without contacting Storage. Runtime startup
  # performs the real create-or-verify apply after MetadataService is ready.
  # Use a host-built CLI here: the release binary may be cross-compiled for
  # Linux/arm64 and cannot be executed on the packaging workstation.
  (cd "${ROOT}" && go run ./modules/cli/cmd/moox-cli metadata apply --file "${seed}" --dry-run >/dev/null)
  grep -q 'host_storage:' "${ROOT}/modules/monitor/config/app.yaml"
  grep -q 'result_retention_days: 14' "${ROOT}/modules/monitor/config/app.yaml"
  grep -q 'data_node_id: storage-node-0' "${seed}"
  grep -q 'keep_duration:' "${seed}"
  for dataset in host_resource_v1 host_fs_v1 host_disk_v1 host_net_v1; do
    grep -q "dataset_id: ${dataset}" "${seed}"
  done
}

validate_default_metadata

rm -rf "${RELEASE_ROOT}"
mkdir -p \
  "${RELEASE_ROOT}/cli/bin" \
  "${RELEASE_ROOT}/admin/bin" \
  "${RELEASE_ROOT}/admin/config" \
  "${RELEASE_ROOT}/gateway/bin" \
  "${RELEASE_ROOT}/gateway/config" \
  "${RELEASE_ROOT}/eventbus/bin" \
  "${RELEASE_ROOT}/eventbus/config" \
  "${RELEASE_ROOT}/web-host/bin" \
  "${RELEASE_ROOT}/cloudnode/bin" \
  "${RELEASE_ROOT}/cloudnode/config" \
  "${RELEASE_ROOT}/collector/bin" \
  "${RELEASE_ROOT}/collector/config" \
  "${RELEASE_ROOT}/factor/bin" \
  "${RELEASE_ROOT}/factor/config" \
  "${RELEASE_ROOT}/factor/factors" \
  "${RELEASE_ROOT}/factor/python-runtime" \
  "${RELEASE_ROOT}/strategy/bin" \
  "${RELEASE_ROOT}/strategy/config" \
  "${RELEASE_ROOT}/trade/bin" \
  "${RELEASE_ROOT}/trade/config" \
  "${RELEASE_ROOT}/monitor/bin" \
  "${RELEASE_ROOT}/monitor/config" \
  "${RELEASE_ROOT}/storage-primary/bin" \
  "${RELEASE_ROOT}/storage-primary/config" \
  "${RELEASE_ROOT}/storage-primary/schema" \
  "${RELEASE_ROOT}/storage-view/bin" \
  "${RELEASE_ROOT}/storage-view/config" \
  "${RELEASE_ROOT}/storage-view/schema" \
  "${RELEASE_ROOT}/archive/bin" \
  "${RELEASE_ROOT}/archive/config" \
  "${RELEASE_ROOT}/examples" \
  "${RELEASE_ROOT}/docs"
mkdir -p "${RELEASE_ROOT}/lib" "${RELEASE_ROOT}/config/caddy" "${RELEASE_ROOT}/config/doctor"
if [[ "${OS}" == "linux" && ( "${ARCH}" == "amd64" || "${ARCH}" == "arm64" ) ]]; then
  mkdir -p "${RELEASE_ROOT}/hostagent/bin" "${RELEASE_ROOT}/hostagent/config"
fi

copy_binary() {
  local name="$1"
  local target_dir="$2"
  local source_name="${name}"
  [[ "${OS}" == "windows" ]] && source_name="${source_name}.exe"
  cp "${ROOT}/bin/${source_name}" "${target_dir}/"
}

copy_binary moox-cli "${RELEASE_ROOT}/cli/bin"
copy_binary moox-admin "${RELEASE_ROOT}/admin/bin"
copy_binary moox-admin-cli "${RELEASE_ROOT}/admin/bin"
copy_binary moox-gateway "${RELEASE_ROOT}/gateway/bin"
copy_binary moox-gateway-cli "${RELEASE_ROOT}/gateway/bin"
copy_binary moox-eventbus "${RELEASE_ROOT}/eventbus/bin"
copy_binary moox-web-host "${RELEASE_ROOT}/web-host/bin"
copy_binary moox-cloudnode "${RELEASE_ROOT}/cloudnode/bin"
copy_binary moox-cloudnode-cli "${RELEASE_ROOT}/cloudnode/bin"
copy_binary moox-collector "${RELEASE_ROOT}/collector/bin"
copy_binary moox-collector-cli "${RELEASE_ROOT}/collector/bin"
copy_binary moox-factor "${RELEASE_ROOT}/factor/bin"
copy_binary moox-factor-cli "${RELEASE_ROOT}/factor/bin"
copy_binary moox-strategy "${RELEASE_ROOT}/strategy/bin"
copy_binary moox-strategy-cli "${RELEASE_ROOT}/strategy/bin"
copy_binary moox-trade "${RELEASE_ROOT}/trade/bin"
copy_binary moox-trade-cli "${RELEASE_ROOT}/trade/bin"
copy_binary moox-monitor "${RELEASE_ROOT}/monitor/bin"
copy_binary moox-monitor-cli "${RELEASE_ROOT}/monitor/bin"
storage_binary_name() {
  if [[ "${OS}" == "windows" ]]; then
    printf '%s.exe' "$1"
  else
    printf '%s' "$1"
  fi
}

cp "${ROOT}/bin/$(storage_binary_name moox-storage-primary)" "${RELEASE_ROOT}/storage-primary/bin/$(storage_binary_name moox-storage-primary)"
cp "${ROOT}/bin/$(storage_binary_name moox-storage-view)" "${RELEASE_ROOT}/storage-view/bin/$(storage_binary_name moox-storage-view)"
cp "${ROOT}/bin/$(storage_binary_name moox-storage-cli)" "${RELEASE_ROOT}/storage-primary/bin/$(storage_binary_name moox-storage-primary-cli)"
copy_binary moox-archive "${RELEASE_ROOT}/archive/bin"
copy_binary moox-archive-cli "${RELEASE_ROOT}/archive/bin"
if [[ -d "${RELEASE_ROOT}/hostagent" ]]; then
  copy_binary moox-host-agent "${RELEASE_ROOT}/hostagent/bin"
  copy_binary moox-host-agent-cli "${RELEASE_ROOT}/hostagent/bin"
  cp "${ROOT}/modules/hostagent/config/app.yaml" "${RELEASE_ROOT}/hostagent/config/app.example.yaml"
  cp "${ROOT}/modules/hostagent/config/eventbus.example.yaml" "${RELEASE_ROOT}/hostagent/config/eventbus.example.yaml"
  cp "${ROOT}/modules/hostagent/config/trpc_go.yaml" "${RELEASE_ROOT}/hostagent/config/trpc_go.yaml"
  grep -q 'trpc.moox.hostagent.sample.timer' "${RELEASE_ROOT}/hostagent/config/trpc_go.yaml" || { echo "missing HostAgent sample Timer schedule" >&2; exit 1; }
fi

cp -R "${ROOT}/modules/admin/config/." "${RELEASE_ROOT}/admin/config/"
cp -R "${ROOT}/modules/gateway/config/." "${RELEASE_ROOT}/gateway/config/"
[[ -f "${RELEASE_ROOT}/gateway/config/trpc_go.yaml" ]] || { echo "missing Gateway tRPC Timer config" >&2; exit 1; }
if grep -q 'refresh_interval' "${RELEASE_ROOT}/gateway/config/app.yaml"; then
  echo "obsolete Gateway refresh_interval remains in release config" >&2
  exit 1
fi
cp -R "${ROOT}/modules/eventbus/config/." "${RELEASE_ROOT}/eventbus/config/"
cp -R "${ROOT}/modules/cloudnode/config/." "${RELEASE_ROOT}/cloudnode/config/"
cp -R "${ROOT}/modules/collector/config/." "${RELEASE_ROOT}/collector/config/"
cp -R "${ROOT}/modules/factor/config/." "${RELEASE_ROOT}/factor/config/"
cp -R "${ROOT}/modules/factor/factors/." "${RELEASE_ROOT}/factor/factors/"
cp -R "${ROOT}/packages/pyruntime/python/." "${RELEASE_ROOT}/factor/python-runtime/"
cp -R "${ROOT}/modules/strategy/config/." "${RELEASE_ROOT}/strategy/config/"
find "${RELEASE_ROOT}/strategy" -type d \( -name __pycache__ -o -name .pytest_cache \) -prune -exec rm -rf {} +
find "${RELEASE_ROOT}/strategy" -type f \( -name '*.pyc' -o -name '*.sqlite' -o -name '*.db' \) -delete
cp -R "${ROOT}/modules/trade/config/." "${RELEASE_ROOT}/trade/config/"
cp -R "${ROOT}/modules/factor/pyworker" "${RELEASE_ROOT}/factor/pyworker"
find "${RELEASE_ROOT}/factor" -type d \( -name __pycache__ -o -name .pytest_cache \) -prune -exec rm -rf {} +
find "${RELEASE_ROOT}/factor" -type f -name '*.pyc' -delete
cp "${ROOT}/modules/storage/config/trpc_go.primary.yaml" "${RELEASE_ROOT}/storage-primary/config/trpc_go.yaml"
printf '\n' >> "${RELEASE_ROOT}/storage-primary/config/trpc_go.yaml"
cat "${ROOT}/modules/storage/config/storage.primary.yaml" >> "${RELEASE_ROOT}/storage-primary/config/trpc_go.yaml"
cp "${ROOT}/modules/storage/config/storage_view/trpc_go.yaml" "${RELEASE_ROOT}/storage-view/config/trpc_go.yaml"
cp -R "${ROOT}/modules/monitor/config/." "${RELEASE_ROOT}/monitor/config/"
cp -R "${ROOT}/modules/storage/schema/." "${RELEASE_ROOT}/storage-primary/schema/"
cp -R "${ROOT}/modules/storage/schema/." "${RELEASE_ROOT}/storage-view/schema/"
cp -R "${ROOT}/modules/archive/config/." "${RELEASE_ROOT}/archive/config/"
cp "${ROOT}/scripts/storage-start.sh" "${RELEASE_ROOT}/storage-primary/start.sh"
cp "${ROOT}/scripts/storage-stop.sh" "${RELEASE_ROOT}/storage-primary/stop.sh"
cp "${ROOT}/scripts/storage-start.sh" "${RELEASE_ROOT}/storage-view/start.sh"
cp "${ROOT}/scripts/storage-stop.sh" "${RELEASE_ROOT}/storage-view/stop.sh"
cp -R "${ROOT}/examples/." "${RELEASE_ROOT}/examples/"
cp "${ROOT}/examples/setup/default/dataset-health-policy.yaml" "${RELEASE_ROOT}/config/dataset-health-policy.yaml"
cp "${ROOT}/modules/cli/config/cli.yaml" "${RELEASE_ROOT}/config/cli.yaml"
cp "${ROOT}/packages/doctor/components.yaml" "${RELEASE_ROOT}/config/doctor/components.yaml"
shasum -a 256 "${RELEASE_ROOT}/config/doctor/components.yaml" | awk '{print "sha256:" $1}' > "${RELEASE_ROOT}/config/doctor/components.yaml.sha256"
cp "${ROOT}/packages/doctor/report.schema.json" "${RELEASE_ROOT}/config/doctor/report.schema.json"
cp -R "${ROOT}/docs/." "${RELEASE_ROOT}/docs/" 2>/dev/null || true
chmod +x "${RELEASE_ROOT}/storage-primary/start.sh" "${RELEASE_ROOT}/storage-primary/stop.sh" "${RELEASE_ROOT}/storage-view/start.sh" "${RELEASE_ROOT}/storage-view/stop.sh"
cp "${ROOT}/README.md" "${RELEASE_ROOT}/README.md" 2>/dev/null || true
cp "${ROOT}/scripts/lib/caddy-managed.sh" "${RELEASE_ROOT}/lib/caddy-managed.sh"
cp "${ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt" "${RELEASE_ROOT}/lib/caddy-v2.11.4-checksums.txt"
cp "${ROOT}/deploy/caddy/Caddyfile" "${RELEASE_ROOT}/config/caddy/Caddyfile"
cp "${ROOT}/deploy/caddy/Caddyfile.no-admin" "${RELEASE_ROOT}/config/caddy/Caddyfile.no-admin"
cp "${ROOT}/deploy/caddy/Caddyfile.public" "${RELEASE_ROOT}/config/caddy/Caddyfile.public"
cp "${ROOT}/deploy/caddy/Caddyfile.public.no-admin" "${RELEASE_ROOT}/config/caddy/Caddyfile.public.no-admin"
chmod +x "${RELEASE_ROOT}/lib/caddy-managed.sh"

tar -C "${ROOT}/release" -czf "${ARCHIVE}" "$(basename "${RELEASE_ROOT}")"

write_storage_release_manifest() {
  [[ "${OS}" == "linux" && "${ARCH}" == "amd64" ]] || return 0
  [[ "${VERSION}" =~ ^[0-9a-fA-F]{40}$ ]] || return 0
  local digest
  if command -v sha256sum >/dev/null 2>&1; then
    digest="$(sha256sum "${ARCHIVE}" | awk '{print $1}')"
  else
    digest="$(shasum -a 256 "${ARCHIVE}" | awk '{print $1}')"
  fi
  local version_lower
  version_lower="$(printf '%s' "${VERSION}" | tr '[:upper:]' '[:lower:]')"
  mkdir -p "${ROOT}/artifacts"
  {
    printf 'schema_version=1\ncommit=%s\narchive=release/%s\narchive_sha256=%s\n' \
      "${version_lower}" "$(basename "${ARCHIVE}")" "${digest}"
    for binary in moox-storage-primary moox-storage-node moox-storage-view; do
      local path
      case "${binary}" in
        moox-storage-primary) path="${RELEASE_ROOT}/storage-primary/bin/${binary}" ;;
        moox-storage-node) path="${ROOT}/bin/${binary}" ;;
        moox-storage-view) path="${RELEASE_ROOT}/storage-view/bin/${binary}" ;;
      esac
      if command -v sha256sum >/dev/null 2>&1; then
        printf '%s=%s\n' "${binary}" "$(sha256sum "${path}" | awk '{print $1}')"
      else
        printf '%s=%s\n' "${binary}" "$(shasum -a 256 "${path}" | awk '{print $1}')"
      fi
    done
  } >"${ROOT}/artifacts/storage-datanode-release-sha256.txt"
}

write_storage_release_manifest
echo "==> release package: ${ARCHIVE}"
