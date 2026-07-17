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

build_web_assets
TARGET_GOOS="${OS}" TARGET_GOARCH="${ARCH}" "${ROOT}/scripts/build.sh"

validate_monitor_metadata_seeds() {
  local seed
  for seed in \
    "${ROOT}/examples/platform-local.seed.yaml" \
    "${ROOT}/examples/metadata-monitor-metrics.seed.yaml" \
    "${ROOT}/examples/metadata-monitor-metrics-local-route.seed.yaml" \
    "${ROOT}/examples/metadata-monitor-host.seed.yaml" \
    "${ROOT}/examples/metadata-monitor-host-local-route.seed.yaml"; do
    [[ -s "${seed}" ]] || {
      echo "missing metadata seed: ${seed}" >&2
      exit 1
    }
  done
  # Validate the release contract without contacting Storage. Runtime startup
  # performs the real create-or-verify apply after MetadataService is ready.
  # Use a host-built CLI here: the release binary may be cross-compiled for
  # Linux/arm64 and cannot be executed on the packaging workstation.
  (cd "${ROOT}" && go run ./modules/cli/cmd/moox-cli metadata apply --file "${ROOT}/examples/metadata-monitor-metrics.seed.yaml" --dry-run >/dev/null)
  (cd "${ROOT}" && go run ./modules/cli/cmd/moox-cli metadata apply --file "${ROOT}/examples/metadata-monitor-metrics-local-route.seed.yaml" --dry-run >/dev/null)
  (cd "${ROOT}" && go run ./modules/cli/cmd/moox-cli metadata apply --file "${ROOT}/examples/metadata-monitor-host.seed.yaml" --dry-run >/dev/null)
  (cd "${ROOT}" && go run ./modules/cli/cmd/moox-cli metadata apply --file "${ROOT}/examples/metadata-monitor-host-local-route.seed.yaml" --dry-run >/dev/null)
  grep -q 'host_storage:' "${ROOT}/modules/monitor/config/app.yaml"
  grep -q 'retention: 72h' "${ROOT}/modules/monitor/config/app.yaml"
  ! grep -q '^primary_store_routes:' "${ROOT}/examples/metadata-monitor-host.seed.yaml"
  grep -q '^primary_store_routes:' "${ROOT}/examples/metadata-monitor-host-local-route.seed.yaml"
  for dataset in host_resource_v1 host_fs_v1 host_disk_v1 host_net_v1; do
    grep -q "dataset_id: ${dataset}" "${ROOT}/examples/metadata-monitor-host.seed.yaml"
    grep -q "dataset_id: ${dataset}" "${ROOT}/examples/metadata-monitor-host-local-route.seed.yaml"
  done
}

validate_monitor_metadata_seeds

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
  "${RELEASE_ROOT}/factor/sections" \
  "${RELEASE_ROOT}/strategy/bin" \
  "${RELEASE_ROOT}/strategy/config" \
  "${RELEASE_ROOT}/strategy/pyworker" \
  "${RELEASE_ROOT}/strategy/python-runtime" \
  "${RELEASE_ROOT}/strategy/pysdk" \
  "${RELEASE_ROOT}/strategy/strategies/example" \
  "${RELEASE_ROOT}/trade/bin" \
  "${RELEASE_ROOT}/trade/config" \
  "${RELEASE_ROOT}/monitor/bin" \
  "${RELEASE_ROOT}/monitor/config" \
  "${RELEASE_ROOT}/storage/bin" \
  "${RELEASE_ROOT}/storage/config" \
  "${RELEASE_ROOT}/storage/schema" \
  "${RELEASE_ROOT}/archive/bin" \
  "${RELEASE_ROOT}/archive/config" \
  "${RELEASE_ROOT}/examples" \
  "${RELEASE_ROOT}/docs"
mkdir -p "${RELEASE_ROOT}/lib" "${RELEASE_ROOT}/config/caddy"
if [[ "${OS}" == "linux" && ( "${ARCH}" == "amd64" || "${ARCH}" == "arm64" ) ]]; then
  mkdir -p "${RELEASE_ROOT}/hostagent/bin" "${RELEASE_ROOT}/hostagent/config"
fi

copy_binary() {
  local name="$1"
  local target_dir="$2"
  cp "${ROOT}/bin/${name}" "${target_dir}/"
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
copy_binary moox-collector-scf "${RELEASE_ROOT}/collector/bin"
copy_binary moox-factor "${RELEASE_ROOT}/factor/bin"
copy_binary moox-factor-cli "${RELEASE_ROOT}/factor/bin"
copy_binary moox-strategy "${RELEASE_ROOT}/strategy/bin"
copy_binary moox-strategy-cli "${RELEASE_ROOT}/strategy/bin"
copy_binary moox-trade "${RELEASE_ROOT}/trade/bin"
copy_binary moox-trade-cli "${RELEASE_ROOT}/trade/bin"
copy_binary moox-monitor "${RELEASE_ROOT}/monitor/bin"
copy_binary moox-monitor-cli "${RELEASE_ROOT}/monitor/bin"
copy_binary moox-storage "${RELEASE_ROOT}/storage/bin"
copy_binary moox-storage-cli "${RELEASE_ROOT}/storage/bin"
copy_binary moox-archive "${RELEASE_ROOT}/archive/bin"
copy_binary moox-archive-cli "${RELEASE_ROOT}/archive/bin"
if [[ -d "${RELEASE_ROOT}/hostagent" ]]; then
  copy_binary moox-host-agent "${RELEASE_ROOT}/hostagent/bin"
  copy_binary moox-host-agent-cli "${RELEASE_ROOT}/hostagent/bin"
  cp "${ROOT}/modules/hostagent/config/app.yaml" "${RELEASE_ROOT}/hostagent/config/app.example.yaml"
  cp "${ROOT}/modules/hostagent/config/eventbus.example.yaml" "${RELEASE_ROOT}/hostagent/config/eventbus.example.yaml"
  cp "${ROOT}/modules/hostagent/config/trpc_go.yaml" "${RELEASE_ROOT}/hostagent/config/trpc_go.yaml"
fi

cp -R "${ROOT}/modules/admin/config/." "${RELEASE_ROOT}/admin/config/"
cp -R "${ROOT}/modules/gateway/config/." "${RELEASE_ROOT}/gateway/config/"
cp -R "${ROOT}/modules/eventbus/config/." "${RELEASE_ROOT}/eventbus/config/"
cp -R "${ROOT}/modules/cloudnode/config/." "${RELEASE_ROOT}/cloudnode/config/"
cp -R "${ROOT}/modules/collector/config/." "${RELEASE_ROOT}/collector/config/"
cp -R "${ROOT}/modules/factor/config/." "${RELEASE_ROOT}/factor/config/"
cp -R "${ROOT}/modules/factor/factors/." "${RELEASE_ROOT}/factor/factors/"
cp -R "${ROOT}/modules/factor/sections/." "${RELEASE_ROOT}/factor/sections/"
cp -R "${ROOT}/modules/strategy/config/." "${RELEASE_ROOT}/strategy/config/"
cp -R "${ROOT}/modules/strategy/pyworker/." "${RELEASE_ROOT}/strategy/pyworker/"
cp -R "${ROOT}/packages/pyruntime/python/." "${RELEASE_ROOT}/strategy/python-runtime/"
cp -R "${ROOT}/modules/strategy/pysdk/." "${RELEASE_ROOT}/strategy/pysdk/"
cp -R "${ROOT}/modules/strategy/strategies/example/." "${RELEASE_ROOT}/strategy/strategies/example/"
find "${RELEASE_ROOT}/strategy" -type d \( -name __pycache__ -o -name .pytest_cache \) -prune -exec rm -rf {} +
find "${RELEASE_ROOT}/strategy" -type f \( -name '*.pyc' -o -name '*.sqlite' -o -name '*.db' \) -delete
cp -R "${ROOT}/modules/trade/config/." "${RELEASE_ROOT}/trade/config/"
cp -R "${ROOT}/modules/factor/pyworker" "${RELEASE_ROOT}/factor/pyworker"
find "${RELEASE_ROOT}/factor/pyworker" -type d -name __pycache__ -prune -exec rm -rf {} +
cp -R "${ROOT}/modules/storage/config/." "${RELEASE_ROOT}/storage/config/"
cp -R "${ROOT}/modules/monitor/config/." "${RELEASE_ROOT}/monitor/config/"
cp -R "${ROOT}/modules/storage/schema/." "${RELEASE_ROOT}/storage/schema/"
cp -R "${ROOT}/modules/archive/config/." "${RELEASE_ROOT}/archive/config/"
cp "${ROOT}/scripts/storage-start.sh" "${RELEASE_ROOT}/storage/start.sh"
cp "${ROOT}/scripts/storage-stop.sh" "${RELEASE_ROOT}/storage/stop.sh"
cp -R "${ROOT}/examples/." "${RELEASE_ROOT}/examples/"
cp -R "${ROOT}/docs/." "${RELEASE_ROOT}/docs/" 2>/dev/null || true
chmod +x "${RELEASE_ROOT}/storage/start.sh" "${RELEASE_ROOT}/storage/stop.sh"
cp "${ROOT}/README.md" "${RELEASE_ROOT}/README.md" 2>/dev/null || true
cp "${ROOT}/scripts/lib/caddy-managed.sh" "${RELEASE_ROOT}/lib/caddy-managed.sh"
cp "${ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt" "${RELEASE_ROOT}/lib/caddy-v2.11.4-checksums.txt"
cp "${ROOT}/deploy/caddy/Caddyfile" "${RELEASE_ROOT}/config/caddy/Caddyfile"
cp "${ROOT}/deploy/caddy/Caddyfile.no-admin" "${RELEASE_ROOT}/config/caddy/Caddyfile.no-admin"
chmod +x "${RELEASE_ROOT}/lib/caddy-managed.sh"

tar -C "${ROOT}/release" -czf "${ARCHIVE}" "$(basename "${RELEASE_ROOT}")"
echo "==> release package: ${ARCHIVE}"
