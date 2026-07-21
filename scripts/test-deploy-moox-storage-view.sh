#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
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

"${ROOT}/scripts/deploy-moox.sh" \
  --target localhost \
  --dir "${DEPLOY_DIR}" \
  --stage "${STAGE_DIR}" \
  --skip-build \
  --with-storage-node \
  --no-start \
  --no-web-host \
  --no-cloudnode \
  --no-collector \
  --no-factor --no-strategy \
  --no-monitor \
  "${GATEWAY_ARGS[@]}" >/dev/null

for binary in moox-storage-primary moox-storage-view moox-storage-node; do
  assert_file "${DEPLOY_DIR}/bin/${binary}"
done
for binary in moox-storage-view-index moox-storage-view-builder moox-storage-view-query; do
  assert_missing "${DEPLOY_DIR}/bin/${binary}"
done

assert_file "${DEPLOY_DIR}/secrets/health-auth.env"
[[ $(stat -f '%Lp' "${DEPLOY_DIR}/secrets/health-auth.env" 2>/dev/null || stat -c '%a' "${DEPLOY_DIR}/secrets/health-auth.env") == 600 ]] || { echo 'health auth mode must be 0600' >&2; exit 1; }
assert_grep '^MOOX_HEALTH_AUTH_VERSION=moox-health-v1$' "${DEPLOY_DIR}/secrets/health-auth.env"
assert_grep '^MOOX_HEALTH_AUTH_SECRET_KEY=[0-9a-f]{64}$' "${DEPLOY_DIR}/secrets/health-auth.env"
secret_before=$(cat "${DEPLOY_DIR}/secrets/health-auth.env")
"${ROOT}/scripts/deploy-moox.sh" --target localhost --dir "${DEPLOY_DIR}" --stage "${STAGE_DIR}" --skip-build --with-storage-node --no-start --no-web-host --no-cloudnode --no-collector --no-factor --no-strategy --no-monitor "${GATEWAY_ARGS[@]}" >/dev/null
[[ $(cat "${DEPLOY_DIR}/secrets/health-auth.env") == "${secret_before}" ]] || { echo 'health auth secret changed on redeploy' >&2; exit 1; }
assert_grep 'source "\$\{ROOT\}/secrets/health-auth.env"' "${DEPLOY_DIR}/start.sh"
assert_grep 'source "\$\{ROOT\}/secrets/health-auth.env"' "${DEPLOY_DIR}/healthcheck.sh"
assert_grep 'sign_health_request' "${DEPLOY_DIR}/healthcheck.sh"
for mapping in \
  'admin.*127\.0\.0\.1:11010/readyz' \
  'collector.*127\.0\.0\.1:11412/readyz' \
  'factor.*127\.0\.0\.1:11414/readyz' \
  'monitor.*127\.0\.0\.1:11409/readyz' \
  'cloudnode.*127\.0\.0\.1:11411/readyz' \
  'archive.*127\.0\.0\.1:11416/readyz' \
  'eventbus.*127\.0\.0\.1:11419/readyz' \
  'web-host.*127\.0\.0\.1:19527/readyz'; do
  assert_grep "${mapping}" "${DEPLOY_DIR}/healthcheck.sh"
done
assert_grep 'health probe failed' "${DEPLOY_DIR}/healthcheck.sh"
assert_grep '"\$\{ROOT\}/stop\.sh" "\$\{name\}"' "${DEPLOY_DIR}/healthcheck.sh"
assert_grep '"\$\{ROOT\}/start\.sh" "\$\{name\}" 9>&-' "${DEPLOY_DIR}/healthcheck.sh"

assert_grep 'start_storage_process "storage-primary" "moox-storage-primary"' "${DEPLOY_DIR}/start.sh"
assert_grep 'conf=config/trpc_go\.yaml' "${DEPLOY_DIR}/start.sh"
assert_grep 'start_storage_view' "${DEPLOY_DIR}/start.sh"
assert_grep 'start_service "storage-view" "\$\{ROOT\}/storage-view"' "${DEPLOY_DIR}/start.sh"
assert_grep '"\$\{ROOT\}/bin/moox-storage-view"' "${DEPLOY_DIR}/start.sh"
assert_grep 'conf=config/trpc_go\.yaml' "${DEPLOY_DIR}/start.sh"
if grep -Eq 'storage-view-(index|builder|query)|storage\.view_(index|builder|query)\.yaml' "${DEPLOY_DIR}/start.sh"; then
  echo "split View processes must not remain in start.sh" >&2
  exit 1
fi

assert_order "${DEPLOY_DIR}/start.sh" \
  '^  start_storage_primary$' \
  '^  start_storage_view$'
assert_order "${DEPLOY_DIR}/stop.sh" \
  'stop_service "storage-view"' \
  'stop_service "storage-primary"'

assert_file "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
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
assert_grep 'scheduler=viewBuilderSchedule&startAtOnce=1' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
if grep -Eq '[?&](params|disable|location)=' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"; then
  echo "view builder timer contains options unsupported by the official tRPC timer" >&2
  exit 1
fi
assert_grep 'timeout: 900000' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"

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

echo "storage-primary plus unified storage-view deployment package verified"
