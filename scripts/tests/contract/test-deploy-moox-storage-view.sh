#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
if [[ "$(basename "${SCRIPT_DIR}")" == "contract" ]]; then
  ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd -P)"
else
  ROOT="$(cd "${SCRIPT_DIR}/.." && pwd -P)"
fi
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-deploy-storage-view.XXXXXX")"
DEPLOY_DIR="${TMP_ROOT}/deploy"
STAGE_DIR="${TMP_ROOT}/stage"
CREATED_BINARIES=()

cleanup() {
  rm -rf "${TMP_ROOT}"
  for path in "${CREATED_BINARIES[@]+"${CREATED_BINARIES[@]}"}"; do
    rm -f "${path}"
  done
}
trap cleanup EXIT

ensure_required_binary() {
  local name="$1"
  local path="${ROOT}/bin/${name}"
  if [[ -x "${path}" ]]; then
    return
  fi
  mkdir -p "${ROOT}/bin"
  cat >"${path}" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == random-secret ]]; then printf '{"status":"ok","bytes":32,"secret":"%064d"}\n' 0; fi
exit 0
EOF
  chmod +x "${path}"
  CREATED_BINARIES+=("${path}")
}

assert_file() {
  local path="$1"
  if [[ ! -e "${path}" ]]; then
    echo "expected file to exist: ${path}" >&2
    exit 1
  fi
}

assert_missing() {
	local path="$1"
	if [[ -e "${path}" ]]; then
		echo "expected path to be removed: ${path}" >&2
		exit 1
	fi
}

assert_grep() {
  local pattern="$1"
  local path="$2"
  if ! grep -Eq "${pattern}" "${path}"; then
    echo "expected ${path} to match: ${pattern}" >&2
    exit 1
  fi
}

assert_order() {
  local path="$1"
  shift
  local previous=0
  local pattern line
  for pattern in "$@"; do
    line="$(grep -n -m1 -E "${pattern}" "${path}" | cut -d: -f1)"
    if [[ -z "${line}" || "${line}" -le "${previous}" ]]; then
      echo "expected ${path} pattern in order after line ${previous}: ${pattern}" >&2
      exit 1
    fi
    previous="${line}"
  done
}

ensure_required_binary moox-admin
ensure_required_binary moox-admin-cli
ensure_required_binary moox-cli
ensure_required_binary moox-eventbus
ensure_required_binary moox-archive
ensure_required_binary moox-archive-cli
ensure_required_binary moox-gateway
ensure_required_binary moox-gateway-cli
ensure_required_binary moox-storage-primary
ensure_required_binary moox-storage-view
ensure_required_binary moox-storage-node
ensure_required_binary moox-storage-cli

HEALTH_SECRET_CLI="${TMP_ROOT}/moox-admin-cli"
cat >"${HEALTH_SECRET_CLI}" <<'EOF'
#!/usr/bin/env bash
printf '{"status":"ok","bytes":32,"secret":"%064d"}\n' 0
EOF
chmod +x "${HEALTH_SECRET_CLI}"
export MOOX_HEALTH_SECRET_CLI="${HEALTH_SECRET_CLI}"
export HOME="${TMP_ROOT}/home"
mkdir -p "${HOME}/.config/moox/credentials" "${DEPLOY_DIR}/data"
export MOOX_STORAGE_VIEW_MAINTENANCE_POLICY_B64="$(printf '%s' '{"maintenance_check_interval":"1m","rebuild_lookback_periods":1000,"max_periods_per_series":2000,"max_view_file_bytes":1073741824,"system_monitor":{"max_periods_per_series":2000},"views":[]}' | base64 | tr -d '\n')"
printf 'fixture-encryption-key' >"${HOME}/.config/moox/credentials/admin-encryption-key"
chmod 0600 "${HOME}/.config/moox/credentials/admin-encryption-key"
: >"${DEPLOY_DIR}/data/admin.db"

printf 'control-secret' >"${TMP_ROOT}/control.key"
printf 'service-secret' >"${TMP_ROOT}/service.key"
chmod 0600 "${TMP_ROOT}/control.key" "${TMP_ROOT}/service.key"
openssl req -x509 -newkey rsa:2048 -nodes -subj /CN=storage-view-one \
  -keyout "${TMP_ROOT}/ca-one.key" -out "${TMP_ROOT}/ca-one.crt" -days 1 >/dev/null 2>&1
openssl req -x509 -newkey rsa:2048 -nodes -subj /CN=storage-view-two \
  -keyout "${TMP_ROOT}/ca-two.key" -out "${TMP_ROOT}/ca-two.crt" -days 1 >/dev/null 2>&1
cat "${TMP_ROOT}/ca-one.crt" "${TMP_ROOT}/ca-two.crt" >"${TMP_ROOT}/peers.pem"
GATEWAY_ARGS=(
  --node-id storage-view
  --gateway-control-url http://127.0.0.1:11000
  --gateway-ca-bundle "${TMP_ROOT}/peers.pem"
  --gateway-control-key-file "${TMP_ROOT}/control.key"
  --gateway-service-key-file "${TMP_ROOT}/service.key"
)

MOOX_EVENTBUS_ENABLE_TLS=1 MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT=256MB "${ROOT}/scripts/deploy-moox.sh" \
  --target localhost \
  --dir "${DEPLOY_DIR}" \
  --stage "${STAGE_DIR}" \
  --skip-build \
  --with-storage-node \
  --no-start \
  --no-web-host \
  --no-cloudnode \
  --no-collector \
  --no-factor --no-strategy --no-trade \
  --no-monitor \
  "${GATEWAY_ARGS[@]}" >/dev/null

for binary in moox-storage-primary moox-storage-view moox-storage-node; do
  assert_file "${DEPLOY_DIR}/bin/${binary}"
done
assert_file "${DEPLOY_DIR}/bin/moox-storage-cli"
assert_file "${DEPLOY_DIR}/bin/moox-cli"
for binary in moox-storage-view-index moox-storage-view-builder moox-storage-view-query; do
  assert_missing "${DEPLOY_DIR}/bin/${binary}"
done

assert_file "${DEPLOY_DIR}/secrets/health-auth.env"
assert_file "${DEPLOY_DIR}/secrets/storage-node-auth.env"
assert_file "${DEPLOY_DIR}/secrets/storage-internal-auth.env"
[[ $(stat -f '%Lp' "${DEPLOY_DIR}/secrets/storage-node-auth.env" 2>/dev/null || stat -c '%a' "${DEPLOY_DIR}/secrets/storage-node-auth.env") == 600 ]] || { echo 'storage node auth mode must be 0600' >&2; exit 1; }
[[ $(stat -f '%Lp' "${DEPLOY_DIR}/secrets/storage-internal-auth.env" 2>/dev/null || stat -c '%a' "${DEPLOY_DIR}/secrets/storage-internal-auth.env") == 600 ]] || { echo 'storage internal auth mode must be 0600' >&2; exit 1; }
assert_grep '^MOOX_STORAGE_NODE_AUTH_SECRET=[0-9a-f]{64}$' "${DEPLOY_DIR}/secrets/storage-node-auth.env"
assert_grep '^MOOX_STORAGE_PRIMARY_AUTH_SECRET=[0-9a-f]{64}$' "${DEPLOY_DIR}/secrets/storage-internal-auth.env"
assert_grep '^MOOX_STORAGE_VIEW_AUTH_SECRET=[0-9a-f]{64}$' "${DEPLOY_DIR}/secrets/storage-internal-auth.env"
[[ $(stat -f '%Lp' "${DEPLOY_DIR}/secrets/health-auth.env" 2>/dev/null || stat -c '%a' "${DEPLOY_DIR}/secrets/health-auth.env") == 600 ]] || { echo 'health auth mode must be 0600' >&2; exit 1; }
assert_grep '^MOOX_HEALTH_AUTH_VERSION=moox-health-v1$' "${DEPLOY_DIR}/secrets/health-auth.env"
assert_grep '^MOOX_HEALTH_AUTH_SECRET_KEY=[0-9a-f]{64}$' "${DEPLOY_DIR}/secrets/health-auth.env"
secret_before=$(cat "${DEPLOY_DIR}/secrets/health-auth.env")
MOOX_EVENTBUS_ENABLE_TLS=1 MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT=256MB "${ROOT}/scripts/deploy-moox.sh" --target localhost --dir "${DEPLOY_DIR}" --stage "${STAGE_DIR}" --skip-build --with-storage-node --no-start --no-web-host --no-cloudnode --no-collector --no-factor --no-strategy --no-trade --no-monitor "${GATEWAY_ARGS[@]}" >/dev/null
[[ $(cat "${DEPLOY_DIR}/secrets/health-auth.env") == "${secret_before}" ]] || { echo 'health auth secret changed on redeploy' >&2; exit 1; }
assert_grep 'source "\$\{ROOT\}/secrets/health-auth.env"' "${DEPLOY_DIR}/start.sh"
assert_grep 'source "\$\{ROOT\}/secrets/health-auth.env"' "${DEPLOY_DIR}/healthcheck.sh"
assert_grep 'sign_health_request' "${DEPLOY_DIR}/healthcheck.sh"
for mapping in \
  'admin.*127\.0\.0\.1:11010/healthz' \
  'collector.*127\.0\.0\.1:11412/healthz' \
  'factor.*127\.0\.0\.1:11414/healthz' \
  'monitor.*127\.0\.0\.1:11409/healthz' \
  'cloudnode.*127\.0\.0\.1:11411/healthz' \
  'archive.*127\.0\.0\.1:11416/healthz' \
  'eventbus.*127\.0\.0\.1:11419/healthz' \
  'web-host.*127\.0\.0\.1:19527/healthz'; do
  assert_grep "${mapping}" "${DEPLOY_DIR}/healthcheck.sh"
done
assert_grep 'health probe failed' "${DEPLOY_DIR}/healthcheck.sh"
assert_grep 'health-failures' "${DEPLOY_DIR}/healthcheck.sh"
assert_grep 'holding restart' "${DEPLOY_DIR}/healthcheck.sh"
assert_grep 'MOOX_HEALTHCHECK_FAILURE_THRESHOLD' "${DEPLOY_DIR}/healthcheck.sh"
assert_grep 'storage-view.*127\.0\.0\.1:20211/readyz' "${DEPLOY_DIR}/healthcheck.sh"
assert_grep 'url=http://127\.0\.0\.1:20211/healthz' "${DEPLOY_DIR}/healthcheck.sh"
assert_grep '"consumer_bound"' "${DEPLOY_DIR}/healthcheck.sh"
assert_grep '/healthz is the liveness endpoint' "${DEPLOY_DIR}/healthcheck.sh"
assert_grep 'process is live but readiness is degraded; leaving it running' "${DEPLOY_DIR}/healthcheck.sh"
assert_grep 'maintenance.lock' "${ROOT}/scripts/moox-storage-view-watchdog.sh"
assert_grep 'start\.sh" storage-view 8>&- 9>&-' "${ROOT}/scripts/moox-storage-view-watchdog.sh"
assert_grep '"\$\{ROOT\}/stop\.sh" "\$\{name\}"' "${DEPLOY_DIR}/healthcheck.sh"
assert_grep '"\$\{ROOT\}/start\.sh" "\$\{name\}" 9>&-' "${DEPLOY_DIR}/healthcheck.sh"

assert_grep 'start_storage_process "storage-primary" "moox-storage-primary"' "${DEPLOY_DIR}/start.sh"
assert_grep 'source "\$\{ROOT\}/secrets/storage-node-auth.env"' "${DEPLOY_DIR}/start.sh"
assert_grep 'source "\$\{ROOT\}/secrets/storage-internal-auth.env"' "${DEPLOY_DIR}/start.sh"
assert_grep 'register-node' "${DEPLOY_DIR}/start.sh"
if grep -q 'import-seed' "${DEPLOY_DIR}/start.sh"; then
  echo 'storage deployment must not import a second metadata seed before setup init' >&2
  exit 1
fi
assert_grep 'doctor bootstrap --format json' "${DEPLOY_DIR}/start.sh"
assert_grep 'activate-datasets' "${DEPLOY_DIR}/start.sh"
assert_file "${DEPLOY_DIR}/secrets/storage-node-auth.env"
assert_grep '^MOOX_STORAGE_NODE_AUTH_SECRET=[0-9a-f]{64}$' "${DEPLOY_DIR}/secrets/storage-node-auth.env"
if grep -Eq 'MOOX_(METRICS|HOST)_STORAGE_ROUTE_SEED' "${ROOT}/scripts/deploy-moox.sh"; then
  echo "storage deployment must not depend on route seed environment variables" >&2
  exit 1
fi
assert_grep 'conf=config/trpc_go\.yaml' "${DEPLOY_DIR}/start.sh"
assert_grep 'start_storage_view' "${DEPLOY_DIR}/start.sh"
assert_grep 'start_service "storage-view" "\$\{ROOT\}/storage-view"' "${DEPLOY_DIR}/start.sh"
assert_grep 'MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT=\$\{MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT:-256MB\}' "${DEPLOY_DIR}/start.sh"
if grep -q 'MOOX_STORAGE_VIEW_REBUILD_LOOKBACK_PERIODS' "${DEPLOY_DIR}/start.sh"; then
  echo "legacy Storage View lookback environment remains in package" >&2
  exit 1
fi
if grep -q 'MOOX_STORAGE_VIEW_DELIVER_POLICY' "${DEPLOY_DIR}/start.sh"; then
  echo "unexpected global Storage View deliver policy override" >&2
  exit 1
fi
assert_grep '^    maintenance_check_interval: 1m$' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
assert_grep '^    rebuild_max_pending: 32$' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
assert_grep '^    rebuild_idle_checks: 3$' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
assert_file "${DEPLOY_DIR}/storage-view/config/maintenance.json"
assert_grep '"max_periods_per_series": 2000' "${DEPLOY_DIR}/storage-view/config/maintenance.json"
assert_grep 'maintenance_policy_file: config/maintenance.json' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
assert_grep 'name: trpc.moox.storage.view.cleanup.timer' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
assert_grep 'port: 20308' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
assert_grep 'network: "\*/30 \* \* \* \* \*"' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
assert_grep 'timeout: 20000' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
for durable in storage_view_kline_v2 storage_view_metrics_v2 storage_view_other_v2; do
  assert_grep "durable: ${durable}" "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
done
if grep -q 'storage_view_period_v1' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"; then
  echo "legacy Storage View durable remains in package" >&2
  exit 1
fi
assert_grep 'MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT=\$\{quoted_storage_view_duckdb_memory_limit\}' "${ROOT}/scripts/deploy-moox.sh"
assert_grep '^MOOX_INSTALLED_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT=256MB$' "${DEPLOY_DIR}/config/components.env"
if grep -Eq 'MOOX_STORAGE_VIEW_REBUILD_LOOKBACK_PERIODS|MOOX_INSTALLED_STORAGE_VIEW_REBUILD_LOOKBACK_PERIODS' "${DEPLOY_DIR}/config/components.env"; then
  echo "legacy Storage View lookback settings remain in components.env" >&2
  exit 1
fi
assert_grep '^MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT=\$\{MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT:-\$\{MOOX_INSTALLED_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT\}\}$' "${DEPLOY_DIR}/config/components.env"
assert_grep 'storage_internal_auth_mismatch_preflight' "${ROOT}/scripts/deploy-moox.sh"
assert_grep 'check_storage_auth_files' "${DEPLOY_DIR}/healthcheck.sh"
if grep -Eq 'wait_eventbus_storage_view_topology|storage_view durable topology' "${DEPLOY_DIR}/start.sh"; then
  echo "storage-view must create and own its Consumer instead of waiting for EventBus topology" >&2
  exit 1
fi
assert_grep '"\$\{ROOT\}/bin/moox-storage-view"' "${DEPLOY_DIR}/start.sh"
assert_grep 'conf=config/trpc_go\.yaml' "${DEPLOY_DIR}/start.sh"
if grep -Eq 'storage-view-(index|builder|query)|storage\.view_(index|builder|query)\.yaml' "${DEPLOY_DIR}/start.sh"; then
  echo "split View processes must not remain in start.sh" >&2
  exit 1
fi

assert_order "${DEPLOY_DIR}/start.sh" \
  '^  start_storage_primary$' \
  '^  start_storage_node$' \
  'wait_tcp 127\.0\.0\.1 20107' \
  '^  register_storage_node$' \
  '^  start_storage_view$' \
  '^  wait_storage_view_live' \
  '^  if run_storage_doctor; then$' \
  '^    activate_storage_datasets$'
doctor_body="$(awk '/^run_storage_doctor\(\) \{/{capture=1} capture{print} capture && /^}/{exit}' "${DEPLOY_DIR}/start.sh")"
grep -q 'doctor bootstrap --format json' <<<"${doctor_body}"
grep -q 'cd "${ROOT}"' <<<"${doctor_body}"
if grep -q 'activate-datasets' <<<"${doctor_body}"; then
  echo 'ordinary Doctor path must not activate Datasets' >&2
  exit 1
fi
assert_order "${DEPLOY_DIR}/stop.sh" \
  'stop_service "storage-view"' \
  'stop_service "storage-primary"'

assert_file "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
assert_file "${DEPLOY_DIR}/archive/config/app.yaml"
assert_grep 'credential_file: ~/.config/moox/eventbus/archive-eventbus.yaml' "${DEPLOY_DIR}/archive/config/app.yaml"
assert_grep 'credential_file: ~/.config/moox/eventbus/storage-eventbus.yaml' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
assert_grep 'credential_file: ~/.config/moox/eventbus/storage-eventbus.yaml' "${DEPLOY_DIR}/storage-node/config/storage.yaml"
[[ $(find "${DEPLOY_DIR}/storage-view/config" -type f -name '*.yaml' | wc -l | tr -d ' ') == 1 ]] || {
  echo "storage-view must read exactly one YAML config" >&2
  exit 1
}
assert_grep 'root: \.\./data/storage' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
assert_grep 'path: \.\./data/storage/metadata/storage_metadata\.db' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
assert_grep 'view_index_root: \.\./data/storage/view-indexes' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
assert_grep '^    - view$' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
if grep -ERq 'duckdb_path:|bleve_path:|rotation:|op=rotate' "${DEPLOY_DIR}/storage" "${DEPLOY_DIR}/storage-view"; then
  echo "legacy View index configuration remains in deployment package" >&2
  exit 1
fi

assert_grep 'enabled: true' "${DEPLOY_DIR}/storage/config/trpc_go.yaml"
assert_grep 'log_path: \.\./logs/storage-primary' "${DEPLOY_DIR}/storage/config/trpc_go.yaml"
assert_grep 'log_path: \.\./logs/storage-view' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
assert_grep 'port: 20104' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
assert_grep 'port: 20202' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
if grep -Eq 'MOOX_(METRICS|HOST)_STORAGE_ROUTE_SEED' "${ROOT}/scripts/deploy-moox.sh"; then
  echo 'legacy storage route seed environment remains in deployment script' >&2
  exit 1
fi

assert_file "${DEPLOY_DIR}/reset-storage-view-indexes.sh"
RESET_ROOT="${TMP_ROOT}/reset-storage"
mkdir -p \
	"${RESET_ROOT}/duckdb" \
	"${RESET_ROOT}/bleve" \
	"${RESET_ROOT}/view-indexes" \
	"${RESET_ROOT}/metadata" \
	"${RESET_ROOT}/pebble"
touch \
	"${RESET_ROOT}/duckdb/views.duckdb" \
	"${RESET_ROOT}/duckdb/views.duckdb.wal" \
	"${RESET_ROOT}/duckdb/views.duckdb.extra" \
	"${RESET_ROOT}/bleve/index" \
	"${RESET_ROOT}/view-indexes/index" \
	"${RESET_ROOT}/metadata/storage_metadata.db" \
	"${RESET_ROOT}/pebble/PRIMARY_STORE"

if "${DEPLOY_DIR}/reset-storage-view-indexes.sh" --root "${RESET_ROOT}" >/dev/null 2>&1; then
	echo "reset script must require --yes" >&2
	exit 1
fi
"${DEPLOY_DIR}/reset-storage-view-indexes.sh" --root "${RESET_ROOT}" --yes >/dev/null
assert_missing "${RESET_ROOT}/duckdb/views.duckdb"
assert_missing "${RESET_ROOT}/duckdb/views.duckdb.wal"
assert_missing "${RESET_ROOT}/duckdb/views.duckdb.extra"
assert_missing "${RESET_ROOT}/bleve"
assert_missing "${RESET_ROOT}/view-indexes"
assert_file "${RESET_ROOT}/metadata/storage_metadata.db"
assert_file "${RESET_ROOT}/pebble/PRIMARY_STORE"

mkdir -p "${RESET_ROOT}/view-indexes"
touch "${RESET_ROOT}/view-indexes/index" "${RESET_ROOT}/metadata/storage_metadata.db-wal"
"${DEPLOY_DIR}/reset-storage-view-indexes.sh" --root "${RESET_ROOT}" --metadata --yes >/dev/null
assert_missing "${RESET_ROOT}/view-indexes"
assert_missing "${RESET_ROOT}/metadata/storage_metadata.db"
assert_missing "${RESET_ROOT}/metadata/storage_metadata.db-wal"
assert_file "${RESET_ROOT}/pebble/PRIMARY_STORE"

ln -s / "${TMP_ROOT}/unsafe-storage-root"
if "${DEPLOY_DIR}/reset-storage-view-indexes.sh" --root "${TMP_ROOT}/unsafe-storage-root" --yes >/dev/null 2>&1; then
	echo "reset script must reject a storage root symlink resolving to /" >&2
	exit 1
fi

# A storage package deployed with a different internal-auth pair must fail
# before it stops the existing services. This is the release gate for the
# browser-facing Admin BFF -> Storage View HMAC contract.
GUARD_DEPLOY_DIR="${TMP_ROOT}/storage"
cp -a "${DEPLOY_DIR}" "${GUARD_DEPLOY_DIR}"
CONTROL_AUTH_ROOT="${TMP_ROOT}/control-root"
mkdir -p "${CONTROL_AUTH_ROOT}/secrets"
printf 'MOOX_STORAGE_PRIMARY_AUTH_SECRET=%064x\nMOOX_STORAGE_VIEW_AUTH_SECRET=%064x\n' 1 2 \
  >"${CONTROL_AUTH_ROOT}/secrets/storage-internal-auth.env"
chmod 600 "${CONTROL_AUTH_ROOT}/secrets/storage-internal-auth.env"
guard_auth_before="$(cat "${GUARD_DEPLOY_DIR}/secrets/storage-internal-auth.env")"
if MOOX_CONTROL_ROOT="${CONTROL_AUTH_ROOT}" \
  MOOX_STORAGE_PRIMARY_AUTH_SECRET="$(printf '%064x' 3)" \
  MOOX_STORAGE_VIEW_AUTH_SECRET="$(printf '%064x' 4)" \
  MOOX_STORAGE_NODE_AUTH_SECRET="$(printf '%064x' 5)" \
  MOOX_EVENTBUS_ENABLE_TLS=1 MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT=256MB \
  "${ROOT}/scripts/deploy-moox.sh" --target localhost --dir "${GUARD_DEPLOY_DIR}" --stage "${STAGE_DIR}" \
  --skip-build --with-storage-node --no-start --no-admin --no-web-host --no-cloudnode --no-collector \
  --no-factor --no-strategy --no-trade --no-monitor "${GATEWAY_ARGS[@]}" >/dev/null 2>&1; then
  echo "storage auth mismatch must fail before a local storage deployment mutates" >&2
  exit 1
fi
[[ "$(cat "${GUARD_DEPLOY_DIR}/secrets/storage-internal-auth.env")" == "${guard_auth_before}" ]] || {
  echo "storage auth preflight changed the existing deployment" >&2
  exit 1
}

echo "storage-primary plus unified storage-view deployment package verified"
