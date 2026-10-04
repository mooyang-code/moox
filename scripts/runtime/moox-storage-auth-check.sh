#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: moox-storage-auth-check.sh [--control-root DIR] [--storage-root DIR] [--processes]

Checks that the control and storage packages use the same internal Storage
authentication file. With --processes, also checks running moox processes on
Linux through /proc without printing secret values.
EOF
}

CONTROL_ROOT="${MOOX_CONTROL_ROOT:-/data/moox/prod}"
STORAGE_ROOT="${MOOX_STORAGE_ROOT:-/data/moox/storage}"
CHECK_PROCESSES=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --control-root) [[ $# -ge 2 ]] || { usage; exit 2; }; CONTROL_ROOT="$2"; shift 2 ;;
    --storage-root) [[ $# -ge 2 ]] || { usage; exit 2; }; STORAGE_ROOT="$2"; shift 2 ;;
    --processes) CHECK_PROCESSES=1; shift ;;
    -h|--help) usage >&1; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage; exit 2 ;;
  esac
done

AUTH_BASENAME="secrets/storage-internal-auth.env"
control_file="${CONTROL_ROOT}/${AUTH_BASENAME}"
storage_file="${STORAGE_ROOT}/${AUTH_BASENAME}"

read_secret() {
  local file="$1" name="$2"
  bash -c 'set -u; source "$1"; printf "%s" "${!2-}"' _ "${file}" "${name}"
}

validate_secret() {
  local file="$1" name="$2" value
  value="$(read_secret "${file}" "${name}")"
  [[ -n "${value}" && "${value}" != *$'\n'* && "${value}" != *$'\r'* ]] || {
    echo "${file}: ${name} must contain exactly one non-empty value" >&2
    return 1
  }
  printf '%s' "${value}"
}

fingerprint() {
  if command -v shasum >/dev/null 2>&1; then
    printf '%s' "$1" | shasum -a 256 | awk '{print substr($1, 1, 12)}'
  else
    printf '%s' "$1" | sha256sum | awk '{print substr($1, 1, 12)}'
  fi
}

[[ -r "${control_file}" ]] || { echo "missing ${control_file}" >&2; exit 1; }
[[ -r "${storage_file}" ]] || { echo "missing ${storage_file}" >&2; exit 1; }

control_primary="$(validate_secret "${control_file}" MOOX_STORAGE_PRIMARY_AUTH_SECRET)"
control_view="$(validate_secret "${control_file}" MOOX_STORAGE_VIEW_AUTH_SECRET)"
storage_primary="$(validate_secret "${storage_file}" MOOX_STORAGE_PRIMARY_AUTH_SECRET)"
storage_view="$(validate_secret "${storage_file}" MOOX_STORAGE_VIEW_AUTH_SECRET)"

[[ "${control_primary}" == "${storage_primary}" ]] || {
  echo "Storage Primary auth mismatch: control=$(fingerprint "${control_primary}") storage=$(fingerprint "${storage_primary}")" >&2
  exit 1
}
[[ "${control_view}" == "${storage_view}" ]] || {
  echo "Storage View auth mismatch: control=$(fingerprint "${control_view}") storage=$(fingerprint "${storage_view}")" >&2
  exit 1
}

echo "Storage auth files match (primary=$(fingerprint "${control_primary}") view=$(fingerprint "${control_view}"))"

if [[ "${CHECK_PROCESSES}" == "1" ]]; then
  if [[ ! -d /proc ]]; then
    echo "process check skipped: /proc is unavailable" >&2
    exit 0
  fi
  process_failed=0
  for service in storage-primary storage-view monitor factor collector; do
    expected="/moox-${service}"
    for proc in /proc/[0-9]*; do
      [[ -d "${proc}" ]] || continue
      pid="${proc##*/}"
      exe="$(readlink "${proc}/exe" 2>/dev/null || true)"
      [[ "${exe}" == *"${expected}" || "${exe}" == *"${expected} (deleted)" ]] || continue
      env_file="${proc}/environ"
      [[ -r "${env_file}" ]] || continue
      process_primary="$(tr '\0' '\n' <"${env_file}" | awk -F= '$1 == "MOOX_STORAGE_PRIMARY_AUTH_SECRET" {print substr($0, index($0, "=") + 1); exit}')"
      if [[ -z "${process_primary}" || "${process_primary}" != "${control_primary}" ]]; then
        echo "${service} pid=${pid} has a stale/missing Storage Primary auth secret" >&2
        process_failed=1
      fi
      if [[ "${service}" == "storage-primary" || "${service}" == "storage-view" ]]; then
        process_view="$(tr '\0' '\n' <"${env_file}" | awk -F= '$1 == "MOOX_STORAGE_VIEW_AUTH_SECRET" {print substr($0, index($0, "=") + 1); exit}')"
        if [[ -z "${process_view}" || "${process_view}" != "${control_view}" ]]; then
          echo "${service} pid=${pid} has a stale/missing Storage View auth secret" >&2
          process_failed=1
        fi
      fi
    done
  done
  [[ "${process_failed}" == "0" ]] || exit 1
  echo "running Storage clients use the same auth fingerprints"
fi
