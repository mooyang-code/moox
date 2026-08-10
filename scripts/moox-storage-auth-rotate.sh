#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: moox-storage-auth-rotate.sh [--control-root DIR] [--storage-root DIR]
                                  [--primary SECRET] [--view SECRET] [--no-restart]

Updates the control and storage packages together. The files are replaced via
same-directory rename, old copies are kept outside secrets, and managed
Storage/Monitor/Factor/Collector processes are restarted unless --no-restart
is supplied.
EOF
}

CONTROL_ROOT="${MOOX_CONTROL_ROOT:-${HOME}/moox/prod}"
STORAGE_ROOT="${MOOX_STORAGE_ROOT:-${HOME}/moox/storage}"
BACKUP_ROOT="${MOOX_STORAGE_AUTH_BACKUP_DIR:-${HOME}/moox/backup/storage-auth}"
PRIMARY_SECRET="${MOOX_STORAGE_PRIMARY_AUTH_SECRET:-}"
VIEW_SECRET="${MOOX_STORAGE_VIEW_AUTH_SECRET:-}"
NO_RESTART=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --control-root) [[ $# -ge 2 ]] || { usage; exit 2; }; CONTROL_ROOT="$2"; shift 2 ;;
    --storage-root) [[ $# -ge 2 ]] || { usage; exit 2; }; STORAGE_ROOT="$2"; shift 2 ;;
    --primary) [[ $# -ge 2 ]] || { usage; exit 2; }; PRIMARY_SECRET="$2"; shift 2 ;;
    --view) [[ $# -ge 2 ]] || { usage; exit 2; }; VIEW_SECRET="$2"; shift 2 ;;
    --no-restart) NO_RESTART=1; shift ;;
    -h|--help) usage >&1; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage; exit 2 ;;
  esac
done

generate_secret() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 32
  else
    od -An -N32 -tx1 /dev/urandom | tr -d ' \n'
  fi
}
validate_secret() {
  local name="$1" value="$2"
  if [[ -z "${value}" || "${value}" == *$'\n'* || "${value}" == *$'\r'* ]]; then
    echo "${name} must contain exactly one non-empty value" >&2
    exit 2
  fi
}
[[ -n "${PRIMARY_SECRET}" ]] || PRIMARY_SECRET="$(generate_secret)"
[[ -n "${VIEW_SECRET}" ]] || VIEW_SECRET="$(generate_secret)"
validate_secret MOOX_STORAGE_PRIMARY_AUTH_SECRET "${PRIMARY_SECRET}"
validate_secret MOOX_STORAGE_VIEW_AUTH_SECRET "${VIEW_SECRET}"

[[ -d "${CONTROL_ROOT}" ]] || { echo "missing control root: ${CONTROL_ROOT}" >&2; exit 1; }
[[ -d "${STORAGE_ROOT}" ]] || { echo "missing storage root: ${STORAGE_ROOT}" >&2; exit 1; }
mkdir -p "${BACKUP_ROOT}"
chmod 0700 "${BACKUP_ROOT}"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)-$$"

write_auth_file() {
  local file="$1" label="$2" temporary
  mkdir -p "$(dirname "${file}")"
  if [[ -f "${file}" ]]; then
    cp -p "${file}" "${BACKUP_ROOT}/storage-internal-auth-${timestamp}-${label}.env"
    chmod 0600 "${BACKUP_ROOT}/"storage-internal-auth-${timestamp}-*.env 2>/dev/null || true
  fi
  temporary="$(mktemp "${file}.next.XXXXXX")"
  trap 'rm -f "${temporary}"' RETURN
  (umask 077; {
    printf 'MOOX_STORAGE_PRIMARY_AUTH_SECRET=%q\n' "${PRIMARY_SECRET}"
    printf 'MOOX_STORAGE_VIEW_AUTH_SECRET=%q\n' "${VIEW_SECRET}"
  } >"${temporary}")
  chmod 0600 "${temporary}"
  mv -f "${temporary}" "${file}"
  trap - RETURN
}

write_auth_file "${CONTROL_ROOT}/secrets/storage-internal-auth.env" control
if [[ "${STORAGE_ROOT}" != "${CONTROL_ROOT}" ]]; then
  write_auth_file "${STORAGE_ROOT}/secrets/storage-internal-auth.env" storage
fi

restart_managed() {
  local root="$1" service="$2"
  [[ "${NO_RESTART}" == "0" ]] || return 0
  [[ -x "${root}/restart.sh" && -f "${root}/run/${service}.pid" ]] || return 0
  echo "restarting ${service} (${root})"
  "${root}/restart.sh" "${service}"
}

# Storage must reload the new verifier before clients are restarted. Some
# packages expose a single `storage` service while the normal package exposes
# separate role pids, so restart whichever shape is actually installed.
if [[ -f "${STORAGE_ROOT}/run/storage.pid" ]]; then
  restart_managed "${STORAGE_ROOT}" storage
else
  restart_managed "${STORAGE_ROOT}" storage-primary
  restart_managed "${STORAGE_ROOT}" storage-view
  restart_managed "${STORAGE_ROOT}" storage-node
fi
for service in monitor factor collector; do
  restart_managed "${CONTROL_ROOT}" "${service}"
done

if [[ "${NO_RESTART}" == "0" ]]; then
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
  checker="${script_dir}/moox-storage-auth-check.sh"
  [[ -x "${checker}" ]] || checker="${script_dir}/moox-storage-auth-check"
  if [[ -x "${checker}" ]]; then
    "${checker}" --control-root "${CONTROL_ROOT}" --storage-root "${STORAGE_ROOT}" --processes
  fi
fi

echo "Storage auth rotated; control and storage fingerprints are synchronized"
