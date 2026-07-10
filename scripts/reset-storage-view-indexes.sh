#!/usr/bin/env bash
set -euo pipefail

ROOT=""
INDEX_ROOT=""
DELETE_METADATA=0
CONFIRMED=0
ALL_STORAGE_DATA=0

usage() {
  cat <<'EOF'
Usage:
  scripts/reset-storage-view-indexes.sh --root <storage-root> --yes [--metadata] [--index-root <path>]
  scripts/reset-storage-view-indexes.sh --root <deploy-root> --all-storage-data --yes

Deletes disposable View-derived state only. PrimaryStore/Pebble is never removed.

Options:
  --root <path>        Resolved storage root. Required.
  --index-root <path>  View index root. Default: <root>/view-indexes.
  --metadata           Also remove the SQLite metadata database for a full new-project reset.
  --yes                Confirm destructive deletion. Required.
  --all-storage-data   Remove exactly <deploy-root>/data/storage after proving managed Storage is stopped.
  -h, --help           Show this help.
EOF
}

fail() {
  printf '[reset-storage-view-indexes] ERROR: %s\n' "$*" >&2
  exit 1
}

resolve_path() {
  local value="$1"
  local parent base
  if [[ "${value}" != /* ]]; then
    value="${PWD}/${value}"
  fi
	if [[ -d "${value}" ]]; then
		(cd "${value}" && pwd -P)
		return
	fi
  parent="$(dirname "${value}")"
  base="$(basename "${value}")"
  [[ -d "${parent}" ]] || fail "parent directory does not exist: ${parent}"
  printf '%s/%s\n' "$(cd "${parent}" && pwd -P)" "${base}"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --root)
      ROOT="${2:-}"
      shift 2
      ;;
    --index-root)
      INDEX_ROOT="${2:-}"
      shift 2
      ;;
    --metadata)
      DELETE_METADATA=1
      shift
      ;;
    --yes)
      CONFIRMED=1
      shift
      ;;
    --all-storage-data)
      ALL_STORAGE_DATA=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1"
      ;;
  esac
done

[[ -n "${ROOT}" ]] || fail "--root is required"
[[ "${CONFIRMED}" -eq 1 ]] || fail "refusing destructive reset without --yes"

ROOT="$(resolve_path "${ROOT}")"
[[ "${ROOT}" != "/" && -n "${ROOT}" ]] || fail "refusing unsafe storage root: ${ROOT}"
[[ -d "${ROOT}" ]] || fail "storage root must be an existing directory: ${ROOT}"

if [[ "${ALL_STORAGE_DATA}" -eq 1 ]]; then
  [[ -z "${INDEX_ROOT}" ]] || fail "--index-root cannot be combined with --all-storage-data"
  STORAGE_DATA="${ROOT}/data/storage"
  [[ -d "${ROOT}/data" ]] || fail "deploy root has no data directory: ${ROOT}/data"
  if [[ -e "${STORAGE_DATA}" ]]; then
    STORAGE_DATA="$(resolve_path "${STORAGE_DATA}")"
  else
    STORAGE_DATA="$(cd "${ROOT}/data" && pwd -P)/storage"
  fi
  [[ "${STORAGE_DATA}" == "${ROOT}/data/storage" ]] || fail "storage data path escaped deploy root: ${STORAGE_DATA}"
  [[ "${STORAGE_DATA}" != "/" && -n "${STORAGE_DATA}" ]] || fail "refusing unsafe storage data path"
  for pid_file in "${ROOT}"/storage/*.pid "${ROOT}"/*.pid; do
    [[ -f "${pid_file}" ]] || continue
    pid="$(cat "${pid_file}" 2>/dev/null || true)"
    if [[ -n "${pid}" ]] && ps -p "${pid}" >/dev/null 2>&1; then
      fail "managed Storage process is still running: pid ${pid} (${pid_file})"
    fi
  done
  while IFS= read -r pid; do
    [[ -n "${pid}" ]] || continue
    fail "managed Storage process is still running: pid ${pid}"
  done < <(pgrep -f "${ROOT}/.*/bin/moox-storage|${ROOT}/bin/moox-storage" 2>/dev/null || true)
  printf '[reset-storage-view-indexes] deploy root: %s\n' "${ROOT}"
  printf '[reset-storage-view-indexes] deleting exact storage data path: %s\n' "${STORAGE_DATA}"
  rm -rf -- "${STORAGE_DATA}"
  mkdir -p -- "${STORAGE_DATA}"
  chmod 0755 "${STORAGE_DATA}"
  printf '[reset-storage-view-indexes] full Storage data reset complete\n'
  exit 0
fi

if [[ -z "${INDEX_ROOT}" ]]; then
  INDEX_ROOT="${ROOT}/view-indexes"
else
  INDEX_ROOT="$(resolve_path "${INDEX_ROOT}")"
fi

case "${INDEX_ROOT}" in
  "${ROOT}"/*) ;;
  *) fail "index root must be inside storage root: ${INDEX_ROOT}" ;;
esac

targets=("${ROOT}/bleve" "${INDEX_ROOT}")
for path in "${ROOT}"/duckdb/views.duckdb*; do
	[[ -e "${path}" ]] || continue
	targets+=("${path}")
done
if [[ "${DELETE_METADATA}" -eq 1 ]]; then
  targets+=(
    "${ROOT}/metadata/storage_metadata.db"
    "${ROOT}/metadata/storage_metadata.db-shm"
    "${ROOT}/metadata/storage_metadata.db-wal"
  )
fi

printf '[reset-storage-view-indexes] storage root: %s\n' "${ROOT}"
printf '[reset-storage-view-indexes] paths selected for deletion:\n'
for path in "${targets[@]}"; do
  case "${path}" in
    "${ROOT}"/*) printf '  %s\n' "${path}" ;;
    *) fail "refusing path outside storage root: ${path}" ;;
  esac
done

for path in "${targets[@]}"; do
  rm -rf -- "${path}"
done

printf '[reset-storage-view-indexes] reset complete; PrimaryStore was preserved\n'
