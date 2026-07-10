#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-deploy-storage-split.XXXXXX")"
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
ensure_required_binary moox-storage
ensure_required_binary moox-storage-cli

"${ROOT}/scripts/deploy-moox.sh" \
  --target localhost \
  --dir "${DEPLOY_DIR}" \
  --stage "${STAGE_DIR}" \
  --skip-build \
  --no-start \
  --no-web-host \
  --no-cloudnode \
  --no-collector \
  --no-factor \
  --no-monitor >/dev/null

for binary in moox-storage-access moox-storage-view-index moox-storage-view-builder moox-storage-view-query; do
  assert_file "${DEPLOY_DIR}/bin/${binary}"
done

assert_grep 'start_storage_process "storage-access" "moox-storage-access"' "${DEPLOY_DIR}/start.sh"
assert_grep 'storage\.access\.yaml' "${DEPLOY_DIR}/start.sh"
assert_grep 'start_storage_process "storage-view-index" "moox-storage-view-index"' "${DEPLOY_DIR}/start.sh"
assert_grep 'storage\.view_index\.yaml' "${DEPLOY_DIR}/start.sh"
assert_grep 'start_storage_process "storage-view-builder" "moox-storage-view-builder"' "${DEPLOY_DIR}/start.sh"
assert_grep 'storage\.view_builder\.yaml' "${DEPLOY_DIR}/start.sh"
assert_grep 'start_storage_process "storage-view-query" "moox-storage-view-query"' "${DEPLOY_DIR}/start.sh"
assert_grep 'storage\.view_query\.yaml' "${DEPLOY_DIR}/start.sh"

assert_order "${DEPLOY_DIR}/start.sh" \
  '^  start_storage_access$' \
  '^  start_storage_view_index$' \
  '^  start_storage_view_builder$' \
  '^  start_storage_view_query$'
assert_order "${DEPLOY_DIR}/stop.sh" \
  'stop_service "storage-view-query"' \
  'stop_service "storage-view-builder"' \
  'stop_service "storage-view-index"' \
  'stop_service "storage-access"'

for conf in storage.access.yaml storage.view_index.yaml storage.view_builder.yaml storage.view_query.yaml; do
  assert_grep 'root: \.\./data/storage' "${DEPLOY_DIR}/storage/config/${conf}"
  assert_grep 'path: \.\./data/storage/metadata/storage_metadata\.db' "${DEPLOY_DIR}/storage/config/${conf}"
  assert_grep 'pebble_path: \.\./data/storage/pebble' "${DEPLOY_DIR}/storage/config/${conf}"
done

assert_grep 'view_index_root: \.\./data/storage/view-indexes' "${DEPLOY_DIR}/storage/config/storage.view_index.yaml"
if grep -Eq 'view_index_root:' "${DEPLOY_DIR}/storage/config/storage.view_builder.yaml" "${DEPLOY_DIR}/storage/config/storage.view_query.yaml"; then
  echo "builder/query configs must not own view_index_root" >&2
  exit 1
fi
if grep -ERq 'duckdb_path:|bleve_path:|rotation:|op=rotate' "${DEPLOY_DIR}/storage/config"; then
  echo "legacy View index configuration remains in deployment package" >&2
  exit 1
fi

assert_grep 'enabled: true' "${DEPLOY_DIR}/storage/config/storage.access.yaml"
assert_grep 'log_path: \.\./logs/storage-access' "${DEPLOY_DIR}/storage/config/trpc_go.access.yaml"
assert_grep 'log_path: \.\./logs/storage-view-index' "${DEPLOY_DIR}/storage/config/trpc_go.view_index.yaml"
assert_grep 'log_path: \.\./logs/storage-view-builder' "${DEPLOY_DIR}/storage/config/trpc_go.view_builder.yaml"
assert_grep 'log_path: \.\./logs/storage-view-query' "${DEPLOY_DIR}/storage/config/trpc_go.view_query.yaml"
assert_grep 'port: 20104' "${DEPLOY_DIR}/storage/config/trpc_go.view_index.yaml"
assert_grep 'op=maintain' "${DEPLOY_DIR}/storage/config/trpc_go.view_builder.yaml"
assert_grep 'timeout: 900000' "${DEPLOY_DIR}/storage/config/trpc_go.view_builder.yaml"

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

echo "storage four-process deployment package verified"
