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
    if [[ ! -d node_modules ]]; then
      CI=true pnpm install --no-frozen-lockfile --config.confirmModulesPurge=false
    fi
    npm run build:prod
  )
  if ! command -v statik >/dev/null 2>&1; then
    go install github.com/rakyll/statik@latest
  fi
  (cd "${ROOT}/web-host" && statik -src=../web/dist -dest=./internal)
}

build_web_assets
TARGET_GOOS="${OS}" TARGET_GOARCH="${ARCH}" "${ROOT}/scripts/build.sh"

validate_metrics_metadata_seeds() {
  local seed
  for seed in \
    "${ROOT}/examples/platform-local.seed.yaml" \
    "${ROOT}/examples/metadata-monitor-metrics.seed.yaml" \
    "${ROOT}/examples/metadata-monitor-metrics-local-route.seed.yaml"; do
    [[ -s "${seed}" ]] || {
      echo "missing metadata seed: ${seed}" >&2
      exit 1
    }
  done
  # Validate the release contract without contacting Storage. Runtime startup
  # performs the real create-or-verify apply after MetadataService is ready.
  "${ROOT}/bin/moox-cli" metadata apply --file "${ROOT}/examples/metadata-monitor-metrics.seed.yaml" --dry-run >/dev/null
  "${ROOT}/bin/moox-cli" metadata apply --file "${ROOT}/examples/metadata-monitor-metrics-local-route.seed.yaml" --dry-run >/dev/null
}

validate_metrics_metadata_seeds

rm -rf "${RELEASE_ROOT}"
mkdir -p \
  "${RELEASE_ROOT}/cli/bin" \
  "${RELEASE_ROOT}/admin/bin" \
  "${RELEASE_ROOT}/admin/config" \
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
  "${RELEASE_ROOT}/trade/bin" \
  "${RELEASE_ROOT}/monitor/bin" \
  "${RELEASE_ROOT}/monitor/config" \
  "${RELEASE_ROOT}/storage/bin" \
  "${RELEASE_ROOT}/storage/config" \
  "${RELEASE_ROOT}/storage/schema" \
  "${RELEASE_ROOT}/examples" \
  "${RELEASE_ROOT}/docs"

copy_binary() {
  local name="$1"
  local target_dir="$2"
  cp "${ROOT}/bin/${name}" "${target_dir}/"
}

copy_binary moox-cli "${RELEASE_ROOT}/cli/bin"
copy_binary moox-admin "${RELEASE_ROOT}/admin/bin"
copy_binary moox-admin-cli "${RELEASE_ROOT}/admin/bin"
copy_binary moox-eventbus "${RELEASE_ROOT}/eventbus/bin"
copy_binary moox-web-host "${RELEASE_ROOT}/web-host/bin"
copy_binary moox-cloudnode "${RELEASE_ROOT}/cloudnode/bin"
copy_binary moox-cloudnode-cli "${RELEASE_ROOT}/cloudnode/bin"
copy_binary moox-collector "${RELEASE_ROOT}/collector/bin"
copy_binary moox-collector-cli "${RELEASE_ROOT}/collector/bin"
copy_binary moox-collector-scf "${RELEASE_ROOT}/collector/bin"
copy_binary moox-factor "${RELEASE_ROOT}/factor/bin"
copy_binary moox-factor-cli "${RELEASE_ROOT}/factor/bin"
copy_binary moox-trade "${RELEASE_ROOT}/trade/bin"
copy_binary moox-trade-cli "${RELEASE_ROOT}/trade/bin"
copy_binary moox-monitor "${RELEASE_ROOT}/monitor/bin"
copy_binary moox-monitor-cli "${RELEASE_ROOT}/monitor/bin"
copy_binary moox-storage "${RELEASE_ROOT}/storage/bin"
copy_binary moox-storage-cli "${RELEASE_ROOT}/storage/bin"

cp -R "${ROOT}/modules/admin/config/." "${RELEASE_ROOT}/admin/config/"
cp -R "${ROOT}/modules/eventbus/config/." "${RELEASE_ROOT}/eventbus/config/"
cp -R "${ROOT}/modules/cloudnode/config/." "${RELEASE_ROOT}/cloudnode/config/"
cp -R "${ROOT}/modules/collector/config/." "${RELEASE_ROOT}/collector/config/"
cp -R "${ROOT}/modules/factor/config/." "${RELEASE_ROOT}/factor/config/"
cp -R "${ROOT}/modules/factor/factors/." "${RELEASE_ROOT}/factor/factors/"
cp -R "${ROOT}/modules/factor/sections/." "${RELEASE_ROOT}/factor/sections/"
cp -R "${ROOT}/modules/factor/pyworker" "${RELEASE_ROOT}/factor/pyworker"
find "${RELEASE_ROOT}/factor/pyworker" -type d -name __pycache__ -prune -exec rm -rf {} +
cp -R "${ROOT}/modules/storage/config/." "${RELEASE_ROOT}/storage/config/"
cp -R "${ROOT}/modules/monitor/config/." "${RELEASE_ROOT}/monitor/config/"
cp -R "${ROOT}/modules/storage/schema/." "${RELEASE_ROOT}/storage/schema/"
cp "${ROOT}/scripts/storage-start.sh" "${RELEASE_ROOT}/storage/start.sh"
cp "${ROOT}/scripts/storage-stop.sh" "${RELEASE_ROOT}/storage/stop.sh"
cp -R "${ROOT}/examples/." "${RELEASE_ROOT}/examples/"
cp -R "${ROOT}/docs/." "${RELEASE_ROOT}/docs/" 2>/dev/null || true
chmod +x "${RELEASE_ROOT}/storage/start.sh" "${RELEASE_ROOT}/storage/stop.sh"
cp "${ROOT}/README.md" "${RELEASE_ROOT}/README.md" 2>/dev/null || true

tar -C "${ROOT}/release" -czf "${ARCHIVE}" "$(basename "${RELEASE_ROOT}")"
echo "==> release package: ${ARCHIVE}"
